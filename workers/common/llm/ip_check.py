"""Canonical IP-classification module for SSRF and DNS-rebind guards.

This is the single source of truth for private/internal IP detection across:
  - Save-time SSRF guard (``workers.common.llm.config.validate_llm_base_url``)
  - Request-time DNS-rebind guard (``workers.common.llm.rebind_guard.RebindGuardedTransport``)

Keeping the logic here breaks the circular import that previously existed when
``rebind_guard`` tried to import from ``config`` (which imports ``anthropic``,
which imports ``rebind_guard``).

No internal project imports — stdlib ``ipaddress`` only.
"""

from __future__ import annotations

import ipaddress

# CGNAT: 100.64.0.0/10 (RFC 6598).  ``ipaddress.is_private`` does not cover
# this range in Python < 3.11 (fixed in cpython#71000).  Explicit constant
# ensures correct classification on all supported Python versions.
CGNAT_NETWORK = ipaddress.IPv4Network("100.64.0.0/10")

# AWS IMDSv2 IPv6 endpoint (announced 2024).  ULA range — not link-local, so
# ``is_link_local`` misses it.  Blocked at tier 1 (cloud-metadata) regardless
# of ``allow_private``.  GCP metadata resolves to 169.254.169.254 today and
# does NOT use this range; only fd00:ec2::/32 is listed here.
_IMDS_V6_NETWORKS = (ipaddress.IPv6Network("fd00:ec2::/32"),)


def _unwrap_ipv4_mapped(
    ip: ipaddress.IPv4Address | ipaddress.IPv6Address,
) -> ipaddress.IPv4Address | ipaddress.IPv6Address:
    """Return the underlying IPv4Address if ``ip`` is an IPv4-mapped IPv6 address.

    ``::ffff:169.254.169.254`` is an ``IPv6Address`` with ``is_link_local=False``
    and ``is_private=False`` — it bypasses all stdlib classification checks that
    operate on the IPv6 type.  Unwrapping to the mapped IPv4 form before
    classification closes this bypass vector.
    """
    if isinstance(ip, ipaddress.IPv6Address) and ip.ipv4_mapped is not None:
        return ip.ipv4_mapped
    return ip


def is_private_or_internal_ip(addr: str) -> bool:
    """Return True if ``addr`` is in any SSRF-denylist range.

    Covers:
    - RFC 1918 private (10/8, 172.16/12, 192.168/16)
    - Loopback (127.0.0.1/8, ::1)
    - Link-local / cloud-metadata (169.254.0.0/16, fe80::/10)
    - CGNAT (100.64.0.0/10, RFC 6598)
    - ULA (fc00::/7)
    - Unspecified (0.0.0.0, ::)
    - Multicast
    - Reserved
    - IPv4-mapped IPv6 (::ffff:<v4>) — unwrapped to underlying IPv4 form

    Returns False for an unparseable ``addr`` (not an IP address string).
    """
    try:
        ip = ipaddress.ip_address(addr)
    except ValueError:
        return False
    ip = _unwrap_ipv4_mapped(ip)
    if ip.is_private or ip.is_loopback or ip.is_link_local or ip.is_unspecified or ip.is_multicast or ip.is_reserved:
        return True
    if isinstance(ip, ipaddress.IPv4Address) and ip in CGNAT_NETWORK:
        return True
    return False


def is_cloud_metadata_ip(addr: str) -> bool:
    """Return True if ``addr`` is a cloud-metadata / link-local IP.

    This check fires regardless of ``allow_private``.  An operator who sets
    ``allow_private=True`` for a self-hosted Ollama instance does not intend
    to allow AWS/GCP/Azure IMDS (169.254.169.254) access.

    Coverage:
    - 169.254.0.0/16 — IPv4 link-local; used by AWS, GCP, Azure, DigitalOcean IMDS
    - fe80::/10      — IPv6 link-local
    - fd00:ec2::/32  — AWS IMDSv2 IPv6 endpoint (ULA, not link-local)
    - ::ffff:<v4>    — IPv4-mapped IPv6 form of any of the above (unwrapped before check)

    ``is_link_local`` covers 169.254.0.0/16 and fe80::/10; the explicit
    ``_IMDS_V6_NETWORKS`` check covers the ULA-range AWS IMDSv2 IPv6 endpoint.
    The function is kept separate from ``is_private_or_internal_ip`` so that
    callers can enforce the cloud-metadata block with a single, named check
    even when ``allow_private=True`` would otherwise permit the address.
    """
    try:
        ip = ipaddress.ip_address(addr)
    except ValueError:
        return False
    ip = _unwrap_ipv4_mapped(ip)
    if ip.is_link_local:
        return True
    if isinstance(ip, ipaddress.IPv6Address):
        for net in _IMDS_V6_NETWORKS:
            if ip in net:
                return True
    return False
