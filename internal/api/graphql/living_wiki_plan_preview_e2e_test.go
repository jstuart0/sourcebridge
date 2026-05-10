// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// CA-146 Phase 4: handler-level integration test for the LIVING_WIKI_PLAN_STALE
// path. This test exercises the full GraphQL HTTP handler so it pins finding #1:
// signature-validation errors MUST surface to the browser (not get buried in the
// closure's job-error handler).

package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/sourcebridge/sourcebridge/internal/appdeps"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// newTestGQLServer spins up a real gqlgen HTTP server backed by the minimal
// Resolver wiring needed to exercise EnableLivingWikiForRepo signature
// validation. It returns the server and a cleanup function.
//
// The server shares the same Resolver construction pattern as the production
// router (router.go:689-740) but with in-memory stores, a stub cluster store,
// and a stub graph store — exactly what the resolver-level tests use.
func newTestGQLServer(t *testing.T, r *Resolver) *httptest.Server {
	t.Helper()
	srv := handler.NewDefaultServer(NewExecutableSchema(Config{
		Resolvers: r,
	}))
	return httptest.NewServer(srv)
}

// gqlRequest sends a single GraphQL request (POST /graphql) and returns the
// parsed response body as a raw map.
func gqlRequest(t *testing.T, srv *httptest.Server, query string, variables map[string]any) map[string]any {
	t.Helper()

	payload := map[string]any{
		"query":     query,
		"variables": variables,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := http.Post(srv.URL+"/graphql", "application/json", bytes.NewReader(body)) //nolint:noctx
	if err != nil {
		t.Fatalf("POST /graphql: %v", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal response: %v\nbody: %s", err, raw)
	}
	return result
}

// TestEnableLivingWikiForRepo_StaleSignaturePathReturnsFreshPlanInGraphQLResponse
// is the H4 acceptance test from codex r1. It exercises the full mutation through
// the GraphQL HTTP handler and asserts that LIVING_WIKI_PLAN_STALE plus a non-empty
// freshPlan extension reach response.Errors[0].Extensions — not the job-error handler.
//
// This test pins finding #1: the signature validation lives in the resolver BODY
// (not inside the goroutine closure) so gqlgen can propagate the error correctly.
func TestEnableLivingWikiForRepo_StaleSignaturePathReturnsFreshPlanInGraphQLResponse(t *testing.T) {
	t.Parallel()

	const repoID = "handler-stale-sig-repo"

	// ── wire Resolver ─────────────────────────────────────────────────────────
	repoStore := livingwiki.NewRepoSettingsMemStore()
	_ = repoStore.SetRepoSettings(context.Background(), livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  "default",
		RepoID:                    repoID,
		Enabled:                   true,
		MaxPagesPerJob:            500,
		LivingWikiDetailedEnabled: true,
		Sinks:                     []livingwiki.RepoWikiSink{{Kind: livingwiki.RepoWikiSinkGitRepo}},
	})
	globalStore := livingwiki.NewMemStore()
	enabled := true
	_ = globalStore.Set(&livingwiki.Settings{Enabled: &enabled})

	jobStore := llm.NewMemStore()
	llmOrch := orchestrator.New(jobStore, orchestrator.Config{MaxConcurrency: 1})
	defer func() { _ = llmOrch.Shutdown(2 * time.Second) }()

	cs := csClusterStore(2) // 2 cluster pages + 3 repo-wide = 5 total

	r := &Resolver{
		Deps: &appdeps.AppDeps{
			LivingWikiRepoStore: repoStore,
			LivingWikiStore:     globalStore,
			ClusterStore:        cs,
			Orchestrator:        llmOrch,
		},
		Store: newStubGraphStore(),
	}
	srv := newTestGQLServer(t, r)
	defer srv.Close()

	// ── Step 1: call previewLivingWikiPlan to capture a real planSignature ────
	previewQuery := `
		query PreviewPlan($repositoryId: ID!, $mode: LivingWikiBuildMode) {
			previewLivingWikiPlan(repositoryId: $repositoryId, mode: $mode) {
				planSignature
				totalPages
				pages { id pageType required }
			}
		}
	`
	previewVars := map[string]any{
		"repositoryId": repoID,
		"mode":         "DETAILED",
	}
	previewResp := gqlRequest(t, srv, previewQuery, previewVars)
	if errs, ok := previewResp["errors"]; ok {
		t.Fatalf("previewLivingWikiPlan returned errors: %v", errs)
	}

	previewData, ok := previewResp["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data object, got: %T", previewResp["data"])
	}
	previewPlan, ok := previewData["previewLivingWikiPlan"].(map[string]any)
	if !ok {
		t.Fatalf("expected previewLivingWikiPlan object, got: %T", previewData["previewLivingWikiPlan"])
	}
	capturedSig, ok := previewPlan["planSignature"].(string)
	if !ok || capturedSig == "" {
		t.Fatalf("expected non-empty planSignature, got: %v", previewPlan["planSignature"])
	}

	// ── Step 2: mutate underlying state to invalidate the signature ───────────
	// Change MaxPagesPerJob — effectiveCap changes → signature changes.
	_ = repoStore.SetRepoSettings(context.Background(), livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  "default",
		RepoID:                    repoID,
		Enabled:                   true,
		MaxPagesPerJob:            3, // was 500; now 3 → different effectiveCap → stale sig
		LivingWikiDetailedEnabled: true,
		Sinks:                     []livingwiki.RepoWikiSink{{Kind: livingwiki.RepoWikiSinkGitRepo}},
	})

	// ── Step 3: call enableLivingWikiForRepo with the now-stale signature ─────
	const enableMutation = `
		mutation Enable($input: EnableLivingWikiForRepoInput!) {
			enableLivingWikiForRepo(input: $input) {
				jobId
				notice
			}
		}
	`
	// Select any page ID from the preview (we just need a non-nil selectedPageIds).
	pages, _ := previewPlan["pages"].([]any)
	if len(pages) == 0 {
		t.Fatal("previewLivingWikiPlan returned no pages; cannot construct selectedPageIds")
	}
	firstPage, _ := pages[0].(map[string]any)
	firstPageID, _ := firstPage["id"].(string)

	enableVars := map[string]any{
		"input": map[string]any{
			"repositoryId": repoID,
			"mode":         "PR_REVIEW",
			"sinks": []map[string]any{{
				"kind":            "GIT_REPO",
				"integrationName": "test-git-repo",
				"audience":        "ENGINEER",
			}},
			"selectedPageIds": []string{firstPageID},
			"planSignature":   capturedSig, // stale after MaxPagesPerJob change
		},
	}
	enableResp := gqlRequest(t, srv, enableMutation, enableVars)

	// ── Assert: response.errors[0].extensions.code == LIVING_WIKI_PLAN_STALE ─
	rawErrors, hasErrors := enableResp["errors"]
	if !hasErrors {
		t.Fatalf("expected errors in response, got none; data=%v", enableResp["data"])
	}
	errList, ok := rawErrors.([]any)
	if !ok || len(errList) == 0 {
		t.Fatalf("expected non-empty errors array, got: %v", rawErrors)
	}
	firstErr, ok := errList[0].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got: %T", errList[0])
	}
	extensions, ok := firstErr["extensions"].(map[string]any)
	if !ok {
		t.Fatalf("expected extensions object in first error, got: %T (message=%v)", firstErr["extensions"], firstErr["message"])
	}
	code, _ := extensions["code"].(string)
	if code != "LIVING_WIKI_PLAN_STALE" {
		t.Errorf("extensions.code: got %q, want LIVING_WIKI_PLAN_STALE", code)
	}

	// ── Assert: extensions.freshPlan is non-empty with the new pages ──────────
	freshPlan, hasFreshPlan := extensions["freshPlan"]
	if !hasFreshPlan || freshPlan == nil {
		t.Fatalf("expected freshPlan extension to be present and non-nil; extensions=%v", extensions)
	}
	freshPlanMap, ok := freshPlan.(map[string]any)
	if !ok {
		t.Fatalf("expected freshPlan to be an object, got: %T", freshPlan)
	}
	freshPages, hasFreshPages := freshPlanMap["pages"]
	if !hasFreshPages {
		t.Fatal("freshPlan missing 'pages' field")
	}
	freshPagesList, ok := freshPages.([]any)
	if !ok || len(freshPagesList) == 0 {
		t.Errorf("freshPlan.pages: expected non-empty list, got: %v", freshPages)
	}
}
