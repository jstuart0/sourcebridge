"""Worker-side summary node cache backed by SurrealDB."""

from __future__ import annotations

import asyncio
import json
import uuid
from dataclasses import dataclass, field
from typing import Any

import structlog

from workers.common.config import WorkerConfig
from workers.common.surreal import SurrealClient
from workers.comprehension.tree import SummaryNode, SummaryTree

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
class SurrealSummaryNodeCache:
    """Persist and restore hierarchical summary trees for retry resume."""

    client: SurrealClient
    _connect_lock: asyncio.Lock = field(default_factory=asyncio.Lock)

    @classmethod
    def from_config(cls, config: WorkerConfig) -> SurrealSummaryNodeCache:
        return cls(
            client=SurrealClient(
                url=config.surreal_url,
                namespace=config.surreal_namespace,
                database=config.surreal_database,
                user=config.surreal_user,
                password=config.surreal_pass,
            )
        )

    async def _ensure_connected(self) -> None:
        if self.client.connected:
            return
        async with self._connect_lock:
            if not self.client.connected:
                await self.client.connect()

    async def load_tree(
        self,
        *,
        corpus_id: str,
        corpus_type: str = "code",
        strategy: str = "hierarchical",
        depth: str = "medium",
    ) -> SummaryTree | None:
        await self._ensure_connected()
        strategy_key = f"{strategy}:{depth}"
        sql = (
            f"SELECT * FROM ca_summary_node WHERE corpus_id = {_sql_string(corpus_id)} "
            f"AND strategy = {_sql_string(strategy_key)} ORDER BY level, unit_id;"
        )
        rows = _normalize_query_result(await self.client.query(sql))
        if not rows:
            return None
        tree = SummaryTree(
            corpus_id=corpus_id,
            corpus_type=corpus_type,
            strategy=str(rows[0].get("strategy") or strategy_key),
            revision_fp=str(rows[0].get("revision_fp") or ""),
        )
        for row in rows:
            tree.add(self._row_to_node(row))
        log.info("summary_node_cache_loaded", corpus_id=corpus_id, nodes=len(tree.nodes))
        return tree

    async def store_tree(self, tree: SummaryTree, *, stage: str | None = None, depth: str = "medium") -> None:
        await self._ensure_connected()
        if not tree.nodes:
            return
        statements: list[str] = []
        for node in tree.nodes.values():
            statements.append(self._upsert_statement(tree, node, depth=depth))
        await self.client.query("\n".join(statements))
        log.info(
            "summary_node_cache_stored",
            corpus_id=tree.corpus_id,
            nodes=len(tree.nodes),
            stage=stage,
        )

    async def store_node(
        self, tree: SummaryTree, node: SummaryNode, *, stage: str | None = None, depth: str = "medium"
    ) -> None:
        await self._ensure_connected()
        await self.client.query(self._upsert_statement(tree, node, depth=depth))
        log.info(
            "summary_node_cache_node_stored",
            corpus_id=tree.corpus_id,
            unit_id=node.unit_id,
            level=node.level,
            stage=stage,
        )

    def _row_to_node(self, row: dict[str, Any]) -> SummaryNode:
        child_ids_raw = row.get("child_ids") or "[]"
        metadata_raw = row.get("metadata") or "{}"
        try:
            child_ids = json.loads(child_ids_raw) if isinstance(child_ids_raw, str) else list(child_ids_raw)
        except (json.JSONDecodeError, TypeError, ValueError):
            child_ids = []
        try:
            metadata = json.loads(metadata_raw) if isinstance(metadata_raw, str) else dict(metadata_raw)
        except (json.JSONDecodeError, TypeError, ValueError):
            metadata = {}
        return SummaryNode(
            id=str(row.get("id") or ""),
            corpus_id=str(row.get("corpus_id") or ""),
            unit_id=str(row.get("unit_id") or ""),
            level=int(row.get("level") or 0),
            parent_id=str(row.get("parent_id") or "") or None,
            child_ids=child_ids,
            summary_text=str(row.get("summary_text") or ""),
            headline=str(row.get("headline") or ""),
            summary_tokens=int(row.get("summary_tokens") or 0),
            source_tokens=int(row.get("source_tokens") or 0),
            content_hash=str(row.get("content_hash") or ""),
            model_used=str(row.get("model_used") or ""),
            strategy=str(row.get("strategy") or ""),
            revision_fp=str(row.get("revision_fp") or ""),
            metadata=metadata,
        )

    def _upsert_statement(self, tree: SummaryTree, node: SummaryNode, *, depth: str) -> str:
        record_id = node.id or str(uuid.uuid4())
        child_ids = json.dumps(node.child_ids)
        metadata = json.dumps(node.metadata, sort_keys=True)
        corpus_id = _sql_string(tree.corpus_id)
        unit_id = _sql_string(node.unit_id)
        parent_id = _sql_string(node.parent_id or "")
        summary_text = _sql_string(node.summary_text)
        headline = _sql_string(node.headline)
        content_hash = _sql_string(node.content_hash)
        model_used = _sql_string(node.model_used)
        base_strategy = (node.strategy or tree.strategy).split(":")[0]
        strategy = _sql_string(f"{base_strategy}:{depth}")
        revision_fp = _sql_string(node.revision_fp or tree.revision_fp)
        child_ids_sql = _sql_string(child_ids)
        metadata_sql = _sql_string(metadata)
        record_id_sql = _sql_string(record_id)
        return f"""
LET $existing = (SELECT id FROM ca_summary_node WHERE corpus_id = {corpus_id} AND unit_id = {unit_id});
IF array::len($existing) > 0 THEN
    (UPDATE ca_summary_node SET
        level = {node.level},
        parent_id = {parent_id},
        child_ids = {child_ids_sql},
        summary_text = {summary_text},
        headline = {headline},
        summary_tokens = {node.summary_tokens},
        source_tokens = {node.source_tokens},
        content_hash = {content_hash},
        model_used = {model_used},
        strategy = {strategy},
        revision_fp = {revision_fp},
        metadata = {metadata_sql},
        generated_at = time::now()
    WHERE corpus_id = {corpus_id} AND unit_id = {unit_id})
ELSE
    (CREATE ca_summary_node SET
        id = type::thing('ca_summary_node', {record_id_sql}),
        corpus_id = {corpus_id},
        unit_id = {unit_id},
        level = {node.level},
        parent_id = {parent_id},
        child_ids = {child_ids_sql},
        summary_text = {summary_text},
        headline = {headline},
        summary_tokens = {node.summary_tokens},
        source_tokens = {node.source_tokens},
        content_hash = {content_hash},
        model_used = {model_used},
        strategy = {strategy},
        revision_fp = {revision_fp},
        metadata = {metadata_sql},
        generated_at = time::now())
END;
"""
