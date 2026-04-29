// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
)

// ─────────────────────────────────────────────────────────────────────────────
// Resilience tests for slice 2 of plan
// 2026-04-29-livingwiki-cold-start-progress.md.
//
// Cover:
//   - per-page LLM errors are soft-failed into the result.Excluded set,
//     not propagated as fatal eg.Wait errors
//   - the systemic-failure breaker trips when many same-category errors
//     accumulate, and is wrapped with the ErrSystemicSoftFailures sentinel
//   - the breaker does not false-trip on an in-flight wave of failures
//     under high concurrency
//   - successful pages are persisted even when the run aborts on
//     time-budget or systemic failure
//   - template-not-found is fatal (not a soft-fail)
// ─────────────────────────────────────────────────────────────────────────────

// erroringTemplate returns the configured error from Generate. It also tracks
// the per-page-ID error to allow shaping outcomes for specific pages.
type erroringTemplate struct {
	id          string
	defaultErr  error                       // returned by default
	perPageErr  map[string]error            // keyed by RepoID (which we use as page-bucket key in tests)
	delay       time.Duration               // optional sleep before returning
	mu          sync.Mutex
	successResp string                      // markdown returned when no error is configured
	calls       int32                       // total Generate calls
}

func (e *erroringTemplate) ID() string { return e.id }

func (e *erroringTemplate) Generate(ctx context.Context, input templates.GenerateInput) (ast.Page, error) {
	atomic.AddInt32(&e.calls, 1)
	if e.delay > 0 {
		select {
		case <-time.After(e.delay):
		case <-ctx.Done():
			return ast.Page{}, ctx.Err()
		}
	}
	e.mu.Lock()
	if err, ok := e.perPageErr[input.RepoID]; ok {
		e.mu.Unlock()
		return ast.Page{}, err
	}
	e.mu.Unlock()
	if e.defaultErr != nil {
		return ast.Page{}, e.defaultErr
	}
	pageID := input.RepoID + "." + e.id
	return ast.Page{
		ID: pageID,
		Manifest: manifest.DependencyManifest{
			PageID:   pageID,
			Template: e.id,
			Audience: string(input.Audience),
		},
		Blocks: []ast.Block{
			{
				ID:   ast.GenerateBlockID(pageID, "", ast.BlockKindParagraph, 0),
				Kind: ast.BlockKindParagraph,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
					Markdown: e.successResp,
				}},
				Owner: ast.OwnerGenerated,
			},
		},
		Provenance: ast.Provenance{GeneratedAt: input.Now},
	}, nil
}

// glossaryPassMarkdown is content that passes the glossary profile (single
// factual_grounding gate; non-behavioral prose without citations is fine).
const glossaryPassMarkdown = "Middleware wraps an HTTP handler. No behavioral claims here."

// makeGlossaryPages returns N planned pages using the "glossary" template,
// each with a unique RepoID so the template generates distinct page IDs.
func makeGlossaryPages(n int, idPrefix string) []orchestrator.PlannedPage {
	pages := make([]orchestrator.PlannedPage, n)
	for i := range pages {
		input := templates.GenerateInput{
			RepoID:   fmt.Sprintf("%s-%d", idPrefix, i),
			Audience: quality.AudienceEngineers,
			Now:      time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC),
		}
		pages[i] = orchestrator.PlannedPage{
			ID:         fmt.Sprintf("%s-%d.glossary", idPrefix, i),
			TemplateID: "glossary",
			Audience:   quality.AudienceEngineers,
			Input:      input,
		}
	}
	return pages
}

// TestSoftFailureOnLLMError — page 0/3 returns context.DeadlineExceeded; pages
// 1-2 succeed. Verify Generate returns nil error, result.Generated has 2,
// result.Excluded has 1 with Reason="llm_error", FailureCategory="deadline_exceeded".
func TestSoftFailureOnLLMError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	pages := makeGlossaryPages(3, "soft")
	tmpl := &erroringTemplate{
		id:          "glossary",
		successResp: glossaryPassMarkdown,
		perPageErr: map[string]error{
			pages[0].Input.RepoID: context.DeadlineExceeded,
		},
	}

	reg := orchestrator.NewMapRegistry(tmpl)
	store := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR("pr-soft")
	orch := orchestrator.New(orchestrator.Config{RepoID: "test"}, reg, store)
	result, err := orch.Generate(ctx, orchestrator.GenerateRequest{Pages: pages, PR: pr})
	if err != nil {
		t.Fatalf("Generate returned unexpected error: %v", err)
	}
	if got, want := len(result.Generated), 2; got != want {
		t.Errorf("Generated count: got %d, want %d", got, want)
	}
	if got, want := len(result.Excluded), 1; got != want {
		t.Fatalf("Excluded count: got %d, want %d", got, want)
	}
	excl := result.Excluded[0]
	if excl.PageID != pages[0].ID {
		t.Errorf("Excluded page ID: got %q, want %q", excl.PageID, pages[0].ID)
	}
	if excl.Reason != orchestrator.ExclusionReasonLLMError {
		t.Errorf("Excluded.Reason: got %q, want %q", excl.Reason, orchestrator.ExclusionReasonLLMError)
	}
	if excl.FailureCategory != orchestrator.SoftFailureCategoryDeadlineExceeded {
		t.Errorf("Excluded.FailureCategory: got %q, want %q",
			excl.FailureCategory, orchestrator.SoftFailureCategoryDeadlineExceeded)
	}
	// Successes were persisted (PR mode, so SetProposed was called).
	for _, p := range result.Generated {
		_, ok, err := store.GetProposed(ctx, "test", "pr-soft", p.ID)
		if err != nil {
			t.Errorf("GetProposed(%q) returned error: %v", p.ID, err)
		}
		if !ok {
			t.Errorf("expected proposed page %q to be stored", p.ID)
		}
	}
}

// TestAbortsOnSystemicFailure — 20 pages all return DeadlineExceeded. With
// the default threshold (max(MaxConcurrency+1, 15) = 15 for MaxConcurrency=5),
// the breaker should trip once 15 same-category failures accumulate. Verify
// Generate returns ErrSystemicSoftFailures.
func TestAbortsOnSystemicFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	pages := makeGlossaryPages(20, "sys")
	perPage := make(map[string]error, len(pages))
	for _, p := range pages {
		perPage[p.Input.RepoID] = context.DeadlineExceeded
	}
	tmpl := &erroringTemplate{
		id:          "glossary",
		successResp: glossaryPassMarkdown,
		perPageErr:  perPage,
	}

	reg := orchestrator.NewMapRegistry(tmpl)
	store := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR("pr-sys")
	orch := orchestrator.New(orchestrator.Config{
		RepoID:         "test",
		MaxConcurrency: 1, // serialize so completion order is deterministic
	}, reg, store)

	_, err := orch.Generate(ctx, orchestrator.GenerateRequest{Pages: pages, PR: pr})
	if err == nil {
		t.Fatalf("expected ErrSystemicSoftFailures, got nil")
	}
	if !errors.Is(err, orchestrator.ErrSystemicSoftFailures) {
		t.Errorf("expected errors.Is(err, ErrSystemicSoftFailures); got %v", err)
	}
	if !orchestrator.IsPartialGenerationError(err) {
		t.Errorf("expected IsPartialGenerationError(err) to be true; err=%v", err)
	}
}

// TestSlidingWindowDoesNotFalseAbortUnderConcurrency — 30 pages, half fail
// the same category, half succeed. With MaxConcurrency=12, a same-category
// failure count of 15 (default threshold) should be reachable but the
// successes should recycle out of the window before the threshold is hit.
//
// The test is constructed so that successes outpace failures (we alternate
// the perPageErr map so every other page fails). Across 30 completions,
// 15 fail and 15 succeed; the window holds the most recent 30 completions
// so the failure count caps at 15 — at-the-edge of the threshold. To make
// the test deterministic, we use 11 failures + 19 successes, which always
// stays below the 15 threshold.
func TestSlidingWindowDoesNotFalseAbortUnderConcurrency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const total = 30
	const failures = 11 // strictly below threshold
	pages := makeGlossaryPages(total, "concur")
	perPage := make(map[string]error, failures)
	for i := 0; i < failures; i++ {
		perPage[pages[i].Input.RepoID] = context.DeadlineExceeded
	}
	tmpl := &erroringTemplate{
		id:          "glossary",
		successResp: glossaryPassMarkdown,
		perPageErr:  perPage,
	}

	reg := orchestrator.NewMapRegistry(tmpl)
	store := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR("pr-concur")
	orch := orchestrator.New(orchestrator.Config{
		RepoID:         "test",
		MaxConcurrency: 12,
	}, reg, store)

	result, err := orch.Generate(ctx, orchestrator.GenerateRequest{Pages: pages, PR: pr})
	if err != nil {
		t.Fatalf("Generate returned unexpected error: %v", err)
	}
	if got, want := len(result.Generated), total-failures; got != want {
		t.Errorf("Generated count: got %d, want %d", got, want)
	}
	if got, want := len(result.Excluded), failures; got != want {
		t.Errorf("Excluded count: got %d, want %d", got, want)
	}
}

// TestTimeBudgetAbortPersistsCompletedPages — pages 0-1 return immediately;
// pages 2-4 sleep 200ms each. With TimeBudget=50ms, the run aborts before
// the slow pages complete. Verify ErrTimeBudgetExceeded is returned AND the
// fast pages are persisted in the store.
//
// Codex r2 [Medium]: this test wraps the page store in ctxSensingStore so
// the assertion proves the persistence phase was called with a LIVE
// context — not the deadline-cancelled runCtx. If the implementation
// regressed to writing with the cancelled ctx, ctxSensingStore would
// reject the SetProposed call and the test would fail.
func TestTimeBudgetAbortPersistsCompletedPages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	pages := makeGlossaryPages(5, "tb")

	// Fast pages: pages[0], pages[1] — no delay.
	// Slow pages: pages[2..4] — 200ms delay.
	tmpl := &delayedTemplate{
		id:          "glossary",
		successResp: glossaryPassMarkdown,
		slowRepos:   map[string]bool{},
	}
	for i := 2; i < 5; i++ {
		tmpl.slowRepos[pages[i].Input.RepoID] = true
	}
	tmpl.slowDelay = 200 * time.Millisecond

	reg := orchestrator.NewMapRegistry(tmpl)
	inner := orchestrator.NewMemoryPageStore()
	store := &ctxSensingStore{inner: inner}
	pr := orchestrator.NewMemoryWikiPR("pr-tb")
	orch := orchestrator.New(orchestrator.Config{
		RepoID:         "test",
		MaxConcurrency: 5, // run all in parallel so fast ones finish quickly
		TimeBudget:     50 * time.Millisecond,
	}, reg, store)

	result, err := orch.Generate(ctx, orchestrator.GenerateRequest{Pages: pages, PR: pr})
	if !errors.Is(err, orchestrator.ErrTimeBudgetExceeded) {
		t.Fatalf("expected ErrTimeBudgetExceeded; got %v", err)
	}
	if len(result.Generated) < 2 {
		t.Errorf("expected at least 2 pages generated (the fast ones); got %d", len(result.Generated))
	}
	// The fast pages must be persisted under a LIVE (not cancelled) ctx.
	for _, p := range result.Generated {
		_, ok, err := inner.GetProposed(ctx, "test", "pr-tb", p.ID)
		if err != nil {
			t.Errorf("GetProposed(%q): %v", p.ID, err)
		}
		if !ok {
			t.Errorf("expected proposed page %q to be persisted under PR mode", p.ID)
		}
	}
	// Codex r2: if any SetProposed write was attempted with a cancelled
	// context, the sensing store would have recorded that here.
	if cancelledWrites := store.cancelledWrites(); cancelledWrites > 0 {
		t.Errorf("expected 0 SetProposed writes with cancelled ctx; got %d", cancelledWrites)
	}
}

// TestPersistenceFailureMidLoopReportsOnlyDurablePages — pages 0-4 all
// succeed in goroutines, but the store rejects SetProposed for page 3.
// Verify result.Generated only includes pages 0-2 (the durably-persisted
// ones), NOT pages 3-4. Codex r2b [Medium]: prevents overstating durable
// generated count when persistence fails mid-loop.
func TestPersistenceFailureMidLoopReportsOnlyDurablePages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	pages := makeGlossaryPages(5, "persistfail")
	tmpl := &erroringTemplate{
		id:          "glossary",
		successResp: glossaryPassMarkdown,
	}

	reg := orchestrator.NewMapRegistry(tmpl)
	inner := orchestrator.NewMemoryPageStore()
	// Reject the 3rd SetProposed call.
	store := &rejectAfterNStore{
		inner:        inner,
		rejectAfterN: 3,
	}
	pr := orchestrator.NewMemoryWikiPR("pr-pf")
	orch := orchestrator.New(orchestrator.Config{
		RepoID:         "test",
		MaxConcurrency: 1, // serialize so rejection is deterministic
	}, reg, store)

	result, err := orch.Generate(ctx, orchestrator.GenerateRequest{Pages: pages, PR: pr})
	if err == nil {
		t.Fatalf("expected error from rejected SetProposed; got nil")
	}
	// result.Generated must reflect only pages durably persisted (the
	// first 3, before the rejection).
	if got, want := len(result.Generated), 3; got != want {
		t.Errorf("Generated count: got %d, want %d (pages durably persisted before rejection)", got, want)
	}
	for _, p := range result.Generated {
		_, ok, _ := inner.GetProposed(ctx, "test", "pr-pf", p.ID)
		if !ok {
			t.Errorf("result.Generated includes page %q that is NOT in the store", p.ID)
		}
	}
}

// rejectAfterNStore wraps a page store and rejects SetProposed after the
// first N successful calls. Lets us simulate a store failure mid-persistence.
type rejectAfterNStore struct {
	inner        orchestrator.PageStore
	mu           sync.Mutex
	rejectAfterN int
	calls        int
}

func (r *rejectAfterNStore) GetCanonical(ctx context.Context, repoID, pageID string) (ast.Page, bool, error) {
	return r.inner.GetCanonical(ctx, repoID, pageID)
}
func (r *rejectAfterNStore) SetCanonical(ctx context.Context, repoID string, page ast.Page) error {
	r.mu.Lock()
	r.calls++
	rejected := r.calls > r.rejectAfterN
	r.mu.Unlock()
	if rejected {
		return fmt.Errorf("rejectAfterNStore: refusing call %d (limit %d)", r.calls, r.rejectAfterN)
	}
	return r.inner.SetCanonical(ctx, repoID, page)
}
func (r *rejectAfterNStore) DeleteCanonical(ctx context.Context, repoID, pageID string) error {
	return r.inner.DeleteCanonical(ctx, repoID, pageID)
}
func (r *rejectAfterNStore) GetProposed(ctx context.Context, repoID, prID, pageID string) (ast.Page, bool, error) {
	return r.inner.GetProposed(ctx, repoID, prID, pageID)
}
func (r *rejectAfterNStore) SetProposed(ctx context.Context, repoID, prID string, page ast.Page) error {
	r.mu.Lock()
	r.calls++
	rejected := r.calls > r.rejectAfterN
	r.mu.Unlock()
	if rejected {
		return fmt.Errorf("rejectAfterNStore: refusing call %d (limit %d)", r.calls, r.rejectAfterN)
	}
	return r.inner.SetProposed(ctx, repoID, prID, page)
}
func (r *rejectAfterNStore) ListProposed(ctx context.Context, repoID, prID string) ([]ast.Page, error) {
	return r.inner.ListProposed(ctx, repoID, prID)
}
func (r *rejectAfterNStore) DeleteProposed(ctx context.Context, repoID, prID string) error {
	return r.inner.DeleteProposed(ctx, repoID, prID)
}
func (r *rejectAfterNStore) PromoteProposed(ctx context.Context, repoID, prID string) error {
	return r.inner.PromoteProposed(ctx, repoID, prID)
}

// ctxSensingStore wraps an orchestrator.PageStore and records whenever a
// write call receives an already-cancelled context. Used by the time-budget
// partial-persistence test to PROVE the orchestrator switched to a fresh
// context for the persistence phase (rather than reusing the deadline-
// cancelled runCtx).
type ctxSensingStore struct {
	inner          orchestrator.PageStore
	mu             sync.Mutex
	cancelledCount int
}

func (c *ctxSensingStore) recordIfCancelled(ctx context.Context) {
	if ctx.Err() != nil {
		c.mu.Lock()
		c.cancelledCount++
		c.mu.Unlock()
	}
}

func (c *ctxSensingStore) cancelledWrites() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cancelledCount
}

func (c *ctxSensingStore) GetCanonical(ctx context.Context, repoID, pageID string) (ast.Page, bool, error) {
	return c.inner.GetCanonical(ctx, repoID, pageID)
}
func (c *ctxSensingStore) SetCanonical(ctx context.Context, repoID string, page ast.Page) error {
	c.recordIfCancelled(ctx)
	return c.inner.SetCanonical(ctx, repoID, page)
}
func (c *ctxSensingStore) DeleteCanonical(ctx context.Context, repoID, pageID string) error {
	return c.inner.DeleteCanonical(ctx, repoID, pageID)
}
func (c *ctxSensingStore) GetProposed(ctx context.Context, repoID, prID, pageID string) (ast.Page, bool, error) {
	return c.inner.GetProposed(ctx, repoID, prID, pageID)
}
func (c *ctxSensingStore) SetProposed(ctx context.Context, repoID, prID string, page ast.Page) error {
	c.recordIfCancelled(ctx)
	return c.inner.SetProposed(ctx, repoID, prID, page)
}
func (c *ctxSensingStore) ListProposed(ctx context.Context, repoID, prID string) ([]ast.Page, error) {
	return c.inner.ListProposed(ctx, repoID, prID)
}
func (c *ctxSensingStore) DeleteProposed(ctx context.Context, repoID, prID string) error {
	return c.inner.DeleteProposed(ctx, repoID, prID)
}
func (c *ctxSensingStore) PromoteProposed(ctx context.Context, repoID, prID string) error {
	return c.inner.PromoteProposed(ctx, repoID, prID)
}

// TestTemplateNotFoundIsFatal — a planned page references a template that
// is NOT in the registry. Generate must return a fatal error, not soft-fail.
func TestTemplateNotFoundIsFatal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Register glossary; plan a page referencing "nonexistent".
	tmpl := &erroringTemplate{id: "glossary", successResp: glossaryPassMarkdown}
	reg := orchestrator.NewMapRegistry(tmpl)
	store := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR("pr-tnf")

	pages := []orchestrator.PlannedPage{
		{
			ID:         "test.bad",
			TemplateID: "nonexistent", // not in registry
			Audience:   quality.AudienceEngineers,
			Input: templates.GenerateInput{
				RepoID:   "tnf-0",
				Audience: quality.AudienceEngineers,
				Now:      time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC),
			},
		},
	}

	orch := orchestrator.New(orchestrator.Config{RepoID: "test"}, reg, store)
	_, err := orch.Generate(ctx, orchestrator.GenerateRequest{Pages: pages, PR: pr})
	if err == nil {
		t.Fatalf("expected fatal error for unknown template; got nil")
	}
	// Must NOT be one of the partial-generation classes.
	if errors.Is(err, orchestrator.ErrTimeBudgetExceeded) {
		t.Errorf("template-not-found error should not be classified as ErrTimeBudgetExceeded")
	}
	if errors.Is(err, orchestrator.ErrSystemicSoftFailures) {
		t.Errorf("template-not-found error should not be classified as ErrSystemicSoftFailures")
	}
	if orchestrator.IsPartialGenerationError(err) {
		t.Errorf("IsPartialGenerationError must be false for template-not-found; err=%v", err)
	}
	// And the message should reference the bad template ID.
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("expected error to mention template ID %q; got %v", "nonexistent", err)
	}
}

// delayedTemplate sleeps for slowDelay when input.RepoID is in slowRepos;
// otherwise returns immediately.
type delayedTemplate struct {
	id          string
	successResp string
	slowRepos   map[string]bool
	slowDelay   time.Duration
}

func (d *delayedTemplate) ID() string { return d.id }

func (d *delayedTemplate) Generate(ctx context.Context, input templates.GenerateInput) (ast.Page, error) {
	if d.slowRepos[input.RepoID] && d.slowDelay > 0 {
		select {
		case <-time.After(d.slowDelay):
		case <-ctx.Done():
			return ast.Page{}, ctx.Err()
		}
	}
	pageID := input.RepoID + "." + d.id
	return ast.Page{
		ID: pageID,
		Manifest: manifest.DependencyManifest{
			PageID:   pageID,
			Template: d.id,
			Audience: string(input.Audience),
		},
		Blocks: []ast.Block{
			{
				ID:   ast.GenerateBlockID(pageID, "", ast.BlockKindParagraph, 0),
				Kind: ast.BlockKindParagraph,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
					Markdown: d.successResp,
				}},
				Owner: ast.OwnerGenerated,
			},
		},
		Provenance: ast.Provenance{GeneratedAt: input.Now},
	}, nil
}
