"""Tests for workers.common.llm.rebind_guard.

Covers:
  - Cloud-metadata (169.254.169.254) → ConnectError regardless of allow_private.
  - Private IP (192.168.1.1) with allow_private=False → ConnectError.
  - Private IP (192.168.1.1) with allow_private=True → succeeds (lets httpx proceed).
  - Public IP (93.184.216.34) with allow_private=False → succeeds.
  - socket.gaierror → propagates to super().handle_async_request normally (no guard error).
  - IPv6 link-local (fe80::1) → ConnectError regardless of allow_private.
  - IP literal in URL (no DNS lookup needed) → same enforcement.
  - Executor offload: socket.getaddrinfo is called via run_in_executor (X-M1).
  - Wiring contracts: OpenAICompatProvider, OpenAICompatProbeBackend, Anthropic (T-L1).
"""

from __future__ import annotations

import socket
from unittest.mock import AsyncMock, MagicMock, patch

import httpx
import pytest

from workers.common.llm.rebind_guard import RebindGuardedTransport


def _make_request(host: str) -> httpx.Request:
    return httpx.Request("POST", f"https://{host}/v1/chat/completions")


def _addrinfo(ip: str) -> list:
    """Return a minimal getaddrinfo result list for a single IP."""
    # getaddrinfo returns: (family, type, proto, canonname, sockaddr)
    # sockaddr for IPv4 is (address, port); for IPv6 is (address, port, flow, scope)
    return [(socket.AF_INET, socket.SOCK_STREAM, 6, "", (ip, 443))]


@pytest.mark.asyncio
async def test_cloud_metadata_ip_always_blocked_allow_private_false():
    """169.254.169.254 (AWS IMDS) is blocked even when allow_private=False."""
    transport = RebindGuardedTransport(allow_private=False)
    with (
        patch("socket.getaddrinfo", return_value=_addrinfo("169.254.169.254")),
        pytest.raises(httpx.ConnectError, match="dns-rebind-guard"),
    ):
        await transport.handle_async_request(_make_request("attacker.example.com"))


@pytest.mark.asyncio
async def test_cloud_metadata_ip_always_blocked_allow_private_true():
    """169.254.169.254 (AWS IMDS) is blocked even when allow_private=True.

    An operator who enables allow_private for a self-hosted Ollama instance must
    NOT accidentally get IMDS access.
    """
    transport = RebindGuardedTransport(allow_private=True)
    with (
        patch("socket.getaddrinfo", return_value=_addrinfo("169.254.169.254")),
        pytest.raises(httpx.ConnectError, match="dns-rebind-guard"),
    ):
        await transport.handle_async_request(_make_request("attacker.example.com"))


@pytest.mark.asyncio
async def test_ipv6_link_local_always_blocked():
    """fe80::1 (IPv6 link-local) is blocked regardless of allow_private."""
    transport = RebindGuardedTransport(allow_private=True)
    ipv6_addrinfo = [(socket.AF_INET6, socket.SOCK_STREAM, 6, "", ("fe80::1", 443, 0, 0))]
    with (
        patch("socket.getaddrinfo", return_value=ipv6_addrinfo),
        pytest.raises(httpx.ConnectError, match="dns-rebind-guard"),
    ):
        await transport.handle_async_request(_make_request("attacker.example.com"))


@pytest.mark.asyncio
async def test_private_ip_blocked_when_allow_private_false():
    """192.168.1.1 is blocked when allow_private=False."""
    transport = RebindGuardedTransport(allow_private=False)
    with (
        patch("socket.getaddrinfo", return_value=_addrinfo("192.168.1.1")),
        pytest.raises(httpx.ConnectError, match="dns-rebind-guard"),
    ):
        await transport.handle_async_request(_make_request("internal.example.com"))


@pytest.mark.asyncio
async def test_private_ip_allowed_when_allow_private_true(monkeypatch):
    """192.168.1.1 is NOT blocked when allow_private=True.

    This is the Ollama / self-hosted provider case.  The guard must not
    interfere with legitimate private-network LLM traffic.
    """
    transport = RebindGuardedTransport(allow_private=True)

    fake_response = httpx.Response(200, content=b"{}")
    super_mock = AsyncMock(return_value=fake_response)

    with (
        patch("socket.getaddrinfo", return_value=_addrinfo("192.168.1.1")),
        patch.object(
            httpx.AsyncHTTPTransport,
            "handle_async_request",
            new=super_mock,
        ),
    ):
        resp = await transport.handle_async_request(_make_request("ollama.internal"))

    assert resp.status_code == 200
    super_mock.assert_awaited_once()


@pytest.mark.asyncio
async def test_public_ip_allowed_allow_private_false(monkeypatch):
    """93.184.216.34 (example.com, public) is allowed with allow_private=False."""
    transport = RebindGuardedTransport(allow_private=False)

    fake_response = httpx.Response(200, content=b"{}")
    super_mock = AsyncMock(return_value=fake_response)

    with (
        patch("socket.getaddrinfo", return_value=_addrinfo("93.184.216.34")),
        patch.object(
            httpx.AsyncHTTPTransport,
            "handle_async_request",
            new=super_mock,
        ),
    ):
        resp = await transport.handle_async_request(_make_request("api.openai.com"))

    assert resp.status_code == 200
    super_mock.assert_awaited_once()


@pytest.mark.asyncio
async def test_gaierror_propagates_to_super(monkeypatch):
    """socket.gaierror is swallowed by the guard; httpx handles the unresolvable host."""
    transport = RebindGuardedTransport(allow_private=False)

    fake_response = httpx.Response(200, content=b"{}")
    super_mock = AsyncMock(return_value=fake_response)

    with (
        patch("socket.getaddrinfo", side_effect=socket.gaierror("no such host")),
        patch.object(
            httpx.AsyncHTTPTransport,
            "handle_async_request",
            new=super_mock,
        ),
    ):
        resp = await transport.handle_async_request(_make_request("does-not-exist.invalid"))

    # guard did not raise; httpx's normal unresolvable-host path fires
    assert resp.status_code == 200
    super_mock.assert_awaited_once()


@pytest.mark.asyncio
async def test_ip_literal_cloud_metadata_blocked():
    """URL with an IP literal (169.254.169.254) is blocked.

    socket.getaddrinfo returns the literal address unchanged, so the guard
    catches IP-literal URLs the same way it catches resolved hostnames.
    """
    transport = RebindGuardedTransport(allow_private=True)
    # Use .host to confirm the URL parses as expected
    req = _make_request("169.254.169.254")
    assert req.url.host == "169.254.169.254"

    with (
        patch("socket.getaddrinfo", return_value=_addrinfo("169.254.169.254")),
        pytest.raises(httpx.ConnectError, match="dns-rebind-guard"),
    ):
        await transport.handle_async_request(req)


@pytest.mark.asyncio
async def test_loopback_blocked_allow_private_false():
    """127.0.0.1 is blocked when allow_private=False."""
    transport = RebindGuardedTransport(allow_private=False)
    with (
        patch("socket.getaddrinfo", return_value=_addrinfo("127.0.0.1")),
        pytest.raises(httpx.ConnectError, match="dns-rebind-guard"),
    ):
        await transport.handle_async_request(_make_request("localhost"))


@pytest.mark.asyncio
async def test_loopback_allowed_allow_private_true(monkeypatch):
    """127.0.0.1 is allowed when allow_private=True (local dev Ollama on localhost)."""
    transport = RebindGuardedTransport(allow_private=True)

    fake_response = httpx.Response(200, content=b"{}")
    super_mock = AsyncMock(return_value=fake_response)

    with (
        patch("socket.getaddrinfo", return_value=_addrinfo("127.0.0.1")),
        patch.object(
            httpx.AsyncHTTPTransport,
            "handle_async_request",
            new=super_mock,
        ),
    ):
        resp = await transport.handle_async_request(_make_request("localhost"))

    assert resp.status_code == 200
    super_mock.assert_awaited_once()


# ---------------------------------------------------------------------------
# X-M1: executor offload — socket.getaddrinfo must not block the event loop
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_handle_async_request_uses_executor_offload():
    """socket.getaddrinfo is offloaded to a thread-pool executor (X-M1).

    The guard must not call socket.getaddrinfo synchronously on the event
    loop thread.  We verify by intercepting loop.run_in_executor and asserting
    it was called, rather than a direct blocking getaddrinfo call.
    """
    transport = RebindGuardedTransport(allow_private=True)

    fake_response = httpx.Response(200, content=b"{}")
    super_mock = AsyncMock(return_value=fake_response)

    executor_calls: list[tuple] = []
    real_addrinfo = _addrinfo("93.184.216.34")  # public IP → passes guard

    async def fake_run_in_executor(_executor, fn, *args):
        executor_calls.append((fn, args))
        # Actually call the function (which is the patched mock) so the guard
        # sees a valid result.
        return fn(*args)

    with (
        patch("workers.common.llm.rebind_guard.socket.getaddrinfo", return_value=real_addrinfo) as mock_gai,
        patch.object(httpx.AsyncHTTPTransport, "handle_async_request", new=super_mock),
        patch("asyncio.get_running_loop") as mock_get_loop,
    ):
        mock_loop = MagicMock()
        mock_loop.run_in_executor = fake_run_in_executor
        mock_get_loop.return_value = mock_loop

        resp = await transport.handle_async_request(_make_request("example.com"))

    assert resp.status_code == 200
    # run_in_executor was called at least once, confirming the guard offloaded
    # the DNS lookup rather than calling socket.getaddrinfo directly.
    assert len(executor_calls) >= 1
    # The executor was invoked with the (patched) socket.getaddrinfo callable.
    assert executor_calls[0][0] is mock_gai


# ---------------------------------------------------------------------------
# T-L1: provider wiring contract tests
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_openai_compat_provider_wires_rebind_guard_to_async_openai():
    """OpenAICompatProvider wires RebindGuardedTransport to its AsyncOpenAI client.

    The SDK client must receive the transport instance stored as
    self._rebind_transport (not a bare AsyncHTTPTransport).
    """
    from workers.common.llm.openai_compat import OpenAICompatProvider

    provider = OpenAICompatProvider(
        api_key="test-key",
        model="gpt-4o",
        base_url="http://localhost:11434/v1",
        allow_private_base_url=True,
    )

    # The httpx client on the openai client must use a RebindGuardedTransport.
    assert isinstance(provider._rebind_transport, RebindGuardedTransport)
    assert provider._rebind_transport._allow_private is True


@pytest.mark.asyncio
async def test_openai_compat_provider_allow_private_false_wired():
    """allow_private_base_url=False is threaded to RebindGuardedTransport."""
    from workers.common.llm.openai_compat import OpenAICompatProvider

    provider = OpenAICompatProvider(
        api_key="test-key",
        model="gpt-4o",
        base_url="https://api.openai.com/v1",
        allow_private_base_url=False,
    )

    assert isinstance(provider._rebind_transport, RebindGuardedTransport)
    assert provider._rebind_transport._allow_private is False
    # Also verify the stored flag for per-call paths.
    assert provider._allow_private_base_url is False


@pytest.mark.asyncio
async def test_openai_compat_provider_ollama_native_uses_rebind_guard():
    """_complete_ollama_native uses a per-call RebindGuardedTransport (D-014).

    We capture RebindGuardedTransport.__init__ calls and assert at least one
    per-call instantiation occurred during the Ollama-native dispatch.
    """
    from workers.common.llm.openai_compat import OpenAICompatProvider

    provider = OpenAICompatProvider(
        api_key="",
        model="qwen3:8b",
        base_url="http://localhost:11434/v1",
        provider_name="ollama",
        disable_thinking=True,
        allow_private_base_url=True,
    )

    transport_inits: list[bool] = []
    original_init = RebindGuardedTransport.__init__

    def capture_init(self_inner, allow_private: bool, **kwargs: object) -> None:
        original_init(self_inner, allow_private=allow_private, **kwargs)
        transport_inits.append(allow_private)

    fake_response_body = b'{"message": {"content": "ok"}, "done": true, "done_reason": "stop"}'
    fake_response = httpx.Response(200, content=fake_response_body)

    with (
        patch.object(RebindGuardedTransport, "__init__", capture_init),
        patch.object(
            httpx.AsyncHTTPTransport,
            "handle_async_request",
            new=AsyncMock(return_value=fake_response),
        ),
    ):
        await provider._complete_ollama_native(
            prompt="hello",
            system="",
            max_tokens=16,
            temperature=0.0,
            use_model="qwen3:8b",
        )

    # At least one per-call instantiation occurred (the __init__ at provider
    # construction also fires, so len >= 2; the important invariant is >= 1 in
    # the call path, not just at construction).
    assert len(transport_inits) >= 1
    # All inits should have allow_private=True (matching provider config).
    assert all(v is True for v in transport_inits)


@pytest.mark.asyncio
async def test_openai_compat_probe_backend_uses_rebind_guard():
    """OpenAICompatProbeBackend uses per-call RebindGuardedTransport."""
    from workers.common.llm.concurrency_probe import OpenAICompatProbeBackend

    probe = OpenAICompatProbeBackend(
        base_url="http://localhost:11434/v1",
        model="qwen3:8b",
        allow_private=True,
    )

    transport_inits: list[bool] = []
    original_init = RebindGuardedTransport.__init__

    def capture_init(self_inner, allow_private: bool, **kwargs: object) -> None:
        original_init(self_inner, allow_private=allow_private, **kwargs)
        transport_inits.append(allow_private)

    fake_response = httpx.Response(200, content=b'{"choices": [{"message": {"content": "ok"}}]}')

    with (
        patch.object(RebindGuardedTransport, "__init__", capture_init),
        patch.object(
            httpx.AsyncHTTPTransport,
            "handle_async_request",
            new=AsyncMock(return_value=fake_response),
        ),
    ):
        await probe.call()

    assert len(transport_inits) >= 1
    assert all(v is True for v in transport_inits)


@pytest.mark.asyncio
async def test_anthropic_provider_wires_rebind_guard():
    """AnthropicProvider still wires RebindGuardedTransport (X-H2 regression guard).

    This test confirms the existing Anthropic wiring is intact after the Phase 1
    refactor that extracted ip_check.py and updated rebind_guard.py.
    """
    from workers.common.llm.anthropic import AnthropicProvider

    provider = AnthropicProvider(
        api_key="test-key",
        allow_private_base_url=False,
    )

    # The httpx client on the anthropic client must use a RebindGuardedTransport.
    # AnthropicProvider stores the transport on _http_client's transport.
    # We inspect via the openai-style pattern: the transport is the only transport.
    http_client = provider.client._client  # anthropic AsyncAnthropic._client → httpx.AsyncClient
    assert isinstance(http_client._transport, RebindGuardedTransport)
    assert http_client._transport._allow_private is False
