// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/usage"
)

// TestTelemetryCountsIncludesUsageKeys verifies that TelemetryCounts returns
// both "queries_30d" and "artifacts_generated_30d" keys in its Counts map,
// and that the values reflect the package-level rolling counters.
func TestTelemetryCountsIncludesUsageKeys(t *testing.T) {
	t.Cleanup(usage.ResetCountersForTest)

	usage.QueriesCounter.Inc()
	usage.ArtifactsCounter.Inc()
	usage.ArtifactsCounter.Inc()

	p := &telemetryCountProvider{store: graph.NewStore()}
	_, _, _, counts := p.TelemetryCounts()

	if counts == nil {
		t.Fatal("TelemetryCounts returned nil counts map")
	}
	if got, ok := counts["queries_30d"]; !ok {
		t.Fatal("counts map missing key 'queries_30d'")
	} else if got != 1 {
		t.Fatalf("queries_30d: expected 1, got %d", got)
	}
	if got, ok := counts["artifacts_generated_30d"]; !ok {
		t.Fatal("counts map missing key 'artifacts_generated_30d'")
	} else if got != 2 {
		t.Fatalf("artifacts_generated_30d: expected 2, got %d", got)
	}
}
