"""Utilities for reading SourceBridge gRPC metadata."""

from __future__ import annotations

import json
from dataclasses import dataclass

import grpc


def resolve_model_override(context: grpc.aio.ServicerContext) -> str | None:
    """Read model override from gRPC metadata, return None to use default.

    The Go API attaches 'x-sb-model' metadata when advanced mode is on.
    """
    if not hasattr(context, "invocation_metadata"):
        return None
    for key, value in context.invocation_metadata():
        if key == "x-sb-model":
            return value or None
    return None


@dataclass
class RuntimeLLMOverride:
    provider: str = ""
    base_url: str = ""
    api_key: str = ""
    model: str = ""
    draft_model: str = ""
    operation: str = ""

    def is_empty(self) -> bool:
        return not any(
            (
                self.provider,
                self.base_url,
                self.api_key,
                self.model,
                self.draft_model,
                self.operation,
            )
        )


def resolve_llm_override(context: grpc.aio.ServicerContext) -> RuntimeLLMOverride | None:
    """Read the API-provided effective LLM config from gRPC metadata."""
    if not hasattr(context, "invocation_metadata"):
        return None
    override = RuntimeLLMOverride()
    for key, value in context.invocation_metadata():
        if key == "x-sb-llm-provider":
            override.provider = value or ""
        elif key == "x-sb-llm-base-url":
            override.base_url = value or ""
        elif key == "x-sb-llm-api-key":
            override.api_key = value or ""
        elif key == "x-sb-model":
            override.model = value or ""
        elif key == "x-sb-llm-draft-model":
            override.draft_model = value or ""
        elif key == "x-sb-operation":
            override.operation = value or ""
    if override.is_empty():
        return None
    return override


@dataclass
class RuntimeJobLogMetadata:
    job_id: str = ""
    repo_id: str = ""
    artifact_id: str = ""
    subsystem: str = ""
    job_type: str = ""

    def is_empty(self) -> bool:
        return not any(
            (
                self.job_id,
                self.repo_id,
                self.artifact_id,
                self.subsystem,
                self.job_type,
            )
        )


def resolve_job_log_metadata(context: grpc.aio.ServicerContext) -> RuntimeJobLogMetadata | None:
    """Read job log metadata from gRPC metadata."""
    if not hasattr(context, "invocation_metadata"):
        return None
    meta = RuntimeJobLogMetadata()
    for key, value in context.invocation_metadata():
        if key == "x-sb-job-id":
            meta.job_id = value or ""
        elif key == "x-sb-repo-id":
            meta.repo_id = value or ""
        elif key == "x-sb-artifact-id":
            meta.artifact_id = value or ""
        elif key == "x-sb-subsystem":
            meta.subsystem = value or ""
        elif key == "x-sb-job-type":
            meta.job_type = value or ""
    if meta.is_empty():
        return None
    return meta


@dataclass
class CliffNotesRenderMetadata:
    render_only: bool = False
    selected_section_titles: list[str] | None = None
    understanding_depth: str = ""
    relevance_profile: str = ""

    def is_empty(self) -> bool:
        return (
            not self.render_only
            and not self.selected_section_titles
            and not self.understanding_depth
            and not self.relevance_profile
        )


def resolve_cliff_notes_render_metadata(
    context: grpc.aio.ServicerContext,
) -> CliffNotesRenderMetadata | None:
    if not hasattr(context, "invocation_metadata"):
        return None
    meta = CliffNotesRenderMetadata()
    for key, value in context.invocation_metadata():
        if key == "x-sb-cliff-render-only":
            meta.render_only = (value or "").strip().lower() == "true"
        elif key == "x-sb-cliff-selected-sections":
            raw = (value or "").strip()
            if not raw:
                continue
            try:
                parsed = json.loads(raw)
            except Exception:
                continue
            if isinstance(parsed, list):
                meta.selected_section_titles = [str(item).strip() for item in parsed if str(item).strip()]
        elif key == "x-sb-cliff-understanding-depth":
            meta.understanding_depth = (value or "").strip().lower()
        elif key == "x-sb-cliff-relevance-profile":
            meta.relevance_profile = (value or "").strip().lower()
    if meta.is_empty():
        return None
    return meta
