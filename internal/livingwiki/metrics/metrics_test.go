// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package metrics_test

import (
	"strings"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/metrics"
)

// ─────────────────────────────────────────────────────────────────────────────
// Collector — counter tests
// ─────────────────────────────────────────────────────────────────────────────

// TestRecordJobIncrementsCounter verifies that RecordJob increments the correct
// label combination and that the counter is reflected in Prometheus output.
func TestRecordJobIncrementsCounter(t *testing.T) {
	c := metrics.NewCollector()
	c.RecordJob("ok", "confluence", 1.5)
	c.RecordJob("ok", "confluence", 0.5)
	c.RecordJob("failed", "notion", 3.0)

	var buf strings.Builder
	c.WritePrometheusText(&buf)
	out := buf.String()

	if !strings.Contains(out, `livingwiki_jobs_total{status="ok",sink="confluence"} 2`) {
		t.Errorf("expected ok/confluence count 2 in metrics output:\n%s", out)
	}
	if !strings.Contains(out, `livingwiki_jobs_total{status="failed",sink="notion"} 1`) {
		t.Errorf("expected failed/notion count 1 in metrics output:\n%s", out)
	}
}

// TestRecordPageGeneratedLabels verifies that RecordPageGenerated creates
// distinct per-audience counters.
func TestRecordPageGeneratedLabels(t *testing.T) {
	c := metrics.NewCollector()
	c.RecordPageGenerated("ENGINEER")
	c.RecordPageGenerated("ENGINEER")
	c.RecordPageGenerated("PRODUCT")

	var buf strings.Builder
	c.WritePrometheusText(&buf)
	out := buf.String()

	if !strings.Contains(out, `livingwiki_pages_generated_total{audience="ENGINEER"} 2`) {
		t.Errorf("expected ENGINEER count 2:\n%s", out)
	}
	if !strings.Contains(out, `livingwiki_pages_generated_total{audience="PRODUCT"} 1`) {
		t.Errorf("expected PRODUCT count 1:\n%s", out)
	}
}

// TestRecordValidationFailureLabels verifies distinct per-validator counters.
func TestRecordValidationFailureLabels(t *testing.T) {
	c := metrics.NewCollector()
	c.RecordValidationFailure("content_gate")
	c.RecordValidationFailure("content_gate")
	c.RecordValidationFailure("length")

	var buf strings.Builder
	c.WritePrometheusText(&buf)
	out := buf.String()

	if !strings.Contains(out, `livingwiki_validation_failures_total{validator="content_gate"} 2`) {
		t.Errorf("expected content_gate count 2:\n%s", out)
	}
	if !strings.Contains(out, `livingwiki_validation_failures_total{validator="length"} 1`) {
		t.Errorf("expected length count 1:\n%s", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Collector — histogram tests
// ─────────────────────────────────────────────────────────────────────────────

// TestRecordJobObservesHistogram verifies that RecordJob writes to the job
// duration histogram and the sum/count are emitted correctly.
func TestRecordJobObservesHistogram(t *testing.T) {
	c := metrics.NewCollector()
	c.RecordJob("ok", "git_repo", 2.0) // falls in ≤5 bucket
	c.RecordJob("ok", "git_repo", 7.0) // falls in ≤10 bucket

	var buf strings.Builder
	c.WritePrometheusText(&buf)
	out := buf.String()

	// Total count for git_repo should be 2.
	if !strings.Contains(out, `livingwiki_job_duration_seconds_count{sink="git_repo"} 2`) {
		t.Errorf("expected job duration count 2 for git_repo:\n%s", out)
	}
	// The ≤10 bucket should have both observations (2.0 ≤ 10 and 7.0 ≤ 10).
	if !strings.Contains(out, `livingwiki_job_duration_seconds_bucket{sink="git_repo",le="10.0"} 2`) {
		t.Errorf("expected le=10 bucket count 2 for git_repo:\n%s", out)
	}
	// The ≤5 bucket should have only the 2.0 observation.
	if !strings.Contains(out, `livingwiki_job_duration_seconds_bucket{sink="git_repo",le="5.0"} 1`) {
		t.Errorf("expected le=5 bucket count 1 for git_repo:\n%s", out)
	}
}

// TestRecordSinkWriteObservesHistogram verifies that RecordSinkWrite writes to
// the sink write latency histogram.
func TestRecordSinkWriteObservesHistogram(t *testing.T) {
	c := metrics.NewCollector()
	c.RecordSinkWrite("confluence", 0.2) // falls in ≤0.5 bucket

	var buf strings.Builder
	c.WritePrometheusText(&buf)
	out := buf.String()

	if !strings.Contains(out, `livingwiki_sink_write_duration_seconds_count{sink="confluence"} 1`) {
		t.Errorf("expected sink write count 1 for confluence:\n%s", out)
	}
	// 0.2 ≤ 0.5 → should appear in that bucket.
	if !strings.Contains(out, `livingwiki_sink_write_duration_seconds_bucket{sink="confluence",le="0.5"} 1`) {
		t.Errorf("expected le=0.5 bucket count 1 for confluence:\n%s", out)
	}
	// 0.2 > 0.1 → should NOT appear in the 0.1 bucket.
	if !strings.Contains(out, `livingwiki_sink_write_duration_seconds_bucket{sink="confluence",le="0.1"} 0`) {
		t.Errorf("expected le=0.1 bucket count 0 for confluence:\n%s", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// WritePrometheusText structure tests
// ─────────────────────────────────────────────────────────────────────────────

// TestWritePrometheusTextContainsAllSeriesHeaders verifies that even with no
// observations the HELP and TYPE comment lines are emitted for all 5 series.
func TestWritePrometheusTextContainsAllSeriesHeaders(t *testing.T) {
	c := metrics.NewCollector()

	var buf strings.Builder
	c.WritePrometheusText(&buf)
	out := buf.String()

	requiredLines := []string{
		"# HELP livingwiki_jobs_total",
		"# TYPE livingwiki_jobs_total counter",
		"# HELP livingwiki_pages_generated_total",
		"# TYPE livingwiki_pages_generated_total counter",
		"# HELP livingwiki_validation_failures_total",
		"# TYPE livingwiki_validation_failures_total counter",
		"# HELP livingwiki_job_duration_seconds",
		"# TYPE livingwiki_job_duration_seconds histogram",
		"# HELP livingwiki_sink_write_duration_seconds",
		"# TYPE livingwiki_sink_write_duration_seconds histogram",
	}

	for _, line := range requiredLines {
		if !strings.Contains(out, line) {
			t.Errorf("metrics output missing expected line: %q", line)
		}
	}
}

// TestCollectorIsIsolatedFromDefault verifies that a fresh collector from
// NewCollector does not share state with metrics.Default or another collector.
func TestCollectorIsIsolatedFromDefault(t *testing.T) {
	metrics.Default.RecordJob("ok", "confluence", 1.0)

	fresh := metrics.NewCollector()
	var buf strings.Builder
	fresh.WritePrometheusText(&buf)
	out := buf.String()

	// The fresh collector should have no job counter lines (only HELP/TYPE).
	if strings.Contains(out, `livingwiki_jobs_total{`) {
		t.Errorf("fresh collector should not contain job counter data lines:\n%s", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Concurrent safety
// ─────────────────────────────────────────────────────────────────────────────

// TestCollectorConcurrentWrites verifies that concurrent increments do not
// race or panic. Run with -race to detect issues.
func TestCollectorConcurrentWrites(t *testing.T) {
	c := metrics.NewCollector()
	done := make(chan struct{})
	const goroutines = 20
	const iterations = 50

	for i := range goroutines {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			for range iterations {
				c.RecordJob("ok", "notion", 0.1)
				c.RecordPageGenerated("ENGINEER")
				c.RecordValidationFailure("length")
				c.RecordSinkWrite("notion", 0.05)
			}
		}(i)
	}
	for range goroutines {
		<-done
	}

	var buf strings.Builder
	c.WritePrometheusText(&buf)
	out := buf.String()

	expected := goroutines * iterations
	jobLine := `livingwiki_jobs_total{status="ok",sink="notion"} `
	idx := strings.Index(out, jobLine)
	if idx == -1 {
		t.Fatalf("expected job counter line in output:\n%s", out)
	}
	// Extract the count value.
	rest := out[idx+len(jobLine):]
	var count int
	if _, err := strings.NewReader(rest).Read(nil); err == nil {
		// parse the integer
		for _, ch := range rest {
			if ch == '\n' {
				break
			}
			if ch >= '0' && ch <= '9' {
				count = count*10 + int(ch-'0')
			}
		}
	}
	if count != expected {
		t.Errorf("expected %d job increments, got %d", expected, count)
	}
}
