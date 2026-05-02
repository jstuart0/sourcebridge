// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package sysoverview implements the System Overview page template (A1.P1).
//
// The system overview page is a single page describing the entire system for
// a product audience (primary) or engineer audience (alternate). It uses a
// single LLM pass with the engineer-to-pm voice profile for product audience,
// or the engineer-to-engineer voice for the engineer alternate.
//
// # Page structure
//
//	## What this system does     — one-paragraph executive summary
//	## Main capabilities         — bullet-style list of the 3–7 top capabilities
//	## Key packages              — table: package | purpose | links to arch pages
//	## External dependencies     — third-party systems this depends on
//
// # Dependency manifest
//
// The system overview opts into transitive dependency scope so that subsystem-
// level changes anywhere in the graph surface as a potential stale signal.
//
// # Validator profile
//
// quality.TemplateSystemOverview / quality.AudienceProduct (default):
//   - vagueness                     (gate)
//   - architectural_relevance        (gate)
//   - empty_headline                 (warning)
//   - reading_level floor 55         (warning)
//   - citation_density off
//   - code_example_present off
package sysoverview

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
)

const templateID = "system_overview"

// Template is the System Overview page template. Construct with [New].
type Template struct{}

// New returns a ready-to-use System Overview template.
func New() *Template { return &Template{} }

// Compile-time interface check.
var _ templates.Template = (*Template)(nil)

// ID returns "system_overview".
func (t *Template) ID() string { return templateID }

// Generate implements templates.Template.
//
// input.SymbolGraph must be non-nil to derive the list of packages.
// input.LLM must be non-nil to generate the overview prose.
// input.Audience should be quality.AudienceProduct (default) or
// quality.AudienceEngineers (alternate).
func (t *Template) Generate(ctx context.Context, input templates.GenerateInput) (ast.Page, error) {
	if input.LLM == nil {
		return ast.Page{}, fmt.Errorf("sysoverview: LLM is required but was not provided")
	}
	if input.SymbolGraph == nil {
		return ast.Page{}, fmt.Errorf("sysoverview: SymbolGraph is required but was not provided")
	}

	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	syms, err := input.SymbolGraph.ExportedSymbols(input.RepoID)
	if err != nil {
		return ast.Page{}, fmt.Errorf("sysoverview: fetching symbols: %w", err)
	}

	// Derive the unique package list for context.
	pkgSet := make(map[string]bool)
	for _, s := range syms {
		pkgSet[s.Package] = true
	}
	pkgs := make([]string, 0, len(pkgSet))
	for p := range pkgSet {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)

	systemPrompt := buildSystemPrompt(input.Audience)
	userPrompt := buildUserPrompt(input.RepoID, pkgs, syms)

	llmOut, err := input.LLM.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return ast.Page{}, fmt.Errorf("sysoverview: LLM generation: %w", err)
	}

	pageID := pageIDFor(input.RepoID)
	blocks := renderLLMOutput(pageID, llmOut, now)

	// Build downstream packages list from all known packages.
	page := ast.Page{
		ID: pageID,
		Manifest: manifest.DependencyManifest{
			PageID:   pageID,
			Template: string(quality.TemplateSystemOverview),
			Audience: string(input.Audience),
			Dependencies: manifest.Dependencies{
				Paths:              []string{"**"},
				DownstreamPackages: pkgs,
				DependencyScope:    manifest.ScopeTransitive,
			},
		},
		Blocks:     blocks,
		Provenance: ast.Provenance{GeneratedAt: now, ModelID: "llm"},
	}

	return page, nil
}

// ValidatorProfile returns the Q.2 profile for the SystemOverview template.
// Report templates are not tier-aware in CA-150; see CA-150-followup-H if a use case emerges.
func ValidatorProfile(audience quality.Audience) (quality.Profile, bool) {
	return quality.DefaultProfile(quality.TemplateSystemOverview, audience, modeltier.TierFrontier)
}

// pageIDFor derives the stable page ID for the system overview page.
func pageIDFor(repoID string) string {
	if repoID != "" {
		return repoID + ".system_overview"
	}
	return "system_overview"
}

// buildSystemPrompt assembles the LLM system prompt, tailored to the audience.
func buildSystemPrompt(audience quality.Audience) string {
	var voiceHint string
	switch audience {
	case quality.AudienceEngineers:
		voiceHint = "Write for an engineer audience: senior teammate to new hire. 70% what, 30% why. Include technical context but keep it system-level, not package-level."
	default:
		// AudienceProduct is the default for system overview.
		voiceHint = "Write for a product-manager or non-engineer reader: 20% what, 80% why and outcomes. Strip method signatures and low-level details. Use plain language."
	}

	return fmt.Sprintf(`You are a senior engineer writing a system overview page for an entire software system.
Your task is to produce a high-level description of what the system does, its main capabilities, and its key packages.

Voice rules:
%s

Format rules:
- Output exactly these four sections in order, each as a level-2 markdown heading:
  ## What this system does
  ## Main capabilities
  ## Key packages
  ## External dependencies
- "What this system does" must be 1–3 plain-English sentences. No bullet points.
- "Main capabilities" must be 3–7 bullet points, each one capability.
- "Key packages" must be a markdown table with columns: Package | Purpose.
- "External dependencies" lists third-party systems (databases, queues, external APIs). If none, write "None identified."
- Do not use vague quantifiers without a number.
- Do not include method signatures or code blocks.`, voiceHint)
}

// buildUserPrompt assembles the user-turn prompt with the system's package data.
func buildUserPrompt(repoID string, pkgs []string, syms []templates.Symbol) string {
	var sb strings.Builder

	if repoID != "" {
		fmt.Fprintf(&sb, "System: %s\n\n", repoID)
	}

	sb.WriteString("Packages in this system:\n")
	for _, p := range pkgs {
		sb.WriteString("  - " + p + "\n")
	}
	sb.WriteString("\n")

	// Provide a brief sample of exported type names per package for context.
	byPkg := make(map[string][]string)
	for _, s := range syms {
		byPkg[s.Package] = append(byPkg[s.Package], s.Name)
	}
	sb.WriteString("Sample exported identifiers per package:\n")
	for _, p := range pkgs {
		names := byPkg[p]
		if len(names) > 5 {
			names = names[:5]
		}
		fmt.Fprintf(&sb, "  %s: %s\n", p, strings.Join(names, ", "))
	}
	sb.WriteString("\nNow write the four-section system overview described in the system prompt.")
	return sb.String()
}

// renderLLMOutput parses the LLM markdown output into [ast.Block] values.
// Same strategy as architecture: parse H2 sections and body into typed blocks.
func renderLLMOutput(pageID, llmOut string, now time.Time) []ast.Block {
	var blocks []ast.Block

	// Page-level H1 title.
	titleID := ast.GenerateBlockID(pageID, "", ast.BlockKindHeading, 0)
	blocks = append(blocks, ast.Block{
		ID:   titleID,
		Kind: ast.BlockKindHeading,
		Content: ast.BlockContent{Heading: &ast.HeadingContent{
			Level: 1,
			Text:  "System Overview",
		}},
		Owner:      ast.OwnerGenerated,
		LastChange: ast.BlockChange{Timestamp: now, Source: "sourcebridge"},
	})

	lines := strings.Split(llmOut, "\n")
	type section struct {
		heading string
		lines   []string
	}
	var sections []section
	var cur *section

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			if cur != nil {
				sections = append(sections, *cur)
			}
			title := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			cur = &section{heading: title}
		} else if cur != nil {
			cur.lines = append(cur.lines, line)
		}
	}
	if cur != nil {
		sections = append(sections, *cur)
	}

	hOrdinal := 0
	for _, sec := range sections {
		hID := ast.GenerateBlockID(pageID, sec.heading, ast.BlockKindHeading, hOrdinal)
		hOrdinal++
		blocks = append(blocks, ast.Block{
			ID:   hID,
			Kind: ast.BlockKindHeading,
			Content: ast.BlockContent{Heading: &ast.HeadingContent{
				Level: 2,
				Text:  sec.heading,
			}},
			Owner:      ast.OwnerGenerated,
			LastChange: ast.BlockChange{Timestamp: now, Source: "sourcebridge"},
		})

		// For the "Key packages" section, try to parse as a table.
		if strings.EqualFold(sec.heading, "Key packages") {
			tableBlock := tryParseTable(pageID, sec.heading, sec.lines, now)
			if tableBlock != nil {
				blocks = append(blocks, *tableBlock)
				continue
			}
		}

		// Generic: merge all prose into one paragraph block per section.
		body := strings.TrimSpace(strings.Join(sec.lines, "\n"))
		if body != "" {
			pID := ast.GenerateBlockID(pageID, sec.heading, ast.BlockKindParagraph, 0)
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

	return blocks
}

// tryParseTable attempts to parse lines as a markdown table and return a
// [ast.BlockKindTable] block. Returns nil when the lines don't look like a table.
func tryParseTable(pageID, headingPath string, lines []string, now time.Time) *ast.Block {
	// Find the header row (the first line starting with "|").
	var tableLines []string
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "|") {
			tableLines = append(tableLines, strings.TrimSpace(l))
		}
	}
	if len(tableLines) < 2 {
		return nil
	}

	parseRow := func(line string) []string {
		line = strings.Trim(line, "| ")
		parts := strings.Split(line, "|")
		out := make([]string, len(parts))
		for i, p := range parts {
			out[i] = strings.TrimSpace(p)
		}
		return out
	}

	headers := parseRow(tableLines[0])
	// Skip separator row (index 1).
	var rows [][]string
	for _, l := range tableLines[2:] {
		if strings.Contains(l, "---") {
			continue
		}
		rows = append(rows, parseRow(l))
	}

	if len(headers) == 0 {
		return nil
	}

	tID := ast.GenerateBlockID(pageID, headingPath, ast.BlockKindTable, 0)
	blk := &ast.Block{
		ID:   tID,
		Kind: ast.BlockKindTable,
		Content: ast.BlockContent{Table: &ast.TableContent{
			Headers: headers,
			Rows:    rows,
		}},
		Owner:      ast.OwnerGenerated,
		LastChange: ast.BlockChange{Timestamp: now, Source: "sourcebridge"},
	}
	return blk
}
