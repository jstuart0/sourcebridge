// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package resolution

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProfileLintSelfTest verifies that the lint catches each of the
// three bypass patterns. Without this self-test the lint can silently
// rot — a regression that disables one of the patterns would still
// report 0 violations on the (clean) production tree.
//
// The self-test writes synthetic violation files into a temp directory
// and invokes the lint scanner directly against that path.
func TestProfileLintSelfTest(t *testing.T) {
	cases := []struct {
		name        string
		filename    string
		source      string
		wantPattern string
		wantDetail  string
	}{
		{
			name:        "string-literal-quoted",
			filename:    "fake_string_quoted.go",
			source:      "package fake\n\nfunc Q() string { return \"SELECT * FROM ca_llm_profile\" }\n",
			wantPattern: "string-literal",
			wantDetail:  `"ca_llm_profile"`,
		},
		{
			name:        "string-literal-raw",
			filename:    "fake_string_raw.go",
			source:      "package fake\n\nfunc R() string { return `UPSERT ca_llm_profile:foo SET name = 'X'` }\n",
			wantPattern: "string-literal",
			wantDetail:  `"ca_llm_profile"`,
		},
		{
			name:     "map-access-profile",
			filename: "fake_map.go",
			source: `package fake

func Read(profile map[string]any) any {
	return profile["api_key"]
}
`,
			wantPattern: "map-access",
			wantDetail:  `<profile>["api_key"]`,
		},
		{
			name:     "map-access-row",
			filename: "fake_map_row.go",
			source: `package fake

func ReadRow(row map[string]any) any {
	return row["api_key"]
}
`,
			wantPattern: "map-access",
			wantDetail:  `<profile>["api_key"]`,
		},
		{
			name:     "selector-chain",
			filename: "fake_selector.go",
			source: `package fake

type rec struct{ LLMProfile struct{ APIKey string } }

func S(r rec) string { return r.LLMProfile.APIKey }
`,
			wantPattern: "selector-chain",
			wantDetail:  ".LLMProfile.APIKey",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.filename)
			if err := os.WriteFile(path, []byte(tc.source), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}

			// Use scanProfileLintFile directly with a synthetic relative
			// path so we can assert the violation is detected without
			// having to inject the file into the production walker.
			rel := "internal/fake/" + tc.filename
			vs := scanProfileLintFile(rel, path)
			if len(vs) == 0 {
				t.Fatalf("expected at least one violation in pattern %q, got 0", tc.wantPattern)
			}
			found := false
			for _, v := range vs {
				if v.pattern == tc.wantPattern && v.detail == tc.wantDetail {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected violation pattern=%q detail=%q in fixture %q; got: %+v",
					tc.wantPattern, tc.wantDetail, tc.name, vs)
			}
		})
	}
}

// TestProfileLintCleanFileHasNoViolations is a negative control: a file
// that does NOT contain any of the three patterns must report zero
// violations. Without this check, a bug that always reports a violation
// would still pass TestProfileLintSelfTest.
func TestProfileLintCleanFileHasNoViolations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clean.go")
	source := `package clean

import "fmt"

type Other struct{ Name string }

func Hello(o Other) string {
	// "ca_llm_config" is the LEGACY table — not the new profiles table.
	m := map[string]any{"name": "x"}
	_ = m["name"]
	return fmt.Sprintf("hello %s from %s", o.Name, "ca_llm_config")
}
`
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	vs := scanProfileLintFile("internal/clean/clean.go", path)
	if len(vs) != 0 {
		t.Fatalf("expected 0 violations on clean fixture; got %d: %+v", len(vs), vs)
	}
}

// TestProfileLintAllowlistShortCircuit verifies that a file in the
// allowlist is not scanned even when it contains every bypass pattern.
// This guards against an allowlist bug that would re-flag the legitimate
// store implementation.
func TestProfileLintAllowlistShortCircuit(t *testing.T) {
	allowlist := profileLintAllowlist()
	// llm_profile_store.go contains every pattern; if it weren't
	// allowlisted, the production lint would report violations there.
	if !allowlist["internal/db/llm_profile_store.go"] {
		t.Fatal("expected internal/db/llm_profile_store.go in allowlist")
	}
	// Spot-check the remaining 7 entries the plan locked in.
	required := []string{
		"internal/db/llm_profile_helpers.go",
		"internal/db/llm_config_migration.go",
		"internal/db/llm_config_store_profiles.go",
		"cli/serve.go",
		"internal/api/rest/llm_profiles.go",
		"internal/api/graphql/repository_llm_override.resolvers.go",
		"internal/llm/resolution/profile_aware_adapter.go",
	}
	for _, p := range required {
		if !allowlist[p] {
			t.Errorf("expected %q in profile lint allowlist", p)
		}
	}
}
