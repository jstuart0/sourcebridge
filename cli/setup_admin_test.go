// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// resetSetupAdminFlags clears the setup-admin command-level flag vars
// so a vector set in one test doesn't bleed into the next.
func resetSetupAdminFlags() {
	setupAdminServer = ""
	setupAdminNoSave = false
	setupAdminPasswordInput = PasswordInputFlags{}
	_ = os.Unsetenv("SOURCEBRIDGE_PASSWORD")
	_ = os.Unsetenv("SOURCEBRIDGE_URL")
}

// newSetupAdminTestServer stands up a fake server emulating
// POST /auth/setup. cfg controls the response shape.
type setupAdminTestServerCfg struct {
	statusCode int    // default 200
	token      string // returned on 200
	errorBody  string // returned on non-200 as {"error":"..."}
}

func newSetupAdminTestServer(t *testing.T, cfg setupAdminTestServerCfg) *httptest.Server {
	t.Helper()
	if cfg.statusCode == 0 {
		cfg.statusCode = http.StatusOK
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/auth/setup" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["password"] == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "password required"})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(cfg.statusCode)
		if cfg.statusCode == http.StatusOK {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      cfg.token,
				"expires_in": 3600,
			})
		} else if cfg.errorBody != "" {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": cfg.errorBody})
		}
	}))
}

func newSetupAdminTestCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return cmd
}

// runSetupAdminInTempHome mirrors runLoginInTempHome — temp HOME, run
// the command, return the persisted token / server URL contents.
func runSetupAdminInTempHome(t *testing.T, serverURL string,
	pwdReader passwordReader, configure func()) (tokenContent, serverContent string, err error) {
	t.Helper()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	resetSetupAdminFlags()
	setupAdminServer = serverURL
	if configure != nil {
		configure()
	}
	t.Cleanup(resetSetupAdminFlags)

	cmd := newSetupAdminTestCmd()
	err = runSetupAdminWith(cmd, pwdReader)

	tokenPath := filepath.Join(homeDir, ".sourcebridge", "token")
	if data, readErr := os.ReadFile(tokenPath); readErr == nil {
		tokenContent = strings.TrimSpace(string(data))
	}
	serverPath := filepath.Join(homeDir, ".sourcebridge", "server")
	if data, readErr := os.ReadFile(serverPath); readErr == nil {
		serverContent = strings.TrimSpace(string(data))
	}
	return tokenContent, serverContent, err
}

// ---- happy paths ------------------------------------------------------------

// TestSetupAdmin_HappyPath_Stdin verifies the canonical CI flow: pipe
// the password into --password-stdin, get back a saved token + server
// URL ready for the next command.
func TestSetupAdmin_HappyPath_Stdin(t *testing.T) {
	srv := newSetupAdminTestServer(t, setupAdminTestServerCfg{
		token: "ca_setup_token_xyz",
	})
	defer srv.Close()

	// fakePasswordReader is the interactive prompt; non-interactive
	// resolution should bypass it entirely. Make it explode if called
	// to prove that.
	pwd := &fakePasswordReader{err: errInteractiveBypassed}

	// Replace stdin with a strings.Reader so --password-stdin reads
	// from our fixture, not the test runner's terminal.
	stdinReader, stdinWriter, _ := os.Pipe()
	origStdin := os.Stdin
	os.Stdin = stdinReader
	t.Cleanup(func() { os.Stdin = origStdin })
	go func() {
		_, _ = stdinWriter.WriteString("super-secret-pw\n")
		_ = stdinWriter.Close()
	}()

	token, server, err := runSetupAdminInTempHome(t, srv.URL, pwd.read, func() {
		setupAdminPasswordInput.Stdin = true
	})
	if err != nil {
		t.Fatalf("runSetupAdmin: %v", err)
	}
	if token != "ca_setup_token_xyz" {
		t.Errorf("token = %q, want ca_setup_token_xyz", token)
	}
	if server != srv.URL {
		t.Errorf("server = %q, want %q", server, srv.URL)
	}
}

// TestSetupAdmin_HappyPath_File confirms --password-file works and the
// returned token is persisted.
func TestSetupAdmin_HappyPath_File(t *testing.T) {
	srv := newSetupAdminTestServer(t, setupAdminTestServerCfg{
		token: "ca_setup_token_file",
	})
	defer srv.Close()

	tmpDir := t.TempDir()
	pwFile := filepath.Join(tmpDir, "pw")
	if err := os.WriteFile(pwFile, []byte("file-pw-1234\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	pwd := &fakePasswordReader{err: errInteractiveBypassed}

	token, _, err := runSetupAdminInTempHome(t, srv.URL, pwd.read, func() {
		setupAdminPasswordInput.File = pwFile
	})
	if err != nil {
		t.Fatalf("runSetupAdmin: %v", err)
	}
	if token != "ca_setup_token_file" {
		t.Errorf("token = %q, want ca_setup_token_file", token)
	}
}

// TestSetupAdmin_HappyPath_Env confirms SOURCEBRIDGE_PASSWORD works.
func TestSetupAdmin_HappyPath_Env(t *testing.T) {
	srv := newSetupAdminTestServer(t, setupAdminTestServerCfg{
		token: "ca_setup_token_env",
	})
	defer srv.Close()

	pwd := &fakePasswordReader{err: errInteractiveBypassed}

	token, _, err := runSetupAdminInTempHome(t, srv.URL, pwd.read, func() {
		t.Setenv("SOURCEBRIDGE_PASSWORD", "env-pw-1234")
	})
	if err != nil {
		t.Fatalf("runSetupAdmin: %v", err)
	}
	if token != "ca_setup_token_env" {
		t.Errorf("token = %q, want ca_setup_token_env", token)
	}
}

// TestSetupAdmin_HappyPath_Interactive verifies the polished default
// (no flags → interactive prompt with confirmation re-type).
func TestSetupAdmin_HappyPath_Interactive(t *testing.T) {
	srv := newSetupAdminTestServer(t, setupAdminTestServerCfg{
		token: "ca_setup_token_interactive",
	})
	defer srv.Close()

	// The interactive reader gets called twice: initial + confirmation.
	// Both return the same value → match → success.
	pwd := &fakePasswordReader{password: "interactive-pw"}
	token, _, err := runSetupAdminInTempHome(t, srv.URL, pwd.read, nil)
	if err != nil {
		t.Fatalf("runSetupAdmin: %v", err)
	}
	if token != "ca_setup_token_interactive" {
		t.Errorf("token = %q, want ca_setup_token_interactive", token)
	}
}

// ---- error paths ------------------------------------------------------------

func TestSetupAdmin_NoServerURL(t *testing.T) {
	pwd := &fakePasswordReader{password: "pw1234567"}
	_, _, err := runSetupAdminInTempHome(t, "", pwd.read, nil)
	if err == nil {
		t.Fatal("expected error for missing server URL, got nil")
	}
	if !strings.Contains(err.Error(), "server URL") {
		t.Errorf("error should mention server URL; got: %v", err)
	}
}

// TestSetupAdmin_PasswordTooShort confirms client-side validation fires
// before the network round-trip — saves a CI iteration.
func TestSetupAdmin_PasswordTooShort(t *testing.T) {
	srv := newSetupAdminTestServer(t, setupAdminTestServerCfg{
		token: "should-not-reach",
	})
	defer srv.Close()

	pwd := &fakePasswordReader{password: "short"} // 5 chars, below 8 min
	_, _, err := runSetupAdminInTempHome(t, srv.URL, pwd.read, nil)
	if err == nil {
		t.Fatal("expected too-short error, got nil")
	}
	if !strings.Contains(err.Error(), "8 characters") {
		t.Errorf("error should mention min length; got: %v", err)
	}
}

func TestSetupAdmin_InteractiveMismatch(t *testing.T) {
	srv := newSetupAdminTestServer(t, setupAdminTestServerCfg{
		token: "should-not-reach",
	})
	defer srv.Close()

	// passwordReader that returns different values on first vs. second
	// call — emulates a typo on confirmation.
	pwd := &mismatchPasswordReader{first: "good-password-1", second: "good-password-2"}
	_, _, err := runSetupAdminInTempHome(t, srv.URL, pwd.read, nil)
	if err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "do not match") {
		t.Errorf("error should mention 'do not match'; got: %v", err)
	}
}

// TestSetupAdmin_AlreadyInitialized confirms the 409 path — the server
// is already set up, so we tell the user to log in instead.
func TestSetupAdmin_AlreadyInitialized(t *testing.T) {
	srv := newSetupAdminTestServer(t, setupAdminTestServerCfg{
		statusCode: http.StatusConflict,
		errorBody:  "setup is already complete",
	})
	defer srv.Close()

	pwd := &fakePasswordReader{password: "pw1234567"}
	_, _, err := runSetupAdminInTempHome(t, srv.URL, pwd.read, nil)
	if err == nil {
		t.Fatal("expected 'already initialized' error, got nil")
	}
	if !strings.Contains(err.Error(), "already initialized") {
		t.Errorf("error should mention 'already initialized'; got: %v", err)
	}
	// Must point at the right next step — login, not retry.
	if !strings.Contains(err.Error(), "sourcebridge login") {
		t.Errorf("error should suggest `sourcebridge login`; got: %v", err)
	}
}

// TestSetupAdmin_NoSave_DoesNotPersistToken pins the --no-save flag.
// Useful for users with multiple SourceBridge servers who don't want
// the setup token clobbering an existing ~/.sourcebridge/token.
func TestSetupAdmin_NoSave_DoesNotPersistToken(t *testing.T) {
	srv := newSetupAdminTestServer(t, setupAdminTestServerCfg{
		token: "ca_no_save_token",
	})
	defer srv.Close()

	pwd := &fakePasswordReader{password: "pw1234567"}
	token, server, err := runSetupAdminInTempHome(t, srv.URL, pwd.read, func() {
		setupAdminNoSave = true
	})
	if err != nil {
		t.Fatalf("runSetupAdmin: %v", err)
	}
	if token != "" {
		t.Errorf("token file should be empty with --no-save; got %q", token)
	}
	// Server URL should still be saved — that's not what --no-save controls.
	if server != srv.URL {
		t.Errorf("server URL should still be persisted; got %q", server)
	}
}

func TestSetupAdmin_PrecedenceConflict(t *testing.T) {
	srv := newSetupAdminTestServer(t, setupAdminTestServerCfg{
		token: "should-not-reach",
	})
	defer srv.Close()

	tmpDir := t.TempDir()
	pwFile := filepath.Join(tmpDir, "pw")
	_ = os.WriteFile(pwFile, []byte("file-pw"), 0o600)

	pwd := &fakePasswordReader{err: errInteractiveBypassed}
	_, _, err := runSetupAdminInTempHome(t, srv.URL, pwd.read, func() {
		t.Setenv("SOURCEBRIDGE_PASSWORD", "env-pw")
		setupAdminPasswordInput.Stdin = true
		setupAdminPasswordInput.File = pwFile
	})
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "more than one") {
		t.Errorf("error should mention 'more than one'; got: %v", err)
	}
}

// ---- helpers ---------------------------------------------------------------

// errInteractiveBypassed is returned by the interactive prompt fake
// to assert "this should not have been called". A non-interactive
// vector should resolve before reaching the prompt.
var errInteractiveBypassed = stringError("interactive prompt should have been bypassed")

type stringError string

func (s stringError) Error() string { return string(s) }

// mismatchPasswordReader returns `first` on the initial call and
// `second` on subsequent calls. Models a typo on the confirmation
// re-type.
type mismatchPasswordReader struct {
	first  string
	second string
	calls  int
}

func (m *mismatchPasswordReader) read(_ string) (string, error) {
	m.calls++
	if m.calls == 1 {
		return m.first, nil
	}
	return m.second, nil
}
