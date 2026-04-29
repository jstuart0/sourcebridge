// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with a SourceBridge server",
	Long: `Authenticate with a SourceBridge server and save the token to
~/.sourcebridge/token (mode 0600) and the server URL to
~/.sourcebridge/server (mode 0644).

For cloud / OIDC installations the command opens a browser and polls
until you complete the login flow. For self-hosted single-admin servers
it prompts for the admin password directly.

Use --method to override the auto-detected flow:
  auto   (default) probe the server; prefer OIDC if both are available
  oidc   force OIDC browser flow
  local  force local-password prompt

Use --no-open to print the auth URL instead of launching a browser
(useful in CI and headless environments).`,
	RunE: runLogin,
}

var (
	loginServer string
	loginMethod string
	loginNoOpen bool
)

func init() {
	loginCmd.Flags().StringVar(&loginServer, "server", "", "SourceBridge server URL")
	loginCmd.Flags().StringVar(&loginMethod, "method", "auto", "Auth method: auto, oidc, or local")
	loginCmd.Flags().BoolVar(&loginNoOpen, "no-open", false, "Print the auth URL instead of opening a browser")
}

// browserOpener is a small seam so tests can inject a fake opener.
type browserOpener func(url string) error

// defaultBrowserOpener opens url in the system browser. It passes the URL as a
// separate argument (never shell-interpolated) to prevent injection via a
// crafted auth_url from a malicious server.
func defaultBrowserOpener(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported OS %q", runtime.GOOS)
	}
	return cmd.Start()
}

// passwordReader is a seam for reading a password without echo. Tests inject a
// fake that returns a fixed password without touching a real terminal.
type passwordReader func(prompt string) (string, error)

// defaultPasswordReader reads a password from stdin with echo disabled.
func defaultPasswordReader(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	raw, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr) // newline after the invisible input
	if err != nil {
		return "", fmt.Errorf("reading password: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// desktopInfoResponse mirrors GET /auth/desktop/info.
type desktopInfoResponse struct {
	LocalAuth   bool `json:"local_auth"`
	SetupDone   bool `json:"setup_done"`
	OIDCEnabled bool `json:"oidc_enabled"`
}

// desktopOIDCStartResp mirrors POST /auth/desktop/oidc/start.
type desktopOIDCStartResp struct {
	SessionID string `json:"session_id"`
	AuthURL   string `json:"auth_url"`
	ExpiresIn int    `json:"expires_in"`
}

// desktopPollResp mirrors GET /auth/desktop/oidc/poll.
type desktopPollResp struct {
	Status string `json:"status"`
	Token  string `json:"token,omitempty"`
}

// desktopLocalLoginResp mirrors POST /auth/desktop/local-login.
type desktopLocalLoginResp struct {
	Token     string `json:"token"`
	ExpiresIn int    `json:"expires_in"`
}

func runLogin(cmd *cobra.Command, args []string) error {
	return runLoginWith(cmd, defaultBrowserOpener, defaultPasswordReader)
}

func runLoginWith(cmd *cobra.Command, opener browserOpener, pwdReader passwordReader) error {
	serverURL := resolveLoginServerURL(cmd)
	if serverURL == "" {
		return fmt.Errorf(
			"no SourceBridge server URL provided.\n" +
				"Pass --server https://your-server.example.com, set SOURCEBRIDGE_URL, " +
				"or run `sourcebridge login --server <url>` with an explicit URL",
		)
	}

	// Probe /auth/desktop/info to determine available flows.
	info, err := fetchDesktopInfo(cmd.Context(), serverURL)
	if err != nil {
		return fmt.Errorf("cannot reach server at %s: %w", serverURL, err)
	}

	// Determine which flow to run.
	method := loginMethod
	if method == "auto" {
		method = pickMethod(info, serverURL)
		if method == "" {
			// pickMethod printed the error.
			return fmt.Errorf(
				"this server doesn't expose desktop auth (neither OIDC nor local-password is configured).\n"+
					"Mint a token at %s/settings/tokens and pass it via --token to `sourcebridge setup claude`.",
				serverURL,
			)
		}
	}

	// Validate method against what the server supports.
	if err := validateMethod(method, info, serverURL); err != nil {
		return err
	}

	var token string
	switch method {
	case "oidc":
		token, err = runOIDCFlow(cmd.Context(), serverURL, opener)
	case "local":
		token, err = runLocalFlow(cmd.Context(), serverURL, pwdReader)
	default:
		return fmt.Errorf("unknown --method %q (expected auto, oidc, or local)", method)
	}
	if err != nil {
		return err
	}

	// Write token BEFORE printing success. If the write fails the session is
	// permanently consumed — surface a clear error telling the user so.
	result, err := saveToken(token, true)
	if err != nil {
		return fmt.Errorf(
			"authenticated successfully but could not save the token: %w\n"+
				"This session is permanently consumed. Run `sourcebridge login` again to get a new one.",
			err,
		)
	}

	if err := saveServerURL(serverURL); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not save server URL: %v\n", err)
	}

	if result.Replaced {
		fmt.Fprintf(os.Stdout, "Replaced existing ~/.sourcebridge/token.\n")
	}
	fmt.Fprintf(os.Stdout, "Saved %s (full token in ~/.sourcebridge/token)\n", tokenPrefix(token))
	fmt.Fprintf(os.Stdout, "Logged in to %s\n", serverURL)
	return nil
}

// resolveLoginServerURL applies the resolution chain for the login command:
// --server flag → SOURCEBRIDGE_URL env → ~/.sourcebridge/server file.
// (No config.toml fallback here — login is the command that sets up config.)
func resolveLoginServerURL(cmd *cobra.Command) string {
	if loginServer != "" {
		return strings.TrimRight(loginServer, "/")
	}
	if env := os.Getenv("SOURCEBRIDGE_URL"); env != "" {
		return strings.TrimRight(env, "/")
	}
	if saved := readServerURL(); saved != "" {
		return saved
	}
	return ""
}

// pickMethod returns "oidc" or "local" based on the server info. Prefers OIDC.
// Returns "" when neither flow is available.
func pickMethod(info *desktopInfoResponse, serverURL string) string {
	if !info.OIDCEnabled && !info.LocalAuth {
		return ""
	}
	if info.OIDCEnabled {
		return "oidc"
	}
	return "local"
}

// validateMethod checks that the requested method is actually available.
func validateMethod(method string, info *desktopInfoResponse, serverURL string) error {
	switch method {
	case "oidc":
		if !info.OIDCEnabled {
			return fmt.Errorf(
				"this server does not have OIDC configured.\n"+
					"Use --method local or --method auto, or visit %s/settings/tokens to mint a token.",
				serverURL,
			)
		}
	case "local":
		if !info.LocalAuth {
			return fmt.Errorf(
				"this server does not have local-password auth configured.\n"+
					"Use --method oidc or --method auto.",
			)
		}
		if !info.SetupDone {
			return fmt.Errorf(
				"this server hasn't been initialized yet.\n"+
					"Visit %s/setup to complete initial setup first.",
				serverURL,
			)
		}
	}
	return nil
}

// fetchDesktopInfo calls GET /auth/desktop/info.
func fetchDesktopInfo(ctx context.Context, serverURL string) (*desktopInfoResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/auth/desktop/info", nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}
	var info desktopInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("parsing /auth/desktop/info response: %w", err)
	}
	return &info, nil
}

// runOIDCFlow starts a desktop OIDC session, optionally opens a browser, and
// polls until the session is complete or expired.
func runOIDCFlow(ctx context.Context, serverURL string, opener browserOpener) (string, error) {
	// Start the OIDC session.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/auth/desktop/oidc/start", nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("starting OIDC session: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OIDC start returned HTTP %d", resp.StatusCode)
	}
	var start desktopOIDCStartResp
	if err := json.NewDecoder(resp.Body).Decode(&start); err != nil {
		return "", fmt.Errorf("parsing OIDC start response: %w", err)
	}
	if start.SessionID == "" || start.AuthURL == "" {
		return "", fmt.Errorf("OIDC start response is missing session_id or auth_url")
	}

	// Open the browser (or print the URL).
	if loginNoOpen {
		fmt.Fprintf(os.Stdout, "Open this URL in your browser to authenticate:\n%s\n", start.AuthURL)
	} else {
		if err := opener(start.AuthURL); err != nil {
			fmt.Fprintf(os.Stdout, "Could not open browser automatically.\nOpen this URL in your browser:\n%s\n", start.AuthURL)
		} else {
			fmt.Fprintf(os.Stdout, "Opening browser for authentication...\n")
		}
	}
	fmt.Fprintf(os.Stdout, "Waiting for authentication")

	// Determine deadline from expires_in (clamped to 15 minutes).
	expiresIn := start.ExpiresIn
	if expiresIn <= 0 || expiresIn > 900 {
		expiresIn = 600 // 10-minute default
	}
	deadline := time.Now().Add(time.Duration(expiresIn) * time.Second)

	// Poll until complete, expired, or context cancelled.
	token, err := pollOIDCSession(ctx, serverURL, start.SessionID, deadline)
	fmt.Fprintln(os.Stdout) // end the dot-line
	return token, err
}

// pollOIDCSession polls GET /auth/desktop/oidc/poll until the session is
// complete, the deadline is exceeded, or the context is cancelled.
func pollOIDCSession(ctx context.Context, serverURL, sessionID string, deadline time.Time) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	for {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("authentication timed out. Run `sourcebridge login` to try again")
		}
		// Select returns immediately if ctx is done.
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			serverURL+"/auth/desktop/oidc/poll?session_id="+sessionID, nil)
		if err != nil {
			return "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			// Transient network error — keep trying until deadline.
			jitter := time.Duration(rand.IntN(500)) * time.Millisecond
			sleepWithContext(ctx, 2*time.Second+jitter)
			continue
		}

		if resp.StatusCode == http.StatusNotFound {
			_ = resp.Body.Close()
			return "", fmt.Errorf(
				"session expired or already consumed.\n" +
					"Run `sourcebridge login` again to start a new session.",
			)
		}

		var poll desktopPollResp
		_ = json.NewDecoder(resp.Body).Decode(&poll)
		_ = resp.Body.Close()

		if poll.Status == "complete" && poll.Token != "" {
			return poll.Token, nil
		}

		// Still pending — print a dot and wait.
		fmt.Fprint(os.Stdout, ".")
		jitter := time.Duration(rand.IntN(500)) * time.Millisecond
		sleepWithContext(ctx, 2*time.Second+jitter)
	}
}

// runLocalFlow prompts for the admin password and exchanges it for a token.
func runLocalFlow(ctx context.Context, serverURL string, pwdReader passwordReader) (string, error) {
	password, err := pwdReader("Admin password: ")
	if err != nil {
		return "", err
	}
	if password == "" {
		return "", fmt.Errorf("password cannot be empty")
	}

	payload := map[string]string{
		"password":   password,
		"token_name": "CLI Session",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		serverURL+"/auth/desktop/local-login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("local login request failed: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var result desktopLocalLoginResp
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return "", fmt.Errorf("parsing local login response: %w", err)
		}
		if result.Token == "" {
			return "", fmt.Errorf("server returned an empty token")
		}
		return result.Token, nil
	case http.StatusUnauthorized:
		return "", fmt.Errorf(
			"incorrect password. Try again, or use --method oidc if OIDC is configured on this server.",
		)
	default:
		return "", fmt.Errorf("local login returned HTTP %d", resp.StatusCode)
	}
}

// sleepWithContext sleeps for d or until ctx is cancelled, whichever comes
// first.
func sleepWithContext(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}
