// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	surrealdb "github.com/surrealdb/surrealdb.go"

	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// newTestJob returns a minimal llm.Job with a generated ID suitable for
// insertion via SurrealStore.Create.
func newTestJob(targetKey, repoID string) *llm.Job {
	return &llm.Job{
		ID:        uuid.NewString(),
		Subsystem: llm.SubsystemLinking,
		JobType:   "test_job",
		TargetKey: targetKey,
		RepoID:    repoID,
		Status:    llm.StatusPending,
	}
}

// TestSurrealStoreHeartbeatAdvancesUpdatedAt verifies that Heartbeat() against
// an active job actually moves updated_at on the row. This is the regression
// test for the gap the MemStore-only test would not catch: a Surreal WHERE
// clause silently no-op'ing due to a status-string mismatch would pass the
// memory-backed test but fail here.
func TestSurrealStoreHeartbeatAdvancesUpdatedAt(t *testing.T) {
	s := startSurrealContainer(t)
	store := NewSurrealStore(s)

	job, err := store.Create(newTestJob("tk-heartbeat-1", "repo-hb-1"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Advance to "generating" so the Heartbeat WHERE clause matches.
	if err := store.SetStatus(job.ID, llm.StatusGenerating); err != nil {
		t.Fatalf("SetStatus generating: %v", err)
	}

	before := store.GetByID(job.ID)
	if before == nil {
		t.Fatal("GetByID before heartbeat returned nil")
	}

	// Sleep to ensure time::now() produces a strictly later timestamp.
	time.Sleep(110 * time.Millisecond)

	if err := store.Heartbeat(job.ID); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	after := store.GetByID(job.ID)
	if after == nil {
		t.Fatal("GetByID after heartbeat returned nil")
	}

	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("Heartbeat did not advance updated_at: before=%v after=%v",
			before.UpdatedAt, after.UpdatedAt)
	}
}

// TestSurrealStoreHeartbeatIsNoopOnTerminalJob verifies that Heartbeat on a
// terminal job (failed/ready/cancelled) is a safe no-op: no error returned,
// updated_at not advanced.
func TestSurrealStoreHeartbeatIsNoopOnTerminalJob(t *testing.T) {
	s := startSurrealContainer(t)
	store := NewSurrealStore(s)

	job, err := store.Create(newTestJob("tk-heartbeat-2", "repo-hb-2"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := store.SetError(job.ID, "test_error", "deliberately failed"); err != nil {
		t.Fatalf("SetError: %v", err)
	}

	before := store.GetByID(job.ID)
	if before == nil {
		t.Fatal("GetByID before heartbeat returned nil")
	}

	time.Sleep(110 * time.Millisecond)

	if err := store.Heartbeat(job.ID); err != nil {
		t.Fatalf("Heartbeat (terminal): unexpected error: %v", err)
	}

	after := store.GetByID(job.ID)
	if after == nil {
		t.Fatal("GetByID after heartbeat returned nil")
	}

	// Allow for sub-millisecond SurrealDB timestamp rounding, but assert
	// Heartbeat did not materially push updated_at forward.
	if after.UpdatedAt.After(before.UpdatedAt.Add(50 * time.Millisecond)) {
		t.Errorf("Heartbeat on terminal job advanced updated_at: before=%v after=%v",
			before.UpdatedAt, after.UpdatedAt)
	}
}

// TestSurrealJobResultStoreSaveIsIdempotentByJobID verifies that the Surreal-
// backed LivingWikiJobResultStore.Save is a true UPSERT keyed on job_id — a
// second Save with the same job_id updates the existing row and does NOT
// insert a duplicate. This is the DB-layer analogue of the mem-store test in
// internal/settings/livingwiki/job_result_store_test.go.
func TestSurrealJobResultStoreSaveIsIdempotentByJobID(t *testing.T) {
	s := startSurrealContainer(t)
	jrs := NewLivingWikiJobResultStore(s)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)

	first := &livingwiki.LivingWikiJobResult{
		RepoID:              "upsert-repo",
		JobID:               "upsert-job-1",
		StartedAt:           now,
		Status:              "running",
		PagesPlanned:        10,
		PagesGenerated:      0,
		PagesExcluded:       0,
		ExcludedPageIDs:     []string{},
		GeneratedPageTitles: []string{},
		ExclusionReasons:    []string{},
	}
	if err := jrs.Save(ctx, "tenant-upsert", first); err != nil {
		t.Fatalf("Save 1: %v", err)
	}

	completed := now.Add(5 * time.Second)
	second := &livingwiki.LivingWikiJobResult{
		RepoID:              "upsert-repo",
		JobID:               "upsert-job-1",
		StartedAt:           now,
		CompletedAt:         &completed,
		Status:              "ok",
		PagesPlanned:        10,
		PagesGenerated:      42,
		PagesExcluded:       0,
		ExcludedPageIDs:     []string{},
		GeneratedPageTitles: []string{"Overview", "API Reference"},
		ExclusionReasons:    []string{},
	}
	if err := jrs.Save(ctx, "tenant-upsert", second); err != nil {
		t.Fatalf("Save 2: %v", err)
	}

	got, err := jrs.GetByJobID(ctx, "upsert-job-1")
	if err != nil {
		t.Fatalf("GetByJobID: %v", err)
	}
	if got == nil {
		t.Fatal("GetByJobID returned nil after upsert")
	}
	if got.Status != "ok" {
		t.Errorf("upsert status: got %q, want %q", got.Status, "ok")
	}
	if got.PagesGenerated != 42 {
		t.Errorf("upsert pages_generated: got %d, want 42", got.PagesGenerated)
	}

	// Assert exactly one row exists for this job_id — duplicates would indicate
	// that Save is append-only rather than a true upsert.
	db := s.DB()
	type countResult struct {
		Count int `json:"count"`
	}
	rows, queryErr := surrealdb.Query[[]countResult](ctx, db,
		"SELECT count() AS count FROM lw_job_results WHERE job_id = $job_id GROUP ALL",
		map[string]any{"job_id": "upsert-job-1"})
	if queryErr != nil {
		t.Fatalf("row-count query: %v", queryErr)
	}
	if rows == nil || len(*rows) == 0 || (*rows)[0].Error != nil {
		t.Fatal("row-count query returned no results")
	}
	if count := (*rows)[0].Result; len(count) == 0 || count[0].Count != 1 {
		var n int
		if len(count) > 0 {
			n = count[0].Count
		}
		t.Errorf("expected exactly 1 row for job_id=upsert-job-1, got %d", n)
	}
}
