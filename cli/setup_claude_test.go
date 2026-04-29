// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"context"
	"strings"
	"testing"
)

func TestSanitizeStatusBody(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"with\x1b[31mansi", "with?[31mansi"},
		{strings.Repeat("a", 600), strings.Repeat("a", 512) + "… [truncated]"},
		{"  \n\nbody\n  ", "body"},
		{"emoji \xf0\x9f\x98\x80 here", "emoji ???? here"},
	}
	for _, tc := range cases {
		got := sanitizeStatusBody(tc.in)
		if got != tc.want {
			t.Errorf("sanitizeStatusBody(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatSymbolCount(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{999, "999"},
		{1000, "1,000"},
		{1247, "1,247"},
		{10000, "10,000"},
		{100000, "100,000"},
		{1000000, "1,000,000"},
	}
	for _, c := range cases {
		got := formatSymbolCount(c.n)
		if got != c.want {
			t.Errorf("formatSymbolCount(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestActionTag(t *testing.T) {
	cases := []struct {
		action string
		want   string
	}{
		{"create", "CREATE"},
		{"update", "MODIFY"},
		{"unchanged", "UNCHANGED"},
		{"skip-user-modified", "SKIP — user-modified"},
		{"skip-orphan-marker", "SKIP — orphan marker"},
	}
	for _, c := range cases {
		got := actionTag(c.action)
		if got != c.want {
			t.Errorf("actionTag(%q) = %q, want %q", c.action, got, c.want)
		}
	}
}

func TestSetupClaudeCmd_ErrorWhenServerUnreachable(t *testing.T) {
	// Override the server flag to a non-existent server and verify the
	// command fails with the expected error pattern.
	//
	// We can't call runSetupClaude directly because it calls cmd.Context()
	// which panics without a running cobra context. Instead we verify that
	// the command is wired and the error path logic is exercised via the
	// helper functions.
	orig := setupClaudeServer
	setupClaudeServer = "http://localhost:19999"
	defer func() { setupClaudeServer = orig }()

	// probeServerReachability should fail for a non-existent server.
	ctx := context.Background()
	err := probeServerReachability(ctx, "http://localhost:19999")
	// The probe either returns nil (server not found treated as older server)
	// or an error. The important thing is that fetchClusters will fail.
	_ = err // probe errors are non-fatal by design (older server fallback)

	// fetchClusters should fail for a non-existent server.
	_, fetchErr := fetchClusters(ctx, "http://localhost:19999", "test-repo")
	if fetchErr == nil {
		t.Error("expected error fetching clusters from non-existent server")
	}
}
