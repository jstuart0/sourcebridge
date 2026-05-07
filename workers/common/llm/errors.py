# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Transient-error classification for LLM backends.

Consolidates _is_provider_compute_error that was duplicated between
workers/comprehension/renderers.py and workers/comprehension/hierarchical.py
with a docstring in the latter acknowledging the duplication risk:
"kept in sync — don't drift".
"""

from __future__ import annotations


def is_provider_compute_error(exc: Exception) -> bool:
    """Classify an exception as a transient LLM-backend error.

    .. deprecated::
        DEPRECATED — see plan 2026-05-06-deliver-worker-llm-concurrency Decision 4.

        The two hand-rolled retry loops that called this function
        (``hierarchical.py`` leaf/file/package retry and
        ``renderers.py:_render_with_retry``) were deleted in Phase 3.  The
        tenacity gate now owns all retry logic via its ``_retry_predicate``
        whitelist, which handles the same set of transient conditions using
        proper SDK exception types rather than string-matching.

        This function is retained for backward compatibility (tests, any
        third-party imports) but is no longer called in the hot path.
        Remove the function entirely in a follow-up cleanup pass.

    Returns True for failures that the retry path should swallow — timeouts,
    broken pipes, partial connection resets, gateway 5xx, and the original
    "compute error" / "server_error" markers.  A timeout in particular is
    indistinguishable from a slow GPU response on the first attempt, so we
    include it in the transient set.

    Adding a new marker requires a change in exactly one place (here).
    """
    text = str(exc).lower()
    transient_markers = (
        "compute error",
        "server_error",
        "request timed out",
        "timeout",
        "deadline exceeded",
        "connection reset",
        "connection refused",
        "broken pipe",
        "503",
        "502",
        "504",
        "gateway",
        "upstream",
    )
    return any(marker in text for marker in transient_markers)
