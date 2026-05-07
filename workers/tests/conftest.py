"""Shared pytest fixtures for the worker test suite.

Provides a function-scoped ``gate_registry`` fixture so tests that exercise
factory functions requiring a ``ProviderGateRegistry`` can do so without
duplicating setup/teardown boilerplate.

Plan: thoughts/shared/plans/active-2026-05-06-deliver-worker-llm-concurrency.md
Phase 2 / H1 fix.
"""

from __future__ import annotations

import pytest_asyncio

from workers.common.llm.concurrency import ConcurrencyConfig, ProviderGateRegistry


@pytest_asyncio.fixture
async def gate_registry():
    """Function-scoped ProviderGateRegistry with default (sentinel-uncapped) config.

    Yields the registry and closes it on teardown so tests don't leak gate
    state across test functions.
    """
    registry = ProviderGateRegistry(ConcurrencyConfig())
    yield registry
    await registry.close()
