"""Utilities for reading SourceBridge gRPC metadata."""

from __future__ import annotations

import grpc


def resolve_model_override(context: grpc.aio.ServicerContext) -> str | None:
    """Read model override from gRPC metadata, return None to use default.

    The Go API attaches 'x-sb-model' metadata when advanced mode is on.
    """
    for key, value in context.invocation_metadata():
        if key == "x-sb-model":
            return value or None
    return None
