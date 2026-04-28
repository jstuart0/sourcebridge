// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package skillcard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testServerURL = "https://test-cloud.example"
	testRepoID    = "abc123"
	testMCPURL    = testServerURL + mcpEndpointPath
)

// readMCPJSON is a helper that reads and parses a .mcp.json file.
func readMCPJSON(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return doc
}

// assertHTTPEntry verifies that doc["mcpServers"]["sourcebridge"] has the
// expected HTTP-transport shape with the correct URL and token placeholder.
func assertHTTPEntry(t *testing.T, doc map[string]interface{}, expectedURL string) {
	t.Helper()
	servers, ok := doc["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatalf("mcpServers not a map: %T", doc["mcpServers"])
	}
	entry, ok := servers["sourcebridge"].(map[string]interface{})
	if !ok {
		t.Fatalf("sourcebridge not a map: %T", servers["sourcebridge"])
	}
	if got := entry["type"]; got != "http" {
		t.Errorf("entry[type] = %q, want %q", got, "http")
	}
	if got := entry["url"]; got != expectedURL {
		t.Errorf("entry[url] = %q, want %q", got, expectedURL)
	}
	headers, ok := entry["headers"].(map[string]interface{})
	if !ok {
		t.Fatalf("entry[headers] not a map: %T", entry["headers"])
	}
	wantAuth := "Bearer " + tokenPlaceholder
	if got := headers["Authorization"]; got != wantAuth {
		t.Errorf("headers[Authorization] = %q, want %q", got, wantAuth)
	}
}

// TestMergeMCPJSON_FreshInstall verifies that a fresh write creates the HTTP entry.
func TestMergeMCPJSON_FreshInstall(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	changed, warn, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true for fresh install")
	}
	if warn != "" {
		t.Errorf("unexpected warning: %q", warn)
	}

	doc := readMCPJSON(t, mcpPath)
	assertHTTPEntry(t, doc, testMCPURL)
}

// TestMergeMCPJSON_Idempotent verifies that a second run on a correct HTTP entry
// returns changed=false and leaves the file byte-for-byte unchanged.
func TestMergeMCPJSON_Idempotent(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	// First write.
	_, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, false)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	before, _ := os.ReadFile(mcpPath)

	// Second write — should be a no-op.
	changed, warn, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, false)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if changed {
		t.Error("expected changed=false on idempotent re-run")
	}
	if warn != "" {
		t.Errorf("unexpected warning on idempotent run: %q", warn)
	}

	after, _ := os.ReadFile(mcpPath)
	if string(before) != string(after) {
		t.Errorf("file changed on idempotent run:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestMergeMCPJSON_MigratesBrokenStdio verifies that the exact v1 broken stdio
// shape is silently rewritten to the HTTP shape without requiring --force.
func TestMergeMCPJSON_MigratesBrokenStdio(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	// Write the broken v1 shape.
	brokenContent := `{"mcpServers":{"sourcebridge":{"command":"sourcebridge","args":["mcp","--repo-id","abc"]}}}`
	if err := os.WriteFile(mcpPath, []byte(brokenContent), 0o644); err != nil {
		t.Fatalf("writing broken file: %v", err)
	}

	changed, warn, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, false)
	if err != nil {
		t.Fatalf("unexpected error migrating broken stdio: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when migrating broken stdio entry")
	}
	if warn != "" {
		t.Errorf("unexpected warning: %q (migration message goes to stderr, not warningMsg)", warn)
	}

	doc := readMCPJSON(t, mcpPath)
	assertHTTPEntry(t, doc, testMCPURL)

	// A backup should have been written.
	backupPath := mcpPath + ".sb-backup"
	if _, err := os.Stat(backupPath); err != nil {
		t.Errorf("expected .sb-backup to exist after migration: %v", err)
	}
}

// TestMergeMCPJSON_OtherStdio_NoForce verifies that a user-customized stdio
// entry aborts without --force.
func TestMergeMCPJSON_OtherStdio_NoForce(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	customContent := `{"mcpServers":{"sourcebridge":{"command":"/usr/local/bin/mcp-remote","args":["https://my.sourcebridge.example/mcp"]}}}`
	if err := os.WriteFile(mcpPath, []byte(customContent), 0o644); err != nil {
		t.Fatalf("writing custom file: %v", err)
	}

	_, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, false)
	if err == nil {
		t.Fatal("expected error for user-customized stdio entry without --force")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention --force; got: %v", err)
	}

	// File should be untouched.
	data, _ := os.ReadFile(mcpPath)
	if string(data) != customContent {
		t.Error("file should be unchanged after abort")
	}
}

// TestMergeMCPJSON_OtherStdio_Force verifies that --force replaces a user-
// customized stdio entry.
func TestMergeMCPJSON_OtherStdio_Force(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	customContent := `{"mcpServers":{"sourcebridge":{"command":"/usr/local/bin/mcp-remote","args":["https://my.sourcebridge.example/mcp"]}}}`
	if err := os.WriteFile(mcpPath, []byte(customContent), 0o644); err != nil {
		t.Fatalf("writing custom file: %v", err)
	}

	changed, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, true)
	if err != nil {
		t.Fatalf("unexpected error with --force: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when --force replaces an entry")
	}

	doc := readMCPJSON(t, mcpPath)
	assertHTTPEntry(t, doc, testMCPURL)
}

// TestMergeMCPJSON_HTTPDifferentURL_NoForce verifies that a correct HTTP entry
// pointing at a different server aborts without --force.
func TestMergeMCPJSON_HTTPDifferentURL_NoForce(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	otherURL := "https://other-cloud.example" + mcpEndpointPath
	existingContent := `{"mcpServers":{"sourcebridge":{"type":"http","url":"` + otherURL + `","headers":{"Authorization":"Bearer ${SOURCEBRIDGE_API_TOKEN}"}}}}`
	if err := os.WriteFile(mcpPath, []byte(existingContent), 0o644); err != nil {
		t.Fatalf("writing existing file: %v", err)
	}

	_, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, false)
	if err == nil {
		t.Fatal("expected error for different URL without --force")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention --force; got: %v", err)
	}
	if !strings.Contains(err.Error(), "other-cloud.example") {
		t.Errorf("error should mention the existing URL; got: %v", err)
	}

	// File should be untouched.
	data, _ := os.ReadFile(mcpPath)
	if string(data) != existingContent {
		t.Error("file should be unchanged after abort")
	}
}

// TestMergeMCPJSON_HTTPDifferentURL_Force verifies that --force retargets to the
// new URL.
func TestMergeMCPJSON_HTTPDifferentURL_Force(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	otherURL := "https://other-cloud.example" + mcpEndpointPath
	existingContent := `{"mcpServers":{"sourcebridge":{"type":"http","url":"` + otherURL + `","headers":{"Authorization":"Bearer ${SOURCEBRIDGE_API_TOKEN}"}}}}`
	if err := os.WriteFile(mcpPath, []byte(existingContent), 0o644); err != nil {
		t.Fatalf("writing existing file: %v", err)
	}

	changed, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, true)
	if err != nil {
		t.Fatalf("unexpected error with --force: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when --force retargets URL")
	}

	doc := readMCPJSON(t, mcpPath)
	assertHTTPEntry(t, doc, testMCPURL)
}

// TestMergeMCPJSON_InvalidJSON verifies that invalid JSON is backed up and
// replaced with a fresh HTTP entry.
func TestMergeMCPJSON_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	if err := os.WriteFile(mcpPath, []byte(`{invalid json`), 0o644); err != nil {
		t.Fatalf("writing invalid file: %v", err)
	}

	changed, warn, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, false)
	if err != nil {
		t.Fatalf("unexpected error for invalid JSON: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when replacing invalid JSON")
	}
	if !strings.Contains(warn, ".sb-backup") {
		t.Errorf("warning should mention .sb-backup; got: %q", warn)
	}

	doc := readMCPJSON(t, mcpPath)
	assertHTTPEntry(t, doc, testMCPURL)

	// Backup should exist.
	if _, err := os.Stat(mcpPath + ".sb-backup"); err != nil {
		t.Errorf("expected .sb-backup to exist: %v", err)
	}
}

// TestMergeMCPJSON_PreservesForeignEntries verifies that other MCP servers in
// the same file are not disturbed when the sourcebridge entry is written.
func TestMergeMCPJSON_PreservesForeignEntries(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	// A file with a foreign entry and the broken sourcebridge entry.
	initial := `{
  "mcpServers": {
    "my-other-server": {
      "type": "http",
      "url": "https://other.example/mcp"
    },
    "sourcebridge": {
      "command": "sourcebridge",
      "args": ["mcp", "--repo-id", "abc"]
    }
  }
}`
	if err := os.WriteFile(mcpPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("writing initial file: %v", err)
	}

	changed, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true for migration")
	}

	doc := readMCPJSON(t, mcpPath)

	// Foreign entry must be preserved.
	servers, ok := doc["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatalf("mcpServers not a map")
	}
	other, ok := servers["my-other-server"].(map[string]interface{})
	if !ok {
		t.Fatalf("my-other-server entry missing or wrong type")
	}
	if other["url"] != "https://other.example/mcp" {
		t.Errorf("foreign entry url changed: %v", other["url"])
	}

	// SourceBridge entry must be the new HTTP shape.
	assertHTTPEntry(t, doc, testMCPURL)
}

// TestMergeMCPJSON_FileInSubdir verifies that directories are created as needed.
func TestMergeMCPJSON_FileInSubdir(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, "nested", "dir", ".mcp.json")

	changed, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, false)
	if err != nil {
		t.Fatalf("unexpected error creating nested path: %v", err)
	}
	if !changed {
		t.Error("expected changed=true")
	}

	doc := readMCPJSON(t, mcpPath)
	assertHTTPEntry(t, doc, testMCPURL)
}

// TestMCPExpectedURL verifies that MCPExpectedURL appends the correct path.
func TestMCPExpectedURL(t *testing.T) {
	got := MCPExpectedURL("https://example.com")
	want := "https://example.com/api/v1/mcp/http"
	if got != want {
		t.Errorf("MCPExpectedURL = %q, want %q", got, want)
	}
}

// TestClassifyExistingEntry exercises all classification branches directly.
func TestClassifyExistingEntry(t *testing.T) {
	cases := []struct {
		name        string
		json        string
		expectedURL string
		want        entryKind
	}{
		{
			name:        "HTTP match",
			json:        `{"type":"http","url":"https://example.com/api/v1/mcp/http"}`,
			expectedURL: "https://example.com/api/v1/mcp/http",
			want:        entryKindHTTPMatch,
		},
		{
			name:        "HTTP different URL",
			json:        `{"type":"http","url":"https://other.example/api/v1/mcp/http"}`,
			expectedURL: "https://example.com/api/v1/mcp/http",
			want:        entryKindHTTPDifferentURL,
		},
		{
			name:        "HTTP no URL",
			json:        `{"type":"http"}`,
			expectedURL: "https://example.com/api/v1/mcp/http",
			want:        entryKindHTTPDifferentURL,
		},
		{
			name:        "broken stdio v1 exact",
			json:        `{"command":"sourcebridge","args":["mcp","--repo-id","abc"]}`,
			expectedURL: "https://example.com/api/v1/mcp/http",
			want:        entryKindBrokenStdio,
		},
		{
			name:        "broken stdio v1 minimal args",
			json:        `{"command":"sourcebridge","args":["mcp"]}`,
			expectedURL: "https://example.com/api/v1/mcp/http",
			want:        entryKindBrokenStdio,
		},
		{
			name:        "other stdio: custom command",
			json:        `{"command":"/usr/local/bin/mcp-remote","args":["https://example.com/mcp"]}`,
			expectedURL: "https://example.com/api/v1/mcp/http",
			want:        entryKindOtherStdio,
		},
		{
			name:        "other stdio: sourcebridge but wrong first arg",
			json:        `{"command":"sourcebridge","args":["serve"]}`,
			expectedURL: "https://example.com/api/v1/mcp/http",
			want:        entryKindOtherStdio,
		},
		{
			name:        "other stdio: sourcebridge no args",
			json:        `{"command":"sourcebridge"}`,
			expectedURL: "https://example.com/api/v1/mcp/http",
			want:        entryKindOtherStdio,
		},
		{
			name:        "empty entry",
			json:        `{}`,
			expectedURL: "https://example.com/api/v1/mcp/http",
			want:        entryKindOtherStdio,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var entry map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.json), &entry); err != nil {
				t.Fatalf("parsing test case json: %v", err)
			}
			got := classifyExistingEntry(entry, tc.expectedURL)
			if got != tc.want {
				t.Errorf("classifyExistingEntry = %v, want %v", got, tc.want)
			}
		})
	}
}
