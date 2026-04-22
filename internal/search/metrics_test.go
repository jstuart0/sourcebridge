// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package search

import "testing"

func TestMetrics_SnapshotComputesPercentiles(t *testing.T) {
	m := NewMetrics(0)
	for i := 1; i <= 100; i++ {
		m.Record("fts", float64(i), true)
	}
	snap := m.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("want 1 stage, got %d", len(snap))
	}
	ss := snap[0]
	if ss.Stage != "fts" {
		t.Errorf("stage: %q", ss.Stage)
	}
	if ss.Count != 100 {
		t.Errorf("count: %d", ss.Count)
	}
	if ss.SuccessPct < 99.9 {
		t.Errorf("success pct: %.2f", ss.SuccessPct)
	}
	// p50 ≈ 50, p95 ≈ 95, p99 ≈ 99 for uniform 1..100.
	if ss.P50Ms < 40 || ss.P50Ms > 60 {
		t.Errorf("p50: %.2f", ss.P50Ms)
	}
	if ss.P95Ms < 90 || ss.P95Ms > 99 {
		t.Errorf("p95: %.2f", ss.P95Ms)
	}
	if ss.P99Ms < 95 || ss.P99Ms > 100 {
		t.Errorf("p99: %.2f", ss.P99Ms)
	}
}

func TestMetrics_RingBufferCaps(t *testing.T) {
	m := NewMetrics(10)
	for i := 0; i < 25; i++ {
		m.Record("exact", float64(i), true)
	}
	snap := m.Snapshot()
	if snap[0].Count != 10 {
		t.Errorf("ring buffer did not cap at 10: %d", snap[0].Count)
	}
}

func TestMetrics_SuccessPctReflectsFailures(t *testing.T) {
	m := NewMetrics(0)
	for i := 0; i < 4; i++ {
		m.Record("vector", 10, true)
	}
	for i := 0; i < 6; i++ {
		m.Record("vector", 10, false)
	}
	snap := m.Snapshot()
	if snap[0].SuccessPct != 40.0 {
		t.Errorf("success pct: want 40, got %.2f", snap[0].SuccessPct)
	}
}
