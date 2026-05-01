// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package testfixtures

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLargeGoRepo_DefaultsAreSensible pins the helper's default shape so
// downstream tests can rely on "no spec" producing a >=500-file Go repo
// on the named branch.
func TestLargeGoRepo_DefaultsAreSensible(t *testing.T) {
	repo := LargeGoRepo(t, LargeGoRepoSpec{})

	// Repo path exists.
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatalf("stat repo: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("repo path is not a directory: %s", repo)
	}

	// .git directory exists (i.e., the helper actually initialized it).
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		t.Fatalf("expected .git directory: %v", err)
	}

	// File count matches default 500.
	count := 0
	if err := filepath.Walk(repo, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			if fi.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") {
			count++
		}
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if count != 500 {
		t.Fatalf("file count = %d, want 500", count)
	}

	// Branch is "main".
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "main" {
		t.Fatalf("branch = %q, want %q", got, "main")
	}
}

// TestLargeGoRepo_CustomSpec exercises the override paths so a smaller
// fixture can be requested for tests that don't need 500 files.
func TestLargeGoRepo_CustomSpec(t *testing.T) {
	repo := LargeGoRepo(t, LargeGoRepoSpec{
		FileCount:      30,
		PackageBuckets: 3,
		Branch:         "feature/x",
	})

	count := 0
	pkgs := map[string]bool{}
	_ = filepath.Walk(repo, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			if fi.Name() == ".git" {
				return filepath.SkipDir
			}
			rel, _ := filepath.Rel(repo, p)
			if rel != "." && !strings.HasPrefix(rel, ".git") {
				pkgs[rel] = true
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") {
			count++
		}
		return nil
	})
	if count != 30 {
		t.Fatalf("file count = %d, want 30", count)
	}
	if len(pkgs) != 3 {
		t.Fatalf("package buckets = %d, want 3", len(pkgs))
	}

	out, err := exec.Command("git", "-C", repo, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "feature/x" {
		t.Fatalf("branch = %q, want %q", got, "feature/x")
	}
}

// TestLargeGoRepo_WriteAndCommit covers the WriteFile / Commit / Branch
// helpers — the test-side surface IndexFiles tests use to simulate
// "agent edited a file."
func TestLargeGoRepo_WriteAndCommit(t *testing.T) {
	repo := LargeGoRepo(t, LargeGoRepoSpec{FileCount: 10, PackageBuckets: 2})

	// Edit an existing file.
	WriteFile(t, repo, "pkg0/file1.go", "package pkg0\n\n// edited\n")
	out, err := os.ReadFile(filepath.Join(repo, "pkg0", "file1.go"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(out), "edited") {
		t.Fatalf("expected edited marker, got: %s", string(out))
	}

	// Commit.
	sha := Commit(t, repo, "edit pkg0/file1.go")
	if len(sha) != 40 {
		t.Fatalf("commit SHA len = %d, want 40 (full SHA1)", len(sha))
	}

	// Switch to a feature branch.
	Branch(t, repo, "feature/y")
	branchOut, err := exec.Command("git", "-C", repo, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if got := strings.TrimSpace(string(branchOut)); got != "feature/y" {
		t.Fatalf("branch = %q, want feature/y", got)
	}
}
