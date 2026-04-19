"""Top-5 medium-depth sweep.

Runs the top 5 models from the DEEP sweep again, this time at MEDIUM depth
(8 required sections instead of 16) so we can see how runtime and quality
change when we ask for less output. Models cover both local Ollama and
OpenRouter cloud paths, so this script juggles make_override per model.

Writes to ``benchmark-results/medium-sweep-v1/<label>/`` with the same
shape as the DEEP sweep; ``analyze_medium_sweep.py`` handles MEDIUM's
8-section required set.
"""

from __future__ import annotations

import argparse
import importlib
import json
import subprocess
import sys
import time
from dataclasses import asdict
from pathlib import Path

_THIS_DIR = Path(__file__).resolve().parent
if str(_THIS_DIR) not in sys.path:
    sys.path.insert(0, str(_THIS_DIR))

import run_deep_depth_bench as bench  # noqa: E402
import run_local_sweep as sweep  # noqa: E402

DEFAULT_RESULTS_DIR = bench.REPO_ROOT / "benchmark-results" / "medium-sweep-v1"
OLLAMA_URL = "http://192.168.10.108:11434/v1"

# Top 5 from the DEEP sweep, ranked by middle-section quality.
# Each entry: (label, model id, size_gb, provider)
MEDIUM_MODELS: list[tuple[str, str, float, str]] = [
    ("qwen3-32b", "qwen3:32b", 20.2, "ollama"),
    ("qwen3.6-35b-a3b-moe", "qwen3.6:35b-a3b-q4_K_M", 23.0, "ollama"),
    ("claude-haiku-4.5", "anthropic/claude-haiku-4.5", 0.0, "openrouter"),
    ("claude-sonnet-4", "anthropic/claude-sonnet-4", 0.0, "openrouter"),
    ("gemini-2.5-flash", "google/gemini-2.5-flash", 0.0, "openrouter"),
]


def start_worker_log_capture(label: str, model_dir: Path):
    log_path = model_dir / "worker.log"
    script = f"""
set -e
LOG_FILE={log_path}
PROJECT_PREFIX=sb-deep-{label.replace('.', '-').replace('_', '-')}-medium_only
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


def install_override(provider: str) -> None:
    importlib.reload(bench)
    if provider == "ollama":
        bench.make_override = sweep.make_ollama_override(OLLAMA_URL)


def run_one(
    label: str, model: str, size_gb: float, provider: str, *, results_dir: Path, repo_mount: str
) -> dict:
    model_dir = results_dir / label
    model_dir.mkdir(parents=True, exist_ok=True)
    summary: dict = {
        "label": label,
        "model": model,
        "size_gb": size_gb,
        "provider": provider,
        "status": "started",
        "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }

    if provider == "ollama" and not sweep.probe_model_ready(OLLAMA_URL, model):
        summary["status"] = "skipped_not_pulled"
        (model_dir / "summary.json").write_text(json.dumps(summary, indent=2))
        return summary

    install_override(provider)
    api_key = "ollama" if provider == "ollama" else bench.decode_openrouter_key()

    log_proc, log_path = start_worker_log_capture(label, model_dir)
    run_started = time.time()
    scenario_result = None
    try:
        scenario_result = bench.benchmark_scenario(
            label,
            model,
            "medium_only",
            api_key,
            0,
            results_dir=model_dir,
            repo_mount=repo_mount,
            repo_name=f"sourcebridge-medium-{label}",
            import_path="/bench/repo",
        )
    except Exception as exc:
        print(f"[medium] {label}: FAILED -- {exc}", flush=True)
        summary["status"] = "failed"
        summary["error"] = str(exc)
    finally:
        try:
            log_proc.terminate()
            log_proc.wait(timeout=5)
        except Exception:
            try:
                log_proc.kill()
            except Exception:
                pass

    summary["wall_seconds"] = int(time.time() - run_started)

    if scenario_result is not None:
        summary.update(asdict(scenario_result))
        summary["status"] = "ok" if scenario_result.section_count >= 8 else "partial"
        artifact_path = (
            model_dir / scenario_result.artifact_path
            if scenario_result.artifact_path
            else None
        )
        if artifact_path and artifact_path.exists():
            summary["metrics"] = sweep.enrich_artifact_metrics(artifact_path)

    summary["tokens"] = sweep.extract_tokens_from_worker_log(log_path)
    (model_dir / "summary.json").write_text(json.dumps(summary, indent=2))
    return summary


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--results-dir", type=Path, default=DEFAULT_RESULTS_DIR)
    parser.add_argument("--repo-path", type=Path, default=bench.REPO_ROOT)
    parser.add_argument("--models", default="", help="Comma-separated label filter")
    args = parser.parse_args()

    args.results_dir.mkdir(parents=True, exist_ok=True)
    repo_mount = str(args.repo_path.resolve())
    selected = {label for label in (args.models.split(",") if args.models else []) if label}

    summaries: list[dict] = []
    for label, model, size_gb, provider in MEDIUM_MODELS:
        if selected and label not in selected:
            continue
        print(f"\n[medium] === {label} ({model}, {provider}, {size_gb} GB) ===", flush=True)
        summaries.append(
            run_one(label, model, size_gb, provider, results_dir=args.results_dir, repo_mount=repo_mount)
        )

    (args.results_dir / "all_summaries.json").write_text(json.dumps(summaries, indent=2))
    print(f"\n[medium] complete — {len(summaries)} models. Results at {args.results_dir}", flush=True)


if __name__ == "__main__":
    main()
