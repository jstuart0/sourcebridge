"""Shared CLI bootstrap helper for CLI entry points and benchmark runners.

Constructs a ``ProviderGateRegistry`` from the environment, wraps the LLM
provider, and returns both for graceful shutdown wiring.

Usage pattern::

    async def main() -> None:
        config = WorkerConfig()
        provider, gate_registry = await build_cli_runtime_provider(config)
        try:
            ...  # use provider
        finally:
            await gate_registry.close()

Plan: thoughts/shared/plans/active-2026-05-06-deliver-worker-llm-concurrency.md
Phase 2 / Decision 7 (H1 fix).
"""

from __future__ import annotations

from workers.common.config import WorkerConfig
from workers.common.llm.concurrency import ConcurrencyConfig, ProviderGateRegistry
from workers.common.llm.config import create_llm_provider
from workers.common.llm.provider import LLMProvider


async def build_cli_runtime_provider(
    config: WorkerConfig,
) -> tuple[LLMProvider, ProviderGateRegistry]:
    """Construct a ``ProviderGateRegistry`` and return a wrapped LLM provider.

    The registry is constructed from ``ConcurrencyConfig.from_env()``.  The
    returned provider is gated through the registry (subject to the kill
    switch).  Callers must ``await gate_registry.close()`` on exit so the
    registry's resources are released cleanly.

    Returns:
        (provider, gate_registry) — the wrapped provider and the registry
        that owns its gate state.
    """
    concurrency_config = ConcurrencyConfig.from_env()
    gate_registry = ProviderGateRegistry(concurrency_config)
    provider = await create_llm_provider(config, gate_registry=gate_registry)
    return provider, gate_registry
