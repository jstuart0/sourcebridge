// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package apiref implements the API Reference page template (A1.P1).
//
// Each API reference page documents the public API of one namespace
// (a package or sub-package) for an engineer audience. The content is
// mostly mechanical — signature extraction plus doc comments — with an
// optional one-paragraph LLM-generated summary per type.
//
// # Page structure (per namespace)
//
//	## <Namespace>
//	  one-paragraph overview (LLM-generated, optional)
//	  ### <TypeOrFunc>
//	    signature in a code block
//	    doc-comment prose
//	    citation (path:start-end)
//
// # Dependency manifest
//
// The generated manifest declares:
//   - dependency_scope: direct
//   - paths: <namespace>/**
//   - symbols: all exported identifiers in the namespace
//
// # Validator profile
//
// quality.TemplateAPIReference / quality.AudienceEngineers:
//   - citation_density ≥1/100w  (gate)
//   - code_example_present      (gate)
//   - vagueness                 (gate)
//   - factual_grounding         (gate)
//   - reading_level floor 50    (warning)
package apiref

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/citations"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
)

const templateID = "api_reference"

// Template is the API Reference page template. Construct with [New].
type Template struct{}

// New returns a ready-to-use API reference template.
func New() *Template { return &Template{} }

// Compile-time interface check.
var _ templates.Template = (*Template)(nil)

// ID returns "api_reference".
func (t *Template) ID() string { return templateID }

// Generate implements templates.Template.
//
// input.SymbolGraph must be non-nil. input.LLM is optional: when provided,
// a one-paragraph overview is generated for each namespace; when nil, only
// the mechanical extraction (signatures + doc comments) is produced.
func (t *Template) Generate(ctx context.Context, input templates.GenerateInput) (ast.Page, error) {
	if input.SymbolGraph == nil {
		return ast.Page{}, fmt.Errorf("apiref: SymbolGraph is required but was not provided")
	}

	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	syms, err := input.SymbolGraph.ExportedSymbols(input.RepoID)
	if err != nil {
		return ast.Page{}, fmt.Errorf("apiref: fetching symbols: %w", err)
	}

	// Group symbols by package (namespace).
	byPkg := make(map[string][]templates.Symbol)
	for _, s := range syms {
		byPkg[s.Package] = append(byPkg[s.Package], s)
	}

	// Sort packages for deterministic output.
	pkgs := make([]string, 0, len(byPkg))
	for p := range byPkg {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)

	pageID := pageIDFor(input.RepoID)
	var blocks []ast.Block
	hOrdinal := 0

	// Page-level H1 title.
	titleID := ast.GenerateBlockID(pageID, "", ast.BlockKindHeading, 0)
	blocks = append(blocks, ast.Block{
		ID:   titleID,
		Kind: ast.BlockKindHeading,
		Content: ast.BlockContent{Heading: &ast.HeadingContent{
			Level: 1,
			Text:  "API Reference",
		}},
		Owner:      ast.OwnerGenerated,
		LastChange: ast.BlockChange{Timestamp: now, Source: "sourcebridge"},
	})

	// Build allSymbolNames in package-sort order so the manifest's symbols list
	// is deterministic across runs (map iteration order is not guaranteed).
	var allSymbolNames []string
	for _, pkg := range pkgs {
		pkgSymsSorted := make([]templates.Symbol, len(byPkg[pkg]))
		copy(pkgSymsSorted, byPkg[pkg])
		sort.Slice(pkgSymsSorted, func(i, j int) bool { return pkgSymsSorted[i].Name < pkgSymsSorted[j].Name })
		for _, s := range pkgSymsSorted {
			allSymbolNames = append(allSymbolNames, s.Package+"."+s.Name)
		}
	}

	for _, pkg := range pkgs {
		pkgSyms := byPkg[pkg]
		sort.Slice(pkgSyms, func(i, j int) bool { return pkgSyms[i].Name < pkgSyms[j].Name })

		// H2 heading per namespace.
		h2ID := ast.GenerateBlockID(pageID, pkg, ast.BlockKindHeading, hOrdinal)
		hOrdinal++
		blocks = append(blocks, ast.Block{
			ID:   h2ID,
			Kind: ast.BlockKindHeading,
			Content: ast.BlockContent{Heading: &ast.HeadingContent{
				Level: 2,
				Text:  pkg,
			}},
			Owner:      ast.OwnerGenerated,
			LastChange: ast.BlockChange{Timestamp: now, Source: "sourcebridge"},
		})

		// Optional LLM-generated one-paragraph overview.
		if input.LLM != nil {
			overview, err := generateOverview(ctx, input.LLM, pkg, pkgSyms)
			if err == nil && overview != "" {
				pID := ast.GenerateBlockID(pageID, pkg, ast.BlockKindParagraph, 0)
				blocks = append(blocks, ast.Block{
					ID:   pID,
					Kind: ast.BlockKindParagraph,
					Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
						Markdown: overview,
					}},
					Owner:      ast.OwnerGenerated,
					LastChange: ast.BlockChange{Timestamp: now, Source: "sourcebridge"},
				})
			}
		}

		// One subsection per exported symbol.
		for symOrdinal, sym := range pkgSyms {
			symHeadingPath := pkg + "/" + sym.Name
			citation := citations.FormatFileRange(sym.FilePath, sym.StartLine, sym.EndLine)

			// H3 for each symbol.
			h3ID := ast.GenerateBlockID(pageID, symHeadingPath, ast.BlockKindHeading, symOrdinal)
			blocks = append(blocks, ast.Block{
				ID:   h3ID,
				Kind: ast.BlockKindHeading,
				Content: ast.BlockContent{Heading: &ast.HeadingContent{
					Level: 3,
					Text:  sym.Name,
				}},
				Owner:      ast.OwnerGenerated,
				LastChange: ast.BlockChange{Timestamp: now, Source: "sourcebridge"},
			})

			// Signature code block.
			sigID := ast.GenerateBlockID(pageID, symHeadingPath, ast.BlockKindCode, 0)
			blocks = append(blocks, ast.Block{
				ID:   sigID,
				Kind: ast.BlockKindCode,
				Content: ast.BlockContent{Code: &ast.CodeContent{
					Language: "go",
					Body:     sym.Signature,
				}},
				Owner:      ast.OwnerGenerated,
				LastChange: ast.BlockChange{Timestamp: now, Source: "sourcebridge"},
			})

			// Doc comment + citation as a single paragraph. The citation must
			// be in the same paragraph so the factual_grounding validator can
			// see it alongside any behavioral assertion in the doc comment.
			doc := strings.TrimSpace(sym.DocComment)
			body := doc
			if citation != "" {
				if body != "" {
					// Use soft line-break (single \n) to keep citation in same
					// paragraph as the doc comment text.
					body += " "
				}
				body += fmt.Sprintf("(%s)", citation)
			}
			if body != "" {
				pID := ast.GenerateBlockID(pageID, symHeadingPath, ast.BlockKindParagraph, 0)
				blocks = append(blocks, ast.Block{
					ID:   pID,
					Kind: ast.BlockKindParagraph,
					Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
						Markdown: body,
					}},
					Owner:      ast.OwnerGenerated,
					LastChange: ast.BlockChange{Timestamp: now, Source: "sourcebridge"},
				})
			}
		}
	}

	page := ast.Page{
		ID: pageID,
		Manifest: manifest.DependencyManifest{
			PageID:   pageID,
			Template: string(quality.TemplateAPIReference),
			Audience: string(input.Audience),
			Dependencies: manifest.Dependencies{
				Paths:           []string{"**/*.go"},
				Symbols:         allSymbolNames,
				DependencyScope: manifest.ScopeDirect,
			},
		},
		Blocks:     blocks,
		Provenance: ast.Provenance{GeneratedAt: now, ModelID: overviewModelID(input.LLM)},
	}

	return page, nil
}

// ValidatorProfile returns the Q.2 profile for the APIReference template.
// Report templates are not tier-aware in CA-150; see CA-150-followup-H if a use case emerges.
func ValidatorProfile(audience quality.Audience) (quality.Profile, bool) {
	return quality.DefaultProfile(quality.TemplateAPIReference, audience, modeltier.TierFrontier)
}

// pageIDFor derives the stable page ID for the API reference page.
func pageIDFor(repoID string) string {
	if repoID != "" {
		return repoID + ".api_reference"
	}
	return "api_reference"
}

// generateOverview asks the LLM for a single paragraph describing a package.
func generateOverview(ctx context.Context, llm templates.LLMCaller, pkg string, syms []templates.Symbol) (string, error) {
	systemPrompt := `You are writing a one-paragraph overview of a Go package for an engineer audience.
The overview must be 2–4 sentences describing what the package does, not how.
Do not use vague quantifiers. Be direct and specific.
Do not include a heading — output only the paragraph.`

	var sb strings.Builder
	fmt.Fprintf(&sb, "Package: %s\n\nExported identifiers:\n", pkg)
	for _, s := range syms {
		fmt.Fprintf(&sb, "  %s\n", s.Signature)
	}
	sb.WriteString("\nWrite the one-paragraph overview.")

	return llm.Complete(ctx, systemPrompt, sb.String())
}

// overviewModelID returns "llm" when an LLM was provided, or "" for zero-LLM generation.
func overviewModelID(llm templates.LLMCaller) string {
	if llm != nil {
		return "llm"
	}
	return ""
}
