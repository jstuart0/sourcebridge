// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sourcebridgeDir returns the path to ~/.sourcebridge, creating it with mode
// 0700 if it does not exist.
func sourcebridgeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	dir := filepath.Join(home, ".sourcebridge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating ~/.sourcebridge: %w", err)
	}
	return dir, nil
}

// readAPIToken reads the API token from either the SOURCEBRIDGE_API_TOKEN env
// var (takes precedence for CI / one-off invocations) or the canonical CLI
// config location. Returns empty string on miss — the request still goes out
// and the server emits a clear 401 if auth is required.
func readAPIToken() string {
	if t := strings.TrimSpace(os.Getenv("SOURCEBRIDGE_API_TOKEN")); t != "" {
		return t
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	candidates := []string{
		filepath.Join(home, ".sourcebridge", "token"),
		filepath.Join(home, ".config", "sourcebridge", "token"),
	}
	for _, p := range candidates {
		if data, err := os.ReadFile(p); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return ""
}

// saveTokenResult is returned by saveToken to describe what happened.
type saveTokenResult struct {
	// Written is true when the file was actually created or updated (i.e. the
	// value changed from what was on disk). False means an idempotent no-op.
	Written bool
	// Replaced is true when an existing (different) token was overwritten.
	// Only meaningful when Written is true.
	Replaced bool
}

// saveToken writes token to ~/.sourcebridge/token atomically with mode 0600.
// It never writes on error, and never writes when the existing file already
// contains the same value (idempotent no-op).
//
// overwrite controls whether an existing (different) token is replaced. When
// overwrite is false and an existing different token is found, saveToken
// returns an error mentioning --force-token. When overwrite is true it
// replaces unconditionally (used by `sourcebridge login`).
func saveToken(token string, overwrite bool) (saveTokenResult, error) {
	dir, err := sourcebridgeDir()
	if err != nil {
		return saveTokenResult{}, err
	}
	tokenPath := filepath.Join(dir, "token")

	// Read existing token (if any) for idempotency / guard checks.
	var existing string
	if data, readErr := os.ReadFile(tokenPath); readErr == nil {
		existing = strings.TrimSpace(string(data))
	}

	// Idempotent: same value already on disk — silent no-op.
	if existing == token {
		return saveTokenResult{Written: false, Replaced: false}, nil
	}

	// Refuse to clobber a different token unless the caller allows it.
	if existing != "" && !overwrite {
		return saveTokenResult{}, fmt.Errorf(
			"refusing to overwrite existing ~/.sourcebridge/token. " +
				"Pass --force-token to replace, or --no-save to use the new token without persisting",
		)
	}

	// Atomic write: temp file in same directory → chmod → rename.
	tmp, err := os.CreateTemp(dir, "token*.tmp")
	if err != nil {
		return saveTokenResult{}, fmt.Errorf("creating temp token file: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := fmt.Fprint(tmp, token); err != nil {
		_ = tmp.Close()
		return saveTokenResult{}, fmt.Errorf("writing token: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return saveTokenResult{}, fmt.Errorf("closing temp token file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return saveTokenResult{}, fmt.Errorf("setting token file permissions: %w", err)
	}
	if err := os.Rename(tmpPath, tokenPath); err != nil {
		return saveTokenResult{}, fmt.Errorf("persisting token: %w", err)
	}
	committed = true

	return saveTokenResult{Written: true, Replaced: existing != ""}, nil
}

// readServerURL reads the persisted server URL from ~/.sourcebridge/server.
// Returns empty string if the file does not exist or cannot be read.
func readServerURL() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".sourcebridge", "server"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// saveServerURL writes the server URL to ~/.sourcebridge/server atomically
// with mode 0644. The parent directory is created with mode 0700 if absent.
func saveServerURL(serverURL string) error {
	dir, err := sourcebridgeDir()
	if err != nil {
		return err
	}
	serverPath := filepath.Join(dir, "server")

	tmp, err := os.CreateTemp(dir, "server*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp server file: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := fmt.Fprint(tmp, strings.TrimRight(serverURL, "/")); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing server URL: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp server file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("setting server file permissions: %w", err)
	}
	if err := os.Rename(tmpPath, serverPath); err != nil {
		return fmt.Errorf("persisting server URL: %w", err)
	}
	committed = true
	return nil
}

// tokenPrefix returns the first 16 characters of a token for display purposes.
// The full token is never printed.
func tokenPrefix(token string) string {
	const n = 16
	if len(token) <= n {
		return token
	}
	return token[:n] + "…"
}
