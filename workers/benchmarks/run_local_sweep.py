"""Local-model sweep benchmark.

Reuses the DEEP-from-understanding flow from ``run_deep_depth_bench`` but
points the worker at an Ollama endpoint (Mac Studio by default) so we can
measure how each local model performs against the same structured-fact
pipeline that was tuned on gemini-2.5-flash.

Writes:
    benchmark-results/<results-dir>/
        <label>/
            report.md, results.json, worker.log, artifacts/
        summary.csv
        per_section.csv
        REPORT.md

Designed to keep going if an individual model fails — the article is only
useful if every candidate gets tried. Failures are logged and the sweep
proceeds.
"""

from __future__ import annotations

import argparse
import csv
import json
import re
import subprocess
import sys
import tempfile
import time
from dataclasses import asdict
from pathlib import Path

_THIS_DIR = Path(__file__).resolve().parent
if str(_THIS_DIR) not in sys.path:
    sys.path.insert(0, str(_THIS_DIR))

import run_deep_depth_bench as bench  # noqa: E402

DEFAULT_OLLAMA_URL = "http://192.168.10.108:11434/v1"
DEFAULT_RESULTS_DIR = bench.REPO_ROOT / "benchmark-results" / "local-sweep-v1"

MIDDLE_SECTIONS = ("Domain Model", "Key Abstractions", "Testing Strategy", "Complexity & Risk Areas")
IDENTIFIER_RE = re.compile(r"`([A-Za-z_][A-Za-z0-9_]{2,})`")

# Ordered smallest-first so cheap data arrives early and the sweep produces
# useful intermediate state if a large model times out.
SWEEP_MODELS: list[tuple[str, str, float]] = [
    ("llama3.2-3b", "llama3.2:3b", 2.0),
    ("qwen3.5-4b", "qwen3.5:4b", 3.4),
    ("qwen3-8b", "qwen3:8b", 5.2),
    ("qwen3-14b", "qwen3:14b", 9.3),
    ("qwen3-32b", "qwen3:32b", 20.2),
    ("qwen3.5-35b-a3b-moe", "qwen3.5:35b-a3b", 23.9),
    ("qwen3.6-35b-a3b-moe", "qwen3.6:35b-a3b-q4_K_M", 23.0),
    ("gemma4-26b-a4b-moe", "gemma4:26b-a4b-it-q4_K_M", 15.5),
    ("gemma4-31b", "gemma4:31b-it-q4_K_M", 18.5),
    ("qwen3.5-122b-a10b-moe", "qwen3.5:122b-a10b", 81.4),
]


def make_ollama_override(ollama_base_url: str):
    """Return a make_override replacement that targets Ollama + Mac Studio.

    The existing harness builds the override for OpenRouter. For the local
    sweep the worker's LLM calls go to the Mac Studio Ollama; the embedding
    provider keeps pointing at the dev host's local Ollama container that
    was set up earlier in this session.
    """

    def _override(model: str, api_key: str, repo_mount: str):
        handle = tempfile.NamedTemporaryFile("w", delete=False, suffix=".yml")
        # Thinking mode is disabled by default in _resolve_disable_thinking()
        # but we set it explicitly so the benchmark config is self-documenting.
        # Reasoning chains from Qwen3.x drastically slow generation and
        # produce weaker final JSON; gemma4/llama ignore the flag harmlessly.
        handle.write(
            f"""
services:
  worker:
    environment:
      - SOURCEBRIDGE_WORKER_LLM_PROVIDER=ollama
      - SOURCEBRIDGE_WORKER_LLM_BASE_URL={ollama_base_url}
      - SOURCEBRIDGE_WORKER_LLM_MODEL={model}
      - SOURCEBRIDGE_WORKER_LLM_API_KEY=ollama
      - SOURCEBRIDGE_WORKER_LLM_DISABLE_THINKING=true
      - SOURCEBRIDGE_WORKER_EMBEDDING_PROVIDER=ollama
      - SOURCEBRIDGE_WORKER_EMBEDDING_BASE_URL=http://host.docker.internal:11434
  sourcebridge:
    volumes:
      - "{repo_mount}:/bench/repo:ro"
    environment:
      - SOURCEBRIDGE_LLM_PROVIDER=ollama
      - SOURCEBRIDGE_LLM_BASE_URL={ollama_base_url}
      - SOURCEBRIDGE_LLM_MODEL={model}
      - SOURCEBRIDGE_LLM_SUMMARY_MODEL={model}
      - SOURCEBRIDGE_LLM_API_KEY=ollama
      - SOURCEBRIDGE_LLM_DISABLE_THINKING=true
"""
        )
        handle.flush()
        handle.close()
        return Path(handle.name)

    return _override


def probe_model_ready(base_url: str, model: str, timeout_s: int = 60) -> bool:
    """Quick Ollama hit to confirm the model is pulled and loads."""
    # Ollama's /api/tags is on the non-/v1 root.
    tags_url = base_url.replace("/v1", "").rstrip("/") + "/api/tags"
    try:
        import urllib.request as ur

        with ur.urlopen(tags_url, timeout=timeout_s) as resp:
            data = json.loads(resp.read().decode())
        pulled = {entry.get("name", "") for entry in data.get("models", [])}
        return model in pulled
    except Exception as exc:
        print(f"[sweep] probe failed for {model}: {exc}", flush=True)
        return False


def start_worker_log_capture(label: str, model_dir: Path) -> tuple[subprocess.Popen | None, Path]:
    """Tail the worker container's docker logs to ``<model_dir>/worker.log``.

    Returns the tail process handle so the caller can stop it after the run.
    Waits until the container actually exists, then exits cleanly if the
    container is later torn down by the bench harness.
    """

    log_path = model_dir / "worker.log"
    script = f"""
set -e
LOG_FILE={log_path}
PROJECT_PREFIX=sb-deep-{label.replace('.', '-').replace('_', '-')}-deep_from_understanding
while : ; do
  CID=$(docker ps -q --filter name=${{PROJECT_PREFIX}}-worker | head -1)
  if [ -n "$CID" ]; then
    docker logs -f "$CID" >>"$LOG_FILE" 2>&1
    break
  fi
  sleep 2
done
"""
    proc = subprocess.Popen(["bash", "-c", script])
    return proc, log_path


def stop_worker_log_capture(proc: subprocess.Popen | None) -> None:
    if proc is None:
        return
    try:
        proc.terminate()
        proc.wait(timeout=5)
    except Exception:
        try:
            proc.kill()
        except Exception:
            pass


def enrich_artifact_metrics(artifact_path: Path) -> dict:
    """Walk a saved cliff notes artifact and extract per-section + totals."""
    try:
        data = json.loads(artifact_path.read_text())
    except Exception:
        return {"error": "artifact_unreadable"}

    sections = data.get("sections") or []
    total_bytes = sum(len((s.get("content") or "")) for s in sections)
    high = sum(1 for s in sections if (s.get("confidence") or "").lower() == "high")
    med = sum(1 for s in sections if (s.get("confidence") or "").lower() == "medium")
    low = sum(1 for s in sections if (s.get("confidence") or "").lower() == "low")

    per_section: list[dict] = []
    middle_identifier_total = 0
    for s in sections:
        content = s.get("content") or ""
        evidence = s.get("evidence") or []
        unique_paths = {e.get("filePath", "") for e in evidence if e.get("filePath")}
        identifiers = {m.group(1) for m in IDENTIFIER_RE.finditer(content)}
        if s.get("title") in MIDDLE_SECTIONS:
            middle_identifier_total += len(identifiers)
        per_section.append(
            {
                "title": s.get("title", ""),
                "confidence": (s.get("confidence") or "").lower(),
                "bytes": len(content),
                "evidence_entries": len(evidence),
                "unique_files": len(unique_paths),
                "identifiers": len(identifiers),
                "refinement_status": s.get("refinementStatus") or "",
            }
        )

    return {
        "sections": len(sections),
        "total_bytes": total_bytes,
        "high_confidence": high,
        "medium_confidence": med,
        "low_confidence": low,
        "middle_identifier_total": middle_identifier_total,
        "per_section": per_section,
    }


def extract_tokens_from_worker_log(log_path: Path) -> dict:
    """Parse the captured worker log for total LLM token usage on this run.

    The log is line-delimited JSON from structlog; we look at
    ``cliff_notes_hierarchical_completed`` (reports total input/output
    tokens for the DEEP render). If multiple cliff-notes runs landed in
    one log we sum the DEEP repository-scope entries only.
    """

    if not log_path.exists():
        return {"input_tokens": 0, "output_tokens": 0}
    total_in = 0
    total_out = 0
    sections_total = 0
    for line in log_path.read_text().splitlines():
        if '"cliff_notes_hierarchical_completed"' not in line:
            continue
        try:
            payload = json.loads(line)
        except Exception:
            continue
        total_in += int(payload.get("input_tokens") or 0)
        total_out += int(payload.get("output_tokens") or 0)
        sections_total += int(payload.get("sections") or 0)
    return {
        "input_tokens": total_in,
        "output_tokens": total_out,
        "sections_accumulated": sections_total,
    }


def run_one_model(
    label: str,
    model: str,
    size_gb: float,
    *,
    results_dir: Path,
    repo_mount: str,
    repo_name: str,
    ollama_base_url: str,
) -> dict:
    """Run the existing benchmark_scenario for one Ollama model."""
    model_dir = results_dir / label
    model_dir.mkdir(parents=True, exist_ok=True)
    summary: dict = {
        "label": label,
        "model": model,
        "size_gb": size_gb,
        "status": "started",
        "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }

    if not probe_model_ready(ollama_base_url, model):
        summary["status"] = "skipped_not_pulled"
        print(f"[sweep] {label}: model not pulled on {ollama_base_url} — skipping", flush=True)
        (model_dir / "summary.json").write_text(json.dumps(summary, indent=2))
        return summary

    # Monkey-patch override builder so benchmark_scenario uses Ollama.
    bench.make_override = make_ollama_override(ollama_base_url)

    log_proc, log_path = start_worker_log_capture(label, model_dir)

    run_started = time.time()
    scenario_result = None
    try:
        scenario_result = bench.benchmark_scenario(
            label,
            model,
            "deep_from_understanding",
            "ollama",
            0,
            results_dir=model_dir,
            repo_mount=repo_mount,
            repo_name=f"{repo_name}-{label}",
            import_path="/bench/repo",
        )
    except Exception as exc:
        print(f"[sweep] {label}: FAILED -- {exc}", flush=True)
        summary["status"] = "failed"
        summary["error"] = str(exc)
    finally:
        stop_worker_log_capture(log_proc)

    wall_s = int(time.time() - run_started)
    summary["wall_seconds"] = wall_s

    if scenario_result is not None:
        summary.update(asdict(scenario_result))
        summary["status"] = "ok" if scenario_result.passed_hard_gates else "partial"

        artifact_path = model_dir / scenario_result.artifact_path if scenario_result.artifact_path else None
        if artifact_path and artifact_path.exists():
            summary["metrics"] = enrich_artifact_metrics(artifact_path)

    summary["tokens"] = extract_tokens_from_worker_log(log_path)
    (model_dir / "summary.json").write_text(json.dumps(summary, indent=2))
    return summary


def write_summary_csv(results_dir: Path, summaries: list[dict]) -> None:
    path = results_dir / "summary.csv"
    fieldnames = [
        "label",
        "model",
        "size_gb",
        "status",
        "wall_seconds",
        "index_seconds",
        "understanding_seconds",
        "deep_seconds",
        "total_seconds",
        "section_count",
        "total_content_bytes",
        "avg_evidence_refs",
        "zero_evidence_sections",
        "forbidden_phrase_count",
        "low_confidence_sections",
        "passed_hard_gates",
        "passed_soft_quality",
        "high_confidence",
        "medium_confidence",
        "low_confidence",
        "middle_identifier_total",
        "input_tokens",
        "output_tokens",
        "tokens_per_deep_second",
    ]
    with path.open("w", newline="") as fh:
        writer = csv.DictWriter(fh, fieldnames=fieldnames)
        writer.writeheader()
        for s in summaries:
            metrics = s.get("metrics") or {}
            tokens = s.get("tokens") or {}
            row = {k: s.get(k, "") for k in fieldnames if k in s}
            row.update(
                {
                    "high_confidence": metrics.get("high_confidence", ""),
                    "medium_confidence": metrics.get("medium_confidence", ""),
                    "low_confidence": metrics.get("low_confidence", ""),
                    "middle_identifier_total": metrics.get("middle_identifier_total", ""),
                    "input_tokens": tokens.get("input_tokens", ""),
                    "output_tokens": tokens.get("output_tokens", ""),
                    "tokens_per_deep_second": (
                        round((tokens.get("output_tokens", 0) or 0) / s["deep_seconds"], 2)
                        if s.get("deep_seconds")
                        else ""
                    ),
                }
            )
            writer.writerow({k: row.get(k, "") for k in fieldnames})


def write_per_section_csv(results_dir: Path, summaries: list[dict]) -> None:
    path = results_dir / "per_section.csv"
    fieldnames = [
        "label",
        "model",
        "title",
        "confidence",
        "bytes",
        "evidence_entries",
        "unique_files",
        "identifiers",
        "refinement_status",
    ]
    with path.open("w", newline="") as fh:
        writer = csv.DictWriter(fh, fieldnames=fieldnames)
        writer.writeheader()
        for s in summaries:
            metrics = s.get("metrics") or {}
            for sec in metrics.get("per_section") or []:
                writer.writerow({"label": s["label"], "model": s["model"], **sec})


def write_report_markdown(results_dir: Path, summaries: list[dict], ollama_base_url: str) -> None:
    path = results_dir / "REPORT.md"
    lines: list[str] = [
        "# Local Model Sweep — DEEP Cliff Notes",
        "",
        f"Target: `sourcebridge` repository, DEEP-from-understanding scenario.",
        f"LLM endpoint: `{ollama_base_url}`.",
        f"Pipeline: structured-fact inventory + post-gate confidence enforcement (v13 code).",
        "",
        "## Summary table",
        "",
        "| Model | Size | Wall s | Deep s | HIGH | MED | LOW | Bytes | Avg refs | In tok | Out tok | tok/s |",
        "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|",
    ]
    for s in summaries:
        metrics = s.get("metrics") or {}
        tokens = s.get("tokens") or {}
        deep_s = s.get("deep_seconds") or 0
        tps = round((tokens.get("output_tokens", 0) or 0) / deep_s, 1) if deep_s else 0
        lines.append(
            "| {model} | {size} GB | {wall} | {deep} | {h} | {m} | {l} | {bytes} | {avg} | {it} | {ot} | {tps} |".format(
                model=s.get("label", "?"),
                size=s.get("size_gb", "?"),
                wall=s.get("wall_seconds", "?"),
                deep=s.get("deep_seconds", "?"),
                h=metrics.get("high_confidence", "?"),
                m=metrics.get("medium_confidence", "?"),
                l=metrics.get("low_confidence", "?"),
                bytes=s.get("total_content_bytes", "?"),
                avg=s.get("avg_evidence_refs", "?"),
                it=tokens.get("input_tokens", "?"),
                ot=tokens.get("output_tokens", "?"),
                tps=tps,
            )
        )

    lines.extend(
        [
            "",
            "## Middle-section identifier density",
            "",
            "Target: ≥5 backtick identifiers per middle section (Domain Model, Key Abstractions,",
            "Testing Strategy, Complexity & Risk Areas).",
            "",
            "| Model | Domain Model | Key Abstractions | Testing Strategy | Complexity & Risk | Total |",
            "|---|---:|---:|---:|---:|---:|",
        ]
    )
    for s in summaries:
        metrics = s.get("metrics") or {}
        per_section_map = {sec["title"]: sec for sec in (metrics.get("per_section") or [])}
        cells = []
        for title in MIDDLE_SECTIONS:
            sec = per_section_map.get(title)
            cells.append(str(sec.get("identifiers", "-")) if sec else "-")
        lines.append(
            "| {model} | {a} | {b} | {c} | {d} | {total} |".format(
                model=s.get("label", "?"),
                a=cells[0],
                b=cells[1],
                c=cells[2],
                d=cells[3],
                total=metrics.get("middle_identifier_total", "-"),
            )
        )

    lines.extend(
        [
            "",
            "## Gate status",
            "",
            "| Model | Hard | Soft | Status |",
            "|---|---|---|---|",
        ]
    )
    for s in summaries:
        lines.append(
            "| {model} | {h} | {so} | {st} |".format(
                model=s.get("label", "?"),
                h="pass" if s.get("passed_hard_gates") else "fail",
                so="pass" if s.get("passed_soft_quality") else "fail",
                st=s.get("status", "?"),
            )
        )

    path.write_text("\n".join(lines) + "\n")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--results-dir", type=Path, default=DEFAULT_RESULTS_DIR)
    parser.add_argument("--ollama-url", default=DEFAULT_OLLAMA_URL)
    parser.add_argument("--repo-path", type=Path, default=bench.REPO_ROOT)
    parser.add_argument("--repo-name", default="sourcebridge-local")
    parser.add_argument(
        "--models",
        default="",
        help="Comma-separated labels to run (defaults to the full SWEEP_MODELS list)",
    )
    args = parser.parse_args()

    args.results_dir.mkdir(parents=True, exist_ok=True)
    selected_labels = set(label for label in (args.models.split(",") if args.models else []) if label)
    summaries: list[dict] = []
    repo_mount = str(args.repo_path.resolve())

    for label, model, size_gb in SWEEP_MODELS:
        if selected_labels and label not in selected_labels:
            continue
        print(f"\n[sweep] === {label} ({model}, {size_gb} GB) ===", flush=True)
        summary = run_one_model(
            label,
            model,
            size_gb,
            results_dir=args.results_dir,
            repo_mount=repo_mount,
            repo_name=args.repo_name,
            ollama_base_url=args.ollama_url,
        )
        summaries.append(summary)
        # Persist partial results after every model so a mid-sweep failure
        # still leaves a usable CSV + REPORT.md.
        write_summary_csv(args.results_dir, summaries)
        write_per_section_csv(args.results_dir, summaries)
        write_report_markdown(args.results_dir, summaries, args.ollama_url)

    (args.results_dir / "all_summaries.json").write_text(json.dumps(summaries, indent=2))
    print(f"\n[sweep] complete — {len(summaries)} models. Results at {args.results_dir}", flush=True)


if __name__ == "__main__":
    main()
