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

// TestSurrealStoreLLMProviderRoundTrip verifies that Job.LLMProvider and
// JobLogEntry.LLMProvider survive a full Surreal round-trip (Create + GetByID
// + Update + AppendLog + ListLogs). R3 slice 3 — the column was added in
// migration 049 and the DB driver must read/write it through the named
// `llm_provider` field.
func TestSurrealStoreLLMProviderRoundTrip(t *testing.T) {
	s := startSurrealContainer(t)
	store := NewSurrealStore(s)

	job := newTestJob("tk-llmprovider-1", "repo-llmprov-1")
	job.LLMProvider = "anthropic"
	created, err := store.Create(job)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.LLMProvider != "anthropic" {
		t.Fatalf("Create round-trip llm_provider: got %q, want anthropic", created.LLMProvider)
	}

	got := store.GetByID(job.ID)
	if got == nil || got.LLMProvider != "anthropic" {
		t.Fatalf("GetByID llm_provider: got %+v", got)
	}

	// Update path — change the provider and confirm it persists.
	got.LLMProvider = "openai"
	if err := store.Update(got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got2 := store.GetByID(job.ID)
	if got2 == nil || got2.LLMProvider != "openai" {
		t.Fatalf("Update round-trip llm_provider: got %+v", got2)
	}

	// Append a log line and verify llm_provider is written and read back.
	entry, err := store.AppendLog(&llm.JobLogEntry{
		JobID:       job.ID,
		Subsystem:   llm.SubsystemKnowledge,
		JobType:     "test_job",
		LLMProvider: "openai",
		Level:       llm.LogLevelInfo,
		Event:       "test",
		Message:     "test",
		Sequence:    time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	if entry == nil || entry.LLMProvider != "openai" {
		t.Fatalf("AppendLog llm_provider: got %+v", entry)
	}
	logs := store.ListLogs(job.ID, llm.JobLogFilter{})
	if len(logs) != 1 || logs[0].LLMProvider != "openai" {
		t.Fatalf("ListLogs llm_provider round-trip failed: %+v", logs)
	}
}

// TestSurrealStoreProcessIDRoundTrip verifies that process_id (added in
// migration 058, CA-175) survives a full SurrealDB round-trip through Create,
// GetByID, and ListActive — the three paths reconcileZombieJobs depends on.
//
// The explicit ListActive assertion is non-negotiable: reconciliation reads
// via ListActive, so a SQL bug that drops process_id from the SELECT would
// silently degrade reconciliation to pure heartbeat-freshness without this
// test catching it.
func TestSurrealStoreProcessIDRoundTrip(t *testing.T) {
	s := startSurrealContainer(t)
	store := NewSurrealStore(s)

	// (a) Create a job with a non-empty ProcessID and confirm it round-trips.
	job := newTestJob("tk-processid-1", "repo-pid-1")
	job.ProcessID = "test-process-uuid-abc123"

	created, err := store.Create(job)
	if err != nil {
		t.Fatalf("Create with process_id: %v", err)
	}
	if created.ProcessID != "test-process-uuid-abc123" {
		t.Fatalf("Create round-trip process_id: got %q, want %q", created.ProcessID, "test-process-uuid-abc123")
	}

	// (b) GetByID must return the job with ProcessID populated.
	got := store.GetByID(job.ID)
	if got == nil {
		t.Fatal("GetByID returned nil")
	}
	if got.ProcessID != "test-process-uuid-abc123" {
		t.Fatalf("GetByID process_id: got %q, want %q", got.ProcessID, "test-process-uuid-abc123")
	}

	// (c) ListActive must return the job with ProcessID populated.
	// This is the critical assertion: reconcileZombieJobs reads via ListActive.
	// If the SELECT in ListActive doesn't include process_id (or the DTO mapping
	// drops it), reconciliation silently degrades to pure heartbeat-freshness.
	active := store.ListActive(llm.ListFilter{})
	var found *llm.Job
	for _, j := range active {
		if j.ID == job.ID {
			found = j
			break
		}
	}
	if found == nil {
		t.Fatalf("ListActive did not return the created job (id=%s)", job.ID)
	}
	if found.ProcessID != "test-process-uuid-abc123" {
		t.Fatalf("ListActive process_id: got %q, want %q; reconciliation would silently degrade",
			found.ProcessID, "test-process-uuid-abc123")
	}

	// (d) Update() must preserve ProcessID. If Update() omits process_id from
	// its SET clause, a read-modify-write caller silently zeros the field in DB;
	// the next reconciliation pass then treats the row as a pre-migration legacy
	// job and may fail it spuriously.
	got.Progress = 42 // arbitrary mutation to confirm the UPDATE fires
	if err := store.Update(got); err != nil {
		t.Fatalf("Update after Create: %v", err)
	}
	afterUpdate := store.GetByID(got.ID)
	if afterUpdate == nil {
		t.Fatal("GetByID after Update returned nil")
	}
	if afterUpdate.ProcessID != "test-process-uuid-abc123" {
		t.Fatalf("Update must preserve ProcessID: got %q, want %q", afterUpdate.ProcessID, "test-process-uuid-abc123")
	}
	if afterUpdate.Progress != 42 {
		t.Fatalf("Update must persist other fields: progress got %v, want 42", afterUpdate.Progress)
	}

	// (f) Create a job with empty ProcessID — field must be absent (or NONE) in
	// SurrealDB and round-trip as empty string. Validates the option<string>
	// field-absence contract from Decision D3: legacy rows with NONE are safe.
	jobNoID := newTestJob("tk-processid-2", "repo-pid-2")
	// ProcessID intentionally left empty.

	createdNoID, err := store.Create(jobNoID)
	if err != nil {
		t.Fatalf("Create without process_id: %v", err)
	}
	if createdNoID.ProcessID != "" {
		t.Fatalf("Create round-trip empty process_id: got %q, want empty string", createdNoID.ProcessID)
	}

	gotNoID := store.GetByID(jobNoID.ID)
	if gotNoID == nil {
		t.Fatal("GetByID (no process_id) returned nil")
	}
	if gotNoID.ProcessID != "" {
		t.Fatalf("GetByID empty process_id: got %q, want empty string", gotNoID.ProcessID)
	}

	// (e) ListActive must also return legacy/pre-migration rows (no process_id)
	// with an empty ProcessID — not a default or sentinel value. This is the
	// contract that makes startup reconciliation safe against old rows.
	listedForNoID := store.ListActive(llm.ListFilter{})
	var foundNoID *llm.Job
	for _, j := range listedForNoID {
		if j.ID == jobNoID.ID {
			foundNoID = j
			break
		}
	}
	if foundNoID == nil {
		t.Fatalf("ListActive must return job with empty ProcessID (id=%s)", jobNoID.ID)
	}
	if foundNoID.ProcessID != "" {
		t.Fatalf("ListActive empty ProcessID must round-trip as empty string, got %q", foundNoID.ProcessID)
	}
}
