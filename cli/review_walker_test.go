// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestFindReviewableFiles_SkipsBuildArtifacts is the regression guard
// for tester-report Issue 6 (Pazaryna 2026-04-30) / CA-124.
//
// The pre-fix review walker maintained its own four-entry skip list
// (.git, node_modules, vendor, __pycache__) and missed every other
// build-artifact directory the indexer's canonical
// git.DefaultIgnorePatterns set already covered (.next, dist, build,
// target, out, .cache, …). The result: `sourcebridge review` on a
// Next.js project walked into .next/ and stalled on hundreds of
// generated chunks.
//
// This test stands up a fake project tree containing one .go file at
// the root plus copies of the same file under every ignored directory
// the canonical set covers, then asserts the walker returns exactly
// the root-level file. If a future change drops one of the directories
// from DefaultIgnorePatterns OR re-introduces a parallel skip list in
// review.go, this test fails.
func TestFindReviewableFiles_SkipsBuildArtifacts(t *testing.T) {
	root := t.TempDir()

	// Source file at the root — must be returned.
	rootFile := filepath.Join(root, "main.go")
	mustWriteFile(t, rootFile, "package main\n")

	// Source files under each ignored directory — must NOT be returned.
	ignoredDirs := []string{
		".git",
		"node_modules",
		"vendor",
		"__pycache__",
		".next",
		"dist",
		"build",
		"target",
		"out",
		".cache",
		"bin",
		"obj",
		".idea",
		".vscode",
		"coverage",
		".mypy_cache",
		".ruff_cache",
		".pytest_cache",
		".tox",
		"gen",
		".venv",
		"venv",
	}
	for _, d := range ignoredDirs {
		nested := filepath.Join(root, d, "should_be_skipped.go")
		mustWriteFile(t, nested, "package x\n")
	}

	// A nested ignored directory inside a non-ignored package — exercises
	// the per-component check (not just root-level).
	mustWriteFile(t, filepath.Join(root, "pkg", "ok.go"), "package pkg\n")
	mustWriteFile(t, filepath.Join(root, "pkg", ".next", "skipped.go"), "package skip\n")
	mustWriteFile(t, filepath.Join(root, "pkg", "out", "skipped.go"), "package skip\n")
	mustWriteFile(t, filepath.Join(root, "pkg", "dist", "skipped.go"), "package skip\n")

	got, err := findReviewableFiles(root)
	if err != nil {
		t.Fatalf("findReviewableFiles: %v", err)
	}
	sort.Strings(got)

	want := []string{
		filepath.Join(root, "main.go"),
		filepath.Join(root, "pkg", "ok.go"),
	}
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("findReviewableFiles returned %d files, want %d.\n  got:  %v\n  want: %v",
			len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("file[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// Defense-in-depth: make sure no returned path contains any of the
	// ignored directory names as a path component. A failure here means
	// the walker started descending into something it shouldn't.
	for _, p := range got {
		for _, d := range ignoredDirs {
			if containsComponent(p, d) {
				t.Errorf("returned path %q contains ignored component %q", p, d)
			}
		}
	}
}

// TestFindReviewableFiles_RootIsIgnoredName confirms that passing a
// directory whose own name happens to match the ignore set (e.g.
// `sourcebridge review ./dist`) still works. Pre-fix this would have
// silently returned nothing because the skip list checked the root's
// base name; the new walker explicitly exempts the root.
func TestFindReviewableFiles_RootIsIgnoredName(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "dist") // root's basename matches ignore set
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mustWriteFile(t, filepath.Join(root, "main.go"), "package main\n")

	got, err := findReviewableFiles(root)
	if err != nil {
		t.Fatalf("findReviewableFiles: %v", err)
	}
	if len(got) != 1 || got[0] != filepath.Join(root, "main.go") {
		t.Fatalf("walker should return the file inside an ignored-named root; got %v", got)
	}
}

// TestFindReviewableFiles_ExtensionFilter pins which extensions are
// considered source code. Adding a new extension must be a deliberate
// edit to reviewableExtensions, not an accident.
func TestFindReviewableFiles_ExtensionFilter(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name string
		want bool
	}{
		{"main.go", true},
		{"app.py", true},
		{"index.ts", true},
		{"index.js", true},
		{"App.java", true},
		{"main.rs", true},
		{"Program.cs", true},
		{"main.cpp", true},
		{"app.rb", true},

		{"README.md", false},
		{"config.toml", false},
		{"data.json", false},
		{"image.png", false},
		{"Makefile", false},
		{"index.tsx", false}, // tsx not yet in the set — pin so a quiet add is intentional
	}
	for _, c := range cases {
		mustWriteFile(t, filepath.Join(root, c.name), "x")
	}

	got, err := findReviewableFiles(root)
	if err != nil {
		t.Fatalf("findReviewableFiles: %v", err)
	}
	gotSet := map[string]bool{}
	for _, p := range got {
		gotSet[filepath.Base(p)] = true
	}
	for _, c := range cases {
		if gotSet[c.name] != c.want {
			t.Errorf("%s: included=%v, want %v", c.name, gotSet[c.name], c.want)
		}
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func containsComponent(path, comp string) bool {
	for _, p := range strings.Split(filepath.ToSlash(path), "/") {
		if p == comp {
			return true
		}
	}
	return false
}
