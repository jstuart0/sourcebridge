# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Shared servicer helpers used by knowledge, requirements, and reasoning servicers.

Consolidates _resolve_provider triplication that previously lived in each
servicer with verbatim-identical bodies.
"""

from __future__ import annotations

from typing import Optional, Tuple

import grpc

from workers.common.grpc_metadata import resolve_llm_override, resolve_model_override
from workers.common.llm.config import create_llm_provider_for_request
from workers.common.llm.provider import LLMProvider


def resolve_provider_for_context(
    llm: LLMProvider,
    config,
    context: grpc.aio.ServicerContext,
    fallback_llm: Optional[LLMProvider] = None,
) -> Tuple[LLMProvider, Optional[str]]:
    """Resolve the LLM provider for a gRPC request, honoring metadata overrides.

    Args:
        llm: the servicer's default LLM provider.
        config: the worker config (WorkerConfig); may be None when not configured.
        context: the gRPC ServicerContext (carries metadata).
        fallback_llm: optional alternate provider used when no override is present
            and a separate report/fallback provider is configured (used by the
            knowledge servicer's _resolve_report_provider variant).

    Returns:
        (provider, model_override) — the provider to use for this request, and
        the per-call model string if one was supplied (None otherwise).

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
            return fallback_llm, model or fallback_model or None
        return llm, model

    # A full provider override is present in the metadata.
    if config is None:
        # No config to build a fresh provider from; use the fallback (or default)
        # and whatever model the override carries.
        return fallback_llm if fallback_llm is not None else llm, override.model or None

    provider, model = create_llm_provider_for_request(
        config,
        provider=override.provider,
        base_url=override.base_url,
        api_key=override.api_key,
        model=override.model,
        draft_model=override.draft_model,
        timeout_seconds=override.timeout_seconds,
    )
    return provider, model or None
