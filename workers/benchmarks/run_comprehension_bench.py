"""Run OSS-safe comprehension benchmarks against fixture and fake-provider cases."""

from __future__ import annotations

import argparse
import asyncio
import json
import time
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any

import yaml

from workers.common.config import WorkerConfig
from workers.common.llm.config import create_llm_provider
from workers.common.llm.fake import FakeLLMProvider
from workers.knowledge.cliff_notes import generate_cliff_notes
from workers.knowledge.code_tour import generate_code_tour
from workers.knowledge.learning_path import generate_learning_path
from workers.knowledge.prompts.cliff_notes import REQUIRED_SECTIONS
from workers.knowledge.workflow_story import generate_workflow_story

REPO_ROOT = Path(__file__).resolve().parents[2]
MANIFEST_PATH = REPO_ROOT / "benchmarks" / "comprehension" / "manifest.yaml"
RESULTS_ROOT = REPO_ROOT / "benchmarks" / "results"

FIXTURE_SNAPSHOT = {
    "repository_id": "fixture-multi-lang-repo",
    "repository_name": "multi-lang-repo",
    "file_count": 7,
    "symbol_count": 8,
    "test_count": 2,
    "languages": [
        {"language": "go", "file_count": 2, "line_count": 90},
        {"language": "python", "file_count": 2, "line_count": 80},
        {"language": "typescript", "file_count": 2, "line_count": 95},
        {"language": "java", "file_count": 1, "line_count": 40},
        {"language": "rust", "file_count": 1, "line_count": 35},
    ],
    "modules": [
        {"name": "go", "path": "go", "file_count": 2},
        {"name": "python", "path": "python", "file_count": 2},
        {"name": "typescript", "path": "typescript", "file_count": 2},
    ],
    "entry_points": [
        {
            "id": "sym-go-main",
            "name": "main",
            "kind": "function",
            "file_path": "go/main.go",
            "start_line": 1,
            "end_line": 20,
        },
        {
            "id": "sym-py-auth",
            "name": "authenticate",
            "kind": "function",
            "file_path": "python/auth.py",
            "start_line": 1,
            "end_line": 40,
        },
    ],
    "public_api": [],
    "complex_symbols": [],
    "high_fan_out": [],
    "high_fan_in": [],
    "test_symbols": [
        {
            "id": "sym-ts-test",
            "name": "api.test",
            "kind": "test",
            "file_path": "typescript/tests/api.test.ts",
            "start_line": 1,
            "end_line": 20,
        }
    ],
    "requirements": [],
    "links": [],
    "docs": [
        {"path": "requirements.md", "title": "Requirements"},
    ],
    "scope_context": {
        "focus_summary": "The login and request-handling path starts in the API surface and fans into language-specific helpers."  # noqa: E501
    },
    "source_revision": {
        "commit_sha": "",
        "branch": "benchmark",
        "content_fingerprint": "fixture-multi-lang-repo-v1",
        "docs_fingerprint": "fixture-multi-lang-repo-docs-v1",
    },
}


@dataclass
class BenchmarkResult:
    case_id: str
    corpus_id: str
    artifact_type: str
    provider_mode: str
    provider_name: str
    model_id: str
    success: bool
    duration_ms: int
    input_tokens: int
    output_tokens: int
    error: str | None
    checks: dict[str, bool]
    metrics: dict[str, int]


def _load_manifest(path: Path = MANIFEST_PATH) -> list[dict[str, Any]]:
    payload = yaml.safe_load(path.read_text())
    cases = payload.get("cases", [])
    if not isinstance(cases, list):
        raise ValueError("manifest cases must be a list")
    return cases


def _effective_provider_mode(case: dict[str, Any], override: str | None) -> str:
    if override is None or override == "manifest":
        return str(case["provider_mode"])
    return override


def _create_provider(provider_mode: str) -> tuple[Any, str, str]:
    if provider_mode == "fake":
        provider = FakeLLMProvider()
        return provider, "fake", provider.default_model
    if provider_mode == "live":
        config = WorkerConfig()
        provider = create_llm_provider(config)
        provider_name = getattr(provider, "provider_name", None) or config.llm_provider
        model_id = getattr(provider, "default_model", None) or config.llm_model
        return provider, provider_name, model_id
    raise ValueError(f"unsupported provider mode: {provider_mode}")


def _sanitize_live_error(exc: Exception) -> str:
    return f"{type(exc).__name__}: live provider run failed"


def _snapshot_json_for_corpus(corpus_id: str) -> str:
    if corpus_id != "multi-lang-repo-fixture":
        raise ValueError(f"unsupported corpus id: {corpus_id}")
    return json.dumps(FIXTURE_SNAPSHOT)


def _check_cliff_notes_required_sections(result: Any) -> bool:
    titles = {section.title for section in result.sections}
    return all(required in titles for required in REQUIRED_SECTIONS)


def _check_cliff_notes_has_evidence(result: Any) -> bool:
    return any(section.evidence for section in result.sections)


def _check_learning_path_non_empty(result: Any) -> bool:
    return bool(result.steps) and all(step.title and step.content for step in result.steps)


def _check_code_tour_non_empty(result: Any) -> bool:
    return bool(result.stops) and all(stop.title and stop.file_path for stop in result.stops)


def _check_workflow_story_non_empty(result: Any) -> bool:
    return bool(result.sections) and any(section.content for section in result.sections)


CHECKS = {
    "cliff_notes_required_sections": _check_cliff_notes_required_sections,
    "cliff_notes_has_evidence": _check_cliff_notes_has_evidence,
    "learning_path_non_empty": _check_learning_path_non_empty,
    "code_tour_non_empty": _check_code_tour_non_empty,
    "workflow_story_non_empty": _check_workflow_story_non_empty,
}


async def _run_case(case: dict[str, Any], provider_mode_override: str | None = None) -> BenchmarkResult:
    provider_mode = _effective_provider_mode(case, provider_mode_override)
    provider, provider_name, model_id = _create_provider(provider_mode)
    snapshot_json = _snapshot_json_for_corpus(case["corpus_id"])
    artifact_type = case["artifact_type"]

    started = time.perf_counter()
    try:
        if artifact_type == "cliff_notes":
            result, usage = await generate_cliff_notes(
                provider=provider,
                repository_name=case["repository_name"],
                audience=case["audience"],
                depth=case["depth"],
                snapshot_json=snapshot_json,
                scope_type=case.get("scope_type", "repository"),
                scope_path=case.get("scope_path", ""),
            )
            metrics = {
                "section_count": len(result.sections),
                "evidence_count": sum(len(section.evidence) for section in result.sections),
            }
        elif artifact_type == "learning_path":
            result, usage = await generate_learning_path(
                provider=provider,
                repository_name=case["repository_name"],
                audience=case["audience"],
                depth=case["depth"],
                snapshot_json=snapshot_json,
            )
            metrics = {"step_count": len(result.steps)}
        elif artifact_type == "code_tour":
            result, usage = await generate_code_tour(
                provider=provider,
                repository_name=case["repository_name"],
                audience=case["audience"],
                depth=case["depth"],
                snapshot_json=snapshot_json,
            )
            metrics = {"stop_count": len(result.stops)}
        elif artifact_type == "workflow_story":
            result, usage = await generate_workflow_story(
                provider=provider,
                repository_name=case["repository_name"],
                audience=case["audience"],
                depth=case["depth"],
                snapshot_json=snapshot_json,
                scope_type=case.get("scope_type", "repository"),
                scope_path=case.get("scope_path", ""),
                anchor_label=case.get("anchor_label", ""),
            )
            metrics = {
                "section_count": len(result.sections),
                "evidence_count": sum(len(section.evidence) for section in result.sections),
            }
        else:
            raise ValueError(f"unsupported artifact type: {artifact_type}")

        elapsed_ms = int((time.perf_counter() - started) * 1000)
        checks = {name: CHECKS[name](result) for name in case.get("expected_checks", [])}
        return BenchmarkResult(
            case_id=case["id"],
            corpus_id=case["corpus_id"],
            artifact_type=artifact_type,
            provider_mode=provider_mode,
            provider_name=provider_name,
            model_id=usage.model or model_id,
            success=all(checks.values()),
            duration_ms=elapsed_ms,
            input_tokens=usage.input_tokens,
            output_tokens=usage.output_tokens,
            error=None,
            checks=checks,
            metrics=metrics,
        )
    except Exception as exc:  # noqa: BLE001
        elapsed_ms = int((time.perf_counter() - started) * 1000)
        return BenchmarkResult(
            case_id=case["id"],
            corpus_id=case["corpus_id"],
            artifact_type=artifact_type,
            provider_mode=provider_mode,
            provider_name=provider_name,
            model_id=model_id,
            success=False,
            duration_ms=elapsed_ms,
            input_tokens=0,
            output_tokens=0,
            error=str(exc) if provider_mode == "fake" else _sanitize_live_error(exc),
            checks={},
            metrics={},
        )


def _write_report(results_dir: Path, results: list[BenchmarkResult]) -> None:
    summary = {
        "total_cases": len(results),
        "successful_cases": sum(1 for result in results if result.success),
        "failed_cases": sum(1 for result in results if not result.success),
        "results": [asdict(result) for result in results],
    }
    results_dir.mkdir(parents=True, exist_ok=True)
    (results_dir / "summary.json").write_text(json.dumps(summary, indent=2))
    for result in results:
        (results_dir / f"{result.case_id}.json").write_text(json.dumps(asdict(result), indent=2))

    lines = [
        "# Comprehension Benchmark Report",
        "",
        f"- Total cases: {summary['total_cases']}",
        f"- Successful: {summary['successful_cases']}",
        f"- Failed: {summary['failed_cases']}",
        "",
        "| Case | Artifact | Success | Duration (ms) | Notes |",
        "|---|---|---:|---:|---|",
    ]
    for result in results:
        notes = result.error or ", ".join(
            f"{name}={'pass' if passed else 'fail'}" for name, passed in result.checks.items()
        )
        lines.append(
            f"| `{result.case_id}` | `{result.artifact_type}` | "
            f"{'yes' if result.success else 'no'} | {result.duration_ms} | {notes} |"
        )
    (results_dir / "report.md").write_text("\n".join(lines) + "\n")


async def _main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path, default=MANIFEST_PATH)
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument(
        "--provider-mode",
        choices=("manifest", "fake", "live"),
        default="manifest",
        help="Override manifest provider mode. Use 'live' to run the fixture suite against the configured worker LLM provider.",  # noqa: E501
    )
    args = parser.parse_args()

    cases = _load_manifest(args.manifest)
    results = [await _run_case(case, args.provider_mode) for case in cases]
    _write_report(args.output_dir, results)
    return 0 if all(result.success for result in results) else 1


if __name__ == "__main__":
    raise SystemExit(asyncio.run(_main()))
