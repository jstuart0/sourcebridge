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
	s := &Server{} // livingWikiLiveOrchestrator == nil
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
	s := &Server{livingWikiLiveOrchestrator: orch}
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

// TestHandleLivingWikiInFlight_PopulatedList asserts 200 + populated response
// when pages are genuinely in-flight.
func TestHandleLivingWikiInFlight_PopulatedList(t *testing.T) {
	release := make(chan struct{}, 3)
	bt := &blockingRestTemplate{id: "glossary", release: release}
	reg := lworch.NewMapRegistry(bt)
	store := lworch.NewMemoryPageStore()
	pr := lworch.NewMemoryWikiPR("pr-rest-inflight")

	orch := lworch.New(lworch.Config{RepoID: "rest-test", MaxConcurrency: 5}, reg, store)
	s := &Server{livingWikiLiveOrchestrator: orch}

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
