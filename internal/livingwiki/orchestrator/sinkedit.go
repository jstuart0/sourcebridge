// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// This file implements the A1.P6 post-merge canonical-state reconciliation flow:
//
//   - [SinkPoller] / [SinkEdit] / [MemorySinkPoller] — sink-edit detection ports.
//   - [SinkOverlayStore] / [MemorySinkOverlayStore] — per-sink overlay persistence.
//   - [Orchestrator.HandleSinkEdit] — dispatches to the correct policy branch.
//   - [Orchestrator.PollAndReconcile] — polls a sink and dispatches each detected edit.
//   - [Orchestrator.OpenSyncPR] — opens a sync-PR for require_review_before_promote edits.
//   - [Orchestrator.HandleSyncPRDecision] — resolves a sync-PR and updates state.
//   - [Orchestrator.RegenerateForSink] — queues regen of a sink after a canonical promotion.

package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/governance"
)

// ─────────────────────────────────────────────────────────────────────────────
// Sink-edit detection ports
// ─────────────────────────────────────────────────────────────────────────────

// SinkEdit is one detected change in a sink. Returned by [SinkPoller.Poll].
type SinkEdit struct {
	// SinkName is the integration ID of the sink (e.g. "confluence-acme-space").
	SinkName ast.SinkName

	// BlockID is the stable block ID that changed in the sink.
	BlockID ast.BlockID

	// NewContent is the new content of the block as read from the sink.
	NewContent ast.BlockContent

	// EditedBy is the sink's user identifier for the person who made the edit.
	EditedBy string

	// EditedAt is when the edit was observed in the sink.
	EditedAt time.Time
}

// SinkPoller detects edits that occurred in a sink since the last poll.
// The actual detection mechanism (HTTP poll, webhook, ETag comparison) is
// sink-specific. Implementations are injected via [PollAndReconcile].
// Real Confluence/Notion implementations are deferred; tests use [MemorySinkPoller].
type SinkPoller interface {
	// Poll returns all edits detected in the sink since the last successful Poll
	// call. Implementations are responsible for tracking the "last polled" cursor.
	// An empty slice means no new edits were detected.
	Poll(ctx context.Context, sinkName ast.SinkName) ([]SinkEdit, error)
}

// MemorySinkPoller is an in-memory [SinkPoller] for tests.
// Callers enqueue edits via [MemorySinkPoller.Enqueue]; [Poll] drains them.
type MemorySinkPoller struct {
	mu    sync.Mutex
	queue map[ast.SinkName][]SinkEdit
}

// NewMemorySinkPoller creates an empty in-memory sink poller.
func NewMemorySinkPoller() *MemorySinkPoller {
	return &MemorySinkPoller{queue: make(map[ast.SinkName][]SinkEdit)}
}

// Compile-time interface check.
var _ SinkPoller = (*MemorySinkPoller)(nil)

// Enqueue adds an edit to the poller's queue for the given sink.
func (m *MemorySinkPoller) Enqueue(edit SinkEdit) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue[edit.SinkName] = append(m.queue[edit.SinkName], edit)
}

// Poll drains and returns all queued edits for the given sink. Subsequent
// calls return an empty slice until more edits are enqueued.
func (m *MemorySinkPoller) Poll(_ context.Context, sinkName ast.SinkName) ([]SinkEdit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	edits := m.queue[sinkName]
	m.queue[sinkName] = nil
	return edits, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Sink overlay persistence
// ─────────────────────────────────────────────────────────────────────────────

// SinkOverlayStore persists and retrieves per-sink block overlays.
// Implementations must be safe for concurrent use.
type SinkOverlayStore interface {
	// GetOverlay returns the overlay for the given sink + page.
	// Returns (SinkOverlay{}, false, nil) when no overlay exists.
	GetOverlay(ctx context.Context, repoID string, sinkName ast.SinkName, pageID string) (ast.SinkOverlay, bool, error)

	// SetOverlay persists an overlay.
	SetOverlay(ctx context.Context, repoID string, overlay ast.SinkOverlay) error

	// DeleteOverlay removes an overlay. Used when a sync-PR is force-overwritten
	// or when a sink integration is disconnected.
	DeleteOverlay(ctx context.Context, repoID string, sinkName ast.SinkName, pageID string) error
}

// MemorySinkOverlayStore is an in-memory [SinkOverlayStore] for tests.
type MemorySinkOverlayStore struct {
	mu      sync.RWMutex
	entries map[string]ast.SinkOverlay // key: repoID + "/" + sinkName + "/" + pageID
}

// NewMemorySinkOverlayStore creates an empty in-memory overlay store.
func NewMemorySinkOverlayStore() *MemorySinkOverlayStore {
	return &MemorySinkOverlayStore{entries: make(map[string]ast.SinkOverlay)}
}

// Compile-time interface check.
var _ SinkOverlayStore = (*MemorySinkOverlayStore)(nil)

func (m *MemorySinkOverlayStore) GetOverlay(_ context.Context, repoID string, sinkName ast.SinkName, pageID string) (ast.SinkOverlay, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	o, ok := m.entries[overlayKey(repoID, sinkName, pageID)]
	return o, ok, nil
}

func (m *MemorySinkOverlayStore) SetOverlay(_ context.Context, repoID string, overlay ast.SinkOverlay) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[overlayKey(repoID, overlay.SinkName, overlay.PageID)] = overlay
	return nil
}

func (m *MemorySinkOverlayStore) DeleteOverlay(_ context.Context, repoID string, sinkName ast.SinkName, pageID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, overlayKey(repoID, sinkName, pageID))
	return nil
}

func overlayKey(repoID string, sinkName ast.SinkName, pageID string) string {
	return repoID + "/" + string(sinkName) + "/" + pageID
}

// ─────────────────────────────────────────────────────────────────────────────
// Sync-PR tracking
// ─────────────────────────────────────────────────────────────────────────────

// SyncPRRecord tracks an open sync-PR for a block edit awaiting review.
type SyncPRRecord struct {
	// PRID is the PR identifier returned by WikiPR.Open.
	PRID string
	// RepoID is the repository this sync-PR belongs to.
	RepoID string
	// SinkName is the sink where the edit originated.
	SinkName ast.SinkName
	// PageID is the page that contains the edited block.
	PageID string
	// BlockID is the block under review.
	BlockID ast.BlockID
	// SinkUser is the sink user who made the edit.
	SinkUser string
	// OpenedAt is when the sync-PR was opened.
	OpenedAt time.Time
}

// SyncPRStore persists open sync-PR records indexed by PR ID.
// Implementations must be safe for concurrent use.
type SyncPRStore interface {
	// Set stores a sync-PR record.
	Set(ctx context.Context, record SyncPRRecord) error
	// Get returns the record for a PR ID, or (zero, false, nil) when not found.
	Get(ctx context.Context, prID string) (SyncPRRecord, bool, error)
	// Delete removes a resolved sync-PR record.
	Delete(ctx context.Context, prID string) error
}

// MemorySyncPRStore is an in-memory [SyncPRStore] for tests.
type MemorySyncPRStore struct {
	mu      sync.RWMutex
	entries map[string]SyncPRRecord
}

// NewMemorySyncPRStore creates an empty in-memory sync-PR store.
func NewMemorySyncPRStore() *MemorySyncPRStore {
	return &MemorySyncPRStore{entries: make(map[string]SyncPRRecord)}
}

// Compile-time interface check.
var _ SyncPRStore = (*MemorySyncPRStore)(nil)

func (m *MemorySyncPRStore) Set(_ context.Context, r SyncPRRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[r.PRID] = r
	return nil
}

func (m *MemorySyncPRStore) Get(_ context.Context, prID string) (SyncPRRecord, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.entries[prID]
	return r, ok, nil
}

func (m *MemorySyncPRStore) Delete(_ context.Context, prID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, prID)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// SinkEditOrchestrator — extended orchestrator state for A1.P6
// ─────────────────────────────────────────────────────────────────────────────

// SinkEditConfig carries the A1.P6 dependencies that are injected separately
// from the base [Config] to avoid bloating the existing type.
type SinkEditConfig struct {
	// AuditLog receives governance events. Required.
	AuditLog governance.AuditLog

	// OverlayStore persists sink overlays. Required.
	OverlayStore SinkOverlayStore

	// SyncPRs tracks open sync-PRs. Required for require_review_before_promote.
	SyncPRs SyncPRStore

	// SinkConfigs maps sink names to their governance configuration.
	// HandleSinkEdit uses this to resolve the effective policy.
	SinkConfigs map[ast.SinkName]governance.SinkConfig

	// SyncPROpener is used by OpenSyncPR to open a PR in the source repo.
	// If nil, OpenSyncPR returns an error.
	SyncPROpener WikiPR
}

// HandleSinkEdit is the entry point called when sink-edit detection (e.g.
// polling Confluence) reveals that a block changed in a sink.
//
// It resolves the sink's effective policy from SinkEditConfig.SinkConfigs and
// dispatches accordingly:
//
//   - promote_to_canonical: promotes the edit to canonical_ast via
//     governance.PromoteToCanonical, then queues regen of the sink's peers.
//   - local_to_sink: writes the edit to the sink's SinkOverlay. Canonical
//     is unchanged. Sets SinkDivergence[sink] = true on the canonical block.
//   - require_review_before_promote: opens a sync-PR via OpenSyncPR with the
//     proposed change; on merge the caller invokes HandleSyncPRDecision.
//
// The method returns the updated canonical page (unchanged unless the policy
// is promote_to_canonical).
func (o *Orchestrator) HandleSinkEdit(
	ctx context.Context,
	secfg SinkEditConfig,
	repoID string,
	pageID string,
	edit SinkEdit,
) (ast.Page, error) {
	if secfg.AuditLog == nil {
		return ast.Page{}, fmt.Errorf("HandleSinkEdit: AuditLog must be non-nil")
	}
	if secfg.OverlayStore == nil {
		return ast.Page{}, fmt.Errorf("HandleSinkEdit: OverlayStore must be non-nil")
	}

	// Resolve effective policy.
	sinkCfg, hasCfg := secfg.SinkConfigs[edit.SinkName]
	if !hasCfg {
		// Unknown sink — default to local_to_sink (safest).
		sinkCfg = governance.NewSinkConfig(governance.SinkKindConfluence, edit.SinkName)
	}
	// Per-block override takes precedence over sink policy.
	policy := sinkCfg.EffectivePolicy()

	// Load the current canonical page.
	canonical, ok, err := o.store.GetCanonical(ctx, repoID, pageID)
	if err != nil {
		return ast.Page{}, fmt.Errorf("HandleSinkEdit: loading canonical page %q: %w", pageID, err)
	}
	if !ok {
		return ast.Page{}, fmt.Errorf("HandleSinkEdit: canonical page %q not found", pageID)
	}

	switch policy {
	case governance.EditPolicyPromoteToCanonical:
		updated, _, err := governance.PromoteToCanonical(
			ctx, canonical, edit.SinkName, edit.EditedBy, edit.BlockID, edit.NewContent, secfg.AuditLog,
		)
		if err != nil {
			return canonical, fmt.Errorf("HandleSinkEdit: promote_to_canonical: %w", err)
		}
		if storeErr := o.store.SetCanonical(ctx, repoID, updated); storeErr != nil {
			return canonical, fmt.Errorf("HandleSinkEdit: storing updated canonical: %w", storeErr)
		}
		// Queue regen of other sinks (best-effort; non-fatal on error).
		_ = o.RegenerateForSink(ctx, repoID, edit.SinkName)
		return updated, nil

	case governance.EditPolicyLocalToSink:
		// Write to overlay; leave canonical unchanged.
		overlay, _, overlayErr := secfg.OverlayStore.GetOverlay(ctx, repoID, edit.SinkName, pageID)
		if overlayErr != nil {
			return canonical, fmt.Errorf("HandleSinkEdit: loading overlay: %w", overlayErr)
		}
		if overlay.Blocks == nil {
			overlay = ast.SinkOverlay{
				SinkName:   edit.SinkName,
				PageID:     pageID,
				Blocks:     make(map[ast.BlockID]ast.BlockContent),
				Provenance: make(map[ast.BlockID]ast.OverlayMeta),
			}
		}
		overlay.Blocks[edit.BlockID] = edit.NewContent
		overlay.Provenance[edit.BlockID] = ast.OverlayMeta{
			EditedBy: edit.EditedBy,
			EditedAt: edit.EditedAt,
		}
		if storeErr := secfg.OverlayStore.SetOverlay(ctx, repoID, overlay); storeErr != nil {
			return canonical, fmt.Errorf("HandleSinkEdit: storing overlay: %w", storeErr)
		}
		// Set SinkDivergence on canonical block (in-memory copy returned to caller).
		updated := setDivergenceFlag(canonical, edit.BlockID, edit.SinkName, true)
		if storeErr := o.store.SetCanonical(ctx, repoID, updated); storeErr != nil {
			return canonical, fmt.Errorf("HandleSinkEdit: storing divergence flag: %w", storeErr)
		}
		return updated, nil

	case governance.EditPolicyRequireReviewBeforePromote:
		if secfg.SyncPRs == nil {
			return canonical, fmt.Errorf("HandleSinkEdit: SyncPRStore must be non-nil for require_review_before_promote")
		}
		// Stage the edit in the overlay so we can show the proposed content while
		// the sync-PR is pending; the overlay is cleaned up by HandleSyncPRDecision.
		overlay, _, overlayErr := secfg.OverlayStore.GetOverlay(ctx, repoID, edit.SinkName, pageID)
		if overlayErr != nil {
			return canonical, fmt.Errorf("HandleSinkEdit: loading overlay for sync-PR: %w", overlayErr)
		}
		if overlay.Blocks == nil {
			overlay = ast.SinkOverlay{
				SinkName:   edit.SinkName,
				PageID:     pageID,
				Blocks:     make(map[ast.BlockID]ast.BlockContent),
				Provenance: make(map[ast.BlockID]ast.OverlayMeta),
			}
		}
		overlay.Blocks[edit.BlockID] = edit.NewContent
		overlay.Provenance[edit.BlockID] = ast.OverlayMeta{
			EditedBy: edit.EditedBy,
			EditedAt: edit.EditedAt,
		}
		if storeErr := secfg.OverlayStore.SetOverlay(ctx, repoID, overlay); storeErr != nil {
			return canonical, fmt.Errorf("HandleSinkEdit: storing overlay for sync-PR: %w", storeErr)
		}
		// Open the sync-PR.
		prID, openErr := o.OpenSyncPR(ctx, secfg, repoID, edit.SinkName, edit.BlockID, edit.NewContent)
		if openErr != nil {
			return canonical, fmt.Errorf("HandleSinkEdit: opening sync-PR: %w", openErr)
		}
		// Persist the sync-PR record so HandleSyncPRDecision can look it up.
		record := SyncPRRecord{
			PRID:     prID,
			RepoID:   repoID,
			SinkName: edit.SinkName,
			PageID:   pageID,
			BlockID:  edit.BlockID,
			SinkUser: edit.EditedBy,
			OpenedAt: time.Now(),
		}
		if setErr := secfg.SyncPRs.Set(ctx, record); setErr != nil {
			return canonical, fmt.Errorf("HandleSinkEdit: persisting sync-PR record: %w", setErr)
		}
		return canonical, nil

	default:
		return canonical, fmt.Errorf("HandleSinkEdit: policy %v not applicable for sink edits", policy)
	}
}

// setDivergenceFlag returns a copy of canonical with SinkDivergence[sinkName] set
// to diverged on the block with the given ID. A no-op when the block is not found.
func setDivergenceFlag(canonical ast.Page, blockID ast.BlockID, sinkName ast.SinkName, diverged bool) ast.Page {
	updated := copyPageShallow(canonical)
	for i, blk := range updated.Blocks {
		if blk.ID != blockID {
			continue
		}
		if updated.Blocks[i].SinkDivergence == nil {
			updated.Blocks[i].SinkDivergence = make(map[ast.SinkName]bool)
		}
		if diverged {
			updated.Blocks[i].SinkDivergence[sinkName] = true
		} else {
			delete(updated.Blocks[i].SinkDivergence, sinkName)
		}
		return updated
	}
	return updated
}

// copyPageShallow returns a shallow copy of p with a fresh Blocks slice.
func copyPageShallow(p ast.Page) ast.Page {
	out := p
	out.Blocks = make([]ast.Block, len(p.Blocks))
	copy(out.Blocks, p.Blocks)
	return out
}

// PollAndReconcile polls a sink for new edits and dispatches each one to
// HandleSinkEdit. It returns the first error encountered (if any); prior
// edits that were successfully handled are not rolled back.
func (o *Orchestrator) PollAndReconcile(
	ctx context.Context,
	secfg SinkEditConfig,
	repoID string,
	pageID string,
	sinkName ast.SinkName,
	poller SinkPoller,
) error {
	edits, err := poller.Poll(ctx, sinkName)
	if err != nil {
		return fmt.Errorf("PollAndReconcile: polling sink %q: %w", sinkName, err)
	}
	for _, edit := range edits {
		if _, dispatchErr := o.HandleSinkEdit(ctx, secfg, repoID, pageID, edit); dispatchErr != nil {
			return fmt.Errorf("PollAndReconcile: handling edit on block %q: %w", edit.BlockID, dispatchErr)
		}
	}
	return nil
}

// RegenerateForSink is a hook called after a promote_to_canonical edit to
// trigger regen of other sinks that should pick up the new canonical content.
// The concrete implementation (scheduling an incremental regen job) is deferred
// to the deployment layer; this method is a no-op placeholder that allows tests
// to verify the call site without requiring a full regen setup.
//
// Callers that need regen to actually run should wrap Orchestrator with a
// scheduler that overrides this behaviour.
func (o *Orchestrator) RegenerateForSink(_ context.Context, _ string, _ ast.SinkName) error {
	// Deferred: production implementation schedules a GenerateIncremental call
	// for all other active sinks of the repo. No-op here keeps the interface
	// callable without a full orchestrator setup.
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Sync-PR mechanism
// ─────────────────────────────────────────────────────────────────────────────

// OpenSyncPR opens a sync-PR for a proposed block edit awaiting engineer review.
// The PR title follows the convention: "wiki: sync edit from <sinkName> (block <blockID>)".
// The PR body contains the proposed content for reviewer inspection.
//
// Returns the PR identifier (as returned by WikiPR.ID()) and any error.
func (o *Orchestrator) OpenSyncPR(
	ctx context.Context,
	secfg SinkEditConfig,
	repoID string,
	sinkName ast.SinkName,
	blockID ast.BlockID,
	newContent ast.BlockContent,
) (string, error) {
	if secfg.SyncPROpener == nil {
		return "", fmt.Errorf("OpenSyncPR: SyncPROpener must be non-nil")
	}

	title := fmt.Sprintf("wiki: sync edit from %s (block %s)", sinkName, blockID)
	body := buildSyncPRBody(sinkName, blockID, newContent)
	branch := fmt.Sprintf("sourcebridge/sync/%s-%s", sanitizeForBranch(string(sinkName)), string(blockID))

	if err := secfg.SyncPROpener.Open(ctx, branch, title, body, nil); err != nil {
		return "", fmt.Errorf("OpenSyncPR: %w", err)
	}
	return secfg.SyncPROpener.ID(), nil
}

// HandleSyncPRDecision applies the outcome of an engineer's review of a sync-PR.
// It wraps governance.ResolveSyncPR and persists the updated canonical and overlay.
//
// decision controls the outcome:
//   - SyncPRDecisionMerge: edit promoted to canonical; overlay cleared.
//   - SyncPRDecisionReject: overlay preserved as local_to_sink; canonical unchanged.
//   - SyncPRDecisionForceOverwrite: overlay cleared; canonical unchanged.
func (o *Orchestrator) HandleSyncPRDecision(
	ctx context.Context,
	secfg SinkEditConfig,
	prID string,
	decision governance.SyncPRDecision,
	reviewer string,
) error {
	if secfg.SyncPRs == nil {
		return fmt.Errorf("HandleSyncPRDecision: SyncPRStore must be non-nil")
	}
	if secfg.OverlayStore == nil {
		return fmt.Errorf("HandleSyncPRDecision: OverlayStore must be non-nil")
	}
	if secfg.AuditLog == nil {
		return fmt.Errorf("HandleSyncPRDecision: AuditLog must be non-nil")
	}

	record, ok, err := secfg.SyncPRs.Get(ctx, prID)
	if err != nil {
		return fmt.Errorf("HandleSyncPRDecision: loading sync-PR record: %w", err)
	}
	if !ok {
		return fmt.Errorf("HandleSyncPRDecision: sync-PR %q not found", prID)
	}

	canonical, canonOK, canonErr := o.store.GetCanonical(ctx, record.RepoID, record.PageID)
	if canonErr != nil {
		return fmt.Errorf("HandleSyncPRDecision: loading canonical page: %w", canonErr)
	}
	if !canonOK {
		return fmt.Errorf("HandleSyncPRDecision: canonical page %q not found", record.PageID)
	}

	overlay, _, overlayErr := secfg.OverlayStore.GetOverlay(ctx, record.RepoID, record.SinkName, record.PageID)
	if overlayErr != nil {
		return fmt.Errorf("HandleSyncPRDecision: loading overlay: %w", overlayErr)
	}
	if overlay.Blocks == nil {
		// No overlay — build a minimal one so ResolveSyncPR has something to work with.
		// This can happen if the overlay was cleared between OpenSyncPR and the decision.
		return fmt.Errorf("HandleSyncPRDecision: overlay for %q/%q is empty (PR may be stale)", record.SinkName, record.PageID)
	}

	newCanonical, newOverlay, resolveErr := governance.ResolveSyncPR(
		ctx,
		canonical,
		overlay,
		record.SinkName,
		record.SinkUser,
		record.BlockID,
		decision,
		reviewer,
		secfg.AuditLog,
	)
	if resolveErr != nil {
		return fmt.Errorf("HandleSyncPRDecision: resolving sync-PR: %w", resolveErr)
	}

	// Persist updated canonical.
	if storeErr := o.store.SetCanonical(ctx, record.RepoID, newCanonical); storeErr != nil {
		return fmt.Errorf("HandleSyncPRDecision: storing canonical: %w", storeErr)
	}

	// Persist updated overlay (may be empty after merge/force-overwrite).
	if len(newOverlay.Blocks) == 0 {
		if delErr := secfg.OverlayStore.DeleteOverlay(ctx, record.RepoID, record.SinkName, record.PageID); delErr != nil {
			return fmt.Errorf("HandleSyncPRDecision: deleting overlay: %w", delErr)
		}
	} else {
		if storeErr := secfg.OverlayStore.SetOverlay(ctx, record.RepoID, newOverlay); storeErr != nil {
			return fmt.Errorf("HandleSyncPRDecision: storing overlay: %w", storeErr)
		}
	}

	// Clean up the sync-PR record.
	if delErr := secfg.SyncPRs.Delete(ctx, prID); delErr != nil {
		return fmt.Errorf("HandleSyncPRDecision: deleting sync-PR record: %w", delErr)
	}

	return nil
}

// buildSyncPRBody generates the PR body for a sync-PR.
func buildSyncPRBody(sinkName ast.SinkName, blockID ast.BlockID, newContent ast.BlockContent) string {
	body := fmt.Sprintf(
		"## SourceBridge Wiki — Sync PR\n\n"+
			"An edit was made to block `%s` in sink **%s** and requires review "+
			"before it can be promoted to canonical.\n\n"+
			"### Proposed content\n\n",
		blockID, sinkName,
	)
	if newContent.Paragraph != nil {
		body += newContent.Paragraph.Markdown
	} else if newContent.Code != nil {
		body += "```" + newContent.Code.Language + "\n" + newContent.Code.Body + "\n```"
	} else {
		body += "_[non-text block; review in the sink directly]_"
	}
	body += "\n\n---\n\n_Merge to promote to canonical. Close without merging to keep this edit local to the sink._"
	return body
}

// sanitizeForBranch replaces characters that are not valid in git branch names
// with hyphens.
func sanitizeForBranch(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			out[i] = c
		} else {
			out[i] = '-'
		}
	}
	return string(out)
}
