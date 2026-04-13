# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

from __future__ import annotations

import pytest

from workers.common.surreal import SurrealClient


class _StubResponse:
    def __init__(self, payload):
        self._payload = payload

    def raise_for_status(self) -> None:
        return None

    def json(self):
        return self._payload


class _StubAsyncClient:
    def __init__(self, payload):
        self.payload = payload
        self.calls: list[tuple[str, str, dict[str, str]]] = []

    async def post(self, path: str, *, content: str, headers: dict[str, str]):
        self.calls.append((path, content, headers))
        return _StubResponse(self.payload)


@pytest.mark.asyncio
async def test_query_uses_surrealdb_content_type() -> None:
    client = SurrealClient()
    stub = _StubAsyncClient([{"status": "OK", "result": [{"count": 1}]}])
    client._client = stub
    client._connected = True

    result = await client.query("SELECT count() AS count FROM ca_summary_node GROUP ALL;")

    assert result == [{"status": "OK", "result": [{"count": 1}]}]
    assert stub.calls == [
        (
            "/sql",
            "SELECT count() AS count FROM ca_summary_node GROUP ALL;",
            {"Content-Type": "application/surrealdb"},
        )
    ]


@pytest.mark.asyncio
async def test_query_raises_on_statement_error() -> None:
    client = SurrealClient()
    client._client = _StubAsyncClient([{"status": "ERR", "result": "Specify a namespace to use"}])
    client._connected = True

    with pytest.raises(RuntimeError, match="Specify a namespace to use"):
        await client.query("SELECT * FROM ca_summary_node;")


@pytest.mark.asyncio
async def test_query_rejects_params() -> None:
    client = SurrealClient()
    client._client = _StubAsyncClient([{"status": "OK", "result": []}])
    client._connected = True

    with pytest.raises(ValueError, match="does not support params"):
        await client.query("SELECT * FROM ca_summary_node;", params={"corpus_id": "repo-1"})
