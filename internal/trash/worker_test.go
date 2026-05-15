// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package trash

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/db"
)

// fakeStore counts SweepExpired invocations so the worker-loop tests
// don't need the full MemStore machinery.
// CA-398: swept chan allows tests to wait for the sweep rather than sleeping.
type fakeStore struct {
	calls  atomic.Int32
	swept  chan struct{}
	sweepOnce sync.Once
}

func newFakeStore() *fakeStore {
	return &fakeStore{swept: make(chan struct{})}
}

func (f *fakeStore) MoveToTrash(_ context.Context, _ TrashableType, _ string, _ MoveOptions) (Entry, error) {
	return Entry{}, nil
}
func (f *fakeStore) RestoreFromTrash(_ context.Context, _ TrashableType, _ string, _ RestoreOptions) (RestoreResult, error) {
	return RestoreResult{}, nil
}
func (f *fakeStore) PermanentlyDelete(_ context.Context, _ TrashableType, _ string) error {
	return nil
}
func (f *fakeStore) Get(_ context.Context, _ TrashableType, _ string) (*Entry, error) {
	return nil, nil
}
func (f *fakeStore) List(_ context.Context, _ ListFilter) ([]Entry, int, error) { return nil, 0, nil }
func (f *fakeStore) SweepExpired(_ context.Context, _ time.Duration, _ int) (int, error) {
	f.calls.Add(1)
	f.sweepOnce.Do(func() { close(f.swept) })
	return 0, nil
}

func TestWorker_SweepsOnStart(t *testing.T) {
	s := newFakeStore()
	w := NewWorker(s, nil, WorkerConfig{SweepInterval: time.Hour, RetentionDays: 7, MaxBatchSize: 1})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// CA-398: wait for the boot sweep signal instead of sleeping.
		select {
		case <-s.swept:
		case <-time.After(5 * time.Second):
		}
		cancel()
	}()
	_ = w.Run(ctx)

	if got := s.calls.Load(); got < 1 {
		t.Errorf("expected at least 1 sweep on start, got %d", got)
	}
	if !w.Stopped() {
		t.Error("worker should be marked stopped after ctx cancel")
	}
}

func TestWorker_LeaderElection_OnlyOneReplicaSweeps(t *testing.T) {
	s1, s2 := newFakeStore(), newFakeStore()
	// Shared in-memory cache simulates Redis SETNX semantics.
	cache := db.NewInMemoryCache()

	w1 := NewWorker(s1, cache, WorkerConfig{SweepInterval: time.Hour, RetentionDays: 7})
	w2 := NewWorker(s2, cache, WorkerConfig{SweepInterval: time.Hour, RetentionDays: 7})

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = w1.Run(ctx) }()
	go func() { defer wg.Done(); _ = w2.Run(ctx) }()

	// CA-398: wait until one of the two stores fires its first sweep, then
	// cancel. Using a merged channel avoids the fixed 150ms sleep while
	// still being deterministic — we cancel the moment the race is decided.
	merged := make(chan struct{}, 1)
	go func() {
		select {
		case <-s1.swept:
			merged <- struct{}{}
		case <-s2.swept:
			merged <- struct{}{}
		case <-time.After(5 * time.Second):
			merged <- struct{}{}
		}
	}()
	<-merged
	cancel()
	wg.Wait()

	total := s1.calls.Load() + s2.calls.Load()
	if total != 1 {
		t.Errorf("exactly one replica should sweep; got total calls = %d (w1=%d, w2=%d)",
			total, s1.calls.Load(), s2.calls.Load())
	}
}

func TestWorker_NilStore_ReturnsImmediately(t *testing.T) {
	w := NewWorker(nil, nil, WorkerConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = w.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker with nil store should return without spinning")
	}
}
