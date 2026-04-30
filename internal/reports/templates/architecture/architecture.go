// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package architecture implements the Architecture page template (A1.P1).
//
// Each architecture page documents one top-level package for an engineer
// audience. The page is generated via a single LLM pass using the
// engineer-to-engineer voice profile. Inputs are the package's exported symbols,
// caller/callee relationships, and doc comments.
//
// # Page structure
//
//	## Overview          — what this package does in 1–3 sentences
//	## Key types         — table of exported types with one-line purpose
//	## Public API        — prose walkthrough of the primary entry points
//	## Dependencies      — packages this package depends on (callees)
//	## Used by           — packages that depend on this package (callers)
//	## Code example      — illustrative usage drawn from real callers
//
// # Dependency manifest
//
// The generated manifest declares:
//   - dependency_scope: direct
//   - paths: <package>/**
//   - stale_when: signature_change_in the package's exported symbols
//
// # Validator profile
//
// quality.TemplateArchitecture / quality.AudienceEngineers:
//   - citation_density ≥1/200w  (gate)
//   - vagueness                 (gate)
//   - factual_grounding         (gate)
//   - empty_headline            (warning)
//   - reading_level floor 50    (warning)
//   - code_example_present      (warning)
package architecture

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/markdown"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
)

const (
	// maxSymbolsInPrompt caps how many symbols are included in the user
	// prompt to keep the total context within ~30 K input tokens.
	maxSymbolsInPrompt = 40
	// maxBodyLines is the maximum source lines emitted per symbol body.
	// Suppliers cap at 200; we re-enforce here as a safety net.
	maxBodyLines = 200
)

const templateID = "architecture"

// Template is the Architecture page template. Construct with [New].
type Template struct{}

// New returns a ready-to-use Architecture template.
func New() *Template { return &Template{} }

// Compile-time interface check.
var _ templates.Template = (*Template)(nil)

// ID returns "architecture".
func (t *Template) ID() string { return templateID }

// KnowledgeArtifactSummary is the narrow view of a pre-computed knowledge
// artifact passed into the architecture template. The orchestrator package
// owns the canonical definition; this mirror lives here so the architecture
// package never imports orchestrator (circular import prevention).
type KnowledgeArtifactSummary struct {
	Type        string // e.g. "cliff_notes", "architecture_diagram"
	Audience    string
	Depth       string // "summary", "medium", or "deep"
	ScopePath   string // module / package path the artifact covers
	Sections    []KnowledgeSection
	RevisionFp  string    // matches the understanding revisionFp
	GeneratedAt time.Time
}

// KnowledgeSection is a single titled section from a knowledge artifact.
type KnowledgeSection struct {
	Title    string
	Content  string // markdown body
	Summary  string // 1-2 line synopsis if available
	Evidence []KnowledgeEvidence
}

// KnowledgeEvidence is a traceable file/line reference from a section, surfaced
// as a verified source citation in the architecture page prompt.
type KnowledgeEvidence struct {
	FilePath  string
	LineStart int
	LineEnd   int
	Rationale string
}

// PackageInput carries the package-specific data needed by Generate.
// Callers populate this and embed it in GenerateInput.Config.
//
// Because templates.GenerateInput.Config is templates.TemplateConfig (a
// struct for common flags), the architecture template reads its
// package-specific inputs from GenerateInput directly via the shared ports:
//   - input.SymbolGraph  → exported symbols for the package
//   - input.GitLog       → unused
//   - input.LLM          → required for the generation pass
//
// The package path to document is conveyed via the RepoID field on
// GenerateInput combined with the PackagePath set in the Options embedded
// in GenerateInput.Config via the Extras map pattern. We keep it simple: callers
// pass PackagePath as a separate parameter via [GeneratePackagePage].
//
// For A1.P1, the caller-facing entry point is [GeneratePackagePage].
type PackageInfo struct {
	// Package is the fully-qualified import path of the package to document.
	Package string

	// Callers is the list of import paths that import this package.
	// Used to populate the "Used by" section.
	Callers []string

	// Callees is the list of import paths that this package imports.
	// Used to populate the "Dependencies" section.
	Callees []string

	// KnowledgeArtifacts contains pre-computed knowledge artifacts for this
	// package, in preference order (deepest + freshest first). When non-empty,
	// GeneratePackagePage builds a different prompt that opens with the artifact
	// sections as authoritative curated context, then includes symbol data as
	// verification material. When empty, behaviour is identical to the
	// raw-symbol-only path used today.
	KnowledgeArtifacts []KnowledgeArtifactSummary
}

// GeneratePackagePage generates an architecture page for a single package.
// This is the primary entry point; it wraps [Template.Generate] with the
// package-specific setup.
//
// pkg describes the package to document. input.SymbolGraph must be non-nil
// and must return symbols for pkg.Package. input.LLM must be non-nil.
func (t *Template) GeneratePackagePage(ctx context.Context, input templates.GenerateInput, pkg PackageInfo) (ast.Page, error) {
	if input.LLM == nil {
		return ast.Page{}, fmt.Errorf("architecture: LLM is required but was not provided")
	}
	if input.SymbolGraph == nil {
		return ast.Page{}, fmt.Errorf("architecture: SymbolGraph is required but was not provided")
	}
	if pkg.Package == "" {
		return ast.Page{}, fmt.Errorf("architecture: PackageInfo.Package must not be empty")
	}

	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	syms, err := input.SymbolGraph.ExportedSymbols(input.RepoID)
	if err != nil {
		return ast.Page{}, fmt.Errorf("architecture: fetching symbols: %w", err)
	}

	// Filter to the requested package.
	var pkgSyms []templates.Symbol
	for _, s := range syms {
		if s.Package == pkg.Package {
			pkgSyms = append(pkgSyms, s)
		}
	}

	pageID := pageIDFor(input.RepoID, pkg.Package)

	// Empty-package fallback: no LLM call needed; emit a single informational
	// paragraph so the page is not blank and the manifest can regenerate when
	// exported symbols appear later.
	if len(pkgSyms) == 0 {
		placeholder := fmt.Sprintf(
			"Package `%s` contains no exported public surface at this time. "+
				"It may expose only internal helpers. This page will regenerate "+
				"automatically when exported symbols appear.",
			pkg.Package,
		)
		titleID := ast.GenerateBlockID(pageID, "", ast.BlockKindHeading, 0)
		paraID := ast.GenerateBlockID(pageID, "Overview", ast.BlockKindParagraph, 0)
		blocks := []ast.Block{
			{
				ID:   titleID,
				Kind: ast.BlockKindHeading,
				Content: ast.BlockContent{Heading: &ast.HeadingContent{
					Level: 1,
					Text:  "Architecture: " + pkg.Package,
				}},
				Owner:      ast.OwnerGenerated,
				LastChange: ast.BlockChange{Timestamp: now, Source: "sourcebridge"},
			},
			{
				ID:   paraID,
				Kind: ast.BlockKindParagraph,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
					Markdown: placeholder,
				}},
				Owner:      ast.OwnerGenerated,
				LastChange: ast.BlockChange{Timestamp: now, Source: "sourcebridge"},
			},
		}
		return ast.Page{
			ID: pageID,
			Manifest: manifest.DependencyManifest{
				PageID:   pageID,
				Template: string(quality.TemplateArchitecture),
				Audience: string(input.Audience),
				Dependencies: manifest.Dependencies{
					Paths:           []string{pkg.Package + "/**"},
					DependencyScope: manifest.ScopeDirect,
				},
				// Regenerate as soon as any symbol appears in this package.
				StaleWhen: []manifest.StaleCondition{
					{SignatureChangeIn: []string{pkg.Package + ".*"}},
				},
			},
			Blocks:     blocks,
			Provenance: ast.Provenance{GeneratedAt: now, ModelID: "llm"},
		}, nil
	}

	systemPrompt := buildSystemPrompt(input.Audience, len(pkg.KnowledgeArtifacts) > 0)
	userPrompt := buildUserPrompt(pkg, pkgSyms)

	llmOut, err := input.LLM.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return ast.Page{}, fmt.Errorf("architecture: LLM generation for %q: %w", pkg.Package, err)
	}

	blocks := renderLLMOutput(pageID, pkg.Package, llmOut, now)

	// When the page was generated from curated knowledge artifacts, prepend an
	// italicised provenance note so readers know the source of the analysis.
	if len(pkg.KnowledgeArtifacts) > 0 {
		noteID := ast.GenerateBlockID(pageID, "artifact_note", ast.BlockKindParagraph, 0)
		noteBlock := ast.Block{
			ID:   noteID,
			Kind: ast.BlockKindParagraph,
			Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
				Markdown: "*Generated from curated knowledge for this package. Falls back to raw-symbol analysis when curated analysis is unavailable.*",
			}},
			Owner:      ast.OwnerGenerated,
			LastChange: ast.BlockChange{Timestamp: now, Source: "sourcebridge"},
		}
		// Insert immediately after the H1 title block (index 0).
		blocks = append([]ast.Block{blocks[0], noteBlock}, blocks[1:]...)
	}

	// Append a "Related pages" block when caller/callee cluster labels are known.
	// Each label is rendered as a Confluence cross-page link so readers can
	// navigate directly to the related architecture page.
	var stubTargetIDs []string
	if len(pkg.Callers) > 0 || len(pkg.Callees) > 0 {
		var relatedXHTML string
		relatedXHTML, stubTargetIDs = buildRelatedPagesXHTML(
			input.RepoID,
			pageID,
			pkg.Callers,
			pkg.Callees,
			input.RelatedPageIDsByLabel,
			input.LinkResolver,
		)
		relID := ast.GenerateBlockID(pageID, "Related pages", ast.BlockKindFreeform, 0)
		blocks = append(blocks, ast.Block{
			ID:   relID,
			Kind: ast.BlockKindFreeform,
			Content: ast.BlockContent{Freeform: &ast.FreeformContent{
				Raw: relatedXHTML,
			}},
			Owner:      ast.OwnerGenerated,
			LastChange: ast.BlockChange{Timestamp: now, Source: "sourcebridge"},
		})
	}

	staleSymbols := symbolNames(pkgSyms)

	page := ast.Page{
		ID: pageID,
		Manifest: manifest.DependencyManifest{
			PageID:   pageID,
			Template: string(quality.TemplateArchitecture),
			Audience: string(input.Audience),
			Dependencies: manifest.Dependencies{
				Paths:              []string{pkg.Package + "/**"},
				Symbols:            staleSymbols,
				UpstreamPackages:   pkg.Callers,
				DownstreamPackages: pkg.Callees,
				DependencyScope:    manifest.ScopeDirect,
			},
			StaleWhen: []manifest.StaleCondition{
				{SignatureChangeIn: staleSymbols},
			},
		},
		Blocks:            blocks,
		Provenance:        ast.Provenance{GeneratedAt: now, ModelID: "llm"},
		StubTargetPageIDs: stubTargetIDs,
	}

	return page, nil
}

// Generate implements templates.Template. For the architecture template,
// callers should prefer [GeneratePackagePage] which carries the package-
// specific inputs explicitly. This method is provided for registry compatibility.
// It requires input.SymbolGraph and input.LLM to be non-nil, and generates
// a page per every unique package returned from the symbol graph, then returns
// only the first one (the registry model passes one page-request at a time).
func (t *Template) Generate(ctx context.Context, input templates.GenerateInput) (ast.Page, error) {
	if input.SymbolGraph == nil || input.LLM == nil {
		return ast.Page{}, fmt.Errorf("architecture: SymbolGraph and LLM are required")
	}
	syms, err := input.SymbolGraph.ExportedSymbols(input.RepoID)
	if err != nil {
		return ast.Page{}, fmt.Errorf("architecture: fetching symbols: %w", err)
	}
	// Derive the first unique package.
	seen := make(map[string]bool)
	for _, s := range syms {
		if !seen[s.Package] {
			seen[s.Package] = true
			return t.GeneratePackagePage(ctx, input, PackageInfo{Package: s.Package})
		}
	}
	return ast.Page{}, fmt.Errorf("architecture: no symbols found for repo %q", input.RepoID)
}

// ValidatorProfile returns the Q.2 profile for the Architecture template.
func ValidatorProfile(audience quality.Audience) (quality.Profile, bool) {
	return quality.DefaultProfile(quality.TemplateArchitecture, audience)
}

// pageIDFor derives the stable page ID for an architecture page.
// Format: "<repoID>.arch.<package>" where package path separators become dots.
func pageIDFor(repoID, pkg string) string {
	slug := strings.ReplaceAll(pkg, "/", ".")
	slug = strings.ReplaceAll(slug, "-", "_")
	if repoID != "" {
		return repoID + ".arch." + slug
	}
	return "arch." + slug
}

// symbolNames returns just the symbol Name fields.
func symbolNames(syms []templates.Symbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = s.Package + "." + s.Name
	}
	return out
}

// buildSystemPrompt assembles the LLM system prompt for the architecture template.
// When hasArtifacts is true, the prompt instructs the model to treat the curated
// knowledge sections as authoritative ground truth and use raw symbols only to
// fill gaps the artifacts do not cover.
func buildSystemPrompt(audience quality.Audience, hasArtifacts bool) string {
	var voiceHint string
	switch audience {
	case quality.AudienceProduct:
		voiceHint = "Write for a product-manager audience: focus on capabilities and outcomes, not implementation details. Omit method signatures."
	case quality.AudienceOperators:
		voiceHint = "Write for an SRE/on-call audience: focus on failure modes, observability surfaces, and runbook entry points."
	default:
		voiceHint = "Write for an engineer audience: senior teammate to new hire. 70% what, 30% why. Direct and specific."
	}

	var artifactGuidance string
	if hasArtifacts {
		artifactGuidance = `
Curated knowledge guidance:
- The user prompt includes one or more "Curated knowledge artifact" sections. These are pre-analysed, human-reviewed summaries of the codebase. Treat them as authoritative ground truth.
- Prefer curated artifact content over your own inference from raw symbols. Only fall back to inferring from raw symbols when the artifacts are silent on a topic.
- When artifacts include "Verified source references", use those citations preferentially. Cite using the (path/file.go:start-end) convention.
- Do not contradict or water down claims the artifacts make unless the raw symbol evidence is unambiguously inconsistent.
`
	}

	return fmt.Sprintf(`You are a senior engineer writing architecture documentation for a software package.
Your task is to produce one architecture page describing the package's role, public API, and relationships.

Voice rules:
%s
%s
Format rules:
- Output exactly these six sections in order, each as a level-2 markdown heading:
  ## Overview
  ## Key types
  ## Public API
  ## Dependencies
  ## Used by
  ## Code example
- Use only what you can infer from the provided symbols and source excerpts. Quote behaviour from the code, not from imagination.
- Every behavioral assertion must end with a citation in the form (path/file.go:start-end).
- Do not use vague quantifiers ("various", "many", "several") without a specific number.
- Keep prose to the point: aim for 200–600 words total excluding code.
- The "Code example" section must contain at least one fenced Go code block drawn from the actual source excerpts.
- If a section has no content (e.g. no callers, no callees), write one sentence explaining that rather than omitting the heading.`, voiceHint, artifactGuidance)
}

// buildUserPrompt assembles the user-turn prompt with the package data.
// It emits full doc comments and fenced source excerpts so the model has
// concrete code to quote from. Symbols are capped at maxSymbolsInPrompt to
// keep the prompt within ~30 K input tokens.
//
// When pkg.KnowledgeArtifacts is non-empty, the prompt opens with the curated
// artifact sections as authoritative context, followed by raw symbol data as
// supporting verification material. The order (artifacts first, symbols second)
// signals to the model which source it should treat as ground truth.
func buildUserPrompt(pkg PackageInfo, syms []templates.Symbol) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "Package: %s\n\n", pkg.Package)

	// ── Curated knowledge artifacts (highest-priority context) ────────────────
	if len(pkg.KnowledgeArtifacts) > 0 {
		sb.WriteString("## Curated knowledge artifacts\n\n")
		sb.WriteString("The following sections were produced by a prior deep analysis of this package.\n")
		sb.WriteString("Treat them as authoritative. Use the raw symbols below only to fill gaps.\n\n")

		for i, art := range pkg.KnowledgeArtifacts {
			fmt.Fprintf(&sb, "### Curated knowledge artifact %d (type=%s depth=%s)\n\n", i+1, art.Type, art.Depth)
			for _, sec := range art.Sections {
				fmt.Fprintf(&sb, "#### %s\n\n", sec.Title)
				if sec.Content != "" {
					sb.WriteString(sec.Content)
					sb.WriteString("\n\n")
				}
				if sec.Summary != "" {
					fmt.Fprintf(&sb, "_Synopsis: %s_\n\n", sec.Summary)
				}
			}
		}

		// Collect all evidence across all artifacts/sections and emit as a
		// de-duplicated "verified source references" block. The model is
		// instructed to cite from these refs first using the (file:start-end)
		// convention.
		type evKey struct{ fp string; lo, hi int }
		seen := make(map[evKey]struct{})
		var evLines []string
		for _, art := range pkg.KnowledgeArtifacts {
			for _, sec := range art.Sections {
				for _, ev := range sec.Evidence {
					if ev.FilePath == "" {
						continue
					}
					k := evKey{ev.FilePath, ev.LineStart, ev.LineEnd}
					if _, ok := seen[k]; ok {
						continue
					}
					seen[k] = struct{}{}
					line := fmt.Sprintf("  - %s:%d-%d", ev.FilePath, ev.LineStart, ev.LineEnd)
					if ev.Rationale != "" {
						line += " — " + ev.Rationale
					}
					evLines = append(evLines, line)
				}
			}
		}
		if len(evLines) > 0 {
			sb.WriteString("### Verified source references\n\n")
			sb.WriteString("Prefer these citations when writing (path/file.go:start-end) references:\n\n")
			for _, l := range evLines {
				sb.WriteString(l)
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}

		sb.WriteString("---\n\n")
		sb.WriteString("## Supporting raw symbol data\n\n")
		sb.WriteString("The symbols below are included for verification and to fill any gaps the curated analysis does not cover.\n\n")
	}

	// ── Package-level doc comment ─────────────────────────────────────────────
	pkgDoc := packageLevelDoc(syms)
	if pkgDoc != "" {
		sb.WriteString("Package documentation:\n")
		sb.WriteString(pkgDoc)
		sb.WriteString("\n\n")
	} else {
		sb.WriteString("(no package-level documentation found)\n\n")
	}

	if len(syms) > 0 {
		// Cap to maxSymbolsInPrompt to stay within token budget.
		capped := syms
		if len(capped) > maxSymbolsInPrompt {
			capped = capped[:maxSymbolsInPrompt]
		}

		sb.WriteString("Exported symbols with source:\n")
		for _, s := range capped {
			// Full signature + file reference.
			if s.FilePath != "" {
				fmt.Fprintf(&sb, "\n### %s (%s:%d-%d)\n", s.Name, s.FilePath, s.StartLine, s.EndLine)
			} else {
				fmt.Fprintf(&sb, "\n### %s\n", s.Name)
			}

			// Full doc comment, not just the first sentence.
			if s.DocComment != "" {
				sb.WriteString(s.DocComment)
				sb.WriteString("\n")
			}

			// Signature.
			fmt.Fprintf(&sb, "\nSignature: `%s`\n", s.Signature)

			// Source body excerpt.
			if s.Body != "" {
				body := capLines(s.Body, maxBodyLines)
				if s.FilePath != "" {
					fmt.Fprintf(&sb, "\n```go\n// %s:%d-%d\n%s\n```\n", s.FilePath, s.StartLine, s.EndLine, body)
				} else {
					fmt.Fprintf(&sb, "\n```go\n%s\n```\n", body)
				}
			}
		}
		if len(syms) > maxSymbolsInPrompt {
			fmt.Fprintf(&sb, "\n(%d additional symbols omitted for brevity)\n", len(syms)-maxSymbolsInPrompt)
		}
		sb.WriteString("\n")
	}

	if len(pkg.Callers) > 0 {
		fmt.Fprintf(&sb, "Packages that import this package (callers):\n")
		for _, c := range pkg.Callers {
			fmt.Fprintf(&sb, "  - %s\n", c)
		}
		sb.WriteString("\n")
	}

	if len(pkg.Callees) > 0 {
		fmt.Fprintf(&sb, "Packages that this package imports (callees):\n")
		for _, c := range pkg.Callees {
			fmt.Fprintf(&sb, "  - %s\n", c)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Now write the six-section architecture page described in the system prompt.")
	return sb.String()
}

// packageLevelDoc returns the first symbol doc that starts with "Package ",
// which is the Go convention for package-level documentation. Falls back to
// the first non-empty doc comment when no package-doc is found.
func packageLevelDoc(syms []templates.Symbol) string {
	for _, s := range syms {
		if strings.HasPrefix(strings.TrimSpace(s.DocComment), "Package ") {
			return strings.TrimSpace(s.DocComment)
		}
	}
	return ""
}

// capLines returns s with at most n lines. When the original has more lines,
// a truncation note is appended.
func capLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n// … (%d lines truncated)", len(lines)-n)
}

// oneLineSummary returns the first sentence of a doc comment, or the full
// comment if it has no sentence boundary.
func oneLineSummary(doc string) string {
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return ""
	}
	if idx := strings.IndexByte(doc, '.'); idx >= 0 && idx < 120 {
		return doc[:idx+1]
	}
	if len(doc) > 120 {
		return doc[:120] + "…"
	}
	return doc
}

// renderLLMOutput parses the LLM markdown output and wraps each H2 section
// in a stable [ast.Block]. This is a best-effort parser: each H2 heading
// becomes a heading block, and the prose following it becomes paragraph blocks.
// Code fences become code blocks.
func renderLLMOutput(pageID, pkg string, llmOut string, now time.Time) []ast.Block {
	// Always prepend an H1 title block.
	var blocks []ast.Block
	titleID := ast.GenerateBlockID(pageID, "", ast.BlockKindHeading, 0)
	blocks = append(blocks, ast.Block{
		ID:   titleID,
		Kind: ast.BlockKindHeading,
		Content: ast.BlockContent{Heading: &ast.HeadingContent{
			Level: 1,
			Text:  "Architecture: " + pkg,
		}},
		Owner:      ast.OwnerGenerated,
		LastChange: ast.BlockChange{Timestamp: now, Source: "sourcebridge"},
	})

	lines := strings.Split(llmOut, "\n")
	headingCounts := make(map[string]int) // track ordinals per heading path
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
			title := strings.TrimPrefix(line, "## ")
			cur = &section{heading: strings.TrimSpace(title)}
		} else if cur != nil {
			cur.lines = append(cur.lines, line)
		}
	}
	if cur != nil {
		sections = append(sections, *cur)
	}

	for _, sec := range sections {
		// Heading block.
		hOrdinal := headingCounts[sec.heading]
		headingCounts[sec.heading]++
		hID := ast.GenerateBlockID(pageID, sec.heading, ast.BlockKindHeading, hOrdinal)
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

		// Parse the section body into paragraph and code blocks.
		bodyBlocks := parseBodyBlocks(pageID, sec.heading, sec.lines, now)
		blocks = append(blocks, bodyBlocks...)
	}

	return blocks
}

// RebuildRelatedPagesXHTML is the exported entry point for the Phase 3 fix-up
// pass. It calls buildRelatedPagesXHTML with the caller-supplied resolver and
// returns the fresh XHTML alongside the stub target IDs (nil when resolver
// never stubs, e.g. NullLinkResolver{}).
//
// This function exists so the orchestrator's fixup.go can call it without
// importing the unexported buildRelatedPagesXHTML. Pass orchestrator.NullLinkResolver{}
// to clear all stubs (fix-up path).
func RebuildRelatedPagesXHTML(
	repoID, sourcePageID string,
	callers, callees []string,
	relatedByLabel map[string]string,
	resolver templates.LinkResolver,
) (xhtml string, stubTargetIDs []string) {
	return buildRelatedPagesXHTML(repoID, sourcePageID, callers, callees, relatedByLabel, resolver)
}

// buildRelatedPagesXHTML generates the XHTML body for the "Related pages" block.
// Each caller/callee is a cluster label; the corresponding architecture page
// title is derived via HumanizePageID so the Confluence ac:link resolves.
//
// When resolver is non-nil and resolver.ShouldStub(sourcePageID, targetPageID)
// returns true, the link is wrapped in a Confluence info macro so readers know
// the target is still being generated. stubTargetIDs collects the page IDs that
// were rendered as stubs so the caller can persist them in lw_page_publish_status.
func buildRelatedPagesXHTML(
	repoID, sourcePageID string,
	callers, callees []string,
	relatedByLabel map[string]string,
	resolver templates.LinkResolver,
) (xhtml string, stubTargetIDs []string) {
	var sb strings.Builder
	sb.WriteString("<h2>Related pages</h2>\n")

	buildList := func(heading string, labels []string) {
		if len(labels) == 0 {
			return
		}
		sb.WriteString("<p><strong>")
		sb.WriteString(heading)
		sb.WriteString("</strong></p>\n<ul>\n")
		for _, label := range labels {
			var pid string
			if relatedByLabel != nil {
				if resolved, ok := relatedByLabel[label]; ok {
					pid = resolved
				}
			}
			if pid == "" {
				pid = pageIDFor(repoID, label)
			}
			title := markdown.HumanizePageID(pid)

			if resolver != nil && resolver.ShouldStub(sourcePageID, pid) {
				stubTargetIDs = append(stubTargetIDs, pid)
				sb.WriteString("  <li>")
				sb.WriteString(buildStubMacroXHTML(label, title))
				sb.WriteString("</li>\n")
			} else {
				sb.WriteString(`  <li><ac:link><ri:page ri:content-title="`)
				sb.WriteString(xmlEscapeSimple(title))
				sb.WriteString(`"/><ac:link-body>`)
				sb.WriteString(xmlEscapeSimple(label))
				sb.WriteString(`</ac:link-body></ac:link></li>`)
				sb.WriteString("\n")
			}
		}
		sb.WriteString("</ul>\n")
	}

	buildList("Used by:", callers)
	buildList("Dependencies:", callees)
	return sb.String(), stubTargetIDs
}

// buildStubMacroXHTML returns the Confluence info-macro XHTML for a single
// stub link, appearing inline in the list item.
func buildStubMacroXHTML(label, title string) string {
	var sb strings.Builder
	sb.WriteString(`<ac:structured-macro ac:name="info">`)
	sb.WriteString(`<ac:rich-text-body><p>📝 Generating — <strong>`)
	sb.WriteString(xmlEscapeSimple(label))
	sb.WriteString(`</strong> (`)
	sb.WriteString(xmlEscapeSimple(title))
	sb.WriteString(`) will link here once ready.</p></ac:rich-text-body>`)
	sb.WriteString(`</ac:structured-macro>`)
	return sb.String()
}

// xmlEscapeSimple escapes the five XML special characters.
// Duplicating this small helper avoids importing the markdown package's
// unexported xmlEscape while keeping the dependency graph clean.
func xmlEscapeSimple(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

// parseBodyBlocks converts lines within a section into typed [ast.Block] values.
// Code fences become [ast.BlockKindCode]; prose accumulates into [ast.BlockKindParagraph].
func parseBodyBlocks(pageID, headingPath string, lines []string, now time.Time) []ast.Block {
	var blocks []ast.Block
	paraOrdinal := 0
	codeOrdinal := 0

	inCode := false
	codeLang := ""
	var codeLines []string
	var paraLines []string

	flushPara := func() {
		text := strings.TrimSpace(strings.Join(paraLines, "\n"))
		if text == "" {
			paraLines = nil
			return
		}
		pID := ast.GenerateBlockID(pageID, headingPath, ast.BlockKindParagraph, paraOrdinal)
		paraOrdinal++
		blocks = append(blocks, ast.Block{
			ID:   pID,
			Kind: ast.BlockKindParagraph,
			Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
				Markdown: text,
			}},
			Owner:      ast.OwnerGenerated,
			LastChange: ast.BlockChange{Timestamp: now, Source: "sourcebridge"},
		})
		paraLines = nil
	}

	flushCode := func() {
		body := strings.Join(codeLines, "\n")
		cID := ast.GenerateBlockID(pageID, headingPath, ast.BlockKindCode, codeOrdinal)
		codeOrdinal++
		blocks = append(blocks, ast.Block{
			ID:   cID,
			Kind: ast.BlockKindCode,
			Content: ast.BlockContent{Code: &ast.CodeContent{
				Language: codeLang,
				Body:     body,
			}},
			Owner:      ast.OwnerGenerated,
			LastChange: ast.BlockChange{Timestamp: now, Source: "sourcebridge"},
		})
		codeLines = nil
		codeLang = ""
	}

	for _, line := range lines {
		stripped := strings.TrimLeft(line, " \t")
		if !inCode {
			if strings.HasPrefix(stripped, "```") {
				flushPara()
				inCode = true
				codeLang = strings.TrimPrefix(stripped, "```")
				codeLines = nil
			} else {
				paraLines = append(paraLines, line)
			}
		} else {
			if strings.HasPrefix(stripped, "```") {
				inCode = false
				flushCode()
			} else {
				codeLines = append(codeLines, line)
			}
		}
	}

	if inCode {
		flushCode() // unterminated fence: emit what we have
	}
	flushPara()

	return blocks
}
