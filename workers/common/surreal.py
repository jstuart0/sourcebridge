"""SurrealDB client for Python workers."""

from __future__ import annotations

import asyncio
from typing import Any

import httpx
import structlog

log = structlog.get_logger()

# Maximum reconnection attempts before giving up on a single query.
_MAX_RECONNECT_ATTEMPTS = 3
_RECONNECT_DELAY = 1.0  # seconds


class SurrealClient:
    """Async SurrealDB client using the HTTP API.

    Uses the SurrealDB HTTP endpoint (/sql) rather than WebSocket RPC,
    which is simpler for the limited worker-side queries needed.

    Auto-reconnects on transient connection errors.
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
        self._lock = asyncio.Lock()

    async def connect(self) -> None:
        """Connect to SurrealDB via HTTP."""
        async with self._lock:
            await self._connect_internal()

    async def _connect_internal(self) -> None:
        """Internal connect — caller must hold self._lock."""
        # Close any stale client
        if self._client is not None:
            try:
                await self._client.aclose()
            except Exception:
                pass
            self._client = None
            self._connected = False

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

    async def _reconnect(self) -> None:
        """Attempt to reconnect. Caller must hold self._lock."""
        log.info("surreal_reconnecting", url=self.base_url)
        await self._connect_internal()

    async def query(self, sql: str, params: dict[str, Any] | None = None) -> list[Any]:
        """Execute a SurrealQL query via the HTTP /sql endpoint.

        Auto-reconnects on transient connection errors and retries.
        """
        if params:
            raise ValueError("SurrealClient.query does not support params with HTTP /sql")

        last_error: Exception | None = None
        for attempt in range(_MAX_RECONNECT_ATTEMPTS):
            # Ensure connected
            if not self._connected or self._client is None:
                async with self._lock:
                    if not self._connected or self._client is None:
                        try:
                            await self._connect_internal()
                        except RuntimeError as e:
                            last_error = e
                            if attempt < _MAX_RECONNECT_ATTEMPTS - 1:
                                await asyncio.sleep(_RECONNECT_DELAY * (attempt + 1))
                                continue
                            raise

            try:
                log.debug("surreal_query", sql=sql[:100])
                resp = await self._client.post(  # type: ignore[union-attr]
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

            except (httpx.ConnectError, httpx.ReadError, httpx.WriteError,
                    httpx.PoolTimeout, httpx.ConnectTimeout, httpx.ReadTimeout) as e:
                # Transient connection error — mark disconnected and retry
                last_error = e
                log.warn(
                    "surreal_query_connection_error",
                    error=str(e),
                    attempt=attempt + 1,
                    max_attempts=_MAX_RECONNECT_ATTEMPTS,
                )
                self._connected = False
                if attempt < _MAX_RECONNECT_ATTEMPTS - 1:
                    await asyncio.sleep(_RECONNECT_DELAY * (attempt + 1))
                    continue
                raise RuntimeError(f"SurrealDB query failed after {_MAX_RECONNECT_ATTEMPTS} attempts: {e}") from e

            except httpx.HTTPStatusError as e:
                # Non-transient HTTP error (4xx, 5xx) — don't retry
                raise RuntimeError(f"SurrealDB query HTTP error: {e}") from e

        # Should not reach here, but just in case
        raise RuntimeError(f"SurrealDB query failed: {last_error}")

    async def close(self) -> None:
        """Close the connection."""
        async with self._lock:
            if self._client is not None:
                await self._client.aclose()
                self._client = None
            self._connected = False
        log.info("surreal_closed")

    @property
    def connected(self) -> bool:
        return self._connected
