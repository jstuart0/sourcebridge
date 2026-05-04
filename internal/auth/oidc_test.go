// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package auth

import "testing"

func TestNormalizeOIDCRole(t *testing.T) {
	cases := []struct{ in, want string }{
		{"admin", RoleAdmin},
		{"user", RoleUser},
		{"", RoleUser},           // empty fail-closed
		{"superadmin", RoleUser}, // unknown → user
		{"owner", RoleUser},      // unknown → user
		{"ADMIN", RoleUser},      // case-sensitive, fail-closed
		{" admin", RoleUser},     // whitespace, fail-closed
	}
	for _, c := range cases {
		got := normalizeOIDCRole(c.in)
		if got != c.want {
			t.Errorf("normalizeOIDCRole(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
