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


# ---------------------------------------------------------------------------
# _GrpcAuthInterceptor — _wrap_handler streaming paths
#
# Prior coverage gap noted at line 51 ("The _wrap_handler path is exercised by
# end-to-end tests"): the streaming handler slots were never directly tested,
# which is why the async-generator TypeError shipped. Tests below close that gap.
# ---------------------------------------------------------------------------


import grpc  # noqa: E402 — used only in the _wrap_handler tests below


class _FakeContext:
    """Minimal stand-in for grpc.ServicerContext."""

    def __init__(self, metadata: dict[str, str]) -> None:
        self._metadata = metadata
        self.aborted: tuple | None = None

    def invocation_metadata(self):
        return list(self._metadata.items())

    async def abort(self, code, details):
        self.aborted = (code, details)


class _FakeHandler:
    """Minimal stand-in for grpc.RpcMethodHandler (named-tuple-like)."""

    __slots__ = ("unary_unary", "unary_stream", "stream_unary", "stream_stream")

    def __init__(self, **kwargs):
        for slot in self.__slots__:
            setattr(self, slot, kwargs.get(slot))

    def _replace(self, **kwargs):
        new = _FakeHandler(**{s: getattr(self, s) for s in self.__slots__})
        for k, v in kwargs.items():
            setattr(new, k, v)
        return new


@pytest.mark.asyncio
async def test_wrap_handler_unary_stream_passes_auth() -> None:
    """unary_stream handler yields chunks when auth passes."""

    async def fake_unary_stream(request, context):
        yield "chunk-a"
        yield "chunk-b"

    interceptor = _GrpcAuthInterceptor("secret")
    handler = _FakeHandler(unary_stream=fake_unary_stream)
    wrapped = interceptor._wrap_handler(handler)

    ctx = _FakeContext({"x-sb-worker-secret": "secret"})
    results = []
    async for chunk in wrapped.unary_stream("req", ctx):
        results.append(chunk)

    assert results == ["chunk-a", "chunk-b"]
    assert ctx.aborted is None


@pytest.mark.asyncio
async def test_wrap_handler_unary_stream_fails_auth() -> None:
    """unary_stream handler aborts and yields nothing when auth fails."""

    async def fake_unary_stream(request, context):
        yield "should-not-appear"  # pragma: no cover

    interceptor = _GrpcAuthInterceptor("secret")
    handler = _FakeHandler(unary_stream=fake_unary_stream)
    wrapped = interceptor._wrap_handler(handler)

    ctx = _FakeContext({"x-sb-worker-secret": "wrong"})
    results = []
    async for chunk in wrapped.unary_stream("req", ctx):
        results.append(chunk)  # pragma: no cover

    assert results == []
    assert ctx.aborted is not None
    assert ctx.aborted[0] == grpc.StatusCode.UNAUTHENTICATED


@pytest.mark.asyncio
async def test_wrap_handler_stream_stream_passes_auth() -> None:
    """stream_stream handler yields chunks when auth passes."""

    async def fake_stream_stream(request_iter, context):
        yield "resp-1"
        yield "resp-2"

    interceptor = _GrpcAuthInterceptor("secret")
    handler = _FakeHandler(stream_stream=fake_stream_stream)
    wrapped = interceptor._wrap_handler(handler)

    ctx = _FakeContext({"x-sb-worker-secret": "secret"})
    results = []
    async for chunk in wrapped.stream_stream(iter([]), ctx):
        results.append(chunk)

    assert results == ["resp-1", "resp-2"]
    assert ctx.aborted is None


@pytest.mark.asyncio
async def test_wrap_handler_stream_stream_fails_auth() -> None:
    """stream_stream handler aborts and yields nothing when auth fails."""

    async def fake_stream_stream(request_iter, context):
        yield "should-not-appear"  # pragma: no cover

    interceptor = _GrpcAuthInterceptor("secret")
    handler = _FakeHandler(stream_stream=fake_stream_stream)
    wrapped = interceptor._wrap_handler(handler)

    ctx = _FakeContext({})
    results = []
    async for chunk in wrapped.stream_stream(iter([]), ctx):
        results.append(chunk)  # pragma: no cover

    assert results == []
    assert ctx.aborted is not None
    assert ctx.aborted[0] == grpc.StatusCode.UNAUTHENTICATED


@pytest.mark.asyncio
async def test_wrap_handler_stream_unary_passes_auth() -> None:
    """stream_unary handler (coroutine) returns its value when auth passes."""

    async def fake_stream_unary(request_iter, context):
        return "single-response"

    interceptor = _GrpcAuthInterceptor("secret")
    handler = _FakeHandler(stream_unary=fake_stream_unary)
    wrapped = interceptor._wrap_handler(handler)

    ctx = _FakeContext({"x-sb-worker-secret": "secret"})
    result = await wrapped.stream_unary(iter([]), ctx)

    assert result == "single-response"
    assert ctx.aborted is None


@pytest.mark.asyncio
async def test_wrap_handler_stream_unary_fails_auth() -> None:
    """stream_unary handler aborts when auth fails."""

    async def fake_stream_unary(request_iter, context):
        return "should-not-appear"  # pragma: no cover

    interceptor = _GrpcAuthInterceptor("secret")
    handler = _FakeHandler(stream_unary=fake_stream_unary)
    wrapped = interceptor._wrap_handler(handler)

    ctx = _FakeContext({})
    result = await wrapped.stream_unary(iter([]), ctx)

    assert result is None
    assert ctx.aborted is not None
    assert ctx.aborted[0] == grpc.StatusCode.UNAUTHENTICATED
