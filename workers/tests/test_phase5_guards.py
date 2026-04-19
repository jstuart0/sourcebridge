# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

from __future__ import annotations

import json

import pytest

from workers.common.llm.fake import FakeLLMProvider
from workers.knowledge.servicer import KnowledgeServicer
from workers.knowledge.summary_nodes import SurrealSummaryNodeCache


class _FailingEmbeddingProvider:
    async def embed(self, texts: list[str]) -> list[list[float]]:
        raise ValueError("embedding backend unavailable")


@pytest.mark.asyncio
async def test_prepare_snapshot_propagates_embedding_config_error() -> None:
    servicer = KnowledgeServicer(
        llm_provider=FakeLLMProvider(),
        embedding_provider=_FailingEmbeddingProvider(),
    )
    snapshot_json = json.dumps(
        {
            "entry_points": [
                {
                    "id": "sym-1",
                    "name": "AuthenticateUser",
                    "qualified_name": "auth.AuthenticateUser",
                    "file_path": "internal/auth/service.go",
                }
            ],
            "docs": [],
            "padding": "x" * 300_001,
        }
    )

    with pytest.raises(ValueError, match="embedding backend unavailable"):
        await servicer._prepare_snapshot(snapshot_json, query="find auth flow")


def test_summary_node_row_to_node_coerces_invalid_json_fields() -> None:
    cache = SurrealSummaryNodeCache(client=None)  # type: ignore[arg-type]
    node = cache._row_to_node(
        {
            "id": "node-1",
            "corpus_id": "corpus-1",
            "unit_id": "unit-1",
            "level": 1,
            "child_ids": "{not-json}",
            "metadata": "{not-json}",
        }
    )

    assert node.child_ids == []
    assert node.metadata == {}
