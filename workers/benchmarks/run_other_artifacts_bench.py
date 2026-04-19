"""Benchmark the non-cliff-notes generative artifacts.

Generates Learning Path, Code Tour, and Workflow Story for the
SourceBridge repo using a single cloud model (claude-haiku-4.5 by
default — fast + cheap + high quality per the cliff-notes sweep).
Captures timing, section counts, confidence distribution, evidence
counts, and hallucinated file citations so we can measure the impact
of porting the cliff-notes quality work (null-int coerce, mechanical
confidence floor, structured-fact prompts) over to these artifacts.

Usage::

    uv run python benchmarks/run_other_artifacts_bench.py \\
        --label before \\
        --model anthropic/claude-haiku-4.5

Writes to ``benchmark-results/other-artifacts-<label>/`` with the same
per-artifact layout as the cliff-notes sweep so a post-run analyzer
can diff before/after cleanly.
"""

from __future__ import annotations

import argparse
import importlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from urllib import request as urlrequest

_THIS_DIR = Path(__file__).resolve().parent
if str(_THIS_DIR) not in sys.path:
    sys.path.insert(0, str(_THIS_DIR))

import run_deep_depth_bench as bench  # noqa: E402

ARTIFACT_TYPES: list[tuple[str, str]] = [
    ("learning_path", "LEARNING_PATH"),
    ("code_tour", "CODE_TOUR"),
    ("workflow_story", "WORKFLOW_STORY"),
]

DEFAULT_RESULTS_DIR = bench.REPO_ROOT / "benchmark-results"
IDENTIFIER_RE = re.compile(r"`([A-Za-z_][A-Za-z0-9_]{2,})`")


def _wrap_input(mutation_name: str) -> str:
    return (
        "mutation GenArtifact($repoId: ID!, $depth: KnowledgeDepth!, $audience: KnowledgeAudience!, $mode: KnowledgeGenerationMode!) { "
        + mutation_name
        + "(input: { repositoryId: $repoId, audience: $audience, depth: $depth, generationMode: $mode }) "
        + "{ id status type depth } }"
    )


MUTATIONS_VARS = {
    "learning_path": _wrap_input("generateLearningPath"),
    "code_tour": _wrap_input("generateCodeTour"),
    "workflow_story": _wrap_input("generateWorkflowStory"),
}


def run_mutation_with_vars(api_url: str, token: str, mutation: str, variables: dict) -> dict:
    import json as _json
    from urllib import request as _ur

    payload = _json.dumps({"query": mutation, "variables": variables}).encode()
    req = _ur.Request(
        f"{api_url}/api/v1/graphql",
        data=payload,
        headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
    )
    with _ur.urlopen(req, timeout=60) as resp:
        return _json.loads(resp.read().decode())


def latest_artifact_full(
    api_url: str, token: str, repo_id: str, artifact_type: str, depth_filter: str = ""
) -> dict | None:
    """Fetch the most recent artifact matching type (and optionally depth).

    IMPORTANT: ``seedRepositoryFieldGuide`` auto-creates MEDIUM artifacts for
    every type when a repo is added. A DEEP request creates a separate
    DEEP artifact. Without the depth filter, polling would pick whichever
    artifact completed first, which is usually the cheaper MEDIUM seed —
    hiding the real DEEP output we asked for.
    """

    query = (
        '{ knowledgeArtifacts(repositoryId: "'
        + repo_id
        + '") { id type depth status generatedAt errorCode errorMessage progressMessage sections { title content confidence inferred evidence { filePath lineStart lineEnd } } } }'
    )
    data = bench.graphql(api_url, token, query)
    artifacts = ((data.get("data") or {}).get("knowledgeArtifacts") or [])
    wanted_depth = depth_filter.upper() if depth_filter else ""
    candidates = [
        a
        for a in artifacts
        if a.get("type") == artifact_type
        and (not wanted_depth or (a.get("depth") or "").upper() == wanted_depth)
    ]
    candidates.sort(key=lambda item: item.get("generatedAt") or "", reverse=True)
    return candidates[0] if candidates else None


def wait_artifact_ready_by_type(
    api_url: str,
    token: str,
    repo_id: str,
    artifact_type: str,
    depth_filter: str = "",
    timeout_s: int = 1800,
) -> dict:
    started = time.time()
    while time.time() - started < timeout_s:
        artifact = latest_artifact_full(api_url, token, repo_id, artifact_type, depth_filter)
        if artifact:
            status = (artifact.get("status") or "").upper()
            if status == "READY":
                return artifact
            if status == "FAILED":
                err = artifact.get("errorCode") or artifact.get("errorMessage") or "failed"
                raise RuntimeError(f"{artifact_type} generation failed: {err}")
        time.sleep(5)
    raise RuntimeError(f"timed out waiting for {artifact_type}")


def index_real_files(repo_root: Path) -> tuple[set[str], set[str], set[str]]:
    """Walk the repo and collect (files, basenames, directories).

    A cited directory path (``internal/graph``) is still grounded — it
    points at a real part of the tree, just not at a single file.
    Flagging it as a hallucination overstates the real halluc rate,
    especially for learning paths where step-level references often
    name a package rather than a single file.
    """

    full_paths: set[str] = set()
    basenames: set[str] = set()
    directories: set[str] = set()
    for p in repo_root.rglob("*"):
        rel = p.relative_to(repo_root).as_posix()
        if rel.startswith(".git/") or rel.startswith("node_modules/") or rel.startswith("benchmark-results/"):
            continue
        if p.is_dir():
            directories.add(rel)
            continue
        full_paths.add(rel)
        basenames.add(p.name)
    return full_paths, basenames, directories


def score_artifact(
    artifact: dict,
    real_files: set[str],
    real_basenames: set[str],
    real_dirs: set[str] | None = None,
) -> dict:
    real_dirs = real_dirs or set()
    sections = artifact.get("sections") or []
    total_bytes = sum(len((s.get("content") or "")) for s in sections)
    confidences = [(s.get("confidence") or "").lower() for s in sections]
    high = sum(1 for c in confidences if c == "high")
    med = sum(1 for c in confidences if c == "medium")
    low = sum(1 for c in confidences if c == "low")

    total_evidence = 0
    unique_cited = 0
    halluc_cited = 0
    total_identifiers = 0
    per_section_details: list[dict] = []

    for s in sections:
        content = s.get("content") or ""
        evidence_entries = s.get("evidence") or []
        paths = {e.get("filePath", "").strip() for e in evidence_entries if e.get("filePath")}
        identifiers = {m.group(1) for m in IDENTIFIER_RE.finditer(content)}
        grounded = 0
        hallucinated_paths = []
        for p in paths:
            if not p:
                continue
            # Normalise trailing slashes — the LLM frequently cites
            # a package path like ``internal/api/graphql/`` which the
            # worker stores verbatim. Strip the trailing / so it can
            # match ``real_dirs``, which never has trailing slashes.
            stripped = p.rstrip("/")
            if (
                p in real_files
                or stripped in real_files
                or ("/" not in p and p in real_basenames)
                or p in real_dirs
                or stripped in real_dirs
            ):
                grounded += 1
            else:
                hallucinated_paths.append(p)
        total_evidence += len(evidence_entries)
        unique_cited += grounded
        halluc_cited += len(hallucinated_paths)
        total_identifiers += len(identifiers)
        per_section_details.append(
            {
                "title": s.get("title", ""),
                "confidence": (s.get("confidence") or "").lower(),
                "bytes": len(content),
                "evidence_entries": len(evidence_entries),
                "unique_files_grounded": grounded,
                "identifiers_backticked": len(identifiers),
                "hallucinated_citations": hallucinated_paths,
            }
        )

    cited_total = unique_cited + halluc_cited
    halluc_rate = round(halluc_cited / cited_total, 3) if cited_total else 0.0

    return {
        "section_count": len(sections),
        "total_bytes": total_bytes,
        "high": high,
        "medium": med,
        "low": low,
        "total_evidence": total_evidence,
        "unique_files_cited": unique_cited,
        "hallucinated_citations": halluc_cited,
        "hallucination_rate": halluc_rate,
        "total_identifiers": total_identifiers,
        "per_section": per_section_details,
    }


def make_override_openrouter(model: str, api_key: str, repo_mount: str) -> Path:
    handle = tempfile.NamedTemporaryFile("w", delete=False, suffix=".yml")
    handle.write(
        f"""
services:
  worker:
    environment:
      - SOURCEBRIDGE_WORKER_LLM_PROVIDER=openrouter
      - SOURCEBRIDGE_WORKER_LLM_BASE_URL=https://openrouter.ai/api/v1
      - SOURCEBRIDGE_WORKER_LLM_MODEL={model}
      - SOURCEBRIDGE_WORKER_LLM_API_KEY={api_key}
      - SOURCEBRIDGE_WORKER_LLM_DISABLE_THINKING=true
      - SOURCEBRIDGE_WORKER_EMBEDDING_PROVIDER=ollama
      - SOURCEBRIDGE_WORKER_EMBEDDING_BASE_URL=http://host.docker.internal:11434
  sourcebridge:
    volumes:
      - "{repo_mount}:/bench/repo:ro"
    environment:
      - SOURCEBRIDGE_LLM_PROVIDER=openrouter
      - SOURCEBRIDGE_LLM_BASE_URL=https://openrouter.ai/api/v1
      - SOURCEBRIDGE_LLM_MODEL={model}
      - SOURCEBRIDGE_LLM_SUMMARY_MODEL={model}
      - SOURCEBRIDGE_LLM_API_KEY={api_key}
      - SOURCEBRIDGE_LLM_DISABLE_THINKING=true
"""
    )
    handle.flush()
    handle.close()
    return Path(handle.name)


def run_bench(
    label: str,
    model: str,
    depth: str,
    results_root: Path,
    repo_mount: str,
    repo_name: str,
) -> None:
    results_dir = results_root / f"other-artifacts-{label}"
    results_dir.mkdir(parents=True, exist_ok=True)
    project = f"sb-other-{label.replace('.', '-').replace('_', '-')}"
    ports = bench.project_ports(project)
    api_url = f"http://localhost:{ports['api']}"

    api_key = bench.decode_openrouter_key()
    override = make_override_openrouter(model, api_key, repo_mount)
    summary_rows: list[dict] = []

    real_files, real_basenames, real_dirs = index_real_files(bench.REPO_ROOT)

    log_path = results_dir / "worker.log"
    log_proc = None
    try:
        print(f"[other] label={label} model={model} depth={depth} start", flush=True)
        bench.compose(project, override, ["down", "-v"])
        bench.compose_up_resilient(project, override)
        bench.wait_http(f"{api_url}/healthz")
        bench.wait_http(f"{api_url}/readyz")

        worker_cid_script = f"""
set -e
while : ; do
  CID=$(docker ps -q --filter name=sb-other-{label.replace('.', '-').replace('_', '-')}-worker | head -1)
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
        print(f"[other] indexed in {index_seconds}s", flush=True)

        understanding_started = time.time()
        bench.graphql(api_url, token, bench.mutation_build_repository_understanding(repo_id))
        und = bench.wait_understanding_ready(api_url, token, repo_id)
        understanding_seconds = int(time.time() - understanding_started)
        print(
            f"[other] understanding in {understanding_seconds}s "
            f"(nodes={und.get('totalNodes', 0)} cached={und.get('cachedNodes', 0)})",
            flush=True,
        )

        for artifact_key, artifact_type in ARTIFACT_TYPES:
            artifact_dir = results_dir / artifact_key
            artifact_dir.mkdir(exist_ok=True)
            started = time.time()
            mutation = MUTATIONS_VARS[artifact_key]
            variables = {
                "repoId": repo_id,
                "depth": depth,
                "audience": "DEVELOPER",
                "mode": "UNDERSTANDING_FIRST",
            }
            resp = run_mutation_with_vars(api_url, token, mutation, variables)
            print(f"[other] {artifact_key} mutation response: {json.dumps(resp)[:300]}", flush=True)
            try:
                artifact = wait_artifact_ready_by_type(
                    api_url, token, repo_id, artifact_type, depth_filter=depth
                )
                render_seconds = int(time.time() - started)
                metrics = score_artifact(artifact, real_files, real_basenames, real_dirs)
                row = {
                    "label": label,
                    "model": model,
                    "depth": depth,
                    "artifact": artifact_key,
                    "render_seconds": render_seconds,
                    "status": "ok",
                    "metrics": metrics,
                }
                (artifact_dir / "artifact.json").write_text(json.dumps(artifact, indent=2))
                (artifact_dir / "summary.json").write_text(json.dumps(row, indent=2))
                summary_rows.append(row)
                print(
                    f"[other] {artifact_key} in {render_seconds}s "
                    f"sections={metrics['section_count']} H/M/L={metrics['high']}/{metrics['medium']}/{metrics['low']} "
                    f"halluc={metrics['hallucination_rate']:.1%}",
                    flush=True,
                )
            except Exception as exc:
                render_seconds = int(time.time() - started)
                row = {
                    "label": label,
                    "model": model,
                    "depth": depth,
                    "artifact": artifact_key,
                    "render_seconds": render_seconds,
                    "status": "failed",
                    "error": str(exc),
                }
                (artifact_dir / "summary.json").write_text(json.dumps(row, indent=2))
                summary_rows.append(row)
                print(f"[other] {artifact_key} FAILED after {render_seconds}s: {exc}", flush=True)

        (results_dir / "all.json").write_text(
            json.dumps(
                {
                    "label": label,
                    "model": model,
                    "depth": depth,
                    "index_seconds": index_seconds,
                    "understanding_seconds": understanding_seconds,
                    "rows": summary_rows,
                },
                indent=2,
            )
        )
        print(f"[other] label={label} complete — results at {results_dir}", flush=True)
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
    parser.add_argument("--label", required=True, help="before | after | arbitrary tag")
    parser.add_argument("--model", default="anthropic/claude-haiku-4.5")
    parser.add_argument("--depth", default="DEEP")
    parser.add_argument("--results-root", type=Path, default=DEFAULT_RESULTS_DIR)
    parser.add_argument("--repo-path", type=Path, default=bench.REPO_ROOT)
    parser.add_argument("--repo-name", default="sourcebridge-local-other")
    args = parser.parse_args()

    if not shutil.which("docker"):
        raise SystemExit("docker is required")
    run_bench(
        args.label,
        args.model,
        args.depth,
        args.results_root.resolve(),
        str(args.repo_path.resolve()),
        args.repo_name,
    )


if __name__ == "__main__":
    main()
