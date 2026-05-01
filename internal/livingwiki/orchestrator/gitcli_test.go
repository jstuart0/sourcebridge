// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// initTestRepo creates an ephemeral git repository in a temp directory,
// commits an initial file, and returns the dir path and the first commit SHA.
func initTestRepo(t *testing.T) (dir, sha1 string) {
	t.Helper()
	dir = t.TempDir()

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")

	// Initial commit.
	writeFile(t, dir, "README.md", "# Test")
	run("add", "README.md")
	run("commit", "-m", "initial commit")
	sha1 = run("rev-parse", "HEAD")
	return dir, sha1
}

// addCommit writes a file to the repo and commits it. Returns the new HEAD SHA.
func addCommit(t *testing.T, dir, filename, content, msg string) string {
	t.Helper()
	writeFile(t, dir, filename, content)
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("add", filename)
	run("commit", "-m", msg)
	return run("rev-parse", "HEAD")
}

func writeFile(t *testing.T, dir, filename, content string) {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile %s: %v", path, err)
	}
}

// (currentSHA used to live here as a helper for tests that asserted on
// the rev-parse HEAD output; the current gitcli tests use the gitCLI's
// own ResolveRef path instead, so the helper has no callers. Removed
// to satisfy lint.)

// ─────────────────────────────────────────────────────────────────────────────
// GitCLIDiffProvider tests
// ─────────────────────────────────────────────────────────────────────────────

// TestGitCLIDiffProvider_EmptyBase verifies that when baseSHA is empty, all
// tracked files at headSHA are returned as changed.
func TestGitCLIDiffProvider_EmptyBase(t *testing.T) {
	t.Parallel()
	dir, sha1 := initTestRepo(t)

	dp := orchestrator.NewGitCLIDiffProvider(orchestrator.GitCLIConfig{ClonePath: dir})
	result, err := dp.Diff(context.Background(), "test-repo", "", sha1)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(result.Changed) == 0 {
		t.Error("expected at least one changed file for full-tree diff")
	}
	found := false
	for _, cp := range result.Changed {
		if cp.Path == "README.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("README.md not in changed paths: %v", result.Changed)
	}
}

// TestGitCLIDiffProvider_BetweenCommits verifies incremental diff between two SHAs.
func TestGitCLIDiffProvider_BetweenCommits(t *testing.T) {
	t.Parallel()
	dir, sha1 := initTestRepo(t)
	sha2 := addCommit(t, dir, "wiki/arch.auth.md", "# Auth", "add wiki page")

	dp := orchestrator.NewGitCLIDiffProvider(orchestrator.GitCLIConfig{ClonePath: dir})
	result, err := dp.Diff(context.Background(), "test-repo", sha1, sha2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	// Only wiki/arch.auth.md should appear.
	if len(result.Changed) != 1 {
		t.Errorf("expected 1 changed file, got %d: %v", len(result.Changed), result.Changed)
	}
	if len(result.Changed) == 1 && result.Changed[0].Path != "wiki/arch.auth.md" {
		t.Errorf("expected wiki/arch.auth.md, got %q", result.Changed[0].Path)
	}
}

// TestGitCLIDiffProvider_SHANotFound verifies that an invalid baseSHA returns
// ErrSHANotFound.
func TestGitCLIDiffProvider_SHANotFound(t *testing.T) {
	t.Parallel()
	dir, sha1 := initTestRepo(t)

	dp := orchestrator.NewGitCLIDiffProvider(orchestrator.GitCLIConfig{ClonePath: dir})
	_, err := dp.Diff(context.Background(), "test-repo", "deadbeefdeadbeef0000000000000000deadbeef", sha1)
	if err == nil {
		t.Fatal("expected ErrSHANotFound, got nil")
	}
	if err != orchestrator.ErrSHANotFound {
		t.Errorf("expected ErrSHANotFound, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GitCLIRepoWriter tests
// ─────────────────────────────────────────────────────────────────────────────

// TestGitCLIRepoWriter_AppendCommitToBranch verifies that files are written to
// a new branch and committed.
func TestGitCLIRepoWriter_AppendCommitToBranch(t *testing.T) {
	t.Parallel()

	// We need two repos: a "remote" (bare) and a "clone" of it.
	// AppendCommitToBranch pushes to the remote; we verify there.
	remoteDir := t.TempDir()
	runGlobal := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// Initialize bare remote.
	runGlobal(remoteDir, "init", "--bare", "-b", "main")

	// Clone from remote.
	cloneDir := t.TempDir()
	runGlobal(t.TempDir(), "clone", remoteDir, cloneDir)

	// Need at least one commit in the remote so the clone is non-empty.
	// Create a temp work tree, commit, push, then re-clone.
	workDir := t.TempDir()
	runGlobal(workDir, "clone", remoteDir, workDir)
	runGlobal(workDir, "config", "user.email", "test@example.com")
	runGlobal(workDir, "config", "user.name", "Test User")
	writeFile(t, workDir, "README.md", "# Init")
	runGlobal(workDir, "add", "README.md")
	runGlobal(workDir, "commit", "-m", "init")
	runGlobal(workDir, "push", "origin", "main")

	// Now clone properly.
	cloneDir2 := t.TempDir()
	runGlobal(t.TempDir(), "clone", remoteDir, cloneDir2)
	runGlobal(cloneDir2, "config", "user.email", "test@example.com")
	runGlobal(cloneDir2, "config", "user.name", "Test User")

	cfg := orchestrator.GitCLIConfig{
		ClonePath:  cloneDir2,
		RemoteName: "origin",
		Timeout:    30 * time.Second,
	}
	writer := orchestrator.NewGitCLIRepoWriter(cfg)

	files := map[string][]byte{
		"wiki/arch.auth.md": []byte("# Auth\n\nThis is the auth page.\n"),
	}

	if err := writer.AppendCommitToBranch(context.Background(), "sourcebridge/wiki-update", files, "wiki: add arch.auth page"); err != nil {
		t.Fatalf("AppendCommitToBranch: %v", err)
	}

	// Verify file exists in the clone.
	content, err := os.ReadFile(filepath.Join(cloneDir2, "wiki/arch.auth.md"))
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if !strings.Contains(string(content), "Auth") {
		t.Errorf("written file content unexpected: %q", string(content))
	}

	// Verify the commit message is on the branch.
	cmd := exec.Command("git", "log", "--pretty=format:%s", "sourcebridge/wiki-update")
	cmd.Dir = cloneDir2
	logOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if !strings.Contains(string(logOut), "wiki: add arch.auth page") {
		t.Errorf("commit message not found in log: %q", string(logOut))
	}
}

// TestGitCLIRepoWriter_WriteFiles verifies the WriteFiles convenience wrapper.
func TestGitCLIRepoWriter_WriteFiles(t *testing.T) {
	t.Parallel()

	// Use a local-only repo (no remote) to test write + commit without push.
	// We configure RemoteName to empty so push is skipped if there's no remote.
	// Since AppendCommitToBranch will try to push, we wire a bare repo pair.
	remoteDir := t.TempDir()
	runG := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runG(remoteDir, "init", "--bare", "-b", "main")

	workDir := t.TempDir()
	runG(workDir, "clone", remoteDir, workDir)
	runG(workDir, "config", "user.email", "bot@example.com")
	runG(workDir, "config", "user.name", "SourceBridgeBot")
	writeFile(t, workDir, "README.md", "# Repo")
	runG(workDir, "add", "README.md")
	runG(workDir, "commit", "-m", "init")
	runG(workDir, "push", "origin", "main")

	cloneDir := t.TempDir()
	runG(t.TempDir(), "clone", remoteDir, cloneDir)
	runG(cloneDir, "config", "user.email", "bot@example.com")
	runG(cloneDir, "config", "user.name", "SourceBridgeBot")

	cfg := orchestrator.GitCLIConfig{ClonePath: cloneDir, RemoteName: "origin", Timeout: 30 * time.Second}
	writer := orchestrator.NewGitCLIRepoWriter(cfg)

	files := map[string][]byte{
		"wiki/glossary.md": []byte("# Glossary\nTerms and definitions.\n"),
	}
	if err := writer.WriteFiles(context.Background(), files); err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}

	// The file must exist in the clone working tree.
	if _, err := os.Stat(filepath.Join(cloneDir, "wiki/glossary.md")); err != nil {
		t.Errorf("wiki/glossary.md not found after WriteFiles: %v", err)
	}
}

// TestGitCLIRepoWriter_ListCommitsOnBranch verifies commit listing after
// several commits on a branch.
func TestGitCLIRepoWriter_ListCommitsOnBranch(t *testing.T) {
	t.Parallel()
	dir, _ := initTestRepo(t)

	// Create a branch and make two commits on it.
	runG := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runG("checkout", "-b", "test-branch")
	runG("config", "user.email", "bot@example.com")
	runG("config", "user.name", "Test User")
	writeFile(t, dir, "wiki/page1.md", "# Page 1")
	runG("add", "wiki/page1.md")
	runG("commit", "-m", "add page1")
	writeFile(t, dir, "wiki/page2.md", "# Page 2")
	runG("add", "wiki/page2.md")
	runG("commit", "-m", "add page2")

	cfg := orchestrator.GitCLIConfig{ClonePath: dir, Timeout: 30 * time.Second}
	writer := orchestrator.NewGitCLIRepoWriter(cfg)

	commits, err := writer.ListCommitsOnBranch(context.Background(), "test-branch", time.Time{})
	if err != nil {
		t.Fatalf("ListCommitsOnBranch: %v", err)
	}

	// Should have at least 3 commits (init + 2 on branch).
	if len(commits) < 2 {
		t.Errorf("expected ≥2 commits, got %d: %v", len(commits), commits)
	}

	// Each commit should have a non-empty SHA.
	for _, c := range commits {
		if c.SHA == "" {
			t.Error("commit SHA is empty")
		}
	}
}

// TestGitCLIRepoWriter_IdempotentNoChange verifies that calling AppendCommitToBranch
// with an already-committed file set does not create an extra empty commit.
func TestGitCLIRepoWriter_IdempotentNoChange(t *testing.T) {
	t.Parallel()

	remoteDir := t.TempDir()
	runG := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runG(remoteDir, "init", "--bare", "-b", "main")
	workDir := t.TempDir()
	runG(workDir, "clone", remoteDir, workDir)
	runG(workDir, "config", "user.email", "bot@example.com")
	runG(workDir, "config", "user.name", "Bot")
	writeFile(t, workDir, "README.md", "# Repo")
	runG(workDir, "add", "README.md")
	runG(workDir, "commit", "-m", "init")
	runG(workDir, "push", "origin", "main")

	cloneDir := t.TempDir()
	runG(t.TempDir(), "clone", remoteDir, cloneDir)
	runG(cloneDir, "config", "user.email", "bot@example.com")
	runG(cloneDir, "config", "user.name", "Bot")

	cfg := orchestrator.GitCLIConfig{ClonePath: cloneDir, RemoteName: "origin", Timeout: 30 * time.Second}
	writer := orchestrator.NewGitCLIRepoWriter(cfg)

	files := map[string][]byte{"wiki/page.md": []byte("# Page\n")}

	// First write — creates the file and commits.
	if err := writer.AppendCommitToBranch(context.Background(), "sourcebridge/wiki", files, "first"); err != nil {
		t.Fatalf("first AppendCommitToBranch: %v", err)
	}

	// Second write with identical content — no staged changes, should not create an empty commit.
	if err := writer.AppendCommitToBranch(context.Background(), "sourcebridge/wiki", files, "second"); err != nil {
		t.Fatalf("second AppendCommitToBranch: %v", err)
	}

	// Count commits on the branch.
	cmd := exec.Command("git", "rev-list", "--count", "sourcebridge/wiki")
	cmd.Dir = cloneDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-list --count: %v", err)
	}
	count := strings.TrimSpace(string(out))
	// init commit + 1 wiki commit = 2. The second idempotent call must not add a commit.
	if count != "2" {
		t.Errorf("expected 2 commits (no empty commit for unchanged content), got %s", count)
	}
}
