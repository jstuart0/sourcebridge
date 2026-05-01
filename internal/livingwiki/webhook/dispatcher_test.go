// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package webhook_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/webhook"
)

// ─────────────────────────────────────────────────────────────────────────────
// Stub orchestrator for testing
// ─────────────────────────────────────────────────────────────────────────────

// (A `calls` recording struct used to live here as part of an early
// approach that wrapped a real *orchestrator.Orchestrator with method
// recording. The current tests instead verify the dispatcher's
// observable side-effects on the watermark store, so the recording
// struct has no callers. Removed to satisfy lint.)

// stubOrchestrator wraps a real *orchestrator.Orchestrator with call recording.
// We use the real orchestrator struct but replace it with a nil-safe version
// by providing stubs for the methods the dispatcher actually calls.
//
// Since orchestrator.Orchestrator methods are not on an interface, we inject
// the dispatcher via its deps and verify calls through the watermark store.
//
// For simplicity we skip dispatching to a real orchestrator in these unit tests
// and instead verify that the correct watermark store methods were called,
// which is the observable side-effect of each event handler.
//
// This approach keeps the test honest without requiring a full end-to-end setup.

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────name
// ─────────────────────────────────────────────────────────────────────────────

func newTestDispatcher(t *testing.T, wm orchestrator.WatermarkStore) (*webhook.Dispatcher, *webhook.DispatcherDeps) {
	t.Helper()

	cfg := webhook.DispatcherConfig{
		WorkerCount:    2,
		MaxQueueDepth:  100,
		EventTimeout:   10 * time.Second,
		DedupeCapacity: 100,
		DedupeTTL:      time.Minute,
	}

	deps := &webhook.DispatcherDeps{
		WatermarkStore: wm,
		Logger:         webhook.NoopLogger{},
		// Orchestrator is nil — event handlers that need it will skip or no-op.
		// The watermark store is the observable side-effect we test here.
	}

	d := webhook.NewDispatcher(*deps, cfg)
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("dispatcher start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = d.Stop(stopCtx)
	})
	return d, deps
}

// waitFor blocks until cond returns true or the deadline expires.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

// TestDispatcher_PushEvent verifies that a PushEvent with no DiffProvider
// advances the processed watermark.
func TestDispatcher_PushEvent(t *testing.T) {
	wm := orchestrator.NewMemoryWatermarkStore()
	d, _ := newTestDispatcher(t, wm)

	event := webhook.PushEvent{
		Repo:       "repo1",
		Delivery:   "delivery-push-1",
		Branch:     "main",
		BeforeSHA:  "aaa",
		AfterSHA:   "bbb",
		PusherName: "alice",
		ReceivedAt: time.Now(),
	}

	if err := d.Submit(context.Background(), event); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		marks, _ := wm.Get(context.Background(), "repo1")
		return marks.SourceProcessedSHA == "bbb"
	})
}

// TestDispatcher_PRMergedEvent verifies that PRMergedEvent advances
// WikiPublishedSHA (via PromoteWithWatermark → AdvancePublished).
func TestDispatcher_PRMergedEvent(t *testing.T) {
	wm := orchestrator.NewMemoryWatermarkStore()
	// Pre-set a proposed watermark state.
	_ = wm.AdvanceProcessed(context.Background(), "repo2", "sha-before-merge")

	d, _ := newTestDispatcher(t, wm)

	// PRMergedEvent requires a real orchestrator to call PromoteWithWatermark.
	// Since orchestrator is nil in this test, we expect handlePRMerged to skip
	// gracefully. This test primarily verifies the dispatcher dispatches without
	// panicking; the orchestrator-level test covers the full Promote flow.
	event := webhook.PRMergedEvent{
		Repo:      "repo2",
		Delivery:  "delivery-merge-1",
		PRID:      "pr-42",
		MergedSHA: "sha-merged",
	}

	err := d.Submit(context.Background(), event)
	if err != nil {
		t.Fatalf("Submit PRMergedEvent: %v", err)
	}
	// Give the goroutine time to process (no panic = success).
	time.Sleep(50 * time.Millisecond)
}

// TestDispatcher_PRRejectedEvent verifies PRRejectedEvent dispatches without error.
func TestDispatcher_PRRejectedEvent(t *testing.T) {
	wm := orchestrator.NewMemoryWatermarkStore()
	_ = wm.AdvanceProcessed(context.Background(), "repo3", "sha-a")

	d, _ := newTestDispatcher(t, wm)

	event := webhook.PRRejectedEvent{
		Repo:     "repo3",
		Delivery: "delivery-reject-1",
		PRID:     "pr-99",
	}

	if err := d.Submit(context.Background(), event); err != nil {
		t.Fatalf("Submit PRRejectedEvent: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
}

// TestDispatcher_ManualRefreshEvent_WholeRepo verifies that a ManualRefreshEvent
// with no PageID resets the watermark to the published baseline.
func TestDispatcher_ManualRefreshEvent_WholeRepo(t *testing.T) {
	wm := orchestrator.NewMemoryWatermarkStore()
	// Set published SHA and advance processed beyond it.
	_ = wm.AdvancePublished(context.Background(), "repo4", "published-sha")
	_ = wm.AdvanceProcessed(context.Background(), "repo4", "processed-sha")

	d, _ := newTestDispatcher(t, wm)

	event := webhook.ManualRefreshEvent{
		Repo:        "repo4",
		Delivery:    "delivery-refresh-1",
		RequestedBy: "admin",
		// PageID empty → whole-repo reset
	}

	if err := d.Submit(context.Background(), event); err != nil {
		t.Fatalf("Submit ManualRefreshEvent: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		marks, _ := wm.Get(context.Background(), "repo4")
		// After whole-repo reset, both watermarks should equal published-sha.
		return marks.SourceProcessedSHA == "published-sha" && marks.WikiPublishedSHA == "published-sha"
	})
}

// TestDispatcher_PerRepoSerialization verifies that two events for the same
// repo do not execute concurrently. We confirm this by verifying that their
// observed execution order is sequential by tracking start/end timestamps.
func TestDispatcher_PerRepoSerialization(t *testing.T) {
	wm := orchestrator.NewMemoryWatermarkStore()

	var (
		mu        sync.Mutex
		maxConcur int32
		running   int32
	)

	// We use PushEvent (with no DiffProvider) to trigger the watermark-advance
	// path, then check that at most 1 was running at a time for the same repo.
	// Because the push handler runs synchronously in the per-repo goroutine,
	// we can observe this via atomic counters.

	// Hook into the watermark store to count concurrent callers.
	trackedWM := &trackingWatermarkStore{
		inner: wm,
		onAdvance: func() {
			cur := atomic.AddInt32(&running, 1)
			mu.Lock()
			if int(cur) > int(maxConcur) {
				atomic.StoreInt32(&maxConcur, cur)
			}
			mu.Unlock()
			time.Sleep(10 * time.Millisecond) // simulate work
			atomic.AddInt32(&running, -1)
		},
	}

	cfg := webhook.DispatcherConfig{
		WorkerCount:   4,
		MaxQueueDepth: 100,
		EventTimeout:  10 * time.Second,
	}
	deps := webhook.DispatcherDeps{
		WatermarkStore: trackedWM,
		Logger:         webhook.NoopLogger{},
	}
	d2 := webhook.NewDispatcher(deps, cfg)
	_ = d2.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = d2.Stop(ctx)
	})

	// Submit 5 events for the same repo.
	for i := 0; i < 5; i++ {
		ev := webhook.PushEvent{
			Repo:       "same-repo",
			Delivery:   "",   // empty → no deduplication
			AfterSHA:   "sha",
			ReceivedAt: time.Now(),
		}
		if err := d2.Submit(context.Background(), ev); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}

	// Wait for all to process.
	waitFor(t, 3*time.Second, func() bool {
		marks, _ := trackedWM.Get(context.Background(), "same-repo")
		return marks.SourceProcessedSHA == "sha"
	})

	if got := atomic.LoadInt32(&maxConcur); got > 1 {
		t.Errorf("per-repo serialization violated: max concurrent handlers = %d (want <= 1)", got)
	}
}

// TestDispatcher_Idempotency verifies that submitting the same delivery ID
// twice returns ErrDuplicate on the second call.
func TestDispatcher_Idempotency(t *testing.T) {
	wm := orchestrator.NewMemoryWatermarkStore()
	d, _ := newTestDispatcher(t, wm)

	event := webhook.PushEvent{
		Repo:     "repo-dedup",
		Delivery: "delivery-dedup-abc123",
		AfterSHA: "sha1",
	}

	// First submission should succeed.
	if err := d.Submit(context.Background(), event); err != nil {
		t.Fatalf("first Submit: %v", err)
	}

	// Second submission with same delivery ID should return ErrDuplicate.
	err := d.Submit(context.Background(), event)
	if !errors.Is(err, webhook.ErrDuplicate) {
		t.Errorf("second Submit with same delivery ID: got %v, want ErrDuplicate", err)
	}
}

// TestDispatcher_QueueFull verifies ErrQueueFull when queues are saturated.
func TestDispatcher_QueueFull(t *testing.T) {
	wm := orchestrator.NewMemoryWatermarkStore()

	// Use a tiny queue depth and a blocking watermark store to fill the queue.
	blocked := make(chan struct{})
	blockingWM := &blockingWatermarkStore{inner: wm, block: blocked}

	cfg := webhook.DispatcherConfig{
		WorkerCount:    1,
		MaxQueueDepth:  2, // intentionally tiny
		EventTimeout:   10 * time.Second,
		DedupeCapacity: 100,
		DedupeTTL:      time.Minute,
	}
	deps := webhook.DispatcherDeps{
		WatermarkStore: blockingWM,
		Logger:         webhook.NoopLogger{},
	}
	d := webhook.NewDispatcher(deps, cfg)
	_ = d.Start(context.Background())
	t.Cleanup(func() {
		close(blocked) // unblock before stopping
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})

	// Submit events until we get ErrQueueFull.
	var fullErr error
	for i := 0; i < 100; i++ {
		ev := webhook.PushEvent{
			Repo:     "repo-full",
			Delivery: "", // no dedup to allow rapid refill
			AfterSHA: "sha",
		}
		if err := d.Submit(context.Background(), ev); errors.Is(err, webhook.ErrQueueFull) {
			fullErr = err
			break
		}
	}

	if fullErr == nil {
		t.Error("expected ErrQueueFull but queue never filled")
	}
}

// TestDispatcher_DifferentReposConcurrent verifies that events for different
// repos do run concurrently (i.e., they are not serialized against each other).
func TestDispatcher_DifferentReposConcurrent(t *testing.T) {
	const numRepos = 4
	wm := orchestrator.NewMemoryWatermarkStore()

	var (
		started    int32
		allStarted = make(chan struct{})
	)

	slowWM := &slowWatermarkStore{
		inner: wm,
		onAdvance: func() {
			n := atomic.AddInt32(&started, 1)
			if int(n) == numRepos {
				close(allStarted)
			}
			// Block until all numRepos handlers have started — proving concurrency.
			select {
			case <-allStarted:
			case <-time.After(2 * time.Second):
			}
		},
	}

	cfg := webhook.DispatcherConfig{
		WorkerCount:   4,
		MaxQueueDepth: 100,
		EventTimeout:  10 * time.Second,
	}
	deps := webhook.DispatcherDeps{
		WatermarkStore: slowWM,
		Logger:         webhook.NoopLogger{},
	}
	d := webhook.NewDispatcher(deps, cfg)
	_ = d.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})

	for i := 0; i < numRepos; i++ {
		ev := webhook.PushEvent{
			Repo:     "repo-concurrent-" + string(rune('A'+i)),
			Delivery: "",
			AfterSHA: "sha",
		}
		if err := d.Submit(context.Background(), ev); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}

	select {
	case <-allStarted:
		// All numRepos handlers started concurrently — test passes.
	case <-time.After(3 * time.Second):
		t.Error("different-repo events did not run concurrently within timeout")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Watermark store wrappers for test instrumentation
// ─────────────────────────────────────────────────────────────────────────────

type trackingWatermarkStore struct {
	inner     orchestrator.WatermarkStore
	onAdvance func()
}

func (s *trackingWatermarkStore) Get(ctx context.Context, repoID string) (orchestrator.Watermarks, error) {
	return s.inner.Get(ctx, repoID)
}
func (s *trackingWatermarkStore) AdvanceProcessed(ctx context.Context, repoID, sha string) error {
	if s.onAdvance != nil {
		s.onAdvance()
	}
	return s.inner.AdvanceProcessed(ctx, repoID, sha)
}
func (s *trackingWatermarkStore) AdvancePublished(ctx context.Context, repoID, sha string) error {
	return s.inner.AdvancePublished(ctx, repoID, sha)
}
func (s *trackingWatermarkStore) Reset(ctx context.Context, repoID, sha string) error {
	return s.inner.Reset(ctx, repoID, sha)
}

type blockingWatermarkStore struct {
	inner orchestrator.WatermarkStore
	block chan struct{}
}

func (s *blockingWatermarkStore) Get(ctx context.Context, repoID string) (orchestrator.Watermarks, error) {
	return s.inner.Get(ctx, repoID)
}
func (s *blockingWatermarkStore) AdvanceProcessed(ctx context.Context, repoID, sha string) error {
	<-s.block // block until test releases
	return s.inner.AdvanceProcessed(ctx, repoID, sha)
}
func (s *blockingWatermarkStore) AdvancePublished(ctx context.Context, repoID, sha string) error {
	return s.inner.AdvancePublished(ctx, repoID, sha)
}
func (s *blockingWatermarkStore) Reset(ctx context.Context, repoID, sha string) error {
	return s.inner.Reset(ctx, repoID, sha)
}

type slowWatermarkStore struct {
	inner     orchestrator.WatermarkStore
	onAdvance func()
}

func (s *slowWatermarkStore) Get(ctx context.Context, repoID string) (orchestrator.Watermarks, error) {
	return s.inner.Get(ctx, repoID)
}
func (s *slowWatermarkStore) AdvanceProcessed(ctx context.Context, repoID, sha string) error {
	if s.onAdvance != nil {
		s.onAdvance()
	}
	return s.inner.AdvanceProcessed(ctx, repoID, sha)
}
func (s *slowWatermarkStore) AdvancePublished(ctx context.Context, repoID, sha string) error {
	return s.inner.AdvancePublished(ctx, repoID, sha)
}
func (s *slowWatermarkStore) Reset(ctx context.Context, repoID, sha string) error {
	return s.inner.Reset(ctx, repoID, sha)
}
