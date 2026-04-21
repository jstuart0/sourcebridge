// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// Verifies the rollback-compat read path: a legacy ca_impact_report row that
// only populated stale_artifacts (the []string column) still projects into a
// usable []StaleArtifactReason on read.
func TestImpactReport_LegacyShape_ProjectsToReasons(t *testing.T) {
	row := &impactReportRow{
		ReportID:             "imp-legacy-1",
		RepoID:               "repo-1",
		StaleArtifacts:       []string{"artifact-A", "artifact-B"},
		StaleArtifactReasons: "", // legacy: column unpopulated
		ComputedAt:           surrealTime{Time: time.Now()},
	}

	got := row.toImpactReport()

	if got.ID != "imp-legacy-1" || got.RepositoryID != "repo-1" {
		t.Fatalf("id/repo round-trip: %+v", got)
	}
	if len(got.StaleArtifacts) != 2 {
		t.Fatalf("expected legacy StaleArtifacts preserved, got %v", got.StaleArtifacts)
	}
	if len(got.StaleArtifactReasons) != 2 {
		t.Fatalf("expected 2 projected reasons, got %d", len(got.StaleArtifactReasons))
	}
	for i, want := range []string{"artifact-A", "artifact-B"} {
		r := got.StaleArtifactReasons[i]
		if r.ArtifactID != want {
			t.Fatalf("reason %d artifact id: got %q want %q", i, r.ArtifactID, want)
		}
		if !r.Blanket {
			t.Fatalf("expected Blanket=true for legacy projection, got %+v", r)
		}
		if r.ReportID != "imp-legacy-1" {
			t.Fatalf("expected report id echoed back, got %q", r.ReportID)
		}
	}
}

// Verifies that when stale_artifact_reasons is populated, we parse it (rather
// than falling back to the legacy projection).
func TestImpactReport_RichShape_ParsesReasons(t *testing.T) {
	// Match the shape StoreImpactReport writes: a JSON-serialized
	// []graph.StaleArtifactReason.
	rich := `[
		{"artifact_id":"A","symbols":["sym-X"],"report_id":"imp-rich-1"},
		{"artifact_id":"B","blanket":true,"report_id":"imp-rich-1"}
	]`
	row := &impactReportRow{
		ReportID:             "imp-rich-1",
		RepoID:               "repo-1",
		StaleArtifacts:       []string{"A", "B"},
		StaleArtifactReasons: rich,
		ComputedAt:           surrealTime{Time: time.Now()},
	}

	got := row.toImpactReport()
	if len(got.StaleArtifactReasons) != 2 {
		t.Fatalf("expected 2 parsed reasons, got %d", len(got.StaleArtifactReasons))
	}
	a := got.StaleArtifactReasons[0]
	if a.ArtifactID != "A" || len(a.Symbols) != 1 || a.Symbols[0] != "sym-X" || a.Blanket {
		t.Fatalf("rich reason 0 mismatch: %+v", a)
	}
	b := got.StaleArtifactReasons[1]
	if b.ArtifactID != "B" || !b.Blanket {
		t.Fatalf("rich reason 1 mismatch: %+v", b)
	}
}

// Sanity: ensure the projection doesn't double-fill reasons when both
// columns are populated.
func TestImpactReport_BothColumns_RichWins(t *testing.T) {
	rich := `[{"artifact_id":"A","blanket":false,"symbols":["s1"]}]`
	row := &impactReportRow{
		ReportID:             "imp-both",
		StaleArtifacts:       []string{"A", "B", "C"}, // legacy list is longer
		StaleArtifactReasons: rich,
		ComputedAt:           surrealTime{Time: time.Now()},
	}
	got := row.toImpactReport()
	if len(got.StaleArtifactReasons) != 1 {
		t.Fatalf("expected rich column to win (1 reason), got %d", len(got.StaleArtifactReasons))
	}
	if got.StaleArtifactReasons[0].ArtifactID != "A" {
		t.Fatalf("expected parsed rich reason, got %+v", got.StaleArtifactReasons[0])
	}
	// Legacy list stays in place for rollback compatibility.
	if len(got.StaleArtifacts) != 3 {
		t.Fatalf("expected legacy StaleArtifacts preserved verbatim, got %v", got.StaleArtifacts)
	}
}

// Compile-time guard that graph.ImpactReport continues to have the fields
// this plan depends on.
var _ = graph.ImpactReport{
	StaleArtifacts:       []string{},
	StaleArtifactReasons: []graph.StaleArtifactReason{},
}
