// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package changewatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/git"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// ImpactApplier is the package-boundary contract for the resolver-side
// post-impact helper. The router calls ApplyImpact after a successful
// IndexFiles + MergeIndexResult so the existing knowledge-store
// invalidation policy (selective MarkStaleForImpact when enabled,
// blanket MarkAllStale otherwise), report persistence, and delta-regen
// goroutine launch fire identically whether the trigger was the
// existing reindex mutation or a new change-watch event.
//
// internal/api/graphql wires the implementation at server-assembly time
// via a tiny adapter that calls (*mutationResolver).applyImpactFromChange.
// This package does NOT depend on internal/api/graphql; the dependency
// flows the right direction (graphql → changewatch is fine; the reverse
// would create the import cycle bob caught in v5).
type ImpactApplier interface {
	ApplyImpact(ctx context.Context, repoID string, report *graphstore.ImpactReport)
}

// BranchValidator decouples the router from the on-disk git layer for
// testability. The production wiring is git.HeadRef directly; tests
// pass a stub that returns a configurable branch.
type BranchValidator interface {
	HeadRef(repoPath string) (string, error)
}

// Indexer is the package-boundary contract for indexer.Indexer. Tests
// substitute a stub so the router can be exercised without spinning up
// a real tree-sitter parser. Production wiring passes a real *indexer.Indexer.
type Indexer interface {
	IndexFiles(
		ctx context.Context,
		repoPath string,
		files []string,
		branch string,
		previousResult *indexer.IndexResult,
	) (*indexer.IndexResult, error)
}

// Config holds the router's runtime configuration. All fields have
// sensible zero-value handling; the router clamps invalid values to
// safe defaults rather than failing closed at boot.
type Config struct {
	// Enabled is the umbrella feature flag. When false, Submit returns
	// ErrChangeWatchDisabled without doing any work. Default false in
	// Phase 1.C; flipped at the end of Phase 1.E.
	Enabled bool

	// RateLimitPerMin is the per-(repo, source.kind) throttle. Default
	// 30 events/min. Zero disables the throttle (events are accepted
	// unconditionally up to the breaker).
	RateLimitPerMin int

	// RepoBreakerPerMin is the per-repo aggregate-across-all-source-kinds
	// trip threshold. The breaker opens when the per-minute rate stays
	// above this for 5 consecutive minutes. Default 60. Zero disables.
	RepoBreakerPerMin int

	// T0BudgetMs is the per-event hard ceiling for the synchronous
	// IndexFiles call. Default 100ms.
	T0BudgetMs int

	// DedupWindow is the time window inside which duplicate event_ids
	// (and identical content_hashes from different connectors) collapse
	// to a single routed event. Default 10s.
	DedupWindow time.Duration
}

// Router is the in-process change-watch dispatch hub. Every connector
// (Watcher, in-process record_change, future HTTP-ingress connectors)
// funnels events into Submit. The router enforces the schema, dedups,
// rate-limits, validates branch, calls IndexFiles + MergeIndexResult +
// ApplyImpact, and updates the freshness state.
//
// Router is goroutine-safe. Internal state is protected by mu.
type Router struct {
	cfg Config

	store         graphstore.GraphStore
	indexer       Indexer
	impactApplier ImpactApplier
	branches      BranchValidator

	// nowFn is the time source used by every router operation. Stored
	// behind atomic.Pointer (not r.mu) so callers reading the clock from
	// inside a critical section don't re-enter the lock — that lock
	// ordering caused the original 1.C deadlock (Submit → timeNow with
	// r.mu held). atomic.Pointer is the minimum-cost shape that gives
	// us "lock-free read, occasional write from SetNow."
	//
	// Always non-nil after construction (NewRouter seeds it with
	// time.Now). callers go through r.timeNow() so the indirection is
	// invisible at the call sites.
	nowFn atomic.Pointer[func() time.Time]

	mu       sync.Mutex
	prev     map[string]*indexer.IndexResult // repoID → cached previous IndexResult
	rates    map[rateKey]*tokenBucket
	breakers map[string]*windowBreaker
	dedup    map[string]time.Time // event_id → received_at; pruned lazily

	// freshness records the most recently observed change-event metadata
	// per repo so the freshness envelope on MCP responses can answer
	// "when was this last verified, on which branch, by which actor."
	// Read by the freshness adapter at envelope-construction time.
	freshness map[string]freshnessRecord

	// bus is an optional notification fan-out for the Watcher's tests
	// and for downstream consumers that want to observe routing
	// outcomes (e.g. integration tests). Wired only when set; nil-safe.
	bus chan<- routerEvent
}

// freshnessRecord captures the per-repo state the freshness envelope
// needs. Kept here so the router is the single owner of mutable
// freshness state in Phase 1.C; later phases can promote this to a
// dedicated FreshnessStore once the Phase 2 migrations land.
type freshnessRecord struct {
	Branch         string
	IndexedCommit  string
	LastVerifiedAt time.Time
	Reason         string
	PartialRefresh bool
	Tier           string // T0 | T1 | T2 | T3
	State          string // fresh | stale | suspect | invalidated
}

// routerEvent is the optional fan-out shape; tests subscribe to it via
// SetEventBus. Production-side consumers in 1.C don't subscribe; the
// hook exists for the integration-test harness.
type routerEvent struct {
	EventID   string
	RepoID    string
	Outcome   SubmitOutcome
	Err       error
	Timestamp time.Time
}

// rateKey identifies a per-(repo, source.kind) bucket.
type rateKey struct {
	RepoID     string
	SourceKind SourceKind
}

// NewRouter constructs a Router with the provided dependencies. Any
// nil dependency is required at construction time (no lazy bootstrap)
// because the router never accepts events while disabled and a
// half-wired router is more dangerous than a clearly-failed boot.
func NewRouter(
	cfg Config,
	store graphstore.GraphStore,
	idx Indexer,
	impact ImpactApplier,
	branches BranchValidator,
) *Router {
	cfg = cfg.applyDefaults()
	r := &Router{
		cfg:           cfg,
		store:         store,
		indexer:       idx,
		impactApplier: impact,
		branches:      branches,
		prev:          make(map[string]*indexer.IndexResult),
		rates:         make(map[rateKey]*tokenBucket),
		breakers:      make(map[string]*windowBreaker),
		dedup:         make(map[string]time.Time),
		freshness:     make(map[string]freshnessRecord),
	}
	defaultNow := func() time.Time { return time.Now() }
	r.nowFn.Store(&defaultNow)
	return r
}

// applyDefaults clamps zero/negative config to sane defaults so the
// router is robust to a partially-populated Config.
func (c Config) applyDefaults() Config {
	if c.RateLimitPerMin < 0 {
		c.RateLimitPerMin = 0
	}
	if c.RepoBreakerPerMin < 0 {
		c.RepoBreakerPerMin = 0
	}
	if c.T0BudgetMs <= 0 {
		c.T0BudgetMs = 100
	}
	if c.DedupWindow <= 0 {
		c.DedupWindow = 10 * time.Second
	}
	return c
}

// SetEventBus wires an optional fan-out channel so tests can observe
// routing outcomes without polling. Production callers leave this nil.
func (r *Router) SetEventBus(bus chan<- routerEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bus = bus
}

// SetNow overrides the time source for tests. Production callers leave
// the default (time.Now). Stored atomically so it's safe to read from
// inside a critical section that already holds r.mu without re-entering
// the lock.
func (r *Router) SetNow(now func() time.Time) {
	if now == nil {
		fallback := func() time.Time { return time.Now() }
		r.nowFn.Store(&fallback)
		return
	}
	r.nowFn.Store(&now)
}

// SeedPrevious primes the per-repo IndexResult cache with the result
// of an earlier index run. Callers must invoke this once per repo
// before submitting events for it (typically after the
// indexing.Service or ReindexRepository mutation completes); without
// the seed, IndexFiles cannot run because it requires previousResult.
//
// Concurrent seeds for the same repo are last-write-wins. The router
// does not validate the seed — callers are responsible for handing in
// a result whose Files slice represents the entire current state of
// the repo.
func (r *Router) SeedPrevious(repoID string, result *indexer.IndexResult) {
	if result == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prev[repoID] = result
	// Initialize freshness for this repo so the envelope has a usable
	// starting state.
	if _, ok := r.freshness[repoID]; !ok {
		r.freshness[repoID] = freshnessRecord{
			Branch:         result.Branch,
			LastVerifiedAt: r.timeNow(),
			Tier:           "T0",
			State:          "fresh",
		}
	}
}

// FreshnessFor returns the most recent freshness record for repoID, or
// the zero value when the router has never routed an event for that
// repo. Internal callers within this package use this; external
// callers use FreshnessForExport, which returns the public-typed
// shape.
func (r *Router) FreshnessFor(repoID string) freshnessRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.freshness[repoID]
}

// FreshnessExport is the package-public freshness shape exposed to
// external callers (the MCP freshness adapter at
// internal/api/rest/mcp_freshness.go). Mirrors the unexported
// freshnessRecord field-for-field; we keep the internal type
// unexported so the router stays in control of the ownership story.
type FreshnessExport struct {
	State          string
	Tier           string
	Branch         string
	IndexedCommit  string
	LastVerifiedAt time.Time
	Reason         string
	PartialRefresh bool
}

// FreshnessForExport is the exported variant of FreshnessFor for
// callers outside this package. Returns the zero FreshnessExport when
// the router has never routed an event for repoID; the envelope
// handler treats that as "default fresh."
func (r *Router) FreshnessForExport(repoID string) FreshnessExport {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.freshness[repoID]
	return FreshnessExport{
		State:          rec.State,
		Tier:           rec.Tier,
		Branch:         rec.Branch,
		IndexedCommit:  rec.IndexedCommit,
		LastVerifiedAt: rec.LastVerifiedAt,
		Reason:         rec.Reason,
		PartialRefresh: rec.PartialRefresh,
	}
}

// PreviousResult returns the most recent cached IndexResult for repoID,
// or nil when none exists. The Watcher reads this to decide whether to
// emit a routed event (no-op if unprimed).
func (r *Router) PreviousResult(repoID string) *indexer.IndexResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prev[repoID]
}

// Submit processes a single ChangeEvent end-to-end. Returns the
// SubmitOutcome the connector should report (typed alias of the wire
// values) and an error when the outcome is non-OK. A successful
// indexing dispatch returns OutcomeIndexing with err=nil.
//
// Submit is the single entry point through which all connectors
// dispatch events; the in-process record_change tool (1.D), the
// fsnotify Watcher (1.C), and the HTTP ingress endpoint (1.D) all
// call it. The router enforces the closed-loop contract here so
// connectors do not have to.
func (r *Router) Submit(ctx context.Context, ev *ChangeEvent) (SubmitOutcome, error) {
	// 1. Schema-level validation (stateless, no I/O). Cheapest possible
	// reject path; runs before every other check.
	if outcome, err := ev.Validate(); err != nil {
		r.publish(ev, outcome, err)
		return outcome, err
	}

	// 2. Umbrella feature flag. We accept the validation cost above
	// even when disabled so a buggy connector can't probe the flag
	// state by sending malformed events; the schema check is
	// content-independent.
	if !r.cfg.Enabled {
		r.publish(ev, OutcomeRejectedSchema, ErrChangeWatchDisabled)
		return OutcomeRejectedSchema, ErrChangeWatchDisabled
	}

	now := r.timeNow()

	r.mu.Lock()
	// 3. Repo resolution. ev.RepositoryID must map to an indexed repo;
	// unknown repos are rejected before any further work. We use the
	// cached prev IndexResult as the proof-of-existence — a repo that
	// was never seeded is one the operator hasn't indexed yet.
	prev, primed := r.prev[ev.RepositoryID]
	if !primed {
		r.mu.Unlock()
		r.publish(ev, OutcomeRejectedUnknownRepo, ErrUnknownRepo)
		return OutcomeRejectedUnknownRepo, ErrUnknownRepo
	}

	// 4. Per-repo aggregate breaker (across all source kinds combined).
	// The breaker observes ALL events; if the rolling rate has stayed
	// above RepoBreakerPerMin for 5 consecutive minutes it is open and
	// every event is dropped until the rate falls back below.
	breaker := r.breakerFor(ev.RepositoryID)
	if breaker.tripped(now) {
		r.mu.Unlock()
		r.publish(ev, OutcomeBreakerTripped, ErrBreakerOpen)
		return OutcomeBreakerTripped, ErrBreakerOpen
	}

	// 5. Per-(repo, source.kind) rate limit. Finer-grained throttle
	// applied alongside the breaker. This is what stops one specific
	// connector from monopolizing the router; the breaker stops the
	// repo as a whole from being a hot loop.
	bucket := r.rateBucketFor(rateKey{RepoID: ev.RepositoryID, SourceKind: ev.Source.Kind})
	if !bucket.allow(now) {
		breaker.observe(now)
		r.mu.Unlock()
		r.publish(ev, OutcomeRateLimited, ErrRateLimited)
		return OutcomeRateLimited, ErrRateLimited
	}
	breaker.observe(now)

	// 6. Dedup window. Both the event_id (idempotency key) and the
	// content_hash (so fsnotify + record_change observing the same edit
	// collapse) are checked. We prune expired entries lazily.
	if r.isDuplicate(ev, now) {
		r.mu.Unlock()
		r.publish(ev, OutcomeDeduped, nil)
		return OutcomeDeduped, nil
	}
	r.recordDedup(ev, now)
	r.mu.Unlock()

	// 7. Branch validation (router-level half of Risk #4 / HIGH fix #6).
	// We compare ev.Branch against the working tree's HEAD as observed
	// by git.HeadRef. Mismatch → rejected_branch_mismatch with both
	// branches in the structured log so an operator can debug.
	repoPath := repoPathFromIndexResult(prev)
	headBranch, err := r.branches.HeadRef(repoPath)
	if err != nil {
		// Not a git repo / can't resolve head — surface as schema
		// rejection rather than crashing. The watcher only fires on
		// indexed repos, which are always git working trees, so this
		// path is rare in practice.
		slog.Warn("changewatch: branch validation failed",
			"event_id", ev.EventID,
			"repo_id", ev.RepositoryID,
			"err", err,
		)
		r.publish(ev, OutcomeRejectedBranchMismatch, fmt.Errorf("%w: head_ref err: %v", ErrBranchMismatch, err))
		return OutcomeRejectedBranchMismatch, fmt.Errorf("%w: %v", ErrBranchMismatch, err)
	}
	if !branchesEqual(headBranch, ev.Branch) {
		slog.Warn("changewatch: branch mismatch — event rejected",
			"event_id", ev.EventID,
			"repo_id", ev.RepositoryID,
			"event_branch", ev.Branch,
			"head_branch", headBranch,
			"source_kind", string(ev.Source.Kind),
		)
		r.publish(ev, OutcomeRejectedBranchMismatch, ErrBranchMismatch)
		return OutcomeRejectedBranchMismatch, ErrBranchMismatch
	}

	// 8. IndexFiles under the T0 budget. Note: we do NOT call
	// IndexRepositoryIncremental or IndexRepository — those walk every
	// file in the repo and would blow the budget. The static-call
	// assertion test in router_no_call_paths_test.go pins this contract
	// at compile time.
	budget := time.Duration(r.cfg.T0BudgetMs) * time.Millisecond
	t0Ctx, cancel := context.WithTimeout(ctx, budget)
	affected := affectedPathsFor(ev)
	newResult, err := r.indexer.IndexFiles(t0Ctx, repoPath, affected, ev.Branch, prev)
	cancel()
	if err != nil {
		// Budget exceeded → serve current data with partial_refresh=true.
		// Other errors → the event is dropped (logged) but the loop
		// keeps running. We do NOT fall back to a full reindex; the
		// delta-only invariant rules that out.
		isBudgetExceeded := errors.Is(err, context.DeadlineExceeded)
		slog.Warn("changewatch: IndexFiles failed",
			"event_id", ev.EventID,
			"repo_id", ev.RepositoryID,
			"affected", affected,
			"budget_exceeded", isBudgetExceeded,
			"err", err,
		)
		r.markPartialRefresh(ev, isBudgetExceeded)
		r.publish(ev, OutcomeIndexing, err)
		return OutcomeIndexing, err
	}

	// 9. Containment assertion. Every file path in the merged result
	// that is NOT in the prior set must be in affected[]. (It's
	// acceptable for a file to be in affected but absent from
	// newResult.Files — that's the deletion case.)
	if violations := containmentViolations(prev, newResult, affected); len(violations) > 0 {
		slog.Error("changewatch: containment violation — IndexFiles produced files outside the declared delta",
			"event_id", ev.EventID,
			"repo_id", ev.RepositoryID,
			"violations", violations,
		)
		// Containment violations are contract bugs (in dev mode the
		// plan calls for panic; here we log and reject so production
		// stays up while the contract tooling catches us).
		r.publish(ev, OutcomeRejectedSchema, ErrContainmentViolation)
		return OutcomeRejectedSchema, ErrContainmentViolation
	}

	// 10. Snapshot OLD symbols + compute changed files set BEFORE the
	// merge. Without this snapshot DiffSymbols can't compute SymbolsAdded
	// / Modified / Removed correctly.
	oldSymbols, _ := r.store.GetSymbols(ev.RepositoryID, nil, nil, 0, 0)

	// 11. Apply the merge to the store. This drops dependent records on
	// affected files and re-inserts them with fresh UUIDs while
	// preserving carry-forward symbol IDs.
	if _, err := r.store.MergeIndexResult(ev.RepositoryID, affected, newResult); err != nil {
		// MergeIndexResult on the SurrealDB backend in 1.C returns
		// ErrMergeNotSupported. Surface it through the freshness
		// envelope as suspect rather than failing loudly.
		slog.Warn("changewatch: MergeIndexResult failed",
			"event_id", ev.EventID,
			"repo_id", ev.RepositoryID,
			"err", err,
		)
		r.markPartialRefresh(ev, false)
		r.publish(ev, OutcomeIndexing, err)
		return OutcomeIndexing, err
	}

	// 12. Compute the impact report and run the existing knowledge-store
	// invalidation policy via the resolver-side helper.
	newSymbols, _ := r.store.GetSymbols(ev.RepositoryID, nil, nil, 0, 0)
	fileDiffs, changedFilesSet := buildFileDiffs(ev)
	symbolChanges := graphstore.DiffSymbols(oldSymbols, newSymbols, changedFilesSet)
	report := graphstore.ComputeImpact(r.store, ev.RepositoryID, fileDiffs, symbolChanges)

	if r.impactApplier != nil {
		// Use a background-friendly context for impact application; the
		// caller's ctx may be a 100ms-budget timeout we already exhausted.
		// The impact application path includes goroutine launches that
		// outlive the original event delivery anyway.
		applyCtx, applyCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer applyCancel()
		r.impactApplier.ApplyImpact(applyCtx, ev.RepositoryID, report)
	}

	// 13. Update caches: cached previous IndexResult and freshness
	// envelope state.
	r.mu.Lock()
	r.prev[ev.RepositoryID] = newResult
	rec := r.freshness[ev.RepositoryID]
	rec.Branch = ev.Branch
	rec.LastVerifiedAt = r.timeNow()
	rec.Tier = "T0"
	rec.State = "fresh"
	rec.PartialRefresh = false
	rec.Reason = describeReason(ev)
	r.freshness[ev.RepositoryID] = rec
	r.mu.Unlock()

	r.publish(ev, OutcomeIndexing, nil)
	return OutcomeIndexing, nil
}

// markPartialRefresh sets the freshness envelope to a partial state on
// the budget-exceeded / merge-not-supported paths so the next MCP read
// can answer "I tried, here's what's current, the refresh hasn't
// completed yet."
func (r *Router) markPartialRefresh(ev *ChangeEvent, budgetExceeded bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.freshness[ev.RepositoryID]
	rec.PartialRefresh = true
	rec.LastVerifiedAt = r.timeNow()
	rec.Reason = describeReason(ev)
	if budgetExceeded {
		rec.State = "stale"
	} else {
		rec.State = "suspect"
	}
	r.freshness[ev.RepositoryID] = rec
}

// publish fans out a routed event to the optional event bus. Nil-safe;
// non-blocking (drops on full bus to avoid backpressure into Submit).
func (r *Router) publish(ev *ChangeEvent, outcome SubmitOutcome, err error) {
	r.mu.Lock()
	bus := r.bus
	r.mu.Unlock()
	if bus == nil {
		return
	}
	select {
	case bus <- routerEvent{
		EventID:   ev.EventID,
		RepoID:    ev.RepositoryID,
		Outcome:   outcome,
		Err:       err,
		Timestamp: r.timeNow(),
	}:
	default:
	}
}

// timeNow returns the current time per the configured clock. Reads the
// clock pointer atomically so callers may invoke timeNow from inside a
// critical section without taking r.mu — that lock ordering caused the
// original 1.C deadlock. Always returns a sane time even if SetNow has
// never been called (NewRouter seeds the default time.Now).
func (r *Router) timeNow() time.Time {
	fn := r.nowFn.Load()
	if fn == nil {
		return time.Now()
	}
	return (*fn)()
}

// breakerFor lazily creates a per-repo breaker. Caller must hold r.mu.
func (r *Router) breakerFor(repoID string) *windowBreaker {
	b, ok := r.breakers[repoID]
	if !ok {
		b = newWindowBreaker(r.cfg.RepoBreakerPerMin)
		r.breakers[repoID] = b
	}
	return b
}

// rateBucketFor lazily creates a per-(repo, kind) bucket. Caller holds r.mu.
func (r *Router) rateBucketFor(k rateKey) *tokenBucket {
	b, ok := r.rates[k]
	if !ok {
		b = newTokenBucket(r.cfg.RateLimitPerMin)
		r.rates[k] = b
	}
	return b
}

// isDuplicate returns true when the event_id is in the dedup window OR
// when an event with an identical content-hash payload landed within
// the window from a different connector. Caller must hold r.mu.
func (r *Router) isDuplicate(ev *ChangeEvent, now time.Time) bool {
	if t, ok := r.dedup[ev.EventID]; ok && now.Sub(t) < r.cfg.DedupWindow {
		return true
	}
	hashKey := contentHashKey(ev)
	if hashKey == "" {
		return false
	}
	if t, ok := r.dedup[hashKey]; ok && now.Sub(t) < r.cfg.DedupWindow {
		return true
	}
	return false
}

// recordDedup pins the event_id (and the content_hash key when set) in
// the dedup window. Caller must hold r.mu. Pruning is amortized in the
// same call to keep the map bounded under sustained load.
func (r *Router) recordDedup(ev *ChangeEvent, now time.Time) {
	r.dedup[ev.EventID] = now
	if k := contentHashKey(ev); k != "" {
		r.dedup[k] = now
	}
	r.pruneDedup(now)
}

// pruneDedup walks the dedup map and drops any entry older than the
// window. Caller must hold r.mu. O(n) where n is the number of events
// in the window — acceptable because the window is small (10s default)
// and the rate limit caps n upstream.
func (r *Router) pruneDedup(now time.Time) {
	cutoff := now.Add(-r.cfg.DedupWindow)
	for k, t := range r.dedup {
		if t.Before(cutoff) {
			delete(r.dedup, k)
		}
	}
}

// contentHashKey builds a stable key over the event's declared content
// hashes. When two connectors observe the same edit they produce the
// same set of file→hash tuples, so the key collapses cleanly. An event
// without any content_hash entries yields the empty string (no
// hash-based dedup possible).
func contentHashKey(ev *ChangeEvent) string {
	// Collect (path, hash) pairs sorted by path so a different ordering
	// from two connectors still hashes to the same key.
	paths := make([]string, 0, len(ev.Files))
	hashes := make(map[string]string, len(ev.Files))
	for _, f := range ev.Files {
		if f.ContentHashAfter == "" {
			continue
		}
		paths = append(paths, f.Path)
		hashes[f.Path] = f.ContentHashAfter
	}
	if len(paths) == 0 {
		return ""
	}
	// Sort paths for stable key construction.
	for i := 1; i < len(paths); i++ {
		for j := i; j > 0 && paths[j-1] > paths[j]; j-- {
			paths[j-1], paths[j] = paths[j], paths[j-1]
		}
	}
	out := "ch:" + ev.RepositoryID
	for _, p := range paths {
		out += "|" + p + "=" + hashes[p]
	}
	return out
}

// affectedPathsFor extracts the file paths the router will dispatch to
// IndexFiles. Renames count both the new and old paths so the old file
// is dropped from the merge.
func affectedPathsFor(ev *ChangeEvent) []string {
	out := make([]string, 0, len(ev.Files))
	seen := make(map[string]bool, len(ev.Files))
	for _, f := range ev.Files {
		if !seen[f.Path] {
			seen[f.Path] = true
			out = append(out, f.Path)
		}
		if f.Status == FileChangeRenamed && f.OldPath != "" && !seen[f.OldPath] {
			seen[f.OldPath] = true
			out = append(out, f.OldPath)
		}
	}
	return out
}

// repoPathFromIndexResult returns the on-disk repo path the router
// uses for git.HeadRef and IndexFiles. The cached previousResult is
// the source of truth — it was set by the seed call (which used the
// path the operator originally indexed).
func repoPathFromIndexResult(r *indexer.IndexResult) string {
	if r == nil {
		return ""
	}
	return r.RepoPath
}

// branchesEqual normalizes "refs/heads/X" vs "X" before comparing so
// the watcher (which uses git.HeadRef → branch name) and a connector
// that emits the full ref (HTTP ingress in 1.D will) interoperate.
func branchesEqual(a, b string) bool {
	const refsHeads = "refs/heads/"
	stripA := a
	stripB := b
	if len(a) > len(refsHeads) && a[:len(refsHeads)] == refsHeads {
		stripA = a[len(refsHeads):]
	}
	if len(b) > len(refsHeads) && b[:len(refsHeads)] == refsHeads {
		stripB = b[len(refsHeads):]
	}
	return stripA == stripB
}

// containmentViolations checks guardrail #3: any file in the merged
// result that wasn't in the prior result and isn't in affectedPaths is
// a violation. Caller must NOT hold r.mu.
func containmentViolations(prev, merged *indexer.IndexResult, affected []string) []string {
	if prev == nil || merged == nil {
		return nil
	}
	allowed := make(map[string]bool, len(affected))
	for _, p := range affected {
		allowed[p] = true
	}
	prevPaths := make(map[string]bool, len(prev.Files))
	for _, f := range prev.Files {
		prevPaths[f.Path] = true
	}
	var violations []string
	for _, f := range merged.Files {
		if prevPaths[f.Path] {
			continue
		}
		if !allowed[f.Path] {
			violations = append(violations, f.Path)
		}
	}
	return violations
}

// buildFileDiffs maps the event's Files[] into the ImpactFileDiff shape
// graph.ComputeImpact expects, plus a parallel changedFiles set keyed
// by post-rename path AND old_path so DiffSymbols sees both.
func buildFileDiffs(ev *ChangeEvent) ([]graphstore.ImpactFileDiff, map[string]bool) {
	diffs := make([]graphstore.ImpactFileDiff, 0, len(ev.Files))
	changed := make(map[string]bool, len(ev.Files))
	for _, f := range ev.Files {
		diffs = append(diffs, graphstore.ImpactFileDiff{
			Path:    f.Path,
			OldPath: f.OldPath,
			Status:  string(f.Status),
		})
		changed[f.Path] = true
		if f.OldPath != "" {
			changed[f.OldPath] = true
		}
	}
	return diffs, changed
}

// describeReason builds the freshness envelope's `reason` string from
// the event's source attribution. UI-friendly; never load-bearing.
func describeReason(ev *ChangeEvent) string {
	actor := ev.Source.Actor
	if actor == "" {
		actor = string(ev.Source.Kind)
	}
	if len(ev.Files) == 1 {
		return fmt.Sprintf("%s edited %s", actor, ev.Files[0].Path)
	}
	return fmt.Sprintf("%s touched %d files", actor, len(ev.Files))
}

// HeadRefBranchValidator is the production-side BranchValidator
// implementation backed by git.HeadRef.
type HeadRefBranchValidator struct{}

// HeadRef returns the branch name git.HeadRef reports for the working
// tree at repoPath. Wrapper exists so the router has a stable interface
// even if git.HeadRef gains parameters later.
func (HeadRefBranchValidator) HeadRef(repoPath string) (string, error) {
	return git.HeadRef(repoPath)
}
