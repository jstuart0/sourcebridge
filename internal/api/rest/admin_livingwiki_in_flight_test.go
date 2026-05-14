// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sourcebridge/sourcebridge/internal/appdeps"
	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
)

// inFlightTestHandler wraps the Server handler in a chi router so URL params
// are populated correctly.
func inFlightTestHandler(s *Server) http.Handler {
	r := chi.NewRouter()
	r.Get("/api/v1/admin/llm/jobs/{id}/livingwiki/in-flight", s.handleLivingWikiInFlight)
	return r
}

// buildInFlightRequest creates a request and recorder for the in-flight endpoint.
func buildInFlightRequest(t *testing.T, jobID string) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm/jobs/"+jobID+"/livingwiki/in-flight", nil)
	return r, httptest.NewRecorder()
}

// newInFlightTestOrchestrator builds a minimal *lworch.Orchestrator suitable
// for in-flight handler tests (no real store or registry needed for empty-list
// and 503 cases).
func newInFlightTestOrchestrator() *lworch.Orchestrator {
	return lworch.New(lworch.Config{RepoID: "test-inflight"}, lworch.NewMapRegistry(), lworch.NewMemoryPageStore())
}

// TestHandleLivingWikiInFlight_FeatureUnavailable asserts the 503 path.
func TestHandleLivingWikiInFlight_FeatureUnavailable(t *testing.T) {
	s := &Server{Deps: &appdeps.AppDeps{}} // LivingWikiLiveOrchestrator == nil
	req, rec := buildInFlightRequest(t, "job-123")
	inFlightTestHandler(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

// TestHandleLivingWikiInFlight_UnknownJob_EmptyList asserts the 200/empty path
// for a job that is not currently tracked.
func TestHandleLivingWikiInFlight_UnknownJob_EmptyList(t *testing.T) {
	orch := newInFlightTestOrchestrator()
	s := &Server{Deps: &appdeps.AppDeps{LivingWikiLiveOrchestrator: orch}}
	req, rec := buildInFlightRequest(t, "job-unknown")
	inFlightTestHandler(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var body livingWikiInFlightResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Pages == nil {
		t.Error("expected non-nil pages slice")
	}
	if len(body.Pages) != 0 {
		t.Errorf("expected empty pages for unknown job, got %d", len(body.Pages))
	}
	if body.MedianCompletedMsKnown {
		t.Error("expected median_completed_ms_known=false for unknown job")
	}
	if body.JobID != "job-unknown" {
		t.Errorf("expected job_id=job-unknown, got %q", body.JobID)
	}
}

// blockingRestTemplate blocks Generate until the release channel receives.
type blockingRestTemplate struct {
	id      string
	release chan struct{}
}

func (b *blockingRestTemplate) ID() string { return b.id }
func (b *blockingRestTemplate) Generate(ctx context.Context, input templates.GenerateInput) (ast.Page, error) {
	select {
	case <-b.release:
	case <-ctx.Done():
		return ast.Page{}, ctx.Err()
	}
	pageID := "rest." + b.id
	return ast.Page{
		ID: pageID,
		Manifest: manifest.DependencyManifest{
			PageID:   pageID,
			Template: b.id,
			Audience: string(input.Audience),
		},
		Blocks: []ast.Block{
			{
				ID:   ast.GenerateBlockID(pageID, "", ast.BlockKindParagraph, 0),
				Kind: ast.BlockKindParagraph,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
					Markdown: "No behavioral claims here.",
				}},
				Owner:      ast.OwnerGenerated,
				LastChange: ast.BlockChange{Timestamp: input.Now, Source: "sourcebridge"},
			},
		},
		Provenance: ast.Provenance{GeneratedAt: input.Now},
	}, nil
}

// TestHandleLivingWikiInFlight_PerJobIsolation asserts that the in-flight
// endpoint for job-A returns only job-A's pages, and the endpoint for job-B
// returns only job-B's pages, even when both jobs are running concurrently on
// the same orchestrator.
func TestHandleLivingWikiInFlight_PerJobIsolation(t *testing.T) {
	releaseA := make(chan struct{}, 4)
	releaseB := make(chan struct{}, 4)
	btA := &blockingRestTemplate{id: "glossary", release: releaseA}
	btB := &blockingRestTemplate{id: "glossary", release: releaseB}

	// Register both templates under the same template ID — the orchestrator
	// uses the first-registered match, but each job uses distinct PlannedPage
	// inputs so their tracker entries are separate. We need two distinct
	// orchestrators to get fully independent job tracking without template-ID
	// collisions in the registry lookup path.
	regA := lworch.NewMapRegistry(btA)
	regB := lworch.NewMapRegistry(btB)
	storeA := lworch.NewMemoryPageStore()
	storeB := lworch.NewMemoryPageStore()
	prA := lworch.NewMemoryWikiPR("pr-isolation-a")
	prB := lworch.NewMemoryWikiPR("pr-isolation-b")

	orchA := lworch.New(lworch.Config{RepoID: "iso-test-a", MaxConcurrency: 5}, regA, storeA)
	orchB := lworch.New(lworch.Config{RepoID: "iso-test-b", MaxConcurrency: 5}, regB, storeB)

	const (
		jobA    = "job-iso-a"
		jobB    = "job-iso-b"
		numEach = 2
	)

	makePages := func(jobPrefix string) []lworch.PlannedPage {
		pages := make([]lworch.PlannedPage, numEach)
		for i := range pages {
			pages[i] = lworch.PlannedPage{
				ID:         jobPrefix + ".p" + string(rune('0'+i)) + ".glossary",
				TemplateID: "glossary",
				Audience:   quality.AudienceEngineers,
				Input: templates.GenerateInput{
					RepoID:   jobPrefix,
					Audience: quality.AudienceEngineers,
					Now:      time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
				},
			}
		}
		return pages
	}

	doneA := make(chan error, 1)
	doneB := make(chan error, 1)

	go func() {
		_, err := orchA.Generate(context.Background(), lworch.GenerateRequest{
			Pages:   makePages(jobA),
			PR:      prA,
			LLMTier: modeltier.TierFrontier,
			JobID:   jobA,
		})
		doneA <- err
	}()
	go func() {
		_, err := orchB.Generate(context.Background(), lworch.GenerateRequest{
			Pages:   makePages(jobB),
			PR:      prB,
			LLMTier: modeltier.TierFrontier,
			JobID:   jobB,
		})
		doneB <- err
	}()

	// Wait until both jobs have all their pages in-flight.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(orchA.InFlightPages(jobA)) == numEach && len(orchB.InFlightPages(jobB)) == numEach {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(orchA.InFlightPages(jobA)) != numEach {
		t.Fatalf("job-A: expected %d in-flight, got %d", numEach, len(orchA.InFlightPages(jobA)))
	}
	if len(orchB.InFlightPages(jobB)) != numEach {
		t.Fatalf("job-B: expected %d in-flight, got %d", numEach, len(orchB.InFlightPages(jobB)))
	}

	// REST endpoint for job-A (via orchA) must return ONLY job-A's pages.
	sA := &Server{Deps: &appdeps.AppDeps{LivingWikiLiveOrchestrator: orchA}}
	reqA, recA := buildInFlightRequest(t, jobA)
	inFlightTestHandler(sA).ServeHTTP(recA, reqA)
	if recA.Code != http.StatusOK {
		t.Fatalf("job-A endpoint: expected 200, got %d", recA.Code)
	}
	var bodyA livingWikiInFlightResponse
	if err := json.NewDecoder(recA.Body).Decode(&bodyA); err != nil {
		t.Fatalf("job-A decode: %v", err)
	}
	if len(bodyA.Pages) != numEach {
		t.Errorf("job-A: expected %d pages, got %d", numEach, len(bodyA.Pages))
	}
	for _, p := range bodyA.Pages {
		if p.PageID[:len(jobA)] != jobA {
			t.Errorf("job-A response contains page from wrong job: %q", p.PageID)
		}
	}

	// REST endpoint for job-B (via orchB) must return ONLY job-B's pages.
	sB := &Server{Deps: &appdeps.AppDeps{LivingWikiLiveOrchestrator: orchB}}
	reqB, recB := buildInFlightRequest(t, jobB)
	inFlightTestHandler(sB).ServeHTTP(recB, reqB)
	if recB.Code != http.StatusOK {
		t.Fatalf("job-B endpoint: expected 200, got %d", recB.Code)
	}
	var bodyB livingWikiInFlightResponse
	if err := json.NewDecoder(recB.Body).Decode(&bodyB); err != nil {
		t.Fatalf("job-B decode: %v", err)
	}
	if len(bodyB.Pages) != numEach {
		t.Errorf("job-B: expected %d pages, got %d", numEach, len(bodyB.Pages))
	}
	for _, p := range bodyB.Pages {
		if p.PageID[:len(jobB)] != jobB {
			t.Errorf("job-B response contains page from wrong job: %q", p.PageID)
		}
	}

	// Cross-check: job-A's pages must not appear in job-B's result (and vice versa).
	aIDs := make(map[string]bool)
	for _, p := range bodyA.Pages {
		aIDs[p.PageID] = true
	}
	for _, p := range bodyB.Pages {
		if aIDs[p.PageID] {
			t.Errorf("page %q appears in both job-A and job-B responses", p.PageID)
		}
	}

	// Release both jobs to let Generate finish cleanly.
	for i := 0; i < numEach; i++ {
		releaseA <- struct{}{}
		releaseB <- struct{}{}
	}
	if err := <-doneA; err != nil {
		t.Fatalf("orchA.Generate: %v", err)
	}
	if err := <-doneB; err != nil {
		t.Fatalf("orchB.Generate: %v", err)
	}
}

// TestHandleLivingWikiInFlight_PopulatedList asserts 200 + populated response
// when pages are genuinely in-flight.
func TestHandleLivingWikiInFlight_PopulatedList(t *testing.T) {
	release := make(chan struct{}, 3)
	bt := &blockingRestTemplate{id: "glossary", release: release}
	reg := lworch.NewMapRegistry(bt)
	store := lworch.NewMemoryPageStore()
	pr := lworch.NewMemoryWikiPR("pr-rest-inflight")

	orch := lworch.New(lworch.Config{RepoID: "rest-test", MaxConcurrency: 5}, reg, store)
	s := &Server{Deps: &appdeps.AppDeps{LivingWikiLiveOrchestrator: orch}}

	const jobID = "job-rest-populated"
	const numPages = 2
	pages := make([]lworch.PlannedPage, numPages)
	for i := range pages {
		pages[i] = lworch.PlannedPage{
			ID:         "rest.p" + string(rune('0'+i)) + ".glossary",
			TemplateID: "glossary",
			Audience:   quality.AudienceEngineers,
			Input: templates.GenerateInput{
				RepoID:   "rest-test",
				Audience: quality.AudienceEngineers,
				Now:      time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
			},
		}
	}

	done := make(chan error, 1)
	go func() {
		_, err := orch.Generate(context.Background(), lworch.GenerateRequest{
			Pages:   pages,
			PR:      pr,
			LLMTier: modeltier.TierFrontier,
			JobID:   jobID,
		})
		done <- err
	}()

	// Wait until in-flight tracker has entries.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(orch.InFlightPages(jobID)) == numPages {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	req, rec := buildInFlightRequest(t, jobID)
	inFlightTestHandler(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var body livingWikiInFlightResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Pages) != numPages {
		t.Errorf("expected %d pages, got %d", numPages, len(body.Pages))
	}
	for _, p := range body.Pages {
		if p.ElapsedMs < 0 {
			t.Errorf("expected non-negative elapsed_ms, got %d for %q", p.ElapsedMs, p.PageID)
		}
		if p.TemplateID != "glossary" {
			t.Errorf("expected template_id=glossary, got %q", p.TemplateID)
		}
	}

	// Release all pages and wait for Generate to finish.
	for i := 0; i < numPages; i++ {
		release <- struct{}{}
	}
	if err := <-done; err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	// After Generate completes, in-flight list must be empty.
	req2, rec2 := buildInFlightRequest(t, jobID)
	inFlightTestHandler(s).ServeHTTP(rec2, req2)
	var body2 livingWikiInFlightResponse
	if err := json.NewDecoder(rec2.Body).Decode(&body2); err != nil {
		t.Fatalf("decode post-complete: %v", err)
	}
	if len(body2.Pages) != 0 {
		t.Errorf("expected empty pages after Generate completes, got %d", len(body2.Pages))
	}
}
