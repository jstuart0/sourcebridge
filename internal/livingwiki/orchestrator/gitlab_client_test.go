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

func newTestGitLabClient(t *testing.T, srv *httptest.Server) *GitLabClient {
	t.Helper()
	c := NewGitLabClient(GitLabClientConfig{
		ProjectID:     "123",
		Token:         "test-token",
		BaseURL:       srv.URL,
		DefaultBranch: "main",
		HTTPTimeout:   5 * time.Second,
	})
	c.retryBaseDelay = time.Millisecond
	return c
}

// ─── TestGitLabClient_Open ────────────────────────────────────────────────────

func TestGitLabClient_Open_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// ensureBranch: branch exists.
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/repository/branches/"):
			writeJSON(w, http.StatusOK, map[string]string{"name": "feature"})

		// appendCommit (create).
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/repository/commits"):
			writeJSON(w, http.StatusCreated, map[string]string{"id": "abc123"})

		// createMR.
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/merge_requests"):
			writeJSON(w, http.StatusCreated, map[string]int{"iid": 7})

		default:
			t.Logf("unexpected: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestGitLabClient(t, srv)
	err := client.Open(context.Background(), "feature", "Test MR", "Body",
		map[string][]byte{"wiki/foo.md": []byte("# Foo")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if client.ID() != "7" {
		t.Errorf("ID() = %q, want %q", client.ID(), "7")
	}
}

// ─── TestGitLabClient_Merged ──────────────────────────────────────────────────

func TestGitLabClient_Merged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"state": "merged"})
	}))
	defer srv.Close()

	client := newTestGitLabClient(t, srv)
	client.mrIID = 3
	client.branch = "some-branch"

	merged, err := client.Merged(context.Background())
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}
	if !merged {
		t.Error("expected merged=true")
	}
}

func TestGitLabClient_Closed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"state": "closed"})
	}))
	defer srv.Close()

	client := newTestGitLabClient(t, srv)
	client.mrIID = 3
	client.branch = "branch"

	closed, err := client.Closed(context.Background())
	if err != nil {
		t.Fatalf("Closed: %v", err)
	}
	if !closed {
		t.Error("expected closed=true")
	}
}

// ─── TestGitLabClient_404 ─────────────────────────────────────────────────────

func TestGitLabClient_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "404 Not Found"})
	}))
	defer srv.Close()

	client := newTestGitLabClient(t, srv)
	client.mrIID = 99

	_, err := client.Merged(context.Background())
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if !IsGitLabNotFound(err) {
		t.Errorf("IsGitLabNotFound = false; err=%v", err)
	}
}

// ─── TestGitLabClient_401 ─────────────────────────────────────────────────────

func TestGitLabClient_401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Unauthorized"})
	}))
	defer srv.Close()

	client := newTestGitLabClient(t, srv)
	client.mrIID = 1

	_, err := client.Merged(context.Background())
	if err == nil {
		t.Fatal("expected error for 401")
	}
	var ge *GitLabAPIError
	if !errors.As(err, &ge) {
		t.Fatalf("expected *GitLabAPIError in chain, got %T: %v", err, err)
	}
	if ge.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want 401", ge.StatusCode)
	}
}

// ─── TestGitLabClient_429_Retry ───────────────────────────────────────────────

func TestGitLabClient_429_Retry(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"message": "rate limit"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"state": "opened"})
	}))
	defer srv.Close()

	client := newTestGitLabClient(t, srv)
	client.mrIID = 1

	merged, err := client.Merged(context.Background())
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}
	if merged {
		t.Error("expected merged=false")
	}
	if attempts < 2 {
		t.Errorf("attempts = %d, want >= 2", attempts)
	}
}

// ─── TestGitLabClient_500_Retry ───────────────────────────────────────────────

func TestGitLabClient_500_Retry(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "server error"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"state": "opened"})
	}))
	defer srv.Close()

	client := newTestGitLabClient(t, srv)
	client.mrIID = 1

	_, err := client.Merged(context.Background())
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}
}

// ─── TestGitLabClient_MalformedJSON ───────────────────────────────────────────

func TestGitLabClient_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{{not json"))
	}))
	defer srv.Close()

	client := newTestGitLabClient(t, srv)
	client.mrIID = 1

	_, err := client.Merged(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

// ─── TestGitLabClient_ListCommitsOnBranch ─────────────────────────────────────

func TestGitLabClient_ListCommitsOnBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.String(), "ref_name=main") {
			t.Errorf("expected ref_name=main in query; got %s", r.URL.RawQuery)
		}
		writeJSON(w, http.StatusOK, []map[string]string{
			{"id": "newest", "committer_name": "Alice", "committer_email": "alice@example.com"},
			{"id": "older", "committer_name": "Bob", "committer_email": "bob@example.com"},
		})
	}))
	defer srv.Close()

	client := newTestGitLabClient(t, srv)
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
}

// ─── TestGitLabClient_PostComment ─────────────────────────────────────────────

func TestGitLabClient_PostComment(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		_ = json.NewDecoder(r.Body).Decode(&payload)
		gotBody = payload["body"]
		writeJSON(w, http.StatusCreated, map[string]string{"id": "1"})
	}))
	defer srv.Close()

	client := newTestGitLabClient(t, srv)
	client.mrIID = 5

	if err := client.PostComment(context.Background(), "Hello"); err != nil {
		t.Fatalf("PostComment: %v", err)
	}
	if gotBody != "Hello" {
		t.Errorf("body = %q, want %q", gotBody, "Hello")
	}
}

// ─── TestGitLabClient_CommitFallback ──────────────────────────────────────────

func TestGitLabClient_CommitFallback_CreateToUpdate(t *testing.T) {
	// First commit call returns 400 (file exists), second with "update" action succeeds.
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/repository/branches/"):
			writeJSON(w, http.StatusOK, map[string]string{"name": "main"})

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/repository/commits"):
			attempts++
			if attempts == 1 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"message": "A file with this name already exists"})
				return
			}
			// Verify the retry used "update" action.
			var payload map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if actions, ok := payload["actions"].([]interface{}); ok {
				for _, a := range actions {
					action := a.(map[string]interface{})
					if action["action"] != "update" {
						t.Errorf("retry action = %q, want %q", action["action"], "update")
					}
				}
			}
			writeJSON(w, http.StatusCreated, map[string]string{"id": "new"})

		default:
			t.Logf("unexpected: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestGitLabClient(t, srv)
	err := client.AppendCommitToBranch(context.Background(), "main",
		map[string][]byte{"wiki/test.md": []byte("content")}, "test commit")
	if err != nil {
		t.Fatalf("AppendCommitToBranch: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
}
