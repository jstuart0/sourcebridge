// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"testing"

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
