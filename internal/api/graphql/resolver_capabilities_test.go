// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/appdeps"
	"github.com/sourcebridge/sourcebridge/internal/capabilities"
	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/entitlements"
)

// TestResolveCapabilities_ZeroValuePlan_BillingFalse verifies that a Resolver
// constructed with a zero-value Plan (empty string ""), as used by tests and
// extensions that don't set Plan explicitly, resolves Billing: false.
//
// Regression guard for codex r2 M2: before the Plan normalization in
// resolveCapabilities, "" != entitlements.PlanOSS ("oss") evaluated to true,
// making a zero-value Resolver incorrectly report Billing: true. The pre-Slice-3
// behavior (currentPlan() returning PlanOSS when no env var was set) produced
// Billing: false. This test pins the backward-compatible behavior.
func TestResolveCapabilities_ZeroValuePlan_BillingFalse(t *testing.T) {
	r := &Resolver{Deps: &appdeps.AppDeps{}} // zero value — Plan is ""
	caps := r.resolveCapabilities()
	if caps.features == nil {
		t.Fatal("resolveCapabilities returned nil features")
	}
	if caps.features.Billing {
		t.Fatalf("zero-value Resolver should produce Billing: false (OSS), got Billing: true; "+
			"pre-Slice-3 contract: no-env currentPlan() returned PlanOSS, so Billing was false. "+
			"r.Plan=%q entitlements.PlanOSS=%q", r.Plan, entitlements.PlanOSS)
	}
}

// TestResolveCapabilities_PlanOSS_BillingFalse verifies that an explicitly-set
// PlanOSS resolver also reports Billing: false.
func TestResolveCapabilities_PlanOSS_BillingFalse(t *testing.T) {
	r := &Resolver{Deps: &appdeps.AppDeps{}, Plan: entitlements.PlanOSS}
	caps := r.resolveCapabilities()
	if caps.features.Billing {
		t.Fatalf("PlanOSS resolver should produce Billing: false, got true")
	}
}

// TestResolveCapabilities_PlanEnterprise_BillingTrue verifies that an
// enterprise-plan resolver reports Billing: true (positive case).
func TestResolveCapabilities_PlanEnterprise_BillingTrue(t *testing.T) {
	r := &Resolver{Deps: &appdeps.AppDeps{}, Plan: entitlements.PlanEnterprise}
	caps := r.resolveCapabilities()
	if !caps.features.Billing {
		t.Fatalf("PlanEnterprise resolver should produce Billing: true, got false")
	}
}

// TestFeatureToCapability_ExactMatchMappings documents which Feature constants
// map to a capability-registry entry and why others don't (semantic mismatch or
// no registry counterpart). This is a table test so additions to featureToCapability
// are automatically covered; it also serves as living documentation of the
// featureToCapability decision rationale.
func TestFeatureToCapability_ExactMatchMappings(t *testing.T) {
	// mapped: Feature → expected non-empty capability name.
	// Both axes must gate the feature identically (see featureToCapability doc).
	mapped := []struct {
		feature entitlements.Feature
		wantCap string
		reason  string
	}{
		{
			feature: entitlements.FeatureAuditLog,
			wantCap: capabilities.CapAuditLog,
			reason:  "enterprise-only in both entitlements (PlanEnterprise) and capability registry (EditionEnterprise); semantically equivalent",
		},
	}
	for _, tc := range mapped {
		got := featureToCapability(tc.feature)
		if got != tc.wantCap {
			t.Errorf("featureToCapability(%q) = %q; want %q (%s)", tc.feature, got, tc.wantCap, tc.reason)
		}
	}

	// unmapped: Feature constants that are intentionally NOT routed through
	// the capability registry (semantic mismatch — plan-gated on entitlements
	// axis but no equivalent registry entry, or the two axes disagree for some
	// plan/edition combinations).
	unmapped := []struct {
		feature entitlements.Feature
		reason  string
	}{
		{entitlements.FeatureSSO, "plan-gated (team+); no CapSSOIdentity registry entry with equivalent semantics"},
		{entitlements.FeatureMultiTenant, "plan-gated (team+); no equivalent capability entry"},
		{entitlements.FeatureLinearConnector, "plan-gated; no equivalent capability entry"},
		{entitlements.FeatureJiraConnector, "plan-gated; no equivalent capability entry"},
		{entitlements.FeatureGitHubApp, "plan-gated; no equivalent capability entry"},
		{entitlements.FeatureGitLabApp, "plan-gated; no equivalent capability entry"},
		{entitlements.FeatureWebhooks, "plan-gated; no equivalent capability entry"},
		{entitlements.FeatureJetBrains, "plan-gated; no equivalent capability entry"},
		{entitlements.FeatureCustomTemplates, "plan-gated (enterprise); no equivalent capability entry"},
		{entitlements.FeatureHelmChart, "plan-gated (enterprise); no equivalent capability entry"},
		{entitlements.FeatureCliffNotes, "plan-gated; knowledge features use a separate hasWorker+hasKnowledge guard"},
		{entitlements.FeatureLearningPaths, "plan-gated; knowledge features use a separate hasWorker+hasKnowledge guard"},
		{entitlements.FeatureCodeTours, "plan-gated; knowledge features use a separate hasWorker+hasKnowledge guard"},
		{entitlements.FeatureSystemExplain, "plan-gated; knowledge features use a separate hasWorker+hasKnowledge guard"},
		{entitlements.FeatureMultiAudienceKnowledge, "plan-gated (team+); no equivalent capability entry"},
		{entitlements.FeatureCustomKnowledgeTemplates, "plan-gated (enterprise); no equivalent capability entry"},
		{entitlements.FeatureAdvancedLearningPaths, "plan-gated (team+); no equivalent capability entry"},
		{entitlements.FeatureSlideGeneration, "plan-gated (enterprise); no equivalent capability entry"},
		{entitlements.FeaturePodcastGeneration, "plan-gated (enterprise); no equivalent capability entry"},
		{entitlements.FeatureKnowledgeScheduling, "plan-gated (enterprise); no equivalent capability entry"},
		{entitlements.FeatureKnowledgeExport, "plan-gated (team+); no equivalent capability entry"},
	}
	for _, tc := range unmapped {
		got := featureToCapability(tc.feature)
		if got != "" {
			t.Errorf("featureToCapability(%q) = %q; want empty (unmapped: %s)", tc.feature, got, tc.reason)
		}
	}
}

// TestResolveGitCredentials_NilGitResolverFallsBackToConfigGit verifies the
// env-bootstrap fallback path in resolveGitCredentialsForOp: when
// r.Deps.GitResolver is nil (test or embedded mode) and r.Deps.Config is
// populated, the resolver returns Config.Git credentials without error.
//
// Regression guard for P11 L2: this path replaced the deleted GitConfigLoader
// branch. Without this test, a future refactor could break the fallback
// silently — every git operation would return empty credentials rather than
// the configured token.
func TestResolveGitCredentials_NilGitResolverFallsBackToConfigGit(t *testing.T) {
	cfg := &config.Config{
		Git: config.GitConfig{
			DefaultToken: "test-pat-token",
			SSHKeyPath:   "/home/user/.ssh/id_ed25519",
		},
	}
	r := &Resolver{Deps: &appdeps.AppDeps{Config: cfg}}
	// GitResolver is nil — triggers the env-bootstrap fallback.
	token, sshKeyPath, err := r.resolveGitCredentialsForOp(context.Background(), "test-op")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if token != cfg.Git.DefaultToken {
		t.Errorf("token: got %q, want %q", token, cfg.Git.DefaultToken)
	}
	if sshKeyPath != cfg.Git.SSHKeyPath {
		t.Errorf("sshKeyPath: got %q, want %q", sshKeyPath, cfg.Git.SSHKeyPath)
	}
}

// TestResolveGitCredentials_NilGitResolverNilConfigReturnsEmpty verifies that
// when both GitResolver and Config are nil, the fallback returns empty strings
// without error (the "no credentials configured" case — callers handle blank
// token by falling back to unauthenticated clone).
func TestResolveGitCredentials_NilGitResolverNilConfigReturnsEmpty(t *testing.T) {
	r := &Resolver{Deps: &appdeps.AppDeps{}}
	token, sshKeyPath, err := r.resolveGitCredentialsForOp(context.Background(), "test-op")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if token != "" {
		t.Errorf("token: got %q, want empty", token)
	}
	if sshKeyPath != "" {
		t.Errorf("sshKeyPath: got %q, want empty", sshKeyPath)
	}
}
