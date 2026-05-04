// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package pathutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Frozen baselines — these are verbatim copies of the pre-existing
// implementations captured before the refactor so the parity tests remain
// stable even after the source files are rewritten to delegate.
// ---------------------------------------------------------------------------

// baseline_graphql_sanitizeRepoName is a verbatim copy of
// internal/api/graphql/helpers.go:sanitizeRepoName before the refactor.
func baseline_graphql_sanitizeRepoName(name string) string { //nolint:revive
	r := strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-")
	return r.Replace(name)
}

// baseline_indexing_sanitizeRepoName is a verbatim copy of
// internal/indexing/service.go:sanitizeRepoName before the refactor.
func baseline_indexing_sanitizeRepoName(name string) string { //nolint:revive
	if name == "" {
		return "repo"
	}
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			out = append(out, r)
		case r == ' ', r == '/', r == '\\':
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		return "repo"
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// Behavior-parity tests
// ---------------------------------------------------------------------------

// TestSanitizeRepoName_BehaviorParity verifies that the consolidated
// SanitizeRepoName function produces exactly the same output as each
// pre-existing implementation for every printable ASCII codepoint and a
// sampled set of non-ASCII inputs.
func TestSanitizeRepoName_BehaviorParity(t *testing.T) {
	// Printable ASCII: 0x20 (space) through 0x7E (~)
	var testInputs []string
	for cp := rune(0x20); cp <= 0x7E; cp++ {
		testInputs = append(testInputs, string(cp))
	}

	// Non-ASCII samples: emoji, non-Latin, NUL, control chars
	extra := []string{
		"",                      // empty
		"\x00",                  // NUL
		"\x01",                  // SOH control char
		"\x1f",                  // US control char
		"こんにちは",               // Hiragana/Katakana
		"привет",                // Cyrillic
		"🚀",                   // emoji (multi-byte)
		"user/repo",             // compound
		"my:project",            // colon
		"my repo name",          // space
		"back\\slash",           // backslash
		"a/b/c",                 // nested path
		"foo:bar/baz qux\\quux", // mixed separators
	}
	testInputs = append(testInputs, extra...)

	for _, input := range testInputs {
		// GraphQLLegacyPolicy must match the graphql baseline.
		got := SanitizeRepoName(input, GraphQLLegacyPolicy)
		want := baseline_graphql_sanitizeRepoName(input)
		if got != want {
			t.Errorf("GraphQLLegacyPolicy(%q): got %q, want %q", input, got, want)
		}

		// StrictPolicy must match the indexing baseline.
		got = SanitizeRepoName(input, StrictPolicy)
		want = baseline_indexing_sanitizeRepoName(input)
		if got != want {
			t.Errorf("StrictPolicy(%q): got %q, want %q", input, got, want)
		}
	}
}

// TestSafeJoinRepoPath_Basic covers happy-path joins.
func TestSafeJoinRepoPath_Basic(t *testing.T) {
	dir := t.TempDir()

	got, err := SafeJoinRepoPath(dir, "foo/bar.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(dir, "foo", "bar.go")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestSafeJoinRepoPath_StripsDotSlash verifies that a leading "./" is stripped.
func TestSafeJoinRepoPath_StripsDotSlash(t *testing.T) {
	dir := t.TempDir()

	got, err := SafeJoinRepoPath(dir, "./main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(dir, "main.go")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestSafeJoinRepoPath_RejectsAbsolute verifies that absolute relPaths are rejected.
func TestSafeJoinRepoPath_RejectsAbsolute(t *testing.T) {
	dir := t.TempDir()
	_, err := SafeJoinRepoPath(dir, "/etc/passwd")
	if err == nil {
		t.Error("expected error for absolute path, got nil")
	}
}

// TestSafeJoinRepoPath_RejectsTraversal verifies that path traversal is rejected.
func TestSafeJoinRepoPath_RejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	_, err := SafeJoinRepoPath(dir, "../../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal, got nil")
	}
}

// TestSafeJoinRepoPath_AllowsRootItself verifies that joining "" or "." returns the root.
func TestSafeJoinRepoPath_AllowsRootItself(t *testing.T) {
	dir := t.TempDir()
	got, err := SafeJoinRepoPath(dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	abs, _ := filepath.Abs(dir)
	if got != abs {
		t.Errorf("got %q, want %q", got, abs)
	}
}

// TestIsGitURL covers all recognized URL patterns.
func TestIsGitURL(t *testing.T) {
	trueInputs := []string{
		"http://github.com/user/repo",
		"https://github.com/user/repo",
		"git://github.com/user/repo",
		"git@github.com:user/repo",
		"ssh://git@github.com/user/repo",
		"github.com/user/repo.git",
	}
	falseInputs := []string{
		"/home/user/repo",
		"./local/repo",
		"C:\\repos\\myrepo",
		"not-a-url",
		"",
	}

	for _, s := range trueInputs {
		if !IsGitURL(s) {
			t.Errorf("IsGitURL(%q): want true, got false", s)
		}
	}
	for _, s := range falseInputs {
		if IsGitURL(s) {
			t.Errorf("IsGitURL(%q): want false, got true", s)
		}
	}
}

// TestSafeJoinRepoPath_WrittenFile verifies that the returned path can actually
// be used to create a file within the repo root.
func TestSafeJoinRepoPath_WrittenFile(t *testing.T) {
	dir := t.TempDir()
	joined, err := SafeJoinRepoPath(dir, "sub/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(joined), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(joined, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
