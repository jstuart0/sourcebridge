// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedFile writes content to path, creating dirs as needed.
func seedFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFileRetriever_AuthDomainHit(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "src/services/auth-service.ts", `export async function signIn(email: string, password: string) {
  // auth flow entry
  return { user: {}, token: "t" };
}

export async function requestMagicLink(email: string) {
  // magic link logic
}
`)
	seedFile(t, root, "src/services/billing-service.ts", "export function chargeCustomer() {}\n")
	seedFile(t, root, "tests/auth/login.test.ts", "test('login works', () => {})\n")

	r := DefaultFileRetriever(root)
	ev := r.BestFiles("how does the auth flow work?", KindExecutionFlow)
	if len(ev) == 0 {
		t.Fatal("expected evidence")
	}
	// Product auth file should outrank the test file.
	if ev[0].Path != "src/services/auth-service.ts" {
		t.Errorf("expected auth-service first, got %+v", ev)
	}
	// Test file should still appear — just later.
	foundTest := false
	for _, e := range ev {
		if strings.Contains(e.Path, "tests/") {
			foundTest = true
		}
	}
	if !foundTest {
		t.Error("expected test file to appear, just lower-ranked")
	}
}

func TestFileRetriever_SkipDirs(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "node_modules/foo/index.js", "export function x() {}\n")
	seedFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
	seedFile(t, root, "src/app.go", "package main\nfunc main() {}\n")
	seedFile(t, root, "dist/out.js", "var x = 1;\n")

	r := DefaultFileRetriever(root)
	ev := r.BestFiles("main", KindBehavior)
	for _, e := range ev {
		if strings.HasPrefix(e.Path, "node_modules/") ||
			strings.HasPrefix(e.Path, ".git/") ||
			strings.HasPrefix(e.Path, "dist/") {
			t.Errorf("should skip %s, got it in results", e.Path)
		}
	}
}

func TestFileRetriever_ArchitectureBoost(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "internal/architecture/diagram.go", `package architecture

func GenerateDiagram() {}
`)
	seedFile(t, root, "unrelated/utils.go", "package utils\nfunc X() {}\n")
	r := DefaultFileRetriever(root)
	ev := r.BestFiles("how is the architecture diagram generated?", KindArchitecture)
	if len(ev) == 0 {
		t.Fatal("expected evidence for architecture question")
	}
	if ev[0].Path != "internal/architecture/diagram.go" {
		t.Errorf("expected architecture/diagram.go first, got %q (evidence: %+v)", ev[0].Path, ev)
	}
}

func TestFileRetriever_RespectsMaxFiles(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		seedFile(t, root, filepath.Join("src", "auth", "file"+string(rune('a'+i))+".ts"),
			"export function foo() { /* auth */ }\n")
	}
	r := DefaultFileRetriever(root)
	ev := r.BestFiles("auth flow", KindExecutionFlow)
	if len(ev) > r.MaxFiles {
		t.Errorf("expected <= %d results, got %d", r.MaxFiles, len(ev))
	}
}

func TestFileRetriever_ArchitectureCapIs4(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 10; i++ {
		seedFile(t, root, filepath.Join("internal", "architecture", "m"+string(rune('a'+i))+".go"),
			"package architecture\nfunc X() {}\n")
	}
	r := DefaultFileRetriever(root)
	ev := r.BestFiles("describe the architecture", KindArchitecture)
	if len(ev) > 4 {
		t.Errorf("architecture should cap at 4, got %d", len(ev))
	}
}

func TestBestSnippet_WindowAroundMatch(t *testing.T) {
	// Build a file where a match appears near line 50.
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		if i == 50 {
			b.WriteString("needle is here\n")
		} else {
			b.WriteString("filler line\n")
		}
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "x.go")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	snip, start, end, err := bestSnippet(p, []string{"needle"}, 20, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snip, "needle is here") {
		t.Errorf("snippet missing needle: %s", snip)
	}
	if end-start+1 > 20 {
		t.Errorf("window size > 20: start=%d end=%d", start, end)
	}
}

func TestBestSnippet_OversizedFileSkipped(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.go")
	if err := os.WriteFile(p, []byte(strings.Repeat("x", 200_000)), 0o644); err != nil {
		t.Fatal(err)
	}
	snip, _, _, err := bestSnippet(p, []string{"x"}, 20, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	if snip != "" {
		t.Errorf("expected empty snippet for oversized file, got %q", snip[:50])
	}
}

func TestIsTestPath(t *testing.T) {
	cases := map[string]bool{
		"tests/foo.test.ts":          true,
		"internal/foo/bar_test.go":   true,
		"src/auth/login.test.ts":     true,
		"src/auth/login.ts":          false,
		"internal/architecture/x.go": false,
		"test/integration.go":        true,
	}
	for p, want := range cases {
		if got := isTestPath(p); got != want {
			t.Errorf("isTestPath(%q) = %v, want %v", p, got, want)
		}
	}
}
