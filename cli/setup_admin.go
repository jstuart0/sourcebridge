// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// setupAdminCmd is the "sourcebridge setup admin" leaf — initial admin
// password setup for self-hosted single-admin servers.
//
// Why a first-class CLI for this (rather than telling users to curl
// POST /auth/setup): the curl form is hand-craftable but it's the
// classic "the CLI is friendly until it isn't" footgun. A user in a CI
// or headless context who wants to bootstrap a fresh server in one
// shell line shouldn't have to learn the API's wire shape. Tester report
// 2026-04-30 (Pazaryna) Issue 4 follow-on / CA-127.
//
// UX bar (per user direction): progressive disclosure. Bare
// `sourcebridge setup admin` is interactive with a confirmation prompt;
// `--help` surfaces the non-interactive flags front-and-center; success
// prints a copy-paste-ready next-step `sourcebridge login` example with
// the actual server URL.
var setupAdminCmd = &cobra.Command{
	Use:   "admin",
	Short: "Initialize the admin password on a fresh SourceBridge server",
	Long: `Set up the initial admin password on a self-hosted SourceBridge
server. After this completes the server is ready for ` + "`sourcebridge login`" + `.

Three non-interactive password vectors are supported (in precedence order):
  --password-stdin       read one line from stdin (recommended for CI)
  --password-file <path> read from a file (warns if mode > 0600)
  SOURCEBRIDGE_PASSWORD  env var (last resort; visible in /proc/<pid>/environ)

When none of these is set, you'll be prompted interactively with a
confirmation re-type, like ` + "`passwd(1)`" + `.

Examples:
  # Interactive (the polished default — confirmation prompt included)
  sourcebridge setup admin --server https://sourcebridge.example.com

  # Fully scriptable bootstrap (ideal for CI, devcontainers, agents)
  echo "$ADMIN_PW" | sourcebridge setup admin \
      --server https://sourcebridge.example.com --password-stdin

  # Mounted secret file
  sourcebridge setup admin --server https://sourcebridge.example.com \
      --password-file /etc/sourcebridge/admin-password

  # Already-set env var
  SOURCEBRIDGE_PASSWORD="$ADMIN_PW" sourcebridge setup admin \
      --server https://sourcebridge.example.com

The server URL can also come from SOURCEBRIDGE_URL or
~/.sourcebridge/server (the same resolution chain ` + "`sourcebridge login`" + `
uses), so a follow-up ` + "`sourcebridge setup admin`" + ` after a previous
login on the same machine doesn't need ` + "`--server`" + ` again.

The minimum admin password length is 8 characters. The server enforces
this; we surface it client-side too so a too-short password fails before
the network round-trip.`,
	RunE: runSetupAdmin,
}

var (
	setupAdminServer        string
	setupAdminPasswordInput PasswordInputFlags
	setupAdminNoSave        bool
)

const (
	// minAdminPasswordLength mirrors internal/auth/local.go's check
	// (`if len(password) < 8`). Surfacing this client-side gives the
	// user a fast-feedback validation rather than a server round-trip.
	minAdminPasswordLength = 8

	// setupConfirmPrompt is the interactive re-type prompt. Matches
	// passwd(1) cadence for muscle memory.
	setupConfirmPrompt = "Confirm admin password: "
	setupInitialPrompt = "Set admin password: "
)

func init() {
	setupAdminCmd.Flags().StringVar(&setupAdminServer, "server", "",
		"SourceBridge server URL (overrides SOURCEBRIDGE_URL and ~/.sourcebridge/server)")
	setupAdminCmd.Flags().BoolVar(&setupAdminNoSave, "no-save", false,
		"Skip writing the returned admin token to ~/.sourcebridge/token. "+
			"By default the token IS saved so an immediately-following `sourcebridge ask` "+
			"or `sourcebridge index` works without a separate `sourcebridge login`.")
	setupAdminPasswordInput.RegisterFlags(setupAdminCmd.Flags().BoolVar, setupAdminCmd.Flags().StringVar)

	setupCmd.AddCommand(setupAdminCmd)
}

// setupAPIResponse mirrors the server's /auth/setup response shape
// (token + expires_in). Pinned here so an accidental wire-shape drift
// on the server fails the build rather than silently degrading
// behavior.
type setupAPIResponse struct {
	Token     string `json:"token"`
	ExpiresIn int    `json:"expires_in"`
}

// setupAPIErrorResponse mirrors the {"error": "..."} shape used by
// handleSetup on failure paths. Decoded on a best-effort basis — if
// the server returned plain text, we surface the status code instead.
type setupAPIErrorResponse struct {
	Error string `json:"error"`
}

func runSetupAdmin(cmd *cobra.Command, args []string) error {
	return runSetupAdminWith(cmd, defaultPasswordReader)
}

// runSetupAdminWith is the testable seam (matching runLoginWith's
// pattern). Tests inject a fake passwordReader; production uses the
// terminal-aware default.
func runSetupAdminWith(cmd *cobra.Command, pwdReader passwordReader) error {
	serverURL := resolveSetupAdminServerURL()
	if serverURL == "" {
		return fmt.Errorf(
			"no SourceBridge server URL provided.\n" +
				"Pass --server https://your-server.example.com, set SOURCEBRIDGE_URL, " +
				"or run `sourcebridge login` first to persist the URL.",
		)
	}

	// Resolve the password. Non-interactive vectors short-circuit the
	// confirmation prompt — a CI run that already knows the password
	// shouldn't be asked to confirm.
	password, src, err := setupAdminPasswordInput.ResolveNonInteractive(os.Stdin)
	if err != nil {
		return err
	}
	if src == PasswordSourceNone {
		// Interactive — prompt twice with confirmation, like passwd(1).
		password, err = readPasswordWithConfirmation(pwdReader)
		if err != nil {
			return err
		}
	} else {
		fmt.Fprintf(os.Stderr, "Using admin password from %s.\n", src)
	}

	if err := validateAdminPassword(password); err != nil {
		return err
	}

	// Post to /auth/setup. The server creates the admin AND returns a
	// token in one call — we use the token immediately (no separate
	// login round-trip).
	token, err := postSetupRequest(cmd.Context(), serverURL, password)
	if err != nil {
		return err
	}

	// Persist the server URL so an immediately-following
	// `sourcebridge ...` works without --server. (saveServerURL is
	// idempotent: same URL is a no-op write.)
	if err := saveServerURL(serverURL); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not save server URL: %v\n", err)
	}

	// Persist the token unless --no-save was set. The setup endpoint
	// returns a session token; saving it means the next CLI command
	// against this server doesn't need its own login.
	if !setupAdminNoSave {
		result, saveErr := saveToken(token, true)
		if saveErr != nil {
			return fmt.Errorf(
				"admin created successfully, but the returned token could not be saved: %w\n"+
					"Run `sourcebridge login --server %s --method local` to authenticate.",
				saveErr, serverURL,
			)
		}
		if result.Replaced {
			fmt.Fprintf(os.Stdout, "Replaced existing ~/.sourcebridge/token.\n")
		}
		fmt.Fprintf(os.Stdout, "Saved %s (full token in ~/.sourcebridge/token).\n", tokenPrefix(token))
	}

	// Polished success message. Tells the user exactly what just
	// happened and exactly what to do next, with the server URL
	// pre-populated so it's a copy-paste line, not a fill-in-the-blank
	// template.
	fmt.Fprintf(os.Stdout, "\nAdmin account initialized on %s.\n", serverURL)
	if setupAdminNoSave {
		fmt.Fprintf(os.Stdout, "\nNext step:\n  sourcebridge login --server %s --method local\n", serverURL)
	} else {
		fmt.Fprintf(os.Stdout, "\nYou're now authenticated. Try:\n")
		fmt.Fprintf(os.Stdout, "  sourcebridge index <path-to-repo>\n")
		fmt.Fprintf(os.Stdout, "  sourcebridge ask \"What does this repo do?\"\n")
	}
	return nil
}

// resolveSetupAdminServerURL applies the same resolution chain as login:
// --server flag → SOURCEBRIDGE_URL env → ~/.sourcebridge/server file.
// (Login is the canonical resolver; we mirror its behavior so users
// don't have to learn two different rules.)
func resolveSetupAdminServerURL() string {
	if setupAdminServer != "" {
		return strings.TrimRight(setupAdminServer, "/")
	}
	if env := os.Getenv("SOURCEBRIDGE_URL"); env != "" {
		return strings.TrimRight(env, "/")
	}
	if saved := readServerURL(); saved != "" {
		return saved
	}
	return ""
}

// readPasswordWithConfirmation prompts twice and returns the password
// only when both entries match. Mirrors passwd(1) — users expect this
// when setting a password for the first time. Mismatch is a hard
// error (not a re-prompt loop) so a wedged terminal doesn't spin.
func readPasswordWithConfirmation(pwdReader passwordReader) (string, error) {
	first, err := pwdReader(setupInitialPrompt)
	if err != nil {
		return "", err
	}
	second, err := pwdReader(setupConfirmPrompt)
	if err != nil {
		return "", err
	}
	if first != second {
		return "", fmt.Errorf("passwords do not match")
	}
	return first, nil
}

// validateAdminPassword applies the server-side rule client-side so
// "too short" is caught before the network round-trip. The server's
// own validation remains the source of truth — this is a UX shortcut.
func validateAdminPassword(password string) error {
	if password == "" {
		return fmt.Errorf("password cannot be empty")
	}
	if len(password) < minAdminPasswordLength {
		return fmt.Errorf(
			"password must be at least %d characters (got %d).",
			minAdminPasswordLength, len(password),
		)
	}
	return nil
}

// postSetupRequest does the HTTP round-trip to POST /auth/setup and
// returns the resulting token. Differentiates the three failure modes
// the user actually hits: server unreachable, server already
// initialized (409), and validation rejection (400).
func postSetupRequest(ctx context.Context, serverURL, password string) (string, error) {
	body, err := json.Marshal(map[string]string{"password": password})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		serverURL+"/auth/setup", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot reach server at %s: %w", serverURL, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var result setupAPIResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return "", fmt.Errorf("parsing setup response: %w", err)
		}
		if result.Token == "" {
			return "", fmt.Errorf("server returned an empty token")
		}
		return result.Token, nil
	case http.StatusConflict:
		// Server is already initialized. Tell the user what to do
		// instead — running setup twice is a benign mistake (often a
		// re-run of an idempotent provisioning script).
		return "", fmt.Errorf(
			"this server is already initialized. Run `sourcebridge login --server %s --method local` "+
				"to authenticate, or use `sourcebridge ... change-password` to rotate the admin password.",
			serverURL,
		)
	case http.StatusBadRequest:
		// Read the server's error message (best-effort) and surface it
		// — typically this is a password-policy rejection.
		errMsg := readErrorMessage(resp.Body)
		if errMsg != "" {
			return "", fmt.Errorf("server rejected setup request: %s", errMsg)
		}
		return "", fmt.Errorf("server rejected setup request (HTTP 400)")
	default:
		errMsg := readErrorMessage(resp.Body)
		if errMsg != "" {
			return "", fmt.Errorf("setup failed (HTTP %d): %s", resp.StatusCode, errMsg)
		}
		return "", fmt.Errorf("setup failed (HTTP %d)", resp.StatusCode)
	}
}

// readErrorMessage best-effort decodes the server's {"error": "..."}
// payload. Returns "" when the body isn't JSON or the field is missing
// — caller falls back to the status code in that case.
func readErrorMessage(body io.Reader) string {
	var resp setupAPIErrorResponse
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return ""
	}
	return resp.Error
}
