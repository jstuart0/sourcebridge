# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Streaming helpers for the seven server-streaming knowledge / report
RPCs (CA-122).

This module hosts:

  * `HeartbeatPump` -- yields `KnowledgeStreamProgress` messages on a
    fixed cadence by polling a snapshot accessor. The caller is
    responsible for spawning and cancelling the underlying work task;
    the pump never shields the task and never awaits it (the caller
    awaits exactly once after the pump exits, so an exception is
    surfaced exactly once).

  * `phase_marker(phase)` / `progress_event(...)` -- small builders for
    the shared `KnowledgeStreamPhaseMarker` and `KnowledgeStreamProgress`
    proto messages.

  * `phase_for(name)` -- maps the internal hierarchical-strategy phase
    string ("leaves", "files", "packages", "root", "ready") into a
    `KnowledgePhase` enum value.

  * `engine_phase_for(name)` -- maps the enterprise report engine's
    phase strings ("collecting", "analyzing", "generating",
    "synthesizing", "validating", "assembling", "rendering", "ready")
    into the same enum.

The cancellation contract is the load-bearing piece: every servicer
method that uses `HeartbeatPump` MUST wrap the pump + work-task pair in
`try/finally` and cancel + bounded-await the task on cancellation /
exception. See `workers/knowledge/servicer.py` for the canonical
pattern.
"""

from __future__ import annotations

import asyncio
import contextlib
import os
from collections.abc import AsyncIterator, Callable

from common.v1 import knowledge_progress_pb2

DEFAULT_HEARTBEAT_SECS = 30.0
_HEARTBEAT_ENV = "SOURCEBRIDGE_KNOWLEDGE_STREAM_HEARTBEAT_SECS"


# Mapping from the existing hierarchical-strategy progress callback
# stage names (workers/comprehension/hierarchical.py:261, 305, 346, 387,
# 439) to the proto KnowledgePhase enum.
_HIERARCHICAL_PHASE_MAP = {
    "leaves": knowledge_progress_pb2.KNOWLEDGE_PHASE_LEAF_SUMMARIES,
    "files": knowledge_progress_pb2.KNOWLEDGE_PHASE_FILE_SUMMARIES,
    "packages": knowledge_progress_pb2.KNOWLEDGE_PHASE_PACKAGE_SUMMARIES,
    "root": knowledge_progress_pb2.KNOWLEDGE_PHASE_ROOT_SYNTHESIS,
    "ready": knowledge_progress_pb2.KNOWLEDGE_PHASE_FINALIZING,
}


# Mapping from the enterprise report engine's progress phase strings
# (workers/reports/engine.py:2225-2405) to the proto KnowledgePhase
# enum. The engine emits much finer-grained phases than our coarse
# enum; we collapse most into RENDER per Decision 4b in the plan.
_ENGINE_PHASE_MAP = {
    "collecting": knowledge_progress_pb2.KNOWLEDGE_PHASE_SNAPSHOT,
    "analyzing": knowledge_progress_pb2.KNOWLEDGE_PHASE_SNAPSHOT,
    "generating": knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER,
    "synthesizing": knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER,
    "validating": knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER,
    "assembling": knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER,
    "rendering": knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER,
    "ready": knowledge_progress_pb2.KNOWLEDGE_PHASE_FINALIZING,
}


def phase_for(name: str) -> int:
    """Map a hierarchical-strategy phase name to the proto enum.

    Returns KNOWLEDGE_PHASE_UNSPECIFIED for unrecognized names so a
    typo at a callsite is benign rather than crash-worthy.
    """
    return _HIERARCHICAL_PHASE_MAP.get(name, knowledge_progress_pb2.KNOWLEDGE_PHASE_UNSPECIFIED)


def engine_phase_for(name: str) -> int:
    """Map a report-engine phase name to the proto enum.

    Returns KNOWLEDGE_PHASE_RENDER for unrecognized names because the
    engine only emits phase names during the render pass; an unknown
    name almost certainly means "we're somewhere mid-render."
    """
    return _ENGINE_PHASE_MAP.get(name, knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER)


def phase_marker(
    phase: int,
    detail: str = "",
) -> knowledge_progress_pb2.KnowledgeStreamPhaseMarker:
    """Build a KnowledgeStreamPhaseMarker for the given phase enum."""
    return knowledge_progress_pb2.KnowledgeStreamPhaseMarker(phase=phase, detail=detail)


def progress_event(
    *,
    phase: int,
    completed_units: int = 0,
    total_units: int = 0,
    unit_kind: str = "",
    message: str = "",
    leaf_cache_hits: int = 0,
    file_cache_hits: int = 0,
    package_cache_hits: int = 0,
    root_cache_hits: int = 0,
) -> knowledge_progress_pb2.KnowledgeStreamProgress:
    """Build a KnowledgeStreamProgress message with the given fields."""
    return knowledge_progress_pb2.KnowledgeStreamProgress(
        phase=phase,
        completed_units=completed_units,
        total_units=total_units,
        unit_kind=unit_kind,
        message=message,
        leaf_cache_hits=leaf_cache_hits,
        file_cache_hits=file_cache_hits,
        package_cache_hits=package_cache_hits,
        root_cache_hits=root_cache_hits,
    )


def progress_from_snapshot(snap: dict) -> knowledge_progress_pb2.KnowledgeStreamProgress:
    """Build a KnowledgeStreamProgress from a strategy.progress_snapshot()
    dict. Translates the strategy's internal phase string to the proto
    enum.
    """
    return progress_event(
        phase=phase_for(snap.get("phase", "")),
        completed_units=int(snap.get("completed_units", 0) or 0),
        total_units=int(snap.get("total_units", 0) or 0),
        unit_kind=str(snap.get("unit_kind", "") or ""),
        message=str(snap.get("message", "") or ""),
        leaf_cache_hits=int(snap.get("leaf_cache_hits", 0) or 0),
        file_cache_hits=int(snap.get("file_cache_hits", 0) or 0),
        package_cache_hits=int(snap.get("package_cache_hits", 0) or 0),
        root_cache_hits=int(snap.get("root_cache_hits", 0) or 0),
    )


def heartbeat_interval_secs() -> float:
    """Resolve the heartbeat cadence in seconds.

    Honors SOURCEBRIDGE_KNOWLEDGE_STREAM_HEARTBEAT_SECS for operator
    override; falls back to DEFAULT_HEARTBEAT_SECS (30s). Invalid env
    values fall back to the default with no error -- the env knob is a
    soft preference.
    """
    raw = os.environ.get(_HEARTBEAT_ENV, "")
    if not raw:
        return DEFAULT_HEARTBEAT_SECS
    try:
        v = float(raw)
        if v <= 0:
            return DEFAULT_HEARTBEAT_SECS
        return v
    except (TypeError, ValueError):
        return DEFAULT_HEARTBEAT_SECS


async def run_with_heartbeat(work_task, phase, message, *, interval_secs=None):
    """Yield periodic phase-only progress messages while a single
    work task runs. Designed for single-call RPCs (LearningPath,
    ArchitectureDiagram, etc.) that have no internal step-by-step
    counters but still need to keep the orchestrator's UpdatedAt
    fresh so the 10-min reaper does not kill a healthy long LLM
    call (codex r2 C1).

    Each yielded KnowledgeStreamProgress carries phase=<phase>,
    total_units=0, unit_kind="" (empty per Decision 4b), and the
    supplied message. The Go-side stream driver treats total_units
    == 0 as "phase-only progress" and parks the bar at the bucket
    minimum; the heartbeat's role is purely keeping the heartbeat
    fresh and letting the UI confirm work is in progress.

    Cancellation contract identical to HeartbeatPump.run_until: never
    shields the work task; outer cancellation propagates upward; the
    caller's try/finally is responsible for cancelling and bounded-
    awaiting the work task.
    """
    interval = interval_secs if interval_secs is not None else heartbeat_interval_secs()
    loop = asyncio.get_running_loop()
    while not work_task.done():
        sleep_task = loop.create_task(asyncio.sleep(interval))
        try:
            done, _ = await asyncio.wait(
                {work_task, sleep_task},
                return_when=asyncio.FIRST_COMPLETED,
            )
        except asyncio.CancelledError:
            sleep_task.cancel()
            with contextlib.suppress(BaseException):
                await sleep_task
            raise
        if sleep_task not in done:
            sleep_task.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await sleep_task
        if work_task.done():
            break
        yield progress_event(
            phase=phase,
            completed_units=0,
            total_units=0,
            unit_kind="",
            message=message,
        )


class HeartbeatPump:
    """Yields heartbeat progress messages on a fixed cadence.

    Usage (the canonical pattern; servicer wraps in try/finally):

        snapshot_fn = lambda: strategy.progress_snapshot()
        pump = HeartbeatPump(snapshot_fn)
        work_task = asyncio.create_task(do_work(...), name="work")
        try:
            async for progress in pump.run_until(work_task):
                yield <wrap progress in stream message>
            result = await work_task  # surfaces work_task's exception
        finally:
            if not work_task.done():
                work_task.cancel()
                try:
                    await asyncio.wait_for(work_task, timeout=5.0)
                except (asyncio.CancelledError, asyncio.TimeoutError):
                    pass
                except Exception:
                    log.exception("work_task cleanup error")

    Critical invariants:

      * The pump does NOT shield work_task. Cancellation of the
        surrounding generator propagates upward; the caller's
        try/finally is responsible for cancelling the task. Shielding
        was rejected as a Critical (codex r1 C3) because it would
        prevent client-cancellation from reaching the strategy.
      * The pump RACES task completion against a sleep
        (asyncio.wait, FIRST_COMPLETED) so an early task completion
        exits the loop immediately rather than blocking up to a full
        interval.
      * The pump observes the work task but never awaits it. The
        caller awaits exactly once after the pump exits so the
        exception is surfaced exactly once (codex r1b M4).
    """

    def __init__(
        self,
        snapshot_fn: Callable[[], dict],
        interval_secs: float | None = None,
    ) -> None:
        self._snapshot = snapshot_fn
        self._interval = interval_secs if interval_secs is not None else heartbeat_interval_secs()

    async def run_until(
        self,
        task: asyncio.Task,
    ) -> AsyncIterator[knowledge_progress_pb2.KnowledgeStreamProgress]:
        """Yield a heartbeat every `interval_secs` until `task` is done.

        Yields nothing if the task completes before the first interval.
        """
        loop = asyncio.get_running_loop()
        while not task.done():
            sleep_task = loop.create_task(asyncio.sleep(self._interval))
            try:
                done, _ = await asyncio.wait(
                    {task, sleep_task},
                    return_when=asyncio.FIRST_COMPLETED,
                )
            except asyncio.CancelledError:
                # The caller's generator (or its outer scope) was
                # cancelled. Cancel the sleep_task so we don't leak
                # it; the work_task is left intact for the caller's
                # try/finally to handle.
                sleep_task.cancel()
                # Suppress the inner cancel error from sleep_task to
                # avoid masking the upstream cause. Catch BaseException
                # too (covers timeouts and the rare KeyboardInterrupt
                # that asyncio can surface during shutdown).
                with contextlib.suppress(BaseException):
                    await sleep_task
                raise
            # Cancel whichever didn't complete.
            if sleep_task not in done:
                sleep_task.cancel()
                with contextlib.suppress(asyncio.CancelledError):
                    await sleep_task
            if task.done():
                # Task finished first. Exit cleanly without emitting a
                # final heartbeat — the caller will yield phase markers
                # + the final terminal message on the way out.
                break
            # Sleep elapsed; emit a heartbeat from the current snapshot.
            try:
                snap = self._snapshot()
            except Exception:  # pragma: no cover -- defensive
                # Snapshot accessor raised; skip this beat. We'd rather
                # silently drop one heartbeat than abort the stream.
                continue
            yield progress_from_snapshot(snap)
