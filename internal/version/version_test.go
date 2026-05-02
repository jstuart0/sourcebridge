// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package version

import (
	"runtime"
	"strings"
	"testing"
)

// TestDefaults asserts the package's exported variables have sensible
// fallback values when the binary is built without -ldflags. Production
// builds override these via the Makefile / Dockerfile / GHA wiring.
func TestDefaults(t *testing.T) {
	cases := []struct {
		name string
		got  string
	}{
		{"Version", Version},
		{"Commit", Commit},
		{"BuildDate", BuildDate},
		{"Edition", Edition},
	}
	for _, tc := range cases {
		if tc.got == "" {
			t.Errorf("%s must not be empty (got %q)", tc.name, tc.got)
		}
	}
}

// TestGoRuntimeMatchesRuntime asserts GoRuntime() returns the same string
// runtime.Version() does — i.e. it cannot be ldflag-mutated.
func TestGoRuntimeMatchesRuntime(t *testing.T) {
	got := GoRuntime()
	want := runtime.Version()
	if got != want {
		t.Errorf("GoRuntime() = %q, want %q", got, want)
	}
	// Sanity: runtime.Version() always starts with "go".
	if !strings.HasPrefix(got, "go") {
		t.Errorf("GoRuntime() = %q, expected go-prefixed runtime version", got)
	}
}
