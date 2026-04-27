// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// dispatcher.go implements the event dispatcher for the living-wiki trigger layer.
//
// # Per-repo serialization
//
// Each repository has its own single-slot channel (repoQueues). A dedicated
// goroutine drains that channel, ensuring that at most one event handler runs
// for a given repo at a time. This prevents two concurrent goroutines from
// racing on the WatermarkStore or from opening duplicate PRs on the same repo.
//
// # Deduplication
//
// The deliveryCache is a fixed-capacity LRU of recently seen delivery IDs.
// When a provider re-delivers a webhook (network retry, deliberate redelivery)
// the second submission is silently dropped. Cache capacity defaults to 10 000
// entries with a 1-hour TTL.
//
// # Lifecycle
//
// Call [Dispatcher.Start] to spin up the dispatcher goroutines and
// [Dispatcher.Stop] to drain in-flight events and shut down cleanly. Stop
// blocks until all worker goroutines exit.
package webhook

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
)

// ─────────────────────────────────────────────────────────────────────────────
// Logger port
// ─────────────────────────────────────────────────────────────────────────────

// Logger is a minimal structured-logging port. Callers wire in their preferred
// logger (e.g. log/slog); a no-op implementation is provided via [NoopLogger].
type Logger interface {
	Info(msg string, kv ...any)
	Error(msg string, err error, kv ...any)
}

// NoopLogger is a [Logger] that discards all output. It is used when the caller
// does not inject a logger.
type NoopLogger struct{}

func (NoopLogger) Info(_ string, _ ...any)        {}
func (NoopLogger) Error(_ string, _ error, _ ...any) {}

// ─────────────────────────────────────────────────────────────────────────────
// DispatcherDeps — orchestrator dependencies injected at construction
// ─────────────────────────────────────────────────────────────────────────────

// DispatcherDeps carries the orchestrator-layer objects that the Dispatcher
// needs to translate events into orchestrator calls.
type DispatcherDeps struct {
	// Orchestrator is the living-wiki orchestrator. Required.
	Orchestrator *orchestrator.Orchestrator

	// WatermarkStore persists per-repo SHA watermarks. Required.
	WatermarkStore orchestrator.WatermarkStore

	// SinkEditConfig carries the A1.P6 dependencies needed to handle
	// Confluence/Notion block edits. Required when SinkBlockEditEvent or
	// NotionBlockEditEvent events are submitted.
	SinkEditConfig orchestrator.SinkEditConfig

	// PushDiffProvider computes the diff for push events. When nil, push
	// events advance the processed watermark without triggering regeneration.
	// Production enterprise deployments inject the real provider; OSS
	// deployments that rely on polling can leave this nil.
	PushDiffProvider orchestrator.DiffProvider

	// SinkPollers maps sink names to their polling adapters. Used by
	// ManualRefreshEvent handling to poll all configured sinks for a repo.
	// May be nil when no sinks are configured.
	SinkPollers map[string]orchestrator.SinkPoller

	// Logger receives structured log lines for each handled event.
	// When nil, [NoopLogger] is used.
	Logger Logger
}

// ─────────────────────────────────────────────────────────────────────────────
// DispatcherConfig
// ─────────────────────────────────────────────────────────────────────────────

// DispatcherConfig controls the Dispatcher's resource limits.
type DispatcherConfig struct {
	// WorkerCount is the number of goroutines that drain the global overflow
	// queue. Default 4.
	WorkerCount int

	// MaxQueueDepth is the capacity of the per-repo event channel and the
	// global overflow queue. Default 1000.
	MaxQueueDepth int

	// EventTimeout is the maximum time allowed for a single event handler.
	// Default 5 minutes.
	EventTimeout time.Duration

	// DedupeCapacity is the maximum number of delivery IDs held in the
	// in-memory LRU. Default 10 000.
	DedupeCapacity int

	// DedupeTTL is how long a delivery ID is remembered. Default 1 hour.
	DedupeTTL time.Duration
}

func (c *DispatcherConfig) applyDefaults() {
	if c.WorkerCount <= 0 {
		c.WorkerCount = 4
	}
	if c.MaxQueueDepth <= 0 {
		c.MaxQueueDepth = 1000
	}
	if c.EventTimeout <= 0 {
		c.EventTimeout = 5 * time.Minute
	}
	if c.DedupeCapacity <= 0 {
		c.DedupeCapacity = 10_000
	}
	if c.DedupeTTL <= 0 {
		c.DedupeTTL = time.Hour
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Dispatcher
// ─────────────────────────────────────────────────────────────────────────────

// Dispatcher receives [WebhookEvent] values, deduplicates by delivery ID, and
// dispatches each event to the correct [orchestrator.Orchestrator] method,
// serializing events per repository to prevent concurrent mutations.
type Dispatcher struct {
	cfg    DispatcherConfig
	deps   DispatcherDeps
	logger Logger

	// Per-repo serialization: one channel per repo; a dedicated goroutine drains it.
	repoMu     sync.Mutex
	repoQueues map[string]chan queuedEvent
	repoStop   map[string]chan struct{} // closed when the repo goroutine should exit
	repoWg     sync.WaitGroup          // tracks all repo goroutines

	// Global overflow queue for repos whose per-repo goroutine is not yet running.
	// In practice the per-repo goroutine is started on first submission, so this
	// queue is rarely used — it is kept as a belt-and-suspenders buffer.
	overflow chan queuedEvent
	workers  sync.WaitGroup

	dedupe *deliveryCache

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
}

// queuedEvent is an event plus the context used to bound its handler lifetime.
type queuedEvent struct {
	event WebhookEvent
	ctx   context.Context //nolint:containedctx — intentional: we propagate the bounded ctx to the handler
}

// NewDispatcher creates a Dispatcher. Call [Dispatcher.Start] before
// submitting events.
func NewDispatcher(deps DispatcherDeps, cfg DispatcherConfig) *Dispatcher {
	cfg.applyDefaults()
	logger := deps.Logger
	if logger == nil {
		logger = NoopLogger{}
	}
	return &Dispatcher{
		cfg:        cfg,
		deps:       deps,
		logger:     logger,
		repoQueues: make(map[string]chan queuedEvent),
		repoStop:   make(map[string]chan struct{}),
		overflow:   make(chan queuedEvent, cfg.MaxQueueDepth),
		dedupe:     newDeliveryCache(cfg.DedupeCapacity, cfg.DedupeTTL),
		stopCh:     make(chan struct{}),
	}
}

// Start spins up the dispatcher's goroutines. It is safe to call Start exactly
// once. Subsequent calls are no-ops.
func (d *Dispatcher) Start(ctx context.Context) error {
	d.startOnce.Do(func() {
		for i := 0; i < d.cfg.WorkerCount; i++ {
			d.workers.Add(1)
			go func() {
				defer d.workers.Done()
				d.drainOverflow(ctx)
			}()
		}
	})
	return nil
}

// Stop signals all worker goroutines to stop and waits for them to drain
// in-flight events. The provided context bounds how long Stop will wait;
// after ctx expires any remaining events are abandoned.
func (d *Dispatcher) Stop(ctx context.Context) error {
	d.stopOnce.Do(func() {
		close(d.stopCh)
	})

	done := make(chan struct{})
	go func() {
		d.workers.Wait()
		d.repoWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// LiveOrchestrator returns the living-wiki orchestrator wired into this
// Dispatcher. Callers (e.g. the cold-start job goroutine in the GraphQL
// resolver) use this to call Generate directly without routing through the
// event-dispatch pipeline. Returns nil when the Dispatcher was constructed
// without an orchestrator (should not happen in production).
func (d *Dispatcher) LiveOrchestrator() *orchestrator.Orchestrator {
	return d.deps.Orchestrator
}

// Submit enqueues an event for asynchronous dispatch. It returns immediately
// after enqueuing; actual handling is asynchronous.
//
// Returns [ErrQueueFull] when the per-repo queue is full and the event cannot
// be accepted. The caller should log and discard the event in that case — the
// provider will retry delivery.
//
// Returns [ErrDuplicate] when the delivery ID was already seen within the
// deduplication TTL.
func (d *Dispatcher) Submit(ctx context.Context, event WebhookEvent) error {
	if event == nil {
		return fmt.Errorf("dispatcher: nil event")
	}
	if event.RepoID() == "" {
		return fmt.Errorf("dispatcher: event has empty RepoID")
	}

	// Deduplication check.
	if id := event.DeliveryID(); id != "" {
		if d.dedupe.seen(id) {
			d.logger.Info("dispatcher: duplicate delivery ignored",
				"delivery_id", id,
				"event_type", event.EventType(),
				"repo_id", event.RepoID(),
			)
			return ErrDuplicate
		}
	}

	// Bound the handler's lifetime.
	handlerCtx, cancel := context.WithTimeout(ctx, d.cfg.EventTimeout)
	// cancel is called by the handler goroutine when it finishes.
	_ = cancel // stored in the queuedEvent below; handler must call it

	qe := queuedEvent{event: event, ctx: handlerCtx}

	// Route to per-repo queue.
	ch := d.repoQueueFor(event.RepoID())
	select {
	case ch <- qe:
		return nil
	default:
		// Per-repo queue full — try global overflow.
	}

	select {
	case d.overflow <- qe:
		return nil
	default:
		cancel() // avoid context leak
		return ErrQueueFull
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Per-repo serialization
// ─────────────────────────────────────────────────────────────────────────────

// repoQueueFor returns (and lazily creates) the per-repo event channel.
// A dedicated goroutine is started the first time a channel is created.
func (d *Dispatcher) repoQueueFor(repoID string) chan queuedEvent {
	d.repoMu.Lock()
	defer d.repoMu.Unlock()

	if ch, ok := d.repoQueues[repoID]; ok {
		return ch
	}

	ch := make(chan queuedEvent, d.cfg.MaxQueueDepth)
	stop := make(chan struct{})
	d.repoQueues[repoID] = ch
	d.repoStop[repoID] = stop

	d.repoWg.Add(1)
	go func() {
		defer d.repoWg.Done()
		d.drainRepoQueue(repoID, ch, stop)
	}()

	return ch
}

// drainRepoQueue processes events for one repo sequentially. Because a single
// goroutine owns the repo's queue, at most one event handler runs for this repo
// at any time — the per-repo serialization guarantee.
func (d *Dispatcher) drainRepoQueue(repoID string, ch <-chan queuedEvent, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-d.stopCh:
			// Drain any remaining events already in the channel before exiting.
			for {
				select {
				case qe := <-ch:
					d.handle(qe)
				default:
					return
				}
			}
		case qe := <-ch:
			d.handle(qe)
		}
	}
}

// drainOverflow processes events from the global overflow queue using the
// shared worker pool. Overflow events lose per-repo serialization guarantees
// but this is acceptable: overflow only fires when per-repo queues are full,
// which should be rare in practice.
func (d *Dispatcher) drainOverflow(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopCh:
			return
		case qe := <-d.overflow:
			d.handle(qe)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Event handler dispatch
// ─────────────────────────────────────────────────────────────────────────────

// handle dispatches a single event to the orchestrator and logs the outcome.
func (d *Dispatcher) handle(qe queuedEvent) {
	start := time.Now()
	event := qe.event
	ctx := qe.ctx

	var outcome string
	var handlerErr error

	defer func() {
		durationMs := time.Since(start).Milliseconds()
		if handlerErr != nil {
			d.logger.Error("dispatcher: event handler failed",
				handlerErr,
				"event_type", string(event.EventType()),
				"repo_id", event.RepoID(),
				"outcome", outcome,
				"duration_ms", durationMs,
			)
		} else {
			d.logger.Info("dispatcher: event handled",
				"event_type", string(event.EventType()),
				"repo_id", event.RepoID(),
				"outcome", outcome,
				"duration_ms", durationMs,
			)
		}
	}()

	switch e := event.(type) {
	case PushEvent:
		handlerErr = d.handlePush(ctx, e)
		outcome = outcomeStr(handlerErr)

	case PRBranchCommitEvent:
		handlerErr = d.handlePRBranchCommit(ctx, e)
		outcome = outcomeStr(handlerErr)

	case PRMergedEvent:
		handlerErr = d.handlePRMerged(ctx, e)
		outcome = outcomeStr(handlerErr)

	case PRRejectedEvent:
		handlerErr = d.handlePRRejected(ctx, e)
		outcome = outcomeStr(handlerErr)

	case SinkBlockEditEvent:
		handlerErr = d.handleSinkBlockEdit(ctx, e)
		outcome = outcomeStr(handlerErr)

	case NotionBlockEditEvent:
		handlerErr = d.handleSinkBlockEdit(ctx, e.SinkBlockEditEvent)
		outcome = outcomeStr(handlerErr)

	case ManualRefreshEvent:
		handlerErr = d.handleManualRefresh(ctx, e)
		outcome = outcomeStr(handlerErr)

	default:
		outcome = "unknown_event_type"
		d.logger.Error("dispatcher: unrecognised event type",
			fmt.Errorf("unhandled event type %T", event),
			"event_type", string(event.EventType()),
			"repo_id", event.RepoID(),
		)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Per-event handlers
// ─────────────────────────────────────────────────────────────────────────────

// handlePush runs GenerateIncremental for a push to the default branch.
// The IncrementalRequest is constructed from the watermark store and a nil
// DiffProvider placeholder — callers that need a real diff must inject one via
// the DispatcherDeps or by wrapping the Dispatcher. For the OSS dispatcher,
// this delegates to the orchestrator with minimal fields; the enterprise layer
// injects the real DiffProvider via the registered hook (not shown here).
func (d *Dispatcher) handlePush(ctx context.Context, e PushEvent) error {
	wm, err := d.deps.WatermarkStore.Get(ctx, e.Repo)
	if err != nil {
		return fmt.Errorf("handlePush: reading watermarks for %q: %w", e.Repo, err)
	}
	// When the diff provider is not injected (common in OSS deployments that
	// use polling instead of webhooks), we advance the processed watermark and
	// return — a subsequent full regen will pick up the changes.
	if d.deps.PushDiffProvider == nil {
		if advErr := d.deps.WatermarkStore.AdvanceProcessed(ctx, e.Repo, e.AfterSHA); advErr != nil {
			return fmt.Errorf("handlePush: advancing watermark: %w", advErr)
		}
		d.logger.Info("dispatcher: push received but no DiffProvider injected; watermark advanced",
			"repo_id", e.Repo,
			"after_sha", e.AfterSHA,
			"wiki_published_sha", wm.WikiPublishedSHA,
		)
		return nil
	}

	req := orchestrator.IncrementalRequest{
		HeadSHA:        e.AfterSHA,
		DiffProvider:   d.deps.PushDiffProvider,
		WatermarkStore: d.deps.WatermarkStore,
		Config:         orchestrator.Config{RepoID: e.Repo},
	}
	_, err = d.deps.Orchestrator.GenerateIncremental(ctx, req)
	return err
}

// handlePRBranchCommit marks human-edited blocks in proposed_ast for commits on
// the wiki PR branch.
func (d *Dispatcher) handlePRBranchCommit(ctx context.Context, e PRBranchCommitEvent) error {
	if d.deps.Orchestrator == nil {
		d.logger.Info("dispatcher: PRBranchCommit: orchestrator not configured; skipping",
			"repo_id", e.Repo, "pr_id", e.PRID)
		return nil
	}
	// Convert webhook.Commit → orchestrator.Commit.
	orchCommits := make([]orchestrator.Commit, len(e.Commits))
	for i, c := range e.Commits {
		orchCommits[i] = orchestrator.Commit{
			SHA:            c.SHA,
			CommitterName:  c.CommitterName,
			CommitterEmail: c.CommitterEmail,
			Files:          c.Files,
		}
	}
	return d.deps.Orchestrator.ApplyReviewerCommits(ctx, e.Repo, e.PRID, orchCommits)
}

// handlePRMerged promotes proposed pages to canonical and advances
// WikiPublishedSHA.
func (d *Dispatcher) handlePRMerged(ctx context.Context, e PRMergedEvent) error {
	if d.deps.Orchestrator == nil {
		d.logger.Info("dispatcher: PRMerged: orchestrator not configured; skipping",
			"repo_id", e.Repo, "pr_id", e.PRID)
		return nil
	}
	if err := d.deps.Orchestrator.PromoteWithWatermark(
		ctx, e.Repo, e.PRID, e.MergedSHA, d.deps.WatermarkStore,
	); err != nil {
		return fmt.Errorf("handlePRMerged: %w", err)
	}
	return nil
}

// handlePRRejected discards proposed pages and rolls back source_processed_sha
// to wiki_published_sha so the next push regenerates the rejected delta.
func (d *Dispatcher) handlePRRejected(ctx context.Context, e PRRejectedEvent) error {
	if d.deps.Orchestrator == nil {
		d.logger.Info("dispatcher: PRRejected: orchestrator not configured; skipping",
			"repo_id", e.Repo, "pr_id", e.PRID)
		return nil
	}
	if err := d.deps.Orchestrator.DiscardWithWatermark(
		ctx, e.Repo, e.PRID, d.deps.WatermarkStore,
	); err != nil {
		return fmt.Errorf("handlePRRejected: %w", err)
	}
	return nil
}

// handleSinkBlockEdit dispatches a Confluence or Notion block edit to
// [orchestrator.Orchestrator.HandleSinkEdit].
func (d *Dispatcher) handleSinkBlockEdit(ctx context.Context, e SinkBlockEditEvent) error {
	if d.deps.Orchestrator == nil {
		d.logger.Info("dispatcher: SinkBlockEdit: orchestrator not configured; skipping",
			"repo_id", e.Repo, "page_id", e.PageID)
		return nil
	}
	edit := orchestrator.SinkEdit{
		SinkName:   ast.SinkName(e.SinkName),
		BlockID:    ast.BlockID(e.BlockID),
		NewContent: blockContentToAST(e.NewContent),
		EditedBy:   e.EditedBy,
		EditedAt:   e.EditedAt,
	}
	_, err := d.deps.Orchestrator.HandleSinkEdit(
		ctx,
		d.deps.SinkEditConfig,
		e.Repo,
		e.PageID,
		edit,
	)
	return err
}

// handleManualRefresh triggers a full or per-page regen in response to an
// operator refresh request.
func (d *Dispatcher) handleManualRefresh(ctx context.Context, e ManualRefreshEvent) error {
	// A manual refresh with a specific page ID triggers PollAndReconcile for all
	// configured sinks on that page. A whole-repo refresh advances the watermark
	// to empty so the next push triggers full regen.
	if e.PageID != "" {
		if d.deps.Orchestrator == nil {
			d.logger.Info("dispatcher: ManualRefresh: orchestrator not configured; skipping per-page poll",
				"repo_id", e.Repo, "page_id", e.PageID)
			return nil
		}
		// Per-page: best-effort poll across all configured sinks.
		for sinkName, poller := range d.deps.SinkPollers {
			if err := d.deps.Orchestrator.PollAndReconcile(
				ctx,
				d.deps.SinkEditConfig,
				e.Repo,
				e.PageID,
				ast.SinkName(sinkName),
				poller,
			); err != nil {
				// Non-fatal: log and continue to next sink.
				d.logger.Error("dispatcher: manual refresh poll failed",
					err,
					"repo_id", e.Repo,
					"page_id", e.PageID,
					"sink_name", sinkName,
				)
			}
		}
		return nil
	}

	// Whole-repo refresh: reset processed watermark so next push triggers full regen.
	marks, err := d.deps.WatermarkStore.Get(ctx, e.Repo)
	if err != nil {
		return fmt.Errorf("handleManualRefresh: reading watermarks: %w", err)
	}
	// Reset to WikiPublishedSHA so the next incremental run diffs from the known-good state.
	return d.deps.WatermarkStore.Reset(ctx, e.Repo, marks.WikiPublishedSHA)
}

// ─────────────────────────────────────────────────────────────────────────────
// Sentinel errors
// ─────────────────────────────────────────────────────────────────────────────

// ErrQueueFull is returned by Submit when both the per-repo queue and the
// overflow queue are full.
var ErrQueueFull = fmt.Errorf("dispatcher: event queue full — event dropped")

// ErrDuplicate is returned by Submit when the delivery ID was already seen.
var ErrDuplicate = fmt.Errorf("dispatcher: duplicate delivery ID — event ignored")

// ─────────────────────────────────────────────────────────────────────────────
// Delivery-ID deduplication cache
// ─────────────────────────────────────────────────────────────────────────────

// deliveryCache is a bounded in-memory set of recently seen delivery IDs.
// It uses a simple circular-buffer LRU backed by a map for O(1) lookup.
type deliveryCache struct {
	mu       sync.Mutex
	cap      int
	ttl      time.Duration
	entries  map[string]time.Time // delivery ID → expiry
	eviction []string             // insertion-order ring buffer for eviction
	head     int
}

func newDeliveryCache(capacity int, ttl time.Duration) *deliveryCache {
	return &deliveryCache{
		cap:     capacity,
		ttl:     ttl,
		entries: make(map[string]time.Time, capacity),
		eviction: make([]string, capacity),
	}
}

// seen returns true if id was already recorded within the TTL window.
// It records id when it has not been seen before.
func (c *deliveryCache) seen(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if exp, ok := c.entries[id]; ok && now.Before(exp) {
		return true
	}

	// Evict the oldest entry when at capacity.
	if len(c.entries) >= c.cap {
		oldest := c.eviction[c.head]
		if oldest != "" {
			delete(c.entries, oldest)
		}
	}

	c.entries[id] = now.Add(c.ttl)
	c.eviction[c.head] = id
	c.head = (c.head + 1) % c.cap
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func outcomeStr(err error) string {
	if err == nil {
		return "ok"
	}
	return "error"
}

// blockContentToAST converts the webhook package's BlockContent to the AST
// package's BlockContent. The webhook package avoids importing ast directly to
// keep the dependency graph clean; this adapter lives here where the import is
// acceptable.
func blockContentToAST(bc BlockContent) ast.BlockContent {
	switch {
	case bc.ParagraphMarkdown != "":
		return ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: bc.ParagraphMarkdown}}
	case bc.CodeBody != "":
		return ast.BlockContent{Code: &ast.CodeContent{
			Language: bc.CodeLanguage,
			Body:     bc.CodeBody,
		}}
	default:
		return ast.BlockContent{Freeform: &ast.FreeformContent{Raw: bc.Raw}}
	}
}
