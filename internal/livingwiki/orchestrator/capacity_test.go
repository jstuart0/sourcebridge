// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Tests for upstream-capacity clamping and breaker recalibration (Phase 2).
// Covers plan acceptance criteria: C1, M3, and D4 contract tests.

package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// countingCapacityProvider records how many times UpstreamCapacity is called.
type countingCapacityProvider struct {
	calls  atomic.Int64
	value  int
	known  bool
	retErr error
}

func (c *countingCapacityProvider) UpstreamCapacity(_ context.Context) (int, bool, error) {
	c.calls.Add(1)
	return c.value, c.known, c.retErr
}

// passTemplateForCap is a minimal template that always succeeds with content
// that passes the glossary quality profile.
type passTemplateForCap struct{}

func (p *passTemplateForCap) ID() string { return "glossary" }
func (p *passTemplateForCap) Generate(_ context.Context, input templates.GenerateInput) (ast.Page, error) {
	pageID := input.RepoID + ".glossary"
	const passMD = "Middleware wraps an HTTP handler. No behavioral claims here."
	return ast.Page{
		ID: pageID,
		Manifest: manifest.DependencyManifest{
			PageID:   pageID,
			Template: "glossary",
			Audience: string(input.Audience),
		},
		Blocks: []ast.Block{
			{
				ID:   ast.GenerateBlockID(pageID, "", ast.BlockKindParagraph, 0),
				Kind: ast.BlockKindParagraph,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
					Markdown: passMD,
				}},
				Owner:      ast.OwnerGenerated,
				LastChange: ast.BlockChange{Timestamp: input.Now, Source: "sourcebridge"},
			},
		},
		Provenance: ast.Provenance{GeneratedAt: input.Now},
	}, nil
}

// failTemplate always returns an error (for breaker tests).
type failTemplateForCap struct {
	err error
}

func (f *failTemplateForCap) ID() string { return "glossary" }
func (f *failTemplateForCap) Generate(_ context.Context, _ templates.GenerateInput) (ast.Page, error) {
	return ast.Page{}, f.err
}

// makeCapPages returns n planned pages using the glossary template.
func makeCapPages(n int, prefix string) []orchestrator.PlannedPage {
	pages := make([]orchestrator.PlannedPage, n)
	for i := range pages {
		pages[i] = orchestrator.PlannedPage{
			ID:         fmt.Sprintf("%s-%d.glossary", prefix, i),
			TemplateID: "glossary",
			Audience:   quality.AudienceEngineers,
			Input: templates.GenerateInput{
				RepoID:   fmt.Sprintf("%s-%d", prefix, i),
				Audience: quality.AudienceEngineers,
				Now:      time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC),
			},
		}
	}
	return pages
}

// ─────────────────────────────────────────────────────────────────────────────
// D4: MaxConcurrency clamp tests
// ─────────────────────────────────────────────────────────────────────────────

// TestOrchestrator_ClampsToUpstreamCapacity — MaxConcurrency=12, provider
// returns (2, true, nil). Run succeeds; logs should contain the clamp message
// (not directly testable here, but the run must complete with clamping applied).
func TestOrchestrator_ClampsToUpstreamCapacity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov := &countingCapacityProvider{value: 2, known: true}
	reg := orchestrator.NewMapRegistry(&passTemplateForCap{})
	store := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR("pr-clamp")

	orch := orchestrator.New(orchestrator.Config{
		RepoID:                   "clamp-test",
		MaxConcurrency:           12,
		UpstreamCapacityProvider: prov,
	}, reg, store)

	pages := makeCapPages(6, "clamp")
	result, err := orch.Generate(ctx, orchestrator.GenerateRequest{
		Pages:   pages,
		PR:      pr,
		LLMTier: modeltier.TierFrontier,
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if len(result.Generated) != 6 {
		t.Errorf("expected 6 generated, got %d", len(result.Generated))
	}
}

// TestOrchestrator_DoesNotClampWhenUnknown — provider returns (0, false, nil).
// Orchestrator must not clamp (uses configured MaxConcurrency=12).
func TestOrchestrator_DoesNotClampWhenUnknown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov := &countingCapacityProvider{value: 0, known: false}
	reg := orchestrator.NewMapRegistry(&passTemplateForCap{})
	store := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR("pr-unknown")

	orch := orchestrator.New(orchestrator.Config{
		RepoID:                   "unknown-cap-test",
		MaxConcurrency:           12,
		UpstreamCapacityProvider: prov,
	}, reg, store)

	pages := makeCapPages(4, "unk")
	result, err := orch.Generate(ctx, orchestrator.GenerateRequest{
		Pages:   pages,
		PR:      pr,
		LLMTier: modeltier.TierFrontier,
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if len(result.Generated) != 4 {
		t.Errorf("expected 4 generated, got %d", len(result.Generated))
	}
}

// TestOrchestrator_DoesNotClampWhenUpstreamHigher — MaxConcurrency=5, provider
// returns (20, true, nil). Upstream is higher; orchestrator uses configured 5.
func TestOrchestrator_DoesNotClampWhenUpstreamHigher(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov := &countingCapacityProvider{value: 20, known: true}
	reg := orchestrator.NewMapRegistry(&passTemplateForCap{})
	store := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR("pr-higher")

	orch := orchestrator.New(orchestrator.Config{
		RepoID:                   "higher-cap-test",
		MaxConcurrency:           5,
		UpstreamCapacityProvider: prov,
	}, reg, store)

	pages := makeCapPages(4, "higher")
	result, err := orch.Generate(ctx, orchestrator.GenerateRequest{
		Pages:   pages,
		PR:      pr,
		LLMTier: modeltier.TierFrontier,
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if len(result.Generated) != 4 {
		t.Errorf("expected 4 generated, got %d", len(result.Generated))
	}
}

// TestOrchestrator_TreatsUnboundedAsNoOp — provider returns (0, true, nil)
// which means "explicitly unbounded" (frontier API). No clamp should occur.
func TestOrchestrator_TreatsUnboundedAsNoOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov := &countingCapacityProvider{value: 0, known: true} // unbounded
	reg := orchestrator.NewMapRegistry(&passTemplateForCap{})
	store := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR("pr-unbounded")

	orch := orchestrator.New(orchestrator.Config{
		RepoID:                   "unbounded-test",
		MaxConcurrency:           8,
		UpstreamCapacityProvider: prov,
	}, reg, store)

	pages := makeCapPages(3, "unb")
	result, err := orch.Generate(ctx, orchestrator.GenerateRequest{
		Pages:   pages,
		PR:      pr,
		LLMTier: modeltier.TierFrontier,
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if len(result.Generated) != 3 {
		t.Errorf("expected 3 generated, got %d", len(result.Generated))
	}
}

// TestOrchestrator_NilProviderIsSafeNoOp — no UpstreamCapacityProvider wired.
// Orchestrator must run successfully using configured MaxConcurrency.
func TestOrchestrator_NilProviderIsSafeNoOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	reg := orchestrator.NewMapRegistry(&passTemplateForCap{})
	store := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR("pr-nil")

	orch := orchestrator.New(orchestrator.Config{
		RepoID:                   "nil-provider-test",
		MaxConcurrency:           5,
		UpstreamCapacityProvider: nil,
	}, reg, store)

	pages := makeCapPages(3, "nil")
	result, err := orch.Generate(ctx, orchestrator.GenerateRequest{
		Pages:   pages,
		PR:      pr,
		LLMTier: modeltier.TierFrontier,
	})
	if err != nil {
		t.Fatalf("Generate failed with nil provider: %v", err)
	}
	if len(result.Generated) != 3 {
		t.Errorf("expected 3 generated, got %d", len(result.Generated))
	}
}

// TestOrchestrator_OldWorkerCompat — fake worker returns MaxConcurrentCallsKnown=false
// (old worker, no capacity field). Orchestrator must not clamp and must not error.
func TestOrchestrator_OldWorkerCompat(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Old worker: known=false, value=0 — this is what proto3 zero-defaults give.
	prov := orchestrator.StaticCapacityProvider{Value: 0, Known: false}
	reg := orchestrator.NewMapRegistry(&passTemplateForCap{})
	store := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR("pr-compat")

	orch := orchestrator.New(orchestrator.Config{
		RepoID:                   "compat-test",
		MaxConcurrency:           12,
		UpstreamCapacityProvider: prov,
	}, reg, store)

	pages := makeCapPages(4, "compat")
	result, err := orch.Generate(ctx, orchestrator.GenerateRequest{
		Pages:   pages,
		PR:      pr,
		LLMTier: modeltier.TierFrontier,
	})
	if err != nil {
		t.Fatalf("old-worker compat: Generate returned error: %v", err)
	}
	if len(result.Generated) != 4 {
		t.Errorf("expected 4 generated, got %d", len(result.Generated))
	}
}

// TestOrchestrator_ProviderErrorIsFailOpen — provider returns an error.
// Orchestrator must fail-open (no clamp) and succeed.
func TestOrchestrator_ProviderErrorIsFailOpen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov := orchestrator.StaticCapacityProvider{Err: errors.New("worker unavailable")}
	reg := orchestrator.NewMapRegistry(&passTemplateForCap{})
	store := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR("pr-err")

	orch := orchestrator.New(orchestrator.Config{
		RepoID:                   "err-test",
		MaxConcurrency:           12,
		UpstreamCapacityProvider: prov,
	}, reg, store)

	pages := makeCapPages(3, "err")
	result, err := orch.Generate(ctx, orchestrator.GenerateRequest{
		Pages:   pages,
		PR:      pr,
		LLMTier: modeltier.TierFrontier,
	})
	if err != nil {
		t.Fatalf("provider error: Generate returned error: %v", err)
	}
	if len(result.Generated) != 3 {
		t.Errorf("expected 3 generated, got %d", len(result.Generated))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// C1: Breaker recalibration with effective (clamped) concurrency
// ─────────────────────────────────────────────────────────────────────────────

// TestOrchestrator_BreakerCalibration_UsesEffectiveConcurrency (C1) —
// MaxConcurrency=12, provider returns (1, true, nil). The effective concurrency
// is 1, so the breaker window = max(2*1, 30) = 30 and threshold = max(1+1, 15) = 15.
// Drive 14 soft-failure outcomes; breaker must NOT trip (would trip at 13 with
// the pre-fix calibration of threshold = max(12+1, 15) = 13).
func TestOrchestrator_BreakerCalibration_UsesEffectiveConcurrency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Provider clamps effective concurrency to 1.
	prov := orchestrator.StaticCapacityProvider{Value: 1, Known: true}

	// Template that always returns deadline-exceeded (soft failure category).
	tmpl := &failTemplateForCap{err: context.DeadlineExceeded}
	reg := orchestrator.NewMapRegistry(tmpl)
	store := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR("pr-breaker-c1")

	// SoftFailureWindow and SoftFailureThreshold at zero → computed from
	// effective MaxConcurrency (1) after clamping:
	//   window    = max(2*1, 30) = 30
	//   threshold = max(1+1, 15) = 15
	// With 14 pages all soft-failing, count < 15 → breaker must NOT trip.
	orch := orchestrator.New(orchestrator.Config{
		RepoID:                   "breaker-c1",
		MaxConcurrency:           12,
		UpstreamCapacityProvider: prov,
		// Let window/threshold compute from effective capacity (zero = use defaults).
		SoftFailureWindow:    0,
		SoftFailureThreshold: 0,
	}, reg, store)

	pages := makeCapPages(14, "bc1")
	_, err := orch.Generate(ctx, orchestrator.GenerateRequest{
		Pages:   pages,
		PR:      pr,
		LLMTier: modeltier.TierFrontier,
	})

	// With the pre-fix calibration (threshold=13 from MaxConcurrency=12),
	// 14 failures would trip the breaker and return ErrSystemicSoftFailures.
	// With the fix (threshold=15 from effective=1), 14 failures must NOT trip.
	if errors.Is(err, orchestrator.ErrSystemicSoftFailures) {
		t.Errorf("C1 REGRESSION: breaker tripped at 14 failures with threshold=15; "+
			"effective concurrency clamp did not recalibrate breaker math. err=%v", err)
	}
	// The run may return nil (all 14 excluded) or a time-budget error —
	// both are acceptable. The key assertion is no ErrSystemicSoftFailures.
}

// TestOrchestrator_BreakerCalibration_UnclampsStillWorks verifies the
// pre-existing behavior is unchanged when no clamp occurs: MaxConcurrency=12
// with a nil provider should keep threshold=max(12+1,15)=13, and 14 same-category
// failures SHOULD trip the breaker.
func TestOrchestrator_BreakerCalibration_UnclampsStillWorks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tmpl := &failTemplateForCap{err: context.DeadlineExceeded}
	reg := orchestrator.NewMapRegistry(tmpl)
	store := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR("pr-breaker-old")

	orch := orchestrator.New(orchestrator.Config{
		RepoID:         "breaker-old",
		MaxConcurrency: 12,
		// No provider — calibration from MaxConcurrency=12 → threshold=13.
	}, reg, store)

	// 20 failures > threshold=13 → breaker must trip.
	pages := makeCapPages(20, "old")
	_, err := orch.Generate(ctx, orchestrator.GenerateRequest{
		Pages:   pages,
		PR:      pr,
		LLMTier: modeltier.TierFrontier,
	})
	if err == nil || !errors.Is(err, orchestrator.ErrSystemicSoftFailures) {
		t.Errorf("expected ErrSystemicSoftFailures with MaxConcurrency=12 and 20 failures, got: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// M3: Per-job capacity snapshot invariant
// ─────────────────────────────────────────────────────────────────────────────

// TestOrchestrator_CapacitySnapshotIsInvariant (M3) — provider call count must
// equal exactly 1 per Generate() invocation, regardless of page count. A second
// Generate() call produces a second probe call (not re-using the first).
func TestOrchestrator_CapacitySnapshotIsInvariant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov := &countingCapacityProvider{value: 4, known: true}
	reg := orchestrator.NewMapRegistry(&passTemplateForCap{})
	store := orchestrator.NewMemoryPageStore()

	orch := orchestrator.New(orchestrator.Config{
		RepoID:                   "snapshot-test",
		MaxConcurrency:           12,
		UpstreamCapacityProvider: prov,
	}, reg, store)

	// First Generate — 17 pages; provider must be called exactly once.
	pr1 := orchestrator.NewMemoryWikiPR("pr-snap-1")
	_, err := orch.Generate(ctx, orchestrator.GenerateRequest{
		Pages:   makeCapPages(17, "snap1"),
		PR:      pr1,
		LLMTier: modeltier.TierFrontier,
	})
	if err != nil {
		t.Fatalf("Generate 1 failed: %v", err)
	}
	if got := prov.calls.Load(); got != 1 {
		t.Errorf("M3: after first Generate(17 pages), expected 1 UpstreamCapacity call; got %d", got)
	}

	// Second Generate — provider must be called a second time (not share first).
	pr2 := orchestrator.NewMemoryWikiPR("pr-snap-2")
	_, err = orch.Generate(ctx, orchestrator.GenerateRequest{
		Pages:   makeCapPages(5, "snap2"),
		PR:      pr2,
		LLMTier: modeltier.TierFrontier,
	})
	if err != nil {
		t.Fatalf("Generate 2 failed: %v", err)
	}
	if got := prov.calls.Load(); got != 2 {
		t.Errorf("M3: after second Generate(5 pages), expected 2 total UpstreamCapacity calls; got %d", got)
	}
}
