# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Tests for the CodeCorpus adapter."""

from __future__ import annotations

import pytest

from workers.comprehension.adapters.code import CodeCorpus
from workers.comprehension.corpus import CorpusSource, UnitKind, walk_by_level, walk_leaves


def _sample_snapshot() -> dict:
    """A small KnowledgeSnapshot-shaped dict spanning two modules and
    three files, with several symbols per file."""
    return {
        "repository_id": "repo-xyz",
        "repository_name": "SampleRepo",
        "source_revision": {
            "commit_sha": "abc123",
            "content_fingerprint": "fp-1",
        },
        "file_count": 3,
        "symbol_count": 5,
        "modules": [
            {"name": "api", "path": "internal/api", "file_count": 2},
            {"name": "store", "path": "internal/store", "file_count": 1},
        ],
        "entry_points": [
            {
                "id": "sym-1",
                "name": "HandleLogin",
                "qualified_name": "api.HandleLogin",
                "kind": "function",
                "signature": "func HandleLogin(ctx context.Context) error",
                "file_path": "internal/api/auth.go",
                "start_line": 10,
                "end_line": 40,
                "doc_comment": "HandleLogin processes a login request.",
            },
            {
                "id": "sym-2",
                "name": "HandleLogout",
                "qualified_name": "api.HandleLogout",
                "kind": "function",
                "signature": "func HandleLogout()",
                "file_path": "internal/api/auth.go",
                "start_line": 50,
                "end_line": 80,
            },
        ],
        "public_api": [
            {
                "id": "sym-3",
                "name": "NewAPI",
                "qualified_name": "api.NewAPI",
                "kind": "function",
                "file_path": "internal/api/api.go",
            },
            # Duplicate of sym-1 — must not produce a duplicate leaf.
            {
                "id": "sym-1",
                "name": "HandleLogin",
                "file_path": "internal/api/auth.go",
            },
        ],
        "complex_symbols": [
            {
                "id": "sym-4",
                "name": "Repository",
                "qualified_name": "store.Repository",
                "kind": "struct",
                "file_path": "internal/store/repo.go",
            },
        ],
        "test_symbols": [],
        "high_fan_out_symbols": [],
        "high_fan_in_symbols": [],
    }


def test_code_corpus_satisfies_protocol() -> None:
    corpus = CodeCorpus(snapshot=_sample_snapshot())
    assert isinstance(corpus, CorpusSource)


def test_code_corpus_hierarchy_levels() -> None:
    corpus = CodeCorpus(snapshot=_sample_snapshot())
    by_level = walk_by_level(corpus)
    assert len(by_level[3]) == 1  # root
    assert len(by_level[2]) == 2  # api + store packages
    assert len(by_level[1]) == 3  # 3 distinct files
    # 4 unique symbols (sym-1 deduped) across the files
    assert len(by_level[0]) == 4


def test_code_corpus_dedupes_symbols_appearing_in_multiple_groups() -> None:
    corpus = CodeCorpus(snapshot=_sample_snapshot())
    leaf_ids = [u.id for u in walk_leaves(corpus)]
    handle_login_leaves = [i for i in leaf_ids if "sym-1" in i]
    assert len(handle_login_leaves) == 1


def test_code_corpus_leaf_content_includes_signature_and_doc() -> None:
    corpus = CodeCorpus(snapshot=_sample_snapshot())
    leaves = list(walk_leaves(corpus))
    login_leaf = next(u for u in leaves if "sym-1" in u.id)
    body = corpus.leaf_content(login_leaf)
    assert "HandleLogin" in body
    assert "HandleLogin processes a login request" in body
    assert "func HandleLogin(ctx context.Context) error" in body
    assert "internal/api/auth.go:10-40" in body


def test_code_corpus_root_metadata_has_repo_name() -> None:
    corpus = CodeCorpus(snapshot=_sample_snapshot())
    root = corpus.root()
    assert root.kind is UnitKind.ROOT
    assert root.label == "SampleRepo"
    assert root.metadata["repository_id"] == "repo-xyz"


def test_code_corpus_revision_fp_falls_back_to_content_fingerprint() -> None:
    corpus = CodeCorpus(snapshot=_sample_snapshot())
    assert corpus.revision_fingerprint() == "fp-1"


def test_code_corpus_handles_snapshot_with_no_modules() -> None:
    snap = {
        "repository_id": "r",
        "repository_name": "r",
        "file_count": 1,
        "symbol_count": 1,
        "entry_points": [
            {
                "id": "s",
                "name": "F",
                "kind": "function",
                "file_path": "main.go",
            }
        ],
    }
    corpus = CodeCorpus(snapshot=snap)
    by_level = walk_by_level(corpus)
    # Root + synthetic single package + 1 file + 1 symbol leaf.
    assert len(by_level[3]) == 1
    assert len(by_level[2]) == 1
    assert len(by_level[1]) == 1
    assert len(by_level[0]) == 1


def test_code_corpus_scope_context_injects_target_file() -> None:
    """Scoped cliff notes may carry a target file that isn't in any
    symbol group. Make sure it still appears in the corpus."""
    snap = {
        "repository_id": "r",
        "repository_name": "r",
        "scope_context": {
            "scope_type": "file",
            "scope_path": "README.md",
            "target_file": {"path": "README.md"},
        },
    }
    corpus = CodeCorpus(snapshot=snap)
    leaf_ids = [u.id for u in walk_leaves(corpus)]
    assert any("README.md" in lid for lid in leaf_ids)


def test_code_corpus_leaf_content_raises_on_non_leaf() -> None:
    corpus = CodeCorpus(snapshot=_sample_snapshot())
    with pytest.raises(ValueError):
        corpus.leaf_content(corpus.root())
