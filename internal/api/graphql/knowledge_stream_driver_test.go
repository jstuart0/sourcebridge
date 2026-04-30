// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	commonv1 "github.com/sourcebridge/sourcebridge/gen/go/common/v1"
	"github.com/sourcebridge/sourcebridge/internal/worker"
)

// streamFakeRuntime is a minimal llm.Runtime used by stream-driver
// tests. We only care about ReportProgress; everything else is a
// no-op. Named to avoid colliding with fakeRuntime in
// living_wiki_coldstart_test.go.
type streamFakeRuntime struct {
	mu       sync.Mutex
	progress []reportedProgress
}

type reportedProgress struct {
	pct     float64
	phase   string
	message string
}

func (f *streamFakeRuntime) JobID() string { return "fake-job" }

func (f *streamFakeRuntime) ReportProgress(p float64, phase, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.progress = append(f.progress, reportedProgress{pct: p, phase: phase, message: message})
}

func (f *streamFakeRuntime) ReportTokens(int, int)   {}
func (f *streamFakeRuntime) ReportSnapshotBytes(int) {}
func (f *streamFakeRuntime) Heartbeat() error        { return nil }

func (f *streamFakeRuntime) snapshot() []reportedProgress {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]reportedProgress, len(f.progress))
	copy(out, f.progress)
	return out
}

// TestStreamProgressDriverWritesPhaseAndProgress confirms that a phase
// marker writes both rt.ReportProgress and the row writer (the plan's
// "BOTH writes" guarantee), and that a subsequent progress event with
// counters lands on the bucket-mapped percentage.
func TestStreamProgressDriverWritesPhaseAndProgress(t *testing.T) {
	rt := &streamFakeRuntime{}
	var (
		writes []reportedProgress
		mu     sync.Mutex
	)
	write := func(p float64, phase, message string) error {
		mu.Lock()
		defer mu.Unlock()
		writes = append(writes, reportedProgress{pct: p, phase: phase, message: message})
		return nil
	}

	d := newStreamProgressDriver(rt, write, rpcBucketCollapsed, "artifact_id", "test")
	defer d.Close()

	handler := d.OnProgress()
	handler(worker.KnowledgeStreamEvent{Phase: &commonv1.KnowledgeStreamPhaseMarker{
		Phase: commonv1.KnowledgePhase_KNOWLEDGE_PHASE_RENDER,
	}})
	handler(worker.KnowledgeStreamEvent{Progress: &commonv1.KnowledgeStreamProgress{
		Phase:          commonv1.KnowledgePhase_KNOWLEDGE_PHASE_RENDER,
		CompletedUnits: 1,
		TotalUnits:     2,
		UnitKind:       "section",
		Message:        "rendering",
	}})
	d.Close()

	rtSnap := rt.snapshot()
	mu.Lock()
	defer mu.Unlock()

	if len(rtSnap) < 2 {
		t.Fatalf("expected at least 2 ReportProgress calls, got %d", len(rtSnap))
	}
	if len(writes) < 2 {
		t.Fatalf("expected at least 2 row writes, got %d", len(writes))
	}

	// Phase marker lands at bucket-min (collapsed RENDER bucket = 0.20).
	if rtSnap[0].pct != 0.20 {
		t.Fatalf("expected phase marker pct=0.20, got %v", rtSnap[0].pct)
	}
	// Progress with completed/total = 1/2 inside RENDER bucket
	// (0.20 -> 0.97) lands halfway through: 0.20 + 0.5*0.77 = 0.585.
	const want = 0.585
	if got := rtSnap[1].pct; got < want-1e-6 || got > want+1e-6 {
		t.Fatalf("expected progress event pct=%v, got %v", want, got)
	}
	if rtSnap[1].message != "rendering" {
		t.Fatalf("expected message 'rendering', got %q", rtSnap[1].message)
	}
}

// TestStreamProgressDriverDropsOldestUnderBackpressure burst-feeds the
// driver while the writer is paused, then unblocks the writer and
// verifies the LATEST event (highest progress) is preserved (codex r2
// M2). The previous "drop newest" behavior was the documented bug.
func TestStreamProgressDriverDropsOldestUnderBackpressure(t *testing.T) {
	rt := &streamFakeRuntime{}
	// Block the writer until a gate releases it. While blocked the
	// channel will fill and OnProgress must rotate (drop oldest, keep
	// newest) for monotonic progress to be preserved.
	gate := make(chan struct{})
	var lastSeen atomic.Int32
	var writeMu sync.Mutex
	var writes []float64
	write := func(p float64, phase, message string) error {
		<-gate
		writeMu.Lock()
		writes = append(writes, p)
		lastSeen.Store(int32(len(writes)))
		writeMu.Unlock()
		return nil
	}

	d := newStreamProgressDriver(rt, write, rpcBucketCollapsed, "artifact_id", "burst")
	handler := d.OnProgress()

	// Burst 100 events with strictly increasing progress counters
	// inside the RENDER bucket.
	const burst = 100
	for i := 0; i < burst; i++ {
		handler(worker.KnowledgeStreamEvent{Progress: &commonv1.KnowledgeStreamProgress{
			Phase:          commonv1.KnowledgePhase_KNOWLEDGE_PHASE_RENDER,
			CompletedUnits: int32(i + 1),
			TotalUnits:     burst,
			UnitKind:       "section",
		}})
	}
	// Release the writer; let it drain whatever survived in the queue.
	close(gate)
	d.Close()

	writeMu.Lock()
	defer writeMu.Unlock()
	if len(writes) == 0 {
		t.Fatalf("expected at least one write to land")
	}
	// The highest stored progress percentage MUST equal the
	// bucket-max-mapped value for the final (i = burst - 1) event,
	// which corresponds to ratio = 1.0 -> bucket max = 0.97.
	max := writes[0]
	for _, p := range writes[1:] {
		if p > max {
			max = p
		}
	}
	const expected = 0.97
	if max < expected-1e-6 {
		t.Fatalf("expected drop-oldest to preserve final progress %v, got max=%v over %d writes (writes=%v)",
			expected, max, len(writes), writes)
	}
}

// TestStreamProgressDriverCloseDrains exercises the Close-before-
// terminal-state-write contract: events queued before Close are all
// written before Close returns.
func TestStreamProgressDriverCloseDrains(t *testing.T) {
	rt := &streamFakeRuntime{}
	var writes int32
	write := func(p float64, phase, message string) error {
		atomic.AddInt32(&writes, 1)
		// Small artificial delay to verify Close waits.
		time.Sleep(2 * time.Millisecond)
		return nil
	}
	d := newStreamProgressDriver(rt, write, rpcBucketCollapsed, "artifact_id", "drain")
	handler := d.OnProgress()
	for i := 0; i < 5; i++ {
		handler(worker.KnowledgeStreamEvent{Progress: &commonv1.KnowledgeStreamProgress{
			Phase:          commonv1.KnowledgePhase_KNOWLEDGE_PHASE_RENDER,
			CompletedUnits: int32(i + 1),
			TotalUnits:     5,
			UnitKind:       "section",
		}})
	}
	d.Close()
	got := atomic.LoadInt32(&writes)
	if got != 5 {
		t.Fatalf("expected all 5 events drained before Close returned, got %d", got)
	}
}
