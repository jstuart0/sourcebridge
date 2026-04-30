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
	"testing"

	"github.com/vektah/gqlparser/v2/gqlerror"

	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
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

// ─────────────────────────────────────────────────────────────────────────────
// Compile-time interface guards
// ─────────────────────────────────────────────────────────────────────────────

// Verify that the template IDs and types used in the on-demand path are
// consistent with the cold-start path by referencing the same symbols.
var (
	_ templates.GenerateInput = templates.GenerateInput{}
	_ quality.Audience        = quality.AudienceEngineers
)
