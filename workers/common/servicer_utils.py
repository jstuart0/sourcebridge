# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Shared servicer helpers used by knowledge, requirements, and reasoning servicers.

Consolidates _resolve_provider triplication that previously lived in each
servicer with verbatim-identical bodies.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Literal

import grpc

from workers.common.grpc_metadata import resolve_llm_override, resolve_model_override
from workers.common.llm.provider import LLMProvider


@dataclass(frozen=True)
class ProviderResolutionKey:
    """Canonical gate-registry key for the resolved provider context.

    Returned as the third element of ``resolve_provider_for_context`` when
    ``gate_registry`` is supplied.  The registry's ``canonical_key_for(...)``
    accepts this and returns the internal lookup tuple that identifies the
    right gate for capability reporting (Decision 12, plan v4 Phase 2).
    """

    provider: str
    base_url: str | None
    kind: Literal["llm", "embedding"] = "llm"


def resolve_provider_for_context(
    llm: LLMProvider,
    config,
    context: grpc.aio.ServicerContext,
    fallback_llm: LLMProvider | None = None,
    *,
    gate_registry=None,
) -> tuple[LLMProvider, str | None, ProviderResolutionKey | None]:
    """Resolve the LLM provider for a gRPC request, honoring metadata overrides.

    Args:
        llm: the servicer's default LLM provider.
        config: the worker config (WorkerConfig); may be None when not configured.
        context: the gRPC ServicerContext (carries metadata).
        fallback_llm: optional alternate provider used when no override is present
            and a separate report/fallback provider is configured (used by the
            knowledge servicer's _resolve_report_provider variant).
        gate_registry: optional ``ProviderGateRegistry``; when supplied the
            third return value is a ``ProviderResolutionKey`` describing the
            canonical gate key for the resolved (provider, base_url).  When
            None, the third return value is None.

    Returns:
        (provider, model_override, resolution_key) — the provider to use for
        this request, the per-call model string (or None), and the canonical
        gate key (or None when gate_registry is None).

    Resolution order:
    1. If a full LLM override is present in the gRPC metadata and a worker
       config is available, build a fresh provider from the override parameters.
    2. If only a model override is present (no full LLM override), return the
       default (or fallback) provider with that model string.
    3. If a fallback_llm is provided and no override is active, return it with
       the best available model string (model override, then config report model,
       then None).
    4. Otherwise return llm with the model override (or None).
    """
    override = resolve_llm_override(context)

    if override is None:
        model = resolve_model_override(context)
        if fallback_llm is not None:
            # _resolve_report_provider path: prefer the fallback provider with
            # its configured report model as the fallback model string.
            fallback_model = getattr(config, "llm_report_model", None) if config is not None else None
            resolution_key = _build_resolution_key(config, gate_registry) if gate_registry is not None else None
            return fallback_llm, model or fallback_model or None, resolution_key
        resolution_key = _build_resolution_key(config, gate_registry) if gate_registry is not None else None
        return llm, model, resolution_key

    # A full provider override is present in the metadata.
    if config is None:
        # No config to build a fresh provider from; use the fallback (or default)
        # and whatever model the override carries.
        return fallback_llm if fallback_llm is not None else llm, override.model or None, None

    # Per-request providers are created synchronously without gate wrapping.
    # Gate wrapping is reserved for long-lived startup providers (plan Phase 2).
    # The resolution_key we return lets callers (GetProviderCapabilities) look up
    # the *gate* for this override's (provider, base_url) without re-wrapping.
    # Lazy import avoids a circular dependency between servicer_utils ↔ config.
    from workers.common.llm.config import _create_llm_provider_sync

    provider, model = _create_llm_provider_sync(
        config,
        provider=override.provider,
        base_url=override.base_url,
        api_key=override.api_key,
        model=override.model,
        draft_model=override.draft_model,
        timeout_seconds=override.timeout_seconds,
    )
    # Build the resolution key from the override's effective provider/base_url.
    resolution_key: ProviderResolutionKey | None = None
    if gate_registry is not None:
        resolved_provider = override.provider or getattr(config, "llm_provider", "")
        resolved_base_url = override.base_url or getattr(config, "llm_base_url", None) or None
        resolution_key = ProviderResolutionKey(
            provider=resolved_provider,
            base_url=resolved_base_url,
            kind="llm",
        )
    return provider, model or None, resolution_key


def _build_resolution_key(config, gate_registry) -> ProviderResolutionKey | None:
    """Build a ProviderResolutionKey from the bootstrap config.

    Returns None when config is None (no provider info available).
    """
    if config is None:
        return None
    provider = getattr(config, "llm_provider", "") or ""
    base_url = getattr(config, "llm_base_url", None) or None
    return ProviderResolutionKey(provider=provider, base_url=base_url, kind="llm")
