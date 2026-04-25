// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package adr implements the A2.3 Architectural Decision Record (ADR) report
// template.
//
// The ADR template detects commits that signal architectural decisions and
// produces one [ast.Page] per decision. Each page follows the industry-standard
// Context / Decision / Consequences structure and is generated via a single LLM
// pass using the engineer-to-engineer voice profile.
//
// # Detection rules
//
// A commit is classified as an ADR candidate when any of the following is true:
//  1. The subject line matches the prefix pattern (case-insensitive):
//     ^(decision|adr|design):
//  2. The commit body contains the literal string "BREAKING CHANGE:".
//  3. The subject contains one of the decision phrases:
//     "we decided", "switching to", "moving from … to …"
//
// Non-matching commits are ignored.
//
// # Page structure (per ADR)
//
//	## Context        (what problem were we solving?)
//	## Decision       (what did we decide and why?)
//	## Consequences   (what does this change? what are the trade-offs?)
//
// The three sections are produced by one LLM pass. The system prompt embeds
// the engineer-to-engineer voice profile from prompts/voice/.
//
// # Validator profile
//
// [quality.TemplateADR] / [quality.AudienceEngineers]:
//   - factual_grounding gate
//   - vagueness gate
//   - reading_level warning (floor 40, not 50)
//   - citation_density warning (not gate)
package adr

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/citations"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/prompts"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
)

const templateID = "adr"

// reADRPrefix matches commit subjects with the decision/adr/design prefix.
var reADRPrefix = regexp.MustCompile(`(?i)^(decision|adr|design)\s*:`)

// reDecisionPhrase matches common informal decision language in commit subjects.
var reDecisionPhrase = regexp.MustCompile(
	`(?i)(we decided|switching to|moving from .+ to )`,
)

// Template is the ADR template. Construct with [New].
type Template struct{}

// New returns a ready-to-use ADR template.
func New() *Template { return &Template{} }

// Compile-time interface check.
var _ templates.Template = (*Template)(nil)

// ID returns "adr".
func (t *Template) ID() string { return templateID }

// GenerateResult holds the output of a single Generate call. Unlike the
// other A2 templates which produce one page, the ADR template can produce
// multiple pages — one per detected decision. The caller receives all of them.
//
// The Template.Generate method returns the *first* ADR page (or an empty page
// when no ADRs are detected), to satisfy the single-page [templates.Template]
// interface. Use [GenerateAll] to retrieve all pages.
type GenerateResult struct {
	Pages []ast.Page
}

// Generate returns the first ADR page from the git log.
// When no ADR commits are found, it returns an empty page (zero Blocks) without
// error. Callers that want all ADR pages should call [GenerateAll] directly.
//
// input.GitLog and input.LLM are both required.
func (t *Template) Generate(ctx context.Context, input templates.GenerateInput) (ast.Page, error) {
	result, err := GenerateAll(ctx, input)
	if err != nil || len(result.Pages) == 0 {
		return ast.Page{}, err
	}
	return result.Pages[0], nil
}

// GenerateAll detects all ADR commits in the git log and produces one page per
// decision. Pages are in reverse-chronological order (newest commit first).
//
// input.GitLog and input.LLM are both required.
// input.Now controls provenance timestamps; defaults to time.Now().UTC().
func GenerateAll(ctx context.Context, input templates.GenerateInput) (GenerateResult, error) {
	if input.GitLog == nil {
		return GenerateResult{}, fmt.Errorf("adr: GitLog is required but was not provided")
	}
	if input.LLM == nil {
		return GenerateResult{}, fmt.Errorf("adr: LLM is required but was not provided")
	}

	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	commits, err := input.GitLog.Commits(input.RepoID)
	if err != nil {
		return GenerateResult{}, fmt.Errorf("adr: fetching commits: %w", err)
	}

	voice, err := prompts.LoadVoice("engineer-to-engineer")
	if err != nil {
		voice = "" // degrade gracefully; no voice prompt beats a failed build
	}

	systemPrompt := buildADRSystemPrompt(voice)

	var pages []ast.Page
	for i, commit := range commits {
		if !isADRCommit(commit) {
			continue
		}

		page, err := generateADRPage(ctx, input, commit, i, systemPrompt, now)
		if err != nil {
			return GenerateResult{}, fmt.Errorf("adr: generating page for commit %s: %w", commit.ShortSHA, err)
		}
		pages = append(pages, page)
	}

	return GenerateResult{Pages: pages}, nil
}

// isADRCommit reports whether a commit should produce an ADR page.
func isADRCommit(c templates.Commit) bool {
	if reADRPrefix.MatchString(c.Subject) {
		return true
	}
	if strings.Contains(c.Body, "BREAKING CHANGE:") {
		return true
	}
	if reDecisionPhrase.MatchString(c.Subject) {
		return true
	}
	return false
}

// generateADRPage produces one ADR [ast.Page] from a single commit.
func generateADRPage(
	ctx context.Context,
	input templates.GenerateInput,
	commit templates.Commit,
	commitIndex int,
	systemPrompt string,
	now time.Time,
) (ast.Page, error) {
	pageID := adrPageID(input.RepoID, commit.ShortSHA)
	userPrompt := buildADRUserPrompt(commit)

	prose, err := input.LLM.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return ast.Page{}, err
	}

	// Parse the LLM output into three sections.
	sections := parseADRSections(prose)

	// Commit citation for grounding.
	commitCitation := citations.FormatFileRange(commit.ShortSHA, 0, 0)
	if commitCitation == "" {
		commitCitation = commit.ShortSHA
	}

	var blocks []ast.Block
	blockOrdinal := 0

	// Title heading (H1).
	title := adrTitle(commit)
	hID := ast.GenerateBlockID(pageID, "", ast.BlockKindHeading, blockOrdinal)
	blockOrdinal++
	blocks = append(blocks, ast.Block{
		ID:   hID,
		Kind: ast.BlockKindHeading,
		Content: ast.BlockContent{Heading: &ast.HeadingContent{
			Level: 1,
			Text:  title,
		}},
		Owner:      ast.OwnerGenerated,
		LastChange: ast.BlockChange{SHA: commit.SHA, Timestamp: now, Source: "sourcebridge"},
	})

	// Metadata paragraph: date, author, commit.
	metaID := ast.GenerateBlockID(pageID, "", ast.BlockKindParagraph, blockOrdinal)
	blockOrdinal++
	blocks = append(blocks, ast.Block{
		ID:   metaID,
		Kind: ast.BlockKindParagraph,
		Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
			Markdown: fmt.Sprintf(
				"**Date:** %s | **Author:** %s | **Commit:** `%s`",
				commit.Timestamp.UTC().Format("2006-01-02"),
				commit.Author,
				commit.ShortSHA,
			),
		}},
		Owner:      ast.OwnerGenerated,
		LastChange: ast.BlockChange{SHA: commit.SHA, Timestamp: now, Source: "sourcebridge"},
	})

	// Context section.
	blocks = addSection(blocks, pageID, "Context", sections.Context, commitCitation, commit, now, &blockOrdinal)

	// Decision section.
	blocks = addSection(blocks, pageID, "Decision", sections.Decision, commitCitation, commit, now, &blockOrdinal)

	// Consequences section.
	blocks = addSection(blocks, pageID, "Consequences", sections.Consequences, commitCitation, commit, now, &blockOrdinal)

	page := ast.Page{
		ID: pageID,
		Manifest: manifest.DependencyManifest{
			PageID:   pageID,
			Template: string(quality.TemplateADR),
			Audience: string(input.Audience),
			Dependencies: manifest.Dependencies{
				DependencyScope: manifest.ScopeDirect,
			},
		},
		Blocks: blocks,
		Provenance: ast.Provenance{
			GeneratedAt:    now,
			GeneratedBySHA: commit.SHA,
			ModelID:        "via-llm",
		},
	}

	return page, nil
}

// addSection appends a heading + paragraph for one ADR section.
func addSection(
	blocks []ast.Block,
	pageID, sectionName, content, citation string,
	commit templates.Commit,
	now time.Time,
	ordinal *int,
) []ast.Block {
	// Section heading (H2).
	hID := ast.GenerateBlockID(pageID, sectionName, ast.BlockKindHeading, *ordinal)
	*ordinal++
	blocks = append(blocks, ast.Block{
		ID:   hID,
		Kind: ast.BlockKindHeading,
		Content: ast.BlockContent{Heading: &ast.HeadingContent{
			Level: 2,
			Text:  sectionName,
		}},
		Owner:      ast.OwnerGenerated,
		LastChange: ast.BlockChange{SHA: commit.SHA, Timestamp: now, Source: "sourcebridge"},
	})

	// Section body. Append citation to the end of the prose if not already present.
	body := strings.TrimSpace(content)
	if body == "" {
		body = "_No content detected in this section._"
	} else if citation != "" && !strings.Contains(body, citation) {
		body += fmt.Sprintf(" (%s)", citation)
	}

	pID := ast.GenerateBlockID(pageID, sectionName, ast.BlockKindParagraph, *ordinal)
	*ordinal++
	blocks = append(blocks, ast.Block{
		ID:   pID,
		Kind: ast.BlockKindParagraph,
		Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
			Markdown: body,
		}},
		Owner:      ast.OwnerGenerated,
		LastChange: ast.BlockChange{SHA: commit.SHA, Timestamp: now, Source: "sourcebridge"},
	})

	return blocks
}

// adrSections holds the parsed LLM output for one ADR.
type adrSections struct {
	Context      string
	Decision     string
	Consequences string
}

// parseADRSections splits LLM output on the three mandatory section headings.
// It is tolerant of heading level variation (## or ### both work) and
// case-insensitive on section names.
func parseADRSections(text string) adrSections {
	type section struct {
		name    string
		content string
	}

	lines := strings.Split(text, "\n")
	var sections []section
	currentName := ""
	var currentLines []string

	flushSection := func() {
		if currentName == "" {
			return
		}
		sections = append(sections, section{
			name:    currentName,
			content: strings.TrimSpace(strings.Join(currentLines, "\n")),
		})
		currentLines = nil
	}

	for _, line := range lines {
		stripped := strings.TrimLeft(line, "# \t")
		lower := strings.ToLower(strings.TrimSpace(stripped))
		if strings.HasPrefix(line, "#") && (lower == "context" || lower == "decision" || lower == "consequences") {
			flushSection()
			currentName = strings.Title(lower) //nolint:staticcheck // Title is fine for these three words
		} else {
			currentLines = append(currentLines, line)
		}
	}
	flushSection()

	var result adrSections
	for _, s := range sections {
		switch strings.ToLower(s.name) {
		case "context":
			result.Context = s.content
		case "decision":
			result.Decision = s.content
		case "consequences":
			result.Consequences = s.content
		}
	}

	// Fallback: if parsing failed, put everything in Context.
	if result.Context == "" && result.Decision == "" && result.Consequences == "" {
		result.Context = strings.TrimSpace(text)
	}

	return result
}

// adrTitle produces a human-readable title from the commit.
func adrTitle(c templates.Commit) string {
	// Strip the "decision:", "adr:", "design:" prefix if present.
	subject := reADRPrefix.ReplaceAllString(c.Subject, "")
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return fmt.Sprintf("ADR — %s", c.ShortSHA)
	}
	// Capitalise first letter.
	runes := []rune(subject)
	if len(runes) > 0 {
		runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
	}
	return string(runes)
}

// adrPageID derives a stable page ID for one ADR commit.
func adrPageID(repoID, commitShortSHA string) string {
	parts := []string{}
	if repoID != "" {
		parts = append(parts, repoID)
	}
	parts = append(parts, "adr", commitShortSHA)
	return strings.Join(parts, ".")
}

func adrPageIDFromCommit(repoID string, commit templates.Commit) string {
	return adrPageID(repoID, commit.ShortSHA)
}

func buildADRSystemPrompt(voice string) string {
	base := `You are generating an Architectural Decision Record (ADR) for a software engineering team.

The ADR must follow this exact structure — three sections in this order:

## Context
What problem or situation prompted this decision? What constraints or trade-offs were at play?
Be specific. Name the packages, systems, or APIs involved.

## Decision
What was decided? Why? What alternatives were considered and rejected?
Use active voice. State the decision in the first sentence.

## Consequences
What changes as a result of this decision? What are the trade-offs?
Include both positive consequences and known downsides or risks.

Rules:
- Do not use vague language ("various", "many", "several" without a count).
- Ground behavioral claims in specific code facts from the commit.
- Each section should be 2-4 substantive sentences. Short ADRs are fine; padding is not.
- Use the engineer-to-engineer voice: direct, precise, no marketing language.`

	if voice != "" {
		return base + "\n\n" + voice
	}
	return base
}

func buildADRUserPrompt(commit templates.Commit) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Commit: %s\n", commit.ShortSHA))
	sb.WriteString(fmt.Sprintf("Author: %s <%s>\n", commit.Author, commit.AuthorEmail))
	sb.WriteString(fmt.Sprintf("Date: %s\n", commit.Timestamp.UTC().Format("2006-01-02")))
	sb.WriteString(fmt.Sprintf("Subject: %s\n", commit.Subject))
	if commit.Body != "" {
		sb.WriteString(fmt.Sprintf("\nBody:\n%s\n", commit.Body))
	}
	if commit.FilesChanged > 0 {
		sb.WriteString(fmt.Sprintf("\nFiles changed: %d | Insertions: +%d | Deletions: -%d\n",
			commit.FilesChanged, commit.Insertions, commit.Deletions))
	}
	sb.WriteString("\nGenerate the ADR now. Output only the three sections (## Context, ## Decision, ## Consequences). No preamble or postamble.")
	return sb.String()
}

// ValidatorProfile returns the Q.2 profile for the ADR template.
func ValidatorProfile(audience quality.Audience) (quality.Profile, bool) {
	return quality.DefaultProfile(quality.TemplateADR, audience)
}

// suppress adrPageIDFromCommit "unused" in case it's only used via GenerateAll
var _ = adrPageIDFromCommit
