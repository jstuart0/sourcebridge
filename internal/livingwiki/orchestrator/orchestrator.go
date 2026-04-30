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
// [Config.TimeBudget]. Pages that completed successfully before the deadline
// are still persisted; see [Orchestrator.Generate].
var ErrTimeBudgetExceeded = errors.New("orchestrator: time budget exceeded")

// ErrSystemicSoftFailures is returned by [Orchestrator.Generate] when the
// soft-failure sliding-window breaker trips — meaning many pages in a row
// have hit the same LLM error category, indicating the LLM provider is
// likely unreachable. Pages that completed successfully before the trip
// are still persisted; the caller should NOT auto-retry.
var ErrSystemicSoftFailures = errors.New("orchestrator: systemic LLM failure detected")

// SystemicAbortDetail is the structured form of [ErrSystemicSoftFailures].
// Use [errors.As] to extract the dominant category and counts; use
// [errors.Is] with [ErrSystemicSoftFailures] (the Unwrap target) to test
// the error class.
//
// LastViolation is the human-readable message from the page-violation that
// tripped the breaker (extracted from the synthesized [ExcludedPage]'s
// SecondResult). The raw underlying [error] value is not available at the
// trip site because [pageOutcome] only carries the synthesized
// [ExcludedPage] by the time the failure is observed.
//
// Callers (e.g. the cold-start runner) use this to label Prometheus metrics
// and structured logs with the dominant per-page category.
type SystemicAbortDetail struct {
	Category      string
	Count         int
	Window        int
	LastViolation string
}

// Error returns the same human-readable message format the prior
// fmt.Errorf-wrapped sentinel produced.
func (e *SystemicAbortDetail) Error() string {
	return fmt.Sprintf(
		"orchestrator: systemic LLM failure detected: %d page failures with category %q in last %d completions; last underlying error: %s",
		e.Count, e.Category, e.Window, e.LastViolation,
	)
}

// Unwrap returns [ErrSystemicSoftFailures] so [errors.Is] continues to
// match the existing partial-generation gating logic in
// [IsPartialGenerationError].
func (e *SystemicAbortDetail) Unwrap() error { return ErrSystemicSoftFailures }

// SystemicAbortCategory returns the dominant failure category from a
// systemic-abort error, or "" when err is not (or does not wrap) a
// [*SystemicAbortDetail]. Callers in non-orchestrator packages use this
// helper instead of importing the type.
func SystemicAbortCategory(err error) string {
	var d *SystemicAbortDetail
	if errors.As(err, &d) {
		return d.Category
	}
	return ""
}

// errTemplateNotFound is the sentinel wrapped when a planned page references
// a template that is not in the registry. This is treated as a fatal config
// bug rather than a per-page soft failure (a soft failure would just hide the
// configuration mistake).
var errTemplateNotFound = errors.New("template not registered")

// IsPartialGenerationError reports whether err represents a Generate error
// for which any pages that completed before the abort have been persisted.
// Callers (e.g. the cold-start runner) use this to decide whether to dispatch
// the partial result to sinks. Errors not in this set indicate either a
// programmer/config bug or an external cancellation; the run produced nothing
// usable and should not be dispatched.
func IsPartialGenerationError(err error) bool {
	return errors.Is(err, ErrTimeBudgetExceeded) || errors.Is(err, ErrSystemicSoftFailures)
}

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

	// RelatedPageIDsByLabel is a caller-supplied map from a related-page label
	// (cluster name or package path) to the resolver-computed page ID for that
	// related page. Templates use this to render cross-page links so all link
	// targets share the resolver's prefix policy (overview./detail./arch.).
	//
	// When nil, templates fall back to their legacy pageIDFor derivation.
	// Populated by the cold-start runner from the FULL taxonomy manifest
	// BEFORE smart-resume splits buckets (CR3 — map must cover skipped pages
	// too so cross-page links from regenerate pages resolve to their correct IDs).
	RelatedPageIDsByLabel map[string]string
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
	// ID is the persisted knowledge.Artifact.ID. Stable across runs as long as
	// the artifact hasn't been deleted and recreated. Included in the page
	// fingerprint so a regenerated artifact (same ScopePath, new ID) invalidates
	// the page fingerprint (CR6).
	ID string

	Type        string // e.g. "cliff_notes", "architecture_diagram"
	Audience    string
	Depth       string // "summary", "medium", or "deep"
	ScopePath   string // module / package path the artifact covers

	// ScopeType is the knowledge.ScopeType ("repository" / "module" / "file" /
	// "symbol" / "requirement"). Combined with ScopePath this fully identifies
	// the slice the artifact covers (CR6).
	ScopeType string

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

	// SoftFailureWindow is the number of most-recent page completions tracked
	// by the systemic-failure breaker. Each entry is either a success or a
	// failure with a normalized category. When zero, defaults to
	// max(2*MaxConcurrency, 30).
	SoftFailureWindow int

	// SoftFailureThreshold is the number of same-category soft failures
	// within the window that trips the breaker, aborting the run with
	// [ErrSystemicSoftFailures]. When zero, defaults to
	// max(MaxConcurrency+1, 15) so a single in-flight wave of same-category
	// failures cannot single-handedly trip the breaker, regardless of
	// MaxConcurrency.
	SoftFailureThreshold int
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

	// OnPageReady, when non-nil, is invoked synchronously inside the orchestrator's
	// post-eg.Wait() persistence loop after each successful SetProposed/SetCanonical
	// call, BEFORE the loop advances to the next page.
	//
	// Contract (CR2 — locked):
	//   - The callback MUST NOT perform blocking I/O. Runners that need to dispatch
	//     pages to sinks MUST enqueue an event onto an internal channel and return
	//     immediately. Blocking here extends the persistence-loop wall-clock and
	//     risks exhausting persistCtx's 30s budget.
	//   - No context argument: the orchestrator does not own dispatch lifetime.
	//     The runner owns dispatchCtx and its cancellation policy.
	//   - Fires ONLY when shouldPersist=true (i.e. genErr == nil or IsPartialGenerationError).
	//     On hard aborts, the persistence loop is skipped and OnPageReady never fires —
	//     preserving the all-or-nothing-on-hard-abort contract (mozart locked surface).
	//   - OnPageDone continues to fire per-page-immediately from the per-page goroutine
	//     (unchanged). OnPageReady fires later, in the post-Wait persistence loop.
	//     Two callbacks, two events.
	//
	// fingerprint is the value from PageFingerprints[page.ID], pre-computed at
	// planning time and passed through unchanged. The orchestrator does not interpret it.
	OnPageReady func(page ast.Page, fingerprint string)

	// PageFingerprints is a map from page ID to pre-computed content fingerprint
	// (computed once at smart-resume time, reused here). The orchestrator passes
	// the matching fingerprint to OnPageReady for each persisted page.
	// May be nil; OnPageReady receives an empty fingerprint in that case.
	PageFingerprints map[string]string
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

// ExcludedPage records a page that was excluded from the run, either after
// two quality-gate failures, or after an unrecoverable per-page LLM/render
// error. Use [ExcludedPage.Reason] and [ExcludedPage.FailureCategory] to
// distinguish without parsing free-text.
type ExcludedPage struct {
	// PageID is the page that was excluded.
	PageID string

	// TemplateID is the template that was used.
	TemplateID string

	// FirstResult is the first-attempt validation result. Populated for
	// gate-failure exclusions; zero-value for LLM/render-error exclusions.
	FirstResult quality.ValidationResult

	// SecondResult is the second-attempt (retry) validation result for
	// gate failures. For LLM-error exclusions, this carries a synthetic
	// ValidationResult with one violation describing the underlying error
	// — that lets [buildPRBody]'s existing rendering surface the message
	// without changes.
	SecondResult quality.ValidationResult

	// Reason classifies the exclusion type for callers/tests that want
	// to distinguish without parsing free-text.
	//   - "gate_failure" — both quality-gate attempts produced violations.
	//   - "llm_error"    — LLM caller / template returned an error mid-page.
	//   - "render_error" — markdown renderer rejected the page output.
	Reason string

	// FailureCategory is set when Reason == "llm_error". One of the
	// normalized categories from classifySoftFailure:
	//   - "deadline_exceeded"
	//   - "provider_unavailable"
	//   - "provider_compute"
	//   - "llm_empty"
	//   - "render_error"
	//   - "template_internal"
	// Empty string for non-LLM exclusions.
	FailureCategory string
}

// Reason values for [ExcludedPage.Reason].
const (
	ExclusionReasonGateFailure  = "gate_failure"
	ExclusionReasonLLMError     = "llm_error"
	ExclusionReasonRenderError  = "render_error"
)

// Soft-failure category values for [ExcludedPage.FailureCategory].
const (
	SoftFailureCategoryDeadlineExceeded   = "deadline_exceeded"
	SoftFailureCategoryProviderUnavailable = "provider_unavailable"
	SoftFailureCategoryProviderCompute    = "provider_compute"
	SoftFailureCategoryLLMEmpty           = "llm_empty"
	SoftFailureCategoryRenderError        = "render_error"
	SoftFailureCategoryTemplateInternal   = "template_internal"
)

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
	cfg.SoftFailureWindow = effectiveSoftFailureWindow(cfg)
	cfg.SoftFailureThreshold = effectiveSoftFailureThreshold(cfg)
	return &Orchestrator{cfg: cfg, registry: registry, store: store, debounce: newRepoDebounceTracker()}
}

// effectiveSoftFailureWindow returns the sliding-window size, applying the
// max(2*MaxConcurrency, 30) default when zero.
func effectiveSoftFailureWindow(cfg Config) int {
	if cfg.SoftFailureWindow > 0 {
		return cfg.SoftFailureWindow
	}
	w := 2 * cfg.MaxConcurrency
	if w < 30 {
		w = 30
	}
	return w
}

// effectiveSoftFailureThreshold returns the breaker threshold, applying the
// max(MaxConcurrency+1, 15) default when zero. The +1 ensures a single
// in-flight wave of same-category failures cannot trip the breaker even
// when MaxConcurrency is large.
func effectiveSoftFailureThreshold(cfg Config) int {
	if cfg.SoftFailureThreshold > 0 {
		return cfg.SoftFailureThreshold
	}
	t := cfg.MaxConcurrency + 1
	if t < 15 {
		t = 15
	}
	return t
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
// Store returns the PageStore that backs this Orchestrator.
// Called by the Phase 3 fix-up pass in the coldstart handler.
func (o *Orchestrator) Store() PageStore { return o.store }

func (o *Orchestrator) Generate(ctx context.Context, req GenerateRequest) (GenerateResult, error) {
	cfg := mergeConfig(o.cfg, req.Config)
	// Recompute window/threshold defaults in case mergeConfig produced a
	// MaxConcurrency that differs from the orchestrator-level value.
	cfg.SoftFailureWindow = effectiveSoftFailureWindow(cfg)
	cfg.SoftFailureThreshold = effectiveSoftFailureThreshold(cfg)

	start := time.Now()
	parentCtx := ctx
	deadline := start.Add(cfg.TimeBudget)
	runCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	outcomes := make([]pageOutcome, len(req.Pages))
	var outcomesMu sync.Mutex
	sw := newSoftFailureWindow(cfg.SoftFailureWindow, cfg.SoftFailureThreshold)

	eg, egCtx := errgroup.WithContext(runCtx)
	eg.SetLimit(cfg.MaxConcurrency)

	for idx, planned := range req.Pages {
		idx, planned := idx, planned // capture for goroutine
		eg.Go(func() error {
			outcome, err := o.generateOnePage(egCtx, cfg, planned)
			if err != nil {
				// Whole-run cancellation (parent ctx, deadline, or
				// peer goroutine errored): propagate so eg.Wait can
				// shut down cleanly.
				if egCtx.Err() != nil {
					return err
				}
				// Programmer/config error — fatal.
				if errors.Is(err, errTemplateNotFound) {
					return fmt.Errorf("page %q: %w", planned.ID, err)
				}
				// Otherwise: convert to a soft-failure outcome so the
				// rest of the run continues. The systemic-failure
				// breaker decides whether the cumulative pattern
				// warrants aborting.
				cat := classifySoftFailure(err)
				excl := newSoftFailureExcludedPage(planned, cat, err)
				outcome = pageOutcome{excluded: excl}
				err = nil
			}

			outcomesMu.Lock()
			outcomes[idx] = outcome
			outcomesMu.Unlock()

			// Update sliding-window counters and check breaker.
			//
			// Codex r2 [High]: count BOTH llm_error and render_error
			// exclusions — both are non-gate soft failures and a
			// systemic render bug should trip the breaker just like a
			// systemic LLM outage. Gate-failure exclusions don't move
			// the breaker; they're already capped at 2 attempts per
			// page and a "page didn't pass quality" pattern shouldn't
			// abort the run.
			isSoftFailure := outcome.excluded != nil &&
				(outcome.excluded.Reason == ExclusionReasonLLMError ||
					outcome.excluded.Reason == ExclusionReasonRenderError)
			if isSoftFailure {
				cat := outcome.excluded.FailureCategory
				if cat == "" {
					cat = SoftFailureCategoryTemplateInternal
				}
				// Codex r2 [Low]: record AND check atomically so the
				// count reported in the error message always reflects
				// the same window state that crossed the threshold.
				count, exceeded := sw.recordAndCheck(cat)
				if exceeded {
					var lastViolation string
					if vr := outcome.excluded.SecondResult; len(vr.Gates) > 0 && len(vr.Gates[0].Violations) > 0 {
						lastViolation = vr.Gates[0].Violations[0].Message
					}
					// Return the structured form so callers can extract the
					// dominant category via errors.As. errors.Is(err,
					// ErrSystemicSoftFailures) still works via Unwrap.
					return &SystemicAbortDetail{
						Category:      cat,
						Count:         count,
						Window:        sw.window,
						LastViolation: lastViolation,
					}
				}
			} else if outcome.excluded == nil {
				// Clean success — reset the streak.
				sw.recordSuccess()
			}

			// Notify the caller (e.g. cold-start job progress reporter)
			// after each page completes. The callback is
			// concurrency-safe by contract.
			if req.OnPageDone != nil {
				switch {
				case outcome.excluded != nil:
					warn := ""
					if outcome.excluded.Reason == ExclusionReasonLLMError {
						warn = outcome.excluded.FailureCategory
					}
					req.OnPageDone(planned.ID, true, warn)
				default:
					req.OnPageDone(planned.ID, false, "")
				}
			}
			return nil
		})
	}

	waitErr := eg.Wait()

	// Normalize waitErr BEFORE the partial-persistence gate so a bare
	// context.DeadlineExceeded (from the runCtx deadline) becomes
	// ErrTimeBudgetExceeded. ErrSystemicSoftFailures is already wrapped
	// at trip-time. (codex r1c [High] — both error classes must satisfy
	// IsPartialGenerationError so persistence and dispatch run.)
	genErr := waitErr
	if genErr != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) &&
		!errors.Is(genErr, ErrTimeBudgetExceeded) &&
		!errors.Is(genErr, ErrSystemicSoftFailures) {
		genErr = fmt.Errorf("%w: %v", ErrTimeBudgetExceeded, genErr)
	}

	// Always collect outcomes; partial-result handling depends on the error class.
	var generated []ast.Page
	var excluded []ExcludedPage
	files := make(map[string][]byte)
	for _, outcome := range outcomes {
		if outcome.excluded != nil {
			excluded = append(excluded, *outcome.excluded)
			continue
		}
		if outcome.page.ID == "" {
			continue // zero-value page means the goroutine didn't run (cancellation)
		}
		generated = append(generated, outcome.page)
		files[wikiFilePath(outcome.page.ID)] = outcome.rendered
	}

	// Decide whether to persist partials.
	shouldPersist := genErr == nil || IsPartialGenerationError(genErr)

	// Use a fresh, non-cancelable context for the cleanup phase so a
	// deadline-cancelled runCtx doesn't bork the writes. Bound it with a
	// short timeout so we don't hang forever on a wedged store. (codex
	// r1b [High] — the original ctx is already cancelled when we get here
	// on the time-budget path.)
	// Codex r2b [Medium]: track pages that were ACTUALLY persisted, so a
	// mid-loop store/PR failure doesn't return the full pre-write slice
	// to callers as if all pages were durable.
	var prID string
	persisted := make([]ast.Page, 0, len(generated))
	if shouldPersist && len(generated) > 0 {
		persistCtx, persistCancel := context.WithTimeout(
			context.WithoutCancel(parentCtx), 30*time.Second,
		)
		defer persistCancel()

		if cfg.DirectPublish {
			for _, page := range generated {
				if err := o.store.SetCanonical(persistCtx, cfg.RepoID, page); err != nil {
					return GenerateResult{Generated: persisted, Excluded: excluded, Duration: time.Since(start)},
						fmt.Errorf("orchestrator: storing canonical page %q: %w (also: %v)", page.ID, err, genErr)
				}
				persisted = append(persisted, page)
				// Fire OnPageReady after successful persistence. The callback must
				// not block (CR2: non-blocking enqueue onto a runner-owned channel).
				if req.OnPageReady != nil {
					fp := ""
					if req.PageFingerprints != nil {
						fp = req.PageFingerprints[page.ID]
					}
					req.OnPageReady(page, fp)
				}
			}
			if req.Writer != nil && len(files) > 0 {
				if err := req.Writer.WriteFiles(persistCtx, files); err != nil {
					// All pages have been written to the store individually;
					// the file write is the bulk path. Surface persisted
					// store-rows so callers see what's durable.
					return GenerateResult{Generated: persisted, Excluded: excluded, Duration: time.Since(start)},
						fmt.Errorf("orchestrator: writing files: %w (also: %v)", err, genErr)
				}
			}
		} else {
			if req.PR == nil {
				return GenerateResult{Generated: persisted, Excluded: excluded, Duration: time.Since(start)},
					fmt.Errorf("orchestrator: WikiPR must be non-nil in PR mode")
			}
			prID = req.PR.ID()
			for _, page := range generated {
				if err := o.store.SetProposed(persistCtx, cfg.RepoID, prID, page); err != nil {
					return GenerateResult{Generated: persisted, Excluded: excluded, PRID: prID, Duration: time.Since(start)},
						fmt.Errorf("orchestrator: storing proposed page %q: %w (also: %v)", page.ID, err, genErr)
				}
				persisted = append(persisted, page)
				// Fire OnPageReady after successful SetProposed. The callback must
				// not block (CR2: non-blocking enqueue onto a runner-owned channel).
				// Fires ONLY when shouldPersist=true — the all-or-nothing-on-hard-abort
				// contract is preserved because the persistence loop is skipped entirely
				// when shouldPersist=false (the gate at orchestrator.go line 669 is unchanged).
				if req.OnPageReady != nil {
					fp := ""
					if req.PageFingerprints != nil {
						fp = req.PageFingerprints[page.ID]
					}
					req.OnPageReady(page, fp)
				}
			}
			body := buildPRBody(persisted, excluded)
			if err := req.PR.Open(persistCtx, cfg.PRBranch, cfg.PRTitle, body, files); err != nil {
				// Store rows persisted, but the PR open itself failed.
				// Pages are durable in the page store, but no PR exists
				// to track them. Surface what's in the store; caller can
				// re-Open the PR if it has a recovery path.
				return GenerateResult{Generated: persisted, Excluded: excluded, PRID: prID, Duration: time.Since(start)},
					fmt.Errorf("orchestrator: opening PR: %w (also: %v)", err, genErr)
			}
		}
	}

	// Final disposition. Codex r1c [Low]: include PRID in partial-error
	// returns so callers can see what PR was opened.
	//
	// Codex r2 [Medium]: result.Generated must reflect ONLY pages that
	// were actually persisted. If shouldPersist was false (hard error
	// path), pages may have completed in goroutines before the abort,
	// but they were NOT written to the store — reporting them as
	// "generated" overstates the durable state. Excluded entries are
	// in-memory anyway, so they're safe to surface either way.
	var resultGenerated []ast.Page
	if shouldPersist {
		resultGenerated = persisted
	}
	result := GenerateResult{
		Generated: resultGenerated,
		Excluded:  excluded,
		PRID:      prID,
		Duration:  time.Since(start),
	}
	if genErr != nil {
		return result, genErr
	}
	return result, nil
}

// newSoftFailureExcludedPage builds an [ExcludedPage] for a per-page LLM /
// render error. Populating SecondResult with a synthetic [quality.ValidationResult]
// lets the existing [buildPRBody] / livingwiki/coldstart [buildExclusionReasons]
// surface the underlying error message in their existing rendering paths
// without changes.
func newSoftFailureExcludedPage(planned PlannedPage, category string, cause error) *ExcludedPage {
	reason := ExclusionReasonLLMError
	if category == SoftFailureCategoryRenderError {
		reason = ExclusionReasonRenderError
	}
	return &ExcludedPage{
		PageID:     planned.ID,
		TemplateID: planned.TemplateID,
		Reason:     reason,
		FailureCategory: category,
		// Synthetic SecondResult so buildExclusionReasons surfaces the
		// underlying error in the existing UI/PR rendering paths.
		SecondResult: quality.ValidationResult{
			AttemptNumber: 2,
			GatesPassed:   false,
			Decision:      quality.RetryReject,
			RunAt:         time.Now().UTC(),
			Gates: []quality.RuleResult{{
				ValidatorID: quality.ValidatorID("llm_generation_error"),
				Level:       quality.LevelGate,
				Violations: []quality.Violation{{
					Message: fmt.Sprintf("%s: %s", category, cause.Error()),
				}},
			}},
		},
	}
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
//
// Exactly one of {page (success), excluded (gate or soft failure)} is
// populated for a non-zero-value outcome.
type pageOutcome struct {
	page     ast.Page
	excluded *ExcludedPage
	rendered []byte
}

// classifySoftFailure maps a per-page generation error to a normalized
// category used by the systemic-failure breaker and by [ExcludedPage.FailureCategory].
// The mapping mirrors patterns from internal/llm/orchestrator/runtime.go:ClassifyError
// but at a coarser granularity suitable for "is the LLM provider broken" detection.
func classifySoftFailure(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return SoftFailureCategoryDeadlineExceeded
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "context deadline"):
		return SoftFailureCategoryDeadlineExceeded
	case strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "transport is closing"),
		strings.Contains(msg, "unavailable"):
		return SoftFailureCategoryProviderUnavailable
	case strings.Contains(msg, "compute error"), strings.Contains(msg, "server_error"):
		return SoftFailureCategoryProviderCompute
	case strings.Contains(msg, "llm returned empty content"),
		strings.Contains(msg, "empty content"):
		return SoftFailureCategoryLLMEmpty
	case strings.Contains(msg, "render"):
		return SoftFailureCategoryRenderError
	}
	return SoftFailureCategoryTemplateInternal
}

// softFailureWindow is a sliding-window same-category counter. The systemic
// breaker calls recordFailure / recordSuccess after each page completion
// (any order — page completions across goroutines are serialised through
// the window's mutex). The breaker trips when any single category has at
// least `threshold` failures within the last `window` completions.
type softFailureWindow struct {
	mu        sync.Mutex
	window    int
	events    []string // ring buffer of category strings; "" = success
	head      int
	size      int            // number of entries currently held; up to window
	byCat     map[string]int // count per category in the current window
	threshold int
}

func newSoftFailureWindow(window, threshold int) *softFailureWindow {
	return &softFailureWindow{
		window:    window,
		threshold: threshold,
		events:    make([]string, window),
		byCat:     make(map[string]int),
	}
}

func (s *softFailureWindow) record(category string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// If at capacity, evict the oldest entry from byCat.
	if s.size == s.window {
		old := s.events[s.head]
		if old != "" {
			s.byCat[old]--
			if s.byCat[old] <= 0 {
				delete(s.byCat, old)
			}
		}
	} else {
		s.size++
	}
	s.events[s.head] = category
	if category != "" {
		s.byCat[category]++
	}
	s.head = (s.head + 1) % s.window
}

func (s *softFailureWindow) recordFailure(category string) { s.record(category) }
func (s *softFailureWindow) recordSuccess()                { s.record("") }

func (s *softFailureWindow) exceeded(category string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byCat[category] >= s.threshold
}

func (s *softFailureWindow) countFor(category string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byCat[category]
}

// recordAndCheck records a failure for the given category and atomically
// returns the new count and whether the breaker has tripped. Use this
// instead of separate recordFailure + exceeded calls so the count in the
// error message always reflects the same window state that crossed the
// threshold (codex r2 [Low]).
func (s *softFailureWindow) recordAndCheck(category string) (count int, exceeded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Inline the record() body so we hold the lock through both phases.
	if s.size == s.window {
		old := s.events[s.head]
		if old != "" {
			s.byCat[old]--
			if s.byCat[old] <= 0 {
				delete(s.byCat, old)
			}
		}
	} else {
		s.size++
	}
	s.events[s.head] = category
	if category != "" {
		s.byCat[category]++
	}
	s.head = (s.head + 1) % s.window
	count = s.byCat[category]
	exceeded = count >= s.threshold
	return count, exceeded
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
		// Wrap the sentinel so callers can distinguish "config bug, hard-fail"
		// from "transient LLM error, soft-fail". See orchestrator.go's
		// errgroup goroutine — errTemplateNotFound is the only soft-fail
		// opt-out.
		return pageOutcome{}, fmt.Errorf("%w: %q", errTemplateNotFound, planned.TemplateID)
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
				Reason:       ExclusionReasonGateFailure,
			}
			return pageOutcome{excluded: excl}, nil
		}
		// RetryWithReasons: loop for attempt 2.
	}

	rendered, err := renderPage(page)
	if err != nil {
		// Wrap so the soft-failure classifier categorizes this as
		// "render_error" rather than a generic template_internal.
		return pageOutcome{}, fmt.Errorf("render: %w", err)
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
		fmt.Fprintf(&sb, "The following pages could not be generated cleanly (gate failure or LLM error) and were excluded:\n\n")
		for _, e := range excluded {
			label := "see quality report below"
			if e.Reason == ExclusionReasonLLMError {
				label = fmt.Sprintf("LLM error: %s", e.FailureCategory)
			}
			fmt.Fprintf(&sb, "- `%s` — %s\n", e.PageID, label)
		}
		fmt.Fprintf(&sb, "\n")
		for _, e := range excluded {
			fmt.Fprintf(&sb, "#### Report for `%s`\n\n", e.PageID)
			if e.Reason == ExclusionReasonGateFailure {
				fmt.Fprintf(&sb, "**Attempt 1:**\n\n%s\n", e.FirstResult.QualityReportMarkdown())
				fmt.Fprintf(&sb, "**Attempt 2:**\n\n%s\n", e.SecondResult.QualityReportMarkdown())
			} else {
				// LLM/render error: only SecondResult carries the synthetic violation.
				fmt.Fprintf(&sb, "**Generation error:**\n\n%s\n", e.SecondResult.QualityReportMarkdown())
			}
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
	if override.SoftFailureWindow > 0 {
		merged.SoftFailureWindow = override.SoftFailureWindow
	}
	if override.SoftFailureThreshold > 0 {
		merged.SoftFailureThreshold = override.SoftFailureThreshold
	}
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
