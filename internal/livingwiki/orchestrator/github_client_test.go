// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestGitHubClient constructs a GitHubClient pointed at the given test
// server. owner/repo can be arbitrary slugs for the test.
func newTestGitHubClient(t *testing.T, srv *httptest.Server) *GitHubClient {
	t.Helper()
	c := NewGitHubClient(GitHubClientConfig{
		Owner:         "acme",
		Repo:          "wiki",
		Token:         "test-token",
		BaseURL:       srv.URL,
		DefaultBranch: "main",
		HTTPTimeout:   5 * time.Second,
	})
	c.retryBaseDelay = time.Millisecond // near-instant retries in tests
	return c
}

// writeJSON encodes v as JSON and writes it to w with the given status.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ─── TestGitHubClient_Open ────────────────────────────────────────────────────

func TestGitHubClient_Open_Success(t *testing.T) {
	// Track which API paths were called.
	calls := map[string]int{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.Method+":"+r.URL.Path]++

		switch {
		// ensureBranch: ref lookup (branch exists)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/ref/heads/feature-branch"):
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"object": map[string]string{"sha": "abc123"},
			})

		// resolveCommitTree
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/commits/"):
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"sha":  "abc123",
				"tree": map[string]string{"sha": "tree111"},
			})

		// createBlob
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/blobs"):
			writeJSON(w, http.StatusCreated, map[string]string{"sha": "blob999"})

		// createTree
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/trees"):
			writeJSON(w, http.StatusCreated, map[string]string{"sha": "newtree"})

		// createCommit
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/commits"):
			writeJSON(w, http.StatusCreated, map[string]string{"sha": "newcommit"})

		// updateRef
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/git/refs/heads/"):
			writeJSON(w, http.StatusOK, map[string]string{})

		// createPR
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls"):
			writeJSON(w, http.StatusCreated, map[string]int{"number": 42})

		default:
			t.Logf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestGitHubClient(t, srv)
	ctx := context.Background()

	err := client.Open(ctx, "feature-branch", "Test PR", "PR body",
		map[string][]byte{"wiki/foo.md": []byte("# Foo")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if client.ID() != "42" {
		t.Errorf("ID() = %q, want %q", client.ID(), "42")
	}
	if client.Branch() != "feature-branch" {
		t.Errorf("Branch() = %q, want %q", client.Branch(), "feature-branch")
	}
}

// ─── TestGitHubClient_Merged ──────────────────────────────────────────────────

func TestGitHubClient_Merged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"state":  "closed",
			"merged": true,
		})
	}))
	defer srv.Close()

	client := newTestGitHubClient(t, srv)
	client.prNum = 7
	client.branch = "some-branch"

	merged, err := client.Merged(context.Background())
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}
	if !merged {
		t.Error("expected merged=true")
	}
}

// ─── TestGitHubClient_404 ─────────────────────────────────────────────────────

func TestGitHubClient_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	}))
	defer srv.Close()

	client := newTestGitHubClient(t, srv)
	client.prNum = 99

	_, err := client.Merged(context.Background())
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("expected IsNotFound(err)=true, got false; err=%v", err)
	}
}

// ─── TestGitHubClient_401 ─────────────────────────────────────────────────────

func TestGitHubClient_401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Requires authentication"})
	}))
	defer srv.Close()

	client := newTestGitHubClient(t, srv)
	client.prNum = 1

	_, err := client.Merged(context.Background())
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
	var ge *GitHubAPIError
	if !errors.As(err, &ge) {
		t.Fatalf("expected *GitHubAPIError in chain, got %T: %v", err, err)
	}
	if ge.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want 401", ge.StatusCode)
	}
}

// ─── TestGitHubClient_429_Retry ───────────────────────────────────────────────

func TestGitHubClient_429_Retry(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.Header().Set("X-RateLimit-Remaining", "1") // not zero — use status code path
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"message": "rate limit exceeded"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"state":  "open",
			"merged": false,
		})
	}))
	defer srv.Close()

	client := newTestGitHubClient(t, srv)
	client.prNum = 1

	merged, err := client.Merged(context.Background())
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}
	if merged {
		t.Error("expected merged=false")
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

// ─── TestGitHubClient_500_Retry ───────────────────────────────────────────────

func TestGitHubClient_500_Retry(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "server error"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"state":  "open",
			"merged": false,
		})
	}))
	defer srv.Close()

	client := newTestGitHubClient(t, srv)
	client.prNum = 1

	_, err := client.Merged(context.Background())
	if err != nil {
		t.Fatalf("Merged after retry: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
}

// ─── TestGitHubClient_MalformedJSON ───────────────────────────────────────────

func TestGitHubClient_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json at all {{{"))
	}))
	defer srv.Close()

	client := newTestGitHubClient(t, srv)
	client.prNum = 1

	_, err := client.Merged(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

// ─── TestGitHubClient_PostComment ─────────────────────────────────────────────

func TestGitHubClient_PostComment(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		_ = json.NewDecoder(r.Body).Decode(&payload)
		gotBody = payload["body"]
		writeJSON(w, http.StatusCreated, map[string]string{"id": "1"})
	}))
	defer srv.Close()

	client := newTestGitHubClient(t, srv)
	client.prNum = 5

	if err := client.PostComment(context.Background(), "Hello world"); err != nil {
		t.Fatalf("PostComment: %v", err)
	}
	if gotBody != "Hello world" {
		t.Errorf("comment body = %q, want %q", gotBody, "Hello world")
	}
}

// ─── TestGitHubClient_ListCommitsOnBranch ─────────────────────────────────────

func TestGitHubClient_ListCommitsOnBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.String(), "sha=main") {
			t.Errorf("expected sha=main in query; got %s", r.URL.RawQuery)
		}
		writeJSON(w, http.StatusOK, []map[string]interface{}{
			{
				"sha": "newest",
				"commit": map[string]interface{}{
					"committer": map[string]string{
						"name":  "Alice",
						"email": "alice@example.com",
					},
				},
			},
			{
				"sha": "older",
				"commit": map[string]interface{}{
					"committer": map[string]string{
						"name":  "Bob",
						"email": "bob@example.com",
					},
				},
			},
		})
	}))
	defer srv.Close()

	client := newTestGitHubClient(t, srv)
	commits, err := client.ListCommitsOnBranch(context.Background(), "main", time.Time{})
	if err != nil {
		t.Fatalf("ListCommitsOnBranch: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("len(commits) = %d, want 2", len(commits))
	}
	// Should be reversed to oldest-first.
	if commits[0].SHA != "older" {
		t.Errorf("commits[0].SHA = %q, want %q", commits[0].SHA, "older")
	}
	if commits[1].SHA != "newest" {
		t.Errorf("commits[1].SHA = %q, want %q", commits[1].SHA, "newest")
	}
}

// ─── TestGitHubClient_BranchCreation ─────────────────────────────────────────

func TestGitHubClient_BranchCreation(t *testing.T) {
	// Test that ensureBranch creates a branch when it does not exist.
	gotCreateBranch := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// Branch lookup returns 404.
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/ref/heads/new-branch"):
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})

		// Default branch lookup.
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/ref/heads/main"):
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"object": map[string]string{"sha": "mainsha"},
			})

		// Create branch ref.
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			gotCreateBranch = true
			writeJSON(w, http.StatusCreated, map[string]string{})

		// resolveCommitTree
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/commits/"):
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"sha":  "mainsha",
				"tree": map[string]string{"sha": "tree000"},
			})

		// createBlob
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/blobs"):
			writeJSON(w, http.StatusCreated, map[string]string{"sha": "blobX"})

		// createTree
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/trees"):
			writeJSON(w, http.StatusCreated, map[string]string{"sha": "newTreeX"})

		// createCommit
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/commits"):
			writeJSON(w, http.StatusCreated, map[string]string{"sha": "newCommitX"})

		// updateRef
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/git/refs/heads/"):
			writeJSON(w, http.StatusOK, map[string]string{})

		// createPR
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls"):
			writeJSON(w, http.StatusCreated, map[string]int{"number": 1})

		default:
			t.Logf("unexpected: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestGitHubClient(t, srv)
	err := client.Open(context.Background(), "new-branch", "Title", "Body",
		map[string][]byte{"wiki/test.md": []byte("content")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !gotCreateBranch {
		t.Error("expected branch creation call to have been made")
	}
}
