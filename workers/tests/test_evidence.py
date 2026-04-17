# SPDX-License-Identifier: AGPL-3.0-or-later

from workers.knowledge.evidence import (
    detect_unsupported_claim_terms,
    extract_section_evidence_refs,
    is_valid_evidence_path,
    relevance_penalty,
    strip_forbidden_phrase_sentences,
    strip_speculative_sentences,
    strip_unsupported_claim_sentences,
)
from workers.knowledge.types import EvidenceRef


def test_is_valid_evidence_path_rejects_placeholder_values() -> None:
    assert not is_valid_evidence_path("")
    assert not is_valid_evidence_path("None")
    assert not is_valid_evidence_path("repository")
    assert not is_valid_evidence_path("src/")


def test_is_valid_evidence_path_accepts_real_files() -> None:
    assert is_valid_evidence_path("README.md")
    assert is_valid_evidence_path("src/services/auth-service.ts")


def test_extract_section_evidence_refs_filters_invalid_paths() -> None:
    refs = extract_section_evidence_refs(
        [
            EvidenceRef(source_type="file", file_path="repository", line_start=1, line_end=2),
            EvidenceRef(source_type="file", file_path="src/services/auth-service.ts", line_start=10, line_end=20),
        ]
    )
    assert len(refs) == 1
    assert refs[0].path == "src/services/auth-service.ts"


def test_relevance_penalty_downranks_example_paths_for_product_core() -> None:
    assert relevance_penalty("examples/demo/main.go", profile="product_core") == 1
    assert relevance_penalty("internal/api/server.go", profile="product_core") == 0


def test_relevance_penalty_keeps_explicitly_scoped_path() -> None:
    assert relevance_penalty("examples/demo/main.go", profile="product_core", scope_path="examples/demo") == 0


def test_detect_unsupported_claim_terms_flags_missing_tech() -> None:
    hits = detect_unsupported_claim_terms(
        "The system uses Redis for queueing and Graphviz for diagrams.",
        "The repository mentions Mermaid and a job queue worker.",
    )
    assert "redis" in hits
    assert "graphviz" in hits


def test_strip_unsupported_claim_sentences_removes_bad_claims() -> None:
    text = "The system uses Redis for queueing. It stores job state in files."
    stripped = strip_unsupported_claim_sentences(text, ["redis"])
    assert "Redis" not in stripped
    assert "stores job state in files" in stripped


def test_strip_forbidden_phrase_sentences_removes_inflated_framing() -> None:
    text = "SourceBridge is a comprehensive platform designed for developers. It exposes a GraphQL API."
    stripped = strip_forbidden_phrase_sentences(text, ["comprehensive platform", "designed for"])
    assert "comprehensive platform" not in stripped.lower()
    assert "GraphQL API" in stripped


def test_strip_speculative_sentences_removes_unbacked_inference() -> None:
    text = (
        "While persistence is not explicitly detailed, it is an inferred component necessary to store artifacts. "
        "The web UI renders architecture diagrams."
    )
    stripped = strip_speculative_sentences(text)
    assert "not explicitly detailed" not in stripped.lower()
    assert "inferred component" not in stripped.lower()
    assert "web UI renders architecture diagrams" in stripped
