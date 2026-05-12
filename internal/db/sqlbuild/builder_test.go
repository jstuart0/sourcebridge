// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package sqlbuild

import (
	"strings"
	"testing"
	"time"

	"github.com/surrealdb/surrealdb.go/pkg/models"
)

func TestBuilder_New_EmptyHasNoClausesAndNoVars(t *testing.T) {
	b := New()
	if got := b.Len(); got != 0 {
		t.Fatalf("Len()=%d want 0", got)
	}
	if got := b.Clause(); got != "" {
		t.Fatalf("Clause()=%q want empty", got)
	}
	if got := len(b.Vars()); got != 0 {
		t.Fatalf("Vars() len=%d want 0", got)
	}
}

func TestBuilder_AddNonEmptyString_SkipsEmpty(t *testing.T) {
	b := New()
	b.AddNonEmptyString("clone_path", "")
	b.AddNonEmptyString("branch", "main")
	if got := b.Len(); got != 1 {
		t.Fatalf("Len()=%d want 1 (empty string must be skipped)", got)
	}
	if got := b.Clause(); got != "branch = $branch" {
		t.Fatalf("Clause()=%q", got)
	}
	if got := b.Vars()["branch"]; got != "main" {
		t.Fatalf("Vars[branch]=%v want \"main\"", got)
	}
	if _, found := b.Vars()["clone_path"]; found {
		t.Fatalf("clone_path must not appear in vars (skipped)")
	}
}

func TestBuilder_AddStringPtr_SkipsNil(t *testing.T) {
	b := New()
	val := "high"
	b.AddStringPtr("priority", nil)
	b.AddStringPtr("priority", &val)
	if got := b.Len(); got != 1 {
		t.Fatalf("Len()=%d want 1 (nil must be skipped)", got)
	}
	if got := b.Clause(); got != "priority = $priority" {
		t.Fatalf("Clause()=%q", got)
	}
}

func TestBuilder_AddStringsPtr_NilSkipped_EmptyKept(t *testing.T) {
	b := New()
	b.AddStringsPtr("tags", nil)
	empty := []string{}
	b.AddStringsPtr("acceptance_criteria", &empty)
	if got := b.Len(); got != 1 {
		t.Fatalf("Len()=%d want 1 (nil-ptr skipped, empty-slice kept)", got)
	}
	if slice, ok := b.Vars()["acceptance_criteria"].([]string); !ok || len(slice) != 0 {
		t.Fatalf("acceptance_criteria=%v ok=%v want empty []string", b.Vars()["acceptance_criteria"], ok)
	}
}

func TestBuilder_AddFloat64Ptr_NilSkipped(t *testing.T) {
	b := New()
	cost := 0.0015
	b.AddFloat64Ptr("cost_per_1k_input", nil)
	b.AddFloat64Ptr("cost_per_1k_output", &cost)
	if got := b.Len(); got != 1 {
		t.Fatalf("Len()=%d want 1", got)
	}
	if got := b.Vars()["cost_per_1k_output"]; got != 0.0015 {
		t.Fatalf("Vars[cost_per_1k_output]=%v want 0.0015", got)
	}
}

func TestBuilder_AddTimePtr_WrapsCustomDateTime_NilSkipped(t *testing.T) {
	b := New()
	now := time.Date(2026, 5, 12, 4, 30, 0, 0, time.UTC)
	b.AddTimePtr("last_probed_at", nil)
	b.AddTimePtr("last_probed_at", &now)
	if got := b.Len(); got != 1 {
		t.Fatalf("Len()=%d want 1 (nil-ptr skipped)", got)
	}
	got, ok := b.Vars()["last_probed_at"].(models.CustomDateTime)
	if !ok {
		t.Fatalf("Vars[last_probed_at]=%T want models.CustomDateTime (CA-179 invariant)", b.Vars()["last_probed_at"])
	}
	if !got.Time.Equal(now) {
		t.Fatalf("Vars[last_probed_at].Time=%v want %v", got.Time, now)
	}
}

func TestBuilder_AddPresent_GatedOnFlag(t *testing.T) {
	b := New()
	b.AddPresent("provider", false, "openai")
	b.AddPresent("provider", true, "anthropic")
	if got := b.Len(); got != 1 {
		t.Fatalf("Len()=%d want 1", got)
	}
	if got := b.Vars()["provider"]; got != "anthropic" {
		t.Fatalf("Vars[provider]=%v want \"anthropic\"", got)
	}
}

func TestBuilder_AddRaw_NoVarBinding(t *testing.T) {
	b := New()
	b.AddRaw("updated_at = time::now()")
	b.AddNonEmptyString("title", "foo")
	if got := b.Len(); got != 2 {
		t.Fatalf("Len()=%d want 2", got)
	}
	if got := b.Clause(); got != "updated_at = time::now(), title = $title" {
		t.Fatalf("Clause()=%q", got)
	}
	if _, found := b.Vars()["updated_at"]; found {
		t.Fatalf("AddRaw must NOT bind variables")
	}
}

func TestBuilder_Prefixed_NamespacesVarKeys(t *testing.T) {
	b := Prefixed("profile_")
	b.AddPresent("provider", true, "anthropic")
	b.AddPresent("base_url", true, "https://api.anthropic.com")
	if got := b.Clause(); got != "provider = $profile_provider, base_url = $profile_base_url" {
		t.Fatalf("Clause()=%q", got)
	}
	if got := b.Vars()["profile_provider"]; got != "anthropic" {
		t.Fatalf("Vars[profile_provider]=%v", got)
	}
	if _, found := b.Vars()["provider"]; found {
		t.Fatalf("unprefixed key must NOT exist when Builder is Prefixed")
	}
}

func TestBuilder_Clauses_ReturnsCopy(t *testing.T) {
	b := New()
	b.AddNonEmptyString("title", "foo")
	c := b.Clauses()
	c[0] = "MUTATED"
	if !strings.Contains(b.Clause(), "title = $title") {
		t.Fatalf("Clauses() must return a copy; caller mutation leaked into Builder")
	}
}

func TestBuilder_DualArmInterpolationSafe(t *testing.T) {
	// Pins the SetModelCapabilities pattern: a single Builder fragment is
	// interpolated into BOTH the UPDATE arm and the CREATE arm of a
	// LET/IF/ELSE statement. Mutating sets after Clause() is captured
	// must affect future Clause() reads but not the captured string.
	b := New()
	b.AddNonEmptyString("provider", "anthropic")
	first := b.Clause()
	b.AddNonEmptyString("model", "claude-sonnet-4-6")
	second := b.Clause()
	if first == second {
		t.Fatalf("Clause() must reflect later Add* calls (sites that capture early then re-read get stale data otherwise)")
	}
	// But the captured `first` string is immutable (strings are value types in Go).
	if first != "provider = $provider" {
		t.Fatalf("captured Clause()=%q must be immutable", first)
	}
}
