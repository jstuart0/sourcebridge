# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""gRPC servicer for the KnowledgeService."""

from __future__ import annotations

import asyncio
import contextlib
import inspect
import json
import os
from collections.abc import AsyncIterator, Callable
from dataclasses import dataclass
from typing import Any

import grpc
import structlog
from common.v1 import knowledge_progress_pb2, types_pb2
from enterprise.v1 import report_pb2
from knowledge.v1 import knowledge_pb2, knowledge_pb2_grpc

from workers.common.config import WorkerConfig
from workers.common.embedding.provider import EmbeddingProvider
from workers.common.grpc_metadata import (
    resolve_cliff_notes_render_metadata,
    resolve_job_log_metadata,
)
from workers.common.llm.provider import LLMProvider, SnapshotTooLargeError
from workers.common.servicer_utils import resolve_provider_for_context
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
from workers.knowledge.parse_utils import coerce_int
from workers.knowledge.proto_enums import resolve_request_audience, resolve_request_depth
from workers.knowledge.retrieval import (
    build_overview_query,
    retrieve_relevant_snapshot,
)
from workers.knowledge.snapshot_truncate import condense_snapshot
from workers.knowledge.streaming import (
    HeartbeatPump,
    phase_marker,
    progress_event,
    run_with_heartbeat,
)
from workers.knowledge.summary_nodes import SurrealSummaryNodeCache
from workers.knowledge.types import CliffNotesResult
from workers.knowledge.workflow_story import generate_workflow_story
from workers.reasoning.types import LLMUsageRecord

log = structlog.get_logger()


def _supports_kwarg(fn: object, name: str) -> bool:
    # Retained for back-compat with extensions / tests; production no longer
    # uses it (all callers pass depth= unconditionally now that SurrealSummaryNodeCache
    # always accepts the param). After two deploys with no extension reports a
    # future cleanup ticket (CA-XXX) can remove this.
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


def _provider_name(provider: LLMProvider) -> str:
    """Return the canonical provider name string for a given LLMProvider instance.

    Resolution order:
    1. ``provider.provider_name`` if the attribute exists and is non-empty
       (set by OpenAICompatProvider and its subclasses).
    2. Class-name heuristic: "anthropic" if the class name contains
       "anthropic", otherwise "unknown".
    """
    name = getattr(provider, "provider_name", None)
    if name:
        return str(name)
    cls_name = type(provider).__name__.lower()
    if "anthropic" in cls_name:
        return "anthropic"
    return "unknown"


def _llm_usage_proto(usage_record, provider: LLMProvider | None = None, *, operation: str = "") -> types_pb2.LLMUsage:
    """Convert an LLMUsageRecord (or duck-typed usage object) to a proto LLMUsage message.

    When *provider* is supplied the ``provider`` field is populated with the
    canonical provider name resolved via :func:`_provider_name`.  If the
    usage record itself carries a non-empty ``provider_name`` (set by the
    LLM adapter — see ``LLMResponse.provider_name``), that value takes
    precedence.

    The *operation* kwarg overrides the record's own ``operation`` field when
    the caller knows a more specific label.
    """
    # Resolution: prefer an explicit "provider" or "provider_name" field on the
    # record (set by LLMUsageRecord or LLMResponse adapters), then fall back to
    # the calling provider context, then leave empty so the Go-side
    # providerFromUsage() heuristic can resolve from the model name.
    # Defense-in-depth: treat the historic "llm" sentinel as missing so it
    # cannot flow into the proto even if a callsite is missed.
    _raw_provider = getattr(usage_record, "provider", None) or getattr(usage_record, "provider_name", None)
    if _raw_provider == "llm":
        _raw_provider = None
    resolved = (
        _raw_provider
        or (provider and _provider_name(provider))
        or ""
    )
    op = operation or getattr(usage_record, "operation", "")
    return types_pb2.LLMUsage(
        model=usage_record.model,
        input_tokens=usage_record.input_tokens,
        output_tokens=usage_record.output_tokens,
        operation=op,
        provider=resolved,
    )


def _resume_checkpoint_payload(
    tree,
    stage_totals: dict[str, int],
    *,
    cached_nodes_loaded: int,
    leaf_cache_hits: int,
    file_cache_hits: int,
    package_cache_hits: int,
    root_cache_hits: int,
) -> dict[str, object]:
    """Build the checkpoint payload dict for a summary tree mid-build.

    Hoisted from _generate_cliff_notes_hierarchical to module scope so it can
    be unit-tested independently.  ``stage_totals`` (previously captured from
    the enclosing scope) is now an explicit parameter.
    """
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


@dataclass
class _HierarchicalCallbacks:
    """Bundle of async callback functions for a single hierarchical pipeline run.

    Built by KnowledgeServicer._build_persistence_callbacks() and consumed by
    _generate_cliff_notes_hierarchical to avoid redefining 4 closures inline.
    """

    persist_stage: Callable[..., Any]
    persist_node: Callable[..., Any]
    emit_job_log: Callable[..., Any]
    sync_resume_state: Callable[..., Any]


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
        # CA-122 / codex r2 H3: per-call strategy plumbing is
        # request-local (see GenerateCliffNotes for the holder
        # pattern); no servicer-instance state for in-flight runs.

    def _resolve_request_provider(self, context: grpc.aio.ServicerContext) -> tuple[LLMProvider, str | None]:
        """Backward-compat wrapper. New code should call resolve_provider_for_context directly."""
        return resolve_provider_for_context(self._llm, self._config, context)

    def _resolve_report_provider(self, context: grpc.aio.ServicerContext) -> tuple[LLMProvider, str | None]:
        """Backward-compat wrapper with self._report_llm fallback."""
        return resolve_provider_for_context(self._llm, self._config, context, fallback_llm=self._report_llm)

    def _build_job_state_updater(
        self,
        job_logger: SurrealJobLogger | None,
        repository_id: str,
    ) -> SurrealJobStateUpdater | None:
        """Build a SurrealJobStateUpdater for the given job logger and repo, or None.

        Extracted from _generate_cliff_notes_hierarchical to remove the inline
        setup block.  Returns None when config is absent or the metadata is empty.
        """
        if self._config is None or job_logger is None:
            return None
        meta = JobStateMetadata(
            job_id=job_logger.metadata.job_id,
            repo_id=job_logger.metadata.repo_id or repository_id,
            artifact_id=job_logger.metadata.artifact_id,
        )
        if meta.is_empty():
            return None
        return SurrealJobStateUpdater.from_config(self._config, meta)

    def _build_persistence_callbacks(
        self,
        *,
        depth: str,
        repository_id: str,
        cached_tree,
        stage_totals: dict[str, int],
        job_state_updater: SurrealJobStateUpdater | None,
        job_logger: SurrealJobLogger | None,
        model_override: str | None,
        strategy_holder: list,
    ) -> _HierarchicalCallbacks:
        """Build the async callback bundle for a single hierarchical pipeline run.

        ``strategy_holder`` must be a single-element list whose slot is filled
        with the HierarchicalStrategy instance once it is created. The
        sync_resume_state callback reads strategy_holder[0] at call time, so
        it sees the strategy even though it is assigned after this method returns.
        """
        cache = self._summary_node_cache

        async def sync_resume_state(tree, *, cached_nodes_loaded: int) -> None:
            if job_state_updater is None:
                return
            strat = strategy_holder[0]
            current = (
                strat.diagnostics()
                if strat is not None
                else {
                    "leaf_cache_hits": 0,
                    "file_cache_hits": 0,
                    "package_cache_hits": 0,
                    "root_cache_hits": 0,
                }
            )
            checkpoint: dict[str, Any] = _resume_checkpoint_payload(
                tree,
                stage_totals,
                cached_nodes_loaded=cached_nodes_loaded,
                leaf_cache_hits=coerce_int(current.get("leaf_cache_hits", 0)),
                file_cache_hits=coerce_int(current.get("file_cache_hits", 0)),
                package_cache_hits=coerce_int(current.get("package_cache_hits", 0)),
                root_cache_hits=coerce_int(current.get("root_cache_hits", 0)),
            )
            skipped_counts = checkpoint.get("skipped_counts")
            skipped = skipped_counts if isinstance(skipped_counts, dict) else {}
            await job_state_updater.update_job_resume_state(
                cached_nodes_loaded=coerce_int(checkpoint.get("cached_nodes_loaded")),
                total_nodes=coerce_int(checkpoint.get("total_nodes")),
                resume_stage=str(checkpoint["resume_stage"]),
                skipped_leaf_units=coerce_int(skipped.get("leaves")),
                skipped_file_units=coerce_int(skipped.get("files")),
                skipped_package_units=coerce_int(skipped.get("packages")),
                skipped_root_units=coerce_int(skipped.get("root")),
                leaf_cache_hits=coerce_int(skipped.get("leaves")),
                file_cache_hits=coerce_int(skipped.get("files")),
                package_cache_hits=coerce_int(skipped.get("packages")),
                root_cache_hits=coerce_int(skipped.get("root")),
            )
            await job_state_updater.update_understanding_checkpoint(
                corpus_id=str(checkpoint["corpus_id"]),
                revision_fp=str(checkpoint["revision_fp"]),
                strategy=str(checkpoint["strategy"]),
                stage="ready" if str(checkpoint["tree_status"]) == "complete" else "building_tree",
                tree_status=str(checkpoint["tree_status"]),
                cached_nodes=coerce_int(checkpoint.get("cached_nodes")),
                total_nodes=coerce_int(checkpoint.get("total_nodes")),
                model_used=model_override or "",
                checkpoint=checkpoint,
            )

        async def persist_stage(stage: str, tree) -> None:
            cached_nodes_loaded = len(cached_tree.nodes) if cached_tree is not None else 0
            if cache is None:
                await sync_resume_state(tree, cached_nodes_loaded=cached_nodes_loaded)
                return
            try:
                await cache.store_tree(tree, stage=stage, depth=depth)
            except Exception as exc:
                log.warning(
                    "summary_node_cache_store_failed",
                    repository_id=repository_id,
                    corpus_id=tree.corpus_id,
                    stage=stage,
                    error=str(exc),
                )
            await sync_resume_state(tree, cached_nodes_loaded=cached_nodes_loaded)

        async def persist_node(stage: str, tree, node) -> None:
            if cache is None:
                return
            try:
                await cache.store_node(tree, node, stage=stage, depth=depth)
            except Exception as exc:
                log.warning(
                    "summary_node_cache_node_store_failed",
                    repository_id=repository_id,
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

        return _HierarchicalCallbacks(
            persist_stage=persist_stage,
            persist_node=persist_node,
            emit_job_log=emit_job_log,
            sync_resume_state=sync_resume_state,
        )

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
            except (json.JSONDecodeError, TypeError) as exc:
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
        on_strategy_ready=None,
        on_phase=None,
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
                    on_strategy_ready=on_strategy_ready,
                    on_phase=on_phase,
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
        on_strategy_ready=None,
        on_phase=None,
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
                on_strategy_ready=on_strategy_ready,
                on_phase=on_phase,
            )
        # codex r2 M3: non-hierarchical strategies (single_shot,
        # long_context_direct) collapse the SNAPSHOT -> RENDER ->
        # FINALIZING flow. Signal the RENDER transition before the
        # blocking strategy.build_tree call so the Go-side bucket
        # advances correctly.
        if on_phase is not None:
            try:
                on_phase("render")
            except Exception:
                # Phase callbacks must never break generation.
                log.exception("on_phase callback raised; ignoring")

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

    async def _load_cached_tree(
        self,
        *,
        corpus: CodeCorpus,
        depth: str,
        repository_id: str,
        render_meta,
    ):
        """Load a cached SummaryTree for the corpus, or return None.

        Tries the requested depth first; if render_only is requested for a
        different understanding_depth and the primary load misses, falls back
        to the understanding depth.  Extracted from _generate_cliff_notes_hierarchical.
        """
        if self._summary_node_cache is None:
            return None
        try:
            load_kwargs = {
                "corpus_id": corpus.corpus_id,
                "corpus_type": corpus.corpus_type,
                "strategy": "hierarchical",
                "depth": depth,
            }
            cached_tree = await self._summary_node_cache.load_tree(**load_kwargs)
            render_only = bool(getattr(render_meta, "render_only", False))
            understanding_depth = str(getattr(render_meta, "understanding_depth", "") or "").strip().lower()
            if (
                render_only
                and cached_tree is None
                and understanding_depth
                and understanding_depth != depth
            ):
                load_kwargs["depth"] = understanding_depth
                cached_tree = await self._summary_node_cache.load_tree(**load_kwargs)
            return cached_tree
        except Exception as exc:
            log.warning(
                "summary_node_cache_load_failed",
                repository_id=repository_id,
                corpus_id=corpus.corpus_id,
                error=str(exc),
            )
            return None

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
        on_strategy_ready=None,
        on_phase=None,
    ) -> tuple[CliffNotesResult, LLMUsageRecord, dict[str, int | bool]]:
        """Run the Phase 3 hierarchical pipeline for cliff notes.

        Orchestrates snapshot parsing, cached-tree loading, callback setup, and
        strategy construction, then delegates to _execute_hierarchical_pipeline
        for the actual tree-build + render stages.
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
        cached_tree = await self._load_cached_tree(
            corpus=corpus,
            depth=depth,
            repository_id=request.repository_id,
            render_meta=render_meta,
        )

        job_state_updater = self._build_job_state_updater(job_logger, request.repository_id)
        # strategy_holder[0] is written after _build_persistence_callbacks returns
        # so sync_resume_state reads the strategy via late-binding closure.
        strategy_holder: list[HierarchicalStrategy | None] = [None]
        cbs = self._build_persistence_callbacks(
            depth=depth,
            repository_id=request.repository_id,
            cached_tree=cached_tree,
            stage_totals=stage_totals,
            job_state_updater=job_state_updater,
            job_logger=job_logger,
            model_override=model_override,
            strategy_holder=strategy_holder,
        )

        cfg = HierarchicalConfig.from_env(
            repository_name=request.repository_name or corpus.root().label,
            depth=depth,
        )
        cfg.cached_tree = cached_tree
        cfg.on_stage_completed = cbs.persist_stage
        cfg.on_node_completed = cbs.persist_node
        cfg.on_log = cbs.emit_job_log
        strategy = HierarchicalStrategy(
            provider=provider,
            config=cfg,
        )
        strategy_holder[0] = strategy
        # CA-122 / codex r2 H3: publish the strategy via the per-call
        # callback (request-local; no servicer-instance state). The
        # GenerateCliffNotes streaming method captures a per-call
        # holder list in its closure and the heartbeat pump reads
        # progress_snapshot() from it. Concurrent cliff-notes streams
        # therefore see only their own strategy.
        if on_strategy_ready is not None:
            on_strategy_ready(strategy)

        return await self._execute_hierarchical_pipeline(
            request=request,
            provider=provider,
            audience=audience,
            depth=depth,
            scope_type=scope_type,
            model_override=model_override,
            snapshot_dict=snapshot_dict,
            corpus=corpus,
            cached_tree=cached_tree,
            corpus_revision_fp=corpus_revision_fp,
            stage_totals=stage_totals,
            cbs=cbs,
            strategy=strategy,
            job_state_updater=job_state_updater,
            render_meta=render_meta,
            on_phase=on_phase,
        )

    async def _execute_hierarchical_pipeline(
        self,
        *,
        request: knowledge_pb2.GenerateCliffNotesRequest,
        provider: LLMProvider,
        audience: str,
        depth: str,
        scope_type: str,
        model_override: str | None,
        snapshot_dict: dict,
        corpus: CodeCorpus,
        cached_tree,
        corpus_revision_fp: str,
        stage_totals: dict[str, int],
        cbs: _HierarchicalCallbacks,
        strategy: HierarchicalStrategy,
        job_state_updater: SurrealJobStateUpdater | None,
        render_meta,
        on_phase,
    ) -> tuple[CliffNotesResult, LLMUsageRecord, dict[str, int | bool]]:
        """Execute the tree-build + render stages of the hierarchical pipeline.

        Extracted from _generate_cliff_notes_hierarchical to keep that outer
        method under 100 lines.  All local state needed by the stages is passed
        explicitly; no shared mutable state is used outside the callbacks bundle.
        """
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
        await cbs.emit_job_log(
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
            await cbs.emit_job_log(
                "resume",
                "summary_node_cache_loaded",
                "Loaded cached summary nodes for resume",
                {
                    "cached_nodes": len(cached_tree.nodes),
                    "resume_stage": _resume_checkpoint_payload(
                        cached_tree,
                        stage_totals,
                        cached_nodes_loaded=len(cached_tree.nodes),
                        leaf_cache_hits=0,
                        file_cache_hits=0,
                        package_cache_hits=0,
                        root_cache_hits=0,
                    )["resume_stage"],
                },
            )
            await cbs.sync_resume_state(cached_tree, cached_nodes_loaded=len(cached_tree.nodes))

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
                diagnostics: dict[str, Any] = {
                    "fallback_count": 0,
                    "provider_compute_errors": 0,
                    "root_fallback": False,
                    "leaf_cache_hits": 0,
                    "file_cache_hits": 0,
                    "package_cache_hits": 0,
                    "root_cache_hits": 1,
                }
                await cbs.emit_job_log(
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
                await cbs.sync_resume_state(tree, cached_nodes_loaded=len(cached_tree.nodes))
            else:
                tree = await strategy.build_tree(corpus, depth=depth)
                diagnostics = strategy.diagnostics()
                await cbs.sync_resume_state(
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
            await cbs.emit_job_log(
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
            await cbs.emit_job_log("llm", "cliff_notes_renderer_started", "Final cliff notes render started", None)
            # codex r2 M3: signal the RENDER phase transition BEFORE
            # the slow renderer.render LLM call. Without this the
            # outer GenerateCliffNotes only emits RENDER after
            # _run_cliff_notes_strategy_chain returns, which is too
            # late — the slow render work is reported under the prior
            # hierarchical phase label and the progress bar stays in
            # the PACKAGE_SUMMARIES / ROOT_SYNTHESIS buckets while
            # rendering. on_phase is best-effort: the streaming
            # generator drains it via a side-channel queue, but if
            # the queue is full or the callback raises we never block
            # the work task.
            if on_phase is not None:
                try:
                    on_phase("render")
                except Exception:
                    log.exception("on_phase callback raised; ignoring")
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
            await cbs.emit_job_log(
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
    ) -> AsyncIterator[knowledge_pb2.KnowledgeServiceGenerateCliffNotesResponse]:
        """Generate cliff notes for a repository from its assembled snapshot.

        CA-122: Server-streaming. Emits phase markers at every coarse
        boundary (snapshot -> leaf/file/package/root for hierarchical
        runs -> render -> finalizing -> final response). For
        repository-scope hierarchical runs, a HeartbeatPump emits
        progress events every ~30s carrying run-time
        completed_units/total_units counters scoped per phase.

        Cancellation contract: client cancellation propagates into
        ctx.Cancelled; the strategy chain is wrapped in a task that is
        cancelled and awaited (with a 5s bound) in the finally block.
        Per codex r1 C3, we DO NOT shield the strategy task — that
        would prevent client cancellation from reaching it.
        """
        audience = resolve_request_audience(request)
        depth = resolve_request_depth(request)
        log.info(
            "generate_cliff_notes",
            repository_id=request.repository_id,
            audience=audience,
            depth=depth,
            scope_type=request.scope_type or "repository",
            scope_path=request.scope_path,
        )

        if not request.snapshot_json:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                "snapshot_json is required",
            )
            return

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

        # Phase: SNAPSHOT
        yield knowledge_pb2.KnowledgeServiceGenerateCliffNotesResponse(
            phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_SNAPSHOT)
        )

        # Spawn the strategy chain in a task so the heartbeat pump can
        # poll progress while it runs, and so cancellation can tear it
        # down deterministically. The strategy chain calls
        # on_strategy_ready(strategy) when a hierarchical strategy is
        # dispatched; the closure below captures it so concurrent
        # cliff-notes streams cannot read each other's strategy
        # (codex r2 H3).
        active_strategy: list[Any] = [None]  # holder; set by callback below

        def _on_strategy_ready(strategy_obj):
            active_strategy[0] = strategy_obj

        # codex r2 M3: in-band phase signals from the strategy chain.
        # The strategy code calls on_phase("render") just before the
        # slow renderer.render LLM call so the Go-side stream driver
        # can advance the bucketed progress floor at the actual
        # transition. Pending phases queue up here and the streaming
        # generator drains them on the next heartbeat tick.
        pending_phases: list[str] = []

        def _on_phase(phase_name: str) -> None:
            pending_phases.append(phase_name)

        # codex r2 M5: track whether job_logger.close() has already
        # run on the success path so the finally block doesn't
        # double-close.
        job_logger_closed = [False]

        work_task: asyncio.Task | None = None
        try:
            work_task = asyncio.create_task(
                self._run_cliff_notes_strategy_chain(
                    request=request,
                    provider=provider,
                    audience=audience,
                    depth=depth,
                    scope_type=scope_type,
                    model_override=model_override,
                    job_logger=job_logger,
                    render_meta=render_meta,
                    on_strategy_ready=_on_strategy_ready,
                    on_phase=_on_phase,
                ),
                name=f"cliff-notes-{request.repository_id}",
            )

            # codex r2 M3: do NOT emit LEAF_SUMMARIES eagerly here.
            # Whether the chain settles on the hierarchical strategy is
            # only known once on_strategy_ready fires (capability
            # gating happens inside _run_cliff_notes_strategy_chain).
            # For non-repository scopes / single-shot / long-context
            # strategies, LEAF_SUMMARIES has no bucket on the Go-side
            # collapsed map (bucketMin == 0) which would regress the
            # progress bar from snapshot bucket-min back to zero.
            # Instead, emit LEAF_SUMMARIES from inside the heartbeat
            # loop as soon as a hierarchical strategy is observed; the
            # heartbeat pump's first iteration runs after a short
            # interval, by which point the strategy chain will have
            # selected and published.

            def _snapshot_fn() -> dict:
                strat = active_strategy[0]
                if strat is None or not hasattr(strat, "progress_snapshot"):
                    # Non-hierarchical strategy or not yet started.
                    return {
                        "phase": "",
                        "completed_units": 0,
                        "total_units": 0,
                        "unit_kind": "",
                        "message": "",
                    }
                return strat.progress_snapshot()

            leaf_summaries_emitted = False
            phase_name_to_proto = {
                "render": knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER,
                "leaf_summaries": knowledge_progress_pb2.KNOWLEDGE_PHASE_LEAF_SUMMARIES,
                "file_summaries": knowledge_progress_pb2.KNOWLEDGE_PHASE_FILE_SUMMARIES,
                "package_summaries": knowledge_progress_pb2.KNOWLEDGE_PHASE_PACKAGE_SUMMARIES,
                "root_synthesis": knowledge_progress_pb2.KNOWLEDGE_PHASE_ROOT_SYNTHESIS,
            }
            # codex r2b M3: hierarchical phase markers (LEAF_SUMMARIES
            # / FILE_SUMMARIES / PACKAGE_SUMMARIES / ROOT_SYNTHESIS)
            # only have a Go-side bucket on the hierarchical bucket
            # map, which is selected only for repository-scope
            # cliff-notes artifacts (see rpcBucketForArtifact in
            # internal/api/graphql/knowledge_stream_driver.go). For
            # file/module/symbol scopes the Go driver uses the
            # collapsed map and bucketMin returns 0 for those phases,
            # which would regress the progress bar from the snapshot
            # floor. We therefore only surface hierarchical phase
            # markers when scope_type == "repository". Non-repository
            # scopes follow the collapsed contract: SNAPSHOT -> RENDER
            # -> FINALIZING. Render is in both bucket maps so it's
            # always safe.
            emit_hierarchical_phases = scope_type == "repository"
            pump = HeartbeatPump(_snapshot_fn)
            async for prog in pump.run_until(work_task):
                # codex r2 M3 + r2b: emit LEAF_SUMMARIES exactly once,
                # the first time we observe a hierarchical strategy
                # in play, AND only on repository scope (see comment
                # above for why).
                if (
                    emit_hierarchical_phases
                    and not leaf_summaries_emitted
                    and active_strategy[0] is not None
                    and hasattr(active_strategy[0], "progress_snapshot")
                ):
                    yield knowledge_pb2.KnowledgeServiceGenerateCliffNotesResponse(
                        phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_LEAF_SUMMARIES)
                    )
                    leaf_summaries_emitted = True
                # codex r2 M3: drain any in-band phase transitions
                # the strategy chain pushed via _on_phase. This is
                # what surfaces the RENDER transition just before
                # renderer.render, instead of waiting until the
                # whole strategy chain returns. RENDER is in both
                # bucket maps so it's always safe to surface; the
                # hierarchical-only phases are gated by scope.
                while pending_phases:
                    name = pending_phases.pop(0)
                    if (
                        not emit_hierarchical_phases
                        and name in {"leaf_summaries", "file_summaries", "package_summaries", "root_synthesis"}
                    ):
                        # Skip hierarchical phase markers on non-
                        # repository scopes (see r2b M3 above).
                        continue
                    proto_phase = phase_name_to_proto.get(name)
                    if proto_phase is not None:
                        yield knowledge_pb2.KnowledgeServiceGenerateCliffNotesResponse(
                            phase=phase_marker(proto_phase)
                        )
                # codex r2b M3 + r2c: when we are NOT emitting
                # hierarchical phase markers, the heartbeat pump's
                # progress event may carry a hierarchical phase and
                # phase-local completed/total counters from
                # strategy.progress_snapshot(). The Go driver maps
                # those against the collapsed bucket map. Force phase
                # to UNSPECIFIED so the bucket isn't recomputed each
                # tick, AND zero the counters so the Go driver parks
                # at bucket-min (no fake fractional motion). The
                # stream still keeps UpdatedAt fresh because the
                # progress event itself is enough heartbeat for the
                # reaper. The collapsed-scope progress contract is
                # therefore: SNAPSHOT bucket-min -> RENDER bucket-min
                # -> FINALIZING bucket-min, with phase markers driving
                # the only motion. Non-monotonic phase-local counters
                # would have produced 20% -> 5% regressions otherwise
                # (codex r2c Medium).
                if not emit_hierarchical_phases:
                    prog.phase = knowledge_progress_pb2.KNOWLEDGE_PHASE_UNSPECIFIED
                    prog.completed_units = 0
                    prog.total_units = 0
                    prog.unit_kind = ""
                yield knowledge_pb2.KnowledgeServiceGenerateCliffNotesResponse(progress=prog)

            # Work task is done. Await it to surface the result or
            # exception; never blocks because pump exited only when
            # task.done() returned True.
            result, usage, selection, diagnostics = await work_task

        except asyncio.CancelledError:
            log.warning(
                "cliff_notes_stream_cancelled",
                repository_id=request.repository_id,
            )
            if job_logger is not None:
                with contextlib.suppress(Exception):
                    await job_logger.warn(
                        phase="cancelled",
                        event="generate_cliff_notes_cancelled",
                        message="Stream cancelled by client; tearing down strategy task",
                        payload={"repository_id": request.repository_id},
                    )
            raise
        except Exception as exc:
            log.error("generate_cliff_notes_failed", error=str(exc))
            if job_logger is not None:
                with contextlib.suppress(Exception):
                    await job_logger.error(
                        phase="failed",
                        event="generate_cliff_notes_failed",
                        message="Cliff notes generation failed in worker",
                        payload={"error": str(exc)},
                    )
            await context.abort(
                grpc.StatusCode.INTERNAL,
                f"Cliff notes generation failed: {exc}",
            )
            return
        finally:
            # CA-122 / codex r1 C3: ALWAYS run cleanup. Cancel the
            # strategy task with a bounded wait so a hung LLM call
            # cannot deadlock the servicer.
            if work_task is not None and not work_task.done():
                work_task.cancel()
                try:
                    await asyncio.wait_for(work_task, timeout=5.0)
                except asyncio.CancelledError:
                    pass
                except TimeoutError:
                    log.error(
                        "cliff_notes_strategy_cleanup_timeout",
                        repository_id=request.repository_id,
                    )
                except Exception:
                    log.exception(
                        "cliff_notes_strategy_cleanup_error",
                        repository_id=request.repository_id,
                    )
            # codex r2 M5: job_logger MUST close on every path. The
            # success path closes it inline before yielding the final
            # message; here we close on cancellation/error paths so
            # buffered logs flush deterministically.
            if job_logger is not None and not job_logger_closed[0]:
                with contextlib.suppress(Exception):
                    await job_logger.close()
                    job_logger_closed[0] = True

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

        # Phase: RENDER
        yield knowledge_pb2.KnowledgeServiceGenerateCliffNotesResponse(
            phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER)
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

        # Phase: FINALIZING
        yield knowledge_pb2.KnowledgeServiceGenerateCliffNotesResponse(
            phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_FINALIZING)
        )

        response = knowledge_pb2.GenerateCliffNotesResponse(
            sections=sections,
            usage=_llm_usage_proto(usage, provider),
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
            job_logger_closed[0] = True

        # Final terminal message — must be last.
        yield knowledge_pb2.KnowledgeServiceGenerateCliffNotesResponse(final=response)

    async def GenerateLearningPath(  # noqa: N802
        self,
        request: knowledge_pb2.GenerateLearningPathRequest,
        context: grpc.aio.ServicerContext,
    ) -> AsyncIterator[knowledge_pb2.KnowledgeServiceGenerateLearningPathResponse]:
        """Generate a learning path for a repository from its snapshot.

        CA-122: Server-streaming. Single-call generator -- no internal
        step-by-step counters -- so emits phase markers only:
        SNAPSHOT -> RENDER -> FINALIZING -> final. Per Decision 4b,
        total_units stays 0 for these RPCs (no fake counter math).
        """
        audience = resolve_request_audience(request)
        depth = resolve_request_depth(request)
        log.info(
            "generate_learning_path",
            repository_id=request.repository_id,
            audience=audience,
            depth=depth,
        )

        if not request.snapshot_json:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                "snapshot_json is required",
            )
            return

        # Phase: SNAPSHOT
        yield knowledge_pb2.KnowledgeServiceGenerateLearningPathResponse(
            phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_SNAPSHOT)
        )

        query = build_overview_query(request.repository_name, "learning_path")
        if request.focus_area:
            query = f"{request.focus_area} {query}"
        snapshot = await self._prepare_snapshot(request.snapshot_json, query)

        provider, model_override = self._resolve_request_provider(context)
        job_logger = self._resolve_job_logger(context)
        # codex r2b M4: outer try/finally so job_logger.close() runs
        # even on cancellation between work_task success and the
        # FINALIZING / final-response yields. Inner branches set
        # job_logger_closed=True to prevent double-close.
        job_logger_closed = [False]
        try:
            if job_logger is not None:
                await job_logger.info(
                    phase="snapshot",
                    event="generate_learning_path_started",
                    message="Learning path request received by worker",
                    payload={"repository_id": request.repository_id, "depth": depth, "audience": audience},
                )

            # Phase: RENDER
            yield knowledge_pb2.KnowledgeServiceGenerateLearningPathResponse(
                phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER)
            )

            # codex r2 C1: spawn the LLM call as a task and pump phase-only
            # heartbeats so the orchestrator's UpdatedAt stays fresh
            # through long single-LLM-call generations. Never shields the
            # work task — cancellation propagates upward and the
            # try/finally tears down the task.
            work_task = asyncio.create_task(
                generate_learning_path(
                    provider=provider,
                    repository_name=request.repository_name,
                    audience=audience,
                    depth=depth,
                    snapshot_json=snapshot,
                    focus_area=request.focus_area,
                    model_override=model_override,
                ),
                name=f"learning-path-{request.repository_id}",
            )
            try:
                async for prog in run_with_heartbeat(
                    work_task,
                    phase=knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER,
                    message="Generating learning path",
                ):
                    yield knowledge_pb2.KnowledgeServiceGenerateLearningPathResponse(progress=prog)
                result, usage = await work_task
            except asyncio.CancelledError:
                log.warning("learning_path_stream_cancelled", repository_id=request.repository_id)
                if not work_task.done():
                    work_task.cancel()
                    with contextlib.suppress(asyncio.CancelledError, TimeoutError, Exception):
                        await asyncio.wait_for(work_task, timeout=5.0)
                if job_logger is not None:
                    with contextlib.suppress(Exception):
                        await job_logger.warn(
                            phase="cancelled",
                            event="generate_learning_path_cancelled",
                            message="Stream cancelled by client",
                            payload={"repository_id": request.repository_id},
                        )
                raise
            except Exception as exc:
                if job_logger is not None:
                    with contextlib.suppress(Exception):
                        await job_logger.error(
                            phase="failed",
                            event="generate_learning_path_failed",
                            message="Learning path generation failed in worker",
                            payload={"error": str(exc)},
                        )
                log.error("generate_learning_path_failed", error=str(exc))
                await context.abort(
                    grpc.StatusCode.INTERNAL,
                    f"Learning path generation failed: {exc}",
                )
                return

            # Phase: FINALIZING
            yield knowledge_pb2.KnowledgeServiceGenerateLearningPathResponse(
                phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_FINALIZING)
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
                usage=_llm_usage_proto(usage, provider),
            )
            if job_logger is not None:
                await job_logger.info(
                    phase="ready",
                    event="generate_learning_path_completed",
                    message="Learning path response ready",
                    payload={"steps": len(steps), "input_tokens": usage.input_tokens, "output_tokens": usage.output_tokens},
                )
                await job_logger.close()
                job_logger_closed[0] = True
            yield knowledge_pb2.KnowledgeServiceGenerateLearningPathResponse(final=response)
        finally:
            if job_logger is not None and not job_logger_closed[0]:
                with contextlib.suppress(Exception):
                    await job_logger.close()
                    job_logger_closed[0] = True

    async def GenerateArchitectureDiagram(  # noqa: N802
        self,
        request: knowledge_pb2.GenerateArchitectureDiagramRequest,
        context: grpc.aio.ServicerContext,
    ) -> AsyncIterator[knowledge_pb2.KnowledgeServiceGenerateArchitectureDiagramResponse]:
        """Generate an AI-authored Mermaid architecture diagram.

        CA-122: Server-streaming. Single-call generator.
        Phases: SNAPSHOT -> RENDER -> FINALIZING -> final.
        """
        audience = resolve_request_audience(request)
        depth = resolve_request_depth(request)
        log.info(
            "generate_architecture_diagram",
            repository_id=request.repository_id,
            audience=audience,
            depth=depth,
        )

        if not request.snapshot_json:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                "snapshot_json is required",
            )
            return

        # Phase: SNAPSHOT
        yield knowledge_pb2.KnowledgeServiceGenerateArchitectureDiagramResponse(
            phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_SNAPSHOT)
        )

        query = build_overview_query(request.repository_name, "architecture_diagram")
        snapshot = await self._prepare_snapshot(request.snapshot_json, query)

        provider, model_override = self._resolve_request_provider(context)
        job_logger = self._resolve_job_logger(context)
        # codex r2b M4: outer try/finally so job_logger.close() runs
        # even on cancellation between work_task success and the
        # FINALIZING / final-response yields.
        job_logger_closed = [False]
        try:
            if job_logger is not None:
                await job_logger.info(
                    phase="snapshot",
                    event="generate_architecture_diagram_started",
                    message="Architecture diagram request received by worker",
                    payload={"repository_id": request.repository_id, "depth": depth, "audience": audience},
                )

            # Phase: RENDER
            yield knowledge_pb2.KnowledgeServiceGenerateArchitectureDiagramResponse(
                phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER)
            )

            # codex r2 C1: heartbeat the LLM call so the orchestrator's
            # UpdatedAt stays fresh; the 10-min reaper would otherwise
            # kill healthy long generations.
            work_task = asyncio.create_task(
                generate_architecture_diagram(
                    provider=provider,
                    repository_name=request.repository_name,
                    audience=audience,
                    depth=depth,
                    snapshot_json=snapshot,
                    deterministic_diagram_json=request.deterministic_diagram_json,
                    model_override=model_override,
                ),
                name=f"arch-diagram-{request.repository_id}",
            )
            try:
                async for prog in run_with_heartbeat(
                    work_task,
                    phase=knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER,
                    message="Generating architecture diagram",
                ):
                    yield knowledge_pb2.KnowledgeServiceGenerateArchitectureDiagramResponse(progress=prog)
                result, usage = await work_task
            except asyncio.CancelledError:
                log.warning("architecture_diagram_stream_cancelled", repository_id=request.repository_id)
                if not work_task.done():
                    work_task.cancel()
                    with contextlib.suppress(asyncio.CancelledError, TimeoutError, Exception):
                        await asyncio.wait_for(work_task, timeout=5.0)
                raise
            except Exception as exc:
                if job_logger is not None:
                    with contextlib.suppress(Exception):
                        await job_logger.error(
                            phase="failed",
                            event="generate_architecture_diagram_failed",
                            message="Architecture diagram generation failed in worker",
                            payload={"error": str(exc)},
                        )
                log.error("generate_architecture_diagram_failed", error=str(exc))
                await context.abort(
                    grpc.StatusCode.INTERNAL,
                    f"Architecture diagram generation failed: {exc}",
                )
                return

            # Phase: FINALIZING
            yield knowledge_pb2.KnowledgeServiceGenerateArchitectureDiagramResponse(
                phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_FINALIZING)
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
                usage=_llm_usage_proto(usage, provider),
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
                job_logger_closed[0] = True
            yield knowledge_pb2.KnowledgeServiceGenerateArchitectureDiagramResponse(final=response)
        finally:
            if job_logger is not None and not job_logger_closed[0]:
                with contextlib.suppress(Exception):
                    await job_logger.close()
                    job_logger_closed[0] = True

    async def GenerateWorkflowStory(  # noqa: N802
        self,
        request: knowledge_pb2.GenerateWorkflowStoryRequest,
        context: grpc.aio.ServicerContext,
    ) -> AsyncIterator[knowledge_pb2.KnowledgeServiceGenerateWorkflowStoryResponse]:
        """Generate a grounded workflow story for a repository scope.

        CA-122: Server-streaming. Single-call generator.
        Phases: SNAPSHOT -> RENDER -> FINALIZING -> final.
        """
        audience = resolve_request_audience(request)
        depth = resolve_request_depth(request)
        log.info(
            "generate_workflow_story",
            repository_id=request.repository_id,
            audience=audience,
            depth=depth,
            scope_type=request.scope_type or "repository",
            scope_path=request.scope_path,
        )

        if not request.snapshot_json:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                "snapshot_json is required",
            )
            return

        # Phase: SNAPSHOT
        yield knowledge_pb2.KnowledgeServiceGenerateWorkflowStoryResponse(
            phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_SNAPSHOT)
        )

        scope_type = request.scope_type or "repository"
        query = build_overview_query(request.repository_name, "workflow_story")
        if request.anchor_label:
            query = f"{request.anchor_label} {query}"
        snapshot = await self._prepare_snapshot(request.snapshot_json, query, scope_type=scope_type)

        provider, model_override = self._resolve_request_provider(context)
        job_logger = self._resolve_job_logger(context)
        # codex r2b M4: outer try/finally so job_logger.close() runs
        # even on cancellation between work_task success and the
        # FINALIZING / final-response yields.
        job_logger_closed = [False]
        try:
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

            # Phase: RENDER
            yield knowledge_pb2.KnowledgeServiceGenerateWorkflowStoryResponse(
                phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER)
            )

            # codex r2 C1: heartbeat the LLM call so the orchestrator's
            # UpdatedAt stays fresh; the 10-min reaper would otherwise
            # kill healthy long generations.
            work_task = asyncio.create_task(
                generate_workflow_story(
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
                ),
                name=f"workflow-story-{request.repository_id}",
            )
            try:
                async for prog in run_with_heartbeat(
                    work_task,
                    phase=knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER,
                    message="Generating workflow story",
                ):
                    yield knowledge_pb2.KnowledgeServiceGenerateWorkflowStoryResponse(progress=prog)
                result, usage = await work_task
            except asyncio.CancelledError:
                log.warning("workflow_story_stream_cancelled", repository_id=request.repository_id)
                if not work_task.done():
                    work_task.cancel()
                    with contextlib.suppress(asyncio.CancelledError, TimeoutError, Exception):
                        await asyncio.wait_for(work_task, timeout=5.0)
                raise
            except Exception as exc:
                import traceback

                if job_logger is not None:
                    with contextlib.suppress(Exception):
                        await job_logger.error(
                            phase="failed",
                            event="generate_workflow_story_failed",
                            message="Workflow story generation failed in worker",
                            payload={"error": str(exc)},
                        )
                log.error("generate_workflow_story_failed", error=str(exc), traceback=traceback.format_exc())
                await context.abort(
                    grpc.StatusCode.INTERNAL,
                    f"Workflow story generation failed: {exc}",
                )
                return

            # Phase: FINALIZING
            yield knowledge_pb2.KnowledgeServiceGenerateWorkflowStoryResponse(
                phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_FINALIZING)
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
                usage=_llm_usage_proto(usage, provider),
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
                job_logger_closed[0] = True
            yield knowledge_pb2.KnowledgeServiceGenerateWorkflowStoryResponse(final=response)
        finally:
            if job_logger is not None and not job_logger_closed[0]:
                with contextlib.suppress(Exception):
                    await job_logger.close()
                    job_logger_closed[0] = True

    async def ExplainSystem(  # noqa: N802
        self,
        request: knowledge_pb2.ExplainSystemRequest,
        context: grpc.aio.ServicerContext,
    ) -> AsyncIterator[knowledge_pb2.KnowledgeServiceExplainSystemResponse]:
        """Generate a transient whole-system explanation.

        CA-122: Server-streaming. Single-call generator.
        Phases: SNAPSHOT -> RENDER -> FINALIZING -> final.
        """
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
            return

        # Phase: SNAPSHOT
        yield knowledge_pb2.KnowledgeServiceExplainSystemResponse(
            phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_SNAPSHOT)
        )

        audience = request.audience or "developer"
        depth = request.depth or "medium"
        query = request.question or build_overview_query(request.repository_name, "explain")
        snapshot = await self._prepare_snapshot(request.snapshot_json, query)

        provider, model_override = self._resolve_request_provider(context)
        job_logger = self._resolve_job_logger(context)
        # codex r2b M4: outer try/finally so job_logger.close() runs
        # even on cancellation between work_task success and the
        # FINALIZING / final-response yields.
        job_logger_closed = [False]
        try:
            if job_logger is not None:
                await job_logger.info(
                    phase="snapshot",
                    event="explain_system_started",
                    message="Explain system request received by worker",
                    payload={"repository_id": request.repository_id, "depth": depth, "audience": audience},
                )

            # Phase: RENDER
            yield knowledge_pb2.KnowledgeServiceExplainSystemResponse(
                phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER)
            )

            # codex r2 C1: heartbeat the LLM call so the orchestrator's
            # UpdatedAt stays fresh; the 10-min reaper would otherwise
            # kill healthy long generations.
            work_task = asyncio.create_task(
                explain_system(
                    provider=provider,
                    repository_name=request.repository_name,
                    audience=audience,
                    depth=depth,
                    question=request.question,
                    snapshot_json=snapshot,
                    model_override=model_override,
                ),
                name=f"explain-system-{request.repository_id}",
            )
            try:
                async for prog in run_with_heartbeat(
                    work_task,
                    phase=knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER,
                    message="Explaining system",
                ):
                    yield knowledge_pb2.KnowledgeServiceExplainSystemResponse(progress=prog)
                result, usage = await work_task
            except asyncio.CancelledError:
                log.warning("explain_system_stream_cancelled", repository_id=request.repository_id)
                if not work_task.done():
                    work_task.cancel()
                    with contextlib.suppress(asyncio.CancelledError, TimeoutError, Exception):
                        await asyncio.wait_for(work_task, timeout=5.0)
                raise
            except Exception as exc:
                if job_logger is not None:
                    with contextlib.suppress(Exception):
                        await job_logger.error(
                            phase="failed",
                            event="explain_system_failed",
                            message="Explain system failed in worker",
                            payload={"error": str(exc)},
                        )
                log.error("explain_system_failed", error=str(exc))
                await context.abort(
                    grpc.StatusCode.INTERNAL,
                    f"System explanation failed: {exc}",
                )
                return

            # Phase: FINALIZING
            yield knowledge_pb2.KnowledgeServiceExplainSystemResponse(
                phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_FINALIZING)
            )

            response = knowledge_pb2.ExplainSystemResponse(
                explanation=result.explanation,
                evidence=[],
                usage=_llm_usage_proto(usage, provider),
            )
            if job_logger is not None:
                await job_logger.info(
                    phase="ready",
                    event="explain_system_completed",
                    message="Explain system response ready",
                    payload={"input_tokens": usage.input_tokens, "output_tokens": usage.output_tokens},
                )
                await job_logger.close()
                job_logger_closed[0] = True
            yield knowledge_pb2.KnowledgeServiceExplainSystemResponse(final=response)
        finally:
            if job_logger is not None and not job_logger_closed[0]:
                with contextlib.suppress(Exception):
                    await job_logger.close()
                    job_logger_closed[0] = True

    async def GenerateCodeTour(  # noqa: N802
        self,
        request: knowledge_pb2.GenerateCodeTourRequest,
        context: grpc.aio.ServicerContext,
    ) -> AsyncIterator[knowledge_pb2.KnowledgeServiceGenerateCodeTourResponse]:
        """Generate a code tour for a repository from its assembled snapshot.

        CA-122: Server-streaming. Single-call generator.
        Phases: SNAPSHOT -> RENDER -> FINALIZING -> final.
        """
        audience = resolve_request_audience(request)
        depth = resolve_request_depth(request)
        log.info(
            "generate_code_tour",
            repository_id=request.repository_id,
            audience=audience,
            depth=depth,
        )

        if not request.snapshot_json:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                "snapshot_json is required",
            )
            return

        # Phase: SNAPSHOT
        yield knowledge_pb2.KnowledgeServiceGenerateCodeTourResponse(
            phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_SNAPSHOT)
        )

        query = build_overview_query(request.repository_name, "code_tour")
        if request.theme:
            query = f"{request.theme} {query}"
        snapshot = await self._prepare_snapshot(request.snapshot_json, query)

        provider, model_override = self._resolve_request_provider(context)
        job_logger = self._resolve_job_logger(context)
        # codex r2b M4: outer try/finally so job_logger.close() runs
        # even on cancellation between work_task success and the
        # FINALIZING / final-response yields.
        job_logger_closed = [False]
        try:
            if job_logger is not None:
                await job_logger.info(
                    phase="snapshot",
                    event="generate_code_tour_started",
                    message="Code tour request received by worker",
                    payload={"repository_id": request.repository_id, "depth": depth, "audience": audience},
                )

            # Phase: RENDER
            yield knowledge_pb2.KnowledgeServiceGenerateCodeTourResponse(
                phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER)
            )

            # codex r2 C1: heartbeat the LLM call so the orchestrator's
            # UpdatedAt stays fresh; the 10-min reaper would otherwise
            # kill healthy long generations.
            work_task = asyncio.create_task(
                generate_code_tour(
                    provider=provider,
                    repository_name=request.repository_name,
                    audience=audience,
                    depth=depth,
                    snapshot_json=snapshot,
                    theme=request.theme,
                    model_override=model_override,
                ),
                name=f"code-tour-{request.repository_id}",
            )
            try:
                async for prog in run_with_heartbeat(
                    work_task,
                    phase=knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER,
                    message="Generating code tour",
                ):
                    yield knowledge_pb2.KnowledgeServiceGenerateCodeTourResponse(progress=prog)
                result, usage = await work_task
            except asyncio.CancelledError:
                log.warning("code_tour_stream_cancelled", repository_id=request.repository_id)
                if not work_task.done():
                    work_task.cancel()
                    with contextlib.suppress(asyncio.CancelledError, TimeoutError, Exception):
                        await asyncio.wait_for(work_task, timeout=5.0)
                raise
            except Exception as exc:
                if job_logger is not None:
                    with contextlib.suppress(Exception):
                        await job_logger.error(
                            phase="failed",
                            event="generate_code_tour_failed",
                            message="Code tour generation failed in worker",
                            payload={"error": str(exc)},
                        )
                log.error("generate_code_tour_failed", error=str(exc))
                await context.abort(
                    grpc.StatusCode.INTERNAL,
                    f"Code tour generation failed: {exc}",
                )
                return

            # Phase: FINALIZING
            yield knowledge_pb2.KnowledgeServiceGenerateCodeTourResponse(
                phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_FINALIZING)
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
                usage=_llm_usage_proto(usage, provider),
            )
            if job_logger is not None:
                await job_logger.info(
                    phase="ready",
                    event="generate_code_tour_completed",
                    message="Code tour response ready",
                    payload={"stops": len(stops), "input_tokens": usage.input_tokens, "output_tokens": usage.output_tokens},
                )
                await job_logger.close()
                job_logger_closed[0] = True
            yield knowledge_pb2.KnowledgeServiceGenerateCodeTourResponse(final=response)
        finally:
            if job_logger is not None and not job_logger_closed[0]:
                with contextlib.suppress(Exception):
                    await job_logger.close()
                    job_logger_closed[0] = True

    async def _generate_report(
        self,
        request: Any,
        context: grpc.aio.ServicerContext,
    ) -> AsyncIterator[report_pb2.EnterpriseReportServiceGenerateReportResponse]:
        """Generate a professional multi-section report.

        CA-122: Server-streaming. The report engine already exposes a
        fine-grained progress callback (workers/reports/engine.py
        signature: ProgressCallback = Callable[[float, str, str],
        Awaitable[None] | None]). We bridge it into the stream as
        KnowledgeStreamProgress events with unit_kind="report_progress",
        quantizing the 0.0-1.0 fraction to (round(fraction * 1000), 1000)
        so the orchestrator's bucket-mapped progress bar gets
        fine-grained motion through the RENDER bucket.
        """
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

            # Phase: SNAPSHOT
            yield report_pb2.EnterpriseReportServiceGenerateReportResponse(
                phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_SNAPSHOT)
            )

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

            # Phase: RENDER
            yield report_pb2.EnterpriseReportServiceGenerateReportResponse(
                phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER)
            )

            # Bridge the engine's progress callback into a queue the
            # outer generator drains as it goes. The callback runs on
            # the engine's task; we never block on it -- queue.put_nowait
            # drops on an absent reader, so a slow consumer doesn't
            # become engine backpressure.
            from workers.knowledge.streaming import engine_phase_for

            progress_queue: asyncio.Queue[
                knowledge_progress_pb2.KnowledgeStreamProgress
            ] = asyncio.Queue(maxsize=64)

            async def _engine_progress(fraction: float, phase_name: str, message: str) -> None:
                evt = progress_event(
                    phase=engine_phase_for(phase_name),
                    completed_units=round(max(0.0, min(1.0, fraction)) * 1000),
                    total_units=1000,
                    unit_kind="report_progress",
                    message=message or "",
                )
                with contextlib.suppress(asyncio.QueueFull):
                    progress_queue.put_nowait(evt)

            # Run the engine in a background task so we can drain
            # progress events alongside it. The engine accepts a
            # `progress` keyword (workers/reports/engine.py:2129).
            engine_task = asyncio.create_task(
                generate_report(
                    report_provider,
                    config,
                    repo_data=repo_data,
                    section_definitions=section_defs,
                    progress=_engine_progress,
                ),
                name=f"report-engine-{request.report_id}",
            )
            try:
                # codex r2 C1: track wall-clock since last engine
                # progress event so we can emit a fallback heartbeat
                # when the engine is mid-LLM-call and not pushing
                # events. Cadence: heartbeat_interval_secs() (default
                # 30s, env-tunable), distinct from the 1s queue poll.
                from workers.knowledge.streaming import heartbeat_interval_secs
                hb_interval = heartbeat_interval_secs()
                last_event_at = asyncio.get_running_loop().time()
                while not engine_task.done():
                    try:
                        evt = await asyncio.wait_for(progress_queue.get(), timeout=1.0)
                        yield report_pb2.EnterpriseReportServiceGenerateReportResponse(progress=evt)
                        last_event_at = asyncio.get_running_loop().time()
                    except TimeoutError:
                        # No engine event in the last second. Emit a
                        # phase-only heartbeat if it has been
                        # >= hb_interval seconds since the last real
                        # event so the orchestrator's UpdatedAt stays
                        # fresh.
                        now = asyncio.get_running_loop().time()
                        if now - last_event_at >= hb_interval:
                            yield report_pb2.EnterpriseReportServiceGenerateReportResponse(
                                progress=progress_event(
                                    phase=knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER,
                                    completed_units=0,
                                    total_units=0,
                                    unit_kind="report_progress",
                                    message="Generating report",
                                )
                            )
                            last_event_at = now
                        continue
                # Drain any remaining progress events the engine pushed
                # right before completing.
                while not progress_queue.empty():
                    yield report_pb2.EnterpriseReportServiceGenerateReportResponse(progress=progress_queue.get_nowait())
                # Surface engine result or exception.
                result = await engine_task
            except asyncio.CancelledError:
                log.warning("report_stream_cancelled", report_id=request.report_id)
                raise
            finally:
                if not engine_task.done():
                    engine_task.cancel()
                    try:
                        await asyncio.wait_for(engine_task, timeout=5.0)
                    except asyncio.CancelledError:
                        pass
                    except TimeoutError:
                        log.error("report_engine_cleanup_timeout", report_id=request.report_id)
                    except Exception:
                        log.exception("report_engine_cleanup_error", report_id=request.report_id)

            # Phase: FINALIZING
            yield report_pb2.EnterpriseReportServiceGenerateReportResponse(
                phase=phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_FINALIZING)
            )

            # Build section results
            section_results = []
            for sec in result.sections:
                section_results.append(
                    report_pb2.ReportSectionResult(
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

            response = report_pb2.GenerateReportResponse(
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
                    provider=_provider_name(report_provider),
                ),
            )
            yield report_pb2.EnterpriseReportServiceGenerateReportResponse(final=response)
        except asyncio.CancelledError:
            raise
        except Exception as exc:
            import traceback

            log.error("generate_report_failed", error=str(exc), traceback=traceback.format_exc())
            await context.abort(
                grpc.StatusCode.INTERNAL,
                f"Report generation failed: {exc}",
            )
            return
