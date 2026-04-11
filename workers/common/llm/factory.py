"""LLM provider factory.

Thin wrapper around the existing config module for a clearer import name.
The canonical factory logic lives in ``workers.common.llm.config``.
"""

from __future__ import annotations

from workers.common.llm.config import create_llm_provider, create_report_provider

__all__ = ["create_llm_provider", "create_report_provider"]
