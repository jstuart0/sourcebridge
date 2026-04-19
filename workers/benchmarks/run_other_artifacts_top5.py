"""Top-5 sweep for the non-cliff-notes artifacts.

Runs Learning Path, Code Tour, and Workflow Story generation against
each of the top-5 models from the DEEP cliff-notes sweep, so we can
see how the tightened artifacts hold up across the same leaderboard.

Writes to ``benchmark-results/other-artifacts-top5/<label>/`` with
per-artifact subdirs matching ``run_other_artifacts_bench`` layout so
the same analyzer can read both.
"""

from __future__ import annotations

import argparse
import importlib
import json
import subprocess
import sys
import time
from pathlib import Path

_THIS_DIR = Path(__file__).resolve().parent
if str(_THIS_DIR) not in sys.path:
    sys.path.insert(0, str(_THIS_DIR))

import run_deep_depth_bench as bench  # noqa: E402
import run_local_sweep as sweep  # noqa: E402
import run_other_artifacts_bench as other  # noqa: E402

DEFAULT_RESULTS_DIR = bench.REPO_ROOT / "benchmark-results" / "other-artifacts-top5"
OLLAMA_URL = "http://192.168.10.108:11434/v1"

# Top-5 from the DEEP cliff-notes sweep (ARTICLE.md). Claude Sonnet and
# Gemini Flash round out the cloud side; the two perfect-scoring local
# models anchor the local side. claude-haiku is already exercised by
# ``run_other_artifacts_bench`` so we include it here for parity with
# the cliff-notes leaderboard, not as a new data point.
# Cloud models first, then the faster Qwen 3.6 MoE. Qwen3-32b dense is
# omitted here because a single DEEP artifact takes ~35-45 min on a Mac
# Studio via Ollama, which blows past the bench polling ceiling even
# after lifting it to 3600s. It needs a dedicated, slower run (or a
# parallel per-artifact harness). The MoE path (qwen3.6) is fast enough
# to fit the budget and still represents local-model quality.
TOP5_MODELS: list[tuple[str, str, float, str]] = [
    ("claude-haiku-4.5", "anthropic/claude-haiku-4.5", 0.0, "openrouter"),
    ("claude-sonnet-4", "anthropic/claude-sonnet-4", 0.0, "openrouter"),
    ("gemini-2.5-flash", "google/gemini-2.5-flash", 0.0, "openrouter"),
    ("qwen3.6-35b-a3b-moe", "qwen3.6:35b-a3b-q4_K_M", 23.0, "ollama"),
]


def install_override_for_provider(provider: str) -> None:
    """Reload bench module so ``make_override`` targets the right provider."""
    importlib.reload(bench)
    importlib.reload(other)
    if provider == "ollama":
        other.make_override_openrouter = _make_ollama_override_for_other
    else:
        # restore default openrouter override
        importlib.reload(other)


def _make_ollama_override_for_other(model: str, _api_key: str, repo_mount: str) -> Path:
    """Write a docker-compose override that points worker + API at local Ollama."""
    import tempfile

    handle = tempfile.NamedTemporaryFile("w", delete=False, suffix=".yml")
    handle.write(
        f"""
services:
  worker:
    environment:
      - SOURCEBRIDGE_WORKER_LLM_PROVIDER=openai
      - SOURCEBRIDGE_WORKER_LLM_BASE_URL={OLLAMA_URL}
      - SOURCEBRIDGE_WORKER_LLM_MODEL={model}
      - SOURCEBRIDGE_WORKER_LLM_API_KEY=ollama
      - SOURCEBRIDGE_WORKER_LLM_DISABLE_THINKING=true
      - SOURCEBRIDGE_WORKER_EMBEDDING_PROVIDER=ollama
      - SOURCEBRIDGE_WORKER_EMBEDDING_BASE_URL={OLLAMA_URL.replace('/v1', '')}
  sourcebridge:
    volumes:
      - "{repo_mount}:/bench/repo:ro"
    environment:
      - SOURCEBRIDGE_LLM_PROVIDER=openai
      - SOURCEBRIDGE_LLM_BASE_URL={OLLAMA_URL}
      - SOURCEBRIDGE_LLM_MODEL={model}
      - SOURCEBRIDGE_LLM_SUMMARY_MODEL={model}
      - SOURCEBRIDGE_LLM_API_KEY=ollama
      - SOURCEBRIDGE_LLM_DISABLE_THINKING=true
"""
    )
    handle.flush()
    handle.close()
    return Path(handle.name)


def run_one(
    label: str,
    model: str,
    size_gb: float,
    provider: str,
    depth: str,
    *,
    results_root: Path,
    repo_mount: str,
    repo_name: str,
) -> dict:
    model_dir = results_root / label
    model_dir.mkdir(parents=True, exist_ok=True)

    if provider == "ollama" and not sweep.probe_model_ready(OLLAMA_URL, model):
        skipped = {"label": label, "model": model, "status": "skipped_not_pulled"}
        (model_dir / "summary.json").write_text(json.dumps(skipped, indent=2))
        return skipped

    install_override_for_provider(provider)
    run_started = time.time()
    try:
        other.run_bench(
            label=label,
            model=model,
            depth=depth,
            results_root=results_root.parent,
            repo_mount=repo_mount,
            repo_name=repo_name,
        )
        status = "ok"
        error: str | None = None
    except Exception as exc:
        status = "failed"
        error = str(exc)
        print(f"[top5] {label}: FAILED -- {exc}", flush=True)

    summary = {
        "label": label,
        "model": model,
        "size_gb": size_gb,
        "provider": provider,
        "depth": depth,
        "status": status,
        "wall_seconds": int(time.time() - run_started),
    }
    if error:
        summary["error"] = error

    # Copy the per-artifact summaries into the top5 dir for analysis.
    src_dir = results_root.parent / f"other-artifacts-{label}"
    if src_dir.exists():
        for artifact in ("learning_path", "code_tour", "workflow_story"):
            src = src_dir / artifact / "summary.json"
            if src.exists():
                dest = model_dir / f"{artifact}.summary.json"
                dest.write_text(src.read_text())
    (model_dir / "summary.json").write_text(json.dumps(summary, indent=2))
    return summary


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--results-dir", type=Path, default=DEFAULT_RESULTS_DIR)
    parser.add_argument("--repo-path", type=Path, default=bench.REPO_ROOT)
    parser.add_argument("--depth", default="DEEP")
    parser.add_argument("--models", default="", help="Comma-separated label filter")
    args = parser.parse_args()

    args.results_dir.mkdir(parents=True, exist_ok=True)
    repo_mount = str(args.repo_path.resolve())
    selected = {label for label in (args.models.split(",") if args.models else []) if label}

    summaries: list[dict] = []
    for label, model, size_gb, provider in TOP5_MODELS:
        if selected and label not in selected:
            continue
        print(f"\n[top5] === {label} ({model}, {provider}, {size_gb} GB) ===", flush=True)
        summaries.append(
            run_one(
                label=label,
                model=model,
                size_gb=size_gb,
                provider=provider,
                depth=args.depth,
                results_root=args.results_dir,
                repo_mount=repo_mount,
                repo_name=f"sourcebridge-top5-{label}",
            )
        )

    (args.results_dir / "all_summaries.json").write_text(json.dumps(summaries, indent=2))
    print(f"\n[top5] complete — {len(summaries)} models. Results at {args.results_dir}", flush=True)


if __name__ == "__main__":
    main()
