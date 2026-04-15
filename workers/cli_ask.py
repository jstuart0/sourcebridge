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

# Ensure workers package is importable when run from workers/ dir
_here = os.path.dirname(os.path.abspath(__file__))
_parent = os.path.dirname(_here)
if _parent not in sys.path:
    sys.path.insert(0, _parent)

from workers.common.config import WorkerConfig  # noqa: E402
from workers.common.llm.config import create_llm_provider  # noqa: E402
from workers.reasoning.discussion import discuss_code  # noqa: E402

SUPPORTED_EXTENSIONS = {".go", ".py", ".ts", ".js", ".java", ".rs"}
MAX_FILES = 8
MAX_SNIPPET_LINES = 120
MAX_FILE_BYTES = 32_000
STOPWORDS = {
    "the",
    "and",
    "for",
    "with",
    "this",
    "that",
    "what",
    "does",
    "how",
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


def _tokenize_question(question: str) -> list[str]:
    tokens = [t.lower() for t in re.findall(r"[a-zA-Z0-9_-]+", question)]
    return [t for t in tokens if len(t) >= 3 and t not in STOPWORDS]


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


async def main() -> None:
    question = sys.argv[1] if len(sys.argv) > 1 else ""
    repo_path = Path(os.environ.get("SOURCEBRIDGE_REPO_PATH", ".")).resolve()

    if not question:
        print(json.dumps({"error": "No question provided"}))
        sys.exit(1)

    if not repo_path.exists():
        print(json.dumps({"error": f"Repository not found: {repo_path}"}))
        sys.exit(1)

    context_metadata, context_code = _build_context(repo_path, question)

    config = WorkerConfig()
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
