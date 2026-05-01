// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package changewatch

import (
	"errors"
	"testing"
)

// TestNormalizePath_HappyPath asserts well-formed repo-relative paths
// pass through unchanged. The contract is "preserve, don't correct" —
// the caller's casing and content are returned verbatim.
func TestNormalizePath_HappyPath(t *testing.T) {
	cases := []string{
		"a.go",
		"src/main.go",
		"internal/api/rest/mcp.go",
		"web/components/Foo.tsx",
		"docs/admin/connectors.md",
		// Case preservation — load-bearing for the macOS/Windows
		// case-insensitive-fs vs git case-sensitive-tree split.
		"src/Foo.go",
		"src/foo.go",
		"WITH_UPPERCASE.go",
	}
	for _, p := range cases {
		got, err := NormalizePath("/repo", p)
		if err != nil {
			t.Errorf("NormalizePath(%q) returned err=%v, want nil", p, err)
			continue
		}
		if got != p {
			t.Errorf("NormalizePath(%q) = %q, want %q (must preserve caller casing/content verbatim)", p, got, p)
		}
	}
}

// TestNormalizePath_RejectsContractViolations exercises the full set of
// contract violations the helper is responsible for catching. Each must
// return a wrapped ErrInvalidPath so callers that compare via errors.Is
// stay correct.
func TestNormalizePath_RejectsContractViolations(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"absolute", "/etc/passwd"},
		{"absolute_repo_relative_looking", "/internal/foo.go"},
		{"leading_dot_slash", "./src/main.go"},
		{"backslash_separator", `src\main.go`},
		{"trailing_slash", "src/"},
		{"double_slash", "src//main.go"},
		{"current_dir_segment", "src/./main.go"},
		{"parent_dir_segment_leading", "../escape.go"},
		{"parent_dir_segment_embedded", "src/../etc/passwd"},
		{"parent_dir_segment_only", ".."},
		{"parent_dir_trailing", "src/.."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NormalizePath("/repo", c.path)
			if err == nil {
				t.Errorf("NormalizePath(%q) returned err=nil, want a wrapped ErrInvalidPath", c.path)
				return
			}
			if !errors.Is(err, ErrInvalidPath) {
				t.Errorf("NormalizePath(%q) err=%v, want errors.Is(ErrInvalidPath)", c.path, err)
			}
		})
	}
}

// TestNormalizePaths_FailsFastOnFirstBadEntry — a single malformed
// path in a batch fails the whole batch. Callers get one error to
// reject on; they don't have to walk a partial result.
func TestNormalizePaths_FailsFastOnFirstBadEntry(t *testing.T) {
	out, err := NormalizePaths("/repo", []string{"a.go", "../bad.go", "c.go"})
	if err == nil {
		t.Errorf("NormalizePaths returned err=nil, want a wrapped ErrInvalidPath")
	}
	if out != nil {
		t.Errorf("NormalizePaths returned out=%v, want nil on error", out)
	}
}

// TestNormalizePaths_HappyPath asserts a clean batch returns a fresh
// copy with caller content preserved.
func TestNormalizePaths_HappyPath(t *testing.T) {
	in := []string{"a.go", "src/Foo.go", "src/foo.go"}
	out, err := NormalizePaths("/repo", in)
	if err != nil {
		t.Fatalf("NormalizePaths returned err=%v, want nil", err)
	}
	if len(out) != len(in) {
		t.Fatalf("NormalizePaths returned %d entries, want %d", len(out), len(in))
	}
	for i, p := range in {
		if out[i] != p {
			t.Errorf("out[%d] = %q, want %q (must preserve caller content)", i, out[i], p)
		}
	}
	// Caller-mutation sanity: confirm the returned slice is a copy.
	out[0] = "MUTATED"
	if in[0] == "MUTATED" {
		t.Errorf("NormalizePaths returned a slice that aliases the caller's slice — must be a fresh copy")
	}
}

// TestNormalizePaths_NilOrEmpty — nil and empty inputs return nil
// without error.
func TestNormalizePaths_NilOrEmpty(t *testing.T) {
	if out, err := NormalizePaths("/repo", nil); err != nil || out != nil {
		t.Errorf("NormalizePaths(nil) = (%v, %v), want (nil, nil)", out, err)
	}
	if out, err := NormalizePaths("/repo", []string{}); err != nil || out != nil {
		t.Errorf("NormalizePaths([]) = (%v, %v), want (nil, nil)", out, err)
	}
}
