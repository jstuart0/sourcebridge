// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package indexing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/config"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
)

func TestIsGitURL(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"https://github.com/foo/bar", true},
		{"http://gitlab.com/foo/bar", true},
		{"git@github.com:foo/bar.git", true},
		{"ssh://git@example.com/foo.git", true},
		{"git://example.com/foo.git", true},
		{"/abs/local/path", false},
		{"./relative/path", false},
		{"my-repo", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsGitURL(c.in); got != c.want {
			t.Errorf("IsGitURL(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestNormalizeGitURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://github.com/foo/bar.git", "https://github.com/foo/bar"},
		{"https://user:pass@github.com/foo/bar.git", "https://github.com/foo/bar"},
		{"https://ghp_xxxx@github.com/foo/bar", "https://github.com/foo/bar"},
		{"git@github.com:foo/bar.git", "git@github.com:foo/bar"},
		{"https://gitlab.com/group/sub/repo", "https://gitlab.com/group/sub/repo"},
	}
	for _, c := range cases {
		if got := NormalizeGitURL(c.in); got != c.want {
			t.Errorf("NormalizeGitURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDeriveRepoName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://github.com/foo/bar.git", "bar"},
		{"/abs/path/my-repo", "my-repo"},
		{"./local/monorepo", "monorepo"},
		{"", "repo"},
	}
	for _, c := range cases {
		if got := deriveRepoName(c.in); got != c.want {
			t.Errorf("deriveRepoName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeRepoName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"my-repo", "my-repo"},
		{"foo/bar", "foo-bar"},
		{"with spaces", "with-spaces"},
		{"weird*chars!", "weirdchars"},
		{"", "repo"},
	}
	for _, c := range cases {
		if got := sanitizeRepoName(c.in); got != c.want {
			t.Errorf("sanitizeRepoName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRunImport_GitCredsIntegrityFailClosed is the codex r2b Low: prove
// that when the credentials function returns an integrity error AND no
// request-scoped token override is supplied, indexing fails closed
// (SetRepositoryError stamps the repo with the integrity error and
// nothing else runs — no clone, no env-fallback). This is the
// non-GraphQL-surface mirror of the same fail-closed contract the
// GraphQL clone path enforces.
func TestRunImport_GitCredsIntegrityFailClosed(t *testing.T) {
	store := graphstore.NewStore()
	cfg := &config.Config{}

	integrityErr := errors.New("git creds integrity failure: corrupt envelope")
	creds := func(ctx context.Context) (string, string, error) {
		// Mirrors what the runtime resolver returns when
		// Snapshot.IntegrityError is set: an error AND empty values.
		// The legacy bug was returning empty values + nil error so the
		// caller silently downgraded to env-only behavior.
		return "", "", integrityErr
	}

	svc := NewService(cfg, store, creds, nil)
	repo, err := store.CreateRepository("test-repo", "https://example.invalid/git/test-repo.git")
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	store.UpdateRepositoryMeta(repo.ID, graphstore.RepositoryMeta{RemoteURL: "https://example.invalid/git/test-repo"})

	// Call runImport synchronously (it would normally be invoked via
	// the Import goroutine; calling it directly avoids the goroutine
	// scheduling timing in the test).
	svc.runImport(repo.ID, "test-repo", "https://example.invalid/git/test-repo.git", true, nil)

	got := store.GetRepository(repo.ID)
	if got == nil {
		t.Fatalf("repository %s missing after runImport", repo.ID)
	}
	if got.Status != "error" {
		t.Errorf("status: want 'error', got %q (runImport should fail closed on creds integrity error)", got.Status)
	}
	if got.IndexError == "" {
		t.Errorf("index_error: want non-empty, got empty (the integrity error must be stamped on the repo)")
	}
	if !strings.Contains(got.IndexError, "integrity") {
		t.Errorf("index_error: want substring 'integrity', got %q", got.IndexError)
	}
	// And critically: the repo should NOT have been silently advanced
	// to a successful state via an env-token fallback. The only signal
	// here is that ClonePath remained empty (clone never ran).
	if got.ClonePath != "" {
		t.Errorf("clone_path: want empty (clone must not have run), got %q", got.ClonePath)
	}
}

// TestRunImport_RequestTokenBypassesIntegrityError is the companion to
// TestRunImport_GitCredsIntegrityFailClosed: when a request-scoped
// token IS provided, the integrity error from the workspace creds is
// not fatal — the request-token wins and the workspace-level integrity
// failure becomes irrelevant. This mirrors the GraphQL Repo-AuthToken
// shadowing logic and is part of the documented contract in
// runImport's comment.
//
// We cannot run a real clone in the unit test, so we verify the
// no-fail-closed-stamp behavior by checking the error message is NOT
// the integrity-failure prefix (clone failure against an invalid URL
// is the expected outcome).
func TestRunImport_RequestTokenBypassesIntegrityError(t *testing.T) {
	store := graphstore.NewStore()
	cfg := &config.Config{Storage: config.StorageConfig{RepoCachePath: t.TempDir()}}

	integrityErr := errors.New("git creds integrity failure: corrupt envelope")
	creds := func(ctx context.Context) (string, string, error) {
		return "", "", integrityErr
	}

	svc := NewService(cfg, store, creds, nil)
	repo, err := store.CreateRepository("test-repo", "https://example.invalid/git/test-repo.git")
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	store.UpdateRepositoryMeta(repo.ID, graphstore.RepositoryMeta{RemoteURL: "https://example.invalid/git/test-repo"})

	requestToken := "request-scoped-pat"
	svc.runImport(repo.ID, "test-repo", "https://example.invalid/git/test-repo.git", true, &requestToken)

	got := store.GetRepository(repo.ID)
	if got == nil {
		t.Fatalf("repository %s missing after runImport", repo.ID)
	}
	// The request token bypasses the integrity error, so we expect
	// the run to proceed past creds resolution. The clone itself will
	// fail (invalid host), and that's the EXPECTED error here — NOT
	// the integrity-failure stamp.
	if strings.Contains(got.IndexError, "integrity") {
		t.Errorf("index_error: request token MUST shadow workspace creds integrity error, got %q", got.IndexError)
	}
}
