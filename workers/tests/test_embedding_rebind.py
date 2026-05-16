"""Wiring contract tests: embedding providers must use RebindGuardedTransport.

D-013 / X-H1 / CA-463: both embedding providers (Ollama-native and
OpenAI-compat) connect to operator-configured base_url values and MUST
enforce DNS-rebind protection with RebindGuardedTransport.

These tests verify the wiring contract without making real HTTP calls:
  - That RebindGuardedTransport is instantiated per-call.
  - That the allow_private flag is correctly threaded from the constructor.
  - That cloud-metadata IPs are blocked regardless of allow_private.
"""

from __future__ import annotations

import json
import socket
from unittest.mock import AsyncMock, patch

import httpx
import pytest

from workers.common.embedding.ollama import OllamaEmbeddingProvider
from workers.common.embedding.openai_compat import OpenAICompatEmbeddingProvider
from workers.common.llm.rebind_guard import RebindGuardedTransport

# ---------------------------------------------------------------------------
# OllamaEmbeddingProvider
# ---------------------------------------------------------------------------


class TestOllamaEmbeddingProviderReBindGuard:
    """OllamaEmbeddingProvider wires RebindGuardedTransport on every request."""

    @pytest.mark.asyncio
    async def test_uses_rebind_guard_transport(self):
        """_embed_batch constructs a RebindGuardedTransport with allow_private=True."""
        provider = OllamaEmbeddingProvider(
            base_url="http://localhost:11434",
            allow_private=True,
        )

        transport_instances: list[RebindGuardedTransport] = []

        original_init = RebindGuardedTransport.__init__

        def capture_init(self_inner, allow_private: bool, **kwargs: object) -> None:
            original_init(self_inner, allow_private=allow_private, **kwargs)
            transport_instances.append(self_inner)

        fake_response = httpx.Response(
            200,
            content=b'{"embeddings": [[0.1, 0.2, 0.3]]}',
        )

        with (
            patch.object(RebindGuardedTransport, "__init__", capture_init),
            patch.object(
                httpx.AsyncHTTPTransport,
                "handle_async_request",
                new=AsyncMock(return_value=fake_response),
            ),
        ):
            await provider._embed_batch(["hello"])

        assert len(transport_instances) >= 1
        assert transport_instances[0]._allow_private is True

    @pytest.mark.asyncio
    async def test_allow_private_false_threaded(self):
        """allow_private=False is passed to RebindGuardedTransport.

        Uses a public IP (93.184.216.34) so the guard doesn't block the
        request — the assertion is purely on the transport flag value.
        """
        import socket

        provider = OllamaEmbeddingProvider(
            base_url="http://93.184.216.34:11434",
            allow_private=False,
        )

        transport_instances: list[RebindGuardedTransport] = []

        original_init = RebindGuardedTransport.__init__

        def capture_init(self_inner, allow_private: bool, **kwargs: object) -> None:
            original_init(self_inner, allow_private=allow_private, **kwargs)
            transport_instances.append(self_inner)

        fake_response = httpx.Response(
            200,
            content=b'{"embeddings": [[0.1, 0.2]]}',
        )

        def _addrinfo_public(ip: str) -> list:
            return [(socket.AF_INET, socket.SOCK_STREAM, 6, "", (ip, 11434))]

        with (
            patch.object(RebindGuardedTransport, "__init__", capture_init),
            patch.object(
                httpx.AsyncHTTPTransport,
                "handle_async_request",
                new=AsyncMock(return_value=fake_response),
            ),
            patch("workers.common.llm.rebind_guard.socket.getaddrinfo", return_value=_addrinfo_public("93.184.216.34")),
        ):
            await provider._embed_batch(["world"])

        assert transport_instances[0]._allow_private is False

    @pytest.mark.asyncio
    async def test_cloud_metadata_blocked_allow_private_true(self):
        """169.254.169.254 is blocked even with allow_private=True."""
        import socket

        provider = OllamaEmbeddingProvider(
            base_url="http://169.254.169.254:11434",
            allow_private=True,
        )

        def _addrinfo(ip: str) -> list:
            return [(socket.AF_INET, socket.SOCK_STREAM, 6, "", (ip, 11434))]

        with (
            patch("socket.getaddrinfo", return_value=_addrinfo("169.254.169.254")),
            pytest.raises(httpx.ConnectError, match="dns-rebind-guard"),
        ):
            await provider._embed_batch(["test"])

    @pytest.mark.asyncio
    async def test_close_is_noop(self):
        """close() is a no-op for per-call clients."""
        provider = OllamaEmbeddingProvider()
        # Should not raise
        await provider.close()


# ---------------------------------------------------------------------------
# OpenAICompatEmbeddingProvider
# ---------------------------------------------------------------------------


class TestOpenAICompatEmbeddingProviderRebindGuard:
    """OpenAICompatEmbeddingProvider wires RebindGuardedTransport on every request."""

    @pytest.mark.asyncio
    async def test_uses_rebind_guard_transport(self):
        """_embed_batch constructs a RebindGuardedTransport with allow_private=True."""
        provider = OpenAICompatEmbeddingProvider(
            base_url="http://localhost:11434",
            allow_private=True,
        )

        transport_instances: list[RebindGuardedTransport] = []

        original_init = RebindGuardedTransport.__init__

        def capture_init(self_inner, allow_private: bool, **kwargs: object) -> None:
            original_init(self_inner, allow_private=allow_private, **kwargs)
            transport_instances.append(self_inner)

        fake_data = [{"index": 0, "embedding": [0.1, 0.2, 0.3]}]
        fake_response = httpx.Response(
            200,
            content=json.dumps({"data": fake_data}).encode(),
        )

        with (
            patch.object(RebindGuardedTransport, "__init__", capture_init),
            patch.object(
                httpx.AsyncHTTPTransport,
                "handle_async_request",
                new=AsyncMock(return_value=fake_response),
            ),
        ):
            await provider._embed_batch(["hello"])

        assert len(transport_instances) >= 1
        assert transport_instances[0]._allow_private is True

    @pytest.mark.asyncio
    async def test_allow_private_false_threaded(self):
        """allow_private=False is passed to RebindGuardedTransport.

        Uses a public IP so the guard doesn't block the request — the assertion
        is purely on the transport flag value.
        """
        provider = OpenAICompatEmbeddingProvider(
            base_url="http://93.184.216.34:8080",
            allow_private=False,
        )

        transport_instances: list[RebindGuardedTransport] = []

        original_init = RebindGuardedTransport.__init__

        def capture_init(self_inner, allow_private: bool, **kwargs: object) -> None:
            original_init(self_inner, allow_private=allow_private, **kwargs)
            transport_instances.append(self_inner)

        fake_data = [{"index": 0, "embedding": [0.1]}]
        fake_response = httpx.Response(
            200,
            content=json.dumps({"data": fake_data}).encode(),
        )

        def _addrinfo_public(ip: str) -> list:
            return [(socket.AF_INET, socket.SOCK_STREAM, 6, "", (ip, 8080))]

        with (
            patch.object(RebindGuardedTransport, "__init__", capture_init),
            patch.object(
                httpx.AsyncHTTPTransport,
                "handle_async_request",
                new=AsyncMock(return_value=fake_response),
            ),
            patch("workers.common.llm.rebind_guard.socket.getaddrinfo", return_value=_addrinfo_public("93.184.216.34")),
        ):
            await provider._embed_batch(["world"])

        assert transport_instances[0]._allow_private is False

    @pytest.mark.asyncio
    async def test_cloud_metadata_blocked_allow_private_true(self):
        """169.254.169.254 is blocked even with allow_private=True."""
        import socket

        provider = OpenAICompatEmbeddingProvider(
            base_url="http://169.254.169.254:8080",
            allow_private=True,
        )

        def _addrinfo(ip: str) -> list:
            return [(socket.AF_INET, socket.SOCK_STREAM, 6, "", (ip, 8080))]

        with (
            patch("socket.getaddrinfo", return_value=_addrinfo("169.254.169.254")),
            pytest.raises(httpx.ConnectError, match="dns-rebind-guard"),
        ):
            await provider._embed_batch(["test"])

    @pytest.mark.asyncio
    async def test_close_is_noop(self):
        """close() is a no-op for per-call clients."""
        provider = OpenAICompatEmbeddingProvider()
        # Should not raise
        await provider.close()
