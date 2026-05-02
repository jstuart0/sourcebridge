"""SourceBridge AI/ML Worker — LLM reasoning, embedding, linking, and requirement ingestion."""

from __future__ import annotations

import os
from typing import Tuple


def _resolve_version() -> Tuple[str, str, str]:
    """Resolve the worker's (version, commit, build_date) tuple.

    Resolution priority (matches scripts/version.sh contract):

    1. ``SOURCEBRIDGE_VERSION`` / ``SOURCEBRIDGE_COMMIT`` / ``SOURCEBRIDGE_BUILD_DATE``
       env vars — set by ``make dev-worker`` so a local worker reports the
       same string as the Go binary, not pyproject.toml's 0.1.0.
    2. ``workers/_version.py`` — generated at image build by Dockerfile.worker
       (echoes the build-args into a Python module). Gitignored.
    3. ``importlib.metadata.version("sourcebridge-worker")`` — falls back to
       the pyproject.toml version (0.1.0). Used for editable installs without
       any of the above wiring.
    4. ``"0.0.0-unknown"`` — if every path above fails.

    Returns ``(version, commit, build_date)`` — every value is a string;
    missing fields default to ``"unknown"``.
    """
    # Priority 1: env vars from make dev-worker.
    env_version = os.environ.get("SOURCEBRIDGE_VERSION")
    if env_version:
        return (
            env_version,
            os.environ.get("SOURCEBRIDGE_COMMIT", "unknown"),
            os.environ.get("SOURCEBRIDGE_BUILD_DATE", "unknown"),
        )

    # Priority 2: _version.py written at image build.
    try:
        from workers import _version  # type: ignore[import-not-found]

        return (
            getattr(_version, "__version__", "0.0.0-unknown"),
            getattr(_version, "__commit__", "unknown"),
            getattr(_version, "__build_date__", "unknown"),
        )
    except ImportError:
        pass

    # Priority 3: importlib.metadata (editable install / uv sync).
    try:
        from importlib.metadata import version as _pkg_version

        return _pkg_version("sourcebridge-worker"), "unknown", "unknown"
    except Exception:
        pass

    # Priority 4: hard fallback.
    return "0.0.0-unknown", "unknown", "unknown"


__version__, __commit__, __build_date__ = _resolve_version()

__all__ = ["__version__", "__commit__", "__build_date__"]
