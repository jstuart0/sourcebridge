// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package installassets

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScript_NotEmpty verifies the embedded bytes are present (the //go:embed
// directive resolved at build time).
func TestScript_NotEmpty(t *testing.T) {
	b := Script()
	if len(b) == 0 {
		t.Fatal("embedded install.sh is empty")
	}
	// Sanity-check the shebang line.
	if !bytes.HasPrefix(b, []byte("#!/bin/sh")) {
		t.Errorf("install.sh does not start with #!/bin/sh; got: %q", string(b[:min(64, len(b))]))
	}
}

// TestScript_MatchesScriptsSymlink verifies the repo-conventional
// scripts/install.sh symlink points at the same bytes as the embed source.
// This is the "single source of truth" invariant — if either gets out of
// sync, this test fails before the binary ships.
func TestScript_MatchesScriptsSymlink(t *testing.T) {
	// Tests run with cwd at the package dir (internal/installassets).
	// The repo's scripts/install.sh is two levels up.
	scriptsLink := filepath.Join("..", "..", "scripts", "install.sh")

	scriptsBytes, err := os.ReadFile(scriptsLink)
	if err != nil {
		t.Skipf("scripts/install.sh not accessible from this layout: %v", err)
	}
	if !bytes.Equal(scriptsBytes, Script()) {
		t.Errorf("scripts/install.sh and embedded install.sh differ — single-source-of-truth invariant broken")
	}
}

// TestHandler_ServesEmbeddedBytes verifies the HTTP handler returns the
// embedded script with the right headers.
func TestHandler_ServesEmbeddedBytes(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/install.sh")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d; want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/x-shellscript") {
		t.Errorf("Content-Type = %q; want text/x-shellscript", ct)
	}
	if cto := resp.Header.Get("X-Content-Type-Options"); cto != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q; want nosniff", cto)
	}
	got := make([]byte, len(Script()))
	n, _ := resp.Body.Read(got)
	if !bytes.HasPrefix(got[:n], []byte("#!/bin/sh")) {
		t.Errorf("body does not start with #!/bin/sh; got: %q", string(got[:min(64, n)]))
	}
}

// min — Go 1.21+ has it builtin; defining locally keeps the test self-contained
// for older toolchains used in tests.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
