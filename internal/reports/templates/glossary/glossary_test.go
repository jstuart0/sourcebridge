// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package glossary_test

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates/glossary"
)

// --- stub SymbolGraph ---

type stubSymbolGraph struct {
	symbols []templates.Symbol
}

func (s *stubSymbolGraph) ExportedSymbols(_ string) ([]templates.Symbol, error) {
	return s.symbols, nil
}

// --- helpers ---

var fixedTime = time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC)

func newInput(syms []templates.Symbol) templates.GenerateInput {
	return templates.GenerateInput{
		RepoID:      "testrepo",
		Audience:    quality.AudienceEngineers,
		SymbolGraph: &stubSymbolGraph{symbols: syms},
		Now:         fixedTime,
	}
}

// --- tests ---

func TestGlossary_ID(t *testing.T) {
	g := glossary.New()
	if g.ID() != "glossary" {
		t.Fatalf("expected ID=glossary, got %q", g.ID())
	}
}

func TestGlossary_EmptyGraph(t *testing.T) {
	g := glossary.New()
	page, err := g.Generate(context.Background(), newInput(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page.Blocks) != 0 {
		t.Errorf("expected 0 blocks for empty graph, got %d", len(page.Blocks))
	}
}

func TestGlossary_RequiresSymbolGraph(t *testing.T) {
	g := glossary.New()
	input := newInput(nil)
	input.SymbolGraph = nil
	_, err := g.Generate(context.Background(), input)
	if err == nil {
		t.Fatal("expected error when SymbolGraph is nil, got none")
	}
}

func TestGlossary_PackagesAreSortedAlphabetically(t *testing.T) {
	syms := []templates.Symbol{
		{Package: "internal/zoo", Name: "Zoo", Signature: "type Zoo struct"},
		{Package: "internal/auth", Name: "Middleware", Signature: "func Middleware(...)"},
		{Package: "internal/bar", Name: "Bar", Signature: "func Bar()"},
	}
	g := glossary.New()
	page, err := g.Generate(context.Background(), newInput(syms))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var headings []string
	for _, blk := range page.Blocks {
		if blk.Kind == ast.BlockKindHeading {
			headings = append(headings, blk.Content.Heading.Text)
		}
	}

	if !sort.StringsAreSorted(headings) {
		t.Errorf("headings are not sorted: %v", headings)
	}
}

func TestGlossary_SymbolsWithinPackageAreSortedAlphabetically(t *testing.T) {
	syms := []templates.Symbol{
		{Package: "internal/auth", Name: "Token", Signature: "type Token string", FilePath: "internal/auth/token.go", StartLine: 10, EndLine: 12},
		{Package: "internal/auth", Name: "Middleware", Signature: "func Middleware(...)", FilePath: "internal/auth/middleware.go", StartLine: 1, EndLine: 20},
		{Package: "internal/auth", Name: "RequireRole", Signature: "func RequireRole(...)", FilePath: "internal/auth/role.go", StartLine: 5, EndLine: 15},
	}
	g := glossary.New()
	page, err := g.Generate(context.Background(), newInput(syms))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var names []string
	for _, blk := range page.Blocks {
		if blk.Kind != ast.BlockKindParagraph {
			continue
		}
		// First bolded name in the paragraph.
		md := blk.Content.Paragraph.Markdown
		start := strings.Index(md, "**")
		end := strings.Index(md[start+2:], "**")
		if start >= 0 && end >= 0 {
			names = append(names, md[start+2:start+2+end])
		}
	}

	if !sort.StringsAreSorted(names) {
		t.Errorf("symbols within package not sorted: %v", names)
	}
}

func TestGlossary_CitationPresentInParagraph(t *testing.T) {
	syms := []templates.Symbol{
		{
			Package:   "internal/auth",
			Name:      "Middleware",
			Signature: "func Middleware(next http.Handler) http.Handler",
			DocComment: "Middleware wraps next and enforces authentication.",
			FilePath:  "internal/auth/middleware.go",
			StartLine: 42,
			EndLine:   55,
		},
	}
	g := glossary.New()
	page, err := g.Generate(context.Background(), newInput(syms))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, blk := range page.Blocks {
		if blk.Kind == ast.BlockKindParagraph {
			if strings.Contains(blk.Content.Paragraph.Markdown, "internal/auth/middleware.go:42-55") {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected citation (internal/auth/middleware.go:42-55) in paragraph, not found")
	}
}

func TestGlossary_DocCommentIncluded(t *testing.T) {
	syms := []templates.Symbol{
		{
			Package:    "internal/auth",
			Name:       "RequireRole",
			Signature:  "func RequireRole(role string) http.HandlerFunc",
			DocComment: "RequireRole panics if called before Init completes.",
			FilePath:   "internal/auth/role.go",
			StartLine:  1,
			EndLine:    10,
		},
	}
	g := glossary.New()
	page, err := g.Generate(context.Background(), newInput(syms))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, blk := range page.Blocks {
		if blk.Kind == ast.BlockKindParagraph {
			if strings.Contains(blk.Content.Paragraph.Markdown, "RequireRole panics if called before Init completes.") {
				return
			}
		}
	}
	t.Error("doc comment not found in any paragraph block")
}

func TestGlossary_OneHeadingPerPackage(t *testing.T) {
	syms := []templates.Symbol{
		{Package: "internal/auth", Name: "A", Signature: "func A()"},
		{Package: "internal/auth", Name: "B", Signature: "func B()"},
		{Package: "internal/jobs", Name: "Dispatcher", Signature: "type Dispatcher struct"},
	}
	g := glossary.New()
	page, err := g.Generate(context.Background(), newInput(syms))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	headings := 0
	for _, blk := range page.Blocks {
		if blk.Kind == ast.BlockKindHeading {
			headings++
		}
	}
	if headings != 2 {
		t.Errorf("expected 2 heading blocks (one per package), got %d", headings)
	}
}

func TestGlossary_BlockIDsAreStable(t *testing.T) {
	syms := []templates.Symbol{
		{Package: "internal/auth", Name: "Middleware", Signature: "func Middleware(...)"},
	}
	g := glossary.New()

	page1, err1 := g.Generate(context.Background(), newInput(syms))
	page2, err2 := g.Generate(context.Background(), newInput(syms))
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v, %v", err1, err2)
	}

	if len(page1.Blocks) != len(page2.Blocks) {
		t.Fatalf("block count changed between calls: %d vs %d", len(page1.Blocks), len(page2.Blocks))
	}
	for i := range page1.Blocks {
		if page1.Blocks[i].ID != page2.Blocks[i].ID {
			t.Errorf("block[%d] ID changed: %q vs %q", i, page1.Blocks[i].ID, page2.Blocks[i].ID)
		}
	}
}

func TestGlossary_PageManifest(t *testing.T) {
	g := glossary.New()
	page, err := g.Generate(context.Background(), newInput(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if page.Manifest.Template != "glossary" {
		t.Errorf("expected template=glossary, got %q", page.Manifest.Template)
	}
	if page.Manifest.Audience != string(quality.AudienceEngineers) {
		t.Errorf("expected audience=%s, got %q", quality.AudienceEngineers, page.Manifest.Audience)
	}
}

func TestGlossary_ProvenanceIsZeroLLM(t *testing.T) {
	g := glossary.New()
	page, err := g.Generate(context.Background(), newInput(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if page.Provenance.ModelID != "" {
		t.Errorf("zero-LLM page should have empty ModelID, got %q", page.Provenance.ModelID)
	}
}

func TestGlossary_MultiplePackagesMultipleSymbols(t *testing.T) {
	syms := []templates.Symbol{
		{Package: "internal/auth", Name: "Init", Signature: "func Init(cfg Config)"},
		{Package: "internal/auth", Name: "Middleware", Signature: "func Middleware(...)"},
		{Package: "internal/jobs", Name: "Dispatcher", Signature: "type Dispatcher struct"},
		{Package: "internal/jobs", Name: "Worker", Signature: "type Worker struct"},
		{Package: "internal/jobs", Name: "Job", Signature: "type Job struct"},
	}
	g := glossary.New()
	page, err := g.Generate(context.Background(), newInput(syms))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	headingCount := 0
	paragraphCount := 0
	for _, blk := range page.Blocks {
		switch blk.Kind {
		case ast.BlockKindHeading:
			headingCount++
		case ast.BlockKindParagraph:
			paragraphCount++
		}
	}
	// 2 packages → 2 headings; 5 symbols → 5 paragraphs.
	if headingCount != 2 {
		t.Errorf("expected 2 headings, got %d", headingCount)
	}
	if paragraphCount != 5 {
		t.Errorf("expected 5 paragraphs, got %d", paragraphCount)
	}
}

func TestGlossaryValidatorProfile(t *testing.T) {
	profile, ok := glossary.ValidatorProfile(quality.AudienceEngineers)
	if !ok {
		t.Fatal("expected a validator profile for glossary/for-engineers, got none")
	}
	if profile.Template != quality.TemplateGlossary {
		t.Errorf("expected TemplateGlossary, got %v", profile.Template)
	}
}
