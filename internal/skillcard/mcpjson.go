// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package skillcard

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// MCPHTTPEntry is the HTTP-transport entry written under mcpServers.sourcebridge.
// The file is safe to commit — it contains no literal secret; the token is
// referenced via the ${SOURCEBRIDGE_API_TOKEN} env var that Claude Code expands
// at startup.
type MCPHTTPEntry struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// mcpEndpointPath is the path appended to the server URL for the MCP HTTP endpoint.
const mcpEndpointPath = "/api/v1/mcp/http"

// tokenPlaceholder is the env-var reference Claude Code interpolates at startup.
const tokenPlaceholder = "${SOURCEBRIDGE_API_TOKEN}"

// MergeMCPJSON idempotently merges the SourceBridge MCP server entry into
// the .mcp.json file at path using the HTTP-transport schema.
//
// Merge rules (decision d from the plan):
//   - No existing mcpServers.sourcebridge → write new HTTP entry.
//   - Old broken stdio shape (command=="sourcebridge" AND args[0]=="mcp") →
//     silently rewrite to HTTP shape; write .sb-backup before rewriting.
//   - Different stdio shape (any other command value) → error unless force.
//   - New HTTP shape, URL matches → idempotent no-op (return changed=false).
//   - New HTTP shape, URL differs → error unless force.
//   - Invalid JSON → back up to .sb-backup, write fresh file, set warningMsg.
//
// 0644 is intentional: file is safe to commit (no literal secret; token is
// env-var reference).
func MergeMCPJSON(path, serverURL, repoID string, force bool) (changed bool, warningMsg string, err error) {
	mcpURL := serverURL + mcpEndpointPath

	newEntry := MCPHTTPEntry{
		Type: "http",
		URL:  mcpURL,
		Headers: map[string]string{
			"Authorization": "Bearer " + tokenPlaceholder,
		},
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
			switch classify := classifyExistingEntry(generic, mcpURL); classify {
			case entryKindHTTPMatch:
				// Already correct — idempotent no-op.
				return false, warningMsg, nil

			case entryKindHTTPDifferentURL:
				if !force {
					var otherURL string
					if u, ok := generic["url"]; ok {
						_ = json.Unmarshal(u, &otherURL)
					}
					return false, "", fmt.Errorf(
						"mcpServers.sourcebridge already points at %q. Pass --force to retarget to %q.",
						otherURL, mcpURL,
					)
				}
				// force=true: fall through to rewrite below.

			case entryKindBrokenStdio:
				// v1-generated broken stdio shape — silently rewrite.
				backupPath := path + ".sb-backup"
				_ = os.WriteFile(backupPath, existing, 0o644)
				fmt.Fprintln(os.Stderr, "migrated old SourceBridge MCP entry to HTTP transport (cloud-compatible)")
				// Fall through to rewrite below.

			case entryKindOtherStdio:
				if !force {
					var otherCmd string
					if c, ok := generic["command"]; ok {
						_ = json.Unmarshal(c, &otherCmd)
					}
					return false, "", fmt.Errorf(
						"mcpServers.sourcebridge already exists with a different command (%q). Pass --force to replace.",
						otherCmd,
					)
				}
				// force=true: fall through to rewrite below.
			}
		}
	}

	// Write the SourceBridge HTTP entry.
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
	// 0644 intentional: file is safe to commit (no literal secret; token is env-var reference).
	if writeErr := os.WriteFile(path, append(out, '\n'), 0o644); writeErr != nil {
		return false, "", fmt.Errorf("writing %s: %w", path, writeErr)
	}
	return true, warningMsg, nil
}

// entryKind describes how an existing sourcebridge entry should be treated.
type entryKind int

const (
	entryKindHTTPMatch      entryKind = iota // HTTP, URL matches → idempotent
	entryKindHTTPDifferentURL                // HTTP, different URL → error unless --force
	entryKindBrokenStdio                     // v1 broken: command==sourcebridge + args[0]==mcp
	entryKindOtherStdio                      // user-configured stdio → error unless --force
)

// classifyExistingEntry inspects a raw sourcebridge entry map and returns
// how MergeMCPJSON should treat it.
func classifyExistingEntry(entry map[string]json.RawMessage, expectedURL string) entryKind {
	// Check for HTTP-transport shape first (has "type" field).
	if typeRaw, ok := entry["type"]; ok {
		var entryType string
		if json.Unmarshal(typeRaw, &entryType) == nil && entryType == "http" {
			// It's an HTTP entry — check whether the URL matches.
			if urlRaw, ok := entry["url"]; ok {
				var entryURL string
				if json.Unmarshal(urlRaw, &entryURL) == nil {
					if entryURL == expectedURL {
						return entryKindHTTPMatch
					}
					return entryKindHTTPDifferentURL
				}
			}
			// HTTP type but no URL — treat as different URL.
			return entryKindHTTPDifferentURL
		}
	}

	// Check for the specific broken v1 stdio shape:
	// command == "sourcebridge" AND args[0] == "mcp"
	if cmdRaw, ok := entry["command"]; ok {
		var cmd string
		if json.Unmarshal(cmdRaw, &cmd) == nil && cmd == "sourcebridge" {
			if argsRaw, ok := entry["args"]; ok {
				var args []string
				if json.Unmarshal(argsRaw, &args) == nil && len(args) > 0 && args[0] == "mcp" {
					return entryKindBrokenStdio
				}
			}
		}
	}

	// Any other shape (user-configured stdio, mcp-remote wrapper, etc.).
	return entryKindOtherStdio
}

// MCPExpectedURL returns the MCP HTTP endpoint URL for a given server URL.
// Used by dryRunMCPTag to detect the correct entry shape without re-importing
// this package's constants.
func MCPExpectedURL(serverURL string) string {
	return serverURL + mcpEndpointPath
}
