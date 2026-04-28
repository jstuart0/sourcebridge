// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package orchestrator implements the living-wiki generation orchestrator for
// Workstream A1.P1.
//
// # Responsibilities
//
// The [Orchestrator] ties together the full A1.P1 cold-start pipeline:
//
//  1. [TaxonomyResolver] derives the set of [PlannedPage] values (one
//     architecture page per top-level package, one API reference page, one
//     system overview, and the glossary) from the repo's symbol graph.
//
//  2. For each planned page, [Orchestrator.Generate] calls the matching
//     template, applies the Q.2 validator profile from [quality.DefaultProfile],
//     and implements the retry policy: one retry with gate violations in the
//     prompt; pages that fail twice are excluded with a log entry.
//
//  3. Successfully generated pages are stored in the [PageStore] as
//     proposed_ast (PR mode, default) or canonical_ast (direct-publish mode).
//
//  4. A [WikiPR] is opened with the rendered markdown for each page.
//
//  5. [Orchestrator.Promote] / [Orchestrator.Discard] handle post-PR-merge
//     and post-PR-rejection state transitions.
//
// # Concurrency
//
// Page generation is parallelised up to [Config.MaxConcurrency] goroutines
// using an errgroup. The overall run is bounded by [Config.TimeBudget].
//
// # Incremental path
//
// The incremental regeneration path (A1.P2) is implemented in incremental.go.
// Use [Orchestrator.GenerateIncremental] with an [IncrementalRequest] to run
// a two-watermark, additive-commit incremental update on an open wiki PR.
package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/sourcebridge/sourcebridge/internal/clustering"
	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/markdown"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates/architecture"
)

// ErrIncrementalNotImplemented is returned by [Orchestrator.GenerateIncremental]
// because incremental generation is deferred to A1.P2.
var ErrIncrementalNotImplemented = errors.New("orchestrator: incremental generation is not implemented (A1.P2)")

// ErrTimeBudgetExceeded is returned when the overall generation run exceeds
// [Config.TimeBudget].
var ErrTimeBudgetExceeded = errors.New("orchestrator: time budget exceeded")

// TemplateRegistry maps template IDs to [templates.Template] implementations.
// The orchestrator looks up templates by the ID stored in each [PlannedPage].
type TemplateRegistry interface {
	// Lookup returns the template for the given ID, or (nil, false) when not found.
	Lookup(id string) (templates.Template, bool)
}

// PageStore is the repository for canonical and proposed wiki page ASTs.
// Implementations must be safe for concurrent use.
type PageStore interface {
	// GetCanonical returns the canonical page for the given repo and page ID.
	// Returns (Page{}, false, nil) when no canonical page exists yet.
	GetCanonical(ctx context.Context, repoID, pageID string) (ast.Page, bool, error)

	// SetCanonical stores a page as the canonical AST for the given repo.
	SetCanonical(ctx context.Context, repoID string, page ast.Page) error

	// DeleteCanonical removes a canonical page from the store. Used when a page
	// is renamed or migrated and the old ID must be retired.
	// A no-op when the page does not exist.
	DeleteCanonical(ctx context.Context, repoID, pageID string) error

	// GetProposed returns the proposed page within the given PR.
	// prID is the PR identifier returned by WikiPR.ID().
	// Returns (Page{}, false, nil) when no proposed page exists for this PR.
	GetProposed(ctx context.Context, repoID, prID, pageID string) (ast.Page, bool, error)

	// SetProposed stores a page as proposed AST for the given PR.
	SetProposed(ctx context.Context, repoID, prID string, page ast.Page) error

	// ListProposed returns all proposed pages for the given PR.
	ListProposed(ctx context.Context, repoID, prID string) ([]ast.Page, error)

	// DeleteProposed discards all proposed pages for a PR (called on rejection).
	DeleteProposed(ctx context.Context, repoID, prID string) error

	// PromoteProposed copies all proposed pages for a PR to canonical storage.
	// Each copied page has OwnerHumanEditedOnPRBranch translated to OwnerHumanEdited
	// via ast.Promote.
	PromoteProposed(ctx context.Context, repoID, prID string) error
}

// WikiPR is the interface for interacting with the wiki PR in the source
// repository. For P1, the concrete implementations are [MemoryWikiPR] (tests)
// and a future GitHub/GitLab integration. The interface is deliberately narrow
// so the real implementation can be dropped in without changing the orchestrator.
type WikiPR interface {
	// ID returns the stable identifier for this PR (e.g. a GitHub PR number as string).
	ID() string

	// Open creates the PR with the given title and body. pages is the set of
	// rendered markdown files to commit: map from wiki/<page-id>.md → content.
	Open(ctx context.Context, branch, title, body string, files map[string][]byte) error

	// Merged reports whether this PR has been merged into the base branch.
	Merged(ctx context.Context) (bool, error)

	// Closed reports whether this PR has been closed without merging.
	Closed(ctx context.Context) (bool, error)
}

// RepoWriter writes rendered wiki files to the source repository.
type RepoWriter interface {
	// WriteFiles writes the given files to the repository under wiki/.
	// path keys are relative to the repo root (e.g. "wiki/arch.auth.md").
	WriteFiles(ctx context.Context, files map[string][]byte) error
}

// PlannedPage is one page the orchestrator intends to generate.
type PlannedPage struct {
	// ID is the stable page ID (e.g. "arch.auth", "api_reference", "system_overview").
	ID string

	// TemplateID is the ID of the template to use (e.g. "architecture", "api_reference").
	TemplateID string

	// Audience is the target audience for this page.
	Audience quality.Audience

	// Input is the pre-populated GenerateInput for this page.
	// The orchestrator passes it to templates.Template.Generate unchanged.
	Input templates.GenerateInput

	// PackageInfo is non-nil for architecture pages; it is passed to
	// architecture.Template.GeneratePackagePage in preference to Generate.
	PackageInfo *ArchitecturePackageInfo
}

// ArchitecturePackageInfo carries the per-package inputs needed by the
// architecture template. It mirrors architecture.PackageInfo without creating
// a circular import.
type ArchitecturePackageInfo struct {
	Package string
	Callers []string
	Callees []string

	// MemberPackages is the set of package import paths that belong to this
	// cluster. Used by the knowledge-artifact resolution step to match
	// module-scoped artifacts whose scopePath aligns with any member package.
	// Empty in the package-path fallback mode (pre-cluster behaviour).
	MemberPackages []string

	// KnowledgeArtifacts is the set of pre-computed knowledge artifacts that
	// cover any of this cluster's member packages, in preference order
	// (deepest + freshest first). The architecture template prefers artifact
	// summaries as ground truth when fresh, and falls back to raw-symbol
	// generation when this is empty or every artifact is stale.
	KnowledgeArtifacts []KnowledgeArtifactSummary
}

// KnowledgeArtifactSummary is the narrow living-wiki view of a knowledge
// artifact. We mirror just the fields the architecture template needs so the
// orchestrator package doesn't take on a heavy dep on the knowledge package.
type KnowledgeArtifactSummary struct {
	Type        string // e.g. "cliff_notes", "architecture_diagram"
	Audience    string
	Depth       string // "summary", "medium", or "deep"
	ScopePath   string // module / package path the artifact covers
	Sections    []KnowledgeSection
	RevisionFp  string    // matches understanding revisionFp
	GeneratedAt time.Time
}

// KnowledgeSection is a single titled section from a knowledge artifact,
// carried through to the architecture template for use as authoritative context.
type KnowledgeSection struct {
	Title    string
	Content  string // markdown body
	Summary  string // 1-2 line synopsis if available
	Evidence []KnowledgeEvidence
}

// KnowledgeEvidence is a traceable file/line reference from a section, used
// to surface verified source citations in the architecture page prompt.
type KnowledgeEvidence struct {
	FilePath  string
	LineStart int
	LineEnd   int
	Rationale string
}

// GraphMetricsProvider supplies page-reference and graph-relation counts for
// a given page ID. These counts are used by the architectural_relevance
// validator. Implementations query the knowledge graph store; tests can use
// [ConstGraphMetrics].
type GraphMetricsProvider interface {
	// PageReferenceCount returns the number of other pages that reference
	// the given page's subject. Used by the architectural_relevance validator.
	PageReferenceCount(repoID, pageID string) int

	// GraphRelationCount returns the number of graph relations the page's
	// subject participates in.
	GraphRelationCount(repoID, pageID string) int
}

// ConstGraphMetrics is a [GraphMetricsProvider] that always returns fixed values.
// Useful in tests to satisfy the architectural_relevance gate without a real graph.
type ConstGraphMetrics struct {
	PageRefs     int
	GraphRelations int
}

func (c ConstGraphMetrics) PageReferenceCount(_, _ string) int  { return c.PageRefs }
func (c ConstGraphMetrics) GraphRelationCount(_, _ string) int  { return c.GraphRelations }

// Config controls the orchestrator's behaviour.
type Config struct {
	// RepoID is the opaque repository identifier.
	RepoID string

	// TimeBudget is the maximum wall-clock time for a complete generation run.
	// When zero, defaults to 5 minutes.
	TimeBudget time.Duration

	// MaxConcurrency is the maximum number of page-generation goroutines.
	// When zero, defaults to 5.
	MaxConcurrency int

	// DirectPublish skips the PR flow and writes pages directly to canonical_ast.
	// Default is false (PR mode). Set to true for teams that do not want a
	// review gate on the initial wiki shape.
	DirectPublish bool

	// PRBranch is the git branch name for the wiki PR.
	// When empty, defaults to "sourcebridge/wiki-initial".
	PRBranch string

	// PRTitle is the title for the wiki PR.
	// When empty, defaults to "wiki: initial generation (sourcebridge)".
	PRTitle string

	// GraphMetrics provides page-reference and relation counts for the
	// architectural_relevance validator. When nil, both counts default to 0,
	// which will cause system_overview pages to fail the architectural_relevance
	// gate unless the validator profile is overridden.
	GraphMetrics GraphMetricsProvider
}

// OnPageDoneFunc is called after each page completes generation (success,
// exclusion, or partial warning). The callback is invoked from the page's
// generation goroutine; implementations must be concurrency-safe.
//
// pageID is the planned page's ID (e.g. "arch.auth").
// excluded is true when the page failed quality gates twice and was excluded.
// warning is non-empty when the page was included but a non-fatal validation
// issue occurred. It is empty for clean successes and full exclusions.
type OnPageDoneFunc func(pageID string, excluded bool, warning string)

// GenerateRequest carries the inputs for a single cold-start generation run.
type GenerateRequest struct {
	// Config overrides the orchestrator-level config for this run.
	// Zero fields inherit the orchestrator's Config.
	Config Config

	// Pages is the list of planned pages to generate.
	// Callers typically build this via [TaxonomyResolver.Resolve].
	Pages []PlannedPage

	// PR is the WikiPR implementation to use for this run.
	// Must be non-nil when Config.DirectPublish is false.
	PR WikiPR

	// Writer is the RepoWriter to use when Config.DirectPublish is true.
	// May be nil when PR mode is active.
	Writer RepoWriter

	// OnPageDone is called after each page is processed (success, excluded, or
	// warning). It is safe to leave nil. Used by the cold-start job goroutine to
	// update the llm.Job progress record as pages complete. The total page count
	// is known before generation starts (len(Pages)), enabling a determinate
	// progress bar from the first callback.
	OnPageDone OnPageDoneFunc
}

// GenerateResult summarises the outcome of a generation run.
type GenerateResult struct {
	// Generated is the list of pages that were successfully generated and stored.
	Generated []ast.Page

	// Excluded is the list of page IDs that were excluded after two gate failures.
	Excluded []ExcludedPage

	// PRID is the PR identifier (empty in direct-publish mode).
	PRID string

	// Duration is how long the generation took.
	Duration time.Duration
}

// ExcludedPage records a page that failed quality gates twice.
type ExcludedPage struct {
	// PageID is the page that was excluded.
	PageID string

	// TemplateID is the template that was used.
	TemplateID string

	// FirstResult is the first-attempt validation result.
	FirstResult quality.ValidationResult

	// SecondResult is the second-attempt (retry) validation result.
	SecondResult quality.ValidationResult
}

// Orchestrator is the living-wiki generation orchestrator.
// Construct with [New].
type Orchestrator struct {
	cfg      Config
	registry TemplateRegistry
	store    PageStore
	debounce *repoDebounceTracker // per-repo 60s debounce for incremental regen
}

// New creates a new Orchestrator. registry and store must be non-nil.
func New(cfg Config, registry TemplateRegistry, store PageStore) *Orchestrator {
	if cfg.TimeBudget <= 0 {
		cfg.TimeBudget = 5 * time.Minute
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 5
	}
	if cfg.PRBranch == "" {
		cfg.PRBranch = "sourcebridge/wiki-initial"
	}
	if cfg.PRTitle == "" {
		cfg.PRTitle = "wiki: initial generation (sourcebridge)"
	}
	return &Orchestrator{cfg: cfg, registry: registry, store: store, debounce: newRepoDebounceTracker()}
}

// Generate runs the cold-start generation pipeline for all planned pages.
//
// The pipeline for each page:
//  1. Call the template's Generate (or GeneratePackagePage for architecture).
//  2. Run quality.Run with the page's profile.
//  3. If gates fail on attempt 1, retry with the rejection reason injected.
//  4. If gates fail on attempt 2, exclude the page and log it.
//  5. On success, render markdown and store as proposed_ast (PR mode) or
//     canonical_ast (direct-publish mode).
//
// After all pages are processed, open the PR (PR mode) or call Writer.WriteFiles
// (direct-publish mode).
func (o *Orchestrator) Generate(ctx context.Context, req GenerateRequest) (GenerateResult, error) {
	cfg := mergeConfig(o.cfg, req.Config)

	start := time.Now()
	deadline := start.Add(cfg.TimeBudget)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	outcomes := make([]pageOutcome, len(req.Pages))
	var outcomesMu sync.Mutex

	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(cfg.MaxConcurrency)

	for idx, planned := range req.Pages {
		idx, planned := idx, planned // capture for goroutine
		eg.Go(func() error {
			outcome, err := o.generateOnePage(egCtx, cfg, planned)
			if err != nil {
				return fmt.Errorf("page %q: %w", planned.ID, err)
			}
			outcomesMu.Lock()
			outcomes[idx] = outcome
			outcomesMu.Unlock()
			// Notify the caller (e.g. cold-start job progress reporter) after
			// each page completes. The callback is concurrency-safe by contract.
			if req.OnPageDone != nil {
				switch {
				case outcome.excluded != nil:
					req.OnPageDone(planned.ID, true, "")
				default:
					req.OnPageDone(planned.ID, false, "")
				}
			}
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return GenerateResult{}, ErrTimeBudgetExceeded
		}
		return GenerateResult{}, err
	}

	// Separate successes from exclusions; collect rendered files.
	var generated []ast.Page
	var excluded []ExcludedPage
	files := make(map[string][]byte)

	for _, outcome := range outcomes {
		if outcome.excluded != nil {
			excluded = append(excluded, *outcome.excluded)
			continue
		}
		if outcome.page.ID == "" {
			continue // zero-value page means nothing was planned (shouldn't happen)
		}
		generated = append(generated, outcome.page)
		files[wikiFilePath(outcome.page.ID)] = outcome.rendered
	}

	// Store and publish.
	var prID string
	if cfg.DirectPublish {
		// Direct-publish: store as canonical and write files.
		for _, page := range generated {
			if err := o.store.SetCanonical(ctx, cfg.RepoID, page); err != nil {
				return GenerateResult{}, fmt.Errorf("orchestrator: storing canonical page %q: %w", page.ID, err)
			}
		}
		if req.Writer != nil && len(files) > 0 {
			if err := req.Writer.WriteFiles(ctx, files); err != nil {
				return GenerateResult{}, fmt.Errorf("orchestrator: writing files: %w", err)
			}
		}
	} else {
		// PR mode: store as proposed and open the PR.
		if req.PR == nil {
			return GenerateResult{}, fmt.Errorf("orchestrator: WikiPR must be non-nil in PR mode")
		}
		prID = req.PR.ID()

		for _, page := range generated {
			if err := o.store.SetProposed(ctx, cfg.RepoID, prID, page); err != nil {
				return GenerateResult{}, fmt.Errorf("orchestrator: storing proposed page %q: %w", page.ID, err)
			}
		}

		body := buildPRBody(generated, excluded)
		if err := req.PR.Open(ctx, cfg.PRBranch, cfg.PRTitle, body, files); err != nil {
			return GenerateResult{}, fmt.Errorf("orchestrator: opening PR: %w", err)
		}
	}

	return GenerateResult{
		Generated: generated,
		Excluded:  excluded,
		PRID:      prID,
		Duration:  time.Since(start),
	}, nil
}

// Promote promotes all proposed pages for the given PR to canonical.
// Call this when the wiki PR merges (step 5 of the cold-start state flow).
func (o *Orchestrator) Promote(ctx context.Context, repoID, prID string) error {
	return o.store.PromoteProposed(ctx, repoID, prID)
}

// Discard discards all proposed pages for the given PR.
// Call this when the wiki PR is rejected/closed without merge (step 6).
func (o *Orchestrator) Discard(ctx context.Context, repoID, prID string) error {
	return o.store.DeleteProposed(ctx, repoID, prID)
}

// ErrIncrementalNotImplemented is kept for API compatibility but is no longer
// returned by GenerateIncremental, which is now fully implemented in A1.P2.
// Callers that tested for this error should update to use the new
// [IncrementalResult] return type.
var _ = ErrIncrementalNotImplemented // prevent "declared and not used" if no callers remain

// pageOutcome is the internal result of generating one page.
type pageOutcome struct {
	page     ast.Page
	excluded *ExcludedPage
	rendered []byte
}

// graphMetricsForPage returns the validator base config populated with graph
// metrics for the given page, if a GraphMetrics provider is configured.
func graphMetricsForPage(cfg Config, pageID string) quality.ValidatorConfig {
	if cfg.GraphMetrics == nil {
		return quality.ValidatorConfig{}
	}
	return quality.ValidatorConfig{
		PageReferenceCount: cfg.GraphMetrics.PageReferenceCount(cfg.RepoID, pageID),
		GraphRelationCount: cfg.GraphMetrics.GraphRelationCount(cfg.RepoID, pageID),
	}
}

// generateOnePage runs the template + validator loop for a single planned page.
func (o *Orchestrator) generateOnePage(ctx context.Context, cfg Config, planned PlannedPage) (pageOutcome, error) {
	tmpl, ok := o.registry.Lookup(planned.TemplateID)
	if !ok {
		return pageOutcome{}, fmt.Errorf("template %q not found in registry", planned.TemplateID)
	}

	profile, hasProfile := quality.DefaultProfile(
		quality.Template(planned.TemplateID),
		planned.Audience,
	)

	var (
		page         ast.Page
		firstResult  quality.ValidationResult
		secondResult quality.ValidationResult
	)

	for attempt := 1; attempt <= 2; attempt++ {
		var err error

		// On retry, inject the rejection reason into the prompt.
		input := planned.Input
		if attempt == 2 && hasProfile {
			input = injectRetryHint(input, firstResult.RetryPromptFragment())
		}

		page, err = callTemplate(ctx, tmpl, input, planned.PackageInfo)
		if err != nil {
			return pageOutcome{}, fmt.Errorf("template generate attempt %d: %w", attempt, err)
		}

		if !hasProfile {
			// No quality profile for this combination — ship without validation.
			break
		}

		// Build validation input from the page's prose content — not from the
		// rendered markdown, which includes block-ID HTML markers that confuse
		// the validators. extractProseMarkdown returns the page's text blocks
		// joined as clean markdown.
		proseMarkdown := extractProseMarkdown(page)
		mdInput := quality.NewMarkdownInput(proseMarkdown)
		baseConfig := graphMetricsForPage(cfg, planned.ID)
		result := quality.Run(profile, mdInput, baseConfig, attempt)

		if attempt == 1 {
			firstResult = result
		} else {
			secondResult = result
		}

		if result.Decision == quality.RetryPass {
			break
		}
		if result.Decision == quality.RetryReject {
			// Both attempts failed — exclude the page.
			excl := &ExcludedPage{
				PageID:       planned.ID,
				TemplateID:   planned.TemplateID,
				FirstResult:  firstResult,
				SecondResult: secondResult,
			}
			return pageOutcome{excluded: excl}, nil
		}
		// RetryWithReasons: loop for attempt 2.
	}

	rendered, err := renderPage(page)
	if err != nil {
		return pageOutcome{}, err
	}

	return pageOutcome{page: page, rendered: rendered}, nil
}

// callTemplate dispatches the Generate call, routing architecture pages to
// GeneratePackagePage when PackageInfo is present.
func callTemplate(ctx context.Context, tmpl templates.Template, input templates.GenerateInput, pkg *ArchitecturePackageInfo) (ast.Page, error) {
	if pkg != nil {
		// Architecture template has a per-package entry point.
		if archTmpl, ok := tmpl.(*architecture.Template); ok {
			archPkg := architecture.PackageInfo{
				Package: pkg.Package,
				Callers: pkg.Callers,
				Callees: pkg.Callees,
			}
			// Map orchestrator knowledge summaries into the architecture package's
			// equivalent types. This translation keeps the import direction clean:
			// orchestrator → architecture, never the reverse.
			for _, ks := range pkg.KnowledgeArtifacts {
				archArt := architecture.KnowledgeArtifactSummary{
					Type:        ks.Type,
					Audience:    ks.Audience,
					Depth:       ks.Depth,
					ScopePath:   ks.ScopePath,
					RevisionFp:  ks.RevisionFp,
					GeneratedAt: ks.GeneratedAt,
				}
				for _, sec := range ks.Sections {
					archSec := architecture.KnowledgeSection{
						Title:   sec.Title,
						Content: sec.Content,
						Summary: sec.Summary,
					}
					for _, ev := range sec.Evidence {
						archSec.Evidence = append(archSec.Evidence, architecture.KnowledgeEvidence{
							FilePath:  ev.FilePath,
							LineStart: ev.LineStart,
							LineEnd:   ev.LineEnd,
							Rationale: ev.Rationale,
						})
					}
					archArt.Sections = append(archArt.Sections, archSec)
				}
				archPkg.KnowledgeArtifacts = append(archPkg.KnowledgeArtifacts, archArt)
			}
			return archTmpl.GeneratePackagePage(ctx, input, archPkg)
		}
	}
	return tmpl.Generate(ctx, input)
}

// extractProseMarkdown reconstructs clean markdown from the page's AST blocks,
// without the sourcebridge block-ID HTML comment markers. This is the text
// that validators should inspect — not the sink-rendered markdown which includes
// markers that would be treated as prose by the validators.
func extractProseMarkdown(page ast.Page) string {
	var sb strings.Builder
	for _, blk := range page.Blocks {
		switch blk.Kind {
		case ast.BlockKindHeading:
			if blk.Content.Heading != nil {
				h := strings.Repeat("#", blk.Content.Heading.Level)
				sb.WriteString(h + " " + blk.Content.Heading.Text + "\n\n")
			}
		case ast.BlockKindParagraph:
			if blk.Content.Paragraph != nil {
				sb.WriteString(blk.Content.Paragraph.Markdown + "\n\n")
			}
		case ast.BlockKindCode:
			if blk.Content.Code != nil {
				sb.WriteString("```" + blk.Content.Code.Language + "\n")
				sb.WriteString(blk.Content.Code.Body + "\n")
				sb.WriteString("```\n\n")
			}
		case ast.BlockKindTable:
			if blk.Content.Table != nil {
				// Write headers.
				sb.WriteString("| " + strings.Join(blk.Content.Table.Headers, " | ") + " |\n")
				seps := make([]string, len(blk.Content.Table.Headers))
				for i := range seps {
					seps[i] = "---"
				}
				sb.WriteString("| " + strings.Join(seps, " | ") + " |\n")
				for _, row := range blk.Content.Table.Rows {
					sb.WriteString("| " + strings.Join(row, " | ") + " |\n")
				}
				sb.WriteString("\n")
			}
		case ast.BlockKindCallout:
			if blk.Content.Callout != nil {
				sb.WriteString("> **" + blk.Content.Callout.Kind + ":** " + blk.Content.Callout.Body + "\n\n")
			}
		case ast.BlockKindFreeform:
			if blk.Content.Freeform != nil {
				sb.WriteString(blk.Content.Freeform.Raw + "\n\n")
			}
		}
	}
	return sb.String()
}

// renderPage renders a page to markdown bytes.
func renderPage(page ast.Page) ([]byte, error) {
	var buf bytes.Buffer
	if err := markdown.Write(&buf, page); err != nil {
		return nil, fmt.Errorf("orchestrator: rendering page %q: %w", page.ID, err)
	}
	return buf.Bytes(), nil
}

// wikiFilePath converts a page ID to a repo-relative file path.
// Example: "arch.auth" → "wiki/arch.auth.md"
func wikiFilePath(pageID string) string {
	return "wiki/" + pageID + ".md"
}

// buildPRBody generates the markdown body for the wiki PR.
func buildPRBody(generated []ast.Page, excluded []ExcludedPage) string {
	var sb bytes.Buffer
	fmt.Fprintf(&sb, "## SourceBridge Wiki — Initial Generation\n\n")
	fmt.Fprintf(&sb, "This PR was opened automatically by [SourceBridge](https://sourcebridge.ai).\n\n")
	fmt.Fprintf(&sb, "> Squash this PR on merge to keep wiki history clean.\n\n")
	fmt.Fprintf(&sb, "### Pages generated (%d)\n\n", len(generated))
	for _, p := range generated {
		fmt.Fprintf(&sb, "- `%s` — `%s`\n", p.ID, p.Manifest.Template)
	}
	if len(excluded) > 0 {
		fmt.Fprintf(&sb, "\n### Pages excluded (%d)\n\n", len(excluded))
		fmt.Fprintf(&sb, "The following pages failed quality gates after 2 attempts and were excluded:\n\n")
		for _, e := range excluded {
			fmt.Fprintf(&sb, "- `%s` — see quality report below\n", e.PageID)
		}
		fmt.Fprintf(&sb, "\n")
		for _, e := range excluded {
			fmt.Fprintf(&sb, "#### Quality report for `%s`\n\n", e.PageID)
			fmt.Fprintf(&sb, "**Attempt 1:**\n\n%s\n", e.FirstResult.QualityReportMarkdown())
			fmt.Fprintf(&sb, "**Attempt 2:**\n\n%s\n", e.SecondResult.QualityReportMarkdown())
		}
	}
	return sb.String()
}

// injectRetryHint returns a copy of input with the retry fragment prepended to
// the system prompt equivalent — since GenerateInput has no explicit system/user
// split at this level, we use a conventions-based field. For now we store the
// hint in GenerateInput.Config's future extensibility point by wrapping the LLM.
func injectRetryHint(input templates.GenerateInput, hint string) templates.GenerateInput {
	if hint == "" || input.LLM == nil {
		return input
	}
	input.LLM = &retryHintLLM{inner: input.LLM, hint: hint}
	return input
}

// retryHintLLM wraps an LLMCaller to prepend the retry hint to the user prompt.
type retryHintLLM struct {
	inner templates.LLMCaller
	hint  string
}

func (r *retryHintLLM) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	augmented := r.hint + "\n\n" + userPrompt
	return r.inner.Complete(ctx, systemPrompt, augmented)
}

// mergeConfig merges a run-level Config override into the orchestrator config.
// Non-zero values in override take precedence.
func mergeConfig(base, override Config) Config {
	merged := base
	if override.RepoID != "" {
		merged.RepoID = override.RepoID
	}
	if override.TimeBudget > 0 {
		merged.TimeBudget = override.TimeBudget
	}
	if override.MaxConcurrency > 0 {
		merged.MaxConcurrency = override.MaxConcurrency
	}
	if override.PRBranch != "" {
		merged.PRBranch = override.PRBranch
	}
	if override.PRTitle != "" {
		merged.PRTitle = override.PRTitle
	}
	// DirectPublish: true in either wins.
	merged.DirectPublish = base.DirectPublish || override.DirectPublish
	return merged
}

// PackageDepsProvider supplies pre-computed package-level dependency edges.
// The graph.GraphStore satisfies this interface; a nil implementation is
// safe — the TaxonomyResolver will produce pages without Callers/Callees.
type PackageDepsProvider interface {
	// GetPackageDependencies returns all package-level dependency records for
	// the given repository. Returns an empty slice when none are available.
	GetPackageDependencies(repoID string) []*graph.StoredPackageDependencies
}

// TaxonomyResolver derives the [PlannedPage] list for a repository from its
// symbol graph. It emits:
//   - One architecture page per top-level package (audience: engineer)
//   - One API reference page (audience: engineer)
//   - One system overview page (audience: product, with engineer alt)
//   - One glossary page (audience: engineer)
type TaxonomyResolver struct {
	repoID      string
	symbolGraph templates.SymbolGraph
	gitLog      templates.GitLog
	llm         templates.LLMCaller
	pkgDeps     PackageDepsProvider // may be nil
}

// NewTaxonomyResolver creates a resolver for the given repository.
// symbolGraph must be non-nil. gitLog and llm may be nil when the caller
// knows no LLM-dependent pages will be requested.
func NewTaxonomyResolver(
	repoID string,
	symbolGraph templates.SymbolGraph,
	gitLog templates.GitLog,
	llm templates.LLMCaller,
) *TaxonomyResolver {
	return &TaxonomyResolver{
		repoID:      repoID,
		symbolGraph: symbolGraph,
		gitLog:      gitLog,
		llm:         llm,
	}
}

// WithPackageDeps wires a PackageDepsProvider so Resolve can populate
// Callers/Callees for cluster-based architecture pages. The graph.GraphStore
// satisfies this interface directly. Calling this is optional — without it
// architecture pages are generated without cross-cluster dependency data.
func (r *TaxonomyResolver) WithPackageDeps(p PackageDepsProvider) *TaxonomyResolver {
	r.pkgDeps = p
	return r
}

// PackageGraphInfo holds the graph-level relationships for one package, used
// when resolving architecture pages.
type PackageGraphInfo struct {
	// Package is the fully-qualified import path.
	Package string
	// Callers is the list of packages that import this package.
	Callers []string
	// Callees is the list of packages that this package imports.
	Callees []string
}

// Resolve returns the full taxonomy of [PlannedPage] values for the repository.
// pkgGraph supplies caller/callee information for architecture pages; pass nil
// or an empty slice to skip caller/callee data (architecture pages will still
// be generated, just without relationship context).
//
// clusters is the primary "areas" signal. When non-empty, Resolve derives one
// architecture page per cluster rather than one per package. When nil or
// empty, it falls back to the existing package-path heuristic. The caller
// (orchestrator call site) is responsible for translating full
// clustering.Cluster records into ClusterSummary before passing them here;
// the livingwiki package takes only what it needs.
//
// now is used for Provenance timestamps; pass time.Now() in production.
func (r *TaxonomyResolver) Resolve(ctx context.Context, pkgGraph []PackageGraphInfo, clusters []clustering.ClusterSummary, now time.Time) ([]PlannedPage, error) {
	syms, err := r.symbolGraph.ExportedSymbols(r.repoID)
	if err != nil {
		return nil, fmt.Errorf("taxonomy: fetching symbols: %w", err)
	}

	var pages []PlannedPage

	baseInput := templates.GenerateInput{
		RepoID:      r.repoID,
		SymbolGraph: r.symbolGraph,
		GitLog:      r.gitLog,
		LLM:         r.llm,
		Now:         now,
	}

	// 1a. Architecture pages — cluster-based (primary signal).
	//
	// When clusters are available, emit one architecture page per cluster
	// using the cluster label as the page scope. Callers/Callees are derived
	// from pre-computed package dependency edges if a PackageDepsProvider has
	// been wired in via WithPackageDeps.
	if len(clusters) > 0 {
		// Build package-level dependency lookup once.
		var pkgDepByPkg map[string]*graph.StoredPackageDependencies
		if r.pkgDeps != nil {
			all := r.pkgDeps.GetPackageDependencies(r.repoID)
			pkgDepByPkg = make(map[string]*graph.StoredPackageDependencies, len(all))
			for _, d := range all {
				pkgDepByPkg[d.Package] = d
			}
		}

		// Build a symbol-level package → cluster label lookup so we can
		// emit cross-cluster labels instead of raw package paths.
		symsForCluster := make(map[string][]string) // clusterLabel → []package
		if pkgDepByPkg != nil {
			// Use the exported symbols we already fetched to derive which
			// packages belong to which cluster. We rely on MemberPackages when
			// populated; otherwise we fall back to the representative symbols.
			for _, cs := range clusters {
				seen := make(map[string]struct{})
				for _, pkg := range cs.MemberPackages {
					if _, ok := seen[pkg]; !ok {
						seen[pkg] = struct{}{}
						symsForCluster[cs.Label] = append(symsForCluster[cs.Label], pkg)
					}
				}
			}
		}

		// Build the reverse map: package path → cluster label.
		pkgToCluster := make(map[string]string)
		for clusterLabel, pkgs := range symsForCluster {
			for _, pkg := range pkgs {
				pkgToCluster[pkg] = clusterLabel
			}
		}

		for _, cs := range clusters {
			archInput := baseInput
			archInput.Audience = quality.AudienceEngineers

			// Derive cross-cluster callers/callees.
			var callerLabels, calleeLabels []string
			if pkgDepByPkg != nil && len(cs.MemberPackages) > 0 {
				memberSet := make(map[string]struct{}, len(cs.MemberPackages))
				for _, pkg := range cs.MemberPackages {
					memberSet[pkg] = struct{}{}
				}

				seenCallers := make(map[string]struct{})
				seenCallees := make(map[string]struct{})

				for _, pkg := range cs.MemberPackages {
					dep, ok := pkgDepByPkg[pkg]
					if !ok {
						continue
					}
					// ImportedBy → callers (other clusters that import us).
					for _, byPkg := range dep.ImportedBy {
						if _, inCluster := memberSet[byPkg]; inCluster {
							continue // intra-cluster edge — skip
						}
						label, ok := pkgToCluster[byPkg]
						if !ok {
							continue // orphan package — no architecture page
						}
						if _, seen := seenCallers[label]; !seen {
							seenCallers[label] = struct{}{}
							callerLabels = append(callerLabels, label)
						}
					}
					// Imports → callees (other clusters we import).
					for _, importPkg := range dep.Imports {
						if _, inCluster := memberSet[importPkg]; inCluster {
							continue // intra-cluster edge — skip
						}
						label, ok := pkgToCluster[importPkg]
						if !ok {
							continue // external/stdlib import — no architecture page
						}
						if _, seen := seenCallees[label]; !seen {
							seenCallees[label] = struct{}{}
							calleeLabels = append(calleeLabels, label)
						}
					}
				}
				sort.Strings(callerLabels)
				sort.Strings(calleeLabels)
			}

			pages = append(pages, PlannedPage{
				ID:         archPageID(r.repoID, cs.Label),
				TemplateID: "architecture",
				Audience:   quality.AudienceEngineers,
				Input:      archInput,
				PackageInfo: &ArchitecturePackageInfo{
					Package:        cs.Label,
					Callers:        callerLabels,
					Callees:        calleeLabels,
					MemberPackages: cs.MemberPackages,
				},
			})
		}
	} else {
		// 1b. Architecture pages — package-path fallback.
		//
		// Clusters are absent or stale: fall back to one architecture page
		// per unique Package value, preserving the pre-Sprint-2 behaviour.
		seen := make(map[string]bool)
		var orderedPkgs []string
		for _, s := range syms {
			if !seen[s.Package] {
				seen[s.Package] = true
				orderedPkgs = append(orderedPkgs, s.Package)
			}
		}

		// Build callers/callees lookup.
		graphByPkg := make(map[string]PackageGraphInfo)
		for _, g := range pkgGraph {
			graphByPkg[g.Package] = g
		}

		for _, pkg := range orderedPkgs {
			gi := graphByPkg[pkg]
			archInput := baseInput
			archInput.Audience = quality.AudienceEngineers

			pages = append(pages, PlannedPage{
				ID:         archPageID(r.repoID, pkg),
				TemplateID: "architecture",
				Audience:   quality.AudienceEngineers,
				Input:      archInput,
				PackageInfo: &ArchitecturePackageInfo{
					Package: pkg,
					Callers: gi.Callers,
					Callees: gi.Callees,
				},
			})
		}
	}

	// 2. API reference page (one per repo).
	apiInput := baseInput
	apiInput.Audience = quality.AudienceEngineers
	pages = append(pages, PlannedPage{
		ID:         apiRefPageID(r.repoID),
		TemplateID: "api_reference",
		Audience:   quality.AudienceEngineers,
		Input:      apiInput,
	})

	// 3. System overview page (product audience is the default).
	sysInput := baseInput
	sysInput.Audience = quality.AudienceProduct
	pages = append(pages, PlannedPage{
		ID:         sysOverviewPageID(r.repoID),
		TemplateID: "system_overview",
		Audience:   quality.AudienceProduct,
		Input:      sysInput,
	})

	// 4. Glossary page.
	glossInput := baseInput
	glossInput.Audience = quality.AudienceEngineers
	pages = append(pages, PlannedPage{
		ID:         glossaryPageID(r.repoID),
		TemplateID: "glossary",
		Audience:   quality.AudienceEngineers,
		Input:      glossInput,
	})

	return pages, nil
}

// Manifest returns the [manifest.DependencyManifest] for a planned page.
// Useful for pre-populating the manifest before page generation.
func Manifest(planned PlannedPage) manifest.DependencyManifest {
	scope := manifest.ScopeDirect
	if planned.TemplateID == "system_overview" {
		scope = manifest.ScopeTransitive
	}
	m := manifest.DependencyManifest{
		PageID:   planned.ID,
		Template: planned.TemplateID,
		Audience: string(planned.Audience),
		Dependencies: manifest.Dependencies{
			DependencyScope: scope,
		},
	}
	if planned.PackageInfo != nil {
		m.Dependencies.Paths = []string{planned.PackageInfo.Package + "/**"}
		m.Dependencies.UpstreamPackages = planned.PackageInfo.Callers
		m.Dependencies.DownstreamPackages = planned.PackageInfo.Callees
	}
	return m
}

// archPageID derives the stable page ID for an architecture page.
func archPageID(repoID, pkg string) string {
	slug := replacePathChars(pkg)
	if repoID != "" {
		return repoID + ".arch." + slug
	}
	return "arch." + slug
}

func apiRefPageID(repoID string) string {
	if repoID != "" {
		return repoID + ".api_reference"
	}
	return "api_reference"
}

func sysOverviewPageID(repoID string) string {
	if repoID != "" {
		return repoID + ".system_overview"
	}
	return "system_overview"
}

func glossaryPageID(repoID string) string {
	if repoID != "" {
		return repoID + ".glossary"
	}
	return "glossary"
}

func replacePathChars(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '/', '-':
			out[i] = '.'
		default:
			out[i] = s[i]
		}
	}
	return string(out)
}
