# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""gRPC servicer for the KnowledgeService."""

from __future__ import annotations

import contextlib
import inspect
import json
import os

import grpc
import structlog
from common.v1 import types_pb2
from knowledge.v1 import knowledge_pb2, knowledge_pb2_grpc

from workers.common.config import WorkerConfig
from workers.common.embedding.provider import EmbeddingProvider
from workers.common.grpc_metadata import (
    resolve_cliff_notes_render_metadata,
    resolve_job_log_metadata,
    resolve_llm_override,
    resolve_model_override,
)
from workers.common.llm.config import create_llm_provider_for_request
from workers.common.llm.provider import LLMProvider, SnapshotTooLargeError
from workers.comprehension.adapters.code import CodeCorpus
from workers.comprehension.corpus import walk_by_level
from workers.comprehension.hierarchical import HierarchicalConfig, HierarchicalStrategy
from workers.comprehension.long_context import (
    LongContextConfig,
    LongContextDirectStrategy,
)
from workers.comprehension.renderers import CliffNotesRenderer
from workers.comprehension.selector import SelectionResult, StrategySelector
from workers.comprehension.single_shot import SingleShotConfig, SingleShotStrategy
from workers.comprehension.strategy import ComprehensionStrategy
from workers.knowledge.architecture_diagram import generate_architecture_diagram
from workers.knowledge.code_tour import generate_code_tour
from workers.knowledge.explain_system import explain_system
from workers.knowledge.job_logs import JobLogMetadata, SurrealJobLogger
from workers.knowledge.job_state import JobStateMetadata, SurrealJobStateUpdater
from workers.knowledge.learning_path import generate_learning_path
from workers.knowledge.retrieval import (
    build_overview_query,
    retrieve_relevant_snapshot,
)
from workers.knowledge.snapshot_truncate import condense_snapshot
from workers.knowledge.summary_nodes import SurrealSummaryNodeCache
from workers.knowledge.types import CliffNotesResult
from workers.knowledge.workflow_story import generate_workflow_story
from workers.reasoning.types import LLMUsageRecord

log = structlog.get_logger()


def _supports_kwarg(fn: object, name: str) -> bool:
    try:
        sig = inspect.signature(fn)
    except (TypeError, ValueError):
        return False
    return name in sig.parameters


# SOURCEBRIDGE_CLIFF_NOTES_STRATEGY is a comma-separated preference chain
# the StrategySelector walks in order. Each entry is a strategy name:
#
#   - "hierarchical"       : Phase 3 bottom-up tree — works on any model
#   - "long_context_direct": Single call with the full snapshot; skipped
#                            when the snapshot doesn't fit the model's
#                            effective context window
#   - "single_shot"        : Legacy single-call path (also serves as the
#                            default-safe fallback)
#
# Default chain: "hierarchical,single_shot" — tries the new path first,
# falls back to legacy if hierarchical is unavailable for the current
# model. Operators can reorder, add, or remove entries to suit their
# deployment. When the variable is unset or empty, the default applies.
CLIFF_NOTES_STRATEGY_ENV = "SOURCEBRIDGE_CLIFF_NOTES_STRATEGY"
DEFAULT_CLIFF_NOTES_CHAIN: list[str] = ["hierarchical", "single_shot"]


def _cliff_notes_preference_chain() -> list[str]:
    """Parse the env var into a list of strategy names.

    Single-name values (e.g. ``"hierarchical"``) are still supported —
    they're treated as a one-entry chain. This keeps operators who
    already set the env var to a single strategy working unchanged.
    """
    raw = (os.environ.get(CLIFF_NOTES_STRATEGY_ENV) or "").strip()
    if not raw:
        return list(DEFAULT_CLIFF_NOTES_CHAIN)
    names: list[str] = []
    for part in raw.split(","):
        name = part.strip().lower()
        if name:
            names.append(name)
    return names or list(DEFAULT_CLIFF_NOTES_CHAIN)


# Back-compat alias used by tests that predate the chain format.
def _selected_cliff_notes_strategy() -> str:
    """Return the first entry in the preference chain — legacy shim.

    Tests that relied on the old env-var semantics call this to get a
    single strategy name. New code should call
    ``_cliff_notes_preference_chain`` and drive the selector.
    """
    chain = _cliff_notes_preference_chain()
    return chain[0] if chain else "single_shot"


def _llm_usage_proto(usage_record) -> types_pb2.LLMUsage:
    """Convert an LLMUsageRecord to a proto LLMUsage message."""
    return types_pb2.LLMUsage(
        model=usage_record.model,
        input_tokens=usage_record.input_tokens,
        output_tokens=usage_record.output_tokens,
        operation=usage_record.operation,
    )


class KnowledgeServicer(knowledge_pb2_grpc.KnowledgeServiceServicer):
    """Implements the KnowledgeService gRPC service."""

    def __init__(
        self,
        llm_provider: LLMProvider,
        embedding_provider: EmbeddingProvider | None = None,
        *,
        default_model_id: str = "",
        report_llm: LLMProvider | None = None,
        worker_config: WorkerConfig | None = None,
        summary_node_cache: SurrealSummaryNodeCache | None = None,
    ) -> None:
        self._llm = llm_provider
        self._embedding = embedding_provider
        self._report_llm = report_llm
        self._config = worker_config
        self._summary_node_cache = summary_node_cache
        # default_model_id is the best-effort identifier of the model
        # the LLM provider is configured with. The selector uses it to
        # look up the model's capability profile when no per-call
        # override is provided via gRPC metadata. Operators set this
        # from cfg.llm.knowledge_model when constructing the servicer.
        self._default_model_id = (default_model_id or "").strip()
        self._selector = StrategySelector()

    def _resolve_request_provider(self, context: grpc.aio.ServicerContext) -> tuple[LLMProvider, str | None]:
        override = resolve_llm_override(context)
        if override is None or self._config is None:
            return self._llm, resolve_model_override(context)
        provider, model = create_llm_provider_for_request(
            self._config,
            provider=override.provider,
            base_url=override.base_url,
            api_key=override.api_key,
            model=override.model,
            draft_model=override.draft_model,
        )
        return provider, model or None

    def _resolve_report_provider(self, context: grpc.aio.ServicerContext) -> tuple[LLMProvider, str | None]:
        override = resolve_llm_override(context)
        if override is None:
            model = resolve_model_override(context)
            if self._report_llm is not None:
                fallback_model = self._config.llm_report_model if self._config else None
                return self._report_llm, model or fallback_model or None
            return self._llm, model
        if self._config is None:
            return self._report_llm or self._llm, override.model or None
        provider, model = create_llm_provider_for_request(
            self._config,
            provider=override.provider,
            base_url=override.base_url,
            api_key=override.api_key,
            model=override.model,
            draft_model=override.draft_model,
        )
        return provider, model or None

    def _resolve_job_logger(self, context: grpc.aio.ServicerContext) -> SurrealJobLogger | None:
        if self._config is None:
            return None
        meta = resolve_job_log_metadata(context)
        if meta is None or not meta.job_id:
            return None
        return SurrealJobLogger.from_config(
            self._config,
            JobLogMetadata(
                job_id=meta.job_id,
                repo_id=meta.repo_id,
                artifact_id=meta.artifact_id,
                subsystem=meta.subsystem or "knowledge",
                job_type=meta.job_type,
            ),
        )

    async def _prepare_snapshot(
        self,
        snapshot_json: str,
        query: str,
        scope_type: str = "repository",
    ) -> str:
        """Select the best context-building strategy for the snapshot.

        If an embedding provider is available and the snapshot is large,
        uses retrieval to build a focused snapshot.  Otherwise falls back
        to the condensation strategy (progressive stripping).
        """
        # Small snapshots don't need any reduction
        if len(snapshot_json) < 300_000:
            return snapshot_json

        # Try retrieval first (best quality)
        if self._embedding is not None and query:
            try:
                return await retrieve_relevant_snapshot(
                    snapshot_json,
                    query,
                    self._embedding,
                )
            except Exception as exc:
                log.warn("retrieval_failed_falling_back", error=str(exc))

        # Fall back to condensation
        return condense_snapshot(snapshot_json, scope_type=scope_type)

    def _resolve_model_id(self, override: str | None) -> str:
        """Pick the best model id for capability lookup.

        Prefers the per-call override supplied via gRPC metadata, falls
        back to the servicer's configured default, and finally to a
        generic label so the selector always has something to look up.
        """
        if override:
            return override.strip()
        if self._default_model_id:
            return self._default_model_id
        return "unknown"

    def _build_cliff_notes_strategies(
        self,
        *,
        provider: LLMProvider,
        request: knowledge_pb2.GenerateCliffNotesRequest,
        audience: str,
        depth: str,
        scope_type: str,
        model_override: str | None,
        snapshot_json: str,
    ) -> dict[str, ComprehensionStrategy]:
        """Instantiate every cliff-notes strategy with per-call context.

        Each strategy is constructed eagerly so the selector can inspect
        their capability requirements without running them. The actual
        LLM work only happens inside ``build_tree``.
        """
        repo_name = request.repository_name
        return {
            "hierarchical": HierarchicalStrategy(
                provider=provider,
                config=HierarchicalConfig.from_env(repository_name=repo_name, depth=depth),
            ),
            "long_context_direct": LongContextDirectStrategy(
                provider=provider,
                config=LongContextConfig.from_env(
                    repository_name=repo_name,
                    audience=audience,
                    depth=depth,
                    scope_type=scope_type,
                    scope_path=request.scope_path,
                    snapshot_json=snapshot_json,
                    model_override=model_override,
                ),
            ),
            "single_shot": SingleShotStrategy(
                provider=provider,
                config=SingleShotConfig(
                    repository_name=repo_name,
                    audience=audience,
                    depth=depth,
                    scope_type=scope_type,
                    scope_path=request.scope_path,
                    snapshot_json=snapshot_json,
                    model_override=model_override,
                ),
            ),
        }

    async def _run_cliff_notes_strategy_chain(
        self,
        *,
        provider: LLMProvider | None = None,
        request: knowledge_pb2.GenerateCliffNotesRequest,
        audience: str,
        depth: str,
        scope_type: str,
        model_override: str | None,
        job_logger: SurrealJobLogger | None = None,
        render_meta=None,
    ) -> tuple[CliffNotesResult, LLMUsageRecord, SelectionResult, dict[str, int | bool]]:
        """Walk the preference chain and run the first viable strategy.

        If a strategy passes capability gating but then raises
        :class:`SnapshotTooLargeError` at runtime (the common failure
        mode for ``long_context_direct`` on a corpus that declared a
        fit but didn't actually fit), the exception is recorded and
        the chain advances to the next entry. Other exceptions
        propagate.

        Returns the final result, usage record, and the selector's
        trace so the caller can log why a particular strategy was used.
        """
        provider = provider or self._llm
        # Condense once up-front and share the same snapshot across all
        # strategies in the chain. The hierarchical path still walks
        # the CodeCorpus built from this JSON, so the retrieval /
        # condensation step from the legacy path is preserved.
        query = build_overview_query(
            request.repository_name,
            "cliff_notes",
            scope_type=scope_type,
            scope_path=request.scope_path,
        )
        snapshot = await self._prepare_snapshot(
            request.snapshot_json,
            query,
            scope_type=scope_type,
        )

        chain = _cliff_notes_preference_chain()
        model_id = self._resolve_model_id(model_override)
        strategies = self._build_cliff_notes_strategies(
            request=request,
            provider=provider,
            audience=audience,
            depth=depth,
            scope_type=scope_type,
            model_override=model_override,
            snapshot_json=snapshot,
        )

        last_error: Exception | None = None
        tried: list[str] = []

        # Walk the chain manually so runtime failures (e.g. the long
        # context guard trips on a snapshot that didn't fit after all)
        # can skip to the next viable entry. The selector runs once per
        # iteration so the trace reflects the actual path taken.
        remaining_chain = list(chain)
        while remaining_chain:
            selection = self._selector.select(
                strategies=strategies,
                preference_chain=remaining_chain,
                model_id=model_id,
            )
            if selection.strategy is None:
                if last_error is not None:
                    raise last_error
                raise RuntimeError(f"no viable strategy for model {model_id}: {selection.trace.summary()}")

            name = selection.strategy_name
            tried.append(name)
            try:
                result, usage, diagnostics = await self._run_one_cliff_notes_strategy(
                    provider=provider,
                    strategy=selection.strategy,
                    strategy_name=name,
                    request=request,
                    audience=audience,
                    depth=depth,
                    scope_type=scope_type,
                    model_override=model_override,
                    snapshot_json=snapshot,
                    job_logger=job_logger,
                    render_meta=render_meta,
                )
                return result, usage, selection, diagnostics
            except SnapshotTooLargeError as exc:
                log.warning(
                    "cliff_notes_strategy_runtime_skip",
                    strategy=name,
                    reason=f"snapshot too large: {exc}",
                )
                last_error = exc
                # Drop this strategy from the chain and retry with the
                # next one.
                remaining_chain = [n for n in remaining_chain if n != name]
                continue

        # Chain exhausted without success — re-raise the last error we
        # saw so the caller can translate it into a gRPC status.
        if last_error is not None:
            raise last_error
        raise RuntimeError(f"no strategies succeeded; tried: {','.join(tried)}")

    async def _run_one_cliff_notes_strategy(
        self,
        *,
        provider: LLMProvider | None = None,
        strategy: ComprehensionStrategy,
        strategy_name: str,
        request: knowledge_pb2.GenerateCliffNotesRequest,
        audience: str,
        depth: str,
        scope_type: str,
        model_override: str | None,
        snapshot_json: str,
        job_logger: SurrealJobLogger | None = None,
        render_meta=None,
    ) -> tuple[CliffNotesResult, LLMUsageRecord, dict[str, int | bool]]:
        """Actually run a single strategy and produce the final cliff
        notes result. Kept separate from the chain walker so the logic
        is easy to unit-test."""
        provider = provider or self._llm
        if strategy_name == "hierarchical":
            # Hierarchical: build tree from the CodeCorpus, then render.
            return await self._generate_cliff_notes_hierarchical(
                request=request,
                provider=provider,
                audience=audience,
                depth=depth,
                scope_type=scope_type,
                model_override=model_override,
                snapshot_json=snapshot_json,
                job_logger=job_logger,
                render_meta=render_meta,
            )

        # Single-shot and long-context strategies both produce the
        # final CliffNotesResult directly inside build_tree; they
        # expose it via last_result / last_usage for the caller.
        corpus = CodeCorpus(snapshot=json.loads(snapshot_json))
        await strategy.build_tree(corpus)
        result = getattr(strategy, "last_result", None)
        usage = getattr(strategy, "last_usage", None)
        if result is None or usage is None:
            raise RuntimeError(
                f"strategy {strategy_name!r} did not populate last_result/last_usage",
            )
        return result, usage, {}

    async def _generate_cliff_notes_hierarchical(
        self,
        *,
        provider: LLMProvider | None = None,
        request: knowledge_pb2.GenerateCliffNotesRequest,
        audience: str,
        depth: str,
        scope_type: str,
        model_override: str | None,
        snapshot_json: str | None = None,
        job_logger: SurrealJobLogger | None = None,
        render_meta=None,
    ) -> tuple[CliffNotesResult, LLMUsageRecord, dict[str, int | bool]]:
        """Run the Phase 3 hierarchical pipeline for cliff notes.

        Steps:
          1. Parse the snapshot JSON into a dict (accepts a pre-condensed
             ``snapshot_json`` from the chain walker, or falls back to
             the raw request payload for direct callers).
          2. Wrap it in a CodeCorpus adapter.
          3. Build a SummaryTree with HierarchicalStrategy — each LLM
             call sees only one segment / one file's children / one
             package's children / one repo's children, so the prompt
             always fits even small-context models.
          4. Render the final structured cliff notes from the tree
             via CliffNotesRenderer.
        """
        provider = provider or self._llm
        raw_snapshot = snapshot_json if snapshot_json is not None else request.snapshot_json
        try:
            snapshot_dict = json.loads(raw_snapshot)
        except json.JSONDecodeError as exc:
            raise ValueError(f"snapshot_json is not valid JSON: {exc}") from exc

        corpus = CodeCorpus(snapshot=snapshot_dict)
        by_level = walk_by_level(corpus)
        stage_totals = {
            "leaves": len(by_level.get(0, [])),
            "files": len(by_level.get(1, [])),
            "packages": len(by_level.get(2, [])),
            "root": len(by_level.get(3, [])),
        }
        corpus_revision_fp = corpus.revision_fingerprint()
        cached_tree = None
        if self._summary_node_cache is not None:
            try:
                load_kwargs = {
                    "corpus_id": corpus.corpus_id,
                    "corpus_type": corpus.corpus_type,
                    "strategy": "hierarchical",
                }
                if _supports_kwarg(self._summary_node_cache.load_tree, "depth"):
                    load_kwargs["depth"] = depth
                cached_tree = await self._summary_node_cache.load_tree(**load_kwargs)
                render_only = bool(getattr(render_meta, "render_only", False))
                understanding_depth = str(getattr(render_meta, "understanding_depth", "") or "").strip().lower()
                if (
                    render_only
                    and cached_tree is None
                    and understanding_depth
                    and understanding_depth != depth
                    and _supports_kwarg(self._summary_node_cache.load_tree, "depth")
                ):
                    load_kwargs["depth"] = understanding_depth
                    cached_tree = await self._summary_node_cache.load_tree(**load_kwargs)
            except Exception as exc:
                log.warning(
                    "summary_node_cache_load_failed",
                    repository_id=request.repository_id,
                    corpus_id=corpus.corpus_id,
                    error=str(exc),
                )

        job_state_updater = None
        if self._config is not None and job_logger is not None:
            meta = JobStateMetadata(
                job_id=job_logger.metadata.job_id,
                repo_id=job_logger.metadata.repo_id or request.repository_id,
                artifact_id=job_logger.metadata.artifact_id,
            )
            if not meta.is_empty():
                job_state_updater = SurrealJobStateUpdater.from_config(self._config, meta)

        strategy: HierarchicalStrategy | None = None

        def _resume_checkpoint_payload(
            tree,
            *,
            cached_nodes_loaded: int,
            leaf_cache_hits: int,
            file_cache_hits: int,
            package_cache_hits: int,
            root_cache_hits: int,
        ) -> dict[str, object]:
            completed_counts = {
                "leaves": len(tree.at_level(0)),
                "files": len(tree.at_level(1)),
                "packages": len(tree.at_level(2)),
                "root": len(tree.at_level(3)),
            }
            completed_stages: list[str] = []
            resume_stage = "render"
            for stage_name in ("leaves", "files", "packages", "root"):
                if completed_counts[stage_name] >= stage_totals[stage_name]:
                    completed_stages.append(stage_name)
                    continue
                resume_stage = stage_name
                break
            total_nodes = sum(stage_totals.values())
            tree_status = "complete" if len(tree.nodes) >= total_nodes and total_nodes > 0 else "partial"
            skipped_counts = {
                "leaves": leaf_cache_hits,
                "files": file_cache_hits,
                "packages": package_cache_hits,
                "root": root_cache_hits,
            }
            return {
                "corpus_id": tree.corpus_id,
                "revision_fp": tree.revision_fp,
                "strategy": tree.strategy,
                "resume_stage": resume_stage,
                "completed_stages": completed_stages,
                "completed_counts": completed_counts,
                "total_counts": stage_totals,
                "skipped_counts": skipped_counts,
                "cached_nodes_loaded": cached_nodes_loaded,
                "cached_nodes": len(tree.nodes),
                "total_nodes": total_nodes,
                "tree_status": tree_status,
                "reused_summaries": sum(skipped_counts.values()),
            }

        async def sync_resume_state(tree, *, cached_nodes_loaded: int) -> None:
            if job_state_updater is None:
                return
            current = (
                strategy.diagnostics()
                if strategy is not None
                else {
                    "leaf_cache_hits": 0,
                    "file_cache_hits": 0,
                    "package_cache_hits": 0,
                    "root_cache_hits": 0,
                }
            )
            checkpoint = _resume_checkpoint_payload(
                tree,
                cached_nodes_loaded=cached_nodes_loaded,
                leaf_cache_hits=int(current.get("leaf_cache_hits", 0)),
                file_cache_hits=int(current.get("file_cache_hits", 0)),
                package_cache_hits=int(current.get("package_cache_hits", 0)),
                root_cache_hits=int(current.get("root_cache_hits", 0)),
            )
            await job_state_updater.update_job_resume_state(
                cached_nodes_loaded=int(checkpoint["cached_nodes_loaded"]),
                total_nodes=int(checkpoint["total_nodes"]),
                resume_stage=str(checkpoint["resume_stage"]),
                skipped_leaf_units=int(checkpoint["skipped_counts"]["leaves"]),
                skipped_file_units=int(checkpoint["skipped_counts"]["files"]),
                skipped_package_units=int(checkpoint["skipped_counts"]["packages"]),
                skipped_root_units=int(checkpoint["skipped_counts"]["root"]),
                leaf_cache_hits=int(checkpoint["skipped_counts"]["leaves"]),
                file_cache_hits=int(checkpoint["skipped_counts"]["files"]),
                package_cache_hits=int(checkpoint["skipped_counts"]["packages"]),
                root_cache_hits=int(checkpoint["skipped_counts"]["root"]),
            )
            await job_state_updater.update_understanding_checkpoint(
                corpus_id=str(checkpoint["corpus_id"]),
                revision_fp=str(checkpoint["revision_fp"]),
                strategy=str(checkpoint["strategy"]),
                stage="ready" if str(checkpoint["tree_status"]) == "complete" else "building_tree",
                tree_status=str(checkpoint["tree_status"]),
                cached_nodes=int(checkpoint["cached_nodes"]),
                total_nodes=int(checkpoint["total_nodes"]),
                model_used=model_override or "",
                checkpoint=checkpoint,
            )

        async def persist_stage(stage: str, tree) -> None:
            cached_nodes_loaded = len(cached_tree.nodes) if cached_tree is not None else 0
            if self._summary_node_cache is None:
                await sync_resume_state(tree, cached_nodes_loaded=cached_nodes_loaded)
                return
            try:
                store_kwargs = {"stage": stage}
                if _supports_kwarg(self._summary_node_cache.store_tree, "depth"):
                    store_kwargs["depth"] = depth
                await self._summary_node_cache.store_tree(tree, **store_kwargs)
            except Exception as exc:
                log.warning(
                    "summary_node_cache_store_failed",
                    repository_id=request.repository_id,
                    corpus_id=tree.corpus_id,
                    stage=stage,
                    error=str(exc),
                )
            await sync_resume_state(tree, cached_nodes_loaded=cached_nodes_loaded)

        async def persist_node(stage: str, tree, node) -> None:
            if self._summary_node_cache is None:
                return
            try:
                store_kwargs = {"stage": stage}
                if _supports_kwarg(self._summary_node_cache.store_node, "depth"):
                    store_kwargs["depth"] = depth
                await self._summary_node_cache.store_node(tree, node, **store_kwargs)
            except Exception as exc:
                log.warning(
                    "summary_node_cache_node_store_failed",
                    repository_id=request.repository_id,
                    corpus_id=tree.corpus_id,
                    stage=stage,
                    unit_id=node.unit_id,
                    error=str(exc),
                )

        async def emit_job_log(
            phase: str,
            event: str,
            message: str,
            payload: dict[str, object] | None = None,
        ) -> None:
            if job_logger is None:
                return
            await job_logger.info(
                phase=phase,
                event=event,
                message=message,
                payload=payload,
            )

        cfg = HierarchicalConfig.from_env(
            repository_name=request.repository_name or corpus.root().label,
            depth=depth,
        )
        cfg.cached_tree = cached_tree
        cfg.on_stage_completed = persist_stage
        cfg.on_node_completed = persist_node
        cfg.on_log = emit_job_log
        strategy = HierarchicalStrategy(
            provider=provider,
            config=cfg,
        )

        render_only = bool(getattr(render_meta, "render_only", False))
        selected_section_titles = list(getattr(render_meta, "selected_section_titles", None) or [])
        relevance_profile = str(getattr(render_meta, "relevance_profile", "") or "").strip().lower() or "product_core"
        understanding_depth = str(getattr(render_meta, "understanding_depth", "") or "").strip().lower()

        log.info(
            "cliff_notes_hierarchical_started",
            repository_id=request.repository_id,
            scope_type=scope_type,
            scope_path=request.scope_path,
        )
        await emit_job_log(
            "leaves",
            "cliff_notes_hierarchical_started",
            "Hierarchical cliff notes generation started",
            {
                "repository_id": request.repository_id,
                "scope_type": scope_type,
                "scope_path": request.scope_path,
            },
        )
        if cached_tree is not None and len(cached_tree.nodes) > 0:
            await emit_job_log(
                "resume",
                "summary_node_cache_loaded",
                "Loaded cached summary nodes for resume",
                {
                    "cached_nodes": len(cached_tree.nodes),
                    "resume_stage": _resume_checkpoint_payload(
                        cached_tree,
                        cached_nodes_loaded=len(cached_tree.nodes),
                        leaf_cache_hits=0,
                        file_cache_hits=0,
                        package_cache_hits=0,
                        root_cache_hits=0,
                    )["resume_stage"],
                },
            )
            await sync_resume_state(cached_tree, cached_nodes_loaded=len(cached_tree.nodes))

        try:
            if render_only:
                if cached_tree is None or cached_tree.root() is None:
                    raise RuntimeError(
                        "render-only cliff notes requested without a reusable understanding tree; "
                        "rebuild understanding instead of triggering a hidden hierarchical pass"
                    )
                if corpus_revision_fp and cached_tree.revision_fp and cached_tree.revision_fp != corpus_revision_fp:
                    raise RuntimeError(
                        "render-only cliff notes requested with a stale understanding tree revision; "
                        "rebuild understanding before rendering"
                    )
                tree = cached_tree
                diagnostics = {
                    "fallback_count": 0,
                    "provider_compute_errors": 0,
                    "root_fallback": False,
                    "leaf_cache_hits": 0,
                    "file_cache_hits": 0,
                    "package_cache_hits": 0,
                    "root_cache_hits": 1,
                }
                await emit_job_log(
                    "rerender",
                    "cliff_notes_render_only_reused_tree",
                    "Reused cached summary tree for cliff notes render",
                    {
                        "cached_nodes": len(cached_tree.nodes),
                        "selected_sections": selected_section_titles,
                        "understanding_depth": understanding_depth or depth,
                        "relevance_profile": relevance_profile,
                    },
                )
                await sync_resume_state(tree, cached_nodes_loaded=len(cached_tree.nodes))
            else:
                tree = await strategy.build_tree(corpus, depth=depth)
                diagnostics = strategy.diagnostics()
                await sync_resume_state(
                    tree,
                    cached_nodes_loaded=len(cached_tree.nodes) if cached_tree is not None else 0,
                )

            log.info(
                "cliff_notes_hierarchical_tree_built",
                repository_id=request.repository_id,
                stats=tree.stats(),
                cached_nodes=len(cached_tree.nodes) if cached_tree is not None else 0,
                fallback_count=diagnostics["fallback_count"],
                provider_compute_errors=diagnostics["provider_compute_errors"],
                root_fallback=diagnostics["root_fallback"],
                leaf_cache_hits=diagnostics["leaf_cache_hits"],
                file_cache_hits=diagnostics["file_cache_hits"],
                package_cache_hits=diagnostics["package_cache_hits"],
                root_cache_hits=diagnostics["root_cache_hits"],
            )
            await emit_job_log(
                "llm",
                "cliff_notes_hierarchical_tree_built",
                "Hierarchical summary tree built",
                {
                    "stats": tree.stats(),
                    "cached_nodes": len(cached_tree.nodes) if cached_tree is not None else 0,
                    "fallback_count": diagnostics["fallback_count"],
                    "provider_compute_errors": diagnostics["provider_compute_errors"],
                    "leaf_cache_hits": diagnostics["leaf_cache_hits"],
                    "file_cache_hits": diagnostics["file_cache_hits"],
                    "package_cache_hits": diagnostics["package_cache_hits"],
                    "root_cache_hits": diagnostics["root_cache_hits"],
                },
            )

            total_nodes = max(len(tree.nodes), 1)
            fallback_count = int(diagnostics["fallback_count"])
            if bool(diagnostics["root_fallback"]) or fallback_count / total_nodes >= 0.2:
                raise RuntimeError(
                    "hierarchical summarization degraded due to repeated model backend compute failures "
                    f"(fallback_nodes={fallback_count}, total_nodes={total_nodes})"
                )

            # Extract pre-analysis from enriched snapshot (deep mode injects
            # repository-level cliff notes as _pre_analysis)
            pre_analysis = snapshot_dict.get("_pre_analysis") if isinstance(snapshot_dict, dict) else None

            renderer = CliffNotesRenderer(
                provider=provider,
                model_override=model_override,
            )
            await emit_job_log("llm", "cliff_notes_renderer_started", "Final cliff notes render started", None)
            result, usage = await renderer.render(
                tree,
                repository_name=request.repository_name,
                audience=audience,
                depth=depth,
                scope_type=scope_type,
                scope_path=request.scope_path,
                pre_analysis=pre_analysis,
                required_section_titles=selected_section_titles or None,
                relevance_profile=relevance_profile,
            )

            if usage.operation == "cliff_notes_render_fallback":
                raise RuntimeError("final cliff notes render degraded due to model backend compute failures")

            log.info(
                "cliff_notes_hierarchical_completed",
                repository_id=request.repository_id,
                sections=len(result.sections),
                input_tokens=usage.input_tokens,
                output_tokens=usage.output_tokens,
                fallback_count=fallback_count,
                provider_compute_errors=diagnostics["provider_compute_errors"],
                leaf_cache_hits=diagnostics["leaf_cache_hits"],
                file_cache_hits=diagnostics["file_cache_hits"],
                package_cache_hits=diagnostics["package_cache_hits"],
                root_cache_hits=diagnostics["root_cache_hits"],
            )
            await emit_job_log(
                "ready",
                "cliff_notes_hierarchical_completed",
                "Hierarchical cliff notes generation completed",
                {
                    "sections": len(result.sections),
                    "input_tokens": usage.input_tokens,
                    "output_tokens": usage.output_tokens,
                    "fallback_count": fallback_count,
                    "provider_compute_errors": diagnostics["provider_compute_errors"],
                },
            )
            return (
                result,
                usage,
                {
                    "cached_nodes": len(cached_tree.nodes) if cached_tree is not None else 0,
                    "fallback_count": fallback_count,
                    "provider_compute_errors": diagnostics["provider_compute_errors"],
                    "leaf_cache_hits": diagnostics["leaf_cache_hits"],
                    "file_cache_hits": diagnostics["file_cache_hits"],
                    "package_cache_hits": diagnostics["package_cache_hits"],
                    "root_cache_hits": diagnostics["root_cache_hits"],
                    "root_fallback": diagnostics["root_fallback"],
                    "total_nodes": len(tree.nodes),
                    "corpus_id": tree.corpus_id,
                    "revision_fp": tree.revision_fp,
                    "strategy": tree.strategy,
                    "model_used": usage.model or (model_override or ""),
                },
            )
        finally:
            if job_state_updater is not None:
                await job_state_updater.close()

    async def GenerateCliffNotes(  # noqa: N802
        self,
        request: knowledge_pb2.GenerateCliffNotesRequest,
        context: grpc.aio.ServicerContext,
    ) -> knowledge_pb2.GenerateCliffNotesResponse:
        """Generate cliff notes for a repository from its assembled snapshot."""
        log.info(
            "generate_cliff_notes",
            repository_id=request.repository_id,
            audience=request.audience,
            depth=request.depth,
            scope_type=request.scope_type or "repository",
            scope_path=request.scope_path,
        )

        if not request.snapshot_json:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                "snapshot_json is required",
            )
            return  # type: ignore[return-value]  # abort raises but mypy doesn't know

        audience = request.audience or "developer"
        depth = request.depth or "medium"
        scope_type = request.scope_type or "repository"
        provider, model_override = self._resolve_request_provider(context)
        job_logger = self._resolve_job_logger(context)
        render_meta = resolve_cliff_notes_render_metadata(context)
        if job_logger is not None:
            await job_logger.info(
                phase="snapshot",
                event="generate_cliff_notes_started",
                message="Cliff notes request received by worker",
                payload={
                    "repository_id": request.repository_id,
                    "repository_name": request.repository_name,
                    "audience": audience,
                    "depth": depth,
                    "scope_type": scope_type,
                    "scope_path": request.scope_path,
                    "render_only": bool(render_meta and render_meta.render_only),
                },
            )

        # Run through the StrategySelector with the preference chain from
        # the environment. The selector handles capability gating and
        # records a trace that we emit on every generation for operator
        # visibility.
        try:
            result, usage, selection, diagnostics = await self._run_cliff_notes_strategy_chain(
                request=request,
                provider=provider,
                audience=audience,
                depth=depth,
                scope_type=scope_type,
                model_override=model_override,
                job_logger=job_logger,
                render_meta=render_meta,
            )
        except Exception as exc:
            if job_logger is not None:
                await job_logger.error(
                    phase="failed",
                    event="generate_cliff_notes_failed",
                    message="Cliff notes generation failed in worker",
                    payload={"error": str(exc)},
                )
                await job_logger.close()
            log.error(
                "generate_cliff_notes_failed",
                error=str(exc),
            )
            await context.abort(
                grpc.StatusCode.INTERNAL,
                f"Cliff notes generation failed: {exc}",
            )

        log.info(
            "cliff_notes_strategy_selection",
            strategy=selection.strategy_name,
            trace=selection.trace.summary(),
        )
        if job_logger is not None:
            await job_logger.info(
                phase="llm",
                event="cliff_notes_strategy_selection",
                message=f"Selected strategy {selection.strategy_name}",
                payload={"strategy": selection.strategy_name, "trace": selection.trace.summary()},
            )

        sections = []
        for sec in result.sections:
            evidence = []
            for ev in sec.evidence:
                evidence.append(
                    knowledge_pb2.KnowledgeEvidence(
                        source_type=ev.source_type,
                        source_id=ev.source_id,
                        file_path=ev.file_path,
                        line_start=ev.line_start,
                        line_end=ev.line_end,
                        rationale=ev.rationale,
                    )
                )
            sections.append(
                knowledge_pb2.KnowledgeSection(
                    title=sec.title,
                    content=sec.content,
                    summary=sec.summary,
                    confidence=sec.confidence,
                    inferred=sec.inferred,
                    evidence=evidence,
                    refinement_status=sec.refinement_status,
                )
            )

        response = knowledge_pb2.GenerateCliffNotesResponse(
            sections=sections,
            usage=_llm_usage_proto(usage),
            diagnostics=knowledge_pb2.CliffNotesDiagnostics(
                cached_nodes=int(diagnostics.get("cached_nodes", 0)),
                fallback_count=int(diagnostics.get("fallback_count", 0)),
                provider_compute_errors=int(diagnostics.get("provider_compute_errors", 0)),
                leaf_cache_hits=int(diagnostics.get("leaf_cache_hits", 0)),
                file_cache_hits=int(diagnostics.get("file_cache_hits", 0)),
                package_cache_hits=int(diagnostics.get("package_cache_hits", 0)),
                root_cache_hits=int(diagnostics.get("root_cache_hits", 0)),
                total_nodes=int(diagnostics.get("total_nodes", 0)),
                corpus_id=str(diagnostics.get("corpus_id", "")),
                revision_fp=str(diagnostics.get("revision_fp", "")),
                strategy=str(diagnostics.get("strategy", selection.strategy_name)),
                model_used=str(diagnostics.get("model_used", usage.model or model_override or "")),
            ),
        )
        if job_logger is not None:
            await job_logger.info(
                phase="ready",
                event="generate_cliff_notes_completed",
                message="Cliff notes response ready",
                payload={
                    "sections": len(sections),
                    "input_tokens": usage.input_tokens,
                    "output_tokens": usage.output_tokens,
                },
            )
            await job_logger.close()
        return response

    async def GenerateLearningPath(  # noqa: N802
        self,
        request: knowledge_pb2.GenerateLearningPathRequest,
        context: grpc.aio.ServicerContext,
    ) -> knowledge_pb2.GenerateLearningPathResponse:
        """Generate a learning path for a repository from its assembled snapshot."""
        log.info(
            "generate_learning_path",
            repository_id=request.repository_id,
            audience=request.audience,
            depth=request.depth,
        )

        if not request.snapshot_json:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                "snapshot_json is required",
            )
            return  # type: ignore[return-value]  # abort raises but mypy doesn't know

        audience = request.audience or "developer"
        depth = request.depth or "medium"
        query = build_overview_query(request.repository_name, "learning_path")
        if request.focus_area:
            query = f"{request.focus_area} {query}"
        snapshot = await self._prepare_snapshot(request.snapshot_json, query)

        provider, model_override = self._resolve_request_provider(context)
        job_logger = self._resolve_job_logger(context)
        if job_logger is not None:
            await job_logger.info(
                phase="snapshot",
                event="generate_learning_path_started",
                message="Learning path request received by worker",
                payload={"repository_id": request.repository_id, "depth": depth, "audience": audience},
            )
        try:
            result, usage = await generate_learning_path(
                provider=provider,
                repository_name=request.repository_name,
                audience=audience,
                depth=depth,
                snapshot_json=snapshot,
                focus_area=request.focus_area,
                model_override=model_override,
            )
        except Exception as exc:
            if job_logger is not None:
                await job_logger.error(
                    phase="failed",
                    event="generate_learning_path_failed",
                    message="Learning path generation failed in worker",
                    payload={"error": str(exc)},
                )
                await job_logger.close()
            log.error("generate_learning_path_failed", error=str(exc))
            await context.abort(
                grpc.StatusCode.INTERNAL,
                f"Learning path generation failed: {exc}",
            )

        steps = []
        for step in result.steps:
            steps.append(
                knowledge_pb2.LearningStep(
                    order=step.order,
                    title=step.title,
                    objective=step.objective,
                    content=step.content,
                    file_paths=step.file_paths,
                    symbol_ids=step.symbol_ids,
                    estimated_time=step.estimated_time,
                    prerequisite_steps=step.prerequisite_steps,
                    difficulty=step.difficulty,
                    exercises=step.exercises,
                    checkpoint=step.checkpoint,
                    confidence=step.confidence,
                    refinement_status=step.refinement_status,
                )
            )

        response = knowledge_pb2.GenerateLearningPathResponse(
            steps=steps,
            usage=_llm_usage_proto(usage),
        )
        if job_logger is not None:
            await job_logger.info(
                phase="ready",
                event="generate_learning_path_completed",
                message="Learning path response ready",
                payload={"steps": len(steps), "input_tokens": usage.input_tokens, "output_tokens": usage.output_tokens},
            )
            await job_logger.close()
        return response

    async def GenerateArchitectureDiagram(  # noqa: N802
        self,
        request: knowledge_pb2.GenerateArchitectureDiagramRequest,
        context: grpc.aio.ServicerContext,
    ) -> knowledge_pb2.GenerateArchitectureDiagramResponse:
        """Generate an AI-authored Mermaid architecture diagram."""
        log.info(
            "generate_architecture_diagram",
            repository_id=request.repository_id,
            audience=request.audience,
            depth=request.depth,
        )

        if not request.snapshot_json:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                "snapshot_json is required",
            )
            return  # type: ignore[return-value]

        audience = request.audience or "developer"
        depth = request.depth or "medium"
        query = build_overview_query(request.repository_name, "architecture_diagram")
        snapshot = await self._prepare_snapshot(request.snapshot_json, query)

        provider, model_override = self._resolve_request_provider(context)
        job_logger = self._resolve_job_logger(context)
        if job_logger is not None:
            await job_logger.info(
                phase="snapshot",
                event="generate_architecture_diagram_started",
                message="Architecture diagram request received by worker",
                payload={"repository_id": request.repository_id, "depth": depth, "audience": audience},
            )
        try:
            result, usage = await generate_architecture_diagram(
                provider=provider,
                repository_name=request.repository_name,
                audience=audience,
                depth=depth,
                snapshot_json=snapshot,
                deterministic_diagram_json=request.deterministic_diagram_json,
                model_override=model_override,
            )
        except Exception as exc:
            if job_logger is not None:
                await job_logger.error(
                    phase="failed",
                    event="generate_architecture_diagram_failed",
                    message="Architecture diagram generation failed in worker",
                    payload={"error": str(exc)},
                )
                await job_logger.close()
            log.error("generate_architecture_diagram_failed", error=str(exc))
            await context.abort(
                grpc.StatusCode.INTERNAL,
                f"Architecture diagram generation failed: {exc}",
            )

        evidence = []
        for ev in result.get("evidence", []):
            evidence.append(
                knowledge_pb2.KnowledgeEvidence(
                    source_type=ev.source_type,
                    source_id=ev.source_id,
                    file_path=ev.file_path,
                    line_start=ev.line_start,
                    line_end=ev.line_end,
                    rationale=ev.rationale,
                )
            )
        detail_evidence = []
        for ev in result.get("detail_evidence", []):
            detail_evidence.append(
                knowledge_pb2.KnowledgeEvidence(
                    source_type=ev.source_type,
                    source_id=ev.source_id,
                    file_path=ev.file_path,
                    line_start=ev.line_start,
                    line_end=ev.line_end,
                    rationale=ev.rationale,
                )
            )
        response = knowledge_pb2.GenerateArchitectureDiagramResponse(
            mermaid_source=str(result.get("mermaid_source", "")),
            raw_mermaid_source=str(result.get("raw_mermaid_source", "")),
            validation_status=str(result.get("validation_status", "")),
            repair_summary=str(result.get("repair_summary", "")),
            diagram_summary=str(result.get("diagram_summary", "")),
            evidence=evidence,
            inferred_edges=[str(item) for item in result.get("inferred_edges", [])],
            usage=_llm_usage_proto(usage),
            detail_mermaid_source=str(result.get("detail_mermaid_source", "")),
            detail_raw_mermaid_source=str(result.get("detail_raw_mermaid_source", "")),
            detail_validation_status=str(result.get("detail_validation_status", "")),
            detail_repair_summary=str(result.get("detail_repair_summary", "")),
            detail_diagram_summary=str(result.get("detail_diagram_summary", "")),
            detail_subsystem_name=str(result.get("detail_subsystem_name", "")),
            detail_candidate_subsystems=[str(item) for item in result.get("detail_candidate_subsystems", [])],
            detail_evidence=detail_evidence,
        )
        if job_logger is not None:
            await job_logger.info(
                phase="ready",
                event="generate_architecture_diagram_completed",
                message="Architecture diagram response ready",
                payload={"input_tokens": usage.input_tokens, "output_tokens": usage.output_tokens},
            )
            await job_logger.close()
        return response

    async def GenerateWorkflowStory(  # noqa: N802
        self,
        request: knowledge_pb2.GenerateWorkflowStoryRequest,
        context: grpc.aio.ServicerContext,
    ) -> knowledge_pb2.GenerateWorkflowStoryResponse:
        """Generate a grounded workflow story for a repository scope."""
        log.info(
            "generate_workflow_story",
            repository_id=request.repository_id,
            audience=request.audience,
            depth=request.depth,
            scope_type=request.scope_type or "repository",
            scope_path=request.scope_path,
        )

        if not request.snapshot_json:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                "snapshot_json is required",
            )
            return  # type: ignore[return-value]  # abort raises but mypy doesn't know

        audience = request.audience or "developer"
        depth = request.depth or "medium"
        scope_type = request.scope_type or "repository"
        query = build_overview_query(request.repository_name, "workflow_story")
        if request.anchor_label:
            query = f"{request.anchor_label} {query}"
        snapshot = await self._prepare_snapshot(request.snapshot_json, query, scope_type=scope_type)

        provider, model_override = self._resolve_request_provider(context)
        job_logger = self._resolve_job_logger(context)
        if job_logger is not None:
            await job_logger.info(
                phase="snapshot",
                event="generate_workflow_story_started",
                message="Workflow story request received by worker",
                payload={
                    "repository_id": request.repository_id,
                    "scope_type": scope_type,
                    "scope_path": request.scope_path,
                },
            )
        try:
            result, usage = await generate_workflow_story(
                provider=provider,
                repository_name=request.repository_name,
                audience=audience,
                depth=depth,
                scope_type=scope_type,
                scope_path=request.scope_path,
                anchor_label=request.anchor_label,
                execution_path_json=request.execution_path_json,
                model_override=model_override,
                snapshot_json=snapshot,
            )
        except Exception as exc:
            import traceback

            if job_logger is not None:
                await job_logger.error(
                    phase="failed",
                    event="generate_workflow_story_failed",
                    message="Workflow story generation failed in worker",
                    payload={"error": str(exc)},
                )
                await job_logger.close()
            log.error("generate_workflow_story_failed", error=str(exc), traceback=traceback.format_exc())
            await context.abort(
                grpc.StatusCode.INTERNAL,
                f"Workflow story generation failed: {exc}",
            )

        sections = []
        for sec in result.sections:
            evidence = []
            for ev in sec.evidence:
                evidence.append(
                    knowledge_pb2.KnowledgeEvidence(
                        source_type=ev.source_type,
                        source_id=ev.source_id,
                        file_path=ev.file_path,
                        line_start=ev.line_start,
                        line_end=ev.line_end,
                        rationale=ev.rationale,
                    )
                )
            sections.append(
                knowledge_pb2.KnowledgeSection(
                    title=sec.title,
                    content=sec.content,
                    summary=sec.summary,
                    confidence=sec.confidence,
                    inferred=sec.inferred,
                    evidence=evidence,
                )
            )

        response = knowledge_pb2.GenerateWorkflowStoryResponse(
            sections=sections,
            usage=_llm_usage_proto(usage),
        )
        if job_logger is not None:
            await job_logger.info(
                phase="ready",
                event="generate_workflow_story_completed",
                message="Workflow story response ready",
                payload={
                    "sections": len(sections),
                    "input_tokens": usage.input_tokens,
                    "output_tokens": usage.output_tokens,
                },
            )
            await job_logger.close()
        return response

    async def ExplainSystem(  # noqa: N802
        self,
        request: knowledge_pb2.ExplainSystemRequest,
        context: grpc.aio.ServicerContext,
    ) -> knowledge_pb2.ExplainSystemResponse:
        """Generate a transient whole-system explanation."""
        log.info(
            "explain_system",
            repository_id=request.repository_id,
            audience=request.audience,
        )

        if not request.snapshot_json:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                "snapshot_json is required",
            )
            return  # type: ignore[return-value]  # abort raises but mypy doesn't know

        audience = request.audience or "developer"
        depth = request.depth or "medium"
        # For Q&A, use the actual question for retrieval
        query = request.question or build_overview_query(request.repository_name, "explain")
        snapshot = await self._prepare_snapshot(request.snapshot_json, query)

        provider, model_override = self._resolve_request_provider(context)
        job_logger = self._resolve_job_logger(context)
        if job_logger is not None:
            await job_logger.info(
                phase="snapshot",
                event="explain_system_started",
                message="Explain system request received by worker",
                payload={"repository_id": request.repository_id, "depth": depth, "audience": audience},
            )
        try:
            result, usage = await explain_system(
                provider=provider,
                repository_name=request.repository_name,
                audience=audience,
                depth=depth,
                question=request.question,
                snapshot_json=snapshot,
                model_override=model_override,
            )
        except Exception as exc:
            if job_logger is not None:
                await job_logger.error(
                    phase="failed",
                    event="explain_system_failed",
                    message="Explain system failed in worker",
                    payload={"error": str(exc)},
                )
                await job_logger.close()
            log.error("explain_system_failed", error=str(exc))
            await context.abort(
                grpc.StatusCode.INTERNAL,
                f"System explanation failed: {exc}",
            )

        response = knowledge_pb2.ExplainSystemResponse(
            explanation=result.explanation,
            evidence=[],
            usage=_llm_usage_proto(usage),
        )
        if job_logger is not None:
            await job_logger.info(
                phase="ready",
                event="explain_system_completed",
                message="Explain system response ready",
                payload={"input_tokens": usage.input_tokens, "output_tokens": usage.output_tokens},
            )
            await job_logger.close()
        return response

    async def GenerateCodeTour(  # noqa: N802
        self,
        request: knowledge_pb2.GenerateCodeTourRequest,
        context: grpc.aio.ServicerContext,
    ) -> knowledge_pb2.GenerateCodeTourResponse:
        """Generate a code tour for a repository from its assembled snapshot."""
        log.info(
            "generate_code_tour",
            repository_id=request.repository_id,
            audience=request.audience,
            depth=request.depth,
        )

        if not request.snapshot_json:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                "snapshot_json is required",
            )
            return  # type: ignore[return-value]  # abort raises but mypy doesn't know

        audience = request.audience or "developer"
        depth = request.depth or "medium"
        query = build_overview_query(request.repository_name, "code_tour")
        if request.theme:
            query = f"{request.theme} {query}"
        snapshot = await self._prepare_snapshot(request.snapshot_json, query)

        provider, model_override = self._resolve_request_provider(context)
        job_logger = self._resolve_job_logger(context)
        if job_logger is not None:
            await job_logger.info(
                phase="snapshot",
                event="generate_code_tour_started",
                message="Code tour request received by worker",
                payload={"repository_id": request.repository_id, "depth": depth, "audience": audience},
            )
        try:
            result, usage = await generate_code_tour(
                provider=provider,
                repository_name=request.repository_name,
                audience=audience,
                depth=depth,
                snapshot_json=snapshot,
                theme=request.theme,
                model_override=model_override,
            )
        except Exception as exc:
            if job_logger is not None:
                await job_logger.error(
                    phase="failed",
                    event="generate_code_tour_failed",
                    message="Code tour generation failed in worker",
                    payload={"error": str(exc)},
                )
                await job_logger.close()
            log.error("generate_code_tour_failed", error=str(exc))
            await context.abort(
                grpc.StatusCode.INTERNAL,
                f"Code tour generation failed: {exc}",
            )

        stops = []
        for stop in result.stops:
            stops.append(
                knowledge_pb2.CodeTourStop(
                    order=stop.order,
                    title=stop.title,
                    description=stop.description,
                    file_path=stop.file_path,
                    line_start=stop.line_start,
                    line_end=stop.line_end,
                    trail=stop.trail,
                    modification_hints=stop.modification_hints,
                    confidence=stop.confidence,
                    refinement_status=stop.refinement_status,
                )
            )

        response = knowledge_pb2.GenerateCodeTourResponse(
            stops=stops,
            usage=_llm_usage_proto(usage),
        )
        if job_logger is not None:
            await job_logger.info(
                phase="ready",
                event="generate_code_tour_completed",
                message="Code tour response ready",
                payload={"stops": len(stops), "input_tokens": usage.input_tokens, "output_tokens": usage.output_tokens},
            )
            await job_logger.close()
        return response

    async def GenerateReport(  # noqa: N802
        self,
        request: knowledge_pb2.GenerateReportRequest,
        context: grpc.aio.ServicerContext,
    ) -> knowledge_pb2.GenerateReportResponse:
        """Generate a professional multi-section report."""
        log.info(
            "generate_report",
            report_id=request.report_id,
            report_type=request.report_type,
            audience=request.audience,
            sections=len(request.selected_sections),
        )

        try:
            # Import the report engine (enterprise-only package)
            from workers.reports.engine import ReportConfig, generate_report

            # Parse repo data and section definitions from JSON
            repo_data = None
            if request.repo_data_json:
                with contextlib.suppress(json.JSONDecodeError, TypeError):
                    repo_data = json.loads(request.repo_data_json)

            section_defs = None
            if request.section_definitions_json:
                with contextlib.suppress(json.JSONDecodeError, TypeError):
                    section_defs = json.loads(request.section_definitions_json)

            # Run deep analysis if requested and clone paths are available
            if repo_data and getattr(request, "analysis_depth", "") == "deep":
                try:
                    from workers.reports.analyzer_runner import run_analyzers

                    repo_data = await run_analyzers(repo_data)
                except ImportError:
                    pass  # Enterprise package not installed
                except Exception:
                    log.warning("analyzer pipeline failed, using base data", exc_info=True)

            config = ReportConfig(
                report_id=request.report_id,
                report_name=request.report_name,
                report_type=request.report_type,
                audience=request.audience,
                repository_ids=list(request.repository_ids),
                selected_sections=list(request.selected_sections),
                include_diagrams=request.include_diagrams,
                loe_mode=request.loe_mode or "human_hours",
                output_dir=request.output_dir,
                model_override=request.model_override or None,
                analysis_depth=request.analysis_depth or "standard",
                enable_validation=self._config.report_validation_enabled if self._config else False,
                validation_model=(self._config.llm_validation_model or None) if self._config else None,
                include_recommendations=request.include_recommendations,
                include_loe=request.include_loe,
                style_system_prompt=request.style_system_prompt or "",
                style_section_rules=request.style_section_rules or "",
            )

            report_provider, report_model = self._resolve_report_provider(context)
            if request.model_override:
                report_model = request.model_override
            config.model_override = report_model or None

            result = await generate_report(
                report_provider,
                config,
                repo_data=repo_data,
                section_definitions=section_defs,
            )

            # Build section results
            section_results = []
            for sec in result.sections:
                section_results.append(
                    knowledge_pb2.ReportSectionResult(
                        key=sec.key,
                        title=sec.title,
                        category=sec.category,
                        status="completed",
                        word_count=sec.word_count,
                        duration_ms=0,
                    )
                )

            total_input = sum(s.input_tokens for s in result.sections)
            total_output = sum(s.output_tokens for s in result.sections)

            log.info(
                "generate_report_completed",
                report_id=request.report_id,
                sections=result.section_count,
                words=result.word_count,
                evidence=result.evidence_count,
            )

            return knowledge_pb2.GenerateReportResponse(
                markdown=result.markdown,
                section_count=result.section_count,
                word_count=result.word_count,
                evidence_count=result.evidence_count,
                content_dir=result.content_dir,
                sections=section_results,
                evidence_json=json.dumps(result.evidence_items),
                usage=types_pb2.LLMUsage(
                    model=getattr(report_provider, "model", "unknown"),
                    input_tokens=total_input,
                    output_tokens=total_output,
                    operation="report_generation",
                ),
            )
        except Exception as exc:
            import traceback

            log.error("generate_report_failed", error=str(exc), traceback=traceback.format_exc())
            await context.abort(
                grpc.StatusCode.INTERNAL,
                f"Report generation failed: {exc}",
            )
