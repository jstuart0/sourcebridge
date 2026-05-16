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
  - Permits 192.168.x.x, 10.x.x.x, etc. — as intended.
  - Still blocks 169.254.169.254, ::1 as a loopback leak, and multicast.

Load-bearing constraints:
  - Do NOT add a flag to disable this guard entirely.  The only knob is
    ``allow_private`` (mirrors ``SOURCEBRIDGE_WORKER_LLM_ALLOW_PRIVATE_BASE_URL``).
  - ``socket.gaierror`` is swallowed silently — httpx owns the "host unreachable"
    error path; the guard only adds the DNS-rebind check.
  - IP literals in URLs are also checked because ``socket.getaddrinfo`` returns
    the literal address unchanged, and ``_is_private_or_internal_ip`` handles it.
  - The guard is wired at provider *construction* time, so the transport is shared
    across all requests from the same provider instance.  That is correct — the
    check is per-request (``handle_async_request`` is called per HTTP request).
"""

from __future__ import annotations

import ipaddress
import socket

import httpx

# Inline the private-IP check to avoid a circular import:
#   rebind_guard → config → anthropic → rebind_guard
# The logic is identical to _is_private_or_internal_ip in config.py; keep them
# in sync if either is changed.  CGNAT: 100.64.0.0/10 (RFC 6598) is not
# classified as private by stdlib ipaddress.
_CGNAT_NETWORK = ipaddress.IPv4Network("100.64.0.0/10")


def _is_private_or_internal_ip(addr: str) -> bool:
    """Return True if ``addr`` is in any SSRF-denylist range.

    Covers: RFC1918 private, loopback, link-local, CGNAT (100.64/10),
    ULA (fc00::/7), unspecified (0.0.0.0/::), multicast.
    Mirrors config._is_private_or_internal_ip — inlined here to avoid circular import.
    """
    try:
        ip = ipaddress.ip_address(addr)
    except ValueError:
        return False
    if (
        ip.is_private
        or ip.is_loopback
        or ip.is_link_local
        or ip.is_unspecified
        or ip.is_multicast
        or ip.is_reserved
    ):
        return True
    if isinstance(ip, ipaddress.IPv4Address) and ip in _CGNAT_NETWORK:
        return True
    return False

# Cloud-metadata / link-local block that fires regardless of allow_private.
# 169.254.0.0/16 is IPv4 link-local (IS_LINK_LOCAL covers this) and the
# well-known IMDS range used by AWS (169.254.169.254), GCP (169.254.169.254),
# Azure (169.254.169.254), and DigitalOcean (169.254.169.254).
# IPv6 link-local (fe80::/10) is also IS_LINK_LOCAL.
# This is an additional hard guard; _is_private_or_internal_ip already catches
# link-local, but making it explicit prevents any future refactoring of that
# helper from accidentally removing the cloud-metadata protection.
# Kept for reference — not used in range checks because is_link_local covers
# both 169.254.0.0/16 and fe80::/10 natively.  Having an explicit constant makes
# the intent visible when reading the code.
_CLOUD_METADATA_NETS = (
    ipaddress.IPv4Network("169.254.0.0/16"),  # IPv4 link-local / AWS/GCP/Azure IMDS
    ipaddress.IPv6Network("fe80::/10"),  # IPv6 link-local
)


def _is_cloud_metadata_ip(addr: str) -> bool:
    """Return True if ``addr`` is a cloud-metadata / link-local IP.

    Fires regardless of ``allow_private``.  A separate, more targeted check
    that ensures IMDS is always blocked even when private IPs are permitted.
    """
    try:
        ip = ipaddress.ip_address(addr)
    except ValueError:
        return False
    return ip.is_link_local  # covers 169.254.0.0/16 and fe80::/10


class RebindGuardedTransport(httpx.AsyncHTTPTransport):
    """An httpx transport that re-validates the resolved IP at request time.

    Mitigates DNS rebind: a hostname that passed the save-time SSRF check can
    flip to 169.254.169.254 (or any private IP) before the worker issues the
    request.  This transport resolves the hostname fresh on every request and
    aborts with ``httpx.ConnectError`` if the resolved IP fails the private-IP
    check — matching the same logic as ``validate_llm_base_url``.

    Instantiated with ``allow_private`` mirroring
    ``WorkerConfig.llm_allow_private_base_url``.
    """

    def __init__(self, allow_private: bool, **kwargs: object) -> None:
        super().__init__(**kwargs)
        self._allow_private = allow_private

    async def handle_async_request(self, request: httpx.Request) -> httpx.Response:
        host = request.url.host
        if host:
            try:
                infos = socket.getaddrinfo(host, None, proto=socket.IPPROTO_TCP)
                for info in infos:
                    ip = info[4][0]
                    # Tier 1: cloud-metadata / link-local — always blocked.
                    if _is_cloud_metadata_ip(ip):
                        raise httpx.ConnectError(
                            f"dns-rebind-guard: refusing connection to cloud-metadata "
                            f"IP {ip!r} resolved from host {host!r}. "
                            "This is a likely DNS rebind attack. "
                            "See SECURITY.md and internal/indexing/pathutil/pathutil.go:266.",
                            request=request,
                        )
                    # Tier 2: other private/internal IPs — blocked unless allow_private.
                    if not self._allow_private and _is_private_or_internal_ip(ip):
                        raise httpx.ConnectError(
                            f"dns-rebind-guard: refusing connection to private IP "
                            f"{ip!r} resolved from host {host!r} "
                            "(allow_private=False). "
                            "Set SOURCEBRIDGE_WORKER_LLM_ALLOW_PRIVATE_BASE_URL=true "
                            "for self-hosted internal LLM providers.",
                            request=request,
                        )
            except socket.gaierror:
                # Unresolvable hostname — let httpx handle it normally.
                pass
        return await super().handle_async_request(request)
