from __future__ import annotations

from pathlib import Path

from workers.cli_ask import DeepUnderstandingContext, _best_deep_files, _understanding_ready


def write_file(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8")


def test_understanding_ready_requires_ready_complete() -> None:
    ready = DeepUnderstandingContext(
        repo_id="repo",
        repo_name="repo",
        repo_lookup="path",
        corpus_id="repo",
        understanding_stage="ready",
        tree_status="complete",
        revision_fp="rev",
        model_used="model",
    )
    failed = DeepUnderstandingContext(
        repo_id="repo",
        repo_name="repo",
        repo_lookup="path",
        corpus_id="repo",
        understanding_stage="failed",
        tree_status="partial",
        revision_fp="rev",
        model_used="model",
    )

    assert _understanding_ready(ready) is True
    assert _understanding_ready(failed) is False


def test_best_deep_files_prefers_architecture_pipeline_files(tmp_path: Path) -> None:
    write_file(
        tmp_path / "web/src/components/architecture/ArchitectureDiagram.tsx",
        "export function ArchitectureDiagram() { refreshArtifact(); generateArchitectureDiagram(); }",
    )
    write_file(
        tmp_path / "internal/architecture/diagram.go",
        "package architecture\n\nfunc BuildDiagram() string { return \"graph TD\" }",
    )
    write_file(
        tmp_path / "workers/knowledge/architecture_diagram.py",
        "def generate_architecture_diagram():\n    return 'ok'\n",
    )
    write_file(
        tmp_path / "internal/api/graphql/knowledge_support.go",
        "package graphql\n\nfunc buildArchitectureDiagramPromptBundle() {}\n",
    )
    write_file(
        tmp_path / "internal/api/graphql/schema.resolvers.go",
        "package graphql\n\nfunc GenerateArchitectureDiagram() {}\n",
    )
    write_file(
        tmp_path / "internal/api/middleware/tenant.go",
        "package middleware\n\nfunc TenantMiddleware() {}\n",
    )
    write_file(
        tmp_path / "workers/tests/test_architecture_diagram.py",
        "def test_it():\n    pass\n",
    )

    ranked = _best_deep_files(
        tmp_path,
        "How are architecture diagrams generated and refreshed in this repo?",
        summary_evidence=[],
    )
    ranked_paths = [str(item.path) for item in ranked]

    assert ranked_paths == [
        "web/src/components/architecture/ArchitectureDiagram.tsx",
        "internal/architecture/diagram.go",
        "workers/knowledge/architecture_diagram.py",
        "internal/api/graphql/knowledge_support.go",
    ]
