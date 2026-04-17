"""Benchmark understanding-first cliff notes on selected OpenRouter models."""

from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import tempfile
import time
from dataclasses import asdict, dataclass
from pathlib import Path
from urllib import request as urlrequest

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_RESULTS_DIR = REPO_ROOT / "benchmark-results" / "deep-depth"
MODELS = [
    ("claude-sonnet-4", "anthropic/claude-sonnet-4"),
    ("claude-haiku-4.5", "anthropic/claude-haiku-4.5"),
    ("gemini-2.5-flash", "google/gemini-2.5-flash"),
    ("qwen2.5-32b", "Qwen/Qwen2.5-32B-Instruct"),
]
FORBIDDEN = [
    "various components",
    "the system handles",
    "as needed",
    "and more",
    "etc.",
    "several modules",
    "in some cases",
    "this functionality",
]
INVALID_EVIDENCE_PATHS = {"", "none", "null", "repository", "repo", "unknown"}


def valid_evidence_path(path: str | None) -> bool:
    candidate = (path or "").strip()
    if candidate.lower() in INVALID_EVIDENCE_PATHS:
        return False
    name = Path(candidate).name
    return "." in name and not name.endswith(".")


@dataclass
class ScenarioResult:
    label: str
    model: str
    scenario: str
    repo: str
    index_seconds: int
    understanding_seconds: int
    medium_seconds: int
    deep_seconds: int
    total_seconds: int
    section_count: int
    total_content_bytes: int
    avg_evidence_refs: float
    zero_evidence_sections: int
    forbidden_phrase_count: int
    low_confidence_sections: int
    passed_hard_gates: bool
    passed_soft_quality: bool
    artifact_path: str
    error: str = ""


def run(cmd: list[str], *, env: dict[str, str] | None = None, cwd: Path | None = None, capture: bool = False) -> str:
    proc = subprocess.run(
        cmd,
        cwd=str(cwd or REPO_ROOT),
        env=env,
        check=True,
        text=True,
        capture_output=capture,
    )
    return proc.stdout if capture else ""


def graphql(api_url: str, token: str, query: str) -> dict:
    payload = json.dumps({"query": query}).encode()
    req = urlrequest.Request(
        f"{api_url}/api/v1/graphql",
        data=payload,
        headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
    )
    with urlrequest.urlopen(req, timeout=60) as resp:
        return json.loads(resp.read().decode())


def post_json(url: str, payload: dict) -> dict:
    req = urlrequest.Request(url, data=json.dumps(payload).encode(), headers={"Content-Type": "application/json"})
    with urlrequest.urlopen(req, timeout=60) as resp:
        return json.loads(resp.read().decode())


def wait_http(url: str, timeout_s: int = 180) -> None:
    started = time.time()
    while time.time() - started < timeout_s:
        try:
            with urlrequest.urlopen(url, timeout=5) as resp:
                if resp.status == 200:
                    return
        except Exception:
            time.sleep(2)
    raise RuntimeError(f"timed out waiting for {url}")


def wait_repo_ready(api_url: str, token: str, repo_id: str, timeout_s: int = 1800) -> None:
    started = time.time()
    last_status = ""
    while time.time() - started < timeout_s:
        data = graphql(api_url, token, f'{{ repository(id: "{repo_id}") {{ status }} }}')
        status = (((data.get("data") or {}).get("repository") or {}).get("status") or "").upper()
        if status == "READY":
            return
        if status == "ERROR":
            raise RuntimeError("repository indexing failed")
        if status and status != last_status:
            print(f"[bench] repository status={status}", flush=True)
            last_status = status
        time.sleep(5)
    raise RuntimeError("timed out waiting for repository ready")


def mutation_generate_cliff_notes(repo_id: str, depth: str) -> str:
    return (
        'mutation { generateCliffNotes(input: { repositoryId: "'
        + repo_id
        + '", audience: DEVELOPER, depth: '
        + depth
        + ', generationMode: UNDERSTANDING_FIRST, scopeType: REPOSITORY }) { id status } }'
    )


def mutation_build_repository_understanding(repo_id: str) -> str:
    return (
        'mutation { buildRepositoryUnderstanding(input: { repositoryId: "'
        + repo_id
        + '", scopeType: REPOSITORY }) { id stage treeStatus revisionFp modelUsed } }'
    )


def latest_understanding(api_url: str, token: str, repo_id: str) -> dict | None:
    query = (
        '{ repositoryUnderstanding(repositoryId: "'
        + repo_id
        + '", scopeType: REPOSITORY) { id stage treeStatus revisionFp modelUsed cachedNodes totalNodes updatedAt } }'
    )
    data = graphql(api_url, token, query)
    return ((data.get("data") or {}).get("repositoryUnderstanding") or None)


def wait_understanding_ready(api_url: str, token: str, repo_id: str, timeout_s: int = 7200) -> dict:
    started = time.time()
    while time.time() - started < timeout_s:
        understanding = latest_understanding(api_url, token, repo_id)
        if understanding:
            stage = (understanding.get("stage") or "").upper()
            tree_status = (understanding.get("treeStatus") or "").upper()
            if stage == "READY" and tree_status == "COMPLETE":
                return understanding
            if stage == "FAILED":
                raise RuntimeError("repository understanding failed")
        time.sleep(10)
    raise RuntimeError("timed out waiting for repository understanding")


def latest_artifact(api_url: str, token: str, repo_id: str, depth: str) -> dict | None:
    query = (
        '{ knowledgeArtifacts(repositoryId: "'
        + repo_id
        + '") { id type depth status generatedAt errorCode errorMessage progressMessage sections { title content confidence evidence { filePath lineStart lineEnd } } } }'
    )
    data = graphql(api_url, token, query)
    artifacts = ((data.get("data") or {}).get("knowledgeArtifacts") or [])
    candidates = [a for a in artifacts if a.get("type") == "CLIFF_NOTES" and a.get("depth") == depth]
    candidates.sort(key=lambda item: item.get("generatedAt") or "", reverse=True)
    return candidates[0] if candidates else None


def wait_artifact_ready(api_url: str, token: str, repo_id: str, depth: str, timeout_s: int = 3600) -> dict:
    started = time.time()
    while time.time() - started < timeout_s:
        artifact = latest_artifact(api_url, token, repo_id, depth)
        if artifact:
            status = (artifact.get("status") or "").upper()
            if status == "READY":
                return artifact
            if status == "FAILED":
                error_code = (artifact.get("errorCode") or "").strip()
                error_message = (artifact.get("errorMessage") or artifact.get("progressMessage") or "").strip()
                detail = " ".join(part for part in [error_code, error_message] if part).strip()
                if detail:
                    raise RuntimeError(f"{depth} cliff notes failed: {detail}")
                raise RuntimeError(f"{depth} cliff notes failed")
        time.sleep(10)
    raise RuntimeError(f"timed out waiting for {depth} cliff notes")


def score_artifact(artifact: dict) -> tuple[int, int, float, int, int, int, bool, bool]:
    sections = artifact.get("sections") or []
    section_count = len(sections)
    content_bytes = sum(len((sec.get("content") or "").encode()) for sec in sections)
    evidence_counts = [
        sum(1 for ev in (sec.get("evidence") or []) if valid_evidence_path(ev.get("filePath")))
        for sec in sections
    ]
    avg_evidence = round(sum(evidence_counts) / max(len(evidence_counts), 1), 2)
    zero_evidence = sum(1 for count in evidence_counts if count == 0)
    low_confidence = sum(1 for sec in sections if (sec.get("confidence") or "").lower() == "low")
    full_text = "\n".join((sec.get("content") or "") for sec in sections).lower()
    forbidden_count = sum(full_text.count(phrase) for phrase in FORBIDDEN)
    passed_hard = section_count >= 14 and zero_evidence == 0
    passed_soft = content_bytes >= 20000 and avg_evidence >= 3.0 and forbidden_count <= 3
    return section_count, content_bytes, avg_evidence, zero_evidence, forbidden_count, low_confidence, passed_hard, passed_soft


def openrouter_key() -> str:
    if os.environ.get("OPENROUTER_API_KEY"):
        return os.environ["OPENROUTER_API_KEY"]
    return run(
        [
            "kubectl",
            "-n",
            "automation",
            "get",
            "secret",
            "openrouter-api-credentials",
            "-o",
            "jsonpath={.data.api-key}",
        ],
        capture=True,
    ).strip()


def decode_openrouter_key() -> str:
    raw = openrouter_key()
    if raw.startswith("sk-or-"):
        return raw.strip()
    import base64

    return base64.b64decode(raw).decode().strip()


def make_override(model: str, api_key: str, repo_mount: str) -> Path:
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
"""
    )
    handle.flush()
    handle.close()
    return Path(handle.name)


def compose(project: str, override: Path, args: list[str]) -> None:
    env = os.environ.copy()
    env.setdefault("SOURCEBRIDGE_API_PORT", project_ports(project)["api"])
    env.setdefault("SOURCEBRIDGE_SURREALDB_PORT", project_ports(project)["surreal"])
    env.setdefault("SOURCEBRIDGE_WORKER_PORT", project_ports(project)["worker"])
    env.setdefault("SOURCEBRIDGE_WEB_PORT", project_ports(project)["web"])
    run(["docker", "compose", "-p", project, "-f", "docker-compose.yml", "-f", str(override), *args], env=env)


def compose_output(project: str, override: Path, args: list[str]) -> str:
    env = os.environ.copy()
    env.setdefault("SOURCEBRIDGE_API_PORT", project_ports(project)["api"])
    env.setdefault("SOURCEBRIDGE_SURREALDB_PORT", project_ports(project)["surreal"])
    env.setdefault("SOURCEBRIDGE_WORKER_PORT", project_ports(project)["worker"])
    env.setdefault("SOURCEBRIDGE_WEB_PORT", project_ports(project)["web"])
    return run(
        ["docker", "compose", "-p", project, "-f", "docker-compose.yml", "-f", str(override), *args],
        env=env,
        capture=True,
    )


def compose_service_container_id(project: str, override: Path, service: str) -> str:
    return compose_output(project, override, ["ps", "-q", service]).strip()


def wait_container_healthy(container_id: str, timeout_s: int = 180) -> None:
    started = time.time()
    while time.time() - started < timeout_s:
        if not container_id:
            time.sleep(2)
            continue
        status = run(
            ["docker", "inspect", "-f", "{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}", container_id],
            capture=True,
        ).strip()
        if status == "healthy":
            return
        if status in {"exited", "dead"}:
            raise RuntimeError(f"container {container_id} exited before becoming healthy")
        time.sleep(2)
    raise RuntimeError(f"timed out waiting for container {container_id} healthy")


def compose_up_resilient(project: str, override: Path) -> None:
    attempts = 3
    last_error: Exception | None = None
    for attempt in range(1, attempts + 1):
        try:
            compose(project, override, ["up", "-d", "--build", "surrealdb"])
            wait_container_healthy(compose_service_container_id(project, override, "surrealdb"), timeout_s=180)
            compose(project, override, ["up", "-d", "--build", "worker", "sourcebridge"])
            return
        except Exception as exc:
            last_error = exc
            if attempt >= attempts:
                raise
            print(f"[bench] compose startup retry {attempt}/{attempts - 1} after startup failure: {exc}", flush=True)
            compose(project, override, ["down", "-v"])
            time.sleep(3)
    if last_error is not None:
        raise last_error


def project_ports(project: str) -> dict[str, str]:
    base = 8200 + (sum(ord(ch) for ch in project) % 200)
    return {
        "api": str(base),
        "surreal": str(base + 1000),
        "worker": str(base + 2000),
        "web": str(base + 3000),
    }


def setup_auth(api_url: str) -> str:
    for _ in range(15):
        try:
            payload = post_json(f"{api_url}/auth/setup", {"password": "benchmark1"})
            token = payload.get("token")
            if token:
                return token
        except Exception as exc:
            if "409" in str(exc):
                payload = post_json(f"{api_url}/auth/login", {"password": "benchmark1"})
                token = payload.get("token")
                if token:
                    return token
            time.sleep(2)
    raise RuntimeError("failed to set up auth")


def add_local_repo(api_url: str, token: str, repo_name: str, import_path: str) -> str:
    data = graphql(
        api_url,
        token,
        'mutation { addRepository(input: { name: "'
        + repo_name
        + '", path: "'
        + import_path
        + '" }) { id } }',
    )
    repo_id = (((data.get("data") or {}).get("addRepository") or {}).get("id") or "").strip()
    if not repo_id:
        raise RuntimeError(f"failed to add repository: {data}")
    return repo_id


def benchmark_scenario(
    model_label: str,
    model: str,
    scenario: str,
    api_key: str,
    _port_seed: int,
    *,
    results_dir: Path,
    repo_mount: str,
    repo_name: str,
    import_path: str,
) -> ScenarioResult:
    canonical_scenario = {
        "direct_deep": "deep_from_understanding",
        "medium_then_deep": "medium_then_deep_from_understanding",
    }.get(scenario, scenario)
    project = f"sb-deep-{model_label.replace('.', '-').replace('_', '-')}-{scenario}"
    ports = project_ports(project)
    api_url = f"http://localhost:{ports['api']}"
    override = make_override(model, api_key, repo_mount)
    index_seconds = 0
    understanding_seconds = 0
    medium_seconds = 0
    deep_seconds = 0
    repo_id = ""
    try:
        print(f"[bench] model={model_label} scenario={canonical_scenario} repo={repo_name} start", flush=True)
        compose(project, override, ["down", "-v"])
        compose_up_resilient(project, override)
        wait_http(f"{api_url}/healthz")
        wait_http(f"{api_url}/readyz")
        token = setup_auth(api_url)
        index_started = time.time()
        repo_id = add_local_repo(api_url, token, repo_name, import_path)
        wait_repo_ready(api_url, token, repo_id)
        index_seconds = int(time.time() - index_started)
        print(f"[bench] model={model_label} scenario={canonical_scenario} indexed in {index_seconds}s", flush=True)

        understanding_started = time.time()
        graphql(api_url, token, mutation_build_repository_understanding(repo_id))
        understanding = wait_understanding_ready(api_url, token, repo_id)
        understanding_seconds = int(time.time() - understanding_started)
        print(
            "[bench] model="
            f"{model_label} scenario={canonical_scenario} understanding in {understanding_seconds}s "
            f"(nodes={understanding.get('totalNodes', 0)} cached={understanding.get('cachedNodes', 0)})",
            flush=True,
        )

        if canonical_scenario == "medium_then_deep_from_understanding":
            started = time.time()
            graphql(api_url, token, mutation_generate_cliff_notes(repo_id, "MEDIUM"))
            wait_artifact_ready(api_url, token, repo_id, "MEDIUM")
            medium_seconds = int(time.time() - started)
            print(f"[bench] model={model_label} scenario={canonical_scenario} medium in {medium_seconds}s", flush=True)

        deep_started = time.time()
        graphql(api_url, token, mutation_generate_cliff_notes(repo_id, "DEEP"))
        deep_artifact = wait_artifact_ready(api_url, token, repo_id, "DEEP")
        deep_seconds = int(time.time() - deep_started)
        print(f"[bench] model={model_label} scenario={canonical_scenario} deep in {deep_seconds}s", flush=True)
        total_seconds = index_seconds + understanding_seconds + medium_seconds + deep_seconds
        (
            section_count,
            content_bytes,
            avg_evidence,
            zero_evidence,
            forbidden_count,
            low_confidence,
            passed_hard,
            passed_soft,
        ) = score_artifact(deep_artifact)
        result = ScenarioResult(
            label=model_label,
            model=model,
            scenario=canonical_scenario,
            repo=repo_name,
            index_seconds=index_seconds,
            understanding_seconds=understanding_seconds,
            medium_seconds=medium_seconds,
            deep_seconds=deep_seconds,
            total_seconds=total_seconds,
            section_count=section_count,
            total_content_bytes=content_bytes,
            avg_evidence_refs=avg_evidence,
            zero_evidence_sections=zero_evidence,
            forbidden_phrase_count=forbidden_count,
            low_confidence_sections=low_confidence,
            passed_hard_gates=passed_hard,
            passed_soft_quality=passed_soft,
            artifact_path="",
        )
        return persist_artifact(results_dir, result, deep_artifact)
    except Exception as exc:
        artifact = latest_artifact(api_url, token, repo_id, "DEEP") if repo_id else None
        error = str(exc)
        if artifact and not error:
            error = (
                artifact.get("errorMessage")
                or artifact.get("progressMessage")
                or artifact.get("errorCode")
                or error
            )
        partial = ScenarioResult(
            label=model_label,
            model=model,
            scenario=canonical_scenario,
            repo=repo_name,
            index_seconds=index_seconds,
            understanding_seconds=understanding_seconds,
            medium_seconds=medium_seconds,
            deep_seconds=deep_seconds,
            total_seconds=index_seconds + understanding_seconds + medium_seconds + deep_seconds,
            section_count=0,
            total_content_bytes=0,
            avg_evidence_refs=0.0,
            zero_evidence_sections=0,
            forbidden_phrase_count=0,
            low_confidence_sections=0,
            passed_hard_gates=False,
            passed_soft_quality=False,
            artifact_path="",
            error=error,
        )
        if artifact:
            return persist_artifact(results_dir, partial, artifact)
        return partial
    finally:
        try:
            compose(project, override, ["down", "-v"])
        finally:
            override.unlink(missing_ok=True)


def write_report(results_dir: Path, results: list[ScenarioResult]) -> None:
    results_dir.mkdir(parents=True, exist_ok=True)
    payload = {"results": [asdict(result) for result in results], "generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}
    (results_dir / "results.json").write_text(json.dumps(payload, indent=2))
    lines = [
        "# Understanding-First DEEP Benchmark",
        "",
        "| Model | Repo | Scenario | Index s | Understanding s | Medium s | Deep s | Sections | Bytes | Avg refs | Zero-evidence | Forbidden | Hard | Soft |",
        "|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|---|",
    ]
    for result in results:
        lines.append(
            f"| {result.label} | {result.repo} | {result.scenario} | {result.index_seconds} | {result.understanding_seconds} | {result.medium_seconds} | {result.deep_seconds} | "
            f"{result.section_count} | {result.total_content_bytes} | {result.avg_evidence_refs} | {result.zero_evidence_sections} | "
            f"{result.forbidden_phrase_count} | {'pass' if result.passed_hard_gates else 'fail'} | {'pass' if result.passed_soft_quality else 'fail'} |"
        )
    (results_dir / "report.md").write_text("\n".join(lines) + "\n")


def persist_artifact(results_dir: Path, result: ScenarioResult, artifact: dict) -> ScenarioResult:
    artifact_dir = results_dir / "artifacts"
    artifact_dir.mkdir(parents=True, exist_ok=True)
    artifact_path = artifact_dir / f"{result.label}-{result.scenario}.json"
    artifact_path.write_text(json.dumps(artifact, indent=2))
    result.artifact_path = str(artifact_path.relative_to(results_dir))
    return result


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--results-dir", type=Path, default=DEFAULT_RESULTS_DIR)
    parser.add_argument("--model-label", action="append", default=[])
    parser.add_argument("--scenario", action="append", default=[])
    parser.add_argument("--repo-path", type=Path, default=REPO_ROOT)
    parser.add_argument("--repo-name", default="sourcebridge-local")
    args = parser.parse_args()
    if not shutil.which("docker"):
        raise SystemExit("docker is required")
    if not shutil.which("kubectl") and not os.environ.get("OPENROUTER_API_KEY"):
        raise SystemExit("OPENROUTER_API_KEY or kubectl access is required")
    api_key = decode_openrouter_key()
    repo_mount = str(args.repo_path.resolve())
    repo_name = args.repo_name
    import_path = "/bench/repo"
    results: list[ScenarioResult] = []
    selected_models = [(label, model) for label, model in MODELS if not args.model_label or label in args.model_label]
    selected_scenarios = args.scenario or ["deep_from_understanding", "medium_then_deep_from_understanding"]
    for idx, (label, model) in enumerate(selected_models):
        for scenario_offset, scenario in enumerate(selected_scenarios):
            result = benchmark_scenario(
                label,
                model,
                scenario,
                api_key,
                idx * 10 + scenario_offset,
                results_dir=args.results_dir,
                repo_mount=repo_mount,
                repo_name=repo_name,
                import_path=import_path,
            )
            results.append(result)
            write_report(args.results_dir, results)


if __name__ == "__main__":
    main()
