# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Tests for snapshot retrieval utilities."""

from __future__ import annotations

from workers.knowledge.retrieval import build_overview_query


def test_build_overview_query_repository_scope() -> None:
    """Repository scope produces a broad overview query."""
    query = build_overview_query("my-repo", "cliff_notes")
    assert "Overview" in query
    assert "my-repo" in query


def test_build_overview_query_symbol_scope() -> None:
    """Symbol scope produces a targeted query with the symbol name."""
    query = build_overview_query(
        "my-repo",
        "cliff_notes",
        scope_type="symbol",
        scope_path="internal/auth.go#handleLogin",
    )
    assert "handleLogin" in query
    assert "internal/auth.go" in query
    assert "callers" in query
    assert "Overview" not in query


def test_build_overview_query_file_scope() -> None:
    """File scope produces a targeted query with the file path."""
    query = build_overview_query(
        "my-repo",
        "cliff_notes",
        scope_type="file",
        scope_path="internal/auth.go",
    )
    assert "internal/auth.go" in query
    assert "symbols" in query
    assert "Overview" not in query


def test_build_overview_query_module_scope() -> None:
    """Module scope produces a targeted query with the module path."""
    query = build_overview_query(
        "my-repo",
        "cliff_notes",
        scope_type="module",
        scope_path="internal/api",
    )
    assert "internal/api" in query
    assert "components" in query
