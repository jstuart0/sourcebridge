"""Parallel variant of run_other_artifacts_bench.

Dispatches learning_path, code_tour, and workflow_story mutations
concurrently against a single compose stack, then polls them in
parallel. For local Ollama models (qwen3:32b, qwen3.6 MoE) this
leans on Ollama 0.21+'s continuous-batching so 3 simultaneous DEEP
prompts share prefill work and total wall-clock drops 1.5–2x vs. the
serial harness.

Usage::

    OLLAMA_NUM_PARALLEL=3 uv run python benchmarks/run_other_artifacts_parallel.py \\
        --label qwen3-32b-parallel \\
        --model qwen3:32b \\
        --provider ollama \\
        --depth DEEP

The ``OLLAMA_NUM_PARALLEL`` env var is read server-side on the Mac
Studio, not by this harness — set it there via ``launchctl setenv``
or in the ``ollama serve`` wrapper. With it unset the harness still
runs, it just serializes server-side.
"""

from __future__ import annotations

import argparse
import concurrent.futures
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

DEFAULT_RESULTS_DIR = bench.REPO_ROOT / "benchmark-results"
OLLAMA_URL = "http://192.168.10.108:11434/v1"

ARTIFACT_TYPES = other.ARTIFACT_TYPES
MUTATIONS_VARS = other.MUTATIONS_VARS


def _make_ollama_override(model: str, _api_key: str, repo_mount: str) -> Path:
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


def _dispatch_and_wait(
    artifact_key: str,
    artifact_type: str,
    api_url: str,
    token: str,
    repo_id: str,
    depth: str,
    real_files: set,
    real_basenames: set,
    real_dirs: set,
    artifact_dir: Path,
    artifact_timeout_s: int,
) -> dict:
    """Dispatch one mutation, poll to ready, score. Runs in a worker thread."""
    mutation = MUTATIONS_VARS[artifact_key]
    variables = {
        "repoId": repo_id,
        "depth": depth,
        "audience": "DEVELOPER",
        "mode": "UNDERSTANDING_FIRST",
    }
    started = time.time()
    resp = other.run_mutation_with_vars(api_url, token, mutation, variables)
    print(f"[par] {artifact_key} dispatched: {json.dumps(resp)[:200]}", flush=True)
    try:
        artifact = other.wait_artifact_ready_by_type(
            api_url,
            token,
            repo_id,
            artifact_type,
            depth_filter=depth,
            timeout_s=artifact_timeout_s,
        )
        render_seconds = int(time.time() - started)
        metrics = other.score_artifact(artifact, real_files, real_basenames, real_dirs)
        row = {
            "artifact": artifact_key,
            "render_seconds": render_seconds,
            "status": "ok",
            "metrics": metrics,
        }
        (artifact_dir / "artifact.json").write_text(json.dumps(artifact, indent=2))
        print(
            f"[par] {artifact_key} in {render_seconds}s "
            f"sections={metrics['section_count']} "
            f"H/M/L={metrics['high']}/{metrics['medium']}/{metrics['low']} "
            f"halluc={metrics['hallucination_rate']:.1%}",
            flush=True,
        )
    except Exception as exc:
        render_seconds = int(time.time() - started)
        row = {
            "artifact": artifact_key,
            "render_seconds": render_seconds,
            "status": "failed",
            "error": str(exc),
        }
        print(f"[par] {artifact_key} FAILED after {render_seconds}s: {exc}", flush=True)
    (artifact_dir / "summary.json").write_text(json.dumps(row, indent=2))
    return row


def run_parallel(
    label: str,
    model: str,
    provider: str,
    depth: str,
    results_root: Path,
    repo_mount: str,
    repo_name: str,
    artifact_timeout_s: int,
) -> None:
    results_dir = results_root / f"other-artifacts-{label}"
    results_dir.mkdir(parents=True, exist_ok=True)
    project = f"sb-par-{label.replace('.', '-').replace('_', '-')}"
    ports = bench.project_ports(project)
    api_url = f"http://localhost:{ports['api']}"

    if provider == "ollama":
        api_key = "ollama"
        override = _make_ollama_override(model, api_key, repo_mount)
    else:
        api_key = bench.decode_openrouter_key()
        override = other.make_override_openrouter(model, api_key, repo_mount)

    real_files, real_basenames, real_dirs = other.index_real_files(bench.REPO_ROOT)

    log_path = results_dir / "worker.log"
    log_proc = None
    try:
        print(f"[par] label={label} model={model} depth={depth} start", flush=True)
        bench.compose(project, override, ["down", "-v"])
        bench.compose_up_resilient(project, override)
        bench.wait_http(f"{api_url}/healthz")
        bench.wait_http(f"{api_url}/readyz")

        worker_cid_script = f"""
set -e
while : ; do
  CID=$(docker ps -q --filter name={project}-worker | head -1)
  if [ -n "$CID" ]; then
    docker logs -f "$CID" >>{log_path} 2>&1
    break
  fi
  sleep 2
done
"""
        log_proc = subprocess.Popen(["bash", "-c", worker_cid_script])

        token = bench.setup_auth(api_url)
        index_started = time.time()
        repo_id = bench.add_local_repo(api_url, token, f"{repo_name}-{label}", "/bench/repo")
        bench.wait_repo_ready(api_url, token, repo_id)
        index_seconds = int(time.time() - index_started)
        print(f"[par] indexed in {index_seconds}s", flush=True)

        understanding_started = time.time()
        bench.graphql(api_url, token, bench.mutation_build_repository_understanding(repo_id))
        und = bench.wait_understanding_ready(api_url, token, repo_id)
        understanding_seconds = int(time.time() - understanding_started)
        print(
            f"[par] understanding in {understanding_seconds}s "
            f"(nodes={und.get('totalNodes', 0)} cached={und.get('cachedNodes', 0)})",
            flush=True,
        )

        # Prepare per-artifact directories so the threaded workers can
        # write into non-overlapping paths without coordination.
        artifact_dirs = {
            key: results_dir / key for key, _ in ARTIFACT_TYPES
        }
        for d in artifact_dirs.values():
            d.mkdir(exist_ok=True)

        parallel_started = time.time()
        with concurrent.futures.ThreadPoolExecutor(max_workers=len(ARTIFACT_TYPES)) as pool:
            futures = {
                pool.submit(
                    _dispatch_and_wait,
                    key,
                    artifact_type,
                    api_url,
                    token,
                    repo_id,
                    depth,
                    real_files,
                    real_basenames,
                    real_dirs,
                    artifact_dirs[key],
                    artifact_timeout_s,
                ): key
                for key, artifact_type in ARTIFACT_TYPES
            }
            rows = [f.result() for f in concurrent.futures.as_completed(futures)]
        parallel_seconds = int(time.time() - parallel_started)
        print(f"[par] all 3 artifacts complete in {parallel_seconds}s wall-clock", flush=True)

        (results_dir / "all.json").write_text(
            json.dumps(
                {
                    "label": label,
                    "model": model,
                    "depth": depth,
                    "index_seconds": index_seconds,
                    "understanding_seconds": understanding_seconds,
                    "parallel_wall_seconds": parallel_seconds,
                    "rows": rows,
                },
                indent=2,
            )
        )
        print(f"[par] label={label} complete — results at {results_dir}", flush=True)
    finally:
        if log_proc is not None:
            try:
                log_proc.terminate()
                log_proc.wait(timeout=5)
            except Exception:
                try:
                    log_proc.kill()
                except Exception:
                    pass
        try:
            bench.compose(project, override, ["down", "-v"])
        finally:
            override.unlink(missing_ok=True)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--label", required=True)
    parser.add_argument("--model", required=True, help="qwen3:32b, qwen3.6:35b-a3b-q4_K_M, anthropic/claude-haiku-4.5, ...")
    parser.add_argument("--provider", choices=["ollama", "openrouter"], default="ollama")
    parser.add_argument("--depth", default="DEEP")
    parser.add_argument("--results-root", type=Path, default=DEFAULT_RESULTS_DIR)
    parser.add_argument("--repo-path", type=Path, default=bench.REPO_ROOT)
    parser.add_argument("--repo-name", default="sourcebridge-parallel")
    parser.add_argument(
        "--artifact-timeout-s",
        type=int,
        default=3600,
        help="Per-artifact polling ceiling in seconds",
    )
    args = parser.parse_args()

    if args.provider == "ollama" and not sweep.probe_model_ready(OLLAMA_URL, args.model):
        raise SystemExit(f"model {args.model} not pulled on {OLLAMA_URL}")

    importlib.reload(bench)
    importlib.reload(other)
    run_parallel(
        label=args.label,
        model=args.model,
        provider=args.provider,
        depth=args.depth,
        results_root=args.results_root.resolve(),
        repo_mount=str(args.repo_path.resolve()),
        repo_name=args.repo_name,
        artifact_timeout_s=args.artifact_timeout_s,
    )


if __name__ == "__main__":
    main()
