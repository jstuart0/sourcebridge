"""OpenRouter cloud models appended to the local sweep.

Runs claude-haiku-4.5, claude-sonnet-4, and llama-3.3-70b-instruct
against the same sourcebridge DEEP-from-understanding scenario so the
final article report can compare local vs cloud directly.

Results land in ``benchmark-results/local-sweep-v1/<label>/`` and the
existing ``analyze_local_sweep.py`` picks them up on the next pass.
"""

from __future__ import annotations

import argparse
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

DEFAULT_RESULTS_DIR = bench.REPO_ROOT / "benchmark-results" / "local-sweep-v1"

# (label, openrouter model id, nominal size for sort ordering)
CLOUD_MODELS: list[tuple[str, str, float]] = [
    ("claude-haiku-4.5", "anthropic/claude-haiku-4.5", 0.0),
    ("llama-3.3-70b-instruct", "meta-llama/llama-3.3-70b-instruct", 0.0),
    ("claude-sonnet-4", "anthropic/claude-sonnet-4", 0.0),
]


def start_worker_log_capture(label: str, model_dir: Path):
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


def run_one(label: str, model: str, *, api_key: str, results_dir: Path, repo_mount: str) -> dict:
    model_dir = results_dir / label
    model_dir.mkdir(parents=True, exist_ok=True)
    summary: dict = {
        "label": label,
        "model": model,
        "size_gb": 0.0,
        "status": "started",
        "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "provider": "openrouter",
    }

    # Restore openrouter-flavored override after any previous sweep run
    # may have monkey-patched make_override (the local sweep does this).
    import importlib

    importlib.reload(bench)
    log_proc, log_path = start_worker_log_capture(label, model_dir)
    run_started = time.time()
    scenario_result = None
    try:
        scenario_result = bench.benchmark_scenario(
            label,
            model,
            "deep_from_understanding",
            api_key,
            0,
            results_dir=model_dir,
            repo_mount=repo_mount,
            repo_name=f"sourcebridge-local-{label}",
            import_path="/bench/repo",
        )
    except Exception as exc:
        print(f"[cloud] {label}: FAILED -- {exc}", flush=True)
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
        summary["status"] = "ok" if scenario_result.passed_hard_gates else "partial"
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
    api_key = bench.decode_openrouter_key()
    selected = {label for label in (args.models.split(",") if args.models else []) if label}

    for label, model, _ in CLOUD_MODELS:
        if selected and label not in selected:
            continue
        print(f"\n[cloud] === {label} ({model}) ===", flush=True)
        run_one(label, model, api_key=api_key, results_dir=args.results_dir, repo_mount=repo_mount)

    print(f"\n[cloud] complete. Re-run analyzer to refresh REPORT.md", flush=True)


if __name__ == "__main__":
    main()
