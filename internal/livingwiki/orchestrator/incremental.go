// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// This file implements the A1.P2 incremental generation model:
// two-watermark diffing, additive commits on open PRs, and reviewer-commit
// detection with block-level human-edit protection.

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

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
)

// SourceBridgeCommitterName is the committer name stamped on every bot-authored
// commit to a wiki PR branch. Bot commits are identified by this name and the
// paired [SourceBridgeCommitterEmail].
const SourceBridgeCommitterName = "sourcebridge[bot]"

// SourceBridgeCommitterEmail is the committer email stamped on every bot-authored
// commit to a wiki PR branch.
const SourceBridgeCommitterEmail = "sourcebridge[bot]@users.noreply.github.com"

// maxLinesSkipGuard is the diff-size threshold above which we skip generation
// for an individual page. Diffs larger than this are overwhelmingly churns
// (vendor updates, generated files) rather than meaningful doc-relevant changes.
const maxLinesSkipGuard = 5_000

// maxPagesPerPush is the per-push LLM budget cap. When more than this many
// pages are affected, the top N by priority are generated and the rest are
// queued for the next push.
const maxPagesPerPush = 5

// debouncePeriod is how long we wait between two consecutive generation
// attempts for the same repo. A push that arrives within this window after a
// previous generation started is a no-op (debounced).
const debouncePeriod = 60 * time.Second

// ErrDebounced is returned by GenerateIncremental when a generation was
// attempted for the same repo within the debounce window.
var ErrDebounced = errors.New("orchestrator: incremental generation debounced — too soon after last attempt")

// ErrSHANotFound is returned by a DiffProvider when the base SHA is no longer
// reachable (e.g. after a force-push rewrites history).
var ErrSHANotFound = errors.New("orchestrator: base SHA not found — source repo history was likely rewritten")

// DiffProvider computes the diff between two commits and returns the changed
// file pairs. The caller injects this so the orchestrator stays decoupled from
// any specific git implementation.
//
// Implementations must be safe for concurrent use.
type DiffProvider interface {
	// Diff returns the set of changed (path, symbol) pairs between baseSHA and
	// headSHA. baseSHA may be empty, meaning "diff from the very beginning"
	// (full tree). Returns [ErrSHANotFound] when baseSHA cannot be resolved
	// (force-push scenario).
	//
	// TotalLines is the total line count of the diff across all files. When
	// TotalLines exceeds [maxLinesSkipGuard] the orchestrator skips generation.
	Diff(ctx context.Context, repoID, baseSHA, headSHA string) (DiffResult, error)
}

// DiffResult is the outcome of a [DiffProvider.Diff] call.
type DiffResult struct {
	// Changed is the set of (path, symbol) pairs that changed.
	Changed []manifest.ChangedPair

	// TotalLines is the total number of added+removed lines across all files.
	TotalLines int
}

// Commit is one commit on a branch, carrying enough metadata for the
// reviewer-commit detection logic.
type Commit struct {
	// SHA is the full commit hash.
	SHA string

	// CommitterName is the name field from the commit's committer object.
	CommitterName string

	// CommitterEmail is the email field from the commit's committer object.
	CommitterEmail string

	// Files is the map of file paths changed by this commit. Values are the
	// file contents after the commit (nil means deleted).
	Files map[string][]byte
}

// IsBotCommit reports whether this commit was authored by the SourceBridge bot,
// identified by a match on [SourceBridgeCommitterName].
func (c Commit) IsBotCommit() bool {
	return c.CommitterName == SourceBridgeCommitterName
}

// IncrementalRequest carries the inputs for one incremental generation run.
type IncrementalRequest struct {
	// HeadSHA is the source commit that triggered this run (the push HEAD).
	HeadSHA string

	// Pages is the full list of known page manifests for this repo.
	// The orchestrator uses these to call manifest.AffectedPages.
	Pages []manifest.DependencyManifest

	// DiffProvider computes the changed pairs. Must be non-nil.
	DiffProvider DiffProvider

	// ManifestResolver resolves packages for the diff → affected-pages logic.
	// May be nil when no transitive pages exist.
	ManifestResolver manifest.PackageGraphResolver

	// PR is the current open wiki PR for this repo, or nil when none is open.
	// When non-nil, the orchestrator appends commits to the PR branch instead
	// of opening a new PR.
	PR ExtendedWikiPR

	// Writer is used when no open PR exists and we need to write incremental
	// files to a branch. May be nil when PR is non-nil.
	Writer ExtendedRepoWriter

	// WatermarkStore persists the two watermarks. Must be non-nil.
	WatermarkStore WatermarkStore

	// Config overrides the orchestrator-level config for this run.
	Config Config

	// Now is the wall-clock time for this run. When zero, time.Now() is used.
	// Provided so tests can inject deterministic times for debounce checks.
	Now time.Time
}

// IncrementalResult summarises the outcome of an incremental generation run.
type IncrementalResult struct {
	// Regenerated is the list of pages that were regenerated.
	Regenerated []ast.Page

	// Queued is the list of page IDs that were deferred because the 5-page
	// budget was exhausted.
	Queued []string

	// Skipped is true when the diff exceeded the 5,000-line skip guard.
	Skipped bool

	// Debounced is true when the run was suppressed by the 60s debounce window.
	Debounced bool

	// ForcePush is true when the diff provider reported SHA-not-found, causing
	// a full regen with watermark reset.
	ForcePush bool

	// PRID is the PR identifier that received the additive commit (or was opened
	// fresh). Empty when the result was skipped or debounced.
	PRID string
}

// ExtendedWikiPR extends [WikiPR] with the incremental-update operations
// needed by A1.P2.
type ExtendedWikiPR interface {
	WikiPR

	// Branch returns the current branch name for this PR.
	Branch() string

	// AppendCommitToBranch appends a new commit to the existing PR branch
	// without force-pushing. message becomes the commit message.
	// files is a map of wiki-relative paths → content.
	AppendCommitToBranch(ctx context.Context, branch string, files map[string][]byte, message string) error

	// ListCommitsOnBranch returns commits on branch that were recorded at or
	// after since. Order is oldest-first. Returns an empty slice when none exist.
	ListCommitsOnBranch(ctx context.Context, branch string, since time.Time) ([]Commit, error)

	// PostComment posts a comment on the PR (used when a stale_when condition
	// fires on a human-edited-on-pr-branch block but we cannot overwrite it).
	PostComment(ctx context.Context, body string) error

	// UpdateDescription replaces the PR description body.
	UpdateDescription(ctx context.Context, body string) error
}

// ExtendedRepoWriter extends [RepoWriter] with the branch-write operations
// needed by A1.P2 when writing to a branch without an open PR handle.
type ExtendedRepoWriter interface {
	RepoWriter

	// AppendCommitToBranch writes files to a branch as a new commit.
	AppendCommitToBranch(ctx context.Context, branch string, files map[string][]byte, message string) error

	// ListCommitsOnBranch returns commits on branch at or after since.
	ListCommitsOnBranch(ctx context.Context, branch string, since time.Time) ([]Commit, error)
}

// repoDebounceTracker tracks the last generation start time per repo.
type repoDebounceTracker struct {
	mu      sync.Mutex
	lastRun map[string]time.Time // key: repoID
}

func newRepoDebounceTracker() *repoDebounceTracker {
	return &repoDebounceTracker{lastRun: make(map[string]time.Time)}
}

// checkAndRecord returns true when the repo is within the debounce window (and
// does not record a new run in that case). When not debounced, it records this
// run's start time and returns false.
func (d *repoDebounceTracker) checkAndRecord(repoID string, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	last, ok := d.lastRun[repoID]
	if ok && now.Sub(last) < debouncePeriod {
		return true // debounced
	}
	d.lastRun[repoID] = now
	return false
}

// GenerateIncremental implements the A1.P2 two-watermark incremental model.
//
// Algorithm:
//  1. Check 60s debounce window for this repo.
//  2. Read WikiPublishedSHA from WatermarkStore.
//  3. Call DiffProvider.Diff(WikiPublishedSHA, HeadSHA).
//  4. If ErrSHANotFound → force-push path: reset watermarks, full regen.
//  5. If TotalLines > 5,000 → skip, advance processed watermark.
//  6. Call manifest.AffectedPages to find pages that need regen.
//  7. Apply 5-page budget: top-N by priority (direct hit > graph hit);
//     remainder goes to Queued.
//  8. Generate affected pages.
//  9. Apply reviewer-commit reconciliation: skip human-edited-on-pr-branch blocks.
//  10. Store updated proposed_ast for each page.
//  11. If open PR → AppendCommitToBranch; else → write via ExtendedRepoWriter.
//  12. AdvanceProcessed(HeadSHA).
func (o *Orchestrator) GenerateIncremental(ctx context.Context, req IncrementalRequest) (IncrementalResult, error) {
	cfg := mergeConfig(o.cfg, req.Config)
	repoID := cfg.RepoID

	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}

	// Step 1: Debounce check.
	if o.debounce.checkAndRecord(repoID, now) {
		return IncrementalResult{Debounced: true}, ErrDebounced
	}

	if req.DiffProvider == nil {
		return IncrementalResult{}, fmt.Errorf("orchestrator: IncrementalRequest.DiffProvider must be non-nil")
	}
	if req.WatermarkStore == nil {
		return IncrementalResult{}, fmt.Errorf("orchestrator: IncrementalRequest.WatermarkStore must be non-nil")
	}

	// Step 2: Read watermarks.
	marks, err := req.WatermarkStore.Get(ctx, repoID)
	if err != nil {
		return IncrementalResult{}, fmt.Errorf("orchestrator: reading watermarks: %w", err)
	}
	baseSHA := marks.WikiPublishedSHA

	// Step 3: Compute diff.
	diffResult, diffErr := req.DiffProvider.Diff(ctx, repoID, baseSHA, req.HeadSHA)

	// Step 4: Force-push detection.
	if errors.Is(diffErr, ErrSHANotFound) {
		return o.handleForcePush(ctx, req, cfg)
	}
	if diffErr != nil {
		return IncrementalResult{}, fmt.Errorf("orchestrator: diff(%q..%q): %w", baseSHA, req.HeadSHA, diffErr)
	}

	// Step 5: Skip-too-large guard.
	if diffResult.TotalLines > maxLinesSkipGuard {
		if advErr := req.WatermarkStore.AdvanceProcessed(ctx, repoID, req.HeadSHA); advErr != nil {
			return IncrementalResult{}, fmt.Errorf("orchestrator: advancing processed watermark: %w", advErr)
		}
		return IncrementalResult{Skipped: true}, nil
	}

	// Step 6: Compute affected pages.
	affected := manifest.AffectedPages(diffResult.Changed, req.Pages, req.ManifestResolver, 2)
	if len(affected) == 0 {
		// Nothing to do; still advance processed watermark.
		if advErr := req.WatermarkStore.AdvanceProcessed(ctx, repoID, req.HeadSHA); advErr != nil {
			return IncrementalResult{}, fmt.Errorf("orchestrator: advancing processed watermark: %w", advErr)
		}
		return IncrementalResult{}, nil
	}

	// Step 7: Apply 5-page budget.
	prioritized, queued := applyPageBudget(affected, maxPagesPerPush)

	// Determine the current PR ID (if any).
	prID := ""
	if req.PR != nil {
		prID = req.PR.ID()
	}

	// Steps 8–10: Generate, reconcile, store.
	files := make(map[string][]byte)
	var regenerated []ast.Page

	for _, ap := range prioritized {
		page, rendered, genErr := o.generateAndReconcileOne(ctx, cfg, ap, req, prID)
		if genErr != nil {
			// Exclusion is logged but does not abort the push.
			continue
		}
		regenerated = append(regenerated, page)
		files[wikiFilePath(page.ID)] = rendered
	}

	// Step 11: Commit.
	commitMsg := fmt.Sprintf("wiki: incremental update (%s)", shortSHA(req.HeadSHA))
	prid, commitErr := o.commitIncrementalFiles(ctx, req, cfg, files, commitMsg, marks)
	if commitErr != nil {
		return IncrementalResult{}, commitErr
	}

	// Update PR description (best-effort; non-fatal on failure).
	if req.PR != nil && prid != "" {
		_ = o.updatePRDescription(ctx, req, cfg, repoID, prID, now)
	}

	// Step 12: Advance source_processed_sha.
	if advErr := req.WatermarkStore.AdvanceProcessed(ctx, repoID, req.HeadSHA); advErr != nil {
		return IncrementalResult{}, fmt.Errorf("orchestrator: advancing processed watermark: %w", advErr)
	}

	return IncrementalResult{
		Regenerated: regenerated,
		Queued:      queuedPageIDs(queued),
		PRID:        prid,
	}, nil
}

// commitIncrementalFiles appends files to the open PR branch or writes them
// via the ExtendedRepoWriter. Returns the PRID of the PR that received the commit.
func (o *Orchestrator) commitIncrementalFiles(
	ctx context.Context,
	req IncrementalRequest,
	cfg Config,
	files map[string][]byte,
	commitMsg string,
	marks Watermarks,
) (string, error) {
	if len(files) == 0 {
		if req.PR != nil {
			return req.PR.ID(), nil
		}
		return "", nil
	}

	if req.PR != nil {
		// Append to the existing open PR branch — never force-push.
		branch := req.PR.Branch()
		if appendErr := req.PR.AppendCommitToBranch(ctx, branch, files, commitMsg); appendErr != nil {
			return "", fmt.Errorf("orchestrator: appending commit to PR branch %q: %w", branch, appendErr)
		}
		return req.PR.ID(), nil
	}

	if req.Writer != nil {
		// No open PR — write to the branch via the writer.
		branch := cfg.PRBranch
		if branch == "" {
			branch = "sourcebridge/wiki-update"
		}
		title := cfg.PRTitle
		if title == "" {
			if marks.WikiPublishedSHA == "" {
				title = "wiki: initial generation (sourcebridge)"
			} else {
				title = commitMsg
			}
		}
		if appendErr := req.Writer.AppendCommitToBranch(ctx, branch, files, commitMsg); appendErr != nil {
			return "", fmt.Errorf("orchestrator: writing incremental files to branch %q: %w", branch, appendErr)
		}
		// The PRID is empty when we only wrote to the branch; the caller is
		// responsible for opening the PR via a separate WikiPR.Open call if needed.
		_ = title
		return "", nil
	}

	return "", fmt.Errorf("orchestrator: no PR and no ExtendedRepoWriter available to commit files")
}

// PromoteWithWatermark extends the base Promote to also advance wiki_published_sha.
// Pass a non-nil WatermarkStore and headSHA to update the watermark; passing nil
// skips the watermark update (used in P1 tests that do not have a watermark store).
func (o *Orchestrator) PromoteWithWatermark(ctx context.Context, repoID, prID, headSHA string, wm WatermarkStore) error {
	if err := o.store.PromoteProposed(ctx, repoID, prID); err != nil {
		return err
	}
	if wm != nil && headSHA != "" {
		return wm.AdvancePublished(ctx, repoID, headSHA)
	}
	return nil
}

// DiscardWithWatermark discards proposed pages and rolls back source_processed_sha
// to wiki_published_sha so the next push regenerates the rejected delta.
func (o *Orchestrator) DiscardWithWatermark(ctx context.Context, repoID, prID string, wm WatermarkStore) error {
	if err := o.store.DeleteProposed(ctx, repoID, prID); err != nil {
		return err
	}
	if wm != nil {
		marks, err := wm.Get(ctx, repoID)
		if err != nil {
			return fmt.Errorf("orchestrator: reading watermarks for rollback: %w", err)
		}
		// Roll source_processed_sha back to wiki_published_sha so the next push
		// regenerates the rejected delta from the published baseline.
		return wm.AdvanceProcessed(ctx, repoID, marks.WikiPublishedSHA)
	}
	return nil
}

// handleForcePush is the recovery path when the diff provider reports
// ErrSHANotFound (source repo history was rewritten). Both watermarks are
// reset and a full regen is triggered against the current HEAD.
func (o *Orchestrator) handleForcePush(ctx context.Context, req IncrementalRequest, cfg Config) (IncrementalResult, error) {
	repoID := cfg.RepoID

	// Reset both watermarks to empty — forces a regen from scratch next time.
	if resetErr := req.WatermarkStore.Reset(ctx, repoID, ""); resetErr != nil {
		return IncrementalResult{}, fmt.Errorf("orchestrator: resetting watermarks after force-push: %w", resetErr)
	}

	commitMsg := fmt.Sprintf("wiki: full regen after history rewrite (%s)", shortSHA(req.HeadSHA))

	// Treat every known page as affected.
	var allAffected []manifest.AffectedPage
	for _, m := range req.Pages {
		allAffected = append(allAffected, manifest.AffectedPage{Manifest: m, DirectHit: true})
	}
	prioritized, queued := applyPageBudget(allAffected, maxPagesPerPush)

	files := make(map[string][]byte)
	var regenerated []ast.Page
	for _, ap := range prioritized {
		page, rendered, genErr := o.generateAndReconcileOne(ctx, cfg, ap, req, "")
		if genErr != nil {
			continue
		}
		regenerated = append(regenerated, page)
		files[wikiFilePath(page.ID)] = rendered
	}

	// Use empty marks for the force-push case (no published baseline).
	prid, commitErr := o.commitIncrementalFiles(ctx, req, cfg, files, commitMsg, Watermarks{})
	if commitErr != nil {
		return IncrementalResult{}, commitErr
	}

	_ = req.WatermarkStore.AdvanceProcessed(ctx, repoID, req.HeadSHA)

	return IncrementalResult{
		Regenerated: regenerated,
		Queued:      queuedPageIDs(queued),
		ForcePush:   true,
		PRID:        prid,
	}, nil
}

// generateAndReconcileOne generates a single affected page and applies
// reviewer-commit reconciliation against the current proposed_ast.
func (o *Orchestrator) generateAndReconcileOne(
	ctx context.Context,
	cfg Config,
	ap manifest.AffectedPage,
	req IncrementalRequest,
	prID string,
) (ast.Page, []byte, error) {
	planned := PlannedPage{
		ID:         ap.Manifest.PageID,
		TemplateID: ap.Manifest.Template,
		Audience:   audienceFromString(ap.Manifest.Audience),
		Input:      buildInputFromManifest(ap.Manifest, cfg),
	}

	// Incremental regen: no Snapshot available at this call site; use
	// TierFrontier to preserve pre-CA-150 gate behavior for incremental
	// updates. CA-150 Phase 4 covers cold-start + on-demand only.
	outcome, err := o.generateOnePage(ctx, cfg, planned, modeltier.TierFrontier, nil)
	if err != nil {
		return ast.Page{}, nil, err
	}
	if outcome.excluded != nil {
		return ast.Page{}, nil, fmt.Errorf("page %q excluded after quality gates", ap.Manifest.PageID)
	}

	newPage := outcome.page

	// Apply reviewer-commit reconciliation: blocks owned by
	// OwnerHumanEditedOnPRBranch or OwnerHumanOnly in the stored proposed_ast
	// are preserved — the bot does not overwrite them.
	if prID != "" {
		existing, ok, storeErr := o.store.GetProposed(ctx, cfg.RepoID, prID, newPage.ID)
		if storeErr == nil && ok {
			newPage = reconcileWithHumanEdits(ctx, newPage, existing, req.PR)
		}
	} else {
		// No open PR: check canonical for human-edited blocks.
		existing, ok, storeErr := o.store.GetCanonical(ctx, cfg.RepoID, newPage.ID)
		if storeErr == nil && ok {
			newPage = reconcileWithHumanEdits(ctx, newPage, existing, req.PR)
		}
	}

	// Store as proposed_ast when we have a PR.
	if prID != "" {
		if storeErr := o.store.SetProposed(ctx, cfg.RepoID, prID, newPage); storeErr != nil {
			return ast.Page{}, nil, fmt.Errorf("storing proposed page %q: %w", newPage.ID, storeErr)
		}
	}

	rendered, renderErr := renderPage(newPage)
	if renderErr != nil {
		return ast.Page{}, nil, renderErr
	}

	return newPage, rendered, nil
}

// reconcileWithHumanEdits applies block-level reconciliation: blocks in
// newPage that correspond to OwnerHumanEditedOnPRBranch or OwnerHumanOnly
// blocks in existingPage are replaced with the existing content.
//
// When a stale_when condition fires on a human-edited-on-pr-branch block (the
// generated replacement has a structurally different Kind), a comment is posted
// to the PR via pr.PostComment and the block is left alone.
func reconcileWithHumanEdits(ctx context.Context, newPage, existingPage ast.Page, pr ExtendedWikiPR) ast.Page {
	if len(existingPage.Blocks) == 0 {
		return newPage
	}

	existingByID := make(map[ast.BlockID]ast.Block, len(existingPage.Blocks))
	for _, blk := range existingPage.Blocks {
		existingByID[blk.ID] = blk
	}

	resultBlocks := make([]ast.Block, len(newPage.Blocks))
	for i, blk := range newPage.Blocks {
		existing, found := existingByID[blk.ID]
		if !found {
			// New block not present in the existing page — take as-is.
			resultBlocks[i] = blk
			continue
		}

		switch existing.Owner {
		case ast.OwnerHumanEditedOnPRBranch:
			// Check structural staleness: if the bot's replacement has a different
			// Kind the content is fundamentally invalid — post a comment and leave it.
			if isBlockStale(existing, blk) && pr != nil {
				comment := buildStaleBlockComment(existing)
				_ = pr.PostComment(ctx, comment) // best-effort; non-fatal
			}
			// In all cases: preserve the human-edited block.
			resultBlocks[i] = existing

		case ast.OwnerHumanOnly:
			// Never touch human-only blocks.
			resultBlocks[i] = existing

		default:
			// OwnerGenerated or OwnerHumanEdited (canonical) — take the newly
			// generated version.
			resultBlocks[i] = blk
		}
	}

	return ast.Page{
		ID:         newPage.ID,
		Manifest:   newPage.Manifest,
		Blocks:     resultBlocks,
		Provenance: newPage.Provenance,
	}
}

// ApplyReviewerCommits detects human commits on the wiki PR branch and marks
// the affected blocks as OwnerHumanEditedOnPRBranch in proposed_ast.
//
// For each commit not authored by the SourceBridge bot, the changed wiki files
// are parsed to extract block IDs, and those blocks' ownership is updated in
// the stored proposed_ast.
func (o *Orchestrator) ApplyReviewerCommits(ctx context.Context, repoID, prID string, commits []Commit) error {
	for _, commit := range commits {
		if commit.IsBotCommit() {
			continue
		}

		for filePath, content := range commit.Files {
			if content == nil {
				continue // deleted file
			}
			if !isWikiFile(filePath) {
				continue
			}

			pageID := pageIDFromWikiPath(filePath)
			if pageID == "" {
				continue
			}

			changedBlockIDs, parseErr := parseChangedBlockIDs(content)
			if parseErr != nil {
				continue // parse failure is not fatal
			}
			if len(changedBlockIDs) == 0 {
				// No block IDs found — mark all blocks in the page as human-edited.
				// This handles the case where a reviewer rewrites the file without
				// block ID markers.
				if allErr := o.markAllBlocksHumanEdited(ctx, repoID, prID, pageID, commit); allErr != nil {
					return allErr
				}
				continue
			}

			page, ok, err := o.store.GetProposed(ctx, repoID, prID, pageID)
			if err != nil || !ok {
				continue
			}

			updated := false
			for i, blk := range page.Blocks {
				if changedBlockIDs[blk.ID] {
					page.Blocks[i].Owner = ast.OwnerHumanEditedOnPRBranch
					page.Blocks[i].LastChange = ast.BlockChange{
						SHA:       commit.SHA,
						Timestamp: time.Now(),
						Source:    "human-pr-branch",
					}
					updated = true
				}
			}

			if updated {
				if storeErr := o.store.SetProposed(ctx, repoID, prID, page); storeErr != nil {
					return fmt.Errorf("orchestrator: updating proposed page after reviewer commit: %w", storeErr)
				}
			}
		}
	}
	return nil
}

// markAllBlocksHumanEdited marks every block in the given proposed page as
// OwnerHumanEditedOnPRBranch. Used when a reviewer rewrites a wiki file
// without block ID markers (e.g. a full rewrite via the GitHub web editor).
func (o *Orchestrator) markAllBlocksHumanEdited(ctx context.Context, repoID, prID, pageID string, commit Commit) error {
	page, ok, err := o.store.GetProposed(ctx, repoID, prID, pageID)
	if err != nil || !ok {
		return nil // page not in proposed — nothing to mark
	}
	for i := range page.Blocks {
		page.Blocks[i].Owner = ast.OwnerHumanEditedOnPRBranch
		page.Blocks[i].LastChange = ast.BlockChange{
			SHA:       commit.SHA,
			Timestamp: time.Now(),
			Source:    "human-pr-branch",
		}
	}
	return o.store.SetProposed(ctx, repoID, prID, page)
}

// updatePRDescription updates the PR description with the current bot/human
// commit statistics and block ownership summary.
func (o *Orchestrator) updatePRDescription(
	ctx context.Context,
	req IncrementalRequest,
	cfg Config,
	repoID, prID string,
	since time.Time,
) error {
	if req.PR == nil {
		return nil
	}

	// Look back 30 days to capture all commits.
	lookback := since.Add(-30 * 24 * time.Hour)
	commits, err := req.PR.ListCommitsOnBranch(ctx, req.PR.Branch(), lookback)
	if err != nil {
		return fmt.Errorf("listing commits: %w", err)
	}

	var botCount, humanCount int
	for _, c := range commits {
		if c.IsBotCommit() {
			botCount++
		} else {
			humanCount++
		}
	}

	pages, listErr := o.store.ListProposed(ctx, repoID, prID)
	if listErr != nil {
		return fmt.Errorf("listing proposed pages: %w", listErr)
	}

	var botBlocks, humanBlocks []string
	for _, page := range pages {
		for _, blk := range page.Blocks {
			desc := fmt.Sprintf("`%s.%s`", page.ID, blk.ID)
			switch blk.Owner {
			case ast.OwnerHumanEditedOnPRBranch:
				humanBlocks = append(humanBlocks, desc)
			case ast.OwnerGenerated:
				botBlocks = append(botBlocks, desc)
			}
		}
	}

	body := buildIncrementalPRBody(cfg, botCount, humanCount, botBlocks, humanBlocks)
	return req.PR.UpdateDescription(ctx, body)
}

// ---- helpers ----

// applyPageBudget splits affected pages into the top N by priority and the
// remainder (queued). Priority: direct hit > graph hit > transitive hit.
func applyPageBudget(affected []manifest.AffectedPage, budget int) (prioritized, queued []manifest.AffectedPage) {
	sorted := make([]manifest.AffectedPage, len(affected))
	copy(sorted, affected)
	sort.SliceStable(sorted, func(i, j int) bool {
		return pageScore(sorted[i]) > pageScore(sorted[j])
	})

	if len(sorted) <= budget {
		return sorted, nil
	}
	return sorted[:budget], sorted[budget:]
}

// pageScore returns a priority score for sorting affected pages.
func pageScore(ap manifest.AffectedPage) int {
	switch {
	case ap.DirectHit:
		return 3
	case ap.GraphHit:
		return 2
	case ap.TransitiveHit:
		return 1
	}
	return 0
}

// queuedPageIDs extracts the page IDs from a slice of AffectedPage.
func queuedPageIDs(queued []manifest.AffectedPage) []string {
	if len(queued) == 0 {
		return nil
	}
	ids := make([]string, len(queued))
	for i, ap := range queued {
		ids[i] = ap.Manifest.PageID
	}
	return ids
}

// shortSHA returns the first 8 characters of a SHA.
func shortSHA(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}

// isWikiFile reports whether a file path is a wiki markdown file.
func isWikiFile(path string) bool {
	return strings.HasPrefix(path, "wiki/") && strings.HasSuffix(path, ".md")
}

// pageIDFromWikiPath derives the page ID from a wiki file path.
// "wiki/arch.auth.md" → "arch.auth"
func pageIDFromWikiPath(path string) string {
	if !isWikiFile(path) {
		return ""
	}
	s := path[len("wiki/"):]
	s = strings.TrimSuffix(s, ".md")
	return s
}

// parseChangedBlockIDs parses a rendered wiki markdown file and returns the
// set of block IDs embedded as HTML comments by the markdown writer.
// Format: <!-- sourcebridge:block id="bXXXXXXXXXXXX" -->
func parseChangedBlockIDs(content []byte) (map[ast.BlockID]bool, error) {
	ids := make(map[ast.BlockID]bool)
	s := string(content)
	const marker = `<!-- sourcebridge:block id="`
	const markerEnd = `" -->`
	for {
		idx := strings.Index(s, marker)
		if idx < 0 {
			break
		}
		rest := s[idx+len(marker):]
		end := strings.Index(rest, markerEnd)
		if end < 0 {
			break
		}
		id := rest[:end]
		if id != "" {
			ids[ast.BlockID(id)] = true
		}
		s = rest[end+len(markerEnd):]
	}
	return ids, nil
}

// isBlockStale returns true when a human-edited block appears to be
// fundamentally invalidated by the newly generated content. We use a
// conservative structural check: stale only when the replacement has a
// different Kind (e.g. the doc section changed from a paragraph to a code block).
func isBlockStale(humanBlock, generatedBlock ast.Block) bool {
	return humanBlock.Kind != generatedBlock.Kind
}

// buildStaleBlockComment builds the PR comment posted when a human-edited-on-pr-branch
// block is structurally stale but cannot be auto-updated.
func buildStaleBlockComment(blk ast.Block) string {
	var sb bytes.Buffer
	fmt.Fprintf(&sb, "**SourceBridge: human-edited block may be stale**\n\n")
	fmt.Fprintf(&sb, "Block `%s` was edited by a reviewer on this PR branch ", blk.ID)
	fmt.Fprintf(&sb, "but the source code it documents has changed significantly. ")
	fmt.Fprintf(&sb, "SourceBridge cannot update this block automatically because it was ")
	fmt.Fprintf(&sb, "marked `%s`.\n\n", blk.Owner)
	fmt.Fprintf(&sb, "Please review this block and update it manually, or mark it as ")
	fmt.Fprintf(&sb, "`generated` to allow SourceBridge to regenerate it on the next push.")
	return sb.String()
}

// buildIncrementalPRBody builds the PR description body for an incremental
// update, summarising bot and human commit counts and block ownership.
func buildIncrementalPRBody(_ Config, botCommits, humanCommits int, botBlocks, humanBlocks []string) string {
	var sb bytes.Buffer
	fmt.Fprintf(&sb, "## SourceBridge Wiki — Incremental Update\n\n")
	fmt.Fprintf(&sb, "This PR was updated automatically by [SourceBridge](https://sourcebridge.ai).\n\n")
	fmt.Fprintf(&sb, "> Squash this PR on merge to keep wiki history clean. ")
	fmt.Fprintf(&sb, "SourceBridge will pick up the squashed commit on next regen.\n\n")
	fmt.Fprintf(&sb, "### Commit summary\n\n")
	fmt.Fprintf(&sb, "- **%d** commits by SourceBridge\n", botCommits)
	fmt.Fprintf(&sb, "- **%d** commits by human reviewers\n\n", humanCommits)
	if len(humanBlocks) > 0 {
		fmt.Fprintf(&sb, "### Blocks owned by humans (%d)\n\n", len(humanBlocks))
		for _, b := range humanBlocks {
			fmt.Fprintf(&sb, "- %s\n", b)
		}
		fmt.Fprintf(&sb, "\n")
	}
	if len(botBlocks) > 0 {
		fmt.Fprintf(&sb, "### Blocks owned by SourceBridge (%d)\n\n", len(botBlocks))
		for _, b := range botBlocks {
			fmt.Fprintf(&sb, "- %s\n", b)
		}
	}
	return sb.String()
}

// buildInputFromManifest builds a GenerateInput from a manifest for use in
// the incremental regeneration path. LLM and SymbolGraph are intentionally nil
// here — callers that need them should inject them via Config or a resolver.
func buildInputFromManifest(m manifest.DependencyManifest, cfg Config) templates.GenerateInput {
	return templates.GenerateInput{
		RepoID:   cfg.RepoID,
		Audience: audienceFromString(m.Audience),
		Now:      time.Now(),
	}
}

// audienceFromString converts a manifest audience string to a quality.Audience.
func audienceFromString(s string) quality.Audience {
	switch s {
	case "for-product":
		return quality.AudienceProduct
	case "for-operators":
		return quality.AudienceOperators
	default:
		return quality.AudienceEngineers
	}
}
