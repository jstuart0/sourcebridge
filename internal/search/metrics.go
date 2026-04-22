// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package search

import (
	"sort"
	"sync"
	"time"
)

// Metrics is a lightweight in-process ring buffer for per-stage
// latency and success counts. The Service.Metrics field accepts this
// sink via MetricsSink so tests can swap it for a fake.
//
// At the scales we care about for this subsystem (interactive search
// traffic), a fixed-size ring buffer plus a fresh sort at query time
// is cheap enough that we don't need to bring in a percentile lib.
type Metrics struct {
	mu       sync.RWMutex
	capacity int
	buckets  map[string]*metricsBucket
}

type metricsBucket struct {
	samples []metricsSample
}

type metricsSample struct {
	durationMs float64
	ok         bool
	ts         time.Time
}

// NewMetrics returns a Metrics sink with the given per-stage sample
// capacity. 0 → 512 (comfortably larger than the orchestrator's 256
// because search is called more often than LLM jobs).
func NewMetrics(capacity int) *Metrics {
	if capacity <= 0 {
		capacity = 512
	}
	return &Metrics{
		capacity: capacity,
		buckets:  make(map[string]*metricsBucket),
	}
}

// Record implements MetricsSink.
func (m *Metrics) Record(stage string, durationMs float64, ok bool) {
	if m == nil || stage == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, present := m.buckets[stage]
	if !present {
		b = &metricsBucket{samples: make([]metricsSample, 0, m.capacity)}
		m.buckets[stage] = b
	}
	if len(b.samples) >= m.capacity {
		b.samples = b.samples[1:]
	}
	b.samples = append(b.samples, metricsSample{
		durationMs: durationMs,
		ok:         ok,
		ts:         time.Now(),
	})
}

// StageSnapshot is the view of one stage's recent samples computed on
// demand. Safe to return by value.
type StageSnapshot struct {
	Stage      string  `json:"stage"`
	Count      int     `json:"count"`
	SuccessPct float64 `json:"success_pct"`
	P50Ms      float64 `json:"p50_ms"`
	P95Ms      float64 `json:"p95_ms"`
	P99Ms      float64 `json:"p99_ms"`
}

// Snapshot returns a per-stage summary computed from the current
// sample buffer. The samples themselves are not modified.
func (m *Metrics) Snapshot() []StageSnapshot {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]StageSnapshot, 0, len(m.buckets))
	for stage, b := range m.buckets {
		if len(b.samples) == 0 {
			continue
		}
		latencies := make([]float64, 0, len(b.samples))
		ok := 0
		for _, s := range b.samples {
			latencies = append(latencies, s.durationMs)
			if s.ok {
				ok++
			}
		}
		sort.Float64s(latencies)
		out = append(out, StageSnapshot{
			Stage:      stage,
			Count:      len(latencies),
			SuccessPct: 100.0 * float64(ok) / float64(len(latencies)),
			P50Ms:      percentile(latencies, 0.50),
			P95Ms:      percentile(latencies, 0.95),
			P99Ms:      percentile(latencies, 0.99),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Stage < out[j].Stage })
	return out
}

// percentile returns the q-th percentile of a pre-sorted float slice.
// q is in [0,1]. Empty slice → 0.
func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(float64(len(sorted)-1) * q)
	return sorted[idx]
}
