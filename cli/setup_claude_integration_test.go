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
	"time"
)

// TestSetupClaude_E2E exercises the full setup flow against an in-process
// test server that returns known cluster data (with packages and warnings).
// It verifies that the written CLAUDE.md contains:
//   - Subsystem headings
//   - "N symbols · M packages (...)" summary lines
//   - "Watch out:" lines for clusters with graph-derived advisories
//   - The "Compare X and Y clusters:" prompt form when 2+ clusters present
func TestSetupClaude_E2E(t *testing.T) {
	// Build the fake clusters response that the test server will return.
	fakeResp := map[string]interface{}{
		"repo_id":      "test-repo-123",
		"status":       "ready",
		"retrieved_at": time.Now().UTC().Format(time.RFC3339),
		"clusters": []map[string]interface{}{
			{
				"id":           "cluster:auth",
				"label":        "auth",
				"member_count": 14,
				"representative_symbols": []string{
					"TokenStore.Rotate",
					"Session.Validate",
					"OAuthFlow.Begin",
				},
				"partial":  false,
				"packages": []string{"auth", "middleware", "session"},
				"warnings": []map[string]interface{}{
					{
						"symbol": "TokenStore.Rotate",
						"kind":   "cross-package-callers",
						"detail": "TokenStore.Rotate has callers in auth, api, and worker — coordinate changes across all of them.",
					},
					{
						"symbol": "Session.Validate",
						"kind":   "hot-path",
						"detail": "Session.Validate is on the hot path (highest in-degree in cluster, 8 callers).",
					},
				},
			},
			{
				"id":           "cluster:billing",
				"label":        "billing",
				"member_count": 9,
				"representative_symbols": []string{
					"InvoiceJob.Run",
				},
				"partial":  false,
				"packages": []string{"billing", "stripe"},
				"warnings": []map[string]interface{}{
					{
						"symbol": "InvoiceJob.Run",
						"kind":   "hot-path",
						"detail": "InvoiceJob.Run is on the hot path (highest in-degree in cluster, 3 callers).",
					},
				},
			},
			{
				"id":           "cluster:storage",
				"label":        "storage",
				"member_count": 11,
				"representative_symbols": []string{
					"TxManager.Commit",
				},
				"partial":  false,
				"packages": []string{"db"},
				"warnings": nil,
			},
		},
	}

	// Fake repo info response.
	fakeRepo := map[string]interface{}{
		"id":   "test-repo-123",
		"name": "payments-service",
		"path": "/tmp/payments-service",
	}

	// Fake repositories list (for lookupRepoByPath, though we pass --repo-id directly).
	fakeRepos := []map[string]interface{}{fakeRepo}

	// Spin up the test server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case strings.HasSuffix(r.URL.Path, "/clusters"):
			if err := json.NewEncoder(w).Encode(fakeResp); err != nil {
				t.Errorf("encoding clusters response: %v", err)
			}
		case strings.HasPrefix(r.URL.Path, "/api/v1/repositories/test-repo-123"):
			if err := json.NewEncoder(w).Encode(fakeRepo); err != nil {
				t.Errorf("encoding repo response: %v", err)
			}
		case r.URL.Path == "/api/v1/repositories":
			if err := json.NewEncoder(w).Encode(fakeRepos); err != nil {
				t.Errorf("encoding repos response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// Create a temp directory for the output files.
	tmpDir := t.TempDir()

	// Override the flags for this test run.
	origServer := setupClaudeServer
	origRepoID := setupClaudeRepoID
	origDryRun := setupClaudeDryRun
	origNoMCP := setupClaudeNoMCP
	origCI := setupClaudeCI
	origForce := setupClaudeForce
	origCommit := setupClaudeCommitConfig
	origNoSkills := setupClaudeNoSkills

	setupClaudeServer = srv.URL
	setupClaudeRepoID = "test-repo-123"
	setupClaudeDryRun = false
	setupClaudeNoMCP = true    // skip .mcp.json for cleaner test
	setupClaudeCI = false
	setupClaudeForce = false
	setupClaudeCommitConfig = true // skip .gitignore patching
	setupClaudeNoSkills = false

	defer func() {
		setupClaudeServer = origServer
		setupClaudeRepoID = origRepoID
		setupClaudeDryRun = origDryRun
		setupClaudeNoMCP = origNoMCP
		setupClaudeCI = origCI
		setupClaudeForce = origForce
		setupClaudeCommitConfig = origCommit
		setupClaudeNoSkills = origNoSkills
	}()

	// Change working directory to tmpDir so output files go there.
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir to tmpDir: %v", err)
	}
	defer func() { _ = os.Chdir(origWD) }()

	// Run the command. The cobra command must have a non-nil context because
	// runSetupClaude calls cmd.Context() to create the request context.
	setupClaudeCmd.SetContext(context.Background())
	if err := runSetupClaude(setupClaudeCmd, nil); err != nil {
		t.Fatalf("runSetupClaude: %v", err)
	}

	// Read the generated CLAUDE.md.
	claudePath := filepath.Join(tmpDir, ".claude", "CLAUDE.md")
	data, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}
	content := string(data)

	t.Logf("Generated CLAUDE.md:\n%s", content)

	// --- Golden assertions ---

	// Start/end markers.
	assertContains(t, content, "<!-- sourcebridge:start -->", "missing start marker")
	assertContains(t, content, "<!-- sourcebridge:end -->", "missing end marker")

	// Header block.
	assertContains(t, content, "# SourceBridge — payments-service", "missing repo name")
	assertContains(t, content, "Repo ID: test-repo-123", "missing repo ID")
	assertContains(t, content, "Server: "+srv.URL, "missing server URL")

	// "Try this first" should compare the two largest clusters (auth=14 and storage=11
	// after sorting by member count descending: auth>storage>billing).
	assertContains(t, content, "Compare the auth and storage clusters", "Try this first should use Compare form for 2+ clusters")

	// Subsystem headings.
	assertContains(t, content, "## Subsystem: auth", "missing auth heading")
	assertContains(t, content, "## Subsystem: billing", "missing billing heading")
	assertContains(t, content, "## Subsystem: storage", "missing storage heading")

	// Package summary lines — the headline fix.
	assertContains(t, content, "14 symbols · 3 packages (auth, middleware, session)", "missing auth packages summary")
	assertContains(t, content, "9 symbols · 2 packages (billing, stripe)", "missing billing packages summary")
	assertContains(t, content, "11 symbols · 1 package (db)", "missing storage packages summary")

	// Watch out lines — the headline fix.
	assertContains(t, content, "Watch out: TokenStore.Rotate", "missing cross-package-callers warning")
	assertContains(t, content, "Watch out: Session.Validate", "missing hot-path warning for auth")
	assertContains(t, content, "Watch out: InvoiceJob.Run", "missing hot-path warning for billing")

	// Storage has no warnings — the section between "## Subsystem: storage" and
	// the next "## Subsystem:" heading should not contain any "Watch out:" lines.
	storageIdx := strings.Index(content, "## Subsystem: storage")
	if storageIdx < 0 {
		t.Fatal("## Subsystem: storage not found")
	}
	nextHeadingIdx := strings.Index(content[storageIdx+1:], "## Subsystem:")
	var storageSection string
	if nextHeadingIdx >= 0 {
		storageSection = content[storageIdx : storageIdx+1+nextHeadingIdx]
	} else {
		// Last section before end marker.
		storageSection = content[storageIdx:]
	}
	if strings.Contains(storageSection, "Watch out:") {
		t.Errorf("storage section should have no Watch out lines; got:\n%s", storageSection)
	}
}

// TestSetupClaude_MCPProxyTransport verifies that the .mcp.json written by
// runSetupClaude uses the stdio-proxy schema with command + args including
// "mcp-proxy" and "--server <url>", not the legacy HTTP-transport schema.
//
// Renamed from TestSetupClaude_MCPHTTPTransport (slice 3 of cli-mcp-proxy-and-installer)
// because the writer now produces the proxy shape; the HTTP-transport assertions
// would fail.
func TestSetupClaude_MCPProxyTransport(t *testing.T) {
	fakeResp := map[string]interface{}{
		"repo_id":      "mcp-test-repo",
		"status":       "ready",
		"retrieved_at": time.Now().UTC().Format(time.RFC3339),
		"clusters":     []map[string]interface{}{},
	}
	fakeRepo := map[string]interface{}{
		"id":   "mcp-test-repo",
		"name": "mcp-test",
		"path": "/tmp/mcp-test",
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case strings.HasSuffix(r.URL.Path, "/clusters"):
			if err := json.NewEncoder(w).Encode(fakeResp); err != nil {
				t.Errorf("encoding clusters: %v", err)
			}
		case strings.HasPrefix(r.URL.Path, "/api/v1/repositories/mcp-test-repo"):
			if err := json.NewEncoder(w).Encode(fakeRepo); err != nil {
				t.Errorf("encoding repo: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmpDir := t.TempDir()

	origServer := setupClaudeServer
	origRepoID := setupClaudeRepoID
	origDryRun := setupClaudeDryRun
	origNoMCP := setupClaudeNoMCP
	origCI := setupClaudeCI
	origForce := setupClaudeForce
	origCommit := setupClaudeCommitConfig
	origNoSkills := setupClaudeNoSkills

	setupClaudeServer = srv.URL
	setupClaudeRepoID = "mcp-test-repo"
	setupClaudeDryRun = false
	setupClaudeNoMCP = false   // exercise MCP write
	setupClaudeCI = false
	setupClaudeForce = false
	setupClaudeCommitConfig = true
	setupClaudeNoSkills = true // skip CLAUDE.md for a focused MCP test

	defer func() {
		setupClaudeServer = origServer
		setupClaudeRepoID = origRepoID
		setupClaudeDryRun = origDryRun
		setupClaudeNoMCP = origNoMCP
		setupClaudeCI = origCI
		setupClaudeForce = origForce
		setupClaudeCommitConfig = origCommit
		setupClaudeNoSkills = origNoSkills
	}()

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origWD) }()

	setupClaudeCmd.SetContext(context.Background())
	if err := runSetupClaude(setupClaudeCmd, nil); err != nil {
		t.Fatalf("runSetupClaude: %v", err)
	}

	// Verify the .mcp.json has the HTTP-transport shape.
	mcpPath := filepath.Join(tmpDir, ".mcp.json")
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("reading .mcp.json: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing .mcp.json: %v", err)
	}

	servers, ok := doc["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatalf("mcpServers not present or not a map")
	}
	entry, ok := servers["sourcebridge"].(map[string]interface{})
	if !ok {
		t.Fatalf("mcpServers.sourcebridge not present")
	}

	// Slice 3 (cli-mcp-proxy-and-installer): the writer now produces the
	// stdio-proxy shape. Assert that explicitly.
	cmd, _ := entry["command"].(string)
	if cmd == "" {
		t.Errorf("entry.command must be non-empty (proxy shape); got %v", entry["command"])
	}
	args, ok := entry["args"].([]interface{})
	if !ok {
		t.Fatalf("entry.args missing or not a slice")
	}
	if len(args) < 3 {
		t.Fatalf("entry.args has fewer than 3 elements: %v", args)
	}
	if args[0] != "mcp-proxy" {
		t.Errorf("entry.args[0] = %v; want \"mcp-proxy\"", args[0])
	}
	if args[1] != "--server" {
		t.Errorf("entry.args[1] = %v; want \"--server\"", args[1])
	}
	if args[2] != srv.URL {
		t.Errorf("entry.args[2] = %v; want %q", args[2], srv.URL)
	}
	// Legacy HTTP-shape fields must be absent.
	if _, has := entry["type"]; has {
		t.Error("entry must not contain 'type' (legacy HTTP shape)")
	}
	if _, has := entry["url"]; has {
		t.Error("entry must not contain 'url' (legacy HTTP shape)")
	}
	if _, has := entry["headers"]; has {
		t.Error("entry must not contain 'headers' (legacy HTTP shape)")
	}
}

// TestSetupClaude_MCPIdempotent verifies that a second run leaves .mcp.json
// byte-identical (UNCHANGED path).
func TestSetupClaude_MCPIdempotent(t *testing.T) {
	fakeResp := map[string]interface{}{
		"repo_id":      "mcp-idm-repo",
		"status":       "ready",
		"retrieved_at": time.Now().UTC().Format(time.RFC3339),
		"clusters":     []map[string]interface{}{},
	}
	fakeRepo := map[string]interface{}{
		"id":   "mcp-idm-repo",
		"name": "mcp-idm",
		"path": "/tmp/mcp-idm",
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case strings.HasSuffix(r.URL.Path, "/clusters"):
			if err := json.NewEncoder(w).Encode(fakeResp); err != nil {
				t.Errorf("encoding clusters: %v", err)
			}
		case strings.HasPrefix(r.URL.Path, "/api/v1/repositories/mcp-idm-repo"):
			if err := json.NewEncoder(w).Encode(fakeRepo); err != nil {
				t.Errorf("encoding repo: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmpDir := t.TempDir()

	origServer := setupClaudeServer
	origRepoID := setupClaudeRepoID
	origDryRun := setupClaudeDryRun
	origNoMCP := setupClaudeNoMCP
	origCI := setupClaudeCI
	origForce := setupClaudeForce
	origCommit := setupClaudeCommitConfig
	origNoSkills := setupClaudeNoSkills

	setupClaudeServer = srv.URL
	setupClaudeRepoID = "mcp-idm-repo"
	setupClaudeDryRun = false
	setupClaudeNoMCP = false
	setupClaudeCI = false
	setupClaudeForce = false
	setupClaudeCommitConfig = true
	setupClaudeNoSkills = true

	defer func() {
		setupClaudeServer = origServer
		setupClaudeRepoID = origRepoID
		setupClaudeDryRun = origDryRun
		setupClaudeNoMCP = origNoMCP
		setupClaudeCI = origCI
		setupClaudeForce = origForce
		setupClaudeCommitConfig = origCommit
		setupClaudeNoSkills = origNoSkills
	}()

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origWD) }()

	setupClaudeCmd.SetContext(context.Background())
	if err := runSetupClaude(setupClaudeCmd, nil); err != nil {
		t.Fatalf("first run: %v", err)
	}

	mcpPath := filepath.Join(tmpDir, ".mcp.json")
	before, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("reading .mcp.json after first run: %v", err)
	}

	if err := runSetupClaude(setupClaudeCmd, nil); err != nil {
		t.Fatalf("second run: %v", err)
	}

	after, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("reading .mcp.json after second run: %v", err)
	}

	if string(before) != string(after) {
		t.Errorf(".mcp.json changed on idempotent second run:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestSetupClaude_MCPMigratesOldShape verifies that an existing v1 broken stdio
// .mcp.json is rewritten to the HTTP shape when runSetupClaude is called.
func TestSetupClaude_MCPMigratesOldShape(t *testing.T) {
	fakeResp := map[string]interface{}{
		"repo_id":      "mcp-migrate-repo",
		"status":       "ready",
		"retrieved_at": time.Now().UTC().Format(time.RFC3339),
		"clusters":     []map[string]interface{}{},
	}
	fakeRepo := map[string]interface{}{
		"id":   "mcp-migrate-repo",
		"name": "mcp-migrate",
		"path": "/tmp/mcp-migrate",
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case strings.HasSuffix(r.URL.Path, "/clusters"):
			if err := json.NewEncoder(w).Encode(fakeResp); err != nil {
				t.Errorf("encoding clusters: %v", err)
			}
		case strings.HasPrefix(r.URL.Path, "/api/v1/repositories/mcp-migrate-repo"):
			if err := json.NewEncoder(w).Encode(fakeRepo); err != nil {
				t.Errorf("encoding repo: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmpDir := t.TempDir()

	// Write the broken v1 stdio shape first.
	mcpPath := filepath.Join(tmpDir, ".mcp.json")
	brokenShape := `{"mcpServers":{"sourcebridge":{"command":"sourcebridge","args":["mcp","--repo-id","mcp-migrate-repo"]}}}`
	if err := os.WriteFile(mcpPath, []byte(brokenShape), 0o644); err != nil {
		t.Fatalf("writing broken shape: %v", err)
	}

	origServer := setupClaudeServer
	origRepoID := setupClaudeRepoID
	origDryRun := setupClaudeDryRun
	origNoMCP := setupClaudeNoMCP
	origCI := setupClaudeCI
	origForce := setupClaudeForce
	origCommit := setupClaudeCommitConfig
	origNoSkills := setupClaudeNoSkills

	setupClaudeServer = srv.URL
	setupClaudeRepoID = "mcp-migrate-repo"
	setupClaudeDryRun = false
	setupClaudeNoMCP = false
	setupClaudeCI = false
	setupClaudeForce = false
	setupClaudeCommitConfig = true
	setupClaudeNoSkills = true

	defer func() {
		setupClaudeServer = origServer
		setupClaudeRepoID = origRepoID
		setupClaudeDryRun = origDryRun
		setupClaudeNoMCP = origNoMCP
		setupClaudeCI = origCI
		setupClaudeForce = origForce
		setupClaudeCommitConfig = origCommit
		setupClaudeNoSkills = origNoSkills
	}()

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origWD) }()

	setupClaudeCmd.SetContext(context.Background())
	if err := runSetupClaude(setupClaudeCmd, nil); err != nil {
		t.Fatalf("runSetupClaude: %v", err)
	}

	data, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("reading .mcp.json: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing .mcp.json: %v", err)
	}

	servers := doc["mcpServers"].(map[string]interface{})
	entry := servers["sourcebridge"].(map[string]interface{})

	// Slice 3: migration target is now the proxy shape, not the HTTP shape.
	args, ok := entry["args"].([]interface{})
	if !ok || len(args) < 3 {
		t.Fatalf("expected proxy-shape args after migration, got %v", entry["args"])
	}
	if args[0] != "mcp-proxy" {
		t.Errorf("expected args[0]=\"mcp-proxy\", got %v", args[0])
	}
	if _, has := entry["type"]; has {
		t.Error("entry must not contain 'type' field after migration to proxy shape")
	}

	// Backup should exist (the broken-stdio path uses .sb-backup, not the
	// timestamped suffix).
	if _, err := os.Stat(mcpPath + ".sb-backup"); err != nil {
		t.Errorf("expected .sb-backup after migration: %v", err)
	}
}

func assertContains(t *testing.T, content, substr, msg string) {
	t.Helper()
	if !strings.Contains(content, substr) {
		t.Errorf("%s: %q not found in output", msg, substr)
	}
}
