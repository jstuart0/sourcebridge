"""SurrealDB client for Python workers."""

from __future__ import annotations

from typing import Any

import httpx
import structlog

log = structlog.get_logger()


class SurrealClient:
    """Async SurrealDB client using the HTTP API.

    Uses the SurrealDB HTTP endpoint (/sql) rather than WebSocket RPC,
    which is simpler for the limited worker-side queries needed.
    """

    def __init__(
        self,
        url: str = "ws://localhost:8000/rpc",
        namespace: str = "sourcebridge",
        database: str = "sourcebridge",
        user: str = "root",
        password: str = "root",
    ) -> None:
        # Convert ws:// URL to http:// for HTTP API
        http_url = url.replace("ws://", "http://").replace("wss://", "https://")
        # Strip /rpc suffix if present
        if http_url.endswith("/rpc"):
            http_url = http_url[:-4]
        self.base_url = http_url
        self.namespace = namespace
        self.database = database
        self.user = user
        self.password = password
        self._connected = False
        self._client: httpx.AsyncClient | None = None

    async def connect(self) -> None:
        """Connect to SurrealDB via HTTP."""
        log.info(
            "surreal_connecting",
            url=self.base_url,
            namespace=self.namespace,
            database=self.database,
        )
        self._client = httpx.AsyncClient(
            base_url=self.base_url,
            auth=(self.user, self.password),
            headers={
                "Accept": "application/json",
                "Surreal-NS": self.namespace,
                "Surreal-DB": self.database,
            },
            timeout=30.0,
        )

        # Verify connectivity
        try:
            resp = await self._client.get("/health")
            resp.raise_for_status()
            self._connected = True
            log.info("surreal_connected", url=self.base_url)
        except httpx.HTTPError as e:
            log.warn("surreal_connection_failed", error=str(e))
            await self._client.aclose()
            self._client = None
            raise RuntimeError(f"SurrealDB connection failed: {e}") from e

    async def query(self, sql: str, params: dict[str, Any] | None = None) -> list[Any]:
        """Execute a SurrealQL query via the HTTP /sql endpoint."""
        if not self._connected or self._client is None:
            raise RuntimeError("Not connected to SurrealDB")
        if params:
            raise ValueError("SurrealClient.query does not support params with HTTP /sql")

        log.debug("surreal_query", sql=sql[:100])
        resp = await self._client.post(
            "/sql",
            content=sql,
            headers={"Content-Type": "application/surrealdb"},
        )
        resp.raise_for_status()
        results = resp.json()
        if isinstance(results, list):
            for idx, result in enumerate(results):
                if isinstance(result, dict) and str(result.get("status", "")).upper() == "ERR":
                    message = str(result.get("result") or result.get("detail") or "unknown SurrealDB statement error")
                    raise RuntimeError(f"SurrealDB statement {idx} failed: {message}")
        if isinstance(results, list):
            return results
        return [results]

    async def close(self) -> None:
        """Close the connection."""
        if self._client is not None:
            await self._client.aclose()
            self._client = None
        self._connected = False
        log.info("surreal_disconnected")

    @property
    def connected(self) -> bool:
        return self._connected
