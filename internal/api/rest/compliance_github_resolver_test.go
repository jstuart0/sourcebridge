//go:build enterprise

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import "testing"

func TestParseGitHubRemote(t *testing.T) {
	cases := []struct {
		remote     string
		owner      string
		name       string
		ok         bool
	}{
		{"https://github.com/acme/svc-payments", "acme", "svc-payments", true},
		{"https://github.com/acme/svc-payments.git", "acme", "svc-payments", true},
		{"https://www.github.com/acme/repo", "acme", "repo", true},
		{"git@github.com:acme/svc.git", "acme", "svc", true},
		{"git@github.com:acme/svc", "acme", "svc", true},
		{"https://gitlab.com/acme/svc", "", "", false},
		{"", "", "", false},
		{"not-a-url", "", "", false},
		{"https://github.com/acme", "", "", false},
	}
	for _, tc := range cases {
		o, n, ok := parseGitHubRemote(tc.remote)
		if ok != tc.ok || o != tc.owner || n != tc.name {
			t.Errorf("parseGitHubRemote(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.remote, o, n, ok, tc.owner, tc.name, tc.ok)
		}
	}
}
