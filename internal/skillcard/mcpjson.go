// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package skillcard

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// MCPProxyEntry is the stdio-proxy entry written under mcpServers.sourcebridge.
// The file is safe to commit — it contains no secret. The proxy reads the
// token from ~/.sourcebridge/token at runtime.
//
// Shape:
//
//	{
//	  "command": "/Users/<you>/.local/bin/sourcebridge",
//	  "args": ["mcp-proxy", "--server", "https://my.sourcebridge.example"]
//	}
//
// `command` is the absolute path of the resolving sourcebridge binary so
// .mcp.json works regardless of GUI-launched Claude Code's PATH (codex r1 C3).
// We do NOT EvalSymlinks: on Homebrew the stable path is the bin/ symlink, and
// resolving it would write a versioned Cellar path that breaks on `brew upgrade`
// (codex r1b M1). The bare-string fallback `"sourcebridge"` is used only when
// resolution fails entirely (or via --portable-command).
//
// `--server` is embedded in args so the file is repo-self-contained — repo A
// and repo B can point at different SourceBridge servers (codex r1 C1).
type MCPProxyEntry struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// MCPHTTPEntry is the legacy HTTP-transport entry shipped before the proxy
// migration. Kept for backward-compatibility detection on existing installs.
type MCPHTTPEntry struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// mcpEndpointPath is the path appended to the server URL for the MCP HTTP endpoint.
const mcpEndpointPath = "/api/v1/mcp/http"

// tokenPlaceholder is the env-var reference used by the legacy HTTP entry.
// Kept here only so the legacy-exact match in classifyExistingEntry can compare
// against it.
const tokenPlaceholder = "${SOURCEBRIDGE_API_TOKEN}"

// resolveSourcebridgeCommand resolves the absolute path to the running
// sourcebridge binary. portable=true returns the bare string "sourcebridge"
// for committed/team-shared .mcp.json files.
//
// The persisted value is os.Executable()'s absolute result WITHOUT
// EvalSymlinks: on Homebrew that's /opt/homebrew/bin/sourcebridge (a symlink
// into a versioned Cellar path), and the symlink is what survives
// `brew upgrade`. EvalSymlinks would write the Cellar target and break the
// generated .mcp.json after every upgrade (codex r1b M1).
func resolveSourcebridgeCommand(portable bool) string {
	if portable {
		return "sourcebridge"
	}
	if exe, err := os.Executable(); err == nil {
		if filepath.IsAbs(exe) {
			return exe
		}
		// On rare systems (FreeBSD) os.Executable() may return a relative
		// path. Make it absolute, but do NOT EvalSymlinks.
		if abs, err := filepath.Abs(exe); err == nil {
			return abs
		}
	}
	// Fallback: search PATH. exec.LookPath returns the first hit, which on
	// Homebrew is the stable bin/ symlink — exactly the path we want to
	// persist.
	if found, err := exec.LookPath("sourcebridge"); err == nil {
		if abs, err := filepath.Abs(found); err == nil {
			return abs
		}
		return found
	}
	return "sourcebridge"
}

// MergeMCPJSON idempotently merges the SourceBridge MCP server entry into
// the .mcp.json file at path using the stdio-proxy schema.
//
// Migration matrix (decision c):
//   - No existing entry → write new proxy entry.
//   - Proxy shape, --server matches → idempotent. Command-path drift is
//     allowed: same --server, different command (e.g. user upgraded
//     sourcebridge to a new install location) silently rewrites the path.
//   - Proxy shape, --server differs → error unless force.
//   - Exact bulletproof HTTP shape (type=http, url matches, headers exactly
//     {"Authorization":"Bearer ${SOURCEBRIDGE_API_TOKEN}"}, no extra fields)
//     → silent migrate; backup written; one stderr note.
//   - HTTP, same URL but custom headers/extra fields → error unless force
//     (refuses to clobber a literal-token Authorization or custom auth).
//   - HTTP, different URL → error unless force.
//   - v1 broken stdio (command="sourcebridge", args[0]="mcp") → silent migrate
//     with backup.
//   - Other stdio (custom command) → error unless force.
//   - Invalid JSON → backup + write fresh entry.
//
// portable controls whether `command` is written as the absolute path of the
// running binary (default) or as the bare string "sourcebridge" (--portable-command).
func MergeMCPJSON(path, serverURL, repoID string, portable, force bool) (changed bool, warningMsg string, err error) {
	mcpURL := serverURL + mcpEndpointPath

	cmdPath := resolveSourcebridgeCommand(portable)
	newEntry := MCPProxyEntry{
		Command: cmdPath,
		Args:    []string{"mcp-proxy", "--server", serverURL},
	}

	existing, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return false, "", fmt.Errorf("reading %s: %w", path, readErr)
	}

	var doc map[string]json.RawMessage
	if len(existing) > 0 {
		if jsonErr := json.Unmarshal(existing, &doc); jsonErr != nil {
			// Invalid JSON: back up and start fresh.
			backupPath := path + ".sb-backup"
			_ = os.WriteFile(backupPath, existing, 0o644)
			warningMsg = fmt.Sprintf("Existing %s contained invalid JSON — backed up to %s and replaced.", path, backupPath)
			doc = nil
		}
	}

	if doc == nil {
		doc = make(map[string]json.RawMessage)
	}

	// Parse or create the mcpServers object.
	var mcpServers map[string]json.RawMessage
	if raw, ok := doc["mcpServers"]; ok {
		if jsonErr := json.Unmarshal(raw, &mcpServers); jsonErr != nil {
			mcpServers = make(map[string]json.RawMessage)
		}
	} else {
		mcpServers = make(map[string]json.RawMessage)
	}

	// Inspect the existing sourcebridge entry (if any).
	if raw, exists := mcpServers["sourcebridge"]; exists {
		var generic map[string]json.RawMessage
		if jsonErr := json.Unmarshal(raw, &generic); jsonErr == nil {
			switch classify := classifyExistingEntry(generic, mcpURL, serverURL); classify {
			case entryKindProxyMatch:
				// Already correct (same --server). Check whether the
				// command-path drifted (e.g. user upgraded install
				// location). If so, rewrite path silently. Otherwise
				// idempotent no-op.
				if existingCmd := stringField(generic, "command"); existingCmd == cmdPath {
					return false, warningMsg, nil
				}
				// fall through to rewrite (path drift).

			case entryKindProxyDifferentURL:
				if !force {
					existingURL := extractProxyServer(generic)
					return false, "", fmt.Errorf(
						"mcpServers.sourcebridge already targets %q. Pass --force to retarget to %q.",
						existingURL, serverURL,
					)
				}

			case entryKindHTTPLegacyExact:
				// Exact bulletproof shape — silent migrate.
				backupPath := timestampedBackupPath(path)
				_ = os.WriteFile(backupPath, existing, 0o644)
				fmt.Fprintln(os.Stderr, "migrated SourceBridge MCP entry to stdio proxy (no env-var setup needed)")

			case entryKindHTTPSameHostCustom:
				if !force {
					return false, "", fmt.Errorf(
						"your existing %s uses a custom Authorization header. "+
							"Pass --force to replace it with the proxy command "+
							"(your token must be saved to ~/.sourcebridge/token first). "+
							"The replaced entry will be backed up.",
						filepath.Base(path),
					)
				}
				backupPath := timestampedBackupPath(path)
				_ = os.WriteFile(backupPath, existing, 0o644)

			case entryKindHTTPDifferentURL:
				if !force {
					existingURL := stringField(generic, "url")
					return false, "", fmt.Errorf(
						"mcpServers.sourcebridge already points at %q. Pass --force to retarget to %q.",
						existingURL, mcpURL,
					)
				}

			case entryKindBrokenStdio:
				// v1-generated broken stdio shape — silently rewrite.
				backupPath := path + ".sb-backup"
				_ = os.WriteFile(backupPath, existing, 0o644)
				fmt.Fprintln(os.Stderr, "migrated old SourceBridge MCP entry to stdio proxy (cloud-compatible)")

			case entryKindOtherStdio:
				if !force {
					existingCmd := stringField(generic, "command")
					return false, "", fmt.Errorf(
						"mcpServers.sourcebridge already exists with a different command (%q). Pass --force to replace.",
						existingCmd,
					)
				}
			}
		}
	}

	// Write the SourceBridge proxy entry.
	entryJSON, marshalErr := json.Marshal(newEntry)
	if marshalErr != nil {
		return false, "", fmt.Errorf("marshaling MCP entry: %w", marshalErr)
	}
	mcpServers["sourcebridge"] = json.RawMessage(entryJSON)

	mcpServersJSON, marshalErr := json.Marshal(mcpServers)
	if marshalErr != nil {
		return false, "", fmt.Errorf("marshaling mcpServers: %w", marshalErr)
	}
	doc["mcpServers"] = json.RawMessage(mcpServersJSON)

	out, marshalErr := json.MarshalIndent(doc, "", "  ")
	if marshalErr != nil {
		return false, "", fmt.Errorf("marshaling .mcp.json: %w", marshalErr)
	}

	if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr != nil {
		return false, "", fmt.Errorf("creating directories for %s: %w", path, mkdirErr)
	}
	// 0644 intentional: file is safe to commit (no literal secret).
	if writeErr := os.WriteFile(path, append(out, '\n'), 0o644); writeErr != nil {
		return false, "", fmt.Errorf("writing %s: %w", path, writeErr)
	}
	return true, warningMsg, nil
}

// timestampedBackupPath returns path.sb-backup-<unix-epoch> so multiple
// migrations on the same file don't overwrite earlier backups.
func timestampedBackupPath(path string) string {
	return fmt.Sprintf("%s.sb-backup-%d", path, time.Now().Unix())
}

// entryKind describes how an existing sourcebridge entry should be treated
// (codex r1b M2: exhaustive ordered enum; the old entryKindHTTPMatch is gone
// because the proxy migration changes "same URL = idempotent" semantics).
type entryKind int

const (
	entryKindProxyMatch         entryKind = iota // proxy shape, --server matches → idempotent (or rewrite-path)
	entryKindProxyDifferentURL                   // proxy shape, --server differs → error unless --force
	entryKindHTTPLegacyExact                     // EXACT bulletproof shape → silent migrate
	entryKindHTTPSameHostCustom                  // type=http + same URL but custom headers/extra fields → error unless --force
	entryKindHTTPDifferentURL                    // type=http with a different URL → error unless --force
	entryKindBrokenStdio                         // command="sourcebridge" + args[0]="mcp" (v1) → silent migrate
	entryKindOtherStdio                          // user-configured stdio → error unless --force
)

// classifyExistingEntry inspects a raw sourcebridge entry map and returns
// how MergeMCPJSON should treat it. expectedMCPURL is `serverURL + mcpEndpointPath`;
// expectedServerURL is the bare server URL (used to compare proxy --server args).
//
// The classifier is exhaustive and order-sensitive — every input maps to
// exactly one kind. Order:
//  1. Proxy shape (command="sourcebridge"/<abs>/sourcebridge AND args[0]="mcp-proxy")
//     → ProxyMatch | ProxyDifferentURL.
//  2. v1 broken stdio (command="sourcebridge" AND args[0]="mcp") → BrokenStdio.
//  3. HTTP shape (type="http"):
//     - URL doesn't match → HTTPDifferentURL.
//     - URL matches AND headers exactly the placeholder shape → HTTPLegacyExact.
//     - URL matches but anything else is custom → HTTPSameHostCustom.
//  4. Anything else → OtherStdio.
func classifyExistingEntry(entry map[string]json.RawMessage, expectedMCPURL, expectedServerURL string) entryKind {
	// 1. Proxy shape.
	if isProxyShape(entry) {
		got := extractProxyServer(entry)
		if got == strings.TrimRight(expectedServerURL, "/") {
			return entryKindProxyMatch
		}
		return entryKindProxyDifferentURL
	}

	// 2. v1 broken stdio.
	if cmdRaw, ok := entry["command"]; ok {
		var cmd string
		if json.Unmarshal(cmdRaw, &cmd) == nil && isSourcebridgeCommand(cmd) {
			if argsRaw, ok := entry["args"]; ok {
				var args []string
				if json.Unmarshal(argsRaw, &args) == nil && len(args) > 0 && args[0] == "mcp" {
					return entryKindBrokenStdio
				}
			}
		}
	}

	// 3. HTTP shape.
	if typeRaw, ok := entry["type"]; ok {
		var entryType string
		if json.Unmarshal(typeRaw, &entryType) == nil && entryType == "http" {
			urlMatches := false
			if urlRaw, ok := entry["url"]; ok {
				var entryURL string
				if json.Unmarshal(urlRaw, &entryURL) == nil && entryURL == expectedMCPURL {
					urlMatches = true
				}
			}
			if !urlMatches {
				return entryKindHTTPDifferentURL
			}
			// URL matches — check whether this is the EXACT bulletproof
			// shape (type, url, headers — and headers is exactly
			// {"Authorization":"Bearer ${SOURCEBRIDGE_API_TOKEN}"} with
			// no extras).
			if isLegacyHTTPExactShape(entry) {
				return entryKindHTTPLegacyExact
			}
			return entryKindHTTPSameHostCustom
		}
	}

	// 4. Anything else.
	return entryKindOtherStdio
}

// isProxyShape returns true iff entry is a stdio-proxy entry. The strongest
// signal is args[0] == "mcp-proxy" — `mcp-proxy` is a SourceBridge-specific
// subcommand, so any entry whose first arg is that string is one we wrote
// (or a hand-rolled equivalent), regardless of how `command` is spelled.
//
// We additionally require `command` to be either:
//   - "sourcebridge", or
//   - any path whose basename is "sourcebridge"/"sourcebridge.exe", or
//   - the path of the running binary (so test binaries that re-invoke
//     mcp-proxy via `go test` are recognized).
//
// The third clause is intentionally lenient: a test harness running `go test`
// produces an entry whose `command` is the test binary path (e.g. cli.test)
// not "sourcebridge". The args array's "mcp-proxy" + "--server" combination
// is enough to identify the entry as proxy-shape; we don't need to gate on
// the command spelling.
func isProxyShape(entry map[string]json.RawMessage) bool {
	argsRaw, ok := entry["args"]
	if !ok {
		return false
	}
	var args []string
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return false
	}
	if len(args) == 0 || args[0] != "mcp-proxy" {
		return false
	}
	// args[0]=="mcp-proxy" is the strong signal. The command field must be
	// non-empty (every shell-spawn config has a command) but we don't require
	// the basename to spell "sourcebridge" — that would falsely classify a
	// test-harness entry as not-proxy.
	return strings.TrimSpace(stringField(entry, "command")) != ""
}

// isSourcebridgeCommand returns true for "sourcebridge" or any absolute path
// ending in /sourcebridge (.exe on Windows tolerated).
func isSourcebridgeCommand(cmd string) bool {
	if cmd == "sourcebridge" {
		return true
	}
	base := filepath.Base(cmd)
	return base == "sourcebridge" || base == "sourcebridge.exe"
}

// extractProxyServer returns the value of the --server arg in a proxy entry,
// or "" if absent. Tolerates `--server <url>` and `--server=<url>`.
func extractProxyServer(entry map[string]json.RawMessage) string {
	argsRaw, ok := entry["args"]
	if !ok {
		return ""
	}
	var args []string
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return ""
	}
	for i, a := range args {
		if a == "--server" && i+1 < len(args) {
			return strings.TrimRight(args[i+1], "/")
		}
		if strings.HasPrefix(a, "--server=") {
			return strings.TrimRight(strings.TrimPrefix(a, "--server="), "/")
		}
	}
	return ""
}

// isLegacyHTTPExactShape returns true iff entry is exactly the bulletproof
// HTTP shape we used to write — no extra top-level fields, headers exactly
// {"Authorization":"Bearer ${SOURCEBRIDGE_API_TOKEN}"}.
//
// Exact-match is required (codex r1 H3): a relaxed match would silently
// migrate user-edited entries with literal tokens or custom auth headers,
// breaking their auth.
func isLegacyHTTPExactShape(entry map[string]json.RawMessage) bool {
	// Only allow type, url, headers — no extra top-level keys.
	allowed := map[string]bool{"type": true, "url": true, "headers": true}
	for k := range entry {
		if !allowed[k] {
			return false
		}
	}
	headersRaw, ok := entry["headers"]
	if !ok {
		return false
	}
	var headers map[string]string
	if err := json.Unmarshal(headersRaw, &headers); err != nil {
		return false
	}
	if len(headers) != 1 {
		return false
	}
	auth, ok := headers["Authorization"]
	if !ok {
		return false
	}
	return auth == "Bearer "+tokenPlaceholder
}

// stringField returns the string value of entry[key], or "" if absent or
// not a JSON string.
func stringField(entry map[string]json.RawMessage, key string) string {
	raw, ok := entry[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// MCPExpectedURL returns the MCP HTTP endpoint URL for a given server URL.
// Used by dryRunMCPTag to detect the correct entry shape without re-importing
// this package's constants.
func MCPExpectedURL(serverURL string) string {
	return serverURL + mcpEndpointPath
}
