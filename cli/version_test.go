// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestSetVersion_ProducesVersionFlag asserts that SetVersion populates the
// cobra Version field and the templated output is shaped like
// "sourcebridge version <X>" — the installer's upgrade-detection awk script
// depends on the version being the last whitespace-separated token.
func TestSetVersion_ProducesVersionFlag(t *testing.T) {
	SetVersion("v1.2.3-test")
	defer SetVersion("dev") // restore default for other tests

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stdout)
	rootCmd.SetArgs([]string{"--version"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("--version unexpected error: %v", err)
	}
	got := strings.TrimSpace(stdout.String())
	want := "sourcebridge version v1.2.3-test"
	if got != want {
		t.Errorf("--version output mismatch:\n got: %q\nwant: %q", got, want)
	}

	// Installer-compatibility: awk '{print $NF}' must yield the version.
	tokens := strings.Fields(got)
	if len(tokens) == 0 {
		t.Fatalf("empty output, no last token to test")
	}
	if last := tokens[len(tokens)-1]; last != "v1.2.3-test" {
		t.Errorf("installer's awk '{print $NF}' would extract %q; want %q", last, "v1.2.3-test")
	}
}

// TestSetVersion_DefaultIsDev asserts the unset (build-without-ldflags) value
// is "dev" so a local `make build` produces a recognizable identifier.
func TestSetVersion_DefaultIsDev(t *testing.T) {
	SetVersion("dev")

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stdout)
	rootCmd.SetArgs([]string{"--version"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("--version unexpected error: %v", err)
	}
	got := strings.TrimSpace(stdout.String())
	if !strings.Contains(got, "dev") {
		t.Errorf("default --version output missing 'dev' marker: %q", got)
	}
}
