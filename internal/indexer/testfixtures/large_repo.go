// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package testfixtures provides on-disk synthetic git repositories for
// indexer / change-watch tests. The package is a regular library
// (not a _test.go) so it can be imported from any test in any package
// without the cross-package _test.go visibility rules getting in the
// way.
//
// Two fixture shapes are exposed:
//
//   - LargeGoRepo creates a synthetic >=500-file Go repository. Used by
//     Phase 1.B's IndexFiles 100ms budget test (Phase 1 done-definition
//     test #6) and by the same Phase's end-to-end ReindexRepository
//     mutation test (Phase 1 done-definition test #11, end-to-end half).
//     The fixture lives in t.TempDir() — no checked-in fixture bloat —
//     and is regenerable in a few hundred milliseconds.
//
//   - WriteFile / Commit / Branch are small helpers that let a test
//     mutate the fixture (simulate an out-of-band edit, switch to a
//     feature branch, etc.).
//
// Why synthetic Go and not multi-lang-repo: the budget test is about
// aggregate-recompute scaling (the dominant cost is the call-graph
// resolution over the merged file set), not language coverage.
// Synthetic Go-only files exercise that scaling cheaply at the
// representative 500-file count without bloating the language-coverage
// fixture, which has a different purpose.
package testfixtures

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// LargeGoRepoSpec controls the LargeGoRepo generator.
type LargeGoRepoSpec struct {
	// FileCount is the number of .go source files to generate. Defaults
	// to 500 when zero. Phase 1.B's budget test uses 500 (the minimum
	// the plan specifies); the constant is exposed so future scaling
	// tests can dial it up without forking the helper.
	FileCount int

	// PackageBuckets is the number of package directories (pkg0,
	// pkg1, ...) the files are spread across. Defaults to 10 when
	// zero. More buckets exercise the call-graph resolver's
	// same-package matching path more thoroughly; fewer buckets
	// concentrate symbols, which exercises the global-match path.
	// 10 is a balanced default that produces ~50 files per package.
	PackageBuckets int

	// Branch names the initial branch. Defaults to "main" when empty.
	Branch string
}

// LargeGoRepo writes a synthetic Go repository to a fresh t.TempDir,
// initializes it as a git repo with one commit on the named branch, and
// returns the absolute repository path. The repository contains
// spec.FileCount .go files spread across spec.PackageBuckets directories.
//
// Each generated file declares: a struct, a constructor, two methods
// (one calls the other), and a free function. This shape exercises the
// indexer's symbol-extraction, call-graph resolution, and
// test-linkage paths — the same shape Phase 1.B's IndexFiles
// implementation must keep correct after a per-file delta.
//
// On any failure, the helper t.Fatals — there is no recoverable error
// path: a broken fixture is a broken test.
func LargeGoRepo(t *testing.T, spec LargeGoRepoSpec) string {
	t.Helper()

	if spec.FileCount == 0 {
		spec.FileCount = 500
	}
	if spec.PackageBuckets == 0 {
		spec.PackageBuckets = 10
	}
	if spec.Branch == "" {
		spec.Branch = "main"
	}

	dir := t.TempDir()

	// Initialize the git repo first so the directory exists in a known
	// state before we start writing files.
	runGit(t, dir, "init", "-q", "-b", spec.Branch)

	// Materialize the package directories and source files.
	filesPerBucket := spec.FileCount / spec.PackageBuckets
	if spec.FileCount%spec.PackageBuckets != 0 {
		filesPerBucket++
	}
	written := 0
	for bucket := 0; bucket < spec.PackageBuckets && written < spec.FileCount; bucket++ {
		pkgName := fmt.Sprintf("pkg%d", bucket)
		pkgDir := filepath.Join(dir, pkgName)
		if err := os.MkdirAll(pkgDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", pkgDir, err)
		}
		for i := 0; i < filesPerBucket && written < spec.FileCount; i++ {
			fileIdx := written + 1
			path := filepath.Join(pkgDir, fmt.Sprintf("file%d.go", fileIdx))
			if err := os.WriteFile(path, []byte(syntheticGoFile(pkgName, fileIdx)), 0o644); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
			written++
		}
	}

	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "synthetic large-repo fixture")
	return dir
}

// WriteFile writes content to relPath inside repoDir, replacing any
// existing file. It does not commit — the change is left in the working
// tree, which is the natural shape for "agent edited a file" tests
// where the IndexFiles call comes before any commit.
func WriteFile(t *testing.T, repoDir, relPath, content string) {
	t.Helper()
	full := filepath.Join(repoDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// Commit stages everything in repoDir and commits with the given
// message. Returns the new commit SHA.
func Commit(t *testing.T, repoDir, message string) string {
	t.Helper()
	runGit(t, repoDir, "add", "-A")
	runGit(t, repoDir, "commit", "-q", "-m", message)
	out, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	sha := string(out)
	for len(sha) > 0 && (sha[len(sha)-1] == '\n' || sha[len(sha)-1] == '\r') {
		sha = sha[:len(sha)-1]
	}
	return sha
}

// Branch switches repoDir to a new branch off the current HEAD.
func Branch(t *testing.T, repoDir, branch string) {
	t.Helper()
	runGit(t, repoDir, "checkout", "-q", "-b", branch)
}

// runGit executes a git command inside repoDir with hermetic identity
// envs so the generator works on machines whose global git config has
// hooks or a missing user.email.
func runGit(t *testing.T, repoDir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, string(out))
	}
}

// syntheticGoFile returns the body of a synthetic .go file. Each file
// is self-consistent (its types and methods are valid Go in isolation)
// so the indexer's parser does not produce noise that would mask
// real-world budget behavior.
func syntheticGoFile(pkgName string, idx int) string {
	return fmt.Sprintf(`package %s

import "fmt"

// Service%d is a synthetic service used by the indexer fixture.
//
// Phase 1.B uses this file shape to exercise IndexFiles' merge and
// aggregate-recompute paths against a representative >=500-file
// repository.
type Service%d struct {
	id int
}

// NewService%d constructs a Service%d.
func NewService%d() *Service%d {
	return &Service%d{id: %d}
}

// Process is the fixture's primary callable. It validates input and
// hands off to processInternal so the synthetic repo has at least one
// intra-file call edge per file (exercising the call-graph resolver's
// same-file path).
func (s *Service%d) Process(input string) error {
	if input == "" {
		return fmt.Errorf("empty input")
	}
	return s.processInternal(input)
}

func (s *Service%d) processInternal(input string) error {
	fmt.Println(input)
	return nil
}

// Helper%d is a free function so each file contributes a global-scope
// callable to the indexer's name index.
func Helper%d(x int) int {
	return x + %d
}
`, pkgName, idx, idx, idx, idx, idx, idx, idx, idx, idx, idx, idx, idx, idx)
}
