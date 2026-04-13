"""Worker-side job-scoped log persistence for in-app monitoring."""

from __future__ import annotations

import asyncio
import json
import uuid
from dataclasses import dataclass, field
from typing import Any

import structlog

from workers.common.config import WorkerConfig
from workers.common.surreal import SurrealClient

log = structlog.get_logger()

MAX_PAYLOAD_BYTES = 64 * 1024


def _sql_string(value: str) -> str:
    return json.dumps(value)


@dataclass
class JobLogMetadata:
    job_id: str
    repo_id: str = ""
    artifact_id: str = ""
    subsystem: str = "knowledge"
    job_type: str = ""


@dataclass
class SurrealJobLogger:
    metadata: JobLogMetadata
    client: SurrealClient
    _connect_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    _sequence_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    _sequence: int = 0

    @classmethod
    def from_config(cls, config: WorkerConfig, metadata: JobLogMetadata) -> SurrealJobLogger:
        return cls(
            metadata=metadata,
            client=SurrealClient(
                url=config.surreal_url,
                namespace=config.surreal_namespace,
                database=config.surreal_database,
                user=config.surreal_user,
                password=config.surreal_pass,
            ),
        )

    async def _ensure_connected(self) -> None:
        if self.client.connected:
            return
        async with self._connect_lock:
            if not self.client.connected:
                await self.client.connect()

    async def _next_sequence(self) -> int:
        async with self._sequence_lock:
            self._sequence += 1
            return self._sequence

    async def info(self, *, phase: str = "", event: str, message: str, payload: dict[str, Any] | None = None) -> None:
        await self._append("info", phase, event, message, payload)

    async def warn(self, *, phase: str = "", event: str, message: str, payload: dict[str, Any] | None = None) -> None:
        await self._append("warn", phase, event, message, payload)

    async def error(self, *, phase: str = "", event: str, message: str, payload: dict[str, Any] | None = None) -> None:
        await self._append("error", phase, event, message, payload)

    async def _append(
        self,
        level: str,
        phase: str,
        event: str,
        message: str,
        payload: dict[str, Any] | None,
    ) -> None:
        if not self.metadata.job_id:
            return
        try:
            await self._ensure_connected()
            sequence = await self._next_sequence()
            payload_json = ""
            if payload:
                encoded = json.dumps(payload, sort_keys=True, default=str)
                if len(encoded.encode("utf-8")) > MAX_PAYLOAD_BYTES:
                    encoded = json.dumps(
                        {
                            "warning": "payload_truncated",
                            "event": event,
                        },
                        sort_keys=True,
                    )
                payload_json = encoded
            record_id = str(uuid.uuid4())
            sql = f"""
CREATE ca_llm_job_log SET
    id = type::thing('ca_llm_job_log', {_sql_string(record_id)}),
    job_id = {_sql_string(self.metadata.job_id)},
    repo_id = {_sql_string(self.metadata.repo_id)},
    artifact_id = {_sql_string(self.metadata.artifact_id)},
    subsystem = {_sql_string(self.metadata.subsystem)},
    job_type = {_sql_string(self.metadata.job_type)},
    level = {_sql_string(level)},
    phase = {_sql_string(phase)},
    event = {_sql_string(event)},
    message = {_sql_string(message)},
    payload_json = {_sql_string(payload_json)},
    sequence = {sequence},
    created_at = time::now();
"""
            await self.client.query(sql)
        except Exception as exc:
            log.warning(
                "job_log_append_failed",
                job_id=self.metadata.job_id,
                log_event=event,
                error=str(exc),
            )

    async def close(self) -> None:
        await self.client.close()
