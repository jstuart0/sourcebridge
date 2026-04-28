// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reposHandler returns a handler that responds to /api/v1/repositories based on
// the bearer token. bearerOK is the token that gets a 200; all others get 401.
// Pass "" for bearerOK to always return 200 (no-auth server).
func reposHandler(t *testing.T, bearerOK string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/repositories":
			if bearerOK != "" {
				got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				if strings.TrimSpace(got) != bearerOK {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
					return
				}
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}
}

// resetTokenFlags resets all slice-2 token flag vars to their zero values.
func resetTokenFlags() {
	setupClaudeToken = ""
	setupClaudeNoSave = false
	setupClaudeForceToken = false
}

// TestResolveToken_ValidToken_PersistsOnSuccess verifies that a valid --token
// is written to disk (mode 0600) when the probe returns 200.
func TestResolveToken_ValidToken_PersistsOnSuccess(t *testing.T) {
	srv := httptest.NewServer(reposHandler(t, "ca_good"))
	defer srv.Close()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	// Remove env-var token so only flag path is active.
	t.Setenv("SOURCEBRIDGE_API_TOKEN", "")

	resetTokenFlags()
	setupClaudeToken = "ca_good"
	defer resetTokenFlags()

	_, err := resolveToken(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}

	tokenPath := filepath.Join(homeDir, ".sourcebridge", "token")
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("token file not written: %v", err)
	}
	if strings.TrimSpace(string(data)) != "ca_good" {
		t.Errorf("token file content = %q, want %q", string(data), "ca_good")
	}

	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %o, want 0600", info.Mode().Perm())
	}
}

// TestResolveToken_InvalidToken_Returns401Error verifies that a bad --token
// gets an actionable error mentioning "invalid or revoked", not "server down".
func TestResolveToken_InvalidToken_Returns401Error(t *testing.T) {
	srv := httptest.NewServer(reposHandler(t, "ca_good"))
	defer srv.Close()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("SOURCEBRIDGE_API_TOKEN", "")

	resetTokenFlags()
	setupClaudeToken = "ca_bad"
	defer resetTokenFlags()

	_, err := resolveToken(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error for invalid token, got nil")
	}
	if !strings.Contains(err.Error(), "invalid or revoked") {
		t.Errorf("error should mention 'invalid or revoked'; got: %v", err)
	}
	// Must NOT contain "server running" framing.
	if strings.Contains(err.Error(), "server running") {
		t.Errorf("error must not say 'server running' for a token error; got: %v", err)
	}

	// Token file must NOT have been written.
	tokenPath := filepath.Join(homeDir, ".sourcebridge", "token")
	if _, err := os.Stat(tokenPath); err == nil {
		t.Error("token file must not be written after a 401 probe response")
	}
}

// TestResolveToken_403_ReturnsPermissionError verifies the 403 branch.
func TestResolveToken_403_ReturnsPermissionError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
		case "/api/v1/repositories":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"forbidden"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("SOURCEBRIDGE_API_TOKEN", "")

	resetTokenFlags()
	setupClaudeToken = "ca_noperms"
	defer resetTokenFlags()

	_, err := resolveToken(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error for 403, got nil")
	}
	if !strings.Contains(err.Error(), "does not have permission") {
		t.Errorf("error should mention 'does not have permission'; got: %v", err)
	}
}

// TestResolveToken_TransportFailure verifies that a transport error (closed port)
// bubbles up from resolveToken without "is the server running?" wrapping — the
// caller decides on framing.
func TestResolveToken_TransportFailure(t *testing.T) {
	// Find a free port, bind it, immediately close the listener so the port is
	// definitely not listening when resolveToken tries to connect.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("SOURCEBRIDGE_API_TOKEN", "")

	resetTokenFlags()
	setupClaudeToken = "ca_any"
	defer resetTokenFlags()

	_, err = resolveToken(context.Background(), "http://"+addr)
	if err == nil {
		t.Fatal("expected transport error, got nil")
	}
	// Error should NOT be of type httpStatusError — it must be a raw transport error.
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		t.Errorf("transport failure should not be wrapped in httpStatusError; got: %v", err)
	}
}

// TestResolveToken_NoSave_SkipsPersistence verifies --no-save prevents file write.
func TestResolveToken_NoSave_SkipsPersistence(t *testing.T) {
	srv := httptest.NewServer(reposHandler(t, "ca_good"))
	defer srv.Close()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("SOURCEBRIDGE_API_TOKEN", "")

	resetTokenFlags()
	setupClaudeToken = "ca_good"
	setupClaudeNoSave = true
	defer resetTokenFlags()

	_, err := resolveToken(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}

	tokenPath := filepath.Join(homeDir, ".sourcebridge", "token")
	if _, err := os.Stat(tokenPath); err == nil {
		t.Error("token file must not be written when --no-save is set")
	}
}

// TestResolveToken_ForceToken_AllowsOverwrite verifies that --force-token lets
// a different token replace the existing one.
func TestResolveToken_ForceToken_AllowsOverwrite(t *testing.T) {
	srv := httptest.NewServer(reposHandler(t, "ca_new"))
	defer srv.Close()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("SOURCEBRIDGE_API_TOKEN", "")

	// Pre-write an existing token.
	sbDir := filepath.Join(homeDir, ".sourcebridge")
	if err := os.MkdirAll(sbDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sbDir, "token"), []byte("ca_old"), 0o600); err != nil {
		t.Fatalf("write existing token: %v", err)
	}

	resetTokenFlags()
	setupClaudeToken = "ca_new"
	setupClaudeForceToken = true
	defer resetTokenFlags()

	_, err := resolveToken(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("resolveToken with --force-token: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(sbDir, "token"))
	if strings.TrimSpace(string(data)) != "ca_new" {
		t.Errorf("token file = %q, want %q", string(data), "ca_new")
	}
}

// TestResolveToken_RefusesOverwriteWithoutForce verifies that a different token
// without --force-token gets a clear refusal error.
func TestResolveToken_RefusesOverwriteWithoutForce(t *testing.T) {
	srv := httptest.NewServer(reposHandler(t, "ca_new"))
	defer srv.Close()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("SOURCEBRIDGE_API_TOKEN", "")

	sbDir := filepath.Join(homeDir, ".sourcebridge")
	if err := os.MkdirAll(sbDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sbDir, "token"), []byte("ca_old"), 0o600); err != nil {
		t.Fatalf("write existing token: %v", err)
	}

	resetTokenFlags()
	setupClaudeToken = "ca_new"
	// --force-token NOT set.
	defer resetTokenFlags()

	_, err := resolveToken(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected refusal error, got nil")
	}
	if !strings.Contains(err.Error(), "force-token") {
		t.Errorf("error should mention --force-token; got: %v", err)
	}

	// Existing token must be untouched.
	data, _ := os.ReadFile(filepath.Join(sbDir, "token"))
	if strings.TrimSpace(string(data)) != "ca_old" {
		t.Errorf("existing token was clobbered; got %q", string(data))
	}
}

// TestResolveToken_IdempotentSameToken verifies that re-passing the already-saved
// token is a silent no-op (no error, no "Saved token..." message, no .tmp artifact).
func TestResolveToken_IdempotentSameToken(t *testing.T) {
	srv := httptest.NewServer(reposHandler(t, "ca_same"))
	defer srv.Close()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("SOURCEBRIDGE_API_TOKEN", "")

	sbDir := filepath.Join(homeDir, ".sourcebridge")
	if err := os.MkdirAll(sbDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sbDir, "token"), []byte("ca_same"), 0o600); err != nil {
		t.Fatalf("write existing token: %v", err)
	}

	resetTokenFlags()
	setupClaudeToken = "ca_same"
	defer resetTokenFlags()

	_, err := resolveToken(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}

	// No .tmp artifact must remain.
	entries, _ := os.ReadDir(sbDir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover .tmp artifact found: %s", e.Name())
		}
	}
}

// TestResolveToken_WhitespacePaddedToken verifies that a whitespace-padded
// --token value is trimmed before saving.
func TestResolveToken_WhitespacePaddedToken(t *testing.T) {
	// The server expects the trimmed value; padded value in the header would 401.
	srv := httptest.NewServer(reposHandler(t, "ca_trimme"))
	defer srv.Close()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("SOURCEBRIDGE_API_TOKEN", "")

	resetTokenFlags()
	// Note: resolveToken calls strings.TrimSpace on the flag before using it,
	// so the padded value should be trimmed for both the probe header and the save.
	setupClaudeToken = "  ca_trimme\n"
	defer resetTokenFlags()

	_, err := resolveToken(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(homeDir, ".sourcebridge", "token"))
	if strings.TrimSpace(string(data)) != "ca_trimme" {
		t.Errorf("saved token = %q, want %q", string(data), "ca_trimme")
	}
}

// TestResolveToken_AtomicWrite_NoTmpArtifactOnSuccess verifies that no .tmp
// file is left behind after a successful write.
func TestResolveToken_AtomicWrite_NoTmpArtifactOnSuccess(t *testing.T) {
	srv := httptest.NewServer(reposHandler(t, "ca_atomic"))
	defer srv.Close()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("SOURCEBRIDGE_API_TOKEN", "")

	resetTokenFlags()
	setupClaudeToken = "ca_atomic"
	defer resetTokenFlags()

	if _, err := resolveToken(context.Background(), srv.URL); err != nil {
		t.Fatalf("resolveToken: %v", err)
	}

	sbDir := filepath.Join(homeDir, ".sourcebridge")
	entries, _ := os.ReadDir(sbDir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover .tmp artifact: %s", e.Name())
		}
	}
}

// TestFetchClusters_401_EmitsActionableError verifies that fetchClusters returns
// the multi-line "To fix this:" error for a 401, not a generic clusters-API error.
func TestFetchClusters_401_EmitsActionableError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/clusters") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	_, err := fetchClusters(context.Background(), srv.URL, "repo-123")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Mint a token at") {
		t.Errorf("401 error should mention 'Mint a token at'; got: %v", err)
	}
	if !strings.Contains(err.Error(), "To fix this") {
		t.Errorf("401 error should contain 'To fix this'; got: %v", err)
	}
	// Must NOT be an httpStatusError — the 401 branch returns a plain error.
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		t.Error("fetchClusters 401 must not be wrapped as httpStatusError")
	}
}

