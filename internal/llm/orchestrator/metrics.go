// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator

import (
	"sort"
	"sync"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/llm"
)

// metrics is an in-memory ring buffer of recent job durations and
// terminal statuses, used to compute p50/p95 latency and success rate
// per (subsystem, job_type). The Monitor page's top strip reads from here.
//
// It's intentionally simple: a fixed-size ring buffer per bucket and a
// fresh sort at query time. At the scales we care about (hundreds of
// jobs per hour on a single-worker deployment), this is cheap enough to
// avoid bringing in a dedicated percentile library.
type metrics struct {
	mu      sync.RWMutex
	buckets map[metricsKey]*metricsBucket
	// capacity caps the number of samples retained per bucket. Older
	// samples are discarded when capacity is reached.
	capacity int
}

type metricsKey struct {
	Subsystem llm.Subsystem
	JobType   string
}

type metricsBucket struct {
	samples []metricsSample
}

type metricsSample struct {
	durationMs int64
	status     llm.JobStatus
	ts         time.Time
}

// newMetrics creates a fresh metrics recorder with the supplied per-bucket
// sample capacity. Pass 0 to use a sensible default (256).
func newMetrics(capacity int) *metrics {
	if capacity <= 0 {
		capacity = 256
	}
	return &metrics{
		buckets:  make(map[metricsKey]*metricsBucket),
		capacity: capacity,
	}
}

// record stores one terminal-state sample. The orchestrator calls this
// from its terminal transitions (ready / failed / cancelled).
func (m *metrics) record(subsystem llm.Subsystem, jobType string, duration time.Duration, status llm.JobStatus) {
	if subsystem == "" && jobType == "" {
		return
	}
	key := metricsKey{Subsystem: subsystem, JobType: jobType}
	m.mu.Lock()
	defer m.mu.Unlock()
	bucket, ok := m.buckets[key]
	if !ok {
		bucket = &metricsBucket{samples: make([]metricsSample, 0, m.capacity)}
		m.buckets[key] = bucket
	}
	if len(bucket.samples) >= m.capacity {
		// Drop the oldest sample.
		bucket.samples = bucket.samples[1:]
	}
	bucket.samples = append(bucket.samples, metricsSample{
		durationMs: duration.Milliseconds(),
		status:     status,
		ts:         time.Now(),
	})
}

// Snapshot is a point-in-time view of the metrics for the Monitor page.
type Snapshot struct {
	BySubsystem map[string]SubsystemStats `json:"by_subsystem"`
	ByJobType   map[string]JobTypeStats   `json:"by_job_type"`
	Overall     OverallStats              `json:"overall"`
}

// SubsystemStats aggregates metrics across every job type in a subsystem.
type SubsystemStats struct {
	Total        int     `json:"total"`
	Succeeded    int     `json:"succeeded"`
	Failed       int     `json:"failed"`
	Cancelled    int     `json:"cancelled"`
	P50LatencyMs int64   `json:"p50_latency_ms"`
	P95LatencyMs int64   `json:"p95_latency_ms"`
	SuccessRate  float64 `json:"success_rate"`
}

// JobTypeStats aggregates metrics for one (subsystem, job_type) bucket.
type JobTypeStats struct {
	Subsystem    string  `json:"subsystem"`
	JobType      string  `json:"job_type"`
	Total        int     `json:"total"`
	Succeeded    int     `json:"succeeded"`
	Failed       int     `json:"failed"`
	P50LatencyMs int64   `json:"p50_latency_ms"`
	P95LatencyMs int64   `json:"p95_latency_ms"`
	SuccessRate  float64 `json:"success_rate"`
}

// OverallStats is the "everything" summary for the Monitor page top strip.
type OverallStats struct {
	Total        int     `json:"total"`
	Succeeded    int     `json:"succeeded"`
	Failed       int     `json:"failed"`
	P50LatencyMs int64   `json:"p50_latency_ms"`
	P95LatencyMs int64   `json:"p95_latency_ms"`
	SuccessRate  float64 `json:"success_rate"`
}

// Snapshot returns a point-in-time metrics snapshot. The result can be
// serialized directly to JSON for the Monitor endpoint.
func (m *metrics) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snap := Snapshot{
		BySubsystem: make(map[string]SubsystemStats),
		ByJobType:   make(map[string]JobTypeStats),
	}

	// Per-job-type stats + collectors for subsystem and overall rollups.
	subsystemDurations := make(map[llm.Subsystem][]int64)
	subsystemCounts := make(map[llm.Subsystem]*counts)
	overallDurations := make([]int64, 0, 1024)
	overall := &counts{}

	for key, bucket := range m.buckets {
		durations := make([]int64, 0, len(bucket.samples))
		typeCounts := &counts{}
		for _, s := range bucket.samples {
			durations = append(durations, s.durationMs)
			typeCounts.add(s.status)
			overallDurations = append(overallDurations, s.durationMs)
			overall.add(s.status)
			subsystemDurations[key.Subsystem] = append(subsystemDurations[key.Subsystem], s.durationMs)
			if _, ok := subsystemCounts[key.Subsystem]; !ok {
				subsystemCounts[key.Subsystem] = &counts{}
			}
			subsystemCounts[key.Subsystem].add(s.status)
		}
		p50, p95 := percentiles(durations)
		bucketKey := string(key.Subsystem) + "/" + key.JobType
		snap.ByJobType[bucketKey] = JobTypeStats{
			Subsystem:    string(key.Subsystem),
			JobType:      key.JobType,
			Total:        typeCounts.total(),
			Succeeded:    typeCounts.ready,
			Failed:       typeCounts.failed,
			P50LatencyMs: p50,
			P95LatencyMs: p95,
			SuccessRate:  typeCounts.successRate(),
		}
	}

	for subsystem, durations := range subsystemDurations {
		p50, p95 := percentiles(durations)
		c := subsystemCounts[subsystem]
		snap.BySubsystem[string(subsystem)] = SubsystemStats{
			Total:        c.total(),
			Succeeded:    c.ready,
			Failed:       c.failed,
			Cancelled:    c.cancelled,
			P50LatencyMs: p50,
			P95LatencyMs: p95,
			SuccessRate:  c.successRate(),
		}
	}

	p50, p95 := percentiles(overallDurations)
	snap.Overall = OverallStats{
		Total:        overall.total(),
		Succeeded:    overall.ready,
		Failed:       overall.failed,
		P50LatencyMs: p50,
		P95LatencyMs: p95,
		SuccessRate:  overall.successRate(),
	}
	return snap
}

// counts tallies terminal job statuses for a bucket.
type counts struct {
	ready     int
	failed    int
	cancelled int
}

func (c *counts) add(status llm.JobStatus) {
	switch status {
	case llm.StatusReady:
		c.ready++
	case llm.StatusFailed:
		c.failed++
	case llm.StatusCancelled:
		c.cancelled++
	}
}

func (c *counts) total() int { return c.ready + c.failed + c.cancelled }

func (c *counts) successRate() float64 {
	total := c.total()
	if total == 0 {
		return 0
	}
	return float64(c.ready) / float64(total)
}

// percentiles returns (p50, p95) in milliseconds from the supplied
// sample durations. Returns (0, 0) for an empty slice. The input is
// sorted in place; callers that care about order should copy first.
func percentiles(durations []int64) (int64, int64) {
	if len(durations) == 0 {
		return 0, 0
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p50 := durations[int(float64(len(durations))*0.5)]
	p95Idx := int(float64(len(durations)) * 0.95)
	if p95Idx >= len(durations) {
		p95Idx = len(durations) - 1
	}
	p95 := durations[p95Idx]
	return p50, p95
}
