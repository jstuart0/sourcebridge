// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package resolution

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoDirectLLMProfileReads enforces that no production code outside a
// small, deliberate allowlist reads the `ca_llm_profile` table directly.
// LLM provider profiles slice 4 (codex-M4): the abstraction is brand-new,
// so direct reads outside the allowlist are bugs from day one. The lint
// runs in ENFORCE mode immediately — there is no record-only window.
//
// The lint walks every Go file under internal/, cli/, and cmd/ that is
// NOT a *_test.go file and flags THREE orthogonal bypass patterns:
//
//  1. String-literal table reference — any string literal whose value
//     contains "ca_llm_profile" (e.g. raw SurrealQL like
//     `SELECT * FROM ca_llm_profile`). Primary guard because direct
//     bypasses in this codebase historically appear as query strings.
//
//  2. Profile-shaped map access — index expressions of the form
//     `<expr>["api_key"]` where the indexed identifier looks like a
//     profile row (named `profile`, `prof`, `profiles`, `row`, `rows`,
//     `record`, `rec`). Heuristic but covers the common bypass shape
//     `row, _ := surrealDB.Select("ca_llm_profile:..."); secret := row["api_key"]`.
//
//  3. Selector chain `.LLMProfile.<field>` — terminating selector chain
//     suggesting someone defined a struct with an `LLMProfile` field and
//     read `APIKey` / `Provider` / etc. directly. Lower-precedence than
//     #1 / #2 because the bypass shape is unusual.
//
// The lint is allowlist-driven: each entry below is a deliberately
// reviewed exception. Adding a new entry MUST be accompanied by a
// `// LLMPROFILE_LINT_OK: <reason>` comment in the touched file naming
// the specific reason.
//
// Self-test: `TestProfileLintSelfTest` verifies all three patterns are
// caught on a synthetic violation in test fixtures (so the lint cannot
// silently regress).
func TestNoDirectLLMProfileReads(t *testing.T) {
	const enforceMode = true

	allowlist := profileLintAllowlist()

	violations := scanProfileLintViolations(t, allowlist, profileLintIncludeNothingExtra)

	if len(violations) > 0 {
		msg := "AST lint: " + intToStrProfile(len(violations)) + " forbidden direct ca_llm_profile read(s) found outside the allowlist.\nEvery production-code read of the profile table must go through *db.SurrealLLMProfileStore (which exposes typed accessors and never leaks api_key plaintext).\nTo allow a specific exception, add the file to the allowlist in profile_lint_test.go AND add a // LLMPROFILE_LINT_OK: <reason> comment in the touched file naming the specific reason.\n\n" + formatProfileLintViolations(violations)
		if enforceMode {
			t.Errorf("%s", msg)
		} else {
			t.Logf("[REPORT-ONLY] %s", msg)
		}
	}
}

// profileLintAllowlist returns the set of repo-relative file paths
// permitted to read ca_llm_profile / profile rows directly. Each entry
// is annotated with the SPECIFIC reason that file is allowed.
//
// Tests within these packages are also allowed by default: a fake or
// integration test naturally needs to seed/inspect the table.
//
// Lint self-test fixtures are explicitly NOT allowlisted — the
// fixtures live under testdata/ which is skipped by the walker, but
// the self-test invokes the lint scanner against a synthetic in-memory
// file rather than committing fixture files to the tree.
func profileLintAllowlist() map[string]bool {
	return map[string]bool{
		// LLMPROFILE_LINT_OK: store implementation — the canonical
		// owner of ca_llm_profile reads/writes.
		"internal/db/llm_profile_store.go": true,

		// LLMPROFILE_LINT_OK: profile-store CAS-guarded write helpers
		// (CreateProfile / UpdateProfile / ActivateProfile / DeleteProfile /
		// reconcileLegacyToActive) — companion to llm_profile_store.go.
		"internal/db/llm_profile_helpers.go": true,

		// LLMPROFILE_LINT_OK: boot migration that seeds the Default
		// profile from legacy ca_llm_config bytes (idempotent on the
		// deterministic record id ca_llm_profile:default-migrated).
		"internal/db/llm_config_migration.go": true,

		// LLMPROFILE_LINT_OK: profile-aware bridge between the legacy
		// ca_llm_config row and the new active-profile pointer; reads
		// active_profile_id + version cell on every Resolve.
		"internal/db/llm_config_store_profiles.go": true,

		// LLMPROFILE_LINT_OK: cli wiring constructs the cipher once and
		// hands a typed *db.SurrealLLMProfileStore to the REST handler,
		// the resolver adapter, and the GraphQL profile-lookup adapter.
		// The adapters here implement narrow interfaces; the rest of the
		// codebase consumes those interfaces, never the concrete store.
		"cli/serve.go": true,

		// LLMPROFILE_LINT_OK: REST handler surface — the only HTTP entry
		// point for profile CRUD. Goes through a narrow LLMProfileStoreAdapter
		// interface; the file references "ca_llm_profile:" only when
		// composing record-id strings for URL-path routing.
		"internal/api/rest/llm_profiles.go": true,

		// LLMPROFILE_LINT_OK: GraphQL per-repo override mutation needs
		// to validate that a referenced profile exists at save time and
		// resolve the name at read time. Both go through the narrow
		// LLMProfileLookup interface (defined in resolver.go), backed by
		// the SurrealLLMProfileStore via a small adapter. The .resolvers.go
		// file itself never touches ca_llm_profile directly — it only
		// holds the field/mutation resolver bodies that delegate.
		"internal/api/graphql/repository_llm_override.resolvers.go": true,

		// LLMPROFILE_LINT_OK: the resolver-adapter layer that the
		// resolution package uses to load a profile by id (slice 1
		// codex-M5). The adapter sits behind a narrow
		// ProfileAwareProfileStore interface and does not export
		// profile structures; it only loads them into resolution-package
		// shapes that strip credentials.
		"internal/llm/resolution/profile_aware_adapter.go": true,
	}
}

// profileLintIncludeNothingExtra is a no-op extra-files merger used by
// the production lint. The self-test (TestProfileLintSelfTest) plugs a
// different value to scan a synthetic violation file.
func profileLintIncludeNothingExtra(_ string, _ map[string]bool) {}

// scanProfileLintViolations walks the production-code tree and returns
// every direct ca_llm_profile read that is not in the allowlist.
//
// Exposed (unexported) so the self-test can call it against a fixture
// directory and verify the three patterns are detected.
func scanProfileLintViolations(
	t *testing.T,
	allowlist map[string]bool,
	extraFn func(repoRoot string, allowlist map[string]bool),
) []profileLintViolation {
	t.Helper()

	repoRoot := findRepoRootProfile(t)
	scanDirs := []string{"internal", "cli", "cmd"}

	// Allow the self-test to extend allowlist or scan extra paths.
	extraFn(repoRoot, allowlist)

	var violations []profileLintViolation

	for _, scanDir := range scanDirs {
		root := filepath.Join(repoRoot, scanDir)
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				if name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(repoRoot, path)
			rel = filepath.ToSlash(rel)
			if allowlist[rel] {
				return nil
			}
			violations = append(violations, scanProfileLintFile(rel, path)...)
			return nil
		})
	}
	return violations
}

// scanProfileLintFile parses one Go file and returns any of the three
// violation patterns found in it.
func scanProfileLintFile(rel, path string) []profileLintViolation {
	fset := token.NewFileSet()
	f, perr := parser.ParseFile(fset, path, nil, parser.AllErrors|parser.ParseComments)
	if perr != nil {
		// Unparseable files are ignored — other tooling will catch them.
		return nil
	}

	var violations []profileLintViolation

	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {

		case *ast.BasicLit:
			// Pattern 1: string-literal "ca_llm_profile"
			if v.Kind != token.STRING {
				return true
			}
			// Strip quotes/backticks. Both raw strings (`...`) and
			// quoted strings ("...") are checked.
			lit := v.Value
			if len(lit) >= 2 && (lit[0] == '"' || lit[0] == '`') {
				lit = lit[1 : len(lit)-1]
			}
			if strings.Contains(lit, "ca_llm_profile") {
				pos := fset.Position(v.Pos())
				violations = append(violations, profileLintViolation{
					file:    rel,
					line:    pos.Line,
					pattern: "string-literal",
					detail:  `"ca_llm_profile"`,
				})
			}

		case *ast.IndexExpr:
			// Pattern 2: <profileLikeExpr>["api_key"]
			lit, ok := v.Index.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			key := lit.Value
			if len(key) >= 2 && key[0] == '"' {
				key = key[1 : len(key)-1]
			}
			if key != "api_key" {
				return true
			}
			if !looksLikeProfileExpr(v.X) {
				return true
			}
			pos := fset.Position(v.Pos())
			violations = append(violations, profileLintViolation{
				file:    rel,
				line:    pos.Line,
				pattern: "map-access",
				detail:  `<profile>["api_key"]`,
			})

		case *ast.SelectorExpr:
			// Pattern 3: <expr>.LLMProfile.<field>
			inner, ok := v.X.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if inner.Sel.Name != "LLMProfile" {
				return true
			}
			pos := fset.Position(v.Pos())
			violations = append(violations, profileLintViolation{
				file:    rel,
				line:    pos.Line,
				pattern: "selector-chain",
				detail:  ".LLMProfile." + v.Sel.Name,
			})
		}
		return true
	})

	return violations
}

// looksLikeProfileExpr returns true when the receiver of a map-access
// expression is named in a way that suggests it holds a profile row.
// Conservative heuristic: only flag when the identifier or the final
// selector name is one of the well-known profile-like names. Avoids
// false positives on unrelated `api_key` map keys.
func looksLikeProfileExpr(expr ast.Expr) bool {
	var name string
	switch e := expr.(type) {
	case *ast.Ident:
		name = e.Name
	case *ast.SelectorExpr:
		name = e.Sel.Name
	default:
		return false
	}
	switch strings.ToLower(name) {
	case "profile", "prof", "profiles", "profrow", "row", "rows", "record", "rec":
		return true
	}
	return false
}

// profileLintViolation describes a single bypass in scanned source.
type profileLintViolation struct {
	file    string
	line    int
	pattern string
	detail  string
}

// formatProfileLintViolations renders a multi-line list of violations.
func formatProfileLintViolations(vs []profileLintViolation) string {
	var b strings.Builder
	for _, v := range vs {
		b.WriteString("  ")
		b.WriteString(v.file)
		b.WriteString(":")
		writeIntProfile(&b, v.line)
		b.WriteString(" [")
		b.WriteString(v.pattern)
		b.WriteString("]: ")
		b.WriteString(v.detail)
		b.WriteString("\n")
	}
	return b.String()
}

// findRepoRootProfile walks up from the test's package dir to locate
// the repository root (the directory containing go.mod).
func findRepoRootProfile(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("abs cwd: %v", err)
	}
	for {
		matches, _ := filepath.Glob(filepath.Join(dir, "go.mod"))
		if len(matches) > 0 {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate go.mod walking up from %s", dir)
		}
		dir = parent
	}
}

func intToStrProfile(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

func writeIntProfile(b *strings.Builder, n int) {
	b.WriteString(intToStrProfile(n))
}
