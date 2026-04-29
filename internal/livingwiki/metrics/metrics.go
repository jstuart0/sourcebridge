// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package metrics holds in-process counters and histograms for the living-wiki
// runtime. The codebase uses a hand-written /metrics endpoint (see
// internal/api/rest/health.go) rather than the prometheus-client-go library,
// so this package follows the same pattern: atomic counters and
// fixed-width histogram buckets implemented with sync/atomic.
//
// Callers integrate via the package-level [Default] collector.
// The server's handleMetrics handler calls [Default.WritePrometheusText]
// to include the living-wiki series in the existing /metrics output.
package metrics

import (
	"fmt"
	"io"
	"math"
	"sync"
	"sync/atomic"
)

// ─────────────────────────────────────────────────────────────────────────────
// Label-keyed counter
// ─────────────────────────────────────────────────────────────────────────────

// labelCounter is a thread-safe counter keyed by an arbitrary label string.
type labelCounter struct {
	mu      sync.Mutex
	entries map[string]*atomic.Int64
}

func newLabelCounter() *labelCounter {
	return &labelCounter{entries: make(map[string]*atomic.Int64)}
}

// Inc increments the counter for the given label key.
func (c *labelCounter) Inc(key string) {
	c.mu.Lock()
	e, ok := c.entries[key]
	if !ok {
		e = new(atomic.Int64)
		c.entries[key] = e
	}
	c.mu.Unlock()
	e.Add(1)
}

// Snapshot returns a copy of all label→count pairs at the moment of the call.
func (c *labelCounter) Snapshot() map[string]int64 {
	c.mu.Lock()
	out := make(map[string]int64, len(c.entries))
	for k, v := range c.entries {
		out[k] = v.Load()
	}
	c.mu.Unlock()
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Label-keyed histogram (fixed buckets)
// ─────────────────────────────────────────────────────────────────────────────

// defaultDurationBuckets are the upper bounds (in seconds) for duration histograms.
// Covers typical living-wiki job durations from sub-second sink writes to
// multi-minute full cold-starts.
var defaultDurationBuckets = []float64{0.1, 0.5, 1, 5, 10, 30, 60, 120, 300, 600}

type histBucket struct {
	le    float64     // upper bound (seconds)
	count atomic.Int64
}

type labelHistogram struct {
	mu      sync.Mutex
	buckets []float64
	entries map[string][]*histBucket
	sumSecs map[string]*atomic.Int64 // sum stored as nanoseconds to avoid float64 races
	count   map[string]*atomic.Int64
}

func newLabelHistogram(buckets []float64) *labelHistogram {
	return &labelHistogram{
		buckets: buckets,
		entries: make(map[string][]*histBucket),
		sumSecs: make(map[string]*atomic.Int64),
		count:   make(map[string]*atomic.Int64),
	}
}

// Observe records a duration observation (in seconds) for the given label key.
func (h *labelHistogram) Observe(key string, seconds float64) {
	h.mu.Lock()
	bs, ok := h.entries[key]
	if !ok {
		bs = make([]*histBucket, len(h.buckets))
		for i, le := range h.buckets {
			bs[i] = &histBucket{le: le}
		}
		h.entries[key] = bs
		h.sumSecs[key] = new(atomic.Int64)
		h.count[key] = new(atomic.Int64)
	}
	h.mu.Unlock()

	for _, b := range bs {
		if seconds <= b.le {
			b.count.Add(1)
		}
	}
	// Store sum as nanoseconds (int64) to avoid float64 atomic races.
	h.sumSecs[key].Add(int64(seconds * float64(1e9)))
	h.count[key].Add(1)
}

type histSnapshot struct {
	buckets []float64
	counts  []int64
	sum     float64
	total   int64
}

// Snapshot returns a copy of the histogram state for each label key.
func (h *labelHistogram) Snapshot() map[string]histSnapshot {
	h.mu.Lock()
	keys := make([]string, 0, len(h.entries))
	for k := range h.entries {
		keys = append(keys, k)
	}
	h.mu.Unlock()

	out := make(map[string]histSnapshot, len(keys))
	for _, k := range keys {
		h.mu.Lock()
		bs := h.entries[k]
		sumAtomic := h.sumSecs[k]
		countAtomic := h.count[k]
		h.mu.Unlock()

		snap := histSnapshot{
			buckets: h.buckets,
			counts:  make([]int64, len(bs)),
			sum:     float64(sumAtomic.Load()) / float64(1e9),
			total:   countAtomic.Load(),
		}
		for i, b := range bs {
			snap.counts[i] = b.count.Load()
		}
		out[k] = snap
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Collector
// ─────────────────────────────────────────────────────────────────────────────

// Collector holds all in-process metrics for the living-wiki runtime.
// Obtain the package-level instance via [Default]; in tests you may
// construct one directly via [NewCollector].
//
// Metric names emitted at /metrics:
//
//	livingwiki_jobs_total{status,sink}
//	livingwiki_pages_generated_total{audience}
//	livingwiki_validation_failures_total{validator}
//	livingwiki_job_duration_seconds{sink} (histogram)
//	livingwiki_sink_write_duration_seconds{sink} (histogram)
//	livingwiki_cold_start_systemic_aborts_total{category}
type Collector struct {
	// JobsTotal counts completed jobs keyed by "status:sink" label pair.
	jobsTotal *labelCounter

	// PagesGeneratedTotal counts successfully generated pages keyed by audience.
	pagesGeneratedTotal *labelCounter

	// ValidationFailuresTotal counts pages excluded by a named validator.
	validationFailuresTotal *labelCounter

	// JobDurationSeconds histograms wall-clock time per job, keyed by sink kind.
	jobDurationSeconds *labelHistogram

	// SinkWriteLatency histograms individual HTTP write latency, keyed by sink kind.
	sinkWriteLatency *labelHistogram

	// ColdStartSystemicAbortsTotal counts cold-start runs aborted by the
	// orchestrator's soft-failure breaker, keyed by the dominant per-page
	// failure category. Cardinality is bounded by RecordColdStartSystemicAbort's
	// allowlist (6 known categories + "unknown").
	coldStartSystemicAbortsTotal *labelCounter
}

// NewCollector allocates a zeroed Collector. Safe for concurrent use after construction.
func NewCollector() *Collector {
	return &Collector{
		jobsTotal:                    newLabelCounter(),
		pagesGeneratedTotal:          newLabelCounter(),
		validationFailuresTotal:      newLabelCounter(),
		jobDurationSeconds:           newLabelHistogram(defaultDurationBuckets),
		sinkWriteLatency:             newLabelHistogram(defaultDurationBuckets),
		coldStartSystemicAbortsTotal: newLabelCounter(),
	}
}

// Default is the package-level collector used by all living-wiki runtime
// components. The handleMetrics handler calls Default.WritePrometheusText
// to include the living-wiki series in the existing /metrics output.
var Default = NewCollector()

// RecordJob increments JobsTotal and observes JobDurationSeconds.
// Call once per completed Generate call.
//
// status should be one of: "ok", "failed", "partial".
// sink is the primary sink kind for the job (e.g. "confluence", "git_repo").
// durationSeconds is the wall-clock duration of the job.
func (c *Collector) RecordJob(status, sink string, durationSeconds float64) {
	c.jobsTotal.Inc(status + ":" + sink)
	c.jobDurationSeconds.Observe(sink, durationSeconds)
}

// RecordPageGenerated increments PagesGeneratedTotal.
// audience should match a [livingwiki.RepoWikiAudience] value: "ENGINEER", "PRODUCT", "OPERATOR".
func (c *Collector) RecordPageGenerated(audience string) {
	c.pagesGeneratedTotal.Inc(audience)
}

// RecordValidationFailure increments ValidationFailuresTotal for the named validator.
// validator is the validator's identifying name (e.g. "content_gate", "length").
func (c *Collector) RecordValidationFailure(validator string) {
	c.validationFailuresTotal.Inc(validator)
}

// RecordSinkWrite observes SinkWriteLatency for one HTTP write to a sink.
// sink is the sink kind (e.g. "confluence", "notion").
// durationSeconds is the round-trip time of the individual HTTP call.
func (c *Collector) RecordSinkWrite(sink string, durationSeconds float64) {
	c.sinkWriteLatency.Observe(sink, durationSeconds)
}

// RecordColdStartSystemicAbort increments the systemic-abort counter for
// the given dominant failure category. Called from the cold-start runner
// when the orchestrator's soft-failure breaker trips with
// [orchestrator.ErrSystemicSoftFailures] /
// [orchestrator.SystemicAbortDetail].
//
// The function clamps category to a known allowlist (the
// SoftFailureCategory* values plus "unknown") so cardinality is bounded
// inside this function and does not depend on every caller getting it
// right.
func (c *Collector) RecordColdStartSystemicAbort(category string) {
	switch category {
	case "deadline_exceeded",
		"provider_unavailable",
		"provider_compute",
		"llm_empty",
		"render_error",
		"template_internal":
		// allowlisted
	default:
		category = "unknown"
	}
	c.coldStartSystemicAbortsTotal.Inc(category)
}

// WritePrometheusText writes the collector's current state in Prometheus text
// exposition format to w. This matches the hand-written format used throughout
// internal/api/rest/health.go.
func (c *Collector) WritePrometheusText(w io.Writer) {
	// ── livingwiki_jobs_total ─────────────────────────────────────────────────
	fmt.Fprintf(w, "# HELP livingwiki_jobs_total Total living-wiki regen jobs by completion status and primary sink\n")
	fmt.Fprintf(w, "# TYPE livingwiki_jobs_total counter\n")
	for key, count := range c.jobsTotal.Snapshot() {
		status, sink := splitLabelPair(key)
		fmt.Fprintf(w, "livingwiki_jobs_total{status=%q,sink=%q} %d\n", status, sink, count)
	}

	// ── livingwiki_pages_generated_total ──────────────────────────────────────
	fmt.Fprintf(w, "# HELP livingwiki_pages_generated_total Total pages successfully generated by audience\n")
	fmt.Fprintf(w, "# TYPE livingwiki_pages_generated_total counter\n")
	for audience, count := range c.pagesGeneratedTotal.Snapshot() {
		fmt.Fprintf(w, "livingwiki_pages_generated_total{audience=%q} %d\n", audience, count)
	}

	// ── livingwiki_validation_failures_total ──────────────────────────────────
	fmt.Fprintf(w, "# HELP livingwiki_validation_failures_total Total pages excluded by each validator\n")
	fmt.Fprintf(w, "# TYPE livingwiki_validation_failures_total counter\n")
	for validator, count := range c.validationFailuresTotal.Snapshot() {
		fmt.Fprintf(w, "livingwiki_validation_failures_total{validator=%q} %d\n", validator, count)
	}

	// ── livingwiki_job_duration_seconds ───────────────────────────────────────
	fmt.Fprintf(w, "# HELP livingwiki_job_duration_seconds Wall-clock duration of each living-wiki regen job in seconds\n")
	fmt.Fprintf(w, "# TYPE livingwiki_job_duration_seconds histogram\n")
	for sink, snap := range c.jobDurationSeconds.Snapshot() {
		for i, le := range snap.buckets {
			leStr := formatFloat(le)
			fmt.Fprintf(w, "livingwiki_job_duration_seconds_bucket{sink=%q,le=%q} %d\n", sink, leStr, snap.counts[i])
		}
		fmt.Fprintf(w, "livingwiki_job_duration_seconds_bucket{sink=%q,le=\"+Inf\"} %d\n", sink, snap.total)
		fmt.Fprintf(w, "livingwiki_job_duration_seconds_sum{sink=%q} %g\n", sink, snap.sum)
		fmt.Fprintf(w, "livingwiki_job_duration_seconds_count{sink=%q} %d\n", sink, snap.total)
	}

	// ── livingwiki_sink_write_duration_seconds ────────────────────────────────
	fmt.Fprintf(w, "# HELP livingwiki_sink_write_duration_seconds Round-trip latency of individual sink HTTP writes in seconds\n")
	fmt.Fprintf(w, "# TYPE livingwiki_sink_write_duration_seconds histogram\n")
	for sink, snap := range c.sinkWriteLatency.Snapshot() {
		for i, le := range snap.buckets {
			leStr := formatFloat(le)
			fmt.Fprintf(w, "livingwiki_sink_write_duration_seconds_bucket{sink=%q,le=%q} %d\n", sink, leStr, snap.counts[i])
		}
		fmt.Fprintf(w, "livingwiki_sink_write_duration_seconds_bucket{sink=%q,le=\"+Inf\"} %d\n", sink, snap.total)
		fmt.Fprintf(w, "livingwiki_sink_write_duration_seconds_sum{sink=%q} %g\n", sink, snap.sum)
		fmt.Fprintf(w, "livingwiki_sink_write_duration_seconds_count{sink=%q} %d\n", sink, snap.total)
	}

	// ── livingwiki_cold_start_systemic_aborts_total ───────────────────────────
	fmt.Fprintf(w, "# HELP livingwiki_cold_start_systemic_aborts_total Cold-start runs aborted by the systemic-failure breaker, by dominant failure category\n")
	fmt.Fprintf(w, "# TYPE livingwiki_cold_start_systemic_aborts_total counter\n")
	for category, count := range c.coldStartSystemicAbortsTotal.Snapshot() {
		fmt.Fprintf(w, "livingwiki_cold_start_systemic_aborts_total{category=%q} %d\n", category, count)
	}
}

// splitLabelPair splits a "status:sink" composite key back into its two parts.
// The separator is the first colon so that sink names containing colons are safe.
func splitLabelPair(key string) (first, second string) {
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

// formatFloat renders a float64 bucket bound without trailing zeros while
// keeping a consistent format recognised by Prometheus parsers.
func formatFloat(f float64) string {
	if math.Trunc(f) == f {
		return fmt.Sprintf("%.1f", f)
	}
	return fmt.Sprintf("%g", f)
}
