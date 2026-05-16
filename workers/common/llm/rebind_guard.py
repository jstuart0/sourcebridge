"""DNS rebind guard for LLM HTTP transports.

Defence against DNS rebinding attacks on LLM base URLs:

  1. An admin saves ``base_url: https://attacker.tld`` — currently a public IP.
     The save-time SSRF guard (``validate_llm_base_url``) passes.
  2. The attacker flips the DNS record to ``169.254.169.254`` (IMDS) with TTL=0.
  3. On the worker's next LLM call the hostname resolves to the cloud metadata
     endpoint, leaking IAM credentials.

``RebindGuardedTransport`` closes this window by re-running the same private-IP
check that ``validate_llm_base_url`` applies at save time — but on *every* DNS
resolution at request time.

Two separate tiers of enforcement:
  - Cloud-metadata / link-local IPs (169.254.0.0/16 and equivalents): ALWAYS
    blocked, regardless of ``allow_private``.  An operator who sets
    ``allow_private=True`` for a self-hosted Ollama instance does not intend to
    allow IMDS access.
  - Other private/internal IPs: blocked only when ``allow_private=False``.

``allow_private=True`` (the default for Ollama/vLLM operators) therefore:
  - Permits 192.168.x.x, 10.x.x.x, loopback (127.0.0.1, ::1), etc. — required
    for local Ollama (http://localhost:11434) and self-hosted vLLM/llama.cpp.
  - Still blocks tier-1 cloud-metadata addresses: 169.254.169.254 and the
    entire 169.254.0.0/16 link-local range, fd00:ec2::/32 (AWS IMDSv2 IPv6),
    fe80::/10 (IPv6 link-local), and their IPv4-mapped forms (::ffff:<v4>).

Load-bearing constraints:
  - Do NOT add a flag to disable this guard entirely.  The only knob is
    ``allow_private`` (mirrors ``SOURCEBRIDGE_WORKER_LLM_ALLOW_PRIVATE_BASE_URL``).
  - ``socket.gaierror`` on the executor-offloaded DNS call falls through to
    ``super().handle_async_request`` — httpx owns the "host unreachable" error
    path; the guard only adds the DNS-rebind check.
  - IP literals in URLs are also checked because ``socket.getaddrinfo`` returns
    the literal address unchanged, and ``is_private_or_internal_ip`` handles it.
  - The guard is wired at provider *construction* time, so the transport is shared
    across all requests from the same provider instance.  That is correct — the
    check is per-request (``handle_async_request`` is called per HTTP request).
"""

from __future__ import annotations

import asyncio
import socket

import httpx

from workers.common.llm.ip_check import is_cloud_metadata_ip, is_private_or_internal_ip


class RebindGuardedTransport(httpx.AsyncHTTPTransport):
    """An httpx transport that re-validates the resolved IP at request time.

    Mitigates DNS rebind: a hostname that passed the save-time SSRF check can
    flip to 169.254.169.254 (or any private IP) before the worker issues the
    request.  This transport resolves the hostname fresh on every request and
    aborts with ``httpx.ConnectError`` if the resolved IP fails the private-IP
    check — matching the same logic as ``validate_llm_base_url``.

    Instantiated with ``allow_private`` mirroring
    ``WorkerConfig.llm_allow_private_base_url``.

    The DNS lookup is offloaded to a thread-pool executor so that the async
    event loop is not blocked during ``socket.getaddrinfo`` (which is a
    blocking syscall).  The default ``ThreadPoolExecutor`` has at least
    ``cpu_count + 4`` threads.  The LLM gate
    (``SOURCEBRIDGE_LLM_PROVIDER_*_MAX_CONCURRENT``, default hard cap 256)
    determines upper concurrency.  Operators on slow-DNS topologies should
    tune LLM concurrency caps to avoid thread-pool saturation; a dedicated
    executor config option is a deferred follow-up.
    """

    def __init__(self, allow_private: bool, **kwargs: object) -> None:
        super().__init__(**kwargs)
        self._allow_private = allow_private

    async def handle_async_request(self, request: httpx.Request) -> httpx.Response:
        host = request.url.host
        if host:
            loop = asyncio.get_running_loop()
            try:
                infos = await loop.run_in_executor(None, socket.getaddrinfo, host, None, 0, 0, socket.IPPROTO_TCP)
                # Pass-through on DNS failure — httpx emits its own host-unreachable
                # error.  Pool size: min(32, cpu_count+4) by default; see class
                # docstring for concurrency tuning guidance.
            except socket.gaierror:
                return await super().handle_async_request(request)

            for info in infos:
                ip = info[4][0]
                # Tier 1: cloud-metadata / link-local — always blocked.
                if is_cloud_metadata_ip(ip):
                    raise httpx.ConnectError(
                        f"dns-rebind-guard: refusing connection to cloud-metadata "
                        f"IP {ip!r} resolved from host {host!r}. "
                        "This is a likely DNS rebind attack. "
                        "See SECURITY.md and internal/indexing/pathutil/pathutil.go:266.",
                        request=request,
                    )
                # Tier 2: other private/internal IPs — blocked unless allow_private.
                if not self._allow_private and is_private_or_internal_ip(ip):
                    raise httpx.ConnectError(
                        f"dns-rebind-guard: refusing connection to private IP "
                        f"{ip!r} resolved from host {host!r} "
                        "(allow_private=False). "
                        "Set SOURCEBRIDGE_WORKER_LLM_ALLOW_PRIVATE_BASE_URL=true "
                        "for self-hosted internal LLM providers.",
                        request=request,
                    )
        return await super().handle_async_request(request)
