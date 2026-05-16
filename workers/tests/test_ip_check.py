"""Tests for workers.common.llm.ip_check.

Covers each SSRF-denylist branch for is_private_or_internal_ip and the
always-blocked cloud-metadata gate in is_cloud_metadata_ip.

Each test uses an individual CIDR assertion to confirm that the extracted
module subsumes all classification logic previously duplicated between
config.py and rebind_guard.py.
"""

from __future__ import annotations

from workers.common.llm.ip_check import CGNAT_NETWORK, is_cloud_metadata_ip, is_private_or_internal_ip

# ---------------------------------------------------------------------------
# is_private_or_internal_ip
# ---------------------------------------------------------------------------


class TestIsPrivateOrInternalIp:
    """Unit tests for is_private_or_internal_ip."""

    def test_rfc1918_class_a(self):
        """10.0.0.1 — RFC 1918 private."""
        assert is_private_or_internal_ip("10.0.0.1") is True

    def test_rfc1918_class_b(self):
        """172.16.0.1 — RFC 1918 private."""
        assert is_private_or_internal_ip("172.16.0.1") is True

    def test_rfc1918_class_c(self):
        """192.168.1.1 — RFC 1918 private."""
        assert is_private_or_internal_ip("192.168.1.1") is True

    def test_loopback_ipv4(self):
        """127.0.0.1 — loopback."""
        assert is_private_or_internal_ip("127.0.0.1") is True

    def test_loopback_ipv6(self):
        """::1 — IPv6 loopback."""
        assert is_private_or_internal_ip("::1") is True

    def test_link_local_ipv4(self):
        """169.254.169.254 — IPv4 link-local / cloud-metadata."""
        assert is_private_or_internal_ip("169.254.169.254") is True

    def test_link_local_ipv6(self):
        """fe80::1 — IPv6 link-local."""
        assert is_private_or_internal_ip("fe80::1") is True

    def test_cgnat(self):
        """100.64.0.1 — CGNAT (RFC 6598), not covered by stdlib is_private on older Pythons."""
        assert is_private_or_internal_ip("100.64.0.1") is True

    def test_cgnat_boundary_low(self):
        """100.64.0.0 — first address in CGNAT range."""
        assert is_private_or_internal_ip("100.64.0.0") is True

    def test_cgnat_boundary_high(self):
        """100.127.255.255 — last address in CGNAT range."""
        assert is_private_or_internal_ip("100.127.255.255") is True

    def test_cgnat_just_outside(self):
        """100.128.0.0 — just outside CGNAT range → public."""
        assert is_private_or_internal_ip("100.128.0.0") is False

    def test_unspecified_ipv4(self):
        """0.0.0.0 — unspecified."""
        assert is_private_or_internal_ip("0.0.0.0") is True

    def test_unspecified_ipv6(self):
        """:: — IPv6 unspecified."""
        assert is_private_or_internal_ip("::") is True

    def test_multicast_ipv4(self):
        """224.0.0.1 — multicast."""
        assert is_private_or_internal_ip("224.0.0.1") is True

    def test_public_ipv4(self):
        """93.184.216.34 (example.com) — public address."""
        assert is_private_or_internal_ip("93.184.216.34") is False

    def test_public_ipv6(self):
        """2001:4860:4860::8888 (Google DNS) — public."""
        assert is_private_or_internal_ip("2001:4860:4860::8888") is False

    def test_unparseable_string(self):
        """Non-IP string returns False (caller handles it)."""
        assert is_private_or_internal_ip("not-an-ip") is False

    def test_empty_string(self):
        """Empty string returns False."""
        assert is_private_or_internal_ip("") is False


# ---------------------------------------------------------------------------
# is_cloud_metadata_ip
# ---------------------------------------------------------------------------


class TestIsCloudMetadataIp:
    """Unit tests for is_cloud_metadata_ip.

    Cloud-metadata block fires regardless of allow_private; covers
    the IMDS endpoints used by AWS, GCP, Azure, and DigitalOcean.
    """

    def test_aws_imds(self):
        """169.254.169.254 — AWS/GCP/Azure/DO IMDS."""
        assert is_cloud_metadata_ip("169.254.169.254") is True

    def test_link_local_first(self):
        """169.254.0.1 — first usable link-local address."""
        assert is_cloud_metadata_ip("169.254.0.1") is True

    def test_link_local_last(self):
        """169.254.255.254 — last usable link-local address."""
        assert is_cloud_metadata_ip("169.254.255.254") is True

    def test_ipv6_link_local(self):
        """fe80::1 — IPv6 link-local."""
        assert is_cloud_metadata_ip("fe80::1") is True

    def test_private_rfc1918_not_cloud_metadata(self):
        """192.168.1.1 — private but NOT cloud-metadata."""
        assert is_cloud_metadata_ip("192.168.1.1") is False

    def test_public_not_cloud_metadata(self):
        """93.184.216.34 — public, not cloud-metadata."""
        assert is_cloud_metadata_ip("93.184.216.34") is False

    def test_loopback_not_cloud_metadata(self):
        """127.0.0.1 — loopback is private but not link-local / cloud-metadata."""
        assert is_cloud_metadata_ip("127.0.0.1") is False

    def test_unparseable(self):
        """Non-IP string returns False."""
        assert is_cloud_metadata_ip("not-an-ip") is False


# ---------------------------------------------------------------------------
# CGNAT_NETWORK constant
# ---------------------------------------------------------------------------


class TestCgnatNetwork:
    """CGNAT_NETWORK constant represents the correct RFC 6598 range."""

    def test_cgnat_network_prefix(self):
        import ipaddress

        assert ipaddress.IPv4Network("100.64.0.0/10") == CGNAT_NETWORK
