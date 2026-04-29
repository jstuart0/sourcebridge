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

// assertProxyEntry verifies that doc["mcpServers"]["sourcebridge"] has the
// expected stdio-proxy shape with the correct --server arg.
func assertProxyEntry(t *testing.T, doc map[string]interface{}, expectedServer string) {
	t.Helper()
	servers, ok := doc["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatalf("mcpServers not a map: %T", doc["mcpServers"])
	}
	entry, ok := servers["sourcebridge"].(map[string]interface{})
	if !ok {
		t.Fatalf("sourcebridge not a map: %T", servers["sourcebridge"])
	}
	cmd, _ := entry["command"].(string)
	// command should be either "sourcebridge" (portable) or any non-empty
	// absolute path. Under `go test` os.Executable() returns the test
	// binary's path (e.g. .../skillcard.test) so the basename test was
	// over-strict — accept any absolute path here.
	if cmd == "" {
		t.Errorf("entry.command is empty")
	}
	if cmd != "sourcebridge" && !filepath.IsAbs(cmd) {
		t.Errorf("entry.command = %q; expected \"sourcebridge\" or absolute path", cmd)
	}
	args, ok := entry["args"].([]interface{})
	if !ok || len(args) < 3 {
		t.Fatalf("entry.args missing or wrong type: %+v", entry["args"])
	}
	if got, _ := args[0].(string); got != "mcp-proxy" {
		t.Errorf("entry.args[0] = %v; want mcp-proxy", args[0])
	}
	if got, _ := args[1].(string); got != "--server" {
		t.Errorf("entry.args[1] = %v; want --server", args[1])
	}
	if got, _ := args[2].(string); got != expectedServer {
		t.Errorf("entry.args[2] = %v; want %q", args[2], expectedServer)
	}
}

// TestMergeMCPJSON_FreshInstall_AbsolutePath verifies that a fresh write
// produces the proxy shape with an absolute command path.
func TestMergeMCPJSON_FreshInstall_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	changed, warn, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, false /*portable*/, false /*force*/)
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
	assertProxyEntry(t, doc, testServerURL)

	// Absolute-path mode: command should be the test binary's path
	// (which is absolute under `go test`).
	servers := doc["mcpServers"].(map[string]interface{})
	entry := servers["sourcebridge"].(map[string]interface{})
	cmd := entry["command"].(string)
	if !filepath.IsAbs(cmd) {
		t.Errorf("expected absolute command path, got %q", cmd)
	}
}

// TestMergeMCPJSON_FreshInstall_PortableCommand verifies that --portable-command
// writes the bare string "sourcebridge".
func TestMergeMCPJSON_FreshInstall_PortableCommand(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	_, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, true /*portable*/, false /*force*/)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	doc := readMCPJSON(t, mcpPath)
	servers := doc["mcpServers"].(map[string]interface{})
	entry := servers["sourcebridge"].(map[string]interface{})
	if got := entry["command"]; got != "sourcebridge" {
		t.Errorf("portable command = %q; want \"sourcebridge\"", got)
	}
}

// TestMergeMCPJSON_IdempotentOnProxyShape verifies that a second run on the
// proxy shape with matching --server is a no-op.
func TestMergeMCPJSON_IdempotentOnProxyShape(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	if _, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, true, false); err != nil {
		t.Fatalf("first write: %v", err)
	}
	before, _ := os.ReadFile(mcpPath)

	changed, warn, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, true, false)
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

// TestMergeMCPJSON_RewritesCommandPathDrift verifies that an entry with the
// same --server but a different `command` (e.g. user upgraded sourcebridge to
// a new install location) is silently rewritten without --force.
func TestMergeMCPJSON_RewritesCommandPathDrift(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	// Pre-populate with an old install path.
	oldEntry := `{"mcpServers":{"sourcebridge":{"command":"/old/path/sourcebridge","args":["mcp-proxy","--server","` + testServerURL + `"]}}}`
	if err := os.WriteFile(mcpPath, []byte(oldEntry), 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	changed, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, false, false)
	if err != nil {
		t.Fatalf("unexpected error rewriting path drift: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when rewriting drifted command path")
	}

	doc := readMCPJSON(t, mcpPath)
	servers := doc["mcpServers"].(map[string]interface{})
	entry := servers["sourcebridge"].(map[string]interface{})
	cmd, _ := entry["command"].(string)
	if cmd == "/old/path/sourcebridge" {
		t.Error("command path was not rewritten")
	}
}

// TestMergeMCPJSON_DifferentProxyServer verifies that an existing proxy entry
// with a different --server aborts without --force.
func TestMergeMCPJSON_DifferentProxyServer(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	otherEntry := `{"mcpServers":{"sourcebridge":{"command":"sourcebridge","args":["mcp-proxy","--server","https://other-cloud.example"]}}}`
	if err := os.WriteFile(mcpPath, []byte(otherEntry), 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	_, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, true, false /*no force*/)
	if err == nil {
		t.Fatal("expected error for different proxy --server without --force")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention --force; got: %v", err)
	}
	if !strings.Contains(err.Error(), "other-cloud.example") {
		t.Errorf("error should mention existing server URL; got: %v", err)
	}
}

// TestMergeMCPJSON_DifferentProxyServer_Force verifies that --force retargets.
func TestMergeMCPJSON_DifferentProxyServer_Force(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	otherEntry := `{"mcpServers":{"sourcebridge":{"command":"sourcebridge","args":["mcp-proxy","--server","https://other-cloud.example"]}}}`
	if err := os.WriteFile(mcpPath, []byte(otherEntry), 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	changed, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, true, true /*force*/)
	if err != nil {
		t.Fatalf("unexpected error with --force: %v", err)
	}
	if !changed {
		t.Error("expected changed=true with --force retarget")
	}
	doc := readMCPJSON(t, mcpPath)
	assertProxyEntry(t, doc, testServerURL)
}

// TestMergeMCPJSON_MigratesFromBulletproofHTTP_Exact verifies the exact
// bulletproof HTTP shape is silently migrated to the proxy shape (codex r1 H3).
func TestMergeMCPJSON_MigratesFromBulletproofHTTP_Exact(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	exactBulletproof := `{"mcpServers":{"sourcebridge":{"type":"http","url":"` + testMCPURL + `","headers":{"Authorization":"Bearer ${SOURCEBRIDGE_API_TOKEN}"}}}}`
	if err := os.WriteFile(mcpPath, []byte(exactBulletproof), 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	changed, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, true, false /*no force*/)
	if err != nil {
		t.Fatalf("expected silent migrate, got error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true on legacy migration")
	}

	doc := readMCPJSON(t, mcpPath)
	assertProxyEntry(t, doc, testServerURL)

	// Backup file with timestamp suffix.
	matches, _ := filepath.Glob(mcpPath + ".sb-backup-*")
	if len(matches) == 0 {
		t.Errorf("expected timestamped backup file; none found")
	}
}

// TestMergeMCPJSON_RefusesHTTPSameHostCustomHeader verifies that HTTP shape
// with a custom Authorization (literal token) requires --force (codex r1 H3).
func TestMergeMCPJSON_RefusesHTTPSameHostCustomHeader(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	customAuth := `{"mcpServers":{"sourcebridge":{"type":"http","url":"` + testMCPURL + `","headers":{"Authorization":"Bearer ca_real_literal_token_xyz"}}}}`
	if err := os.WriteFile(mcpPath, []byte(customAuth), 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	_, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, true, false)
	if err == nil {
		t.Fatal("expected error for custom Authorization header without --force")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention --force; got: %v", err)
	}

	// File should be untouched.
	data, _ := os.ReadFile(mcpPath)
	if string(data) != customAuth {
		t.Error("file should be unchanged after abort")
	}
}

// TestMergeMCPJSON_RefusesHTTPSameHostExtraField verifies that any extra
// top-level field in the HTTP entry blocks silent migration.
func TestMergeMCPJSON_RefusesHTTPSameHostExtraField(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	withExtra := `{"mcpServers":{"sourcebridge":{"type":"http","url":"` + testMCPURL + `","headers":{"Authorization":"Bearer ${SOURCEBRIDGE_API_TOKEN}"},"extra_field":"do_not_clobber"}}}`
	if err := os.WriteFile(mcpPath, []byte(withExtra), 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	_, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, true, false)
	if err == nil {
		t.Fatal("expected error for entry with extra top-level field without --force")
	}
}

// TestMergeMCPJSON_MigratesFromBrokenStdio_StillWorks verifies that the v1
// broken-stdio detection still fires and lands on the proxy shape.
func TestMergeMCPJSON_MigratesFromBrokenStdio_StillWorks(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	broken := `{"mcpServers":{"sourcebridge":{"command":"sourcebridge","args":["mcp","--repo-id","abc"]}}}`
	if err := os.WriteFile(mcpPath, []byte(broken), 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	changed, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, true, false)
	if err != nil {
		t.Fatalf("expected silent migrate, got: %v", err)
	}
	if !changed {
		t.Error("expected changed=true")
	}

	doc := readMCPJSON(t, mcpPath)
	assertProxyEntry(t, doc, testServerURL)

	// Backup written.
	if _, err := os.Stat(mcpPath + ".sb-backup"); err != nil {
		t.Errorf("expected .sb-backup; got: %v", err)
	}
}

// TestMergeMCPJSON_DifferentURLStillErrors verifies the existing
// HTTPDifferentURL behavior (different host, HTTP shape) is preserved.
func TestMergeMCPJSON_DifferentURLStillErrors(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	differentURL := `{"mcpServers":{"sourcebridge":{"type":"http","url":"https://other-cloud.example/api/v1/mcp/http","headers":{"Authorization":"Bearer ${SOURCEBRIDGE_API_TOKEN}"}}}}`
	if err := os.WriteFile(mcpPath, []byte(differentURL), 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	_, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, true, false)
	if err == nil {
		t.Fatal("expected error for different URL without --force")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention --force; got: %v", err)
	}
}

// TestMergeMCPJSON_OtherStdioStillErrors verifies that user-customized stdio
// entries (mcp-remote wrapper, etc.) still require --force.
func TestMergeMCPJSON_OtherStdioStillErrors(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	custom := `{"mcpServers":{"sourcebridge":{"command":"/usr/local/bin/mcp-remote","args":["https://my.sourcebridge.example/mcp"]}}}`
	if err := os.WriteFile(mcpPath, []byte(custom), 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	_, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, true, false)
	if err == nil {
		t.Fatal("expected error for custom stdio command without --force")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention --force; got: %v", err)
	}
}

// TestMergeMCPJSON_InvalidJSON verifies that invalid JSON is backed up and
// replaced with a fresh proxy entry (existing behavior).
func TestMergeMCPJSON_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	if err := os.WriteFile(mcpPath, []byte(`{invalid json`), 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	changed, warn, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true")
	}
	if !strings.Contains(warn, ".sb-backup") {
		t.Errorf("warn should mention .sb-backup; got: %q", warn)
	}

	doc := readMCPJSON(t, mcpPath)
	assertProxyEntry(t, doc, testServerURL)
}

// TestMergeMCPJSON_PreservesForeignEntries verifies that other MCP servers
// in the same file are not disturbed.
func TestMergeMCPJSON_PreservesForeignEntries(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

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
		t.Fatalf("seeding: %v", err)
	}

	_, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	doc := readMCPJSON(t, mcpPath)
	servers := doc["mcpServers"].(map[string]interface{})
	other := servers["my-other-server"].(map[string]interface{})
	if other["url"] != "https://other.example/mcp" {
		t.Errorf("foreign entry url changed: %v", other["url"])
	}
	assertProxyEntry(t, doc, testServerURL)
}

// TestMergeMCPJSON_FileInSubdir verifies directory creation as needed.
func TestMergeMCPJSON_FileInSubdir(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, "nested", "dir", ".mcp.json")

	_, _, err := MergeMCPJSON(mcpPath, testServerURL, testRepoID, true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	doc := readMCPJSON(t, mcpPath)
	assertProxyEntry(t, doc, testServerURL)
}

// TestMCPExpectedURL is preserved.
func TestMCPExpectedURL(t *testing.T) {
	got := MCPExpectedURL("https://example.com")
	want := "https://example.com/api/v1/mcp/http"
	if got != want {
		t.Errorf("MCPExpectedURL = %q, want %q", got, want)
	}
}

// TestClassifyExistingEntry exercises every classification branch.
func TestClassifyExistingEntry(t *testing.T) {
	const expServer = "https://example.com"
	const expMCP = expServer + mcpEndpointPath

	cases := []struct {
		name string
		json string
		want entryKind
	}{
		{
			name: "proxy shape, --server matches",
			json: `{"command":"sourcebridge","args":["mcp-proxy","--server","https://example.com"]}`,
			want: entryKindProxyMatch,
		},
		{
			name: "proxy shape with absolute path, --server matches",
			json: `{"command":"/usr/local/bin/sourcebridge","args":["mcp-proxy","--server","https://example.com"]}`,
			want: entryKindProxyMatch,
		},
		{
			name: "proxy shape, --server differs",
			json: `{"command":"sourcebridge","args":["mcp-proxy","--server","https://other.example"]}`,
			want: entryKindProxyDifferentURL,
		},
		{
			name: "proxy shape, --server=foo equals form",
			json: `{"command":"sourcebridge","args":["mcp-proxy","--server=https://example.com"]}`,
			want: entryKindProxyMatch,
		},
		{
			name: "v1 broken stdio",
			json: `{"command":"sourcebridge","args":["mcp","--repo-id","abc"]}`,
			want: entryKindBrokenStdio,
		},
		{
			name: "exact bulletproof HTTP shape",
			json: `{"type":"http","url":"https://example.com/api/v1/mcp/http","headers":{"Authorization":"Bearer ${SOURCEBRIDGE_API_TOKEN}"}}`,
			want: entryKindHTTPLegacyExact,
		},
		{
			name: "HTTP same URL, literal token",
			json: `{"type":"http","url":"https://example.com/api/v1/mcp/http","headers":{"Authorization":"Bearer ca_xxx"}}`,
			want: entryKindHTTPSameHostCustom,
		},
		{
			name: "HTTP same URL, extra top-level field",
			json: `{"type":"http","url":"https://example.com/api/v1/mcp/http","headers":{"Authorization":"Bearer ${SOURCEBRIDGE_API_TOKEN}"},"extra":1}`,
			want: entryKindHTTPSameHostCustom,
		},
		{
			name: "HTTP same URL, extra header",
			json: `{"type":"http","url":"https://example.com/api/v1/mcp/http","headers":{"Authorization":"Bearer ${SOURCEBRIDGE_API_TOKEN}","X-Custom":"yes"}}`,
			want: entryKindHTTPSameHostCustom,
		},
		{
			name: "HTTP different URL",
			json: `{"type":"http","url":"https://other.example/api/v1/mcp/http","headers":{"Authorization":"Bearer ${SOURCEBRIDGE_API_TOKEN}"}}`,
			want: entryKindHTTPDifferentURL,
		},
		{
			name: "custom stdio",
			json: `{"command":"/usr/local/bin/mcp-remote","args":["https://example.com/mcp"]}`,
			want: entryKindOtherStdio,
		},
		{
			name: "sourcebridge with non-mcp-proxy first arg",
			json: `{"command":"sourcebridge","args":["serve"]}`,
			want: entryKindOtherStdio,
		},
		{
			name: "empty entry",
			json: `{}`,
			want: entryKindOtherStdio,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var entry map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.json), &entry); err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := classifyExistingEntry(entry, expMCP, expServer)
			if got != tc.want {
				t.Errorf("classifyExistingEntry = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestResolveSourcebridgeCommand_PreservesSymlinkPath is the codex r1b M1
// guard: a symlink to the test binary should be persisted as-is, not resolved
// to its target.
func TestResolveSourcebridgeCommand_PreservesSymlinkPath(t *testing.T) {
	// We can't reliably mock os.Executable() in-process, but we can verify
	// the documented invariant on a synthetic path: filepath.EvalSymlinks
	// is NOT called by the resolver. The most reliable way is to assert
	// that for an arbitrary symlink we control, evaluating with
	// filepath.IsAbs returns true and the path string contains the symlink
	// directory, not the target directory.
	if testing.Short() {
		t.Skip("skipping symlink test in -short mode")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "target", "sourcebridge")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	symlinkDir := filepath.Join(dir, "stable", "bin")
	if err := os.MkdirAll(symlinkDir, 0o755); err != nil {
		t.Fatalf("mkdir symlinkDir: %v", err)
	}
	symlinkPath := filepath.Join(symlinkDir, "sourcebridge")
	if err := os.Symlink(target, symlinkPath); err != nil {
		t.Skipf("cannot create symlink (skipping symlink invariant test): %v", err)
	}

	// The invariant we care about: filepath.EvalSymlinks(symlinkPath) !=
	// symlinkPath, but the resolver (if asked to look at this path) would
	// preserve the symlink. We can't redirect os.Executable() in-process,
	// but we can verify the building blocks of the resolver don't
	// accidentally call filepath.EvalSymlinks. Comment-stripped scan to
	// avoid false positives on the docstring that explicitly explains we
	// do NOT call this function.
	implBytes, err := os.ReadFile(mcpjsonSourcePath())
	if err != nil {
		t.Fatalf("reading mcpjson.go: %v", err)
	}
	src := string(implBytes)
	// Strip line comments so we don't trip on "We do NOT EvalSymlinks" in
	// docstrings.
	stripped := stripLineComments(src)
	if strings.Contains(stripped, "filepath.EvalSymlinks") {
		t.Errorf("mcpjson.go calls filepath.EvalSymlinks — codex r1b M1 invariant broken")
	}
	_ = symlinkPath
}

// stripLineComments removes any text from "//" to end of line so a docstring
// mentioning a forbidden symbol doesn't trip the source-level invariant test.
func stripLineComments(src string) string {
	var out strings.Builder
	inBlockComment := false
	lines := strings.Split(src, "\n")
	for _, line := range lines {
		clean := line
		if inBlockComment {
			if idx := strings.Index(clean, "*/"); idx >= 0 {
				clean = clean[idx+2:]
				inBlockComment = false
			} else {
				continue
			}
		}
		// Find // not inside a string literal — a simple rule for this
		// codebase since we don't use //-in-strings here.
		if i := strings.Index(clean, "//"); i >= 0 {
			clean = clean[:i]
		}
		// Block-comment open without close on same line.
		if i := strings.Index(clean, "/*"); i >= 0 {
			clean = clean[:i]
			inBlockComment = true
		}
		out.WriteString(clean)
		out.WriteByte('\n')
	}
	return out.String()
}

// mcpjsonSourcePath returns the path to mcpjson.go for source-level test.
func mcpjsonSourcePath() string {
	// Tests run with cwd == package dir, so a relative path works.
	return "mcpjson.go"
}
