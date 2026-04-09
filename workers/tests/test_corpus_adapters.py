# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Tests for the RequirementsCorpus and DocumentCorpus adapters."""

from __future__ import annotations

import pytest

from workers.comprehension.adapters.document import DocumentCorpus
from workers.comprehension.adapters.requirements import RequirementsCorpus
from workers.comprehension.corpus import (
    CorpusSource,
    UnitKind,
    walk_by_level,
    walk_leaves,
)

# =====================================================================
# RequirementsCorpus
# =====================================================================


def _sample_requirements_payload() -> dict:
    return {
        "collection_id": "prd-2026q2",
        "collection_name": "Q2 Product Requirements",
        "documents": [
            {
                "id": "auth",
                "title": "Authentication",
                "sections": [
                    {
                        "id": "login",
                        "title": "Login flows",
                        "requirements": [
                            {
                                "id": "REQ-1",
                                "external_id": "AUTH-01",
                                "title": "Password login",
                                "description": "Users must be able to log in with email + password.",
                                "priority": "must",
                                "tags": ["auth", "login"],
                            },
                            {
                                "id": "REQ-2",
                                "external_id": "AUTH-02",
                                "title": "OIDC login",
                                "description": "SSO via OIDC for enterprise tenants.",
                                "priority": "should",
                            },
                        ],
                    }
                ],
            },
            {
                "id": "billing",
                "title": "Billing",
                # Flat requirements list — no sections.
                "requirements": [
                    {
                        "id": "REQ-3",
                        "external_id": "BIL-01",
                        "title": "Usage metering",
                        "description": "Track per-seat usage.",
                    }
                ],
            },
        ],
    }


def test_requirements_corpus_satisfies_protocol() -> None:
    corpus = RequirementsCorpus(payload=_sample_requirements_payload())
    assert isinstance(corpus, CorpusSource)
    assert corpus.corpus_type == "requirements"
    assert corpus.corpus_id == "prd-2026q2"


def test_requirements_corpus_hierarchy_levels() -> None:
    corpus = RequirementsCorpus(payload=_sample_requirements_payload())
    by_level = walk_by_level(corpus)
    assert len(by_level[3]) == 1  # collection
    assert len(by_level[2]) == 2  # auth + billing documents
    # 1 section (login) under auth + 1 synthesized section under billing
    assert len(by_level[1]) == 2
    # REQ-1, REQ-2 under login; REQ-3 under billing's synthesized section
    assert len(by_level[0]) == 3


def test_requirements_corpus_leaf_content_includes_description_and_priority() -> None:
    corpus = RequirementsCorpus(payload=_sample_requirements_payload())
    leaves = list(walk_leaves(corpus))
    req1 = next(u for u in leaves if "REQ-1" in u.id)
    body = corpus.leaf_content(req1)
    assert "Password login" in body
    assert "log in with email" in body
    assert "Priority: must" in body
    assert "auth" in body


def test_requirements_corpus_accepts_flat_requirements_top_level() -> None:
    """Top-level 'requirements' list without nested documents should work."""
    payload = {
        "collection_name": "Ad-hoc",
        "requirements": [
            {"id": "R1", "title": "Do thing", "description": "Description 1"},
            {"id": "R2", "title": "Do other", "description": "Description 2"},
        ],
    }
    corpus = RequirementsCorpus(payload=payload)
    by_level = walk_by_level(corpus)
    assert len(by_level[3]) == 1  # collection
    assert len(by_level[2]) == 1  # synthesized document
    assert len(by_level[1]) == 1  # synthesized section
    assert len(by_level[0]) == 2  # R1, R2


def test_requirements_corpus_handles_empty_section() -> None:
    """Empty sections should still emit a placeholder leaf."""
    payload = {
        "collection_name": "Test",
        "documents": [
            {
                "id": "d",
                "title": "D",
                "sections": [{"id": "s", "title": "Empty", "requirements": []}],
            }
        ],
    }
    corpus = RequirementsCorpus(payload=payload)
    leaves = list(walk_leaves(corpus))
    assert len(leaves) == 1
    body = corpus.leaf_content(leaves[0])
    assert "no requirements" in body.lower()


def test_requirements_corpus_leaf_raises_on_non_leaf() -> None:
    corpus = RequirementsCorpus(payload=_sample_requirements_payload())
    with pytest.raises(ValueError):
        corpus.leaf_content(corpus.root())


# =====================================================================
# DocumentCorpus
# =====================================================================


_README_CONTENT = """\
# My Project

This is a brief intro to the project.

## Installation

Run `pip install my-project`.

You may also want to install the dev extras:

    pip install my-project[dev]

## Usage

Import and call the entry point:

```python
from my_project import main
main()
```

### Advanced

Advanced options are documented separately.
"""


def _sample_document_payload() -> dict:
    return {
        "collection_id": "onboarding",
        "collection_name": "Onboarding Docs",
        "documents": [
            {"id": "readme", "title": "README", "content": _README_CONTENT},
            {
                "id": "notes",
                "title": "Release Notes",
                "content": "No release yet.\n\nSee the README for now.",
            },
        ],
    }


def test_document_corpus_satisfies_protocol() -> None:
    corpus = DocumentCorpus(payload=_sample_document_payload())
    assert isinstance(corpus, CorpusSource)
    assert corpus.corpus_type == "document"


def test_document_corpus_splits_markdown_into_sections() -> None:
    corpus = DocumentCorpus(payload=_sample_document_payload())
    by_level = walk_by_level(corpus)
    assert len(by_level[3]) == 1  # collection
    assert len(by_level[2]) == 2  # 2 documents
    # README: "Overview" pre-H2 + Installation + Usage = 3 sections
    # notes.md: single synthetic section = 1 section
    # Total: 4 sections
    assert len(by_level[1]) == 4
    # Leaves are paragraphs; at least 5 across the two documents.
    assert len(by_level[0]) >= 5


def test_document_corpus_preserves_paragraph_content() -> None:
    corpus = DocumentCorpus(payload=_sample_document_payload())
    leaves = list(walk_leaves(corpus))
    bodies = [corpus.leaf_content(u) for u in leaves]
    joined = "\n".join(bodies)
    assert "pip install my-project" in joined
    assert "brief intro" in joined
    assert "No release yet" in joined


def test_document_corpus_falls_back_to_paragraphs_without_headings() -> None:
    payload = {
        "collection_name": "Plain",
        "documents": [
            {
                "id": "plain",
                "title": "Plain text",
                "content": "First paragraph.\n\nSecond paragraph.\n\nThird paragraph.",
            }
        ],
    }
    corpus = DocumentCorpus(payload=payload)
    by_level = walk_by_level(corpus)
    assert len(by_level[1]) == 1  # synthetic single section
    assert len(by_level[0]) == 3  # three paragraphs


def test_document_corpus_top_level_single_document() -> None:
    """A single document can be passed at the top level."""
    payload = {
        "id": "single",
        "title": "Single",
        "content": "First.\n\nSecond.",
    }
    corpus = DocumentCorpus(payload=payload)
    assert len(list(walk_leaves(corpus))) == 2


def test_document_corpus_caps_paragraphs_per_section() -> None:
    """Runaway documents shouldn't produce thousands of leaves."""
    paragraphs = "\n\n".join(f"para {i}" for i in range(200))
    payload = {
        "collection_name": "Big",
        "documents": [{"id": "big", "title": "Big", "content": paragraphs}],
    }
    corpus = DocumentCorpus(payload=payload, max_paragraphs_per_section=10)
    leaves = list(walk_leaves(corpus))
    assert len(leaves) == 10


def test_document_corpus_empty_document_still_appears() -> None:
    payload = {
        "collection_name": "Docs",
        "documents": [{"id": "empty", "title": "Empty", "content": ""}],
    }
    corpus = DocumentCorpus(payload=payload)
    by_level = walk_by_level(corpus)
    assert len(by_level[2]) == 1  # empty document still emits a GROUP
    assert len(by_level[1]) == 1  # synthetic empty section
    assert len(by_level[0]) == 1  # placeholder leaf
    leaf = next(walk_leaves(corpus))
    assert "empty" in corpus.leaf_content(leaf).lower()


def test_document_corpus_unit_kinds_are_correct() -> None:
    corpus = DocumentCorpus(payload=_sample_document_payload())
    assert corpus.root().kind is UnitKind.ROOT
    # First document
    docs = list(corpus.children(corpus.root()))
    assert all(d.kind is UnitKind.GROUP for d in docs)
    sections = list(corpus.children(docs[0]))
    assert all(s.kind is UnitKind.LEAF_CONTAINER for s in sections)
    leaves = list(corpus.children(sections[0]))
    assert all(lf.kind is UnitKind.LEAF for lf in leaves)
