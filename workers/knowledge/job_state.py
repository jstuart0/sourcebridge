"""Worker-side persistence for resumable job and understanding state."""

from __future__ import annotations

import asyncio
import json
from dataclasses import dataclass, field
from typing import Any

import structlog

from workers.common.config import WorkerConfig
from workers.common.surreal import SurrealClient

log = structlog.get_logger()


def _sql_string(value: str) -> str:
    return json.dumps(value)


def _normalize_query_result(raw: list[Any]) -> list[dict[str, Any]]:
    if not raw:
        return []
    if isinstance(raw[0], dict) and "result" in raw[0]:
        result = raw[0].get("result")
        if isinstance(result, list):
            return [row for row in result if isinstance(row, dict)]
        return []
    return [row for row in raw if isinstance(row, dict)]


@dataclass
class JobStateMetadata:
    job_id: str = ""
    repo_id: str = ""
    artifact_id: str = ""
    scope_key: str = "repository:"

    def is_empty(self) -> bool:
        return not self.job_id and not self.repo_id


@dataclass
class SurrealJobStateUpdater:
    metadata: JobStateMetadata
    client: SurrealClient
    _connect_lock: asyncio.Lock = field(default_factory=asyncio.Lock)

    @classmethod
    def from_config(cls, config: WorkerConfig, metadata: JobStateMetadata) -> SurrealJobStateUpdater:
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

    async def update_job_resume_state(
        self,
        *,
        cached_nodes_loaded: int = 0,
        total_nodes: int = 0,
        resume_stage: str = "",
        skipped_leaf_units: int = 0,
        skipped_file_units: int = 0,
        skipped_package_units: int = 0,
        skipped_root_units: int = 0,
        leaf_cache_hits: int = 0,
        file_cache_hits: int = 0,
        package_cache_hits: int = 0,
        root_cache_hits: int = 0,
    ) -> None:
        if not self.metadata.job_id:
            return
        await self._ensure_connected()
        reused = leaf_cache_hits + file_cache_hits + package_cache_hits + root_cache_hits
        sql = f"""
UPDATE type::thing('ca_llm_job', {_sql_string(self.metadata.job_id)}) SET
    reused_summaries = {reused},
    leaf_cache_hits = {leaf_cache_hits},
    file_cache_hits = {file_cache_hits},
    package_cache_hits = {package_cache_hits},
    root_cache_hits = {root_cache_hits},
    cached_nodes_loaded = {cached_nodes_loaded},
    total_nodes = {total_nodes},
    resume_stage = {_sql_string(resume_stage)},
    skipped_leaf_units = {skipped_leaf_units},
    skipped_file_units = {skipped_file_units},
    skipped_package_units = {skipped_package_units},
    skipped_root_units = {skipped_root_units},
    updated_at = time::now()
WHERE status = 'pending' OR status = 'generating' OR status = 'ready';
"""
        try:
            await self.client.query(sql)
        except Exception as exc:
            log.warning("job_resume_state_update_failed", job_id=self.metadata.job_id, error=str(exc))

    async def update_understanding_checkpoint(
        self,
        *,
        corpus_id: str,
        revision_fp: str,
        strategy: str,
        stage: str,
        tree_status: str,
        cached_nodes: int,
        total_nodes: int,
        model_used: str,
        checkpoint: dict[str, Any],
    ) -> None:
        if not self.metadata.repo_id:
            return
        await self._ensure_connected()
        existing_meta: dict[str, Any] = {}
        try:
            rows = _normalize_query_result(
                await self.client.query(
                    "SELECT metadata FROM ca_repository_understanding "
                    f"WHERE repo_id = {_sql_string(self.metadata.repo_id)} "
                    f"AND scope_key = {_sql_string(self.metadata.scope_key)} LIMIT 1;"
                )
            )
            if rows:
                raw_meta = rows[0].get("metadata") or "{}"
                if isinstance(raw_meta, str):
                    existing_meta = json.loads(raw_meta or "{}")
                elif isinstance(raw_meta, dict):
                    existing_meta = dict(raw_meta)
        except Exception as exc:
            log.warning("understanding_checkpoint_load_failed", repo_id=self.metadata.repo_id, error=str(exc))
            existing_meta = {}

        existing_meta["resume"] = checkpoint
        metadata_json = json.dumps(existing_meta, sort_keys=True)
        sql = f"""
UPDATE ca_repository_understanding SET
    corpus_id = {_sql_string(corpus_id)},
    revision_fp = {_sql_string(revision_fp)},
    strategy = {_sql_string(strategy)},
    stage = {_sql_string(stage)},
    tree_status = {_sql_string(tree_status)},
    cached_nodes = {cached_nodes},
    total_nodes = {total_nodes},
    model_used = {_sql_string(model_used)},
    metadata = {_sql_string(metadata_json)},
    updated_at = time::now()
WHERE repo_id = {_sql_string(self.metadata.repo_id)}
  AND scope_key = {_sql_string(self.metadata.scope_key)};
"""
        try:
            await self.client.query(sql)
        except Exception as exc:
            log.warning("understanding_checkpoint_update_failed", repo_id=self.metadata.repo_id, error=str(exc))

    async def close(self) -> None:
        await self.client.close()
