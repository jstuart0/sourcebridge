// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package glossary implements the A2.1 Glossary report template.
//
// The Glossary is a zero-LLM template that extracts exported symbols from the
// symbol graph and renders them as an [ast.Page]. No LLM is invoked; the
// content is drawn exclusively from the indexed symbol data.
//
// # Page structure
//
//	## <package> (one heading block per package, alphabetical)
//	  <Name> — <signature>
//	  <doc-comment prose>                    (one paragraph block per symbol)
//
// Symbols within each package are sorted alphabetically by name. Packages are
// sorted alphabetically by import path.
//
// # Validator profile
//
// The [quality.TemplateGlossary] profile is applied: only factual_grounding
// gates, and mechanical-extraction pages are not penalised by voice validators.
//
// # Citations
//
// Each symbol paragraph carries a citation in the form
// (file/path.go:startLine-endLine) so readers can jump to the declaration.
package glossary

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/citations"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
)

const templateID = "glossary"

// Template is the Glossary template. Construct with [New].
type Template struct{}

// New returns a ready-to-use Glossary template.
func New() *Template { return &Template{} }

// Compile-time interface check.
var _ templates.Template = (*Template)(nil)

// ID returns "glossary".
func (t *Template) ID() string { return templateID }

// Generate produces a Glossary [ast.Page] for the given repo.
//
// input.SymbolGraph must be non-nil; input.LLM and input.GitLog are not used.
// If input.Now is zero, time.Now().UTC() is used.
func (t *Template) Generate(ctx context.Context, input templates.GenerateInput) (ast.Page, error) {
	if input.SymbolGraph == nil {
		return ast.Page{}, fmt.Errorf("glossary: SymbolGraph is required but was not provided")
	}

	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	syms, err := input.SymbolGraph.ExportedSymbols(input.RepoID)
	if err != nil {
		return ast.Page{}, fmt.Errorf("glossary: fetching exported symbols: %w", err)
	}

	// Group symbols by package.
	byPackage := make(map[string][]templates.Symbol)
	for _, s := range syms {
		byPackage[s.Package] = append(byPackage[s.Package], s)
	}

	// Sort packages alphabetically.
	packages := make([]string, 0, len(byPackage))
	for pkg := range byPackage {
		packages = append(packages, pkg)
	}
	sort.Strings(packages)

	pageID := pageIDFor(input.RepoID)
	var blocks []ast.Block

	// Heading ordinal tracker per heading level.
	pkgOrdinal := 0

	for _, pkg := range packages {
		pkgSyms := byPackage[pkg]

		// Sort symbols within the package alphabetically.
		sort.Slice(pkgSyms, func(i, j int) bool {
			return pkgSyms[i].Name < pkgSyms[j].Name
		})

		// Package heading block (H2).
		headingID := ast.GenerateBlockID(pageID, pkg, ast.BlockKindHeading, pkgOrdinal)
		pkgOrdinal++
		blocks = append(blocks, ast.Block{
			ID:   headingID,
			Kind: ast.BlockKindHeading,
			Content: ast.BlockContent{Heading: &ast.HeadingContent{
				Level: 2,
				Text:  pkg,
			}},
			Owner:      ast.OwnerGenerated,
			LastChange: ast.BlockChange{SHA: "", Timestamp: now, Source: "sourcebridge"},
		})

		// One paragraph block per symbol.
		for symOrdinal, sym := range pkgSyms {
			citation := citations.FormatFileRange(sym.FilePath, sym.StartLine, sym.EndLine)
			body := buildSymbolParagraph(sym, citation)

			symID := ast.GenerateBlockID(pageID, pkg, ast.BlockKindParagraph, symOrdinal)
			blocks = append(blocks, ast.Block{
				ID:   symID,
				Kind: ast.BlockKindParagraph,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
					Markdown: body,
				}},
				Owner:      ast.OwnerGenerated,
				LastChange: ast.BlockChange{SHA: "", Timestamp: now, Source: "sourcebridge"},
			})
		}
	}

	page := ast.Page{
		ID: pageID,
		Manifest: manifest.DependencyManifest{
			PageID:   pageID,
			Template: string(quality.TemplateGlossary),
			Audience: string(input.Audience),
			Dependencies: manifest.Dependencies{
				DependencyScope: manifest.ScopeDirect,
			},
		},
		Blocks: blocks,
		Provenance: ast.Provenance{
			GeneratedAt:    now,
			GeneratedBySHA: "",
			ModelID:        "", // zero-LLM
		},
	}

	return page, nil
}

// buildSymbolParagraph renders one symbol as a markdown paragraph.
// Format:
//
//	**Name** `signature` (file:start-end)
//	doc-comment prose
func buildSymbolParagraph(sym templates.Symbol, citation string) string {
	var sb strings.Builder

	// Bold name, code-formatted signature, citation.
	sb.WriteString(fmt.Sprintf("**%s** `%s`", sym.Name, sym.Signature))
	if citation != "" {
		sb.WriteString(fmt.Sprintf(" (%s)", citation))
	}

	// Doc comment on the next line, if present.
	doc := strings.TrimSpace(sym.DocComment)
	if doc != "" {
		sb.WriteString("\\\n")
		sb.WriteString(doc)
	}

	return sb.String()
}

// pageIDFor derives the stable page ID for a glossary page.
// The ID is "<repoID>.glossary" when repoID is non-empty; otherwise "glossary".
func pageIDFor(repoID string) string {
	if repoID != "" {
		return repoID + ".glossary"
	}
	return templateID
}

// ValidatorProfile returns the Q.2 profile for the Glossary template.
// Provided as a convenience so callers can run quality.Run without
// looking up the profile separately.
func ValidatorProfile(audience quality.Audience) (quality.Profile, bool) {
	return quality.DefaultProfile(quality.TemplateGlossary, audience)
}
