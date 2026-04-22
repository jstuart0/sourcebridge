"""CLI entry point for code discussion — invoked by Go CLI."""

from __future__ import annotations

import asyncio
import contextlib
import json
import os
import re
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any

# Ensure workers package is importable when run from workers/ dir
_here = os.path.dirname(os.path.abspath(__file__))
_parent = os.path.dirname(_here)
if _parent not in sys.path:
    sys.path.insert(0, _parent)

from workers.common.config import WorkerConfig  # noqa: E402
from workers.common.llm.config import create_llm_provider  # noqa: E402
from workers.common.surreal import SurrealClient  # noqa: E402
from workers.reasoning.discussion import discuss_code  # noqa: E402

SUPPORTED_EXTENSIONS = {".go", ".py", ".ts", ".js", ".java", ".rs"}
MAX_FILES = 8
MAX_SNIPPET_LINES = 80
MAX_FILE_BYTES = 32_000
STOPWORDS = {
    "a",
    "an",
    "are",
    "can",
    "generated",
    "generate",
    "how",
    "in",
    "is",
    "it",
    "of",
    "on",
    "or",
    "refresh",
    "refreshed",
    "the",
    "to",
    "via",
    "which",
    "who",
    "why",
    "and",
    "for",
    "with",
    "this",
    "that",
    "what",
    "does",
    "into",
    "from",
    "repo",
    "repository",
    "flow",
    "code",
    "doesn",
    "doesnt",
    "your",
    "about",
    "when",
}


@dataclass
class FileEvidence:
    path: Path
    score: int
    snippet: str
    reference: str
    reason: str


@dataclass
class DeepUnderstandingContext:
    repo_id: str
    repo_name: str
    repo_lookup: str
    corpus_id: str
    understanding_stage: str
    tree_status: str
    revision_fp: str
    model_used: str


@dataclass
class SummaryEvidence:
    unit_id: str
    level: int
    headline: str
    summary_text: str
    metadata: dict[str, Any]
    score: int
    reason: str


def _is_useful_summary_text(text: str) -> bool:
    stripped = text.strip()
    if not stripped:
        return False
    lowered = stripped.lower()
    return not (
        lowered.startswith("could not summarize")
        or lowered == "n/a"
        or lowered == "unknown"
    )


def _understanding_ready(understanding: DeepUnderstandingContext) -> bool:
    return (
        understanding.understanding_stage.lower() == "ready"
        and understanding.tree_status.lower() == "complete"
    )


def _tokenize_question(question: str) -> list[str]:
    tokens = [t.lower() for t in re.findall(r"[a-zA-Z0-9_-]+", question)]
    return [t for t in tokens if len(t) >= 3 and t not in STOPWORDS]


def _normalize_query_result(raw: list[Any]) -> list[dict[str, Any]]:
    if not raw:
        return []
    if isinstance(raw[0], dict) and "result" in raw[0]:
        result = raw[0].get("result")
        if isinstance(result, list):
            return [row for row in result if isinstance(row, dict)]
        return []
    return [row for row in raw if isinstance(row, dict)]


def _sql_string(value: str) -> str:
    return json.dumps(value)


def _normalize_record_id(value: str) -> str:
    if ":" in value:
        value = value.split(":", 1)[1]
    return value.strip().strip("⟨⟩")


def _question_type(question: str) -> str:
    q = question.lower()
    if any(term in q for term in ("architecture", "high level", "1000 foot", "subsystem")):
        return "architecture"
    if any(term in q for term in ("flow", "path", "request", "how does", "what happens when")):
        return "execution_flow"
    if "requirement" in q or "req-" in q:
        return "requirement_coverage"
    if any(term in q for term in ("where is", "which file", "where does")):
        return "ownership"
    if any(term in q for term in ("schema", "model", "table", "entity", "data")):
        return "data_model"
    if any(term in q for term in ("risk", "bug", "review", "unsafe", "vulnerability")):
        return "risk_review"
    return "behavior"


def _extract_requirement_lines(repo_path: Path) -> list[str]:
    readme = repo_path / "README.md"
    if not readme.exists():
        return []
    try:
        lines = readme.read_text(encoding="utf-8").splitlines()
    except Exception:
        return []
    return [line.strip() for line in lines if re.search(r"\bREQ-[A-Z0-9-]+\b", line)]


def _select_relevant_requirements(
    requirement_lines: list[str],
    evidences: list[FileEvidence],
    question: str,
) -> list[str]:
    if not requirement_lines:
        return []

    explicit_ids = {
        match.group(0)
        for match in re.finditer(r"\bREQ-[A-Z0-9-]+\b", question.upper())
    }
    snippet_ids = {
        match.group(0)
        for evidence in evidences
        for match in re.finditer(r"\bREQ-[A-Z0-9-]+\b", evidence.snippet.upper())
    }

    selected_ids = explicit_ids | snippet_ids
    if not selected_ids and "requirement" not in question.lower():
        return []

    selected_lines = [
        line
        for line in requirement_lines
        if any(req_id in line for req_id in selected_ids)
    ]

    if selected_lines:
        return selected_lines[:8]

    if "requirement" in question.lower():
        question_tokens = _tokenize_question(question)
        ranked: list[tuple[int, str]] = []
        for line in requirement_lines:
            line_lower = line.lower()
            score = sum(1 for token in question_tokens if token in line_lower)
            if score > 0:
                ranked.append((score, line))
        ranked.sort(key=lambda item: (-item[0], item[1]))
        return [line for _, line in ranked[:8]]

    return []


def _score_file(path: Path, question_tokens: list[str]) -> tuple[int, str]:
    path_text = str(path).lower()
    score = 0
    reasons: list[str] = []
    for token in question_tokens:
        if token in path_text:
            score += 8
            reasons.append(f"path:{token}")
    for special, patterns in {
        "auth": ("auth", "session", "jwt", "magic-link", "signin", "signup"),
        "billing": ("billing", "stripe", "payment"),
        "team": ("team", "invitation", "member"),
    }.items():
        if special in question_tokens and any(pattern in path_text for pattern in patterns):
            score += 12
            reasons.append(f"domain:{special}")
    if "routes" in path.parts:
        score += 2
    if "services" in path.parts:
        score += 3
    if path.name.lower() == "readme.md":
        score += 1
    return score, ", ".join(reasons)


def _deep_path_boosts(path: Path, question: str, question_type: str) -> tuple[int, list[str]]:
    path_text = str(path).lower()
    name = path.name.lower()
    score = 0
    reasons: list[str] = []

    if any(marker in path_text for marker in ("/test", "_test.", ".test.", "tests/")):
        score -= 6
        reasons.append("penalty:test")
    if any(marker in path_text for marker in ("examples/", "benchmark", "docs/")):
        score -= 4
        reasons.append("penalty:non-product")

    q = question.lower()
    if question_type == "architecture":
        if "architecture" in path_text:
            score += 12
            reasons.append("plan:architecture")
        if "diagram" in path_text:
            score += 10
            reasons.append("plan:diagram")
        if "architecture diagram" in q or ("architecture" in q and "diagram" in q):
            if any(marker in path_text for marker in (
                "web/src/components/architecture/architecturediagram.tsx",
                "workers/knowledge/architecture_diagram.py",
                "internal/api/graphql/knowledge_support.go",
                "internal/api/graphql/schema.resolvers.go",
                "internal/architecture/diagram.go",
                "web/src/lib/graphql/queries.ts",
            )):
                score += 24
                reasons.append("plan:architecture-diagram")
        if "workers/knowledge/prompts/architecture_diagram.py" in path_text:
            score -= 8
            reasons.append("penalty:prompt-template")
        if any(marker in q for marker in ("refresh", "regenerate", "generated")) and any(
            marker in path_text for marker in ("knowledge_support.go", "schema.resolvers.go", "architecturediagram.tsx", "queries.ts")
        ):
            score += 14
            reasons.append("plan:refresh")

    if question_type == "execution_flow":
        if any(marker in path_text for marker in ("routes/", "handler", "service", "worker", "job")):
            score += 8
            reasons.append("plan:flow")

    if question_type == "behavior":
        if any(marker in path_text for marker in ("routes/", "service", "auth", "session", "store")):
            score += 6
            reasons.append("plan:behavior")

    if name == "schema.resolvers.go":
        score += 3
        reasons.append("plan:resolver")
    if name == "knowledge_support.go":
        score += 3
        reasons.append("plan:graphql-support")
    if name == "architecturediagram.tsx":
        score += 6
        reasons.append("plan:ui")
    if name == "queries.ts":
        score += 5
        reasons.append("plan:graphql-query")
    return score, reasons


def _plan_hint_files(repo_path: Path, question: str, question_type: str) -> list[FileEvidence]:
    q = question.lower()
    if question_type == "architecture" and ("architecture diagram" in q or ("architecture" in q and "diagram" in q)):
        preferred = [
            "internal/architecture/diagram.go",
            "workers/knowledge/architecture_diagram.py",
            "internal/api/graphql/knowledge_support.go",
            "internal/api/graphql/schema.resolvers.go",
            "web/src/components/architecture/ArchitectureDiagram.tsx",
        ]
        hinted: list[FileEvidence] = []
        question_tokens = _tokenize_question(question)
        for rel_text in preferred:
            path = repo_path / rel_text
            if not path.exists() or not path.is_file():
                continue
            rel = path.relative_to(repo_path)
            boost_score, boost_reasons = _deep_path_boosts(rel, question, question_type)
            try:
                snippet, reference = _best_snippet(path, question_tokens, repo_path)
            except Exception:
                continue
            hinted.append(
                FileEvidence(
                    path=rel,
                    score=max(boost_score, 20),
                    snippet=snippet,
                    reference=reference,
                    reason=";".join(boost_reasons) or "plan-hint",
                )
            )
        return hinted

    planned_hints: list[FileEvidence] = []
    question_tokens = _tokenize_question(question)
    for path in repo_path.rglob("*"):
        if not path.is_file():
            continue
        if path.suffix.lower() not in SUPPORTED_EXTENSIONS:
            continue
        if any(part in {"node_modules", ".git", "dist", "__pycache__"} for part in path.parts):
            continue
        rel = path.relative_to(repo_path)
        boost_score, boost_reasons = _deep_path_boosts(rel, question, question_type)
        if boost_score < 10:
            continue
        try:
            snippet, reference = _best_snippet(path, question_tokens, repo_path)
        except Exception:
            continue
        planned_hints.append(
            FileEvidence(
                path=rel,
                score=boost_score,
                snippet=snippet,
                reference=reference,
                reason=";".join(boost_reasons) or "plan-hint",
            )
        )
    planned_hints.sort(key=lambda item: (-item.score, str(item.path)))
    return planned_hints[:MAX_FILES]


def _best_snippet(path: Path, question_tokens: list[str], repo_path: Path) -> tuple[str, str]:
    text = path.read_text(encoding="utf-8")
    lines = text.splitlines()
    if not lines:
        rel = path.relative_to(repo_path)
        return "", f"{rel}:1-1"

    best_start = 0
    best_score = -1
    window = min(MAX_SNIPPET_LINES, max(30, len(lines)))
    for idx, line in enumerate(lines):
        line_text = line.lower()
        score = 0
        for token in question_tokens:
            if token in line_text:
                score += 6
        if re.search(r"\bexport async function\b|\bexport function\b|\basync function\b|\bfunction\b", line):
            score += 2
        if "auth" in question_tokens and any(marker in line_text for marker in ("signin", "signup", "magic", "session", "token", "jwt", "password")):
            score += 5
        if score > best_score:
            best_score = score
            best_start = idx

    start = max(0, best_start - 4)
    end = min(len(lines), start + window)
    snippet = "\n".join(lines[start:end])
    rel = path.relative_to(repo_path)
    return snippet, f"{rel}:{start + 1}-{end}"


def _collect_file_evidence(repo_path: Path, question: str) -> list[FileEvidence]:
    question_tokens = _tokenize_question(question)
    candidates: list[tuple[int, str, Path]] = []

    for path in repo_path.rglob("*"):
        if not path.is_file():
            continue
        if path.suffix.lower() not in SUPPORTED_EXTENSIONS:
            continue
        if any(part in {"node_modules", ".git", "dist", "__pycache__"} for part in path.parts):
            continue
        try:
            if path.stat().st_size > MAX_FILE_BYTES:
                continue
        except Exception:
            continue
        score, reason = _score_file(path.relative_to(repo_path), question_tokens)
        if score > 0:
            candidates.append((score, reason, path))

    candidates.sort(key=lambda item: (-item[0], str(item[2])))
    if not candidates:
        for path in sorted(repo_path.rglob("*")):
            if path.is_file() and path.suffix.lower() in SUPPORTED_EXTENSIONS:
                candidates.append((1, "fallback", path))
            if len(candidates) >= MAX_FILES:
                break

    evidences: list[FileEvidence] = []
    for score, reason, path in candidates[:MAX_FILES]:
        try:
            snippet, reference = _best_snippet(path, question_tokens, repo_path)
        except Exception:
            continue
        evidences.append(
            FileEvidence(
                path=path.relative_to(repo_path),
                score=score,
                snippet=snippet,
                reference=reference,
                reason=reason or "ranked",
            )
        )
    return evidences


def _build_context(repo_path: Path, question: str) -> tuple[str, str]:
    evidences = _collect_file_evidence(repo_path, question)
    requirements = _select_relevant_requirements(
        _extract_requirement_lines(repo_path),
        evidences,
        question,
    )

    metadata_parts = [
        f"Repository: {repo_path.name}",
        f"Question focus tokens: {', '.join(_tokenize_question(question)) or '(none)'}",
    ]
    if requirements:
        metadata_parts.append("Relevant requirements from README:")
        metadata_parts.extend(f"- {line}" for line in requirements[:8])

    metadata_parts.append("Selected evidence files:")
    metadata_parts.extend(
        f"- {e.path} ({e.reason}, score={e.score}, ref={e.reference})"
        for e in evidences
    )

    code_parts = []
    for evidence in evidences:
        code_parts.append(f"--- {evidence.reference} ({evidence.reason}) ---\n{evidence.snippet}")

    return "\n".join(metadata_parts), "\n\n".join(code_parts) if code_parts else "No code evidence found."


async def _load_deep_understanding(repo_path: Path, config: WorkerConfig) -> DeepUnderstandingContext | None:
    client = SurrealClient(
        url=config.surreal_url,
        namespace=config.surreal_namespace,
        database=config.surreal_database,
        user=config.surreal_user,
        password=config.surreal_pass,
    )
    await client.connect()
    try:
        repo_rows = _normalize_query_result(
            await client.query(
                "SELECT id, name, path, clone_path FROM ca_repository "
                f"WHERE path = {_sql_string(str(repo_path))} LIMIT 1;"
            )
        )
        lookup_mode = "path"
        if not repo_rows:
            repo_name = repo_path.name
            name_rows = _normalize_query_result(
                await client.query(
                    "SELECT id, name, path, clone_path FROM ca_repository "
                    f"WHERE name = {_sql_string(repo_name)};"
                )
            )
            if len(name_rows) == 1:
                repo_rows = name_rows
                lookup_mode = "name"
        if not repo_rows:
            return None
        repo_row = repo_rows[0]
        repo_id = _normalize_record_id(str(repo_row.get("id") or ""))
        understanding_rows = _normalize_query_result(
            await client.query(
                "SELECT * FROM ca_repository_understanding "
                f"WHERE repo_id = {_sql_string(repo_id)} AND scope_key = 'repository:' "
                "AND stage = 'ready' AND tree_status = 'complete' "
                "ORDER BY updated_at DESC LIMIT 1;"
            )
        )
        if not understanding_rows:
            understanding_rows = _normalize_query_result(
                await client.query(
                    "SELECT * FROM ca_repository_understanding "
                    f"WHERE repo_id = {_sql_string(repo_id)} AND scope_key = 'repository:' "
                    "ORDER BY updated_at DESC LIMIT 1;"
                )
            )
        if not understanding_rows:
            return None
        row = understanding_rows[0]
        return DeepUnderstandingContext(
            repo_id=repo_id,
            repo_name=str(repo_row.get("name") or repo_path.name),
            repo_lookup=lookup_mode,
            corpus_id=str(row.get("corpus_id") or ""),
            understanding_stage=str(row.get("stage") or ""),
            tree_status=str(row.get("tree_status") or ""),
            revision_fp=str(row.get("revision_fp") or ""),
            model_used=str(row.get("model_used") or ""),
        )
    finally:
        await client.close()


async def _load_summary_evidence(
    corpus_id: str,
    question: str,
    config: WorkerConfig,
) -> list[SummaryEvidence]:
    if not corpus_id:
        return []
    client = SurrealClient(
        url=config.surreal_url,
        namespace=config.surreal_namespace,
        database=config.surreal_database,
        user=config.surreal_user,
        password=config.surreal_pass,
    )
    await client.connect()
    try:
        rows = _normalize_query_result(
            await client.query(
                "SELECT * FROM ca_summary_node "
                f"WHERE corpus_id = {_sql_string(corpus_id)} ORDER BY level DESC, unit_id;"
            )
        )
    finally:
        await client.close()

    if not rows:
        return []

    tokens = _tokenize_question(question)
    question_kind = _question_type(question)
    selected: list[SummaryEvidence] = []
    for row in rows:
        unit_id = str(row.get("unit_id") or "")
        headline = str(row.get("headline") or "")
        summary_text = str(row.get("summary_text") or "")
        try:
            metadata = json.loads(row.get("metadata") or "{}") if isinstance(row.get("metadata"), str) else dict(row.get("metadata") or {})
        except Exception:
            metadata = {}
        haystack = "\n".join([unit_id, headline, summary_text, json.dumps(metadata, sort_keys=True)]).lower()
        score = 0
        reasons: list[str] = []
        for token in tokens:
            if token in haystack:
                score += 8
                reasons.append(f"match:{token}")
        level = int(row.get("level") or 0)
        if level == 1:
            score += 5
            reasons.append("file-level")
        elif level == 0:
            score += 1
        else:
            score += min(level, 3)
        file_path = str(metadata.get("file_path") or "")
        if question_kind in {"execution_flow", "behavior"} and any(marker in haystack for marker in ("route", "handler", "service", "session", "token", "auth")):
            score += 4
            reasons.append("flow-signal")
        if question_kind == "architecture" and level > 0:
            score += 5
            reasons.append("architecture-level")
        if file_path:
            score += 1
        if _is_useful_summary_text(summary_text) or _is_useful_summary_text(headline):
            score += 3
            reasons.append("useful-summary")
        else:
            continue
        if score <= 0:
            continue
        selected.append(
            SummaryEvidence(
                unit_id=unit_id,
                level=level,
                headline=headline,
                summary_text=summary_text,
                metadata=metadata,
                score=score,
                reason=", ".join(reasons) or "ranked",
            )
        )

    selected.sort(key=lambda item: (-item.score, -item.level, item.unit_id))
    return selected


def _best_deep_files(
    repo_path: Path,
    question: str,
    summary_evidence: list[SummaryEvidence],
) -> list[FileEvidence]:
    question_tokens = _tokenize_question(question)
    question_kind = _question_type(question)
    merged: dict[str, FileEvidence] = {}

    for item in summary_evidence:
        file_path = str(item.metadata.get("file_path") or "").strip()
        if not file_path:
            continue
        abs_path = repo_path / file_path
        if not abs_path.exists() or not abs_path.is_file():
            continue
        try:
            snippet, reference = _best_snippet(abs_path, question_tokens, repo_path)
        except Exception:
            continue
        merged[file_path] = FileEvidence(
            path=Path(file_path),
            score=max(item.score, merged.get(file_path, FileEvidence(Path(file_path), 0, "", "", "")).score),
            snippet=snippet,
            reference=reference,
            reason=f"understanding:{item.reason}",
        )

    for hint in _plan_hint_files(repo_path, question, question_kind):
        merged[str(hint.path)] = hint

    for evidence in _collect_file_evidence(repo_path, question):
        key = str(evidence.path)
        boost_score, boost_reasons = _deep_path_boosts(evidence.path, question, question_kind)
        evidence.score += boost_score
        if boost_reasons:
            evidence.reason = ";".join(filter(None, [evidence.reason, *boost_reasons]))
        if key in merged:
            existing = merged[key]
            if evidence.score > existing.score:
                merged[key] = FileEvidence(
                    path=evidence.path,
                    score=evidence.score,
                    snippet=evidence.snippet,
                    reference=evidence.reference,
                    reason=f"{existing.reason};heuristic:{evidence.reason}",
                )
            else:
                existing.reason = f"{existing.reason};heuristic:{evidence.reason}"
        else:
            merged[key] = evidence

    ranked = sorted(merged.values(), key=lambda item: (-item.score, str(item.path)))
    non_tests = [item for item in ranked if all(marker not in str(item.path).lower() for marker in ("/test", "_test.", ".test.", "tests/"))]
    if non_tests:
        ranked = non_tests + [item for item in ranked if item not in non_tests]
    limit = 4 if question_kind == "architecture" else MAX_FILES
    return ranked[:limit]


async def _build_deep_context(
    repo_path: Path,
    question: str,
    config: WorkerConfig,
) -> tuple[str, str, dict[str, Any]]:
    understanding = await _load_deep_understanding(repo_path, config)
    if understanding is None:
        return (
            "",
            "",
            {
                "fallback_used": "blocked_for_understanding",
                "understanding_used": False,
                "graph_expansion_used": False,
                "files_considered": 0,
                "files_used": 0,
                "question_type": _question_type(question),
            },
        )
    if not _understanding_ready(understanding):
        return (
            "",
            "",
            {
                "fallback_used": "blocked_for_understanding",
                "understanding_used": False,
                "graph_expansion_used": False,
                "files_considered": 0,
                "files_used": 0,
                "question_type": _question_type(question),
                "understanding_stage": understanding.understanding_stage,
                "tree_status": understanding.tree_status,
                "repository_lookup": understanding.repo_lookup,
            },
        )

    summary_evidence = await _load_summary_evidence(understanding.corpus_id, question, config)
    file_evidence = _best_deep_files(repo_path, question, summary_evidence)
    requirements = _select_relevant_requirements(_extract_requirement_lines(repo_path), file_evidence, question)

    metadata_parts = [
        "Mode: deep",
        f"Repository: {repo_path.name}",
        f"Question type: {_question_type(question)}",
        f"Repository lookup: {understanding.repo_lookup}",
        f"Understanding stage: {understanding.understanding_stage}",
        f"Understanding tree status: {understanding.tree_status}",
        f"Understanding revision: {understanding.revision_fp or '(unknown)'}",
    ]
    if understanding.model_used:
        metadata_parts.append(f"Understanding model: {understanding.model_used}")

    if requirements:
        metadata_parts.append("Relevant requirements from README or evidence:")
        metadata_parts.extend(f"- {line}" for line in requirements[:8])

    useful_summaries = [
        item for item in summary_evidence
        if _is_useful_summary_text(item.summary_text) or _is_useful_summary_text(item.headline)
    ]
    if useful_summaries:
        metadata_parts.append("Repository understanding evidence:")
        for item in useful_summaries[:5]:
            label = item.headline or item.unit_id
            summary = item.summary_text.strip()
            file_path = str(item.metadata.get("file_path") or "")
            loc = file_path or item.unit_id
            metadata_parts.append(f"- {label} [{loc}] ({item.reason})")
            if summary:
                metadata_parts.append(f"  Summary: {summary}")

    metadata_parts.append("Selected exact code evidence:")
    metadata_parts.extend(
        f"- {e.path} ({e.reason}, score={e.score}, ref={e.reference})"
        for e in file_evidence
    )

    code_parts = []
    for evidence in file_evidence:
        code_parts.append(f"--- {evidence.reference} ({evidence.reason}) ---\n{evidence.snippet}")

    diagnostics = {
        "fallback_used": "none",
        "understanding_used": True,
        "graph_expansion_used": False,
        "files_considered": len(summary_evidence),
        "files_used": len(file_evidence),
        "question_type": _question_type(question),
        "understanding_stage": understanding.understanding_stage,
        "tree_status": understanding.tree_status,
        "repository_lookup": understanding.repo_lookup,
    }
    return "\n".join(metadata_parts), "\n\n".join(code_parts), diagnostics


async def main() -> None:
    question = sys.argv[1] if len(sys.argv) > 1 else ""
    mode = sys.argv[2] if len(sys.argv) > 2 else "fast"
    repo_path = Path(os.environ.get("SOURCEBRIDGE_REPO_PATH", ".")).resolve()

    if not question:
        print(json.dumps({"error": "No question provided"}))
        sys.exit(1)

    if mode not in {"fast", "deep"}:
        print(json.dumps({"error": f"Invalid mode: {mode}"}))
        sys.exit(1)

    # Deprecation warning: the server-side orchestrator (internal/qa)
    # is the target path for deep mode going forward. Local-desktop
    # installs still use the subprocess fast path; set
    # SOURCEBRIDGE_QA_LEGACY=1 to silence.
    if mode == "deep" and os.environ.get("SOURCEBRIDGE_QA_LEGACY") != "1":
        print(
            "warning: cli_ask.py deep mode is deprecated — prefer the server's "
            "POST /api/v1/ask endpoint via `sourcebridge ask`. "
            "Set SOURCEBRIDGE_QA_LEGACY=1 to silence.",
            file=sys.stderr,
        )

    if not repo_path.exists():
        print(json.dumps({"error": f"Repository not found: {repo_path}"}))
        sys.exit(1)

    config = WorkerConfig()
    if mode == "deep":
        try:
            with contextlib.redirect_stdout(sys.stderr):
                context_metadata, context_code, diagnostics = await _build_deep_context(repo_path, question, config)
        except Exception as exc:
            output = {
                "answer": "Deep mode could not load repository understanding for this repo. Check repository-understanding availability or rerun with --mode fast.",
                "references": [],
                "related_requirements": [],
                "mode": mode,
                "diagnostics": {
                    "fallback_used": "blocked_for_understanding",
                    "understanding_used": False,
                    "graph_expansion_used": False,
                    "files_considered": 0,
                    "files_used": 0,
                    "question_type": _question_type(question),
                    "error": str(exc),
                },
                "usage": {
                    "provider": "none",
                    "model": "",
                    "input_tokens": 0,
                    "output_tokens": 0,
                },
            }
            print(json.dumps(output, indent=2))
            return
        if diagnostics.get("fallback_used") == "blocked_for_understanding":
            output = {
                "answer": "Deep mode requires repository understanding for this repo. Build understanding first, or rerun with --mode fast.",
                "references": [],
                "related_requirements": [],
                "mode": mode,
                "diagnostics": diagnostics,
                "usage": {
                    "provider": "none",
                    "model": "",
                    "input_tokens": 0,
                    "output_tokens": 0,
                },
            }
            print(json.dumps(output, indent=2))
            return
    else:
        context_metadata, context_code = _build_context(repo_path, question)
        fast_evidence = _collect_file_evidence(repo_path, question)
        diagnostics = {
            "fallback_used": "none",
            "understanding_used": False,
            "graph_expansion_used": False,
            "files_considered": len(fast_evidence),
            "files_used": len(fast_evidence),
            "question_type": _question_type(question),
        }

    provider = create_llm_provider(config)
    with contextlib.redirect_stdout(sys.stderr):
        answer, usage = await discuss_code(
            provider,
            question,
            context_code,
            context_metadata=context_metadata,
        )

    output = {
        "answer": answer.answer,
        "references": answer.references,
        "related_requirements": answer.related_requirements,
        "mode": mode,
        "diagnostics": diagnostics,
        "usage": {
            "provider": usage.provider,
            "model": usage.model,
            "input_tokens": usage.input_tokens,
            "output_tokens": usage.output_tokens,
        },
    }
    print(json.dumps(output, indent=2))


if __name__ == "__main__":
    asyncio.run(main())
