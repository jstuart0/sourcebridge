// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package manifest_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
)

// --- Frontmatter parse/write round-trip ---

const samplePage = `---
sourcebridge:
  page_id: arch.auth
  template: architecture
  audience: for-engineers
  dependencies:
    paths:
      - internal/auth/**
    symbols:
      - auth.Middleware
      - auth.RequireRole
    upstream_packages:
      - internal/api/rest
      - internal/billing
    downstream_packages:
      - internal/jwt
      - internal/sessions
    dependency_scope: direct
  stale_when:
    - signature_change_in:
        - auth.Middleware
        - auth.RequireRole
    - new_caller_added_to:
        - auth.RequireRole
---
# Auth Package

This package handles authentication middleware.
`

func TestParseFrontmatter_RoundTrip(t *testing.T) {
	m, body, err := manifest.ParseFrontmatter([]byte(samplePage))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}

	// Verify manifest fields.
	if m.PageID != "arch.auth" {
		t.Errorf("PageID: got %q, want %q", m.PageID, "arch.auth")
	}
	if m.Template != "architecture" {
		t.Errorf("Template: got %q, want %q", m.Template, "architecture")
	}
	if m.Audience != "for-engineers" {
		t.Errorf("Audience: got %q, want %q", m.Audience, "for-engineers")
	}
	if m.Dependencies.DependencyScope != manifest.ScopeDirect {
		t.Errorf("DependencyScope: got %q, want %q", m.Dependencies.DependencyScope, manifest.ScopeDirect)
	}
	if len(m.Dependencies.Paths) != 1 || m.Dependencies.Paths[0] != "internal/auth/**" {
		t.Errorf("Paths: got %v", m.Dependencies.Paths)
	}
	if len(m.Dependencies.Symbols) != 2 {
		t.Errorf("Symbols: got %v", m.Dependencies.Symbols)
	}
	if len(m.Dependencies.UpstreamPackages) != 2 {
		t.Errorf("UpstreamPackages: got %v", m.Dependencies.UpstreamPackages)
	}
	if len(m.Dependencies.DownstreamPackages) != 2 {
		t.Errorf("DownstreamPackages: got %v", m.Dependencies.DownstreamPackages)
	}
	if len(m.StaleWhen) != 2 {
		t.Fatalf("StaleWhen: got %d conditions, want 2", len(m.StaleWhen))
	}
	if len(m.StaleWhen[0].SignatureChangeIn) != 2 {
		t.Errorf("StaleWhen[0].SignatureChangeIn: got %v", m.StaleWhen[0].SignatureChangeIn)
	}
	if len(m.StaleWhen[1].NewCallerAddedTo) != 1 {
		t.Errorf("StaleWhen[1].NewCallerAddedTo: got %v", m.StaleWhen[1].NewCallerAddedTo)
	}

	// Verify body.
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "# Auth Package") {
		t.Errorf("body does not contain heading: %q", bodyStr)
	}

	// Write back and verify round-trip produces parseable YAML.
	var buf bytes.Buffer
	if err := manifest.WriteFrontmatter(&buf, m, body); err != nil {
		t.Fatalf("WriteFrontmatter: %v", err)
	}

	m2, body2, err := manifest.ParseFrontmatter(buf.Bytes())
	if err != nil {
		t.Fatalf("ParseFrontmatter after WriteFrontmatter: %v", err)
	}
	if m2.PageID != m.PageID {
		t.Errorf("round-trip PageID: got %q, want %q", m2.PageID, m.PageID)
	}
	if string(body2) != string(body) {
		t.Errorf("round-trip body mismatch:\ngot:  %q\nwant: %q", string(body2), string(body))
	}
}

func TestParseFrontmatter_NoFrontmatter(t *testing.T) {
	src := []byte("# Just markdown\n\nNo frontmatter here.\n")
	m, body, err := manifest.ParseFrontmatter(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.PageID != "" {
		t.Errorf("expected zero manifest, got PageID=%q", m.PageID)
	}
	if string(body) != string(src) {
		t.Errorf("body should be unchanged when no frontmatter")
	}
}

func TestParseFrontmatter_MissingClosingDelimiter(t *testing.T) {
	src := []byte("---\nsourcebridge:\n  page_id: test\n")
	_, _, err := manifest.ParseFrontmatter(src)
	if err == nil {
		t.Fatal("expected error for missing closing delimiter")
	}
}

func TestWriteFrontmatter_ZeroManifest(t *testing.T) {
	body := []byte("# Page\n\nContent.\n")
	var buf bytes.Buffer
	if err := manifest.WriteFrontmatter(&buf, manifest.DependencyManifest{}, body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(buf.Bytes()) != string(body) {
		t.Errorf("zero manifest should write body only, got: %q", buf.String())
	}
}

// --- Diff-to-affected-pages ---

// stubResolver implements PackageGraphResolver for tests.
type stubResolver struct {
	pathToPackage map[string]string
	dependants    map[string][]string
}

func (s *stubResolver) PackageForPath(path string) (string, bool) {
	pkg, ok := s.pathToPackage[path]
	return pkg, ok
}

func (s *stubResolver) TransitiveDependants(pkg string, _ int) []string {
	return s.dependants[pkg]
}

func TestAffectedPages_DirectPathHit(t *testing.T) {
	pages := []manifest.DependencyManifest{
		{
			PageID: "arch.auth",
			Dependencies: manifest.Dependencies{
				Paths:           []string{"internal/auth/**"},
				DependencyScope: manifest.ScopeDirect,
			},
		},
		{
			PageID: "arch.billing",
			Dependencies: manifest.Dependencies{
				Paths:           []string{"internal/billing/**"},
				DependencyScope: manifest.ScopeDirect,
			},
		},
	}

	changed := []manifest.ChangedPair{
		{Path: "internal/auth/auth.go", Symbol: ""},
	}

	affected := manifest.AffectedPages(changed, pages, nil, 2)
	if len(affected) != 1 {
		t.Fatalf("expected 1 affected page, got %d", len(affected))
	}
	if affected[0].Manifest.PageID != "arch.auth" {
		t.Errorf("expected arch.auth, got %q", affected[0].Manifest.PageID)
	}
	if !affected[0].DirectHit {
		t.Error("expected DirectHit=true")
	}
}

func TestAffectedPages_DirectSymbolHit(t *testing.T) {
	pages := []manifest.DependencyManifest{
		{
			PageID: "arch.auth",
			Dependencies: manifest.Dependencies{
				Symbols:         []string{"auth.Middleware", "auth.RequireRole"},
				DependencyScope: manifest.ScopeDirect,
			},
		},
	}

	changed := []manifest.ChangedPair{
		{Path: "internal/auth/middleware.go", Symbol: "auth.Middleware"},
	}

	affected := manifest.AffectedPages(changed, pages, nil, 2)
	if len(affected) != 1 || !affected[0].DirectHit {
		t.Fatalf("expected 1 direct hit, got %v", affected)
	}
}

func TestAffectedPages_GraphHit(t *testing.T) {
	pages := []manifest.DependencyManifest{
		{
			PageID: "arch.auth",
			Dependencies: manifest.Dependencies{
				UpstreamPackages: []string{"internal/api/rest"},
				DependencyScope:  manifest.ScopeDirect,
			},
		},
	}

	resolver := &stubResolver{
		pathToPackage: map[string]string{
			"internal/api/rest/handler.go": "internal/api/rest",
		},
	}

	changed := []manifest.ChangedPair{
		{Path: "internal/api/rest/handler.go", Symbol: ""},
	}

	affected := manifest.AffectedPages(changed, pages, resolver, 2)
	if len(affected) != 1 {
		t.Fatalf("expected 1 affected page, got %d", len(affected))
	}
	if !affected[0].GraphHit {
		t.Error("expected GraphHit=true")
	}
}

func TestAffectedPages_TransitiveHit(t *testing.T) {
	pages := []manifest.DependencyManifest{
		{
			PageID: "overview.system",
			Dependencies: manifest.Dependencies{
				UpstreamPackages: []string{"internal/billing"},
				DependencyScope:  manifest.ScopeTransitive,
			},
		},
		{
			PageID: "arch.auth",
			Dependencies: manifest.Dependencies{
				UpstreamPackages: []string{"internal/api/rest"},
				DependencyScope:  manifest.ScopeDirect, // not transitive
			},
		},
	}

	resolver := &stubResolver{
		pathToPackage: map[string]string{
			"internal/jwt/jwt.go": "internal/jwt",
		},
		// internal/jwt is transitively depended on by internal/billing
		dependants: map[string][]string{
			"internal/jwt": {"internal/billing", "internal/sessions"},
		},
	}

	changed := []manifest.ChangedPair{
		{Path: "internal/jwt/jwt.go", Symbol: ""},
	}

	affected := manifest.AffectedPages(changed, pages, resolver, 2)
	// overview.system should be hit transitively; arch.auth should not.
	if len(affected) != 1 {
		t.Fatalf("expected 1 affected page, got %d: %v", len(affected), affected)
	}
	if affected[0].Manifest.PageID != "overview.system" {
		t.Errorf("expected overview.system, got %q", affected[0].Manifest.PageID)
	}
	if !affected[0].TransitiveHit {
		t.Error("expected TransitiveHit=true")
	}
}

func TestAffectedPages_NoHit(t *testing.T) {
	pages := []manifest.DependencyManifest{
		{
			PageID: "arch.auth",
			Dependencies: manifest.Dependencies{
				Paths:           []string{"internal/auth/**"},
				DependencyScope: manifest.ScopeDirect,
			},
		},
	}

	changed := []manifest.ChangedPair{
		{Path: "internal/payments/pay.go", Symbol: ""},
	}

	affected := manifest.AffectedPages(changed, pages, nil, 2)
	if len(affected) != 0 {
		t.Errorf("expected no affected pages, got %d", len(affected))
	}
}

// --- StaleWhen evaluation ---

func TestEvaluateStaleConditions_SignatureChangeIn(t *testing.T) {
	m := manifest.DependencyManifest{
		PageID: "arch.auth",
		StaleWhen: []manifest.StaleCondition{
			{SignatureChangeIn: []string{"auth.Middleware", "auth.RequireRole"}},
		},
	}

	changed := []manifest.ChangedPair{
		{Symbol: "auth.Middleware"},
	}

	signals := manifest.EvaluateStaleConditions(m, changed)
	if len(signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(signals))
	}
	if signals[0].Kind != "signature_change_in" {
		t.Errorf("Kind: got %q, want %q", signals[0].Kind, "signature_change_in")
	}
	if len(signals[0].TriggeringSymbols) != 1 || signals[0].TriggeringSymbols[0] != "auth.Middleware" {
		t.Errorf("TriggeringSymbols: got %v", signals[0].TriggeringSymbols)
	}
}

func TestEvaluateStaleConditions_NewCallerAddedTo(t *testing.T) {
	m := manifest.DependencyManifest{
		PageID: "arch.auth",
		StaleWhen: []manifest.StaleCondition{
			{NewCallerAddedTo: []string{"auth.RequireRole"}},
		},
	}

	changed := []manifest.ChangedPair{
		{Symbol: "auth.RequireRole"},
	}

	signals := manifest.EvaluateStaleConditions(m, changed)
	if len(signals) != 1 || signals[0].Kind != "new_caller_added_to" {
		t.Errorf("unexpected signals: %v", signals)
	}
}

func TestEvaluateStaleConditions_NoMatch(t *testing.T) {
	m := manifest.DependencyManifest{
		PageID: "arch.auth",
		StaleWhen: []manifest.StaleCondition{
			{SignatureChangeIn: []string{"auth.Middleware"}},
		},
	}

	changed := []manifest.ChangedPair{
		{Symbol: "billing.Invoice"},
	}

	signals := manifest.EvaluateStaleConditions(m, changed)
	if len(signals) != 0 {
		t.Errorf("expected no signals, got %d", len(signals))
	}
}

func TestEvaluateStaleConditions_MultipleConditions(t *testing.T) {
	m := manifest.DependencyManifest{
		PageID: "arch.auth",
		StaleWhen: []manifest.StaleCondition{
			{SignatureChangeIn: []string{"auth.Middleware"}},
			{NewCallerAddedTo: []string{"auth.RequireRole"}},
		},
	}

	// Both symbols changed — both conditions should fire.
	changed := []manifest.ChangedPair{
		{Symbol: "auth.Middleware"},
		{Symbol: "auth.RequireRole"},
	}

	signals := manifest.EvaluateStaleConditions(m, changed)
	if len(signals) != 2 {
		t.Errorf("expected 2 signals, got %d: %v", len(signals), signals)
	}
}
