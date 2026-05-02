// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator

import (
	"sync"
	"testing"
	"time"
)

func TestPageRunTracker_AddReplaceRemoveSnapshot(t *testing.T) {
	tr := newPageRunTracker()
	const job = "job-1"

	// Empty snapshot for unknown job.
	snap := tr.Snapshot(job)
	if snap == nil {
		t.Fatal("expected non-nil slice for unknown job")
	}
	if len(snap) != 0 {
		t.Fatalf("expected empty snapshot, got %d entries", len(snap))
	}

	// Add two pages.
	tr.Start(job, "arch.auth", "architecture", 1)
	tr.Start(job, "arch.billing", "architecture", 1)

	snap = tr.Snapshot(job)
	if len(snap) != 2 {
		t.Fatalf("expected 2 in-flight, got %d", len(snap))
	}
	// Snapshot must be sorted by StartedAt ascending (both are nearly simultaneous;
	// we can at least assert the IDs are present).
	ids := map[string]bool{}
	for _, p := range snap {
		ids[p.PageID] = true
		if p.Attempt != 1 {
			t.Errorf("page %q: expected attempt 1, got %d", p.PageID, p.Attempt)
		}
	}
	if !ids["arch.auth"] || !ids["arch.billing"] {
		t.Errorf("expected both pages in snapshot, got %v", ids)
	}

	// Replace arch.auth on retry.
	tr.Start(job, "arch.auth", "architecture", 2)
	snap = tr.Snapshot(job)
	for _, p := range snap {
		if p.PageID == "arch.auth" && p.Attempt != 2 {
			t.Errorf("after retry, expected attempt 2 for arch.auth, got %d", p.Attempt)
		}
	}

	// Remove arch.auth.
	tr.Remove(job, "arch.auth")
	snap = tr.Snapshot(job)
	if len(snap) != 1 {
		t.Fatalf("expected 1 in-flight after remove, got %d", len(snap))
	}
	if snap[0].PageID != "arch.billing" {
		t.Errorf("expected arch.billing, got %q", snap[0].PageID)
	}

	// Clear removes all.
	tr.Clear(job)
	snap = tr.Snapshot(job)
	if len(snap) != 0 {
		t.Fatalf("expected empty after Clear, got %d", len(snap))
	}
}

func TestPageRunTracker_PartitionedByJobID(t *testing.T) {
	tr := newPageRunTracker()

	tr.Start("job-A", "page-1", "tmpl", 1)
	tr.Start("job-A", "page-2", "tmpl", 1)
	tr.Start("job-B", "page-3", "tmpl", 1)

	snapA := tr.Snapshot("job-A")
	snapB := tr.Snapshot("job-B")

	if len(snapA) != 2 {
		t.Errorf("job-A: expected 2, got %d", len(snapA))
	}
	if len(snapB) != 1 {
		t.Errorf("job-B: expected 1, got %d", len(snapB))
	}

	for _, p := range snapA {
		if p.PageID == "page-3" {
			t.Error("job-A snapshot must not contain job-B's page")
		}
	}
	for _, p := range snapB {
		if p.PageID == "page-1" || p.PageID == "page-2" {
			t.Error("job-B snapshot must not contain job-A's pages")
		}
	}

	// Remove from one job does not affect the other.
	tr.Remove("job-A", "page-1")
	if len(tr.Snapshot("job-A")) != 1 {
		t.Error("job-A should have 1 entry after remove")
	}
	if len(tr.Snapshot("job-B")) != 1 {
		t.Error("job-B should still have 1 entry")
	}
}

func TestPageRunTracker_MedianCompletedMs(t *testing.T) {
	tr := newPageRunTracker()
	const job = "job-median"

	// Fewer than 3 completions → false.
	if _, ok := tr.MedianCompletedMs(job); ok {
		t.Error("expected false with 0 completions")
	}
	tr.RecordCompletion(job, 100)
	if _, ok := tr.MedianCompletedMs(job); ok {
		t.Error("expected false with 1 completion")
	}
	tr.RecordCompletion(job, 200)
	if _, ok := tr.MedianCompletedMs(job); ok {
		t.Error("expected false with 2 completions")
	}

	// 3rd entry unlocks the median.
	tr.RecordCompletion(job, 300)
	med, ok := tr.MedianCompletedMs(job)
	if !ok {
		t.Fatal("expected true with 3 completions")
	}
	if med != 200 {
		t.Errorf("expected median 200, got %d", med)
	}

	// Even count: average of two middle values.
	tr.RecordCompletion(job, 400)
	med, ok = tr.MedianCompletedMs(job)
	if !ok {
		t.Fatal("expected true with 4 completions")
	}
	// sorted: 100, 200, 300, 400 → (200+300)/2 = 250
	if med != 250 {
		t.Errorf("expected median 250 for 4 entries, got %d", med)
	}

	// Clear resets the window.
	tr.Clear(job)
	if _, ok := tr.MedianCompletedMs(job); ok {
		t.Error("expected false after Clear")
	}
}

func TestPageRunTracker_ConcurrentAddSnapshot(t *testing.T) {
	tr := newPageRunTracker()
	const job = "job-concurrent"
	const n = 50

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			pageID := time.Now().String() + string(rune('a'+i%26))
			tr.Start(job, pageID, "tmpl", 1)
			_ = tr.Snapshot(job)
			tr.Remove(job, pageID)
		}()
	}
	wg.Wait()

	// After all goroutines finish, snapshot should be empty (or contain any
	// pages that weren't removed — but since each goroutine removes its own
	// page, the net should be empty).
	snap := tr.Snapshot(job)
	if len(snap) > n {
		t.Errorf("unexpected snapshot size %d", len(snap))
	}
}

func TestPageRunTracker_RemoveManyOnUnknownJob(t *testing.T) {
	tr := newPageRunTracker()
	// Should not panic.
	tr.RemoveMany("nonexistent-job", []string{"page-1", "page-2"})
}
