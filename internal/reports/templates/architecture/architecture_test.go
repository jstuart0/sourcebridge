// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package architecture_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates/architecture"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test doubles
// ─────────────────────────────────────────────────────────────────────────────

// stubSymbolGraph returns a fixed slice of symbols.
type stubSymbolGraph struct {
	syms []templates.Symbol
}

func (s *stubSymbolGraph) ExportedSymbols(_ string) ([]templates.Symbol, error) {
	return s.syms, nil
}

// capturingLLM records the prompts it receives and returns a fixed response.
type capturingLLM struct {
	systemPrompt string
	userPrompt   string
	response     string
}

func (l *capturingLLM) Complete(_ context.Context, sys, user string) (string, error) {
	l.systemPrompt = sys
	l.userPrompt = user
	return l.response, nil
}

// minimalLLMResponse is a minimal six-section response that passes the renderer.
const minimalLLMResponse = `## Overview
Package src/db opens and manages database connections.

## Key types
No exported types.

## Public API
Connect opens a connection (src/db/conn.go:10-25).

## Dependencies
No dependencies.

## Used by
No callers.

## Code example
` + "```go" + `
// src/db/conn.go:10-25
func Connect(dsn string) error {
    return nil
}
` + "```"

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

// TestGeneratePackagePage_BodyInPrompt asserts that when symbols have a Body
// field populated, the user prompt contains the fenced source excerpt and the
// full doc comment (not just a one-line summary).
func TestGeneratePackagePage_BodyInPrompt(t *testing.T) {
	t.Parallel()

	llm := &capturingLLM{response: minimalLLMResponse}
	graph := &stubSymbolGraph{
		syms: []templates.Symbol{
			{
				Package:    "src/db",
				Name:       "Connect",
				Signature:  "func Connect(dsn string) error",
				DocComment: "Connect opens a database connection using the provided DSN.\nIt validates the connection before returning.",
				FilePath:   "src/db/conn.go",
				StartLine:  10,
				EndLine:    25,
				Body:       "func Connect(dsn string) error {\n\treturn db.Open(dsn)\n}",
			},
		},
	}

	tmpl := architecture.New()
	input := templates.GenerateInput{
		RepoID:      "test-repo",
		Audience:    quality.AudienceEngineers,
		SymbolGraph: graph,
		LLM:         llm,
		Now:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	page, err := tmpl.GeneratePackagePage(context.Background(), input, architecture.PackageInfo{
		Package: "src/db",
	})
	if err != nil {
		t.Fatalf("GeneratePackagePage: %v", err)
	}
	if page.ID == "" {
		t.Error("expected non-empty page ID")
	}

	// The user prompt must contain:
	// 1. The fenced source body.
	if !strings.Contains(llm.userPrompt, "```go") {
		t.Error("user prompt should contain a fenced Go code block with the source body")
	}
	if !strings.Contains(llm.userPrompt, "func Connect(dsn string) error") {
		t.Error("user prompt should contain the Connect function body")
	}
	// 2. The full doc comment, not just a one-line summary.
	if !strings.Contains(llm.userPrompt, "validates the connection before returning") {
		t.Error("user prompt should contain the full doc comment, not just the first line")
	}
	// 3. The file/line citation.
	if !strings.Contains(llm.userPrompt, "src/db/conn.go:10-25") {
		t.Error("user prompt should contain the file:line-line citation")
	}
}

// TestGeneratePackagePage_EmptyPackage asserts that a package with no exported
// symbols produces a non-empty page (the placeholder) without calling the LLM.
func TestGeneratePackagePage_EmptyPackage(t *testing.T) {
	t.Parallel()

	llm := &capturingLLM{response: minimalLLMResponse}
	graph := &stubSymbolGraph{syms: nil}

	tmpl := architecture.New()
	input := templates.GenerateInput{
		RepoID:      "test-repo",
		Audience:    quality.AudienceEngineers,
		SymbolGraph: graph,
		LLM:         llm,
		Now:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	page, err := tmpl.GeneratePackagePage(context.Background(), input, architecture.PackageInfo{
		Package: "src/db",
	})
	if err != nil {
		t.Fatalf("GeneratePackagePage (empty package): %v", err)
	}

	// LLM must not have been called.
	if llm.userPrompt != "" {
		t.Error("LLM should not have been called for an empty package")
	}

	// The page must have at least two blocks: a heading and a paragraph.
	if len(page.Blocks) < 2 {
		t.Errorf("expected at least 2 blocks, got %d", len(page.Blocks))
	}

	// The page must contain a heading block.
	hasHeading := false
	for _, b := range page.Blocks {
		if b.Kind == ast.BlockKindHeading {
			hasHeading = true
			break
		}
	}
	if !hasHeading {
		t.Error("expected at least one heading block in the empty-package page")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Knowledge artifact integration
// ─────────────────────────────────────────────────────────────────────────────

// TestGeneratePackagePage_ArtifactsInPrompt asserts that when PackageInfo
// carries a non-empty KnowledgeArtifacts slice, the user prompt contains the
// artifact's section content and the system prompt contains the curated-
// knowledge guidance block, distinguishing it from the raw-symbol-only path.
func TestGeneratePackagePage_ArtifactsInPrompt(t *testing.T) {
	t.Parallel()

	llm := &capturingLLM{response: minimalLLMResponse}
	graph := &stubSymbolGraph{
		syms: []templates.Symbol{
			{
				Package:   "src/db",
				Name:      "Connect",
				Signature: "func Connect(dsn string) error",
				FilePath:  "src/db/conn.go",
				StartLine: 10,
				EndLine:   25,
			},
		},
	}

	tmpl := architecture.New()
	input := templates.GenerateInput{
		RepoID:      "test-repo",
		Audience:    quality.AudienceEngineers,
		SymbolGraph: graph,
		LLM:         llm,
		Now:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	art := architecture.KnowledgeArtifactSummary{
		Type:      "cliff_notes",
		Depth:     "deep",
		ScopePath: "src/db",
		Sections: []architecture.KnowledgeSection{
			{
				Title:   "Purpose",
				Content: "The db package manages the connection pool lifecycle and provides typed query helpers.",
				Summary: "Connection pool + typed queries.",
				Evidence: []architecture.KnowledgeEvidence{
					{FilePath: "src/db/conn.go", LineStart: 10, LineEnd: 25, Rationale: "pool init"},
				},
			},
		},
		GeneratedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	pkg := architecture.PackageInfo{
		Package:            "src/db",
		KnowledgeArtifacts: []architecture.KnowledgeArtifactSummary{art},
	}

	page, err := tmpl.GeneratePackagePage(context.Background(), input, pkg)
	if err != nil {
		t.Fatalf("GeneratePackagePage: %v", err)
	}

	// The user prompt must contain the artifact's section title and content.
	if !strings.Contains(llm.userPrompt, "Curated knowledge artifact") {
		t.Error("user prompt should contain 'Curated knowledge artifact' header")
	}
	if !strings.Contains(llm.userPrompt, "connection pool lifecycle") {
		t.Error("user prompt should contain the artifact section content")
	}
	// The verified source references block must surface the evidence.
	if !strings.Contains(llm.userPrompt, "src/db/conn.go:10-25") {
		t.Error("user prompt should contain the evidence citation from the artifact")
	}

	// The system prompt must include curated-knowledge guidance when artifacts
	// are present.
	if !strings.Contains(llm.systemPrompt, "Curated knowledge guidance") {
		t.Error("system prompt should contain 'Curated knowledge guidance' when artifacts are present")
	}
	if !strings.Contains(llm.systemPrompt, "authoritative ground truth") {
		t.Error("system prompt should instruct model to treat curated content as ground truth")
	}

	// The generated page must contain the provenance note block.
	hasNote := false
	for _, b := range page.Blocks {
		if b.Kind == ast.BlockKindParagraph && b.Content.Paragraph != nil &&
			strings.Contains(b.Content.Paragraph.Markdown, "curated knowledge") {
			hasNote = true
			break
		}
	}
	if !hasNote {
		t.Error("page blocks should contain the italicised curated-knowledge provenance note")
	}
}

// TestGeneratePackagePage_NoArtifacts_IdenticalToBaseline asserts that when
// KnowledgeArtifacts is empty the prompt and system prompt are identical to
// the pre-artifact behaviour: no "Curated knowledge" text, no provenance note.
func TestGeneratePackagePage_NoArtifacts_IdenticalToBaseline(t *testing.T) {
	t.Parallel()

	llm := &capturingLLM{response: minimalLLMResponse}
	graph := &stubSymbolGraph{
		syms: []templates.Symbol{
			{
				Package:   "src/db",
				Name:      "Connect",
				Signature: "func Connect(dsn string) error",
				FilePath:  "src/db/conn.go",
				StartLine: 1,
				EndLine:   5,
			},
		},
	}

	tmpl := architecture.New()
	input := templates.GenerateInput{
		RepoID:      "test-repo",
		Audience:    quality.AudienceEngineers,
		SymbolGraph: graph,
		LLM:         llm,
		Now:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	// Empty KnowledgeArtifacts — should use raw-symbol path.
	pkg := architecture.PackageInfo{
		Package:            "src/db",
		KnowledgeArtifacts: nil,
	}

	page, err := tmpl.GeneratePackagePage(context.Background(), input, pkg)
	if err != nil {
		t.Fatalf("GeneratePackagePage: %v", err)
	}

	// No curated-knowledge text in either prompt.
	if strings.Contains(llm.userPrompt, "Curated knowledge artifact") {
		t.Error("user prompt must NOT contain 'Curated knowledge artifact' when artifacts are absent")
	}
	if strings.Contains(llm.systemPrompt, "Curated knowledge guidance") {
		t.Error("system prompt must NOT contain 'Curated knowledge guidance' when artifacts are absent")
	}

	// No provenance note block.
	for _, b := range page.Blocks {
		if b.Kind == ast.BlockKindParagraph && b.Content.Paragraph != nil &&
			strings.Contains(b.Content.Paragraph.Markdown, "curated knowledge") {
			t.Error("page blocks must NOT contain the provenance note when artifacts are absent")
		}
	}
}

// TestGeneratePackagePage_PackageDoc asserts that when a symbol's DocComment
// starts with "Package ", it is emitted as package-level documentation in the
// prompt.
func TestGeneratePackagePage_PackageDoc(t *testing.T) {
	t.Parallel()

	llm := &capturingLLM{response: minimalLLMResponse}
	graph := &stubSymbolGraph{
		syms: []templates.Symbol{
			{
				Package:    "internal/auth",
				Name:       "Middleware",
				Signature:  "func Middleware(next http.Handler) http.Handler",
				DocComment: "Package auth provides HTTP middleware for JWT authentication.\n\nMiddleware wraps a handler to verify the bearer token.",
				FilePath:   "internal/auth/middleware.go",
				StartLine:  5,
				EndLine:    30,
				Body:       "func Middleware(next http.Handler) http.Handler {\n\treturn http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {\n\t\t// verify token\n\t\tnext.ServeHTTP(w, r)\n\t})\n}",
			},
		},
	}

	tmpl := architecture.New()
	input := templates.GenerateInput{
		RepoID:      "test-repo",
		Audience:    quality.AudienceEngineers,
		SymbolGraph: graph,
		LLM:         llm,
		Now:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	_, err := tmpl.GeneratePackagePage(context.Background(), input, architecture.PackageInfo{
		Package: "internal/auth",
	})
	if err != nil {
		t.Fatalf("GeneratePackagePage: %v", err)
	}

	// The prompt should contain the package-level doc section.
	if !strings.Contains(llm.userPrompt, "Package documentation:") {
		t.Error("user prompt should contain 'Package documentation:' header")
	}
	if !strings.Contains(llm.userPrompt, "provides HTTP middleware for JWT authentication") {
		t.Error("user prompt should contain the package-level doc text")
	}
}

// TestArchitectureTemplateEmitsStubForUnreadyTargets asserts that when the
// LinkResolver marks a target as unready, GeneratePackagePage emits a
// Confluence info-macro stub instead of a bare ac:link (Phase 3 / LD-4).
// The test also verifies that page.StubTargetPageIDs is populated.
func TestArchitectureTemplateEmitsStubForUnreadyTargets(t *testing.T) {
	t.Parallel()

	llm := &capturingLLM{response: minimalLLMResponse}
	// Provide at least one exported symbol so the package does not take the
	// empty-package early-return path (which skips buildRelatedPagesXHTML).
	graph := &stubSymbolGraph{syms: []templates.Symbol{
		{
			Package:   "src/auth",
			Name:      "Verify",
			Signature: "func Verify(token string) error",
		},
	}}

	// callerPageID is in the same mode (arch) as the page under test.
	const (
		sourcePageID = "arch.src-auth"
		callerPageID = "arch.src-users"
		callerPkg    = "src/users"
	)

	// allStubResolver stubs every same-mode link whose target appears in planned.
	allStub := stubAllResolver{planned: map[string]struct{}{callerPageID: {}}}

	tmpl := architecture.New()
	input := templates.GenerateInput{
		RepoID:      "test-repo",
		PageID:      sourcePageID,
		Audience:    quality.AudienceEngineers,
		SymbolGraph: graph,
		LLM:         llm,
		Now:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		// RelatedPageIDsByLabel maps the caller package path to callerPageID.
		RelatedPageIDsByLabel: map[string]string{callerPkg: callerPageID},
		LinkResolver:          allStub,
	}

	page, err := tmpl.GeneratePackagePage(context.Background(), input, architecture.PackageInfo{
		Package: "src/auth",
		Callers: []string{callerPkg},
	})
	if err != nil {
		t.Fatalf("GeneratePackagePage: %v", err)
	}

	// The page must have at least one stub target recorded.
	if !page.HasStubMarkers() {
		t.Fatal("expected page.HasStubMarkers() == true; got false")
	}
	if len(page.StubTargetIDs()) == 0 || page.StubTargetIDs()[0] != callerPageID {
		t.Errorf("StubTargetIDs = %v; want [%q]", page.StubTargetIDs(), callerPageID)
	}

	// Find the "Related pages" freeform block and assert it contains the info macro.
	const relatedPagesHeading = "Related pages"
	var relatedBlock *ast.Block
	for i := range page.Blocks {
		if page.Blocks[i].Kind == ast.BlockKindFreeform {
			// The Related pages block is a freeform block; check its raw XHTML.
			relatedBlock = &page.Blocks[i]
			break
		}
	}
	if relatedBlock == nil {
		t.Fatal("no freeform (Related pages) block found in generated page")
	}
	xhtml := relatedBlock.Content.Freeform.Raw
	if !strings.Contains(xhtml, `ac:name="info"`) {
		t.Errorf("Related pages XHTML should contain an info macro for unready target; got:\n%s", xhtml)
	}
	_ = relatedPagesHeading
}

// stubAllResolver is a templates.LinkResolver that stubs every link whose
// target appears in its planned set (ignoring cross-mode for simplicity).
type stubAllResolver struct {
	planned map[string]struct{}
}

func (r stubAllResolver) ShouldStub(_, targetPageID string) bool {
	_, ok := r.planned[targetPageID]
	return ok
}
