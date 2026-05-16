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

    Returns False for an unparseable ``addr`` (not an IP address string).
    """
    try:
        ip = ipaddress.ip_address(addr)
    except ValueError:
        return False
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

    Both ranges are subsumed by ``ipaddress.ip_address.is_link_local``.
    The function is kept separate from ``is_private_or_internal_ip`` so that
    callers can enforce the cloud-metadata block with a single, named check
    even when ``allow_private=True`` would otherwise permit the address.
    """
    try:
        ip = ipaddress.ip_address(addr)
    except ValueError:
        return False
    return ip.is_link_local  # covers 169.254.0.0/16 AND fe80::/10
