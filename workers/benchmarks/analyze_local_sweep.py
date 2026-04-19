"""Post-sweep qualitative analyzer.

Reads every model's DEEP cliff notes artifact from
``benchmark-results/local-sweep-v1/<label>/artifacts/`` and scores each
run on substance — not just gate metrics. Includes a hallucination
check (evidence paths that actually exist in the repo under test),
generic-phrase detection, and side-by-side content excerpts for the
four hardest sections (Domain Model, Key Abstractions, Testing
Strategy, Complexity & Risk Areas).

Writes (and overwrites):
    <results_dir>/REPORT.md
    <results_dir>/quality_analysis.json
"""

from __future__ import annotations

import argparse
import json
import re
from dataclasses import dataclass, field, asdict
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_RESULTS_DIR = REPO_ROOT / "benchmark-results" / "local-sweep-v1"
DEFAULT_REPO_ROOT = REPO_ROOT

MIDDLE_SECTIONS = ("Domain Model", "Key Abstractions", "Testing Strategy", "Complexity & Risk Areas")
OPENING_SECTIONS = ("System Purpose", "Architecture Overview", "Core System Flows", "Suggested Starting Points")

# Conservative "generic filler" list — each hit drops the quality score.
GENERIC_FILLERS = [
    "various components",
    "the system handles",
    "as needed",
    "and more",
    "etc.",
    "several modules",
    "in some cases",
    "this functionality",
    "revolves around",
    "fundamental concept",
    "crucial component",
    "central abstraction",
    "key abstractions include",
    "plays a crucial role",
    "plays a key role",
]

IDENTIFIER_RE = re.compile(r"`([A-Za-z_][A-Za-z0-9_]{2,})`")
FILE_CITATION_RE = re.compile(r"`([A-Za-z0-9_./-]+\.[A-Za-z0-9]+)`")


def index_real_files(repo_root: Path) -> tuple[set[str], set[str]]:
    """Walk the repo and collect real file paths + basenames.

    Returns ``(full_paths, basenames)``. Citations that match either
    are treated as grounded; only paths that match neither count as
    hallucinated. Without the basename allowance, valid unqualified
    references like ``document.go`` get flagged even though the file
    exists as ``internal/architecture/document.go``.
    """

    full_paths: set[str] = set()
    basenames: set[str] = set()
    for p in repo_root.rglob("*"):
        if not p.is_file():
            continue
        rel = p.relative_to(repo_root).as_posix()
        if rel.startswith(".git/") or rel.startswith("node_modules/") or rel.startswith("benchmark-results/"):
            continue
        full_paths.add(rel)
        basenames.add(p.name)
    return full_paths, basenames


@dataclass
class SectionQuality:
    title: str
    confidence: str
    bytes: int
    evidence_entries: int
    unique_files_cited: int
    unique_symbols: int
    generic_phrases: list[str] = field(default_factory=list)
    hallucinated_citations: list[str] = field(default_factory=list)
    score: float = 0.0
    excerpt: str = ""


@dataclass
class ModelQuality:
    label: str
    model: str
    size_gb: float
    status: str
    deep_seconds: int
    total_bytes: int
    avg_evidence_refs: float
    high_count: int
    medium_count: int
    low_count: int
    tokens_in: int
    tokens_out: int
    tokens_per_second: float
    hallucination_rate: float
    generic_phrase_count: int
    middle_section_quality_avg: float
    sections: list[SectionQuality] = field(default_factory=list)


def score_section(section: dict, real_files: set[str], real_basenames: set[str]) -> SectionQuality:
    """Score a single DEEP section 0-10 on substance.

    Rubric:
    +3 HIGH confidence (after post-gate enforcement)
    +2 cites >=3 unique real files (grounding)
    +2 names >=3 backtick identifiers (specificity)
    +1 content >= 600 bytes (substance)
    +1 content >= 900 bytes (depth)
    +1 no generic filler phrases (hygiene)
    -2 per hallucinated citation (inventing paths)

    Max 10, min can go negative with hallucinations.
    """

    content = (section.get("content") or "").strip()
    confidence = (section.get("confidence") or "").lower()
    evidence = section.get("evidence") or []
    cited_files = [e.get("filePath", "").strip() for e in evidence if e.get("filePath")]
    inline_file_hits = {m.group(1) for m in FILE_CITATION_RE.finditer(content) if "/" in m.group(1) or "." in m.group(1)}
    all_cited = {c for c in cited_files if c} | inline_file_hits

    def _is_grounded(path: str) -> bool:
        if path in real_files:
            return True
        # Bare filename — accept if any real file has this basename.
        if "/" not in path and path in real_basenames:
            return True
        return False

    hallucinated = sorted(f for f in all_cited if f and not _is_grounded(f))
    symbols = sorted({m.group(1) for m in IDENTIFIER_RE.finditer(content)})
    lowered = content.lower()
    generic_hits = [p for p in GENERIC_FILLERS if p in lowered]

    score = 0.0
    if confidence == "high":
        score += 3
    elif confidence == "medium":
        score += 1
    if len({c for c in cited_files if c}) >= 3:
        score += 2
    if len(symbols) >= 3:
        score += 2
    if len(content) >= 600:
        score += 1
    if len(content) >= 900:
        score += 1
    if not generic_hits:
        score += 1
    score -= 2 * len(hallucinated)

    excerpt = content[:400].replace("\n\n", " ").replace("\n", " ")

    return SectionQuality(
        title=section.get("title", ""),
        confidence=confidence,
        bytes=len(content),
        evidence_entries=len(evidence),
        unique_files_cited=len({c for c in cited_files if c}),
        unique_symbols=len(symbols),
        generic_phrases=generic_hits,
        hallucinated_citations=hallucinated,
        score=round(score, 2),
        excerpt=excerpt,
    )


def analyze_model(
    results_dir: Path, label: str, real_files: set[str], real_basenames: set[str]
) -> ModelQuality | None:
    model_dir = results_dir / label
    summary_path = model_dir / "summary.json"
    if not summary_path.exists():
        return None
    summary = json.loads(summary_path.read_text())

    artifact_path_field = summary.get("artifact_path") or ""
    artifact_path = (model_dir / artifact_path_field) if artifact_path_field else None
    sections_data: list[dict] = []
    if artifact_path and artifact_path.exists():
        artifact = json.loads(artifact_path.read_text())
        sections_data = artifact.get("sections") or []

    # Fall back to any artifact file in the artifacts/ dir if path resolution failed.
    if not sections_data:
        artifact_dir = model_dir / "artifacts"
        if artifact_dir.exists():
            for candidate in artifact_dir.glob("*.json"):
                try:
                    sections_data = (json.loads(candidate.read_text()) or {}).get("sections") or []
                    if sections_data:
                        break
                except Exception:
                    continue

    sections = [score_section(s, real_files, real_basenames) for s in sections_data]
    tokens = summary.get("tokens") or {}
    metrics = summary.get("metrics") or {}
    deep_s = int(summary.get("deep_seconds") or 0)
    tokens_out = int(tokens.get("output_tokens") or 0)
    tps = round(tokens_out / deep_s, 2) if deep_s else 0.0

    hallucinated_total = sum(len(s.hallucinated_citations) for s in sections)
    cited_total = sum(s.unique_files_cited for s in sections)
    halluc_rate = round(hallucinated_total / cited_total, 3) if cited_total else 0.0

    middle = [s for s in sections if s.title in MIDDLE_SECTIONS]
    middle_avg = round(sum(s.score for s in middle) / len(middle), 2) if middle else 0.0

    return ModelQuality(
        label=label,
        model=summary.get("model", ""),
        size_gb=float(summary.get("size_gb") or 0),
        status=summary.get("status", "?"),
        deep_seconds=deep_s,
        total_bytes=int(summary.get("total_content_bytes") or 0),
        avg_evidence_refs=float(summary.get("avg_evidence_refs") or 0),
        high_count=int(metrics.get("high_confidence") or 0),
        medium_count=int(metrics.get("medium_confidence") or 0),
        low_count=int(metrics.get("low_confidence") or 0),
        tokens_in=int(tokens.get("input_tokens") or 0),
        tokens_out=tokens_out,
        tokens_per_second=tps,
        hallucination_rate=halluc_rate,
        generic_phrase_count=sum(len(s.generic_phrases) for s in sections),
        middle_section_quality_avg=middle_avg,
        sections=sections,
    )


def write_quality_report(results_dir: Path, analyses: list[ModelQuality]) -> None:
    path = results_dir / "REPORT.md"
    lines: list[str] = [
        "# Local Model Sweep — DEEP Cliff Notes Quality",
        "",
        "Every local model runs against the same sourcebridge repo through the same",
        "DEEP-from-understanding pipeline. Quality is scored on substance (not just",
        "gate pass/fail): grounded citations, named specificity, absence of generic",
        "filler, and crucially whether cited files actually exist in the repo",
        "(hallucination check).",
        "",
        "Section score rubric (0-10): HIGH confidence +3, >=3 files +2, >=3 symbols +2,",
        ">=600 bytes +1, >=900 bytes +1, no generic filler +1, -2 per hallucinated",
        "citation.",
        "",
        "## Model comparison",
        "",
        "| Model | Size | Deep s | H/M/L | Bytes | Avg refs | Tok/s | Halluc % | Middle avg | Gen phrases |",
        "|---|---:|---:|---|---:|---:|---:|---:|---:|---:|",
    ]
    for a in analyses:
        lines.append(
            "| {label} | {size:.1f} | {ds} | {h}/{m}/{l} | {bytes} | {avg:.2f} | {tps:.1f} | {halluc:.1%} | {mid:.1f} | {gen} |".format(
                label=a.label,
                size=a.size_gb,
                ds=a.deep_seconds,
                h=a.high_count,
                m=a.medium_count,
                l=a.low_count,
                bytes=a.total_bytes,
                avg=a.avg_evidence_refs,
                tps=a.tokens_per_second,
                halluc=a.hallucination_rate,
                mid=a.middle_section_quality_avg,
                gen=a.generic_phrase_count,
            )
        )

    lines += ["", "## Middle sections — side by side", ""]
    for title in MIDDLE_SECTIONS:
        lines += [f"### {title}", ""]
        lines += [
            "| Model | Conf | Bytes | Files | Symbols | Hallucs | Score |",
            "|---|---|---:|---:|---:|---:|---:|",
        ]
        for a in analyses:
            sec = next((s for s in a.sections if s.title == title), None)
            if not sec:
                lines.append(f"| {a.label} | — | — | — | — | — | — |")
                continue
            lines.append(
                "| {l} | {c} | {b} | {f} | {sym} | {h} | {sc:.1f} |".format(
                    l=a.label,
                    c=sec.confidence,
                    b=sec.bytes,
                    f=sec.unique_files_cited,
                    sym=sec.unique_symbols,
                    h=len(sec.hallucinated_citations),
                    sc=sec.score,
                )
            )
        lines += ["", "**Excerpts:**", ""]
        for a in analyses:
            sec = next((s for s in a.sections if s.title == title), None)
            if not sec:
                continue
            lines += [
                f"- **{a.label}** ({sec.confidence}, {sec.bytes} B, {sec.unique_symbols} symbols):",
                f"  > {sec.excerpt}",
                "",
            ]

    lines += [
        "",
        "## Hallucination details (cited paths not present in the repo)",
        "",
    ]
    any_halluc = False
    for a in analyses:
        per_model_hallucs: list[tuple[str, list[str]]] = []
        for sec in a.sections:
            if sec.hallucinated_citations:
                per_model_hallucs.append((sec.title, sec.hallucinated_citations))
        if not per_model_hallucs:
            continue
        any_halluc = True
        lines.append(f"### {a.label}")
        for title, paths in per_model_hallucs:
            lines.append(f"- **{title}**: {', '.join(paths)}")
        lines.append("")
    if not any_halluc:
        lines.append("_No hallucinated file citations detected across any model._")
        lines.append("")

    lines += [
        "",
        "## Smallest-viable bar",
        "",
        "\"Viable\" here means: hard gates pass (16 sections, zero zero-evidence),",
        "middle-section average quality score >= 4, no more than 1 hallucinated",
        "citation per section on average.",
        "",
    ]
    for a in analyses:
        hard = "pass" if a.low_count < 16 and a.total_bytes > 0 else "fail"
        mid_ok = "yes" if a.middle_section_quality_avg >= 4 else "no"
        lines.append(f"- `{a.label}` ({a.size_gb:.1f} GB): middle_avg={a.middle_section_quality_avg:.1f} → viable={mid_ok}")
    lines.append("")

    path.write_text("\n".join(lines) + "\n")
    (results_dir / "quality_analysis.json").write_text(
        json.dumps([asdict(a) for a in analyses], indent=2)
    )


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--results-dir", type=Path, default=DEFAULT_RESULTS_DIR)
    parser.add_argument("--repo-root", type=Path, default=DEFAULT_REPO_ROOT)
    args = parser.parse_args()

    real_files, real_basenames = index_real_files(args.repo_root)
    print(f"[analyze] indexed {len(real_files)} real files ({len(real_basenames)} basenames) under {args.repo_root}")

    analyses: list[ModelQuality] = []
    for model_dir in sorted(args.results_dir.iterdir()):
        if not model_dir.is_dir() or model_dir.name in {"artifacts",}:
            continue
        label = model_dir.name
        result = analyze_model(args.results_dir, label, real_files, real_basenames)
        if result is None:
            continue
        analyses.append(result)
        print(
            f"[analyze] {label}: middle_avg={result.middle_section_quality_avg:.2f} "
            f"H/M/L={result.high_count}/{result.medium_count}/{result.low_count} "
            f"halluc_rate={result.hallucination_rate:.1%}"
        )

    if not analyses:
        print("[analyze] no model summaries found")
        return

    write_quality_report(args.results_dir, analyses)
    print(f"[analyze] wrote REPORT.md + quality_analysis.json to {args.results_dir}")


if __name__ == "__main__":
    main()
