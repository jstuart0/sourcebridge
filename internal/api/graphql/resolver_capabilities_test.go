// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/capabilities"
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
	r := &Resolver{} // zero value — Plan is ""
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
	r := &Resolver{Plan: entitlements.PlanOSS}
	caps := r.resolveCapabilities()
	if caps.features.Billing {
		t.Fatalf("PlanOSS resolver should produce Billing: false, got true")
	}
}

// TestResolveCapabilities_PlanEnterprise_BillingTrue verifies that an
// enterprise-plan resolver reports Billing: true (positive case).
func TestResolveCapabilities_PlanEnterprise_BillingTrue(t *testing.T) {
	r := &Resolver{Plan: entitlements.PlanEnterprise}
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
