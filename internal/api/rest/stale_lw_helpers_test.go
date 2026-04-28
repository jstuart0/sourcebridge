// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import "testing"

func TestParseLWTargetKey(t *testing.T) {
	cases := []struct {
		in           string
		wantTenant   string
		wantRepoID   string
	}{
		{"lw:default:7c9d4387-5f3f-4acf-ac29-4b89d3f2922f", "default", "7c9d4387-5f3f-4acf-ac29-4b89d3f2922f"},
		{"lw:acme:repo-1", "acme", "repo-1"},
		{"lw:default:repo:with:colons", "default", "repo:with:colons"},
		{"qa:default:something", "", ""},
		{"", "", ""},
		{"lw", "", ""},
		{"lw:default", "", ""},
	}
	for _, tc := range cases {
		gotTenant, gotRepo := parseLWTargetKey(tc.in)
		if gotTenant != tc.wantTenant || gotRepo != tc.wantRepoID {
			t.Errorf("parseLWTargetKey(%q) = (%q, %q), want (%q, %q)",
				tc.in, gotTenant, gotRepo, tc.wantTenant, tc.wantRepoID)
		}
	}
}

func TestParseLWProgressMessage(t *testing.T) {
	cases := []struct {
		in            string
		wantPlanned   int
		wantGenerated int
	}{
		{"35/169 pages complete", 169, 35},
		{"0/12 pages complete", 12, 0},
		{"168/169 pages complete", 169, 168},
		{"Resolving page taxonomy", 0, 0},
		{"", 0, 0},
		{"foo/bar baz", 0, 0},
		{"35/abc pages", 0, 0},
		{"abc/12 pages", 0, 0},
	}
	for _, tc := range cases {
		gotPlanned, gotGenerated := parseLWProgressMessage(tc.in)
		if gotPlanned != tc.wantPlanned || gotGenerated != tc.wantGenerated {
			t.Errorf("parseLWProgressMessage(%q) = (%d, %d), want (%d, %d)",
				tc.in, gotPlanned, gotGenerated, tc.wantPlanned, tc.wantGenerated)
		}
	}
}
