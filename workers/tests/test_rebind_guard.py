"""Tests for workers.common.llm.rebind_guard.

Covers:
  - Cloud-metadata (169.254.169.254) → ConnectError regardless of allow_private.
  - Private IP (192.168.1.1) with allow_private=False → ConnectError.
  - Private IP (192.168.1.1) with allow_private=True → succeeds (lets httpx proceed).
  - Public IP (93.184.216.34) with allow_private=False → succeeds.
  - socket.gaierror → propagates to super().handle_async_request normally (no guard error).
  - IPv6 link-local (fe80::1) → ConnectError regardless of allow_private.
  - IP literal in URL (no DNS lookup needed) → same enforcement.
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
    with patch("socket.getaddrinfo", return_value=_addrinfo("169.254.169.254")):
        with pytest.raises(httpx.ConnectError, match="dns-rebind-guard"):
            await transport.handle_async_request(_make_request("attacker.example.com"))


@pytest.mark.asyncio
async def test_cloud_metadata_ip_always_blocked_allow_private_true():
    """169.254.169.254 (AWS IMDS) is blocked even when allow_private=True.

    An operator who enables allow_private for a self-hosted Ollama instance must
    NOT accidentally get IMDS access.
    """
    transport = RebindGuardedTransport(allow_private=True)
    with patch("socket.getaddrinfo", return_value=_addrinfo("169.254.169.254")):
        with pytest.raises(httpx.ConnectError, match="dns-rebind-guard"):
            await transport.handle_async_request(_make_request("attacker.example.com"))


@pytest.mark.asyncio
async def test_ipv6_link_local_always_blocked():
    """fe80::1 (IPv6 link-local) is blocked regardless of allow_private."""
    transport = RebindGuardedTransport(allow_private=True)
    ipv6_addrinfo = [(socket.AF_INET6, socket.SOCK_STREAM, 6, "", ("fe80::1", 443, 0, 0))]
    with patch("socket.getaddrinfo", return_value=ipv6_addrinfo):
        with pytest.raises(httpx.ConnectError, match="dns-rebind-guard"):
            await transport.handle_async_request(_make_request("attacker.example.com"))


@pytest.mark.asyncio
async def test_private_ip_blocked_when_allow_private_false():
    """192.168.1.1 is blocked when allow_private=False."""
    transport = RebindGuardedTransport(allow_private=False)
    with patch("socket.getaddrinfo", return_value=_addrinfo("192.168.1.1")):
        with pytest.raises(httpx.ConnectError, match="dns-rebind-guard"):
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

    with patch("socket.getaddrinfo", return_value=_addrinfo("192.168.1.1")):
        with patch.object(
            httpx.AsyncHTTPTransport,
            "handle_async_request",
            new=super_mock,
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

    with patch("socket.getaddrinfo", return_value=_addrinfo("93.184.216.34")):
        with patch.object(
            httpx.AsyncHTTPTransport,
            "handle_async_request",
            new=super_mock,
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

    with patch("socket.getaddrinfo", side_effect=socket.gaierror("no such host")):
        with patch.object(
            httpx.AsyncHTTPTransport,
            "handle_async_request",
            new=super_mock,
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

    with patch("socket.getaddrinfo", return_value=_addrinfo("169.254.169.254")):
        with pytest.raises(httpx.ConnectError, match="dns-rebind-guard"):
            await transport.handle_async_request(req)


@pytest.mark.asyncio
async def test_loopback_blocked_allow_private_false():
    """127.0.0.1 is blocked when allow_private=False."""
    transport = RebindGuardedTransport(allow_private=False)
    with patch("socket.getaddrinfo", return_value=_addrinfo("127.0.0.1")):
        with pytest.raises(httpx.ConnectError, match="dns-rebind-guard"):
            await transport.handle_async_request(_make_request("localhost"))


@pytest.mark.asyncio
async def test_loopback_allowed_allow_private_true(monkeypatch):
    """127.0.0.1 is allowed when allow_private=True (local dev Ollama on localhost)."""
    transport = RebindGuardedTransport(allow_private=True)

    fake_response = httpx.Response(200, content=b"{}")
    super_mock = AsyncMock(return_value=fake_response)

    with patch("socket.getaddrinfo", return_value=_addrinfo("127.0.0.1")):
        with patch.object(
            httpx.AsyncHTTPTransport,
            "handle_async_request",
            new=super_mock,
        ):
            resp = await transport.handle_async_request(_make_request("localhost"))

    assert resp.status_code == 200
    super_mock.assert_awaited_once()
