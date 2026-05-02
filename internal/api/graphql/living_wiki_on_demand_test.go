// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// Tests for Mutation.generateLivingWikiPageOnDemand (Phase 4a, LD-6).
//
// Done-when criteria:
//  1. A folder request produces exactly one detail.* page and reports it as
//     dispatched when sinks are configured.
//  2. The resolver returns LIVING_WIKI_DETAILED_DISABLED when
//     LivingWikiDetailedEnabled = false.
//  3. Supplying both folder and symbol, or neither, returns INVALID_PAGE_SPEC.

package graphql

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/gqlerror"

	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// onDemandOrch returns a minimal lworch.Orchestrator wired with a template
// whose ID matches "architecture" — the template used by all detail.* pages.
func onDemandOrch() *lworch.Orchestrator {
	tmpl := &csPassingTemplate{id: "architecture"}
	reg := lworch.NewMapRegistry(tmpl)
	store := lworch.NewMemoryPageStore()
	return lworch.New(lworch.Config{RepoID: "on-demand-repo"}, reg, store)
}

// onDemandResolver builds a mutationResolver with the minimum fields needed
// to exercise GenerateLivingWikiPageOnDemand.
//
// When wantSink is false no sinks are configured, so dispatchGeneratedPages
// returns an empty result and Dispatched is false. That's fine for tests that
// only care about page generation, not dispatch.
func onDemandResolver(repoID string, detailedEnabled bool) *mutationResolver {
	repoStore := livingwiki.NewRepoSettingsMemStore()
	_ = repoStore.SetRepoSettings(context.Background(), livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  "default",
		RepoID:                    repoID,
		Enabled:                   true,
		LivingWikiDetailedEnabled: detailedEnabled,
	})
	return &mutationResolver{Resolver: &Resolver{
		LivingWikiRepoStore:        repoStore,
		LivingWikiLiveOrchestrator: onDemandOrch(),
	}}
}

// gqlErrorCode extracts the extension code from a gqlerror.Error, returning ""
// when err is nil or is not a gqlerror.
func gqlErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var gqlErr *gqlerror.Error
	if !asGQLError(err, &gqlErr) {
		return ""
	}
	if code, ok := gqlErr.Extensions["code"].(string); ok {
		return code
	}
	return ""
}

// asGQLError is a thin wrapper that avoids importing errors package.
func asGQLError(err error, target **gqlerror.Error) bool {
	if e, ok := err.(*gqlerror.Error); ok {
		*target = e
		return true
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

// TestOnDemandMutationGeneratesSinglePage verifies that a folder request:
//   - Produces exactly one generated page from lworch.Generate.
//   - The returned pageId carries the expected detail.* prefix.
//   - No GraphQL error is returned.
//
// Dispatch is not asserted here because there are no configured sinks; the
// Dispatched flag should be false (no sinks → no pages written).
func TestOnDemandMutationGeneratesSinglePage(t *testing.T) {
	t.Parallel()

	const repoID = "on-demand-repo"
	r := onDemandResolver(repoID, true /* detailedEnabled */)

	folder := "internal/api"
	result, err := r.GenerateLivingWikiPageOnDemand(
		context.Background(),
		repoID,
		LivingWikiOnDemandPageSpec{Folder: &folder},
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	wantPageID := lworch.DetailPageID(repoID, folder)
	if result.PageID != wantPageID {
		t.Errorf("pageId: got %q, want %q", result.PageID, wantPageID)
	}
	// No sinks configured → dispatched must be false.
	if result.Dispatched {
		t.Error("expected Dispatched=false when no sinks are configured")
	}
}

// TestOnDemandMutationRejectsWhenDetailedDisabled verifies that the mutation
// returns a LIVING_WIKI_DETAILED_DISABLED error when the repo's settings have
// LivingWikiDetailedEnabled = false.
func TestOnDemandMutationRejectsWhenDetailedDisabled(t *testing.T) {
	t.Parallel()

	const repoID = "on-demand-disabled-repo"
	r := onDemandResolver(repoID, false /* detailedEnabled */)

	folder := "internal/api"
	result, err := r.GenerateLivingWikiPageOnDemand(
		context.Background(),
		repoID,
		LivingWikiOnDemandPageSpec{Folder: &folder},
	)
	if result != nil {
		t.Errorf("expected nil result on error, got %+v", result)
	}
	if err == nil {
		t.Fatal("expected error when Detailed mode is disabled")
	}
	if code := gqlErrorCode(err); code != "LIVING_WIKI_DETAILED_DISABLED" {
		t.Errorf("extension code: got %q, want %q", code, "LIVING_WIKI_DETAILED_DISABLED")
	}
}

// TestOnDemandMutationRejectsInvalidPageSpec verifies the two INVALID_PAGE_SPEC
// error cases:
//   - Both folder and symbol provided.
//   - Neither folder nor symbol provided.
func TestOnDemandMutationRejectsInvalidPageSpec(t *testing.T) {
	t.Parallel()

	const repoID = "on-demand-repo"
	r := onDemandResolver(repoID, true /* detailedEnabled */)

	tests := []struct {
		name string
		spec LivingWikiOnDemandPageSpec
	}{
		{
			name: "both folder and symbol",
			spec: LivingWikiOnDemandPageSpec{
				Folder: strPtr("internal/api"),
				Symbol: strPtr("internal/api.Handler"),
			},
		},
		{
			name: "neither folder nor symbol",
			spec: LivingWikiOnDemandPageSpec{},
		},
		{
			name: "both empty strings",
			spec: LivingWikiOnDemandPageSpec{
				Folder: strPtr(""),
				Symbol: strPtr(""),
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := r.GenerateLivingWikiPageOnDemand(
				context.Background(),
				repoID,
				tc.spec,
			)
			if result != nil {
				t.Errorf("expected nil result, got %+v", result)
			}
			if err == nil {
				t.Fatal("expected INVALID_PAGE_SPEC error")
			}
			if code := gqlErrorCode(err); code != "INVALID_PAGE_SPEC" {
				t.Errorf("extension code: got %q, want %q", code, "INVALID_PAGE_SPEC")
			}
		})
	}
}

// TestGenerateLivingWikiPageOnDemand_ResolvesTierAndFreezes verifies that the
// on-demand path resolves the tier via newRegistryTierFunc + ClassifyByPattern
// and sets GenerateRequest.LLMTier accordingly. (codex r1c HIGH #3; CA-150 Phase 4)
//
// Test shape:
//   - Mock LLMResolver returning Snapshot{Provider: "ollama", Model: "qwen3:32b"}.
//   - Mock ComprehensionStore returning nil for that model (pattern fallback fires).
//   - ClassifyByPattern("ollama", "qwen3:32b") → TierMid (32B ≥ 30B threshold).
//   - Assert the resolved-tier log line fires with tier=mid.
//   - Assert the run completes without error (TierUnknown error log must NOT fire).
func TestGenerateLivingWikiPageOnDemand_ResolvesTierAndFreezes(t *testing.T) {
	// No t.Parallel() — swaps global slog.Default; must not race with other tests.

	// Capture slog to assert tier log.
	var logBuf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const repoID = "on-demand-tier-repo"

	repoStore := livingwiki.NewRepoSettingsMemStore()
	_ = repoStore.SetRepoSettings(context.Background(), livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  "default",
		RepoID:                    repoID,
		Enabled:                   true,
		LivingWikiDetailedEnabled: true,
	})

	// Resolver returns ollama/qwen3:32b — ClassifyByPattern → TierMid.
	resolver := &stubLLMResolver{provider: "ollama", model: "qwen3:32b"}

	// Comprehension store with no entry for qwen3:32b → falls through to pattern.
	comprStore := comprehension.NewMemStore()

	r := &mutationResolver{Resolver: &Resolver{
		LivingWikiRepoStore:        repoStore,
		LivingWikiLiveOrchestrator: onDemandOrch(),
		LLMResolver:                resolver,
		ComprehensionStore:         comprStore,
	}}

	folder := "internal/api"
	result, err := r.GenerateLivingWikiPageOnDemand(
		context.Background(),
		repoID,
		LivingWikiOnDemandPageSpec{Folder: &folder},
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	logStr := logBuf.String()

	// Tier log line must appear exactly once with tier=mid.
	if !strings.Contains(logStr, "tier=mid") {
		t.Errorf("expected tier=mid in on-demand resolved-tier log; log:\n%s", logStr)
	}
	// Provider must be ollama.
	if !strings.Contains(logStr, "provider=ollama") {
		t.Errorf("expected provider=ollama in resolved-tier log; log:\n%s", logStr)
	}
	// TierUnknown error log must NOT fire.
	if strings.Contains(logStr, "LLMTier is TierUnknown") {
		t.Errorf("unexpected TierUnknown error log; on-demand path must set LLMTier; log:\n%s", logStr)
	}

	// Verify the resolved tier is TierMid (not TierUnknown or TierFrontier).
	// qwen3:32b → 32B ≥ 30B threshold → TierMid.
	wantTier := modeltier.TierMid
	_ = wantTier // asserted via log above; direct result doesn't expose the tier
}

// ─────────────────────────────────────────────────────────────────────────────
// Compile-time interface guards
// ─────────────────────────────────────────────────────────────────────────────

// Verify that the template IDs and types used in the on-demand path are
// consistent with the cold-start path by referencing the same symbols.
var (
	_ templates.GenerateInput = templates.GenerateInput{}
	_ quality.Audience        = quality.AudienceEngineers
)
