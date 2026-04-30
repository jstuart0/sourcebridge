// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/clustering"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
)

// ---- LLM stubs ----

// constLLM always returns the same response.
type constLLM struct{ response string }

func (c *constLLM) Complete(_ context.Context, _, _ string) (string, error) {
	return c.response, nil
}

// roundRobinLLM cycles through a list of responses. Thread-safe.
type roundRobinLLM struct {
	mu        sync.Mutex
	responses []string
	idx       int
}

func (r *roundRobinLLM) Complete(_ context.Context, _, _ string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.responses) == 0 {
		return "No response configured.", nil
	}
	resp := r.responses[r.idx%len(r.responses)]
	r.idx++
	return resp, nil
}

// ---- Symbol graph stubs ----

// twoPackageGraph returns symbols for two packages.
type twoPackageGraph struct{}

func (twoPackageGraph) ExportedSymbols(_ string) ([]templates.Symbol, error) {
	return []templates.Symbol{
		{
			Package:    "internal/auth",
			Name:       "Middleware",
			Signature:  "func Middleware(next http.Handler) http.Handler",
			DocComment: "Middleware wraps an http.Handler with session verification.",
			FilePath:   "internal/auth/auth.go",
			StartLine:  12,
			EndLine:    25,
		},
		{
			Package:    "internal/billing",
			Name:       "Charge",
			Signature:  "func Charge(ctx context.Context, amount int) error",
			DocComment: "Charge initiates a payment. Returns an error when the payment provider rejects the request.",
			FilePath:   "internal/billing/billing.go",
			StartLine:  5,
			EndLine:    18,
		},
	}, nil
}

// ---- Canned LLM responses ----

// archResponse returns LLM output that the architecture template parser accepts.
// It contains a code block and citations sufficient to satisfy the engineer/architecture profile.
func archResponse(pkg string) string {
	return fmt.Sprintf(`## Overview
The %s package handles authentication for inbound HTTP requests. (internal/auth/auth.go:1-10)
It validates session tokens and enforces role-based access control on all protected routes.

## Key types
| Type | Purpose |
|---|---|
| Middleware | Wraps http.Handler with session verification (internal/auth/auth.go:12-25) |

## Public API
Middleware is the primary entry point. Pass it any http.Handler to enable authentication. (internal/auth/auth.go:12-25)
RequireRole asserts that the authenticated user holds a given role. (internal/auth/auth.go:30-45)

## Dependencies
- internal/jwt for token parsing (internal/jwt/jwt.go:1-50)
- internal/sessions for session lookup (internal/sessions/sessions.go:1-80)

## Used by
- internal/api/rest (internal/api/rest/rest.go:1-20)
- internal/billing (internal/billing/billing.go:1-20)

## Code example
`+"```go"+`
mux.Handle("/api/", auth.Middleware(apiHandler))
`+"```", pkg)
}

// sysOverviewResponse returns LLM output that satisfies the system_overview profile.
func sysOverviewResponse() string {
	return `## What this system does
SourceBridge indexes source repositories and generates living documentation for engineer and product audiences. It satisfies the need for always-current architecture documentation without manual maintenance. The platform provides 3 distinct report surfaces: wiki pages, compliance reports, and API references.

## Main capabilities
- Indexes Go, Python, and TypeScript repositories via push webhook
- Generates per-package architecture pages using LLM with engineer-to-engineer voice
- Validates documentation quality with 8 configurable gate validators
- Opens wiki PRs for engineer review before publication
- Maintains stable block IDs across regenerations to preserve human edits
- Supports 5 sink types: git repo, Confluence, Notion, GitHub wiki, GitLab wiki

## Key packages
| Package | Purpose |
| --- | --- |
| internal/auth | Authentication middleware and role enforcement |
| internal/livingwiki | Living wiki orchestration and page management |
| internal/quality | Template-scoped documentation quality validators |
| internal/citations | Canonical citation format shared across all report surfaces |

## External dependencies
PostgreSQL (page store and graph store), Redis (job queue), GitHub API (PR management), Anthropic API (LLM inference)`
}

// ---- Template stubs ----

// stubTemplate is a minimal templates.Template that generates a distinct page
// per input.RepoID. The markdown content is configurable per test.
type stubTemplate struct {
	id       string
	markdown string
}

func (s *stubTemplate) ID() string { return s.id }

func (s *stubTemplate) Generate(_ context.Context, input templates.GenerateInput) (ast.Page, error) {
	// Use repoID in the page ID to differentiate pages across runs.
	pageID := input.RepoID + "." + s.id
	return ast.Page{
		ID: pageID,
		Manifest: manifest.DependencyManifest{
			PageID:   pageID,
			Template: s.id,
			Audience: string(input.Audience),
		},
		Blocks: []ast.Block{
			{
				ID:   ast.GenerateBlockID(pageID, "", ast.BlockKindParagraph, 0),
				Kind: ast.BlockKindParagraph,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
					Markdown: s.markdown,
				}},
				Owner:      ast.OwnerGenerated,
				LastChange: ast.BlockChange{Timestamp: input.Now, Source: "sourcebridge"},
			},
		},
		Provenance: ast.Provenance{GeneratedAt: input.Now},
	}, nil
}

// stubFailThenPassTemplate fails quality gates on the first Generate call per instance.
// On the second call (retry) it returns passing content. Uses s.id in the page ID.
type stubFailThenPassTemplate struct {
	id        string
	callCount int
	failing   string // markdown whose quality gate check will fire
	passing   string // markdown that passes all gates in the profile
}

func (s *stubFailThenPassTemplate) ID() string { return s.id }

func (s *stubFailThenPassTemplate) Generate(_ context.Context, input templates.GenerateInput) (ast.Page, error) {
	s.callCount++
	content := s.passing
	if s.callCount == 1 {
		content = s.failing
	}
	pageID := "test." + s.id
	return ast.Page{
		ID: pageID,
		Manifest: manifest.DependencyManifest{
			PageID:   pageID,
			Template: s.id,
			Audience: string(input.Audience),
		},
		Blocks: []ast.Block{
			{
				ID:   ast.GenerateBlockID(pageID, "", ast.BlockKindParagraph, 0),
				Kind: ast.BlockKindParagraph,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
					Markdown: content,
				}},
				Owner: ast.OwnerGenerated,
			},
		},
		Provenance: ast.Provenance{GeneratedAt: input.Now},
	}, nil
}

// stubAlwaysFailTemplate generates a page that will fail all quality gates
// by returning an empty blocks slice (no content at all).
type stubAlwaysFailTemplate struct {
	id        string
	callCount int
}

func (s *stubAlwaysFailTemplate) ID() string { return s.id }

func (s *stubAlwaysFailTemplate) Generate(_ context.Context, input templates.GenerateInput) (ast.Page, error) {
	s.callCount++
	pageID := "test." + s.id
	return ast.Page{
		ID: pageID,
		Manifest: manifest.DependencyManifest{
			PageID:   pageID,
			Template: s.id,
			Audience: string(input.Audience),
		},
		// Intentionally zero blocks — will fail block_count gate for architecture.
		Provenance: ast.Provenance{GeneratedAt: input.Now},
	}, nil
}

// ---- Helpers ----

func makeBaseInput(graph templates.SymbolGraph, llm templates.LLMCaller) templates.GenerateInput {
	return templates.GenerateInput{
		RepoID:      "test-repo",
		Audience:    quality.AudienceEngineers,
		SymbolGraph: graph,
		LLM:         llm,
		Now:         time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
	}
}

const repoID = "test-repo"

// ---- Tests ----

// TestColdStartHappyPath verifies that a 5-page cold-start run stores all pages
// as proposed_ast and opens the PR with a page-per-planned-page mapping.
func TestColdStartHappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Use the glossary profile (single factual_grounding gate) to minimise
	// incidental failures. The glossary gate only fires on behavioral assertion
	// paragraphs that lack citations; our passing content is citation-free prose
	// that makes no behavioral claims → passes.
	const passMarkdown = "Middleware wraps an HTTP handler. No behavioral claims here."

	// 5 planned pages, each with a different index in its RepoID so the stub
	// template generates a distinct page ID per page.
	pages := make([]orchestrator.PlannedPage, 5)
	for i := range pages {
		input := makeBaseInput(nil, nil)
		input.RepoID = fmt.Sprintf("repo-%d", i)
		pages[i] = orchestrator.PlannedPage{
			ID:         fmt.Sprintf("repo-%d.glossary", i),
			TemplateID: "glossary",
			Audience:   quality.AudienceEngineers,
			Input:      input,
		}
	}

	pass := &stubTemplate{id: "glossary", markdown: passMarkdown}
	reg := orchestrator.NewMapRegistry(pass)
	store := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR("pr-001")

	orch := orchestrator.New(orchestrator.Config{RepoID: repoID}, reg, store)
	result, err := orch.Generate(ctx, orchestrator.GenerateRequest{
		Pages: pages,
		PR:    pr,
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	if len(result.Generated) != 5 {
		t.Errorf("expected 5 generated pages, got %d (excluded: %v)", len(result.Generated), result.Excluded)
	}
	if len(result.Excluded) != 0 {
		t.Errorf("expected 0 excluded pages, got %d", len(result.Excluded))
	}
	if !pr.IsOpen() {
		t.Error("expected PR to be open")
	}
	if result.PRID != "pr-001" {
		t.Errorf("expected PRID=pr-001, got %q", result.PRID)
	}

	// Verify proposed_ast was stored using the orchestrator's repoID.
	for _, p := range result.Generated {
		stored, ok, err := store.GetProposed(ctx, repoID, "pr-001", p.ID)
		if err != nil {
			t.Errorf("GetProposed(%q): %v", p.ID, err)
			continue
		}
		if !ok {
			t.Errorf("expected proposed page %q to be stored under repoID=%q", p.ID, repoID)
		}
		if ok && stored.ID != p.ID {
			t.Errorf("stored page ID mismatch: got %q want %q", stored.ID, p.ID)
		}
	}

	// Verify PR files exist.
	files := pr.Files()
	if len(files) != 5 {
		t.Errorf("expected 5 files in PR, got %d", len(files))
	}
}

// TestGateFailRetrySucceed verifies that a page that fails on attempt 1 is
// retried and succeeds on attempt 2.
func TestGateFailRetrySucceed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Use the architecture profile (engineer audience) — citation_density is a gate.
	// The failing content has no citation; the passing content has one.
	//
	// architecture/for-engineers profile gates: citation_density, vagueness, factual_grounding.
	// Failing: paragraph that contains a behavioral assertion without a citation.
	// Passing: same paragraph with a citation appended.
	const failMD = "This package returns an error when authentication fails."
	const passMD = "This package returns an error when authentication fails. (internal/auth/auth.go:12-25)"

	tmpl := &stubFailThenPassTemplate{
		id:      "architecture",
		failing: failMD,
		passing: passMD,
	}

	reg := orchestrator.NewMapRegistry(tmpl)
	store := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR("pr-retry")

	orch := orchestrator.New(orchestrator.Config{RepoID: repoID}, reg, store)
	result, err := orch.Generate(ctx, orchestrator.GenerateRequest{
		Pages: []orchestrator.PlannedPage{
			{
				ID:         "test.architecture",
				TemplateID: "architecture",
				Audience:   quality.AudienceEngineers,
				Input:      makeBaseInput(nil, nil),
			},
		},
		PR: pr,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if len(result.Generated) != 1 {
		t.Errorf("expected 1 generated page (retry succeeded), got %d (excluded: %v)", len(result.Generated), result.Excluded)
	}
	if len(result.Excluded) != 0 {
		t.Errorf("expected 0 excluded, got %d", len(result.Excluded))
	}
	if tmpl.callCount != 2 {
		t.Errorf("expected 2 Generate calls (attempt + retry), got %d", tmpl.callCount)
	}
}

// TestGateFailTwiceExcludes verifies that a page excluded after two gate
// failures appears in result.Excluded and is not in result.Generated.
func TestGateFailTwiceExcludes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// architecture/for-engineers profile has gates: citation_density, vagueness, factual_grounding.
	// An empty page (no blocks) will have no prose → citation_density passes (no words).
	// However block_count (warning not gate) won't exclude; we need a gate to fire.
	// The stub returns no blocks so factual_grounding passes (no assertions).
	// We need citation_density to fire: content with >200 words and zero citations.
	//
	// Actually the easiest approach: use architecture profile, generate a paragraph
	// with a behavioral assertion but no citation. That fires factual_grounding.
	tmpl := &stubAlwaysFailTemplate{id: "architecture"}

	// We'll override the page to produce content with an assertion and no citation:
	// We do this by using a different stub variant.
	assertNoCitationTmpl := &assertNoCitationTemplate{id: "architecture"}

	reg := orchestrator.NewMapRegistry(assertNoCitationTmpl)
	store := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR("pr-exclude")

	orch := orchestrator.New(orchestrator.Config{RepoID: repoID}, reg, store)
	result, err := orch.Generate(ctx, orchestrator.GenerateRequest{
		Pages: []orchestrator.PlannedPage{
			{
				ID:         "test.arch.internal.auth",
				TemplateID: "architecture",
				Audience:   quality.AudienceEngineers,
				Input:      makeBaseInput(nil, nil),
			},
		},
		PR: pr,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if len(result.Generated) != 0 {
		t.Errorf("expected 0 generated pages, got %d", len(result.Generated))
	}
	if len(result.Excluded) != 1 {
		t.Errorf("expected 1 excluded page, got %d", len(result.Excluded))
	}
	if len(result.Excluded) == 1 && result.Excluded[0].PageID != "test.arch.internal.auth" {
		t.Errorf("excluded page ID mismatch: got %q", result.Excluded[0].PageID)
	}
	if len(result.Excluded) == 1 && result.Excluded[0].SecondResult.Decision != quality.RetryReject {
		t.Errorf("expected SecondResult.Decision=reject, got %v", result.Excluded[0].SecondResult.Decision)
	}
	if assertNoCitationTmpl.callCount != 2 {
		t.Errorf("expected 2 Generate calls, got %d", assertNoCitationTmpl.callCount)
	}
	_ = tmpl // suppress unused warning
}

// assertNoCitationTemplate always generates a paragraph with a behavioral
// assertion and no citation — guaranteed to fail factual_grounding gate.
type assertNoCitationTemplate struct {
	id        string
	callCount int
}

func (s *assertNoCitationTemplate) ID() string { return s.id }

func (s *assertNoCitationTemplate) Generate(_ context.Context, input templates.GenerateInput) (ast.Page, error) {
	s.callCount++
	pageID := "test." + s.id
	// Content: behavioral assertions without citations → fires factual_grounding.
	const md = "This package returns a session token when authentication succeeds. It validates the role claim and ensures the request carries a valid bearer token."
	return ast.Page{
		ID: pageID,
		Manifest: manifest.DependencyManifest{
			PageID:   pageID,
			Template: s.id,
			Audience: string(input.Audience),
		},
		Blocks: []ast.Block{
			{
				ID:   ast.GenerateBlockID(pageID, "", ast.BlockKindParagraph, 0),
				Kind: ast.BlockKindParagraph,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: md}},
				Owner: ast.OwnerGenerated,
			},
		},
		Provenance: ast.Provenance{GeneratedAt: input.Now},
	}, nil
}

// TestDirectPublishMode verifies that pages go directly to canonical_ast
// when Config.DirectPublish is true, and no PR is opened.
func TestDirectPublishMode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const (
		dpRepoID = "direct-repo"
		pageID   = dpRepoID + ".glossary"
	)

	pass := &stubTemplate{
		id:       "glossary",
		markdown: "Middleware wraps an HTTP handler. No behavioral assertions.",
	}

	reg := orchestrator.NewMapRegistry(pass)
	store := orchestrator.NewMemoryPageStore()

	// Write to a temp dir.
	dir := t.TempDir()
	writer := orchestrator.NewFilesystemRepoWriter(dir)

	orch := orchestrator.New(orchestrator.Config{
		RepoID:        dpRepoID,
		DirectPublish: true,
	}, reg, store)

	input := makeBaseInput(nil, nil)
	input.RepoID = dpRepoID

	result, err := orch.Generate(ctx, orchestrator.GenerateRequest{
		Pages: []orchestrator.PlannedPage{
			{
				ID:         pageID,
				TemplateID: "glossary",
				Audience:   quality.AudienceEngineers,
				Input:      input,
			},
		},
		Writer: writer,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if len(result.Generated) != 1 {
		t.Errorf("expected 1 generated page, got %d (excluded: %v)", len(result.Generated), result.Excluded)
	}
	if result.PRID != "" {
		t.Errorf("expected empty PRID in direct-publish mode, got %q", result.PRID)
	}

	// The page must be in canonical, not proposed.
	generatedPageID := result.Generated[0].ID
	canonical, ok, err := store.GetCanonical(ctx, dpRepoID, generatedPageID)
	if err != nil {
		t.Fatalf("GetCanonical: %v", err)
	}
	if !ok {
		t.Error("expected canonical page to exist in direct-publish mode")
	}
	if canonical.ID != generatedPageID {
		t.Errorf("canonical page ID mismatch: got %q want %q", canonical.ID, generatedPageID)
	}

	// The file must have been written to disk.
	expectedPath := dir + "/wiki/" + generatedPageID + ".md"
	if _, err := os.Stat(expectedPath); err != nil {
		t.Errorf("expected wiki file %q to exist on disk: %v", expectedPath, err)
	}
}

// TestPromotion verifies that after Promote is called, proposed pages become
// canonical and OwnerHumanEditedOnPRBranch translates to OwnerHumanEdited.
func TestPromotion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const (
		promoRepoID = "promo-repo"
		promoPRID   = "pr-promo"
	)

	store := orchestrator.NewMemoryPageStore()

	// Manually store a proposed page with a human-edited-on-pr-branch block.
	proposedPage := ast.Page{
		ID: "test.page",
		Manifest: manifest.DependencyManifest{
			PageID:   "test.page",
			Template: "glossary",
			Audience: string(quality.AudienceEngineers),
		},
		Blocks: []ast.Block{
			{
				ID:    "b001",
				Kind:  ast.BlockKindParagraph,
				Owner: ast.OwnerHumanEditedOnPRBranch,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
					Markdown: "Human edit on PR branch.",
				}},
			},
			{
				ID:    "b002",
				Kind:  ast.BlockKindParagraph,
				Owner: ast.OwnerGenerated,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
					Markdown: "Bot-generated paragraph.",
				}},
			},
		},
	}
	if err := store.SetProposed(ctx, promoRepoID, promoPRID, proposedPage); err != nil {
		t.Fatalf("SetProposed: %v", err)
	}

	reg := orchestrator.NewMapRegistry() // no templates needed for this test
	orch := orchestrator.New(orchestrator.Config{RepoID: promoRepoID}, reg, store)

	if err := orch.Promote(ctx, promoRepoID, promoPRID); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	// Canonical must exist.
	canonical, ok, err := store.GetCanonical(ctx, promoRepoID, "test.page")
	if err != nil {
		t.Fatalf("GetCanonical: %v", err)
	}
	if !ok {
		t.Fatal("expected canonical page to exist after promotion")
	}

	// OwnerHumanEditedOnPRBranch must have been translated to OwnerHumanEdited.
	for _, blk := range canonical.Blocks {
		switch blk.ID {
		case "b001":
			if blk.Owner != ast.OwnerHumanEdited {
				t.Errorf("block b001: expected OwnerHumanEdited after promotion, got %v", blk.Owner)
			}
		case "b002":
			if blk.Owner != ast.OwnerGenerated {
				t.Errorf("block b002: expected OwnerGenerated unchanged, got %v", blk.Owner)
			}
		}
	}

	// Proposed page must be deleted.
	_, stillProposed, err := store.GetProposed(ctx, promoRepoID, promoPRID, "test.page")
	if err != nil {
		t.Fatalf("GetProposed after promote: %v", err)
	}
	if stillProposed {
		t.Error("expected proposed page to be deleted after promotion")
	}
}

// TestRejection verifies that Discard removes proposed pages and leaves
// canonical unchanged.
func TestRejection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const (
		rejRepoID = "reject-repo"
		rejPRID   = "pr-reject"
	)

	store := orchestrator.NewMemoryPageStore()

	// Set a canonical baseline.
	canonicalBaseline := ast.Page{
		ID: "test.page",
		Manifest: manifest.DependencyManifest{PageID: "test.page"},
		Blocks: []ast.Block{
			{
				ID:    "b001",
				Kind:  ast.BlockKindParagraph,
				Owner: ast.OwnerGenerated,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
					Markdown: "Original canonical content.",
				}},
			},
		},
	}
	if err := store.SetCanonical(ctx, rejRepoID, canonicalBaseline); err != nil {
		t.Fatalf("SetCanonical: %v", err)
	}

	// Store a proposed page with different content.
	proposedPage := ast.Page{
		ID: "test.page",
		Blocks: []ast.Block{
			{
				ID:    "b001",
				Kind:  ast.BlockKindParagraph,
				Owner: ast.OwnerGenerated,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
					Markdown: "New proposed content.",
				}},
			},
		},
	}
	if err := store.SetProposed(ctx, rejRepoID, rejPRID, proposedPage); err != nil {
		t.Fatalf("SetProposed: %v", err)
	}

	reg := orchestrator.NewMapRegistry()
	orch := orchestrator.New(orchestrator.Config{RepoID: rejRepoID}, reg, store)

	if err := orch.Discard(ctx, rejRepoID, rejPRID); err != nil {
		t.Fatalf("Discard: %v", err)
	}

	// Proposed must be gone.
	_, ok, err := store.GetProposed(ctx, rejRepoID, rejPRID, "test.page")
	if err != nil {
		t.Fatalf("GetProposed after discard: %v", err)
	}
	if ok {
		t.Error("expected proposed page to be deleted after discard")
	}

	// Canonical must be unchanged.
	canonical, ok, err := store.GetCanonical(ctx, rejRepoID, "test.page")
	if err != nil {
		t.Fatalf("GetCanonical: %v", err)
	}
	if !ok {
		t.Fatal("expected canonical page to still exist after discard")
	}
	if canonical.Blocks[0].Content.Paragraph.Markdown != "Original canonical content." {
		t.Errorf("canonical content changed after discard: got %q", canonical.Blocks[0].Content.Paragraph.Markdown)
	}
}

// TestConcurrencyBudget verifies that 30 pages are generated concurrently
// within a 2s time budget using stub templates (no LLM calls, orchestration only).
func TestConcurrencyBudget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	pass := &stubTemplate{
		id:       "glossary",
		markdown: "Middleware wraps an HTTP handler. No behavioral assertions.",
	}

	const n = 30
	pages := make([]orchestrator.PlannedPage, n)
	for i := range pages {
		input := makeBaseInput(nil, nil)
		input.RepoID = fmt.Sprintf("bench-repo-%d", i)
		pages[i] = orchestrator.PlannedPage{
			ID:         fmt.Sprintf("bench-repo-%d.glossary", i),
			TemplateID: "glossary",
			Audience:   quality.AudienceEngineers,
			Input:      input,
		}
	}

	reg := orchestrator.NewMapRegistry(pass)
	store := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR("pr-bench")

	orch := orchestrator.New(orchestrator.Config{
		RepoID:         "bench",
		TimeBudget:     2 * time.Second,
		MaxConcurrency: 10,
	}, reg, store)

	start := time.Now()
	result, err := orch.Generate(ctx, orchestrator.GenerateRequest{
		Pages: pages,
		PR:    pr,
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(result.Generated) != n {
		t.Errorf("expected %d generated pages, got %d (excluded: %v)", n, len(result.Generated), result.Excluded)
	}
	if elapsed > 2*time.Second {
		t.Errorf("30-page generation took %v, exceeded 2s budget", elapsed)
	}
	t.Logf("30 pages generated in %v", elapsed)
}

// TestTaxonomyResolver verifies that Resolve returns the expected page taxonomy.
func TestTaxonomyResolver(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	resolver := orchestrator.NewTaxonomyResolver(
		"test-repo",
		twoPackageGraph{},
		nil, // no git log
		nil, // no LLM
	)

	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	pages, err := resolver.Resolve(ctx, nil, nil, now)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Expect: 2 architecture + 1 API reference + 1 system overview + 1 glossary = 5.
	if len(pages) != 5 {
		t.Errorf("expected 5 planned pages, got %d", len(pages))
		for _, p := range pages {
			t.Logf("  page: id=%q template=%q", p.ID, p.TemplateID)
		}
	}

	counts := make(map[string]int)
	for _, p := range pages {
		counts[p.TemplateID]++
	}
	if counts["architecture"] != 2 {
		t.Errorf("expected 2 architecture pages, got %d", counts["architecture"])
	}
	if counts["api_reference"] != 1 {
		t.Errorf("expected 1 api_reference page, got %d", counts["api_reference"])
	}
	if counts["system_overview"] != 1 {
		t.Errorf("expected 1 system_overview page, got %d", counts["system_overview"])
	}
	if counts["glossary"] != 1 {
		t.Errorf("expected 1 glossary page, got %d", counts["glossary"])
	}

	// Architecture pages must have PackageInfo set.
	archPages := filterByTemplate(pages, "architecture")
	if len(archPages) == 2 {
		pkgs := map[string]bool{
			archPages[0].PackageInfo.Package: true,
			archPages[1].PackageInfo.Package: true,
		}
		if !pkgs["internal/auth"] || !pkgs["internal/billing"] {
			t.Errorf("expected arch pages for internal/auth and internal/billing, got %v", pkgs)
		}
	}
}

// TestEndToEndWithRealTemplates runs a complete cold-start with the default
// registry and a stub LLM, then writes sample output to samples/wiki-example/.
func TestEndToEndWithRealTemplates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Use a single LLM response that satisfies the architecture profile gates:
	// - citation_density ≥1/200w: the response has several (path:line-line) citations
	// - factual_grounding: every assertion is paired with a citation
	// - vagueness: no vague quantifiers
	//
	// The same response is returned for all LLM calls (arch, sysoverview, apiref overview).
	// This is simpler than ordering responses in concurrent execution.
	llm := &constLLM{response: archResponse("internal/auth")}

	graph := twoPackageGraph{}
	resolver := orchestrator.NewTaxonomyResolver("test-repo", graph, nil, llm)
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	pages, err := resolver.Resolve(ctx, nil, nil, now)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	reg := orchestrator.NewDefaultRegistry()
	store := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR("pr-e2e")

	// Supply sufficient graph metrics so architectural_relevance gate passes.
	orch := orchestrator.New(orchestrator.Config{
		RepoID:         "test-repo",
		MaxConcurrency: 5,
		TimeBudget:     30 * time.Second,
		GraphMetrics:   orchestrator.ConstGraphMetrics{PageRefs: 5, GraphRelations: 10},
	}, reg, store)

	result, err := orch.Generate(ctx, orchestrator.GenerateRequest{
		Pages: pages,
		PR:    pr,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	t.Logf("generated %d pages, excluded %d pages in %v", len(result.Generated), len(result.Excluded), result.Duration)
	for _, e := range result.Excluded {
		t.Logf("  excluded %q: first=%v second=%v", e.PageID, e.FirstResult.Gates, e.SecondResult.Gates)
	}

	if !pr.IsOpen() {
		t.Error("expected PR to be open after generation")
	}
	if pr.Title() != "wiki: initial generation (sourcebridge)" {
		t.Errorf("unexpected PR title: %q", pr.Title())
	}

	// Write sample files to disk for inspection.
	sampleDir := findSamplesDir(t)
	for path, content := range pr.Files() {
		if err := writeSampleFile(sampleDir, path, content); err != nil {
			t.Errorf("writing sample %q: %v", path, err)
		}
	}

	// Verify PR files include the expected paths.
	files := pr.Files()
	expectedPaths := []string{
		"wiki/test-repo.arch.internal.auth.md",
		"wiki/test-repo.arch.internal.billing.md",
		"wiki/test-repo.api_reference.md",
		"wiki/test-repo.system_overview.md",
		"wiki/test-repo.glossary.md",
	}
	for _, path := range expectedPaths {
		if _, ok := files[path]; !ok {
			t.Errorf("expected file %q in PR files, not found (files: %v)", path, fileKeys(files))
		}
	}

	// All generated files must have YAML frontmatter.
	for path, content := range files {
		if !strings.HasPrefix(string(content), "---\n") {
			t.Errorf("file %q missing YAML frontmatter", path)
		}
	}
}

// ---- helpers ----

func filterByTemplate(pages []orchestrator.PlannedPage, tmpl string) []orchestrator.PlannedPage {
	var out []orchestrator.PlannedPage
	for _, p := range pages {
		if p.TemplateID == tmpl {
			out = append(out, p)
		}
	}
	return out
}

func fileKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// findSamplesDir returns the repo root (so WriteSampleFile can join "samples/wiki-example/").
func findSamplesDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(dir + "/go.mod"); statErr == nil {
			break
		}
		parent := dir[:strings.LastIndex(dir, "/")]
		if parent == dir {
			t.Fatal("could not find repo root (go.mod)")
		}
		dir = parent
	}
	sampleDir := dir + "/samples/wiki-example"
	if err := os.MkdirAll(sampleDir, 0o755); err != nil {
		t.Fatalf("creating samples dir: %v", err)
	}
	return dir
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase 4a: Overview / Detailed mode taxonomy tests
// ─────────────────────────────────────────────────────────────────────────────

// threeClusterSummaries returns three ClusterSummary values for use in tests.
func threeClusterSummaries() []clustering.ClusterSummary {
	return []clustering.ClusterSummary{
		{Label: "auth_subsystem", MemberPackages: []string{"internal/auth"}},
		{Label: "billing_subsystem", MemberPackages: []string{"internal/billing"}},
		{Label: "api_gateway", MemberPackages: []string{"internal/api"}},
	}
}

// multiPackageGraph returns symbols spread across 30 packages under 5 top-level dirs.
type multiPackageGraph struct{}

func (multiPackageGraph) ExportedSymbols(_ string) ([]templates.Symbol, error) {
	var syms []templates.Symbol
	topDirs := []string{"cmd", "internal", "pkg", "tools", "vendor"}
	// 6 packages per top-level dir → 30 packages total.
	for _, dir := range topDirs {
		for i := 0; i < 6; i++ {
			syms = append(syms, templates.Symbol{
				Package:    fmt.Sprintf("%s/sub%d", dir, i),
				Name:       fmt.Sprintf("Sym%d", i),
				Signature:  "func Sym()",
				DocComment: "A symbol.",
				FilePath:   fmt.Sprintf("%s/sub%d/file.go", dir, i),
				StartLine:  1,
				EndLine:    5,
			})
		}
	}
	return syms, nil
}

// TestResolveOverviewEmitsClusterScopedPages verifies that ResolveOverview produces
// one overview.* page per cluster plus the 3 repo-wide pages.
func TestResolveOverviewEmitsClusterScopedPages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	resolver := orchestrator.NewTaxonomyResolver("test-repo", twoPackageGraph{}, nil, nil)
	clusters := threeClusterSummaries()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	pages, err := resolver.ResolveOverview(ctx, nil, clusters, now)
	if err != nil {
		t.Fatalf("ResolveOverview: %v", err)
	}

	// 3 cluster pages + 3 repo-wide = 6 total.
	if len(pages) != 6 {
		t.Errorf("expected 6 pages, got %d", len(pages))
		for _, p := range pages {
			t.Logf("  %q (%s)", p.ID, p.TemplateID)
		}
	}

	const repoPrefix = "test-repo.overview."
	for _, cs := range clusters {
		wantID := orchestrator.OverviewPageID("test-repo", cs.Label)
		if !strings.HasPrefix(wantID, repoPrefix) {
			t.Errorf("OverviewPageID(%q) = %q, expected %q prefix", cs.Label, wantID, repoPrefix)
		}
		found := false
		for _, p := range pages {
			if p.ID == wantID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("page %q not found in results", wantID)
		}
	}
}

// TestResolveDetailedEmitsPackageScopedPages verifies that ResolveDetailed with
// clusters emits one detail.* page per cluster plus the 3 repo-wide pages.
func TestResolveDetailedEmitsPackageScopedPages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	resolver := orchestrator.NewTaxonomyResolver("test-repo", twoPackageGraph{}, nil, nil)
	clusters := threeClusterSummaries()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	pages, err := resolver.ResolveDetailed(ctx, nil, clusters, now)
	if err != nil {
		t.Fatalf("ResolveDetailed: %v", err)
	}

	// 3 cluster pages + 3 repo-wide = 6 total.
	if len(pages) != 6 {
		t.Errorf("expected 6 pages, got %d", len(pages))
		for _, p := range pages {
			t.Logf("  %q (%s)", p.ID, p.TemplateID)
		}
	}

	const repoPrefix = "test-repo.detail."
	for _, cs := range clusters {
		wantID := orchestrator.DetailPageID("test-repo", cs.Label)
		if !strings.HasPrefix(wantID, repoPrefix) {
			t.Errorf("DetailPageID(%q) = %q, expected %q prefix", cs.Label, wantID, repoPrefix)
		}
		found := false
		for _, p := range pages {
			if p.ID == wantID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("page %q not found in results", wantID)
		}
	}
}

// TestResolveBothModesDedupesRepoWidePages verifies that calling ResolveForMode
// for both overview and detailed produces no duplicated repo-wide page IDs.
func TestResolveBothModesDedupesRepoWidePages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	resolver := orchestrator.NewTaxonomyResolver("test-repo", twoPackageGraph{}, nil, nil)
	clusters := threeClusterSummaries()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	ovPages, err := resolver.ResolveForMode(ctx, "lw_overview", nil, clusters, now)
	if err != nil {
		t.Fatalf("ResolveForMode(lw_overview): %v", err)
	}
	detPages, err := resolver.ResolveForMode(ctx, "lw_detailed", nil, clusters, now)
	if err != nil {
		t.Fatalf("ResolveForMode(lw_detailed): %v", err)
	}

	repoWideTemplates := map[string]bool{
		"api_reference":   true,
		"system_overview": true,
		"glossary":        true,
	}

	// Combine all pages and count how many times each repo-wide template appears.
	combined := append(ovPages, detPages...)
	counts := make(map[string]int)
	for _, p := range combined {
		if repoWideTemplates[p.TemplateID] {
			counts[p.TemplateID]++
		}
	}
	// Each repo-wide template should appear in each mode exactly once → 2 total when combined.
	for tmpl := range repoWideTemplates {
		if counts[tmpl] != 2 {
			t.Errorf("template %q appears %d times across both modes, expected 2 (one per mode)", tmpl, counts[tmpl])
		}
	}

	// Verify the IDs within each mode are unique.
	for _, pages := range [][]orchestrator.PlannedPage{ovPages, detPages} {
		seen := make(map[string]bool)
		for _, p := range pages {
			if seen[p.ID] {
				t.Errorf("duplicate page ID %q in result set", p.ID)
			}
			seen[p.ID] = true
		}
	}
}

// TestResolveDetailedFallbackWhenNoClusters verifies that Detailed mode emits
// top-level-dir pages under the detail. prefix when clusters are absent.
func TestResolveDetailedFallbackWhenNoClusters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// multiPackageGraph has 30 packages under 5 top-level dirs (cmd, internal, pkg, tools, vendor).
	resolver := orchestrator.NewTaxonomyResolver("test-repo", multiPackageGraph{}, nil, nil)
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	pages, err := resolver.ResolveDetailed(ctx, nil, nil /* no clusters */, now)
	if err != nil {
		t.Fatalf("ResolveDetailed (no clusters): %v", err)
	}

	archPages := filterByTemplate(pages, "architecture")

	// 5 top-level dirs → 5 detail.* architecture pages.
	if len(archPages) != 5 {
		t.Errorf("expected 5 architecture pages for 5 top-level dirs, got %d", len(archPages))
		for _, p := range archPages {
			t.Logf("  %q", p.ID)
		}
	}

	// All architecture pages must carry the detail. prefix.
	for _, p := range archPages {
		if !strings.HasPrefix(p.ID, "test-repo.detail.") {
			t.Errorf("architecture page %q missing detail. prefix", p.ID)
		}
	}

	// Expected top dirs (alphabetical order, capped at 25 — here only 5).
	expectedTopDirs := []string{"cmd", "internal", "pkg", "tools", "vendor"}
	for _, dir := range expectedTopDirs {
		wantID := orchestrator.DetailPageID("test-repo", dir)
		found := false
		for _, p := range archPages {
			if p.ID == wantID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected page %q not found; arch pages: %v",
				wantID, pageIDs(archPages))
		}
	}
}

// TestResolveDetailedFallbackCappedAt25 verifies that the top-level-dir fallback
// emits at most 25 architecture pages even when more than 25 dirs are present.
func TestResolveDetailedFallbackCappedAt25(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Build a graph with 30 distinct top-level dirs.
	resolver := orchestrator.NewTaxonomyResolver("test-repo", manyTopDirsGraph{}, nil, nil)
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	pages, err := resolver.ResolveDetailed(ctx, nil, nil, now)
	if err != nil {
		t.Fatalf("ResolveDetailed (30 dirs): %v", err)
	}

	archPages := filterByTemplate(pages, "architecture")
	if len(archPages) > 25 {
		t.Errorf("expected at most 25 architecture pages, got %d", len(archPages))
	}
}

// manyTopDirsGraph returns one symbol per unique top-level dir (30 dirs total).
type manyTopDirsGraph struct{}

func (manyTopDirsGraph) ExportedSymbols(_ string) ([]templates.Symbol, error) {
	var syms []templates.Symbol
	for i := 0; i < 30; i++ {
		syms = append(syms, templates.Symbol{
			Package:    fmt.Sprintf("dir%02d/sub", i),
			Name:       "Sym",
			Signature:  "func Sym()",
			DocComment: "A symbol.",
			FilePath:   fmt.Sprintf("dir%02d/sub/file.go", i),
			StartLine:  1,
			EndLine:    2,
		})
	}
	return syms, nil
}

// TestOverviewPageIDPrefix verifies the OverviewPageID helper produces the correct format.
func TestOverviewPageIDPrefix(t *testing.T) {
	t.Parallel()
	got := orchestrator.OverviewPageID("repo", "auth_subsystem")
	want := "repo.overview.auth_subsystem"
	if got != want {
		t.Errorf("OverviewPageID = %q, want %q", got, want)
	}
}

// TestDetailPageIDPrefix verifies the DetailPageID helper produces the correct format,
// including replacing "/" with "." in the slug.
func TestDetailPageIDPrefix(t *testing.T) {
	t.Parallel()
	got := orchestrator.DetailPageID("repo", "internal/api/middleware")
	want := "repo.detail.internal.api.middleware"
	if got != want {
		t.Errorf("DetailPageID = %q, want %q", got, want)
	}
}

// pageIDs returns the IDs of all pages in a slice (for test failure diagnostics).
func pageIDs(pages []orchestrator.PlannedPage) []string {
	ids := make([]string, len(pages))
	for i, p := range pages {
		ids[i] = p.ID
	}
	return ids
}

// writeSampleFile writes a sample wiki file under samples/wiki-example/.
func writeSampleFile(repoRoot, relPath string, content []byte) error {
	idx := strings.LastIndex(relPath, "/")
	filename := relPath
	if idx >= 0 {
		filename = relPath[idx+1:]
	}
	targetDir := repoRoot + "/samples/wiki-example"
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(targetDir+"/"+filename, content, 0o644)
}
