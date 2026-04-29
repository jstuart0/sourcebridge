// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package resolution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHKeyPathValidator_Empty(t *testing.T) {
	v := NewSSHKeyPathValidator("")
	if err := v.Validate(""); err != nil {
		t.Fatalf("empty should be allowed: %v", err)
	}
}

func TestSSHKeyPathValidator_DefaultRoot_Accepts(t *testing.T) {
	v := NewSSHKeyPathValidator("")
	good := []string{
		"/etc/sourcebridge/git-keys",                  // root itself
		"/etc/sourcebridge/git-keys/id_ed25519",       // direct child
		"/etc/sourcebridge/git-keys/sub/dir/id_rsa",   // nested
		"/etc/sourcebridge/git-keys/key-with-dashes",  // safe chars
		"/etc/sourcebridge/git-keys/key.with.dots",    // dots
		"/etc/sourcebridge/git-keys/key_with_under",   // underscores
	}
	for _, p := range good {
		if err := v.Validate(p); err != nil {
			t.Errorf("Validate(%q) want ok, got %v", p, err)
		}
	}
}

func TestSSHKeyPathValidator_DefaultRoot_RejectsRelative(t *testing.T) {
	v := NewSSHKeyPathValidator("")
	bad := []string{
		"id_rsa",
		"./id_rsa",
		"keys/id_rsa",
	}
	for _, p := range bad {
		if err := v.Validate(p); err == nil {
			t.Errorf("Validate(%q) should reject relative path", p)
		}
	}
}

func TestSSHKeyPathValidator_RejectsTraversal(t *testing.T) {
	v := NewSSHKeyPathValidator("")
	bad := []string{
		"/etc/sourcebridge/git-keys/../id_rsa",
		"/etc/sourcebridge/git-keys/sub/../id_rsa",
		"/etc/sourcebridge//git-keys/id_rsa", // redundant separator
	}
	for _, p := range bad {
		if err := v.Validate(p); err == nil {
			t.Errorf("Validate(%q) should reject traversal/redundant", p)
		}
	}
}

func TestSSHKeyPathValidator_RejectsShellMeta(t *testing.T) {
	v := NewSSHKeyPathValidator("")
	bad := []string{
		"/etc/sourcebridge/git-keys/id; rm -rf /",
		"/etc/sourcebridge/git-keys/id&touch x",
		"/etc/sourcebridge/git-keys/id|cat",
		"/etc/sourcebridge/git-keys/id$VAR",
		"/etc/sourcebridge/git-keys/id`whoami`",
		"/etc/sourcebridge/git-keys/id\"x",
		"/etc/sourcebridge/git-keys/id'x",
		"/etc/sourcebridge/git-keys/id (x)",
		"/etc/sourcebridge/git-keys/id<x",
		"/etc/sourcebridge/git-keys/id>x",
		"/etc/sourcebridge/git-keys/id*",
		"/etc/sourcebridge/git-keys/id\\x",
		"/etc/sourcebridge/git-keys/id\nx",
		"/etc/sourcebridge/git-keys/id\tx",
		"/etc/sourcebridge/git-keys/id with space",
	}
	for _, p := range bad {
		if err := v.Validate(p); err == nil {
			t.Errorf("Validate(%q) should reject shell metachar", p)
		}
	}
}

func TestSSHKeyPathValidator_RejectsGlob(t *testing.T) {
	v := NewSSHKeyPathValidator("")
	bad := []string{
		"/etc/sourcebridge/git-keys/id?",
		"/etc/sourcebridge/git-keys/id[abc]",
		"/etc/sourcebridge/git-keys/id{a,b}",
	}
	for _, p := range bad {
		if err := v.Validate(p); err == nil {
			t.Errorf("Validate(%q) should reject glob char", p)
		}
	}
}

func TestSSHKeyPathValidator_RejectsOutsideRoot(t *testing.T) {
	v := NewSSHKeyPathValidator("")
	bad := []string{
		"/etc/passwd",
		"/etc/sourcebridge/other/id",      // sibling subtree
		"/etc/sourcebridge/git-keys-bad",  // looks-like-root prefix
	}
	for _, p := range bad {
		if err := v.Validate(p); err == nil {
			t.Errorf("Validate(%q) should reject outside-root", p)
		}
	}
}

func TestSSHKeyPathValidator_CustomRoot(t *testing.T) {
	tmp := t.TempDir()
	v := NewSSHKeyPathValidator(tmp)

	// Lay down a real file inside the root so the symlink-resolution
	// branch fires.
	keyPath := filepath.Join(tmp, "id_test")
	if err := os.WriteFile(keyPath, []byte("test"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := v.Validate(keyPath); err != nil {
		t.Fatalf("Validate(%q) under custom root: %v", keyPath, err)
	}

	// Outside-root rejected even when the path doesn't exist.
	outside := "/tmp/some-other/place/key"
	if !strings.HasPrefix(outside, tmp) {
		// Sanity: confirm /tmp != t.TempDir() (macOS /tmp → /private/tmp).
		if err := v.Validate(outside); err == nil {
			t.Errorf("expected outside-root rejection for %q under custom root %q", outside, tmp)
		}
	}
}

func TestSSHKeyPathValidator_SymlinkEscape(t *testing.T) {
	// Create a temp root and a symlink inside it pointing to /etc/passwd.
	// Validate should reject because the resolved target is outside the
	// allow-root.
	tmp := t.TempDir()
	target := "/etc/passwd"
	if _, err := os.Stat(target); os.IsNotExist(err) {
		t.Skip("/etc/passwd missing; skipping symlink-escape test")
	}
	link := filepath.Join(tmp, "escape")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	v := NewSSHKeyPathValidator(tmp)
	if err := v.Validate(link); err == nil {
		t.Errorf("Validate(%q) should reject symlink escape (target=%q, root=%q)", link, target, tmp)
	}
}

func TestSSHKeyPathValidator_NonexistentUnderRootAccepted(t *testing.T) {
	tmp := t.TempDir()
	v := NewSSHKeyPathValidator(tmp)
	missing := filepath.Join(tmp, "not-yet-mounted", "id_rsa")
	if err := v.Validate(missing); err != nil {
		t.Errorf("Validate(%q) should accept non-existent path under root (lazy mount): %v", missing, err)
	}
}
