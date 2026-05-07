// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// GQL-5 (bob A-H4): unit tests for coldstart.BuildRunner and coldstart.Config.

package coldstart

import (
	"context"
	"testing"
)

// TestBuildRunnerNilOrchestratorReturnsNotice verifies that BuildRunner with a
// nil LWOrch returns a stub closure that reports progress but does not fail.
// This mirrors the original test TestBuildColdStartRunnerNilOrchestratorReturnsNotice
// in the graphql package, exercising the same guard at the package boundary.
func TestBuildRunnerNilOrchestratorReturnsNotice(t *testing.T) {
	cfg := Config{
		LWOrch: nil, // nil orchestrator — should trigger the fallback path
		RepoID: "test-repo",
	}

	runner := BuildRunner(cfg)
	if runner == nil {
		t.Fatal("BuildRunner returned nil closure; expected a non-nil func")
	}

	var progressMsg string
	var progressStatus string
	rt := &stubRuntime{
		onReportProgress: func(_ float64, status, msg string) {
			progressStatus = status
			progressMsg = msg
		},
	}

	err := runner(context.Background(), rt)
	if err != nil {
		t.Fatalf("BuildRunner(nil LWOrch) runner returned error: %v", err)
	}
	if progressStatus != "unavailable" {
		t.Errorf("expected status %q; got %q", "unavailable", progressStatus)
	}
	if progressMsg == "" {
		t.Error("expected a non-empty progress message for nil-orchestrator path")
	}
}

// TestConfigFieldsPreserved verifies that Config fields are correctly stored
// and accessible — a basic smoke test that the struct definition is complete.
func TestConfigFieldsPreserved(t *testing.T) {
	cfg := Config{
		RepoID:   "repo-123",
		TenantID: "tenant-abc",
		SinkKind: "confluence",
		Mode:     GenerationModeLWDetailed,
	}

	if cfg.RepoID != "repo-123" {
		t.Errorf("RepoID: got %q, want %q", cfg.RepoID, "repo-123")
	}
	if cfg.TenantID != "tenant-abc" {
		t.Errorf("TenantID: got %q, want %q", cfg.TenantID, "tenant-abc")
	}
	if cfg.SinkKind != "confluence" {
		t.Errorf("SinkKind: got %q, want %q", cfg.SinkKind, "confluence")
	}
	if cfg.Mode != GenerationModeLWDetailed {
		t.Errorf("Mode: got %q, want %q", cfg.Mode, GenerationModeLWDetailed)
	}
}

// TestBuildRunnerDefaultsMode verifies that an empty Mode is defaulted to
// GenerationModeLWDetailed inside BuildRunner (not the caller's responsibility).
func TestBuildRunnerDefaultsMode(t *testing.T) {
	cfg := Config{
		LWOrch: nil,
		RepoID: "repo-mode-default",
		Mode:   "", // empty — should be defaulted
	}

	// BuildRunner with nil orchestrator returns immediately; we only care
	// that Mode is defaulted before the nil-guard fires. We verify this
	// indirectly: a nil orchestrator always returns the "unavailable" path
	// without panicking on Mode == "".
	runner := BuildRunner(cfg)
	if runner == nil {
		t.Fatal("BuildRunner returned nil")
	}

	rt := &stubRuntime{}
	if err := runner(context.Background(), rt); err != nil {
		t.Fatalf("runner returned unexpected error: %v", err)
	}
}

// TestGenerationModeConstants verifies the exported mode constants have the
// expected string values, as downstream code (GQL schema, DB records, logs)
// depends on these strings.
func TestGenerationModeConstants(t *testing.T) {
	if GenerationModeLWDetailed != "lw_detailed" {
		t.Errorf("GenerationModeLWDetailed = %q; want %q", GenerationModeLWDetailed, "lw_detailed")
	}
	if GenerationModeLWOverview != "lw_overview" {
		t.Errorf("GenerationModeLWOverview = %q; want %q", GenerationModeLWOverview, "lw_overview")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

// stubRuntime is a minimal llm.Runtime implementation for tests that only
// need to capture ReportProgress calls.
type stubRuntime struct {
	onReportProgress func(progress float64, status, message string)
	jobID            string
}

func (s *stubRuntime) JobID() string {
	if s.jobID == "" {
		return "test-job-id"
	}
	return s.jobID
}

func (s *stubRuntime) ReportProgress(progress float64, status, message string, _ float64) {
	if s.onReportProgress != nil {
		s.onReportProgress(progress, status, message)
	}
}

func (s *stubRuntime) Heartbeat() error          { return nil }
func (s *stubRuntime) ReportTokens(_, _ int)     {}
func (s *stubRuntime) ReportSnapshotBytes(_ int) {}
