// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
)

// ---- test helpers -----------------------------------------------------------

// fakeBrowserOpener records calls and returns a pre-configured error.
type fakeBrowserOpener struct {
	calls []string
	err   error
}

func (f *fakeBrowserOpener) open(url string) error {
	f.calls = append(f.calls, url)
	return f.err
}

// fakePasswordReader returns a fixed password.
type fakePasswordReader struct {
	password string
	err      error
}

func (f *fakePasswordReader) read(_ string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.password, nil
}

// loginTestServer builds a minimal httptest.Server that emulates the desktop
// auth endpoints. Configuration drives each endpoint's behavior.
type loginTestServerCfg struct {
	// /auth/desktop/info response
	localAuth   bool
	setupDone   bool
	oidcEnabled bool

	// /auth/desktop/oidc/start response
	oidcSessionID string
	oidcAuthURL   string
	oidcExpiresIn int

	// /auth/desktop/oidc/poll: number of "pending" responses before "complete"
	oidcPendingBeforeComplete int
	oidcFinalToken            string
	// when true, poll always returns 404 ("session expired")
	oidcPollExpired bool

	// /auth/desktop/local-login response (200 vs 401)
	localLoginStatusCode int
	localLoginToken      string
}

func newLoginTestServer(t *testing.T, cfg loginTestServerCfg) *httptest.Server {
	t.Helper()

	if cfg.oidcExpiresIn == 0 {
		cfg.oidcExpiresIn = 600
	}
	if cfg.localLoginStatusCode == 0 {
		cfg.localLoginStatusCode = http.StatusOK
	}
	if cfg.oidcSessionID == "" {
		cfg.oidcSessionID = "ide_test_session"
	}
	if cfg.oidcAuthURL == "" {
		cfg.oidcAuthURL = "https://sso.example.com/auth?state=abc"
	}

	var pollCount atomic.Int32

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/auth/desktop/info":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"local_auth":   cfg.localAuth,
				"setup_done":   cfg.setupDone,
				"oidc_enabled": cfg.oidcEnabled,
			})

		case r.Method == http.MethodPost && r.URL.Path == "/auth/desktop/oidc/start":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"session_id": cfg.oidcSessionID,
				"auth_url":   cfg.oidcAuthURL,
				"expires_in": cfg.oidcExpiresIn,
			})

		case r.Method == http.MethodGet && r.URL.Path == "/auth/desktop/oidc/poll":
			if cfg.oidcPollExpired {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "session not found or expired"})
				return
			}
			n := int(pollCount.Add(1))
			if n <= cfg.oidcPendingBeforeComplete {
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "complete",
				"token":  cfg.oidcFinalToken,
			})

		case r.Method == http.MethodPost && r.URL.Path == "/auth/desktop/local-login":
			if cfg.localLoginStatusCode == http.StatusUnauthorized {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid credentials"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      cfg.localLoginToken,
				"expires_in": 0,
			})

		default:
			http.NotFound(w, r)
		}
	}))
}

// newTestCmd returns a cobra.Command with a background context set.
func newTestCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return cmd
}

// resetLoginFlags resets all login flag vars to zero values.
func resetLoginFlags() {
	loginServer = ""
	loginMethod = "auto"
	loginNoOpen = false
}

// runLoginInTempHome sets HOME to a temp dir, resets login flags, runs
// runLoginWith, and returns the written token and server files.
func runLoginInTempHome(t *testing.T, serverURL string, method string, noOpen bool,
	opener browserOpener, pwdReader passwordReader) (tokenContent, serverContent string, err error) {
	t.Helper()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("SOURCEBRIDGE_API_TOKEN", "")

	resetLoginFlags()
	loginServer = serverURL
	loginMethod = method
	loginNoOpen = noOpen
	defer resetLoginFlags()

	cmd := newTestCmd()
	err = runLoginWith(cmd, opener, pwdReader)

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

// ---- tests ------------------------------------------------------------------

// TestLogin_OIDC_HappyPath verifies the full OIDC flow: start → poll-pending →
// poll-complete → token and server URL saved.
func TestLogin_OIDC_HappyPath(t *testing.T) {
	srv := newLoginTestServer(t, loginTestServerCfg{
		oidcEnabled:               true,
		localAuth:                 false,
		setupDone:                 false,
		oidcFinalToken:            "ca_oidc_token_abc123",
		oidcPendingBeforeComplete: 2,
	})
	defer srv.Close()

	opener := &fakeBrowserOpener{}
	pwd := &fakePasswordReader{}

	token, server, err := runLoginInTempHome(t, srv.URL, "oidc", false,
		opener.open, pwd.read)
	if err != nil {
		t.Fatalf("runLoginWith: %v", err)
	}
	if token != "ca_oidc_token_abc123" {
		t.Errorf("token = %q, want %q", token, "ca_oidc_token_abc123")
	}
	if server != srv.URL {
		t.Errorf("server = %q, want %q", server, srv.URL)
	}
	// Browser opener must have been called once.
	if len(opener.calls) != 1 {
		t.Errorf("opener called %d times, want 1", len(opener.calls))
	}
}

// TestLogin_Local_HappyPath verifies the local-password flow saves token and
// server correctly.
func TestLogin_Local_HappyPath(t *testing.T) {
	srv := newLoginTestServer(t, loginTestServerCfg{
		localAuth:        true,
		setupDone:        true,
		oidcEnabled:      false,
		localLoginToken:  "ca_local_token_xyz",
		localLoginStatusCode: http.StatusOK,
	})
	defer srv.Close()

	opener := &fakeBrowserOpener{}
	pwd := &fakePasswordReader{password: "mysecret"}

	token, server, err := runLoginInTempHome(t, srv.URL, "local", false,
		opener.open, pwd.read)
	if err != nil {
		t.Fatalf("runLoginWith: %v", err)
	}
	if token != "ca_local_token_xyz" {
		t.Errorf("token = %q, want %q", token, "ca_local_token_xyz")
	}
	if server != srv.URL {
		t.Errorf("server = %q, want %q", server, srv.URL)
	}
	// Browser opener must NOT have been called.
	if len(opener.calls) != 0 {
		t.Errorf("opener called unexpectedly: %v", opener.calls)
	}
}

// TestLogin_Auto_PrefersOIDC verifies that --method auto picks OIDC when both
// are available.
func TestLogin_Auto_PrefersOIDC(t *testing.T) {
	srv := newLoginTestServer(t, loginTestServerCfg{
		oidcEnabled:    true,
		localAuth:      true,
		setupDone:      true,
		oidcFinalToken: "ca_oidc_preferred",
	})
	defer srv.Close()

	opener := &fakeBrowserOpener{}
	pwd := &fakePasswordReader{}

	token, _, err := runLoginInTempHome(t, srv.URL, "auto", false,
		opener.open, pwd.read)
	if err != nil {
		t.Fatalf("runLoginWith: %v", err)
	}
	// OIDC was picked — browser opener called, password reader not.
	if len(opener.calls) != 1 {
		t.Errorf("expected 1 opener call (OIDC), got %d", len(opener.calls))
	}
	if token != "ca_oidc_preferred" {
		t.Errorf("token = %q, want %q", token, "ca_oidc_preferred")
	}
}

// TestLogin_Auto_PicksLocalWhenOnlyLocalAvailable verifies that --method auto
// falls back to local when OIDC is not configured.
func TestLogin_Auto_PicksLocalWhenOnlyLocalAvailable(t *testing.T) {
	srv := newLoginTestServer(t, loginTestServerCfg{
		localAuth:        true,
		setupDone:        true,
		oidcEnabled:      false,
		localLoginToken:  "ca_local_only",
		localLoginStatusCode: http.StatusOK,
	})
	defer srv.Close()

	opener := &fakeBrowserOpener{}
	pwd := &fakePasswordReader{password: "pw"}

	token, _, err := runLoginInTempHome(t, srv.URL, "auto", false,
		opener.open, pwd.read)
	if err != nil {
		t.Fatalf("runLoginWith: %v", err)
	}
	if len(opener.calls) != 0 {
		t.Errorf("browser opener should not be called for local flow")
	}
	if token != "ca_local_only" {
		t.Errorf("token = %q, want %q", token, "ca_local_only")
	}
}

// TestLogin_Auto_ErrorsWhenNeitherConfigured verifies that --method auto
// returns a clear error when neither OIDC nor local is available.
func TestLogin_Auto_ErrorsWhenNeitherConfigured(t *testing.T) {
	srv := newLoginTestServer(t, loginTestServerCfg{
		localAuth:   false,
		setupDone:   false,
		oidcEnabled: false,
	})
	defer srv.Close()

	opener := &fakeBrowserOpener{}
	pwd := &fakePasswordReader{}

	_, _, err := runLoginInTempHome(t, srv.URL, "auto", false, opener.open, pwd.read)
	if err == nil {
		t.Fatal("expected error when neither OIDC nor local configured, got nil")
	}
	if !strings.Contains(err.Error(), "settings/tokens") {
		t.Errorf("error should mention settings/tokens; got: %v", err)
	}
}

// TestLogin_NoOpen_PrintsURLDoesNotCallOpener verifies that --no-open skips the
// browser opener but does not block the poll.
func TestLogin_NoOpen_PrintsURLDoesNotCallOpener(t *testing.T) {
	srv := newLoginTestServer(t, loginTestServerCfg{
		oidcEnabled:    true,
		oidcFinalToken: "ca_noopen_token",
	})
	defer srv.Close()

	opener := &fakeBrowserOpener{}
	pwd := &fakePasswordReader{}

	// Capture stdout to verify URL is printed.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_, _, err := runLoginInTempHome(t, srv.URL, "oidc", true, opener.open, pwd.read)

	_ = w.Close()
	os.Stdout = old
	var buf strings.Builder
	tmp := make([]byte, 4096)
	for {
		n, rErr := r.Read(tmp)
		buf.Write(tmp[:n])
		if rErr != nil {
			break
		}
	}
	output := buf.String()

	if err != nil {
		t.Fatalf("runLoginWith: %v", err)
	}
	if len(opener.calls) != 0 {
		t.Errorf("opener must not be called with --no-open; got calls: %v", opener.calls)
	}
	if !strings.Contains(output, "https://sso.example.com/auth") {
		t.Errorf("auth URL not printed in --no-open mode; output: %q", output)
	}
}

// TestLogin_BrowserOpenFails_FallsBackToPrinting verifies that a browser-open
// failure causes the URL to be printed instead.
func TestLogin_BrowserOpenFails_FallsBackToPrinting(t *testing.T) {
	srv := newLoginTestServer(t, loginTestServerCfg{
		oidcEnabled:    true,
		oidcFinalToken: "ca_fallback_token",
	})
	defer srv.Close()

	opener := &fakeBrowserOpener{err: fmt.Errorf("open: binary not found")}
	pwd := &fakePasswordReader{}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_, _, err := runLoginInTempHome(t, srv.URL, "oidc", false, opener.open, pwd.read)

	_ = w.Close()
	os.Stdout = old
	var buf strings.Builder
	tmp := make([]byte, 4096)
	for {
		n, rErr := r.Read(tmp)
		buf.Write(tmp[:n])
		if rErr != nil {
			break
		}
	}
	output := buf.String()

	if err != nil {
		t.Fatalf("runLoginWith: %v", err)
	}
	if !strings.Contains(output, "sso.example.com") {
		t.Errorf("URL should be printed when browser open fails; output: %q", output)
	}
}

// TestLogin_PollSessionExpired_ClearError verifies that a 404 poll response
// surfaces a clear "session expired" error and is not retried.
func TestLogin_PollSessionExpired_ClearError(t *testing.T) {
	srv := newLoginTestServer(t, loginTestServerCfg{
		oidcEnabled:     true,
		oidcPollExpired: true,
	})
	defer srv.Close()

	opener := &fakeBrowserOpener{}
	pwd := &fakePasswordReader{}

	_, _, err := runLoginInTempHome(t, srv.URL, "oidc", false, opener.open, pwd.read)
	if err == nil {
		t.Fatal("expected error for expired session, got nil")
	}
	if !strings.Contains(err.Error(), "expired") && !strings.Contains(err.Error(), "consumed") {
		t.Errorf("error should mention 'expired' or 'consumed'; got: %v", err)
	}
	if !strings.Contains(err.Error(), "sourcebridge login") {
		t.Errorf("error should mention 'sourcebridge login'; got: %v", err)
	}
}

// TestLogin_LocalLogin_WrongPassword_ClearError verifies that a 401 from the
// local-login endpoint yields an actionable "incorrect password" message.
func TestLogin_LocalLogin_WrongPassword_ClearError(t *testing.T) {
	srv := newLoginTestServer(t, loginTestServerCfg{
		localAuth:            true,
		setupDone:            true,
		oidcEnabled:          false,
		localLoginStatusCode: http.StatusUnauthorized,
	})
	defer srv.Close()

	opener := &fakeBrowserOpener{}
	pwd := &fakePasswordReader{password: "wrong"}

	_, _, err := runLoginInTempHome(t, srv.URL, "local", false, opener.open, pwd.read)
	if err == nil {
		t.Fatal("expected error for wrong password, got nil")
	}
	if !strings.Contains(err.Error(), "incorrect password") {
		t.Errorf("error should mention 'incorrect password'; got: %v", err)
	}
}

// TestLogin_ExistingToken_ReplacedNotice verifies that overwriting an existing
// token prints the "Replaced existing" notice.
func TestLogin_ExistingToken_ReplacedNotice(t *testing.T) {
	srv := newLoginTestServer(t, loginTestServerCfg{
		oidcEnabled:    true,
		oidcFinalToken: "ca_new_token",
	})
	defer srv.Close()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("SOURCEBRIDGE_API_TOKEN", "")

	// Pre-write an existing token.
	sbDir := filepath.Join(homeDir, ".sourcebridge")
	if err := os.MkdirAll(sbDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sbDir, "token"), []byte("ca_old_token"), 0o600); err != nil {
		t.Fatalf("pre-write token: %v", err)
	}

	resetLoginFlags()
	loginServer = srv.URL
	loginMethod = "oidc"
	defer resetLoginFlags()

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	opener := &fakeBrowserOpener{}
	cmd := newTestCmd()
	err := runLoginWith(cmd, opener.open, (&fakePasswordReader{}).read)

	_ = w.Close()
	os.Stdout = old
	var buf strings.Builder
	tmp := make([]byte, 4096)
	for {
		n, rErr := r.Read(tmp)
		buf.Write(tmp[:n])
		if rErr != nil {
			break
		}
	}
	output := buf.String()

	if err != nil {
		t.Fatalf("runLoginWith: %v", err)
	}

	// Verify the new token is on disk.
	data, _ := os.ReadFile(filepath.Join(sbDir, "token"))
	if strings.TrimSpace(string(data)) != "ca_new_token" {
		t.Errorf("token file = %q, want %q", strings.TrimSpace(string(data)), "ca_new_token")
	}
	if !strings.Contains(output, "Replaced existing") {
		t.Errorf("output should mention 'Replaced existing'; got: %q", output)
	}
}

// TestLogin_TokenFilePermission verifies the token file is mode 0600 and
// server file is mode 0644.
func TestLogin_TokenFilePermission(t *testing.T) {
	srv := newLoginTestServer(t, loginTestServerCfg{
		oidcEnabled:    true,
		oidcFinalToken: "ca_perm_token",
	})
	defer srv.Close()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("SOURCEBRIDGE_API_TOKEN", "")

	resetLoginFlags()
	loginServer = srv.URL
	loginMethod = "oidc"
	defer resetLoginFlags()

	cmd := newTestCmd()
	if err := runLoginWith(cmd, (&fakeBrowserOpener{}).open, (&fakePasswordReader{}).read); err != nil {
		t.Fatalf("runLoginWith: %v", err)
	}

	tokenInfo, err := os.Stat(filepath.Join(homeDir, ".sourcebridge", "token"))
	if err != nil {
		t.Fatalf("stat token: %v", err)
	}
	if tokenInfo.Mode().Perm() != 0o600 {
		t.Errorf("token mode = %o, want 0600", tokenInfo.Mode().Perm())
	}

	serverInfo, err := os.Stat(filepath.Join(homeDir, ".sourcebridge", "server"))
	if err != nil {
		t.Fatalf("stat server: %v", err)
	}
	if serverInfo.Mode().Perm() != 0o644 {
		t.Errorf("server mode = %o, want 0644", serverInfo.Mode().Perm())
	}
}

// TestLogin_TokenNeverPrinted verifies the full token value does not appear in
// stdout.
func TestLogin_TokenNeverPrinted(t *testing.T) {
	const fullToken = "ca_supersecret_full_token_value_1234567890"
	srv := newLoginTestServer(t, loginTestServerCfg{
		oidcEnabled:    true,
		oidcFinalToken: fullToken,
	})
	defer srv.Close()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("SOURCEBRIDGE_API_TOKEN", "")

	resetLoginFlags()
	loginServer = srv.URL
	loginMethod = "oidc"
	defer resetLoginFlags()

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	oldErr := os.Stderr
	re, we, _ := os.Pipe()
	os.Stderr = we

	cmd := newTestCmd()
	err := runLoginWith(cmd, (&fakeBrowserOpener{}).open, (&fakePasswordReader{}).read)

	_ = w.Close()
	_ = we.Close()
	os.Stdout = old
	os.Stderr = oldErr

	var stdoutBuf, stderrBuf strings.Builder
	tmp := make([]byte, 4096)
	for {
		n, rErr := r.Read(tmp)
		stdoutBuf.Write(tmp[:n])
		if rErr != nil {
			break
		}
	}
	for {
		n, rErr := re.Read(tmp)
		stderrBuf.Write(tmp[:n])
		if rErr != nil {
			break
		}
	}

	if err != nil {
		t.Fatalf("runLoginWith: %v", err)
	}
	combined := stdoutBuf.String() + stderrBuf.String()
	if strings.Contains(combined, fullToken) {
		t.Errorf("full token must not appear in stdout/stderr; got: %q", combined)
	}
	// Prefix should appear.
	if !strings.Contains(combined, tokenPrefix(fullToken)) {
		t.Errorf("token prefix %q should appear in output; got: %q", tokenPrefix(fullToken), combined)
	}
}

// TestLogin_ResolveLoginServerURL_Chain verifies the precedence chain for
// server URL resolution.
func TestLogin_ResolveLoginServerURL_Chain(t *testing.T) {
	t.Run("flag wins over env", func(t *testing.T) {
		resetLoginFlags()
		loginServer = "https://flag.example.com"
		t.Setenv("SOURCEBRIDGE_URL", "https://env.example.com")
		defer resetLoginFlags()
		cmd := newTestCmd()
		got := resolveLoginServerURL(cmd)
		if got != "https://flag.example.com" {
			t.Errorf("got %q, want flag value", got)
		}
	})

	t.Run("env wins over saved file", func(t *testing.T) {
		homeDir := t.TempDir()
		t.Setenv("HOME", homeDir)
		sbDir := filepath.Join(homeDir, ".sourcebridge")
		_ = os.MkdirAll(sbDir, 0o700)
		_ = os.WriteFile(filepath.Join(sbDir, "server"), []byte("https://saved.example.com"), 0o644)

		resetLoginFlags()
		t.Setenv("SOURCEBRIDGE_URL", "https://env.example.com")
		defer resetLoginFlags()
		cmd := newTestCmd()
		got := resolveLoginServerURL(cmd)
		if got != "https://env.example.com" {
			t.Errorf("got %q, want env value", got)
		}
	})

	t.Run("saved file when no flag or env", func(t *testing.T) {
		homeDir := t.TempDir()
		t.Setenv("HOME", homeDir)
		sbDir := filepath.Join(homeDir, ".sourcebridge")
		_ = os.MkdirAll(sbDir, 0o700)
		_ = os.WriteFile(filepath.Join(sbDir, "server"), []byte("https://saved.example.com"), 0o644)

		resetLoginFlags()
		t.Setenv("SOURCEBRIDGE_URL", "")
		defer resetLoginFlags()
		cmd := newTestCmd()
		got := resolveLoginServerURL(cmd)
		if got != "https://saved.example.com" {
			t.Errorf("got %q, want saved value", got)
		}
	})
}

// TestLogin_SetupNotDone_ClearError verifies that a local server with
// setup_done=false gives a clear "visit /setup" error.
func TestLogin_SetupNotDone_ClearError(t *testing.T) {
	srv := newLoginTestServer(t, loginTestServerCfg{
		localAuth:   true,
		setupDone:   false, // not initialized
		oidcEnabled: false,
	})
	defer srv.Close()

	_, _, err := runLoginInTempHome(t, srv.URL, "local", false,
		(&fakeBrowserOpener{}).open, (&fakePasswordReader{password: "pw"}).read)
	if err == nil {
		t.Fatal("expected error for setup not done, got nil")
	}
	if !strings.Contains(err.Error(), "setup") {
		t.Errorf("error should mention setup; got: %v", err)
	}
}

// TestLogin_OIDC_AuthURLWithUserinfo_Rejected verifies that a server returning
// an auth_url containing embedded credentials (user:pass@host) is rejected
// before the browser opener is called (NEW-3).
func TestLogin_OIDC_AuthURLWithUserinfo_Rejected(t *testing.T) {
	srv := newLoginTestServer(t, loginTestServerCfg{
		oidcEnabled:    true,
		oidcAuthURL:    "https://user:pass@evil.example.com/phish?state=abc",
		oidcFinalToken: "ca_should_never_arrive",
	})
	defer srv.Close()

	opener := &fakeBrowserOpener{}
	pwd := &fakePasswordReader{}

	_, _, err := runLoginInTempHome(t, srv.URL, "oidc", false, opener.open, pwd.read)
	if err == nil {
		t.Fatal("expected error for auth_url with embedded credentials, got nil")
	}
	if !strings.Contains(err.Error(), "embedded credentials") {
		t.Errorf("error should mention 'embedded credentials'; got: %v", err)
	}
	if len(opener.calls) != 0 {
		t.Errorf("browser opener must not be called when auth_url has userinfo; got calls: %v", opener.calls)
	}
}

// TestLogin_TokenWriteFailure_ClearError simulates a token-write failure by
// making the .sourcebridge directory unwritable. The error message must mention
// that the session is permanently consumed.
func TestLogin_TokenWriteFailure_ClearError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root — permission checks don't apply")
	}

	srv := newLoginTestServer(t, loginTestServerCfg{
		oidcEnabled:    true,
		oidcFinalToken: "ca_write_fail_token",
	})
	defer srv.Close()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("SOURCEBRIDGE_API_TOKEN", "")

	// Create the dir but make it read-only so CreateTemp fails.
	sbDir := filepath.Join(homeDir, ".sourcebridge")
	if err := os.MkdirAll(sbDir, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(sbDir, 0o700) })

	resetLoginFlags()
	loginServer = srv.URL
	loginMethod = "oidc"
	defer resetLoginFlags()

	cmd := newTestCmd()
	err := runLoginWith(cmd, (&fakeBrowserOpener{}).open, (&fakePasswordReader{}).read)
	if err == nil {
		t.Fatal("expected error when token file write fails, got nil")
	}
	if !strings.Contains(err.Error(), "permanently consumed") {
		t.Errorf("error should mention 'permanently consumed'; got: %v", err)
	}
}
