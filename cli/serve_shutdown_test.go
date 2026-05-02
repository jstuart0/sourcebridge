// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// cli/serve_shutdown_test.go — regression tests for serve.go signal handling
// and security hardening introduced in CA-142 codex r2 reconcile.
package cli

import (
	"testing"
)

// TestValidateLoopbackAddr_AcceptsLoopback verifies that well-known loopback
// addresses are accepted by validateLoopbackAddr. CA-142 High.
func TestValidateLoopbackAddr_AcceptsLoopback(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		addr string
	}{
		{"IPv4 loopback", "127.0.0.1:8081"},
		{"IPv4 loopback alt port", "127.0.0.1:9999"},
		{"localhost", "localhost:8081"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := validateLoopbackAddr(tc.addr); err != nil {
				t.Fatalf("validateLoopbackAddr(%q) = %v; want nil", tc.addr, err)
			}
		})
	}
}

// TestValidateLoopbackAddr_RejectsNonLoopback verifies that non-loopback and
// all-interface addresses are rejected. CA-142 High.
func TestValidateLoopbackAddr_RejectsNonLoopback(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		addr string
	}{
		{"all interfaces IPv4", "0.0.0.0:8081"},
		{"all interfaces bare", ":8081"},
		{"public IP", "192.168.1.1:8081"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := validateLoopbackAddr(tc.addr); err == nil {
				t.Fatalf("validateLoopbackAddr(%q) = nil; want error for non-loopback address", tc.addr)
			}
		})
	}
}

// TestValidateLoopbackAddr_RejectsMalformed verifies that malformed addresses
// are rejected. CA-142 High.
func TestValidateLoopbackAddr_RejectsMalformed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		addr string
	}{
		{"no port", "127.0.0.1"},
		{"empty", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := validateLoopbackAddr(tc.addr); err == nil {
				t.Fatalf("validateLoopbackAddr(%q) = nil; want error for malformed address", tc.addr)
			}
		})
	}
}
