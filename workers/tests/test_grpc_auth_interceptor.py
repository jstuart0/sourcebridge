"""Unit tests for the _GrpcAuthInterceptor and startup-WARN helper (D10 / Phase 1)."""

from __future__ import annotations

import pytest

from workers.__main__ import _GrpcAuthInterceptor, _is_non_loopback

# ---------------------------------------------------------------------------
# _is_non_loopback helper
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "addr, expected",
    [
        ("127.0.0.1", False),
        ("127.0.0.1:50051", False),
        ("localhost", False),
        ("localhost:50051", False),
        ("0.0.0.0", True),
        ("0.0.0.0:50051", True),
        ("192.168.1.100", True),
        ("", True),  # empty = default gRPC "all interfaces"
        ("[::]", True),
    ],
)
def test_is_non_loopback(addr: str, expected: bool) -> None:
    assert _is_non_loopback(addr) == expected


# IPv6 loopback — strip brackets correctly
@pytest.mark.parametrize(
    "addr",
    [
        "::1",
        "[::1]",
        "[::1]:50051",
    ],
)
def test_is_non_loopback_ipv6_loopback(addr: str) -> None:
    """IPv6 loopback addresses in any format are correctly detected as loopback."""
    assert not _is_non_loopback(addr)


# ---------------------------------------------------------------------------
# _GrpcAuthInterceptor — metadata acceptance and rejection
# ---------------------------------------------------------------------------
# We test via check_auth_metadata() (the extracted pure-function auth check)
# rather than through intercept_service(), which requires wiring gRPC handler
# machinery. The _wrap_handler path is exercised by end-to-end tests (Phase 7).
# ---------------------------------------------------------------------------


def test_interceptor_no_secret_passes_all() -> None:
    """Empty secret = no auth; all metadata passes."""
    interceptor = _GrpcAuthInterceptor("")
    assert interceptor.check_auth_metadata({})
    assert interceptor.check_auth_metadata({"x-sb-worker-secret": "anything"})


def test_interceptor_missing_header_rejected() -> None:
    """Request without x-sb-worker-secret is rejected."""
    interceptor = _GrpcAuthInterceptor("secret123")
    assert not interceptor.check_auth_metadata({})


def test_interceptor_wrong_secret_rejected() -> None:
    """Request with wrong secret is rejected."""
    interceptor = _GrpcAuthInterceptor("correct-secret")
    assert not interceptor.check_auth_metadata({"x-sb-worker-secret": "wrong-secret"})


def test_interceptor_correct_secret_passes() -> None:
    """Request with the correct secret passes."""
    interceptor = _GrpcAuthInterceptor("secret123")
    assert interceptor.check_auth_metadata({"x-sb-worker-secret": "secret123"})


def test_interceptor_comma_separated_secrets_rotation() -> None:
    """Comma-separated secrets: any match succeeds (zero-downtime rotation R8)."""
    interceptor = _GrpcAuthInterceptor("old-secret,new-secret")
    assert interceptor.check_auth_metadata({"x-sb-worker-secret": "old-secret"})
    assert interceptor.check_auth_metadata({"x-sb-worker-secret": "new-secret"})
    assert not interceptor.check_auth_metadata({"x-sb-worker-secret": "other-secret"})


def test_interceptor_trailing_comma_does_not_match_empty() -> None:
    """Trailing comma does not create an empty secret that matches everything."""
    interceptor = _GrpcAuthInterceptor("real-secret,")
    assert not interceptor.check_auth_metadata({})
    assert not interceptor.check_auth_metadata({"x-sb-worker-secret": ""})
    assert interceptor.check_auth_metadata({"x-sb-worker-secret": "real-secret"})
