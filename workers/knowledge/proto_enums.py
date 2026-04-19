from __future__ import annotations

from collections import Counter

import structlog
from knowledge.v1 import knowledge_pb2

log = structlog.get_logger(__name__)

_fallback_counts: Counter[str] = Counter()

_AUDIENCE_MAP = {
    knowledge_pb2.AUDIENCE_BEGINNER: "beginner",
    knowledge_pb2.AUDIENCE_DEVELOPER: "developer",
}

_DEPTH_MAP = {
    knowledge_pb2.DEPTH_SUMMARY: "summary",
    knowledge_pb2.DEPTH_MEDIUM: "medium",
    knowledge_pb2.DEPTH_DEEP: "deep",
}


def _resolve_enum_value(*, field: str, enum_value: int, mapping: dict[int, str], legacy_value: str, default: str) -> str:
    resolved = mapping.get(enum_value)
    if resolved:
        return resolved

    legacy = (legacy_value or "").strip().lower()
    if legacy:
        _fallback_counts[field] += 1
        log.info("proto_enum_fallback_used", field=field, value=legacy)
        return legacy

    return default


def resolve_request_audience(request) -> str:
    return _resolve_enum_value(
        field="audience",
        enum_value=int(getattr(request, "audience_enum", 0) or 0),
        mapping=_AUDIENCE_MAP,
        legacy_value=str(getattr(request, "audience", "") or ""),
        default="developer",
    )


def resolve_request_depth(request) -> str:
    return _resolve_enum_value(
        field="depth",
        enum_value=int(getattr(request, "depth_enum", 0) or 0),
        mapping=_DEPTH_MAP,
        legacy_value=str(getattr(request, "depth", "") or ""),
        default="medium",
    )


def proto_enum_fallback_counts() -> dict[str, int]:
    return dict(_fallback_counts)
