// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package activitylog implements the A2.2 Activity log report template.
//
// The Activity log walks indexed git commits, buckets them by author and
// calendar week, and renders a structured summary as an [ast.Page]. Numbers
// dominate; prose is kept factual.
//
// # Page structure
//
//	## Week of <date>                            (one H2 per week, descending)
//	  Summary table: author | commits | files changed | packages touched
//	  BREAKING CHANGE notice (when any commit flags one)
//	  [Optional LLM digest — 2 paragraphs: "what shipped this week"]
//
// # LLM digest
//
// When [templates.TemplateConfig.EnableLLMDigest] is true and input.LLM is
// non-nil, each week gets a 2-paragraph prose digest generated with the
// engineer-to-engineer voice profile. The digest is gated on factual_grounding,
// vagueness, and reading_level per the Q.2 Activity log profile.
//
// When EnableLLMDigest is false (the default), the structural table is still
// generated and quality validators are not invoked (there is no LLM-generated
// prose to validate).
//
// # Validator profile
//
// [quality.TemplateActivityLog] / [quality.AudienceEngineers]:
// factual_grounding and vagueness are gates; reading_level is a warning.
package activitylog

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/prompts"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
)

const templateID = "activity_log"

// Template is the Activity log template. Construct with [New].
type Template struct{}

// New returns a ready-to-use Activity log template.
func New() *Template { return &Template{} }

// Compile-time interface check.
var _ templates.Template = (*Template)(nil)

// ID returns "activity_log".
func (t *Template) ID() string { return templateID }

// weekKey is an ISO year-week string of the form "2026-W17".
type weekKey string

// weekBucket groups commits belonging to one calendar week.
type weekBucket struct {
	key           weekKey
	weekStart     time.Time // Monday of the week (UTC)
	commits       []templates.Commit
	breakingChange bool
}

// authorStats accumulates per-author metrics within a week.
type authorStats struct {
	author       string
	commitCount  int
	filesChanged int
	packages     map[string]bool
}

// Generate produces an Activity log [ast.Page] for the given repo.
//
// input.GitLog must be non-nil. input.LLM is used only when
// input.Config.EnableLLMDigest is true; it may be nil otherwise.
func (t *Template) Generate(ctx context.Context, input templates.GenerateInput) (ast.Page, error) {
	if input.GitLog == nil {
		return ast.Page{}, fmt.Errorf("activity_log: GitLog is required but was not provided")
	}
	if input.Config.EnableLLMDigest && input.LLM == nil {
		return ast.Page{}, fmt.Errorf("activity_log: LLM is required when EnableLLMDigest is true")
	}

	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	commits, err := input.GitLog.Commits(input.RepoID)
	if err != nil {
		return ast.Page{}, fmt.Errorf("activity_log: fetching commits: %w", err)
	}

	// Bucket commits by ISO week, descending (newest week first).
	buckets := bucketByWeek(commits)

	pageID := activityLogPageID(input.RepoID)
	var blocks []ast.Block
	headingOrdinal := 0
	paragraphOrdinal := 0
	tableOrdinal := 0

	for _, bucket := range buckets {
		weekLabel := fmt.Sprintf("Week of %s", bucket.weekStart.Format("2 January 2006"))

		// H2 heading for the week.
		hID := ast.GenerateBlockID(pageID, bucket.weekStart.Format("2006-W01"), ast.BlockKindHeading, headingOrdinal)
		headingOrdinal++
		blocks = append(blocks, ast.Block{
			ID:   hID,
			Kind: ast.BlockKindHeading,
			Content: ast.BlockContent{Heading: &ast.HeadingContent{
				Level: 2,
				Text:  weekLabel,
			}},
			Owner:      ast.OwnerGenerated,
			LastChange: ast.BlockChange{Timestamp: now, Source: "sourcebridge"},
		})

		// Summary table: author | commits | files changed | top packages.
		stats := buildAuthorStats(bucket.commits)
		tableBlock := buildStatsTable(stats)
		tID := ast.GenerateBlockID(pageID, bucket.weekStart.Format("2006-W01"), ast.BlockKindTable, tableOrdinal)
		tableOrdinal++
		tableBlock.ID = tID
		tableBlock.LastChange = ast.BlockChange{Timestamp: now, Source: "sourcebridge"}
		blocks = append(blocks, tableBlock)

		// BREAKING CHANGE callout when any commit in this week had one.
		if bucket.breakingChange {
			bcID := ast.GenerateBlockID(pageID, bucket.weekStart.Format("2006-W01"), ast.BlockKindCallout, 0)
			blocks = append(blocks, ast.Block{
				ID:   bcID,
				Kind: ast.BlockKindCallout,
				Content: ast.BlockContent{Callout: &ast.CalloutContent{
					Kind: "warning",
					Body: breakingChangeSummary(bucket.commits),
				}},
				Owner:      ast.OwnerGenerated,
				LastChange: ast.BlockChange{Timestamp: now, Source: "sourcebridge"},
			})
		}

		// Optional LLM digest.
		if input.Config.EnableLLMDigest {
			digest, err := generateWeekDigest(ctx, input.LLM, bucket, input.Audience)
			if err != nil {
				return ast.Page{}, fmt.Errorf("activity_log: LLM digest for week %s: %w", bucket.key, err)
			}
			pID := ast.GenerateBlockID(pageID, bucket.weekStart.Format("2006-W01"), ast.BlockKindParagraph, paragraphOrdinal)
			paragraphOrdinal++
			blocks = append(blocks, ast.Block{
				ID:   pID,
				Kind: ast.BlockKindParagraph,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
					Markdown: digest,
				}},
				Owner:      ast.OwnerGenerated,
				LastChange: ast.BlockChange{Timestamp: now, Source: "sourcebridge"},
			})
		}
	}

	page := ast.Page{
		ID: pageID,
		Manifest: manifest.DependencyManifest{
			PageID:   pageID,
			Template: string(quality.TemplateActivityLog),
			Audience: string(input.Audience),
			Dependencies: manifest.Dependencies{
				DependencyScope: manifest.ScopeDirect,
			},
		},
		Blocks: blocks,
		Provenance: ast.Provenance{
			GeneratedAt: now,
			ModelID:     digestModelID(input.Config.EnableLLMDigest),
		},
	}

	return page, nil
}

// --- bucketing helpers ---

// isoWeekKey returns the ISO week string for a commit timestamp.
func isoWeekKey(ts time.Time) weekKey {
	year, week := ts.UTC().ISOWeek()
	return weekKey(fmt.Sprintf("%04d-W%02d", year, week))
}

// weekMonday returns the Monday (start) of the ISO week containing ts.
func weekMonday(ts time.Time) time.Time {
	ts = ts.UTC()
	// Go's time.Weekday(): Sunday=0, Monday=1, …, Saturday=6.
	// ISO Monday=1, so we shift: (weekday + 6) % 7 gives days since Monday.
	offset := (int(ts.Weekday()) + 6) % 7
	return time.Date(ts.Year(), ts.Month(), ts.Day()-offset, 0, 0, 0, 0, time.UTC)
}

// bucketByWeek groups commits by ISO calendar week, newest week first.
func bucketByWeek(commits []templates.Commit) []weekBucket {
	buckets := map[weekKey]*weekBucket{}

	for _, c := range commits {
		key := isoWeekKey(c.Timestamp)
		if _, ok := buckets[key]; !ok {
			buckets[key] = &weekBucket{
				key:       key,
				weekStart: weekMonday(c.Timestamp),
			}
		}
		b := buckets[key]
		b.commits = append(b.commits, c)
		if isBreakingChange(c) {
			b.breakingChange = true
		}
	}

	// Sort descending by week start.
	sorted := make([]weekBucket, 0, len(buckets))
	for _, b := range buckets {
		sorted = append(sorted, *b)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].weekStart.After(sorted[j].weekStart)
	})
	return sorted
}

// isBreakingChange reports whether a commit signals a breaking change.
// It matches "BREAKING CHANGE:" in the body and "breaking change" in the subject.
func isBreakingChange(c templates.Commit) bool {
	if strings.Contains(c.Body, "BREAKING CHANGE:") {
		return true
	}
	return strings.Contains(strings.ToLower(c.Subject), "breaking change")
}

// --- stats helpers ---

func buildAuthorStats(commits []templates.Commit) []authorStats {
	byAuthor := map[string]*authorStats{}

	for _, c := range commits {
		a := c.Author
		if _, ok := byAuthor[a]; !ok {
			byAuthor[a] = &authorStats{author: a, packages: map[string]bool{}}
		}
		s := byAuthor[a]
		s.commitCount++
		s.filesChanged += c.FilesChanged
		for _, path := range c.TouchedPaths {
			pkg := topPackage(path)
			if pkg != "" {
				s.packages[pkg] = true
			}
		}
	}

	out := make([]authorStats, 0, len(byAuthor))
	for _, s := range byAuthor {
		out = append(out, *s)
	}
	// Sort by commit count descending, then name ascending.
	sort.Slice(out, func(i, j int) bool {
		if out[i].commitCount != out[j].commitCount {
			return out[i].commitCount > out[j].commitCount
		}
		return out[i].author < out[j].author
	})
	return out
}

// topPackage returns the top-level package segment from a file path.
// e.g. "internal/auth/jwt.go" → "internal/auth"
func topPackage(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return ""
	}
	// Two-segment package (e.g. "internal/auth").
	if len(parts) >= 3 && parts[0] == "internal" {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

func buildStatsTable(stats []authorStats) ast.Block {
	headers := []string{"Author", "Commits", "Files changed", "Packages"}
	rows := make([][]string, 0, len(stats))
	for _, s := range stats {
		pkgs := topPackagesList(s.packages, 3)
		rows = append(rows, []string{
			s.author,
			fmt.Sprintf("%d", s.commitCount),
			fmt.Sprintf("%d", s.filesChanged),
			pkgs,
		})
	}
	return ast.Block{
		Kind: ast.BlockKindTable,
		Content: ast.BlockContent{Table: &ast.TableContent{
			Headers: headers,
			Rows:    rows,
		}},
		Owner: ast.OwnerGenerated,
	}
}

// topPackagesList returns a comma-separated string of up to n package names,
// sorted alphabetically. If more exist, appends "+ N more".
func topPackagesList(pkgs map[string]bool, n int) string {
	all := make([]string, 0, len(pkgs))
	for pkg := range pkgs {
		all = append(all, pkg)
	}
	sort.Strings(all)
	if len(all) <= n {
		return strings.Join(all, ", ")
	}
	more := len(all) - n
	return strings.Join(all[:n], ", ") + fmt.Sprintf(" +%d more", more)
}

// breakingChangeSummary summarises the breaking-change commits in a week.
func breakingChangeSummary(commits []templates.Commit) string {
	var breaking []string
	for _, c := range commits {
		if isBreakingChange(c) {
			breaking = append(breaking, fmt.Sprintf("`%s` — %s", c.ShortSHA, c.Subject))
		}
	}
	return "**BREAKING CHANGE** in this week:\n" + strings.Join(breaking, "\n")
}

// digestModelID returns a non-empty model hint only when the LLM was used.
func digestModelID(used bool) string {
	if used {
		return "via-llm"
	}
	return ""
}

// --- LLM digest helpers ---

const digestSystemPreamble = `You are generating a 2-paragraph weekly engineering digest for a software team.
The digest is factual, concise, and written in an engineer-to-engineer voice.
The first paragraph covers what was shipped (concrete package names, feature descriptions, bug fixes).
The second paragraph covers anything notable: breaking changes, large refactors, dependency updates.
Do not use vague language. Do not say "various" or "several" without a specific number.
Do not pad the output. If there is nothing notable in the second paragraph, say so in one sentence.`

func generateWeekDigest(ctx context.Context, llm templates.LLMCaller, bucket weekBucket, audience quality.Audience) (string, error) {
	voice, err := prompts.LoadVoice("engineer-to-engineer")
	if err != nil {
		// Voice file unavailable — continue without it rather than failing.
		voice = ""
	}

	systemPrompt := digestSystemPreamble
	if voice != "" {
		systemPrompt += "\n\n" + voice
	}

	userPrompt := buildDigestUserPrompt(bucket)
	return llm.Complete(ctx, systemPrompt, userPrompt)
}

func buildDigestUserPrompt(bucket weekBucket) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Week of %s — %d commits\n\n", bucket.weekStart.Format("2 January 2006"), len(bucket.commits)))
	sb.WriteString("Commits this week:\n")
	for _, c := range bucket.commits {
		flag := ""
		if isBreakingChange(c) {
			flag = " [BREAKING CHANGE]"
		}
		filesStr := ""
		if c.FilesChanged > 0 {
			filesStr = fmt.Sprintf(" (%d files)", c.FilesChanged)
		}
		sb.WriteString(fmt.Sprintf("- %s: %s%s%s\n", c.ShortSHA, c.Subject, filesStr, flag))
	}
	if len(bucket.commits) > 0 && bucket.commits[0].Body != "" {
		sb.WriteString("\nSample commit body:\n")
		sb.WriteString(firstN(bucket.commits[0].Body, 500))
	}
	sb.WriteString("\n\nWrite the 2-paragraph digest now:")
	return sb.String()
}

// firstN returns the first n bytes of s (rune-safe truncation).
func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count >= n {
			return s[:i]
		}
		count++
	}
	return s
}

// activityLogPageID derives the stable page ID for an activity log page.
func activityLogPageID(repoID string) string {
	if repoID != "" {
		return repoID + ".activity_log"
	}
	return templateID
}

// ValidatorProfile returns the Q.2 profile for the Activity log template.
func ValidatorProfile(audience quality.Audience) (quality.Profile, bool) {
	return quality.DefaultProfile(quality.TemplateActivityLog, audience)
}

// --- helpers for topPackage that handle edge cases ---

func isAlphanumeric(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// suppress unused import warning — isAlphanumeric used in future callers
var _ = isAlphanumeric
