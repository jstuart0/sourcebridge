# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""CA-122 Phase 8: streaming-RPC tests for the worker side.

Covers the HeartbeatPump's contract (no-shield, race-against-sleep,
clean exit when work completes), the strategy.progress_snapshot()
accessor, and the cancellation pattern in HeartbeatPump.
"""

from __future__ import annotations

import asyncio

import pytest
from common.v1 import knowledge_progress_pb2

from workers.knowledge.streaming import (
    DEFAULT_HEARTBEAT_SECS,
    HeartbeatPump,
    engine_phase_for,
    heartbeat_interval_secs,
    phase_for,
    phase_marker,
    progress_event,
    progress_from_snapshot,
)


def test_phase_for_known_names():
    assert phase_for("leaves") == knowledge_progress_pb2.KNOWLEDGE_PHASE_LEAF_SUMMARIES
    assert phase_for("files") == knowledge_progress_pb2.KNOWLEDGE_PHASE_FILE_SUMMARIES
    assert phase_for("packages") == knowledge_progress_pb2.KNOWLEDGE_PHASE_PACKAGE_SUMMARIES
    assert phase_for("root") == knowledge_progress_pb2.KNOWLEDGE_PHASE_ROOT_SYNTHESIS
    assert phase_for("ready") == knowledge_progress_pb2.KNOWLEDGE_PHASE_FINALIZING


def test_phase_for_unknown_returns_unspecified():
    assert phase_for("") == knowledge_progress_pb2.KNOWLEDGE_PHASE_UNSPECIFIED
    assert phase_for("nonsense") == knowledge_progress_pb2.KNOWLEDGE_PHASE_UNSPECIFIED


def test_engine_phase_for_collecting_is_snapshot():
    assert engine_phase_for("collecting") == knowledge_progress_pb2.KNOWLEDGE_PHASE_SNAPSHOT
    assert engine_phase_for("analyzing") == knowledge_progress_pb2.KNOWLEDGE_PHASE_SNAPSHOT


def test_engine_phase_for_render_aliases():
    assert engine_phase_for("generating") == knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER
    assert engine_phase_for("synthesizing") == knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER
    assert engine_phase_for("validating") == knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER
    assert engine_phase_for("assembling") == knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER
    assert engine_phase_for("rendering") == knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER


def test_engine_phase_for_ready():
    assert engine_phase_for("ready") == knowledge_progress_pb2.KNOWLEDGE_PHASE_FINALIZING


def test_engine_phase_for_unknown_defaults_to_render():
    # Per-comment: unknown phases during a report run almost certainly
    # mean "we're somewhere mid-render."
    assert engine_phase_for("nonsense") == knowledge_progress_pb2.KNOWLEDGE_PHASE_RENDER


def test_phase_marker_construction():
    pm = phase_marker(knowledge_progress_pb2.KNOWLEDGE_PHASE_LEAF_SUMMARIES, detail="hierarchical")
    assert pm.phase == knowledge_progress_pb2.KNOWLEDGE_PHASE_LEAF_SUMMARIES
    assert pm.detail == "hierarchical"


def test_progress_event_full_field_set():
    pe = progress_event(
        phase=knowledge_progress_pb2.KNOWLEDGE_PHASE_LEAF_SUMMARIES,
        completed_units=42,
        total_units=100,
        unit_kind="summary_units",
        message="42/100",
        leaf_cache_hits=5,
        file_cache_hits=2,
        package_cache_hits=1,
        root_cache_hits=0,
    )
    assert pe.phase == knowledge_progress_pb2.KNOWLEDGE_PHASE_LEAF_SUMMARIES
    assert pe.completed_units == 42
    assert pe.total_units == 100
    assert pe.unit_kind == "summary_units"
    assert pe.message == "42/100"
    assert pe.leaf_cache_hits == 5


def test_progress_from_snapshot_translates_phase_string():
    snap = {
        "phase": "files",
        "completed_units": 10,
        "total_units": 50,
        "unit_kind": "summary_units",
        "message": "files 10/50",
        "leaf_cache_hits": 0,
        "file_cache_hits": 3,
        "package_cache_hits": 0,
        "root_cache_hits": 0,
    }
    pe = progress_from_snapshot(snap)
    assert pe.phase == knowledge_progress_pb2.KNOWLEDGE_PHASE_FILE_SUMMARIES
    assert pe.completed_units == 10
    assert pe.total_units == 50
    assert pe.file_cache_hits == 3


def test_progress_from_snapshot_handles_missing_fields():
    # Missing fields default to zero / empty -- no KeyError, no crash.
    pe = progress_from_snapshot({})
    assert pe.phase == knowledge_progress_pb2.KNOWLEDGE_PHASE_UNSPECIFIED
    assert pe.completed_units == 0
    assert pe.total_units == 0


def test_heartbeat_interval_default(monkeypatch):
    monkeypatch.delenv("SOURCEBRIDGE_KNOWLEDGE_STREAM_HEARTBEAT_SECS", raising=False)
    assert heartbeat_interval_secs() == DEFAULT_HEARTBEAT_SECS


def test_heartbeat_interval_env_override(monkeypatch):
    monkeypatch.setenv("SOURCEBRIDGE_KNOWLEDGE_STREAM_HEARTBEAT_SECS", "5")
    assert heartbeat_interval_secs() == 5.0


def test_heartbeat_interval_invalid_env_falls_back(monkeypatch):
    monkeypatch.setenv("SOURCEBRIDGE_KNOWLEDGE_STREAM_HEARTBEAT_SECS", "not-a-float")
    assert heartbeat_interval_secs() == DEFAULT_HEARTBEAT_SECS


def test_heartbeat_interval_zero_falls_back(monkeypatch):
    monkeypatch.setenv("SOURCEBRIDGE_KNOWLEDGE_STREAM_HEARTBEAT_SECS", "0")
    assert heartbeat_interval_secs() == DEFAULT_HEARTBEAT_SECS


@pytest.mark.asyncio
async def test_heartbeat_pump_emits_until_task_done():
    # A task that takes ~0.4s should produce ~3-4 heartbeats at a 0.1s
    # interval. We assert at least one heartbeat fires and the loop
    # exits cleanly when the task completes.
    snap_calls = {"n": 0}

    def snap():
        snap_calls["n"] += 1
        return {
            "phase": "leaves",
            "completed_units": snap_calls["n"],
            "total_units": 100,
            "unit_kind": "summary_units",
            "message": f"snap {snap_calls['n']}",
            "leaf_cache_hits": 0,
            "file_cache_hits": 0,
            "package_cache_hits": 0,
            "root_cache_hits": 0,
        }

    async def work():
        await asyncio.sleep(0.4)
        return "done"

    work_task = asyncio.create_task(work())
    pump = HeartbeatPump(snap, interval_secs=0.1)
    events = []
    async for evt in pump.run_until(work_task):
        events.append(evt)
    result = await work_task

    assert result == "done"
    # At a 0.1s interval over a 0.4s task we expect 3-4 heartbeats; allow 1+
    assert len(events) >= 1
    # All events have the right phase
    for ev in events:
        assert ev.phase == knowledge_progress_pb2.KNOWLEDGE_PHASE_LEAF_SUMMARIES


@pytest.mark.asyncio
async def test_heartbeat_pump_exits_immediately_when_task_already_done():
    # If the work task finishes before the first interval, the pump
    # should yield zero heartbeats (no fake one on the way out).
    async def fast():
        return "fast"

    work_task = asyncio.create_task(fast())
    # Wait for the task to complete first.
    await work_task

    pump = HeartbeatPump(lambda: {"phase": "leaves"}, interval_secs=10.0)
    events = []
    async for evt in pump.run_until(work_task):
        events.append(evt)
    # No heartbeats — the pump never blocked on sleep.
    assert events == []


@pytest.mark.asyncio
async def test_heartbeat_pump_exits_when_long_task_finishes_first():
    # With a 60s interval, a 0.05s task means task wins the race; pump
    # exits without yielding any heartbeat.
    async def quick():
        await asyncio.sleep(0.05)
        return "quick"

    work_task = asyncio.create_task(quick())
    pump = HeartbeatPump(lambda: {"phase": "leaves"}, interval_secs=60.0)
    events = []
    async for evt in pump.run_until(work_task):
        events.append(evt)
    result = await work_task
    assert result == "quick"
    assert events == []


@pytest.mark.asyncio
async def test_heartbeat_pump_does_not_shield_task_on_outer_cancel():
    # Codex r1 C3 — the pump must not shield the work task. If the
    # surrounding generator is cancelled, the cancellation must
    # propagate; the pump itself raises CancelledError (caller's
    # try/finally is responsible for cancelling the work task).
    async def slow():
        try:
            await asyncio.sleep(10.0)
            return "should-not-finish"
        except asyncio.CancelledError:
            raise

    work_task = asyncio.create_task(slow())
    pump = HeartbeatPump(lambda: {"phase": "leaves"}, interval_secs=10.0)

    async def consume():
        async for _ in pump.run_until(work_task):
            pass

    consumer = asyncio.create_task(consume())
    await asyncio.sleep(0.05)
    consumer.cancel()
    with pytest.raises(asyncio.CancelledError):
        await consumer

    # The work task is still running here -- the pump did NOT cancel
    # it (caller's responsibility). We clean up to avoid leaking.
    work_task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await work_task


def test_hierarchical_strategy_progress_snapshot_initial():
    # Verify the new accessor exists on HierarchicalStrategy and
    # returns a sane initial dict before build_tree runs.
    from unittest.mock import MagicMock

    from workers.comprehension.hierarchical import HierarchicalStrategy

    strat = HierarchicalStrategy(provider=MagicMock())
    snap = strat.progress_snapshot()
    assert snap["phase"] == ""
    assert snap["completed_units"] == 0
    assert snap["total_units"] == 0
    assert snap["unit_kind"] == ""
    assert snap["leaf_cache_hits"] == 0
    assert snap["file_cache_hits"] == 0
    assert snap["package_cache_hits"] == 0
    assert snap["root_cache_hits"] == 0


def test_hierarchical_strategy_set_phase_progress():
    # _set_phase_progress should reset counters at a phase boundary.
    from unittest.mock import MagicMock

    from workers.comprehension.hierarchical import HierarchicalStrategy

    strat = HierarchicalStrategy(provider=MagicMock())
    strat._set_phase_progress("leaves", completed=5, total=100, message="leaves 5/100")
    snap = strat.progress_snapshot()
    assert snap["phase"] == "leaves"
    assert snap["completed_units"] == 5
    assert snap["total_units"] == 100
    assert snap["unit_kind"] == "summary_units"
    assert snap["message"] == "leaves 5/100"

    # Phase transition resets counters.
    strat._set_phase_progress("files", completed=0, total=20, message="files 0/20")
    snap2 = strat.progress_snapshot()
    assert snap2["phase"] == "files"
    assert snap2["completed_units"] == 0
    assert snap2["total_units"] == 20
