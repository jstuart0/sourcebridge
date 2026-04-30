// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package resolution_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoDirectLLMProfileReads enforces that no production code outside a
// small, deliberately-named allowlist bypasses the named-profile
// architectural seams.
//
// Slice 4 of the 2026-04-29-llm-provider-profiles plan: the profile
// store is the storage seam for named LLM provider profiles. Every
// non-wiring/non-test consumer must go through one of the two
// architectural seams:
//
//   - resolution.ProfileLookupStore (the resolver-side narrow
//     interface used by the per-repo override path), or
//   - rest.LLMProfileStoreAdapter (the REST-side service-layer
//     interface implemented by the cli/serve.go adapter).
//
// Going through these interfaces means:
//   - the cipher invariant (api_key never crosses package boundaries
//     in plaintext beyond what the adapter explicitly returns) is
//     enforced at the boundary;
//   - the active-profile / version-bump / watermark concerns owned
//     by the cli adapter are not bypassed;
//   - tests can substitute fakes without rewiring SurrealDB.
//
// Per the converged plan (codex-M4), the lint scans THREE orthogonal
// bypass shapes:
//
//   1. Selector-style method calls on *db.SurrealLLMProfileStore-shaped
//      receivers (`lps.LoadProfile(...)`, `a.profileStore.EncryptedAPIKey(...)`,
//      etc). Catches the most direct shape: someone got hold of the
//      concrete store and called a method on it.
//   2. String literals containing the table name `ca_llm_profile`.
//      Catches the bypass shape "raw SurrealQL embedded in a non-store
//      file" (e.g. `surrealdb.Query(..., "SELECT api_key FROM
//      ca_llm_profile WHERE ...")`). Doc-comment / error-message
//      mentions are exempt because the lint scans only Go BasicLit
//      string nodes, and outside the allowlist there should be no
//      legitimate non-comment use of the table name.
//   3. Map index expressions reading `["api_key"]` from a value whose
//      surrounding name suggests a profile context (an identifier
//      containing "profile" case-insensitively). Catches the bypass
//      shape "profile row decoded into map[string]any, then api_key
//      pulled out by hand".
//
// Allowlist entries name a specific reason for the direct read; new
// entries should be a deliberate review decision, not a convenience.
//
// The companion TestProfileLintCatchesPlantedBypass exercises each
// scanner against synthetic source so the matchers don't quietly
// degrade to no-ops over time.
func TestNoDirectLLMProfileReads(t *testing.T) {
	// ENFORCE mode from day one. Slice 4 ships the lint as a hard
	// gate; there is no REPORT-ONLY transition window because slice
	// 1 introduced the seams atomically with the storage. A new
	// caller MUST go through one of the two interfaces above (or
	// land an allowlist entry with a documented reason).
	const enforceMode = true

	root := repoRootForLint(t)
	scanDirs := []string{"internal", "cli", "cmd"}

	var violations []string

	for _, scanDir := range scanDirs {
		base := filepath.Join(root, scanDir)
		_, err := os.Stat(base)
		if err != nil {
			// Some repos may not have all three dirs; the cmd dir
			// is conventionally optional. Skip silently.
			continue
		}
		walkErr := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
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
			rel, err := filepath.Rel(root, path)
			if err != nil {
				rel = path
			}
			relSlash := filepath.ToSlash(rel)
			if isProfileStoreAllowlisted(relSlash) {
				return nil
			}

			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution|parser.ParseComments)
			if err != nil {
				// Other tooling will surface unparseable files; this
				// lint should not fail on parse errors.
				return nil
			}

			ast.Inspect(file, func(n ast.Node) bool {
				// Scanner #1: method-call bypass.
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
						if _, protected := protectedProfileStoreMethods[sel.Sel.Name]; protected {
							if looksLikeProfileStoreReceiver(sel.X) {
								pos := fset.Position(call.Pos())
								if !hasProfileAllowComment(file, fset, pos.Line) {
									violations = append(violations,
										formatProfileMethodViolation(relSlash, pos.Line, sel.Sel.Name))
								}
							}
						}
					}
				}

				// Scanner #2: string-literal bypass (raw SurrealQL).
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					// lit.Value includes the surrounding quotes; do a
					// substring check on the raw value.
					if strings.Contains(lit.Value, profileTableName) {
						pos := fset.Position(lit.Pos())
						if !hasProfileAllowComment(file, fset, pos.Line) {
							violations = append(violations,
								formatProfileLiteralViolation(relSlash, pos.Line))
						}
					}
				}

				// Scanner #3: map-access bypass (m["api_key"] in a
				// profile-named context).
				if idx, ok := n.(*ast.IndexExpr); ok {
					if isProfileMapAccess(idx) {
						pos := fset.Position(idx.Pos())
						if !hasProfileAllowComment(file, fset, pos.Line) {
							violations = append(violations,
								formatProfileMapAccessViolation(relSlash, pos.Line))
						}
					}
				}

				return true
			})
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walk %s: %v", scanDir, walkErr)
		}
	}

	if len(violations) > 0 {
		msg := "AST lint: profile-storage bypass(es) found outside the allowlist.\n" +
			"Every consumer must go through resolution.ProfileLookupStore or rest.LLMProfileStoreAdapter.\n" +
			"Categories scanned: (1) method-call bypass, (2) raw 'ca_llm_profile' string literal, (3) map['api_key'] read in profile-named context.\n" +
			"To allow a specific exception, add a // llmprofile:allow comment on or above the offending line.\n\n" +
			strings.Join(violations, "\n")
		if enforceMode {
			t.Errorf("%s", msg)
		} else {
			t.Logf("[REPORT-ONLY] %s", msg)
		}
	}
}

// TestProfileLintCatchesPlantedBypass is the negative-control test: it
// runs each scanner against synthetic source containing planted
// bypasses (one per category) and asserts the matchers report them.
// This makes the lint robust against a future refactor that
// accidentally narrows any of the three scanners to a no-op.
//
// Cases are tagged with which scanner is expected to fire (or none
// for the negative cases) so a failure tells us *which* scanner
// broke.
func TestProfileLintCatchesPlantedBypass(t *testing.T) {
	type expectFlags struct {
		method  bool // scanner #1: protected method on profile-store-shaped receiver
		literal bool // scanner #2: "ca_llm_profile" string literal
		mapacc  bool // scanner #3: <profile-named>["api_key"] read
	}
	cases := []struct {
		name   string
		src    string
		expect expectFlags
	}{
		// ── Scanner #1: method-call bypasses ──────────────────────
		{
			name: "method_ident_lps_LoadProfile",
			src: `package fake
func bypass() {
	var lps *struct{}
	_ = lps
	lps.LoadProfile(nil, "id")
}`,
			expect: expectFlags{method: true},
		},
		{
			name: "method_selector_a_lps_ListProfiles",
			src: `package fake
type adapter struct{ lps *struct{} }
func (a *adapter) bypass() {
	a.lps.ListProfiles(nil)
}`,
			expect: expectFlags{method: true},
		},
		{
			name: "method_selector_a_profileStore_EncryptedAPIKey",
			src: `package fake
type adapter struct{ profileStore *struct{} }
func (a *adapter) bypass() {
	_, _ = a.profileStore.EncryptedAPIKey("plain")
}`,
			expect: expectFlags{method: true},
		},
		{
			name: "method_ident_profileStore_DeleteProfile",
			src: `package fake
func bypass() {
	var profileStore *struct{}
	_ = profileStore
	profileStore.DeleteProfile(nil, "id")
}`,
			expect: expectFlags{method: true},
		},
		{
			name: "method_selector_s_store_LoadProfile",
			src: `package fake
type shim struct{ store *struct{} }
func (s *shim) load() {
	s.store.LoadProfile(nil, "id")
}`,
			expect: expectFlags{method: true},
		},
		// ── Scanner #2: string-literal bypasses ───────────────────
		{
			name: "literal_raw_query_select",
			src: `package fake
func bypass() {
	_ = "SELECT api_key FROM ca_llm_profile WHERE id = $id"
}`,
			expect: expectFlags{literal: true},
		},
		{
			name: "literal_raw_query_format",
			src: `package fake
import "fmt"
func bypass() {
	_ = fmt.Sprintf("UPDATE %s SET api_key = $k", "ca_llm_profile")
}`,
			expect: expectFlags{literal: true},
		},
		{
			// Multi-line backtick-quoted SurrealQL — also caught
			// because the raw value contains the table name.
			name: "literal_backtick_block",
			src: "package fake\nfunc bypass() {\n\t_ = `SELECT * FROM ca_llm_profile;`\n}\n",
			expect: expectFlags{literal: true},
		},
		// ── Scanner #3: map-access bypasses ───────────────────────
		{
			name: "mapaccess_ident_profile_apikey",
			src: `package fake
func bypass() {
	profile := map[string]any{}
	_ = profile["api_key"]
}`,
			expect: expectFlags{mapacc: true},
		},
		{
			name: "mapaccess_selector_x_profileRow_apikey",
			src: `package fake
type holder struct{ profileRow map[string]any }
func (h *holder) bypass() {
	_ = h.profileRow["api_key"]
}`,
			expect: expectFlags{mapacc: true},
		},
		{
			name: "mapaccess_call_loadProfileRow_apikey",
			src: `package fake
func loadProfileRow() map[string]any { return nil }
func bypass() {
	_ = loadProfileRow()["api_key"]
}`,
			expect: expectFlags{mapacc: true},
		},
		// ── Negative cases ────────────────────────────────────────
		{
			name: "negative_unrelated_method",
			src: `package fake
type other struct{}
func (o *other) Unrelated() {}
func use() {
	var x other
	x.Unrelated()
}`,
			expect: expectFlags{},
		},
		{
			name: "negative_matched_method_unrelated_receiver",
			src: `package fake
type unrelatedKVStore struct{}
func (u *unrelatedKVStore) LoadProfile(ctx interface{}, id string) {}
func use() {
	var u unrelatedKVStore
	u.LoadProfile(nil, "id")
}`,
			expect: expectFlags{},
		},
		{
			// Map access on a non-profile-named receiver: NOT flagged
			// (the scanner is conservative on the receiver-name
			// heuristic; the // llmprofile:allow escape hatch covers
			// any deliberate carve-out).
			name: "negative_mapaccess_non_profile_name",
			src: `package fake
func use() {
	cfg := map[string]string{}
	_ = cfg["api_key"]
}`,
			expect: expectFlags{},
		},
		{
			// String literal containing "profile" but NOT the table
			// name — NOT flagged. We're targeting the SurrealDB
			// identifier specifically.
			name: "negative_literal_unrelated",
			src: `package fake
func bypass() {
	_ = "the user profile is being loaded"
}`,
			expect: expectFlags{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, tc.name+".go", tc.src, parser.SkipObjectResolution|parser.ParseComments)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			var got expectFlags
			ast.Inspect(file, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
						if _, protected := protectedProfileStoreMethods[sel.Sel.Name]; protected {
							if looksLikeProfileStoreReceiver(sel.X) {
								got.method = true
							}
						}
					}
				}
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if strings.Contains(lit.Value, profileTableName) {
						got.literal = true
					}
				}
				if idx, ok := n.(*ast.IndexExpr); ok {
					if isProfileMapAccess(idx) {
						got.mapacc = true
					}
				}
				return true
			})
			if got != tc.expect {
				t.Fatalf("scanner flags: got %+v, want %+v for case %s", got, tc.expect, tc.name)
			}
		})
	}
}

// TestProfileLintAllowlistEntries verifies the allowlist exactly matches
// the architectural seam set. A new entry should require a deliberate
// edit and a paired comment in the touched file explaining why a
// direct profile-store call is justified there.
func TestProfileLintAllowlistEntries(t *testing.T) {
	want := []string{
		"internal/db/llm_profile_store.go",
		"internal/db/llm_config_migration.go",
		"internal/db/llm_profile_helpers.go",
		"internal/db/llm_config_store_profiles.go",
		"cli/serve.go",
		"internal/llm/resolution/profile_aware_adapter.go",
		"internal/api/rest/llm_profiles.go",
		"internal/api/graphql/repository_llm_override.resolvers.go",
	}
	if len(profileStoreAllowlist) != len(want) {
		t.Fatalf("allowlist size mismatch: got %d, want %d (entries: %v)", len(profileStoreAllowlist), len(want), profileStoreAllowlist)
	}
	got := make(map[string]bool, len(profileStoreAllowlist))
	for _, e := range profileStoreAllowlist {
		got[e] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Fatalf("allowlist missing required entry: %s (current allowlist: %v)", w, profileStoreAllowlist)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────
// Lint primitives
// ─────────────────────────────────────────────────────────────────────

// protectedProfileStoreMethods is the set of *db.SurrealLLMProfileStore
// methods that gate access to the named-profile storage seam. Any
// invocation of one of these names on a profile-store-shaped receiver
// outside the allowlist is a forbidden bypass and fails this lint.
//
// To add a new method:
//  1. Add it to *db.SurrealLLMProfileStore.
//  2. Add the method name here.
//  3. Add the corresponding seam method to either
//     resolution.ProfileLookupStore (resolver-side) or
//     rest.LLMProfileStoreAdapter (REST-side) so consumers have a
//     non-bypass path.
var protectedProfileStoreMethods = map[string]struct{}{
	"ListProfiles":             {},
	"LoadProfile":              {},
	"LoadAllProfileIDs":        {},
	"LoadProfileForResolution": {},
	"CreateProfile":            {},
	"UpdateProfile":            {},
	"DeleteProfile":            {},
	"EncryptedAPIKey":          {},
	"EnsureSchema":             {},
	// Cipher() getter intentionally NOT here — slice 4 codex-r2 Low
	// #6 removed it from the store; no production caller needs it.
	"IsEnvelopeEncrypted":      {},
	"ActivateProfile":          {},
}

// profileStoreAllowlist names the production source files that may
// invoke protected profile-store methods directly. Each entry has a
// specific architectural justification documented in the touched
// file. New entries should require a deliberate review decision.
var profileStoreAllowlist = []string{
	// Profile store implementation itself — every protected method is
	// a method on this file's type. The receiver inside its own methods
	// is `s` which the heuristic does NOT flag (so this file is mostly
	// self-clean), but allowlisting the file is belt-and-suspenders.
	"internal/db/llm_profile_store.go",

	// Boot-time migration: legacy ca_llm_config:default → seeded Default
	// profile. This is the only place that may LoadProfile/CreateProfile
	// pre-handler-mount; downstream code must go through the adapter.
	"internal/db/llm_config_migration.go",

	// CAS-guarded BEGIN/COMMIT helpers (write-active, write-non-active,
	// activate, delete-non-active, reconcile). These are package-private
	// SurrealQL composers that the helpers wire together.
	"internal/db/llm_profile_helpers.go",
	"internal/db/llm_config_store_profiles.go",

	// CLI wiring: constructs the store ONCE at boot and threads it into
	// the adapters (resolution.ProfileAwareLLMResolverAdapter for the
	// resolver, llmProfileStoreAdapter for the REST adapter,
	// llmConfigAdapter for the legacy /admin/llm-config bridge).
	// Direct calls live inside the adapter implementations defined in
	// this file.
	"cli/serve.go",

	// Resolver-side adapter — implements the narrow ProfileLookupStore
	// interface in front of the concrete store, plus the legacy/active
	// reconciliation watermark scheme. Direct calls are how the adapter
	// fulfills its interface contract.
	"internal/llm/resolution/profile_aware_adapter.go",

	// REST handlers — go through s.llmProfileStore which is the
	// LLMProfileStoreAdapter interface. This file contains the handler
	// shells; the allowlist is defensive (e.g. a future helper that
	// briefly handles a *db.SurrealLLMProfileStore directly during a
	// migration would be quarantined here, not duplicated elsewhere).
	"internal/api/rest/llm_profiles.go",

	// GraphQL resolver for per-repo override (slice 3): the
	// LLMProfileLookup interface backing the field resolver and the
	// mutation existence check. The resolver consumes profile data
	// through a structurally-typed lookup; allowlisting prevents a
	// future change from accidentally introducing a direct read here.
	"internal/api/graphql/repository_llm_override.resolvers.go",
}

func isProfileStoreAllowlisted(relSlash string) bool {
	for _, allow := range profileStoreAllowlist {
		if relSlash == allow {
			return true
		}
		// Defensive: also match if the path SUFFIX equals an allowlist
		// entry. This protects against the lint being run from a
		// subdirectory where filepath.Rel returns a longer prefix.
		if strings.HasSuffix(relSlash, "/"+allow) {
			return true
		}
	}
	return false
}

// looksLikeProfileStoreReceiver returns true when expr looks like a
// reference to a *db.SurrealLLMProfileStore value. Without full type
// resolution we use the codebase's established naming conventions:
//
//	lps.<Method>             — local var / wiring
//	a.lps.<Method>           — adapter struct field
//	a.profileStore.<Method>  — alternate adapter struct field
//	s.store.<Method>         — shim struct field (*Shim).store
//	profileStore.<Method>    — local var alias
//	lpStore.<Method>         — alternate local var name
//
// The match is conservative: any selector whose final identifier is
// in the receiver-name set is flagged, regardless of the owner's type.
// This catches every call site we actually care about. False positives
// on unrelated types are addressed by the allowlist (file path) or the
// // llmprofile:allow comment escape hatch.
func looksLikeProfileStoreReceiver(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		switch e.Name {
		case "lps", "profileStore", "lpStore":
			return true
		}
	case *ast.SelectorExpr:
		switch e.Sel.Name {
		case "lps", "profileStore", "lpStore":
			return true
		case "store":
			// `s.store` is the convention used inside both
			// profileAwareProfileStoreShim (cli/serve.go, ALLOWLISTED)
			// and any future shim. Conservative match: only flag
			// when the inner expression is also an Ident or
			// SelectorExpr (i.e. it's a real receiver chain, not a
			// package-level identifier).
			switch e.X.(type) {
			case *ast.Ident, *ast.SelectorExpr:
				return true
			}
		}
	}
	return false
}

// hasProfileAllowComment recognizes the // llmprofile:allow escape
// hatch when written within a 6-line window above the call (or on the
// same line). Mirrors the // llmcall:allow shape from the existing
// worker-RPC lint.
func hasProfileAllowComment(file *ast.File, fset *token.FileSet, line int) bool {
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			pos := fset.Position(c.Pos())
			if pos.Line >= line-6 && pos.Line <= line {
				if strings.Contains(c.Text, "llmprofile:allow") {
					return true
				}
			}
		}
	}
	return false
}

// profileTableName is the SurrealDB table holding profile rows. The
// string-literal scanner flags any non-allowlisted Go BasicLit
// string node containing this name, catching raw SurrealQL bypasses
// like `surrealdb.Query(..., "SELECT api_key FROM ca_llm_profile ...")`.
const profileTableName = "ca_llm_profile"

func formatProfileMethodViolation(rel string, line int, method string) string {
	return "  " + rel + ":" + intToStrForProfileLint(line) +
		": direct *db.SurrealLLMProfileStore." + method +
		" — must go through resolution.ProfileLookupStore or rest.LLMProfileStoreAdapter"
}

func formatProfileLiteralViolation(rel string, line int) string {
	return "  " + rel + ":" + intToStrForProfileLint(line) +
		`: string literal references "ca_llm_profile" — raw SurrealQL bypasses must go through internal/db helpers`
}

func formatProfileMapAccessViolation(rel string, line int) string {
	return "  " + rel + ":" + intToStrForProfileLint(line) +
		`: map["api_key"] read in profile-named context — decoded profile rows must not leak api_key through map decoding outside internal/db`
}

// isProfileMapAccess returns true when expr is `<x>["api_key"]` AND
// `<x>` is an identifier or selector chain whose final identifier
// contains "profile" (case-insensitively). The heuristic is narrow on
// purpose: we don't want false-fire on unrelated `m["api_key"]` reads
// in other parts of the codebase. The `// llmprofile:allow` escape
// hatch covers any deliberate exception.
func isProfileMapAccess(idx *ast.IndexExpr) bool {
	lit, ok := idx.Index.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	// lit.Value includes surrounding quotes; strip and compare.
	v := lit.Value
	if len(v) < 2 {
		return false
	}
	v = v[1 : len(v)-1]
	if v != "api_key" {
		return false
	}
	// Walk the receiver chain and find any identifier whose name
	// contains "profile" (case-insensitive). This catches `profile["api_key"]`,
	// `row["api_key"]` where `row` was earlier loaded from a profile
	// (only when its name suggests so), `p["api_key"]` etc.
	return chainContainsProfileName(idx.X)
}

func chainContainsProfileName(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return strings.Contains(strings.ToLower(e.Name), "profile")
	case *ast.SelectorExpr:
		if strings.Contains(strings.ToLower(e.Sel.Name), "profile") {
			return true
		}
		return chainContainsProfileName(e.X)
	case *ast.CallExpr:
		// e.g. `loadProfile(ctx)["api_key"]`. Recurse into the call's
		// function expression so a name like `loadProfileRow(...)`
		// trips the heuristic.
		return chainContainsProfileName(e.Fun)
	}
	return false
}

// repoRootForLint walks up from CWD to find go.mod. Renamed from the
// repoRoot helper in lint_test.go (same package) to avoid collisions
// — the existing helper expects `*testing.T.Helper()` only, which is
// fine, but Go's same-package test files share a flat function
// namespace.
func repoRootForLint(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find go.mod in any parent of %s", cwd)
		}
		dir = parent
	}
}

// intToStrForProfileLint is a strconv-free integer formatter so this
// lint test stays consistent in shape with the existing worker-RPC
// lint (which avoids strconv noise too).
func intToStrForProfileLint(n int) string {
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
