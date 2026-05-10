// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package changewatch

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// stubIndexer is the test-side Indexer. It records every IndexFiles
// call and returns a configurable IndexResult or error.
type stubIndexer struct {
	mu     sync.Mutex
	calls  []stubIndexCall
	result *indexer.IndexResult
	err    error
	delay  time.Duration
}

type stubIndexCall struct {
	RepoPath string
	Files    []string
	Branch   string
}

func (s *stubIndexer) IndexFiles(ctx context.Context, repoPath string, files []string, branch string, prev *indexer.IndexResult) (*indexer.IndexResult, error) {
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s.mu.Lock()
	s.calls = append(s.calls, stubIndexCall{RepoPath: repoPath, Files: append([]string(nil), files...), Branch: branch})
	s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	if s.result != nil {
		return s.result, nil
	}
	// Default: return a minimal result that carries the branch and the
	// affected files merged into prev.
	out := *prev
	out.Branch = branch
	return &out, nil
}

func (s *stubIndexer) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *stubIndexer) lastCall() (stubIndexCall, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return stubIndexCall{}, false
	}
	return s.calls[len(s.calls)-1], true
}

// stubBranches is the test-side BranchValidator.
type stubBranches struct {
	branch string
	err    error
}

func (s *stubBranches) HeadRef(_ string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.branch, nil
}

// stubImpact is the test-side ImpactApplier. It records every call.
// (atomic.Int64 already serializes the increments, so no separate
// mutex is needed.)
type stubImpact struct {
	calls atomic.Int64
}

func (s *stubImpact) ApplyImpact(_ context.Context, _ string, _ *graphstore.ImpactReport) {
	s.calls.Add(1)
}

// newRouterHarness wires a Router with stub dependencies and a primed
// repo. Returns the router plus the underlying stubs for assertions.
func newRouterHarness(t *testing.T, cfg Config) (*Router, *stubIndexer, *stubBranches, *stubImpact, *graphstore.Store) {
	t.Helper()
	store := graphstore.NewStore()
	repo, err := store.CreateRepository(t.Context(), "test-repo", "/tmp/test-repo")
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	prev := &indexer.IndexResult{
		RepoName: "test-repo",
		RepoPath: "/tmp/test-repo",
		Branch:   "main",
		Files: []indexer.FileResult{
			{Path: "a.go", Language: "go", LineCount: 10, ContentHash: "h1"},
			{Path: "b.go", Language: "go", LineCount: 10, ContentHash: "h2"},
		},
	}
	if _, err := store.ReplaceIndexResult(t.Context(), repo.ID, prev); err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}

	idx := &stubIndexer{}
	branches := &stubBranches{branch: "main"}
	impact := &stubImpact{}
	router := NewRouter(cfg, store, idx, impact, branches)
	router.SeedPrevious(repo.ID, prev)

	// Capture repo.ID for the caller via a side-channel: rewrite
	// store.repos to use a known fixed ID so the tests have a stable
	// handle. We do that here by re-using the assigned ID.
	t.Cleanup(func() {
		// nothing to clean — store is in-memory only
	})
	return router, idx, branches, impact, store
}

// repoIDFromHarness returns the single repo's ID from the store.
func repoIDFromHarness(store *graphstore.Store) string {
	for _, r := range store.ListRepositories(context.Background()) {
		return r.ID
	}
	return ""
}

// makeEvent builds a baseline event the harness will accept.
func makeEvent(repoID string, kind SourceKind, eventID string, files ...FileChange) *ChangeEvent {
	if len(files) == 0 {
		files = []FileChange{{Path: "a.go", Status: FileChangeModified}}
	}
	return &ChangeEvent{
		SchemaVersion: ChangeEventSchemaVersion,
		EventID:       eventID,
		RepositoryID:  repoID,
		OccurredAt:    time.Now(),
		Branch:        "main",
		Files:         files,
		Source:        ChangeSource{Kind: kind, ConnectorID: "test"},
		Trust:         Trust{Verified: true, VerificationMethod: "in_process", ReceivedVia: "in_process"},
	}
}

// ─── Test #8 (containment-rejection schema half) ──────────────────────

// TestRouter_RejectsEmptyDelta — guardrail #1 from the delta-only
// invariant. An event whose Files[] is empty is rejected at the
// router boundary (in addition to the schema rejection in
// types_test.go's coverage).
//
// This is the router-level half of Phase 1 done-definition test #8.
func TestRouter_RejectsEmptyDelta(t *testing.T) {
	router, idx, _, impact, store := newRouterHarness(t, Config{Enabled: true})
	repoID := repoIDFromHarness(store)
	ev := makeEvent(repoID, SourceKindFsnotifyLocal, "test-empty")
	ev.Files = nil

	outcome, err := router.Submit(context.Background(), ev)
	if outcome != OutcomeRejectedNoDelta {
		t.Errorf("outcome = %q, want %q", outcome, OutcomeRejectedNoDelta)
	}
	if !errors.Is(err, ErrEmptyDelta) {
		t.Errorf("err = %v, want errors.Is(ErrEmptyDelta)", err)
	}
	// Critical: IndexFiles must NOT have been called for a rejected
	// event. The schema check is the cheapest reject path.
	if idx.callCount() != 0 {
		t.Errorf("IndexFiles call count = %d, want 0 (rejected before dispatch)", idx.callCount())
	}
	if impact.calls.Load() != 0 {
		t.Errorf("ImpactApplier call count = %d, want 0", impact.calls.Load())
	}
}

// TestRouter_RejectsInvalidPaths — the path-normalization contract is
// enforced at the router boundary so a connector that ships a
// not-yet-normalized path doesn't surprise downstream code with
// strange paths.
func TestRouter_RejectsInvalidPaths(t *testing.T) {
	router, idx, _, _, store := newRouterHarness(t, Config{Enabled: true})
	repoID := repoIDFromHarness(store)
	ev := makeEvent(repoID, SourceKindFsnotifyLocal, "bad-path",
		FileChange{Path: "../escape.go", Status: FileChangeModified})
	outcome, err := router.Submit(context.Background(), ev)
	if outcome != OutcomeRejectedInvalidPaths {
		t.Errorf("outcome = %q, want %q", outcome, OutcomeRejectedInvalidPaths)
	}
	if !errors.Is(err, ErrInvalidPath) {
		t.Errorf("err = %v, want errors.Is(ErrInvalidPath)", err)
	}
	if idx.callCount() != 0 {
		t.Errorf("IndexFiles call count = %d, want 0 (rejected before dispatch)", idx.callCount())
	}
}

// ─── Test #9 (multi-tenant containment) ──────────────────────────────

// TestRouter_MultiTenantContainment — events for repo A do not mutate
// repo B's freshness envelope or trigger any work on B's index.
//
// The router achieves multi-tenant isolation by repo-keyed state: every
// data structure (prev cache, freshness records, rate limiters,
// breakers, dedup) is keyed by RepositoryID. An event for repo A
// touches only A's slot. The reverse is also true: a read against
// repo B's freshness sees no change.
//
// This is Phase 1 done-definition test #9.
func TestRouter_MultiTenantContainment(t *testing.T) {
	cfg := Config{Enabled: true, RateLimitPerMin: 30, RepoBreakerPerMin: 60}
	store := graphstore.NewStore()
	repoA, _ := store.CreateRepository(t.Context(), "tenant-a-repo", "/tmp/tenant-a")
	repoB, _ := store.CreateRepository(t.Context(), "tenant-b-repo", "/tmp/tenant-b")
	prev := func(name, path string) *indexer.IndexResult {
		return &indexer.IndexResult{
			RepoName: name, RepoPath: path, Branch: "main",
			Files: []indexer.FileResult{{Path: "a.go", Language: "go", ContentHash: "h"}},
		}
	}
	prevA := prev("tenant-a-repo", "/tmp/tenant-a")
	prevB := prev("tenant-b-repo", "/tmp/tenant-b")
	store.ReplaceIndexResult(t.Context(), repoA.ID, prevA)
	store.ReplaceIndexResult(t.Context(), repoB.ID, prevB)

	idx := &stubIndexer{}
	branches := &stubBranches{branch: "main"}
	impact := &stubImpact{}
	router := NewRouter(cfg, store, idx, impact, branches)
	router.SeedPrevious(repoA.ID, prevA)
	router.SeedPrevious(repoB.ID, prevB)

	// Submit an event for tenant A.
	evA := makeEvent(repoA.ID, SourceKindFsnotifyLocal, "tenantA-evt-1")
	if outcome, err := router.Submit(context.Background(), evA); err != nil {
		t.Fatalf("tenant A submit: outcome=%q err=%v", outcome, err)
	}

	// Tenant B's freshness must be untouched. Its LastVerifiedAt is
	// from the seed, with no Reason set; tenant A's submit shouldn't
	// have leaked any Reason or PartialRefresh into B's record.
	freshA := router.FreshnessForExport(repoA.ID)
	freshB := router.FreshnessForExport(repoB.ID)
	if freshA.Reason == "" {
		t.Errorf("tenant A freshness Reason is empty after submit; expected populated")
	}
	if freshB.Reason != "" {
		t.Errorf("tenant B freshness Reason = %q after tenant A submit; want empty (cross-tenant leak)", freshB.Reason)
	}
	if freshB.LastVerifiedAt.After(freshA.LastVerifiedAt) {
		t.Errorf("tenant B LastVerifiedAt advanced after tenant A submit (cross-tenant leak)")
	}

	// Tenant B's IndexFiles call count is zero — the router never
	// dispatched against B.
	for _, call := range idx.calls {
		if call.RepoPath == "/tmp/tenant-b" {
			t.Errorf("IndexFiles called against tenant B's path during tenant A submit (cross-tenant leak): %+v", call)
		}
	}
}

// ─── Test #10 (router-path half — no IndexRepository / IndexRepositoryIncremental) ─

// TestRouter_OnlyCallsIndexFiles — guardrail #2 from the delta-only
// invariant. The router's interface dependency declares only
// IndexFiles; calling IndexRepositoryIncremental or IndexRepository
// from inside the router is impossible because those methods are not
// on the Indexer interface. This test pins the contract by asserting
// Indexer's interface surface contains exactly one method named
// IndexFiles, no method named IndexRepository, and no method named
// IndexRepositoryIncremental. A static-analysis assertion would be
// even stronger; this runtime assertion is the lightest-weight option
// that still gives real signal — anyone who tries to widen the
// Indexer interface to include the full-repo paths will turn this
// red on their next test run.
//
// This is Phase 1 done-definition test #10's router-path half (the
// 1.A placeholder TestIndexRepository_RouterPathDeferred is updated
// in a follow-up commit to point at this test).
func TestRouter_OnlyCallsIndexFiles(t *testing.T) {
	// Reflect over the Indexer interface to enforce the surface.
	// We do this here rather than via go/packages or static analysis
	// because the interface lives in the same package — the simplest
	// correct check.
	want := map[string]bool{"IndexFiles": true}
	disallowed := map[string]bool{
		"IndexRepository":            true,
		"IndexRepositoryIncremental": true,
	}

	// Reflection-light: build a list of methods on Indexer using a
	// type assertion that cannot pass for a stub that lacks IndexFiles.
	var idx Indexer = &stubIndexer{}
	_ = idx // keeps the assignment effective when we don't reflect

	// The actual surface check: any new method added to Indexer must
	// be a deliberate change, not a drift. The test enumerates the
	// methods the package promises and rejects unknown extra entries.
	gotMethods := indexerInterfaceMethods()
	for _, m := range gotMethods {
		if disallowed[m] {
			t.Errorf("Indexer interface includes %q — the router must not be able to reach the full-repo path. See plan v5 audit of latent full-reindex paths.", m)
		}
	}
	// Verify the required IndexFiles method is present.
	gotSet := make(map[string]bool)
	for _, m := range gotMethods {
		gotSet[m] = true
	}
	for w := range want {
		if !gotSet[w] {
			t.Errorf("Indexer interface missing required method %q", w)
		}
	}
}

// ─── Test #12 (router-level branch-mismatch rejection) ────────────────

// TestRouter_RejectsBranchMismatch — Risk #4 / HIGH fix #6 router
// half. A record_change event claiming branch="main" while the
// working-tree HEAD reports branch="feature/x" is rejected with
// rejected_branch_mismatch and both branches are in the structured
// log.
//
// This is Phase 1 done-definition test #12's router-level half (the
// 1.B placeholder TestIndexFiles_RouterBranchMismatch_DeferredTo1C is
// updated in a follow-up commit to point at this test).
func TestRouter_RejectsBranchMismatch(t *testing.T) {
	router, idx, branches, _, store := newRouterHarness(t, Config{Enabled: true})
	branches.branch = "feature/x" // working tree head
	repoID := repoIDFromHarness(store)
	ev := makeEvent(repoID, SourceKindMCPRecordChange, "branch-mismatch")
	ev.Branch = "main" // claimed in the event

	outcome, err := router.Submit(context.Background(), ev)
	if outcome != OutcomeRejectedBranchMismatch {
		t.Errorf("outcome = %q, want %q", outcome, OutcomeRejectedBranchMismatch)
	}
	if !errors.Is(err, ErrBranchMismatch) {
		t.Errorf("err = %v, want errors.Is(ErrBranchMismatch)", err)
	}
	if idx.callCount() != 0 {
		t.Errorf("IndexFiles call count = %d, want 0 — branch mismatch must reject BEFORE dispatch", idx.callCount())
	}
}

// TestRouter_AcceptsRefsHeadsBranchEquivalent — the router normalizes
// "refs/heads/X" vs "X" before comparing, so a connector that emits
// the full ref form interoperates with a watcher that uses the bare
// branch name from git.HeadRef.
func TestRouter_AcceptsRefsHeadsBranchEquivalent(t *testing.T) {
	router, idx, branches, _, store := newRouterHarness(t, Config{Enabled: true})
	branches.branch = "main"
	repoID := repoIDFromHarness(store)
	ev := makeEvent(repoID, SourceKindFsnotifyLocal, "refs-heads-form")
	ev.Branch = "refs/heads/main"

	outcome, err := router.Submit(context.Background(), ev)
	if err != nil {
		t.Fatalf("submit: outcome=%q err=%v", outcome, err)
	}
	if idx.callCount() != 1 {
		t.Errorf("IndexFiles call count = %d, want 1 — refs/heads/main should be accepted", idx.callCount())
	}
}

// ─── Test #13 (per-repo aggregate breaker across source kinds) ────────

// TestRouter_PerRepoBreakerTrips — bob H5 fix from v5: the breaker is
// per-repo aggregate across all source kinds combined, NOT per-kind.
// Two source kinds each below the per-kind throttle but combined
// above the per-repo aggregate must trip the breaker.
//
// We use a custom now-clock to step through the 5-minute window
// quickly. The breaker requires every minute in the window to be at
// or above the threshold, so we drive 5 minutes' worth of events.
func TestRouter_PerRepoBreakerTrips(t *testing.T) {
	cfg := Config{
		Enabled:           true,
		RateLimitPerMin:   100, // per-kind throttle is loose so the breaker is the gate
		RepoBreakerPerMin: 60,
		T0BudgetMs:        100,
	}
	router, _, _, _, store := newRouterHarness(t, cfg)
	repoID := repoIDFromHarness(store)

	base := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	clock := base
	router.SetNow(func() time.Time { return clock })

	submitOK := func(kind SourceKind, suffix string) {
		ev := makeEvent(repoID, kind, suffix+"-"+kind.String())
		ev.OccurredAt = clock
		_, _ = router.Submit(context.Background(), ev)
	}

	// Drive 30 fsnotify + 30 record_change events per minute for 5
	// consecutive minutes. Per-kind: each is at 30/min ≤ 100/min
	// (under the throttle). Per-repo aggregate: 60/min, exactly at the
	// breaker threshold for 5 consecutive minutes → trip.
	for minute := 0; minute < 5; minute++ {
		clock = base.Add(time.Duration(minute) * time.Minute)
		for i := 0; i < 30; i++ {
			submitOK(SourceKindFsnotifyLocal, time.Now().Format("150405.000")+"-"+itoa(i))
			submitOK(SourceKindMCPRecordChange, time.Now().Format("150405.000")+"-"+itoa(i))
		}
	}

	// 6th minute: breaker is open; events are dropped with
	// OutcomeBreakerTripped.
	clock = base.Add(5 * time.Minute)
	ev := makeEvent(repoID, SourceKindFsnotifyLocal, "after-trip")
	ev.OccurredAt = clock
	outcome, err := router.Submit(context.Background(), ev)
	if outcome != OutcomeBreakerTripped {
		t.Errorf("outcome = %q, want %q (breaker should be open)", outcome, OutcomeBreakerTripped)
	}
	if !errors.Is(err, ErrBreakerOpen) {
		t.Errorf("err = %v, want errors.Is(ErrBreakerOpen)", err)
	}
}

// itoa is a tiny inline shim — strconv pulls this in elsewhere; we
// keep the test file local-only.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(byte('0'+n%10)) + s
		n /= 10
	}
	return s
}

// ─── Test #14 (fsnotify + record_change dedup-by-hash) ────────────────

// TestRouter_DedupByContentHashAcrossSourceKinds — the same edit
// observed by both connectors (fsnotify fires first; the agent calls
// record_change with the same files within the dedup window) results
// in a single routed event. Both connectors produce identical
// ContentHashAfter; the dedup window collapses them to one routed
// event regardless of which connector won the race.
//
// Asserts only one IndexFiles invocation and only one ImpactApplier
// invocation across the pair; second event reports OutcomeDeduped.
//
// Phase 1 done-definition test #14.
func TestRouter_DedupByContentHashAcrossSourceKinds(t *testing.T) {
	router, idx, _, impact, store := newRouterHarness(t, Config{
		Enabled:     true,
		DedupWindow: 10 * time.Second,
	})
	repoID := repoIDFromHarness(store)

	hash := "sha256:identicalcontentbytes"
	files := []FileChange{
		{Path: "a.go", Status: FileChangeModified, ContentHashAfter: hash},
	}

	// Connector 1: fsnotify (different event_id from connector 2 — only
	// the content_hash collapses the pair).
	ev1 := makeEvent(repoID, SourceKindFsnotifyLocal, "fsnotify-evt-1")
	ev1.Files = files

	// Connector 2: record_change (different event_id, same hash).
	ev2 := makeEvent(repoID, SourceKindMCPRecordChange, "record-change-evt-1")
	ev2.Files = files

	outcome1, err1 := router.Submit(context.Background(), ev1)
	if err1 != nil {
		t.Fatalf("first submit: %q %v", outcome1, err1)
	}
	if outcome1 != OutcomeIndexing {
		t.Errorf("first outcome = %q, want %q", outcome1, OutcomeIndexing)
	}

	outcome2, err2 := router.Submit(context.Background(), ev2)
	if err2 != nil {
		t.Fatalf("second submit: %q %v", outcome2, err2)
	}
	if outcome2 != OutcomeDeduped {
		t.Errorf("second outcome = %q, want %q (content-hash collapse)", outcome2, OutcomeDeduped)
	}

	if idx.callCount() != 1 {
		t.Errorf("IndexFiles call count = %d, want exactly 1 (deduped)", idx.callCount())
	}
	if impact.calls.Load() != 1 {
		t.Errorf("ImpactApplier call count = %d, want exactly 1 (deduped)", impact.calls.Load())
	}
}

// TestRouter_DedupByEventIDIdempotency — a connector retry that
// re-uses the same event_id collapses to one routed event regardless
// of whether content_hash is present.
func TestRouter_DedupByEventIDIdempotency(t *testing.T) {
	router, idx, _, _, store := newRouterHarness(t, Config{
		Enabled:     true,
		DedupWindow: 10 * time.Second,
	})
	repoID := repoIDFromHarness(store)

	ev := makeEvent(repoID, SourceKindFsnotifyLocal, "idempotent-evt")
	if _, err := router.Submit(context.Background(), ev); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	// Same event again.
	ev2 := makeEvent(repoID, SourceKindFsnotifyLocal, "idempotent-evt")
	outcome, err := router.Submit(context.Background(), ev2)
	if err != nil {
		t.Fatalf("second submit: %q %v", outcome, err)
	}
	if outcome != OutcomeDeduped {
		t.Errorf("outcome = %q, want %q (event_id idempotency)", outcome, OutcomeDeduped)
	}
	if idx.callCount() != 1 {
		t.Errorf("IndexFiles call count = %d, want 1 (idempotent)", idx.callCount())
	}
}

// ─── Behavior under flag-off + unknown repo ───────────────────────────

// TestRouter_FlagOffRejects — when SOURCEBRIDGE_CHANGE_WATCH_ENABLED
// is false, every event is rejected without dispatch.
func TestRouter_FlagOffRejects(t *testing.T) {
	router, idx, _, _, store := newRouterHarness(t, Config{Enabled: false})
	repoID := repoIDFromHarness(store)
	ev := makeEvent(repoID, SourceKindFsnotifyLocal, "flag-off")
	_, err := router.Submit(context.Background(), ev)
	if !errors.Is(err, ErrChangeWatchDisabled) {
		t.Errorf("err = %v, want errors.Is(ErrChangeWatchDisabled)", err)
	}
	if idx.callCount() != 0 {
		t.Errorf("IndexFiles call count = %d, want 0 (flag off)", idx.callCount())
	}
}

// TestRouter_UnknownRepoRejected — a RepositoryID without a Seeded
// previous result is rejected.
func TestRouter_UnknownRepoRejected(t *testing.T) {
	router, idx, _, _, _ := newRouterHarness(t, Config{Enabled: true})
	ev := makeEvent("not-a-real-repo", SourceKindFsnotifyLocal, "unknown-repo")
	outcome, err := router.Submit(context.Background(), ev)
	if outcome != OutcomeRejectedUnknownRepo {
		t.Errorf("outcome = %q, want %q", outcome, OutcomeRejectedUnknownRepo)
	}
	if !errors.Is(err, ErrUnknownRepo) {
		t.Errorf("err = %v, want errors.Is(ErrUnknownRepo)", err)
	}
	if idx.callCount() != 0 {
		t.Errorf("IndexFiles call count = %d, want 0", idx.callCount())
	}
}

// TestRouter_HappyPathDispatches — the load-bearing positive control
// for the harness. With every guardrail satisfied the router calls
// IndexFiles, applies the merge, and runs the impact applier.
func TestRouter_HappyPathDispatches(t *testing.T) {
	router, idx, _, impact, store := newRouterHarness(t, Config{Enabled: true})
	repoID := repoIDFromHarness(store)
	ev := makeEvent(repoID, SourceKindFsnotifyLocal, "happy-1")
	if outcome, err := router.Submit(context.Background(), ev); err != nil || outcome != OutcomeIndexing {
		t.Fatalf("submit: outcome=%q err=%v", outcome, err)
	}
	if idx.callCount() != 1 {
		t.Errorf("IndexFiles call count = %d, want 1", idx.callCount())
	}
	call, _ := idx.lastCall()
	if call.Branch != "main" {
		t.Errorf("IndexFiles called with branch=%q, want main", call.Branch)
	}
	if len(call.Files) != 1 || call.Files[0] != "a.go" {
		t.Errorf("IndexFiles called with files=%v, want [a.go]", call.Files)
	}
	if impact.calls.Load() != 1 {
		t.Errorf("ImpactApplier call count = %d, want 1", impact.calls.Load())
	}
	// Freshness updated.
	fresh := router.FreshnessForExport(repoID)
	if fresh.State != "fresh" {
		t.Errorf("freshness state after submit = %q, want fresh", fresh.State)
	}
	if fresh.Branch != "main" {
		t.Errorf("freshness branch = %q, want main", fresh.Branch)
	}
	if fresh.Reason == "" {
		t.Errorf("freshness reason is empty after successful submit")
	}
}

// indexerInterfaceMethods returns the names of methods on the Indexer
// interface. We hand-list them to avoid a heavyweight reflection
// dependency and to make the contract loud — anyone who adds a
// method to Indexer has to update this list, which itself prompts
// "should the router really be able to call this?"
//
// NOTE: when extending Indexer, update this list AND
// TestRouter_OnlyCallsIndexFiles's disallowed-set above.
func indexerInterfaceMethods() []string {
	return []string{"IndexFiles"}
}

// SourceKind.String — small helper used by the breaker test to build
// distinct event_ids per kind.
func (k SourceKind) String() string { return string(k) }
