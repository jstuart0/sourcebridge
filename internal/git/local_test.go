// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package git

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestIsIgnoredPath_DecisionParityWithScanRepository is Phase 1.A
// done-definition test #15.
//
// Establishes that the new IsIgnoredPath helper returns the same
// keep-or-drop decision as the inline filtering ScanRepository applies
// during its filepath.Walk callback. The walker's behavior is the
// reference; the helper is the new shared implementation.
//
// Strategy: build a fixture directory with a representative spread of
// ignored vs. allowed paths (covering directory-level skips, hidden
// files, unknown-language files, and ordinary source files), run the
// real ScanRepository over it, and confirm the helper's verdict on
// every fixture path matches whether ScanRepository included it.
//
// If this test fails, IsIgnoredPath has drifted from ScanRepository —
// the watcher in Phase 1.C and the inline scanner would then disagree
// about which files to track, which is the exact correctness hazard
// the helper was extracted to prevent.
func TestIsIgnoredPath_DecisionParityWithScanRepository(t *testing.T) {
	tmp := t.TempDir()

	// Fixture file layout. The map value is whether the path should be
	// kept by ScanRepository (true = expect to appear in repo.Files;
	// false = expect to be skipped). The same expectation applies to
	// IsIgnoredPath: kept ↔ !IsIgnoredPath(root, path).
	type expect struct {
		kept     bool
		oversize bool // size-based skip, not handled by IsIgnoredPath
	}
	fixture := map[string]expect{
		// Ordinary source files — kept.
		"src/main.go":               {kept: true},
		"src/util/helper.go":        {kept: true},
		"docs/intro.md":             {kept: true},
		"config/app.yaml":           {kept: true},
		"deeply/nested/pkg/file.go": {kept: true},

		// Directory-level skips per DefaultIgnorePatterns.
		"node_modules/x/index.js":         {kept: false},
		"vendor/lib/foo.go":               {kept: false},
		"build/output.go":                 {kept: false},
		"target/release/x.go":             {kept: false},
		"dist/bundle.js":                  {kept: false},
		"src/.cache/precompiled.go":       {kept: false},
		".git/objects/abc":                {kept: false},
		"__pycache__/foo.cpython-311.pyc": {kept: false},

		// Hidden-directory skips (start with "." but not in DefaultIgnorePatterns).
		".github/workflows/ci.yml": {kept: false},
		".husky/pre-commit":        {kept: false},

		// Hidden file at root — skipped by ScanRepository's
		// strings.HasPrefix(name, ".") check.
		".env":       {kept: false},
		".gitignore": {kept: false},

		// Unknown extensions — DetectLanguage returns "" → skipped.
		"src/output.bin":   {kept: false},
		"docs/diagram.svg": {kept: false},

		// Oversize file (>1 MiB) — skipped by ScanRepository's
		// size check, NOT by IsIgnoredPath. Marked oversize: true so
		// the parity assertion handles this case correctly (the
		// helper says "keep" but the scanner says "skip"; the
		// helper's contract documents that size is not its concern).
		"src/big.go": {kept: false, oversize: true},
	}

	// Materialize the fixture.
	for relPath, ex := range fixture {
		full := filepath.Join(tmp, relPath)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", relPath, err)
		}
		var content []byte
		if ex.oversize {
			content = make([]byte, (1<<20)+1024) // 1 MiB + 1 KiB
		} else {
			content = []byte("package x\n")
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
	}

	// Run the real scanner.
	repo, err := ScanRepository(tmp)
	if err != nil {
		t.Fatalf("ScanRepository: %v", err)
	}
	scannerKept := make(map[string]bool, len(repo.Files))
	for _, f := range repo.Files {
		scannerKept[filepath.ToSlash(f.Path)] = true
	}

	// Per-fixture assertions.
	var mismatches []string
	for relPath, ex := range fixture {
		got := !IsIgnoredPath(tmp, relPath)

		// IsIgnoredPath does not consult file size. For the oversize
		// case, the helper is allowed to say "keep" while the scanner
		// says "skip"; the contract is documented in IsIgnoredPath's
		// godoc.
		expected := ex.kept
		if ex.oversize {
			expected = true // helper says keep
		}
		if got != expected {
			mismatches = append(mismatches,
				"helper disagreement on "+relPath+" (helper kept="+itos(got)+", expected="+itos(expected)+")")
		}

		// Cross-check: the scanner's actual decision on the path
		// matches the fixture's `kept` (excluding oversize, where
		// the scanner's size rule applies).
		scannerSays := scannerKept[relPath]
		if scannerSays != ex.kept {
			mismatches = append(mismatches,
				"scanner disagreement on "+relPath+" (scanner kept="+itos(scannerSays)+", expected="+itos(ex.kept)+")")
		}

		// Parity (the load-bearing assertion): for non-oversize
		// fixtures, scanner and helper must agree.
		if !ex.oversize && got != scannerSays {
			mismatches = append(mismatches,
				"PARITY VIOLATION on "+relPath+" (helper kept="+itos(got)+", scanner kept="+itos(scannerSays)+")")
		}
	}

	if len(mismatches) > 0 {
		sort.Strings(mismatches)
		t.Fatalf("parity check failed:\n%s", strings.Join(mismatches, "\n"))
	}
}

// TestIsIgnoredPath_TableCases covers edge cases that don't lend
// themselves to filesystem fixtures: malformed inputs, slash-style
// variations, and explicit hidden-component cases. These guard the
// helper's contract independently of ScanRepository.
func TestIsIgnoredPath_TableCases(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		ignored bool
	}{
		{"empty path", "", false},
		{"current dir", ".", false},
		{"plain go file", "main.go", false},
		{"nested go file", "internal/api/handler.go", false},

		{"node_modules root", "node_modules/foo.js", true},
		{"node_modules nested", "src/node_modules/foo.js", true},
		{"vendor", "vendor/lib/x.go", true},
		{".git", ".git/HEAD", true},
		{".cache nested", "src/.cache/x.go", true},

		{"hidden file at root", ".env", true},
		{"hidden file nested", "src/.env", true},
		{"hidden dir", ".github/x.yml", true},

		{"unknown ext", "doc.pdf", true},
		{"no extension", "Makefile", true}, // DetectLanguage returns "" for no-ext

		// Leading "./" tolerated (callers occasionally hand us paths
		// from filepath.Rel-style normalizations that include this).
		{"leading dot-slash", "./src/main.go", false},

		// Note on path separators: callers MUST hand us
		// forward-slash, repo-relative paths per the plan v5
		// path-normalization contract. On non-Windows hosts a
		// backslash is a valid filename byte, so the helper does
		// NOT silently treat "src\\node_modules\\foo.js" as a
		// node_modules path — it's a single bizarre filename. The
		// watcher in Phase 1.C and the HTTP ingress in Phase 1.D
		// reject paths violating the contract; the helper does not
		// re-validate.
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsIgnoredPath("/repo", c.path)
			if got != c.ignored {
				t.Fatalf("IsIgnoredPath(%q) = %v, want %v", c.path, got, c.ignored)
			}
		})
	}
}

func itos(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
