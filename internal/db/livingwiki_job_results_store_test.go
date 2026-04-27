// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"encoding/json"
	"testing"
	"time"
)

// TestSurrealLWJobResultToResult verifies that the DTO→domain conversion
// correctly decodes JSON-encoded slice fields, maps timestamps, and handles
// optional fields (CompletedAt, ErrorMessage).
func TestSurrealLWJobResultToResult(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	completed := now.Add(5 * time.Second)

	encIDs, _ := json.Marshal([]string{"page-1", "page-2"})
	encTitles, _ := json.Marshal([]string{"Overview", "API Reference"})
	encReasons, _ := json.Marshal([]string{"length", "content_gate"})

	dto := &surrealLWJobResult{
		TenantID:            "default",
		RepoID:              "repo-abc",
		JobID:               "job-xyz",
		StartedAt:           surrealTime{Time: now},
		CompletedAt:         &surrealTime{Time: completed},
		PagesPlanned:        20,
		PagesGenerated:      18,
		PagesExcluded:       2,
		ExcludedPageIDs:     string(encIDs),
		GeneratedPageTitles: string(encTitles),
		ExclusionReasons:    string(encReasons),
		Status:              "ok",
		ErrorMessage:        "",
	}

	result, err := dto.toResult()
	if err != nil {
		t.Fatalf("toResult: %v", err)
	}
	if result.JobID != "job-xyz" {
		t.Errorf("JobID: got %q, want %q", result.JobID, "job-xyz")
	}
	if !result.StartedAt.Equal(now) {
		t.Errorf("StartedAt: got %v, want %v", result.StartedAt, now)
	}
	if result.CompletedAt == nil {
		t.Fatal("CompletedAt should not be nil")
	}
	if !result.CompletedAt.Equal(completed) {
		t.Errorf("CompletedAt: got %v, want %v", result.CompletedAt, completed)
	}
	if result.PagesPlanned != 20 {
		t.Errorf("PagesPlanned: got %d, want 20", result.PagesPlanned)
	}
	if result.PagesGenerated != 18 {
		t.Errorf("PagesGenerated: got %d, want 18", result.PagesGenerated)
	}
	if result.PagesExcluded != 2 {
		t.Errorf("PagesExcluded: got %d, want 2", result.PagesExcluded)
	}
	if len(result.ExcludedPageIDs) != 2 || result.ExcludedPageIDs[0] != "page-1" {
		t.Errorf("ExcludedPageIDs: got %v", result.ExcludedPageIDs)
	}
	if len(result.GeneratedPageTitles) != 2 || result.GeneratedPageTitles[1] != "API Reference" {
		t.Errorf("GeneratedPageTitles: got %v", result.GeneratedPageTitles)
	}
	if len(result.ExclusionReasons) != 2 || result.ExclusionReasons[0] != "length" {
		t.Errorf("ExclusionReasons: got %v", result.ExclusionReasons)
	}
	if result.Status != "ok" {
		t.Errorf("Status: got %q, want %q", result.Status, "ok")
	}
}

// TestSurrealLWJobResultToResult_NilCompletedAt verifies nil CompletedAt
// in the DTO maps to nil in the domain result (in-progress job scenario).
func TestSurrealLWJobResultToResult_NilCompletedAt(t *testing.T) {
	dto := &surrealLWJobResult{
		JobID:               "job-in-progress",
		StartedAt:           surrealTime{Time: time.Now().UTC()},
		CompletedAt:         nil,
		ExcludedPageIDs:     "[]",
		GeneratedPageTitles: "[]",
		ExclusionReasons:    "[]",
		Status:              "running",
	}

	result, err := dto.toResult()
	if err != nil {
		t.Fatalf("toResult: %v", err)
	}
	if result.CompletedAt != nil {
		t.Errorf("expected nil CompletedAt for in-progress job, got %v", result.CompletedAt)
	}
}

// TestSurrealLWJobResultToResult_EmptySlices verifies that empty JSON arrays
// and empty strings produce non-nil empty slices (not nil slices) in the domain
// result, keeping GraphQL serialization consistent.
func TestSurrealLWJobResultToResult_EmptySlices(t *testing.T) {
	dto := &surrealLWJobResult{
		JobID:               "job-empty",
		StartedAt:           surrealTime{Time: time.Now().UTC()},
		ExcludedPageIDs:     "",
		GeneratedPageTitles: "[]",
		ExclusionReasons:    "",
		Status:              "ok",
	}

	result, err := dto.toResult()
	if err != nil {
		t.Fatalf("toResult: %v", err)
	}
	if result.ExcludedPageIDs == nil {
		t.Error("ExcludedPageIDs should be non-nil empty slice, got nil")
	}
	if result.GeneratedPageTitles == nil {
		t.Error("GeneratedPageTitles should be non-nil empty slice, got nil")
	}
	if result.ExclusionReasons == nil {
		t.Error("ExclusionReasons should be non-nil empty slice, got nil")
	}
}

// TestSurrealLWJobResultToResult_WithErrorMessage verifies that ErrorMessage
// is propagated from the DTO to the domain result.
func TestSurrealLWJobResultToResult_WithErrorMessage(t *testing.T) {
	dto := &surrealLWJobResult{
		JobID:               "job-failed",
		StartedAt:           surrealTime{Time: time.Now().UTC()},
		ExcludedPageIDs:     "[]",
		GeneratedPageTitles: "[]",
		ExclusionReasons:    "[]",
		Status:              "failed",
		ErrorMessage:        "LLM context window exceeded: 128k tokens",
	}

	result, err := dto.toResult()
	if err != nil {
		t.Fatalf("toResult: %v", err)
	}
	if result.ErrorMessage != dto.ErrorMessage {
		t.Errorf("ErrorMessage: got %q, want %q", result.ErrorMessage, dto.ErrorMessage)
	}
	if result.Status != "failed" {
		t.Errorf("Status: got %q, want %q", result.Status, "failed")
	}
}
