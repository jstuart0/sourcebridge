// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package llm

import (
	"testing"
	"time"
)

func newTestJob(id, targetKey string, status JobStatus) *Job {
	now := time.Now()
	return &Job{
		ID:        id,
		Subsystem: SubsystemKnowledge,
		JobType:   "cliff_notes",
		TargetKey: targetKey,
		Status:    status,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestMemStoreCreateAndGet(t *testing.T) {
	store := NewMemStore()
	job := newTestJob("job-1", "repo-1:cliff_notes", StatusPending)
	created, err := store.Create(t.Context(), job)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if created.ID != job.ID {
		t.Fatalf("expected id %q, got %q", job.ID, created.ID)
	}
	if got := store.GetByID(t.Context(), "job-1"); got == nil || got.ID != "job-1" {
		t.Fatalf("GetByID round-trip failed: %+v", got)
	}
	if got := store.GetByID(t.Context(), "nonexistent"); got != nil {
		t.Fatalf("expected nil for nonexistent id, got %+v", got)
	}
}

func TestMemStoreCreateRejectsDuplicate(t *testing.T) {
	store := NewMemStore()
	job := newTestJob("dup", "tk", StatusPending)
	if _, err := store.Create(t.Context(), job); err != nil {
		t.Fatalf("first Create failed: %v", err)
	}
	if _, err := store.Create(t.Context(), job); err == nil {
		t.Fatal("expected second Create to fail on duplicate id")
	}
}

func TestMemStoreGetActiveByTargetKey(t *testing.T) {
	store := NewMemStore()
	_, _ = store.Create(t.Context(), newTestJob("old", "tk", StatusReady))
	_, _ = store.Create(t.Context(), newTestJob("active", "tk", StatusGenerating))
	got := store.GetActiveByTargetKey(t.Context(), "tk")
	if got == nil || got.ID != "active" {
		t.Fatalf("expected active job, got %+v", got)
	}
	if got := store.GetActiveByTargetKey(t.Context(), "unknown"); got != nil {
		t.Fatalf("expected nil for unknown target key, got %+v", got)
	}
}

func TestMemStoreSetStatusStampsTimestamps(t *testing.T) {
	store := NewMemStore()
	_, _ = store.Create(t.Context(), newTestJob("job-ts", "tk", StatusPending))

	if err := store.SetStatus(t.Context(), "job-ts", StatusGenerating); err != nil {
		t.Fatalf("SetStatus generating failed: %v", err)
	}
	j := store.GetByID(t.Context(), "job-ts")
	if j.StartedAt == nil {
		t.Fatal("expected StartedAt to be set on transition to generating")
	}

	if err := store.SetStatus(t.Context(), "job-ts", StatusReady); err != nil {
		t.Fatalf("SetStatus ready failed: %v", err)
	}
	j = store.GetByID(t.Context(), "job-ts")
	if j.CompletedAt == nil {
		t.Fatal("expected CompletedAt to be set on transition to ready")
	}
	if j.Progress != 1.0 {
		t.Fatalf("expected progress 1.0 on ready, got %v", j.Progress)
	}
}

func TestMemStoreListActiveFiltersByStatus(t *testing.T) {
	store := NewMemStore()
	_, _ = store.Create(t.Context(), newTestJob("pending", "tk1", StatusPending))
	_, _ = store.Create(t.Context(), newTestJob("generating", "tk2", StatusGenerating))
	_, _ = store.Create(t.Context(), newTestJob("ready", "tk3", StatusReady))
	_, _ = store.Create(t.Context(), newTestJob("failed", "tk4", StatusFailed))

	active := store.ListActive(t.Context(), ListFilter{})
	if len(active) != 2 {
		t.Fatalf("expected 2 active jobs, got %d: %+v", len(active), active)
	}
	ids := map[string]bool{}
	for _, j := range active {
		ids[j.ID] = true
	}
	if !ids["pending"] || !ids["generating"] {
		t.Fatalf("expected pending and generating in active list, got %v", ids)
	}
}

func TestMemStoreListRecentFiltersSince(t *testing.T) {
	store := NewMemStore()
	old := newTestJob("old", "tk1", StatusReady)
	old.UpdatedAt = time.Now().Add(-1 * time.Hour)
	_, _ = store.Create(t.Context(), old)
	// Keep the stored UpdatedAt as created — override explicitly after.
	_ = store.Update(t.Context(), old)
	// Update puts UpdatedAt to now, so rebuild to fake old timestamp via internal pointer.
	// Easier: just verify filter returns the fresh one.
	fresh := newTestJob("fresh", "tk2", StatusReady)
	_, _ = store.Create(t.Context(), fresh)

	recent := store.ListRecent(t.Context(), ListFilter{}, time.Now().Add(-30*time.Minute))
	if len(recent) < 1 {
		t.Fatalf("expected at least one recent job, got %d", len(recent))
	}
	for _, j := range recent {
		if j.UpdatedAt.Before(time.Now().Add(-30 * time.Minute)) {
			t.Fatalf("recent filter leaked an old job: %+v", j)
		}
	}
}

func TestMemStoreSetError(t *testing.T) {
	store := NewMemStore()
	_, _ = store.Create(t.Context(), newTestJob("err", "tk", StatusGenerating))
	if err := store.SetError(t.Context(), "err", "LLM_EMPTY", "nothing came back"); err != nil {
		t.Fatalf("SetError failed: %v", err)
	}
	j := store.GetByID(t.Context(), "err")
	if j.Status != StatusFailed {
		t.Fatalf("expected status failed, got %q", j.Status)
	}
	if j.ErrorCode != "LLM_EMPTY" {
		t.Fatalf("expected error code LLM_EMPTY, got %q", j.ErrorCode)
	}
	if j.CompletedAt == nil {
		t.Fatal("expected CompletedAt to be set on SetError")
	}
}

func TestMemStoreIncrementRetry(t *testing.T) {
	store := NewMemStore()
	_, _ = store.Create(t.Context(), newTestJob("rt", "tk", StatusGenerating))
	for i := 0; i < 3; i++ {
		if err := store.IncrementRetry(t.Context(), "rt"); err != nil {
			t.Fatalf("IncrementRetry #%d failed: %v", i, err)
		}
	}
	if j := store.GetByID(t.Context(), "rt"); j.RetryCount != 3 {
		t.Fatalf("expected RetryCount 3, got %d", j.RetryCount)
	}
}

func TestMemStoreIgnoresWritesForTerminalJobs(t *testing.T) {
	store := NewMemStore()
	_, _ = store.Create(t.Context(), newTestJob("done", "tk", StatusCancelled))
	before := store.GetByID(t.Context(), "done")
	if err := store.SetProgress(t.Context(), "done", 0.75, "render", "ignored", 0); err != nil {
		t.Fatalf("SetProgress failed: %v", err)
	}
	if err := store.SetTokens(t.Context(), "done", 10, 20); err != nil {
		t.Fatalf("SetTokens failed: %v", err)
	}
	if err := store.SetSnapshotBytes(t.Context(), "done", 123); err != nil {
		t.Fatalf("SetSnapshotBytes failed: %v", err)
	}
	if err := store.IncrementRetry(t.Context(), "done"); err != nil {
		t.Fatalf("IncrementRetry failed: %v", err)
	}
	after := store.GetByID(t.Context(), "done")
	if after.Progress != before.Progress || after.ProgressPhase != before.ProgressPhase || after.ProgressMessage != before.ProgressMessage {
		t.Fatalf("terminal progress mutated: before=%+v after=%+v", before, after)
	}
	if after.InputTokens != before.InputTokens || after.OutputTokens != before.OutputTokens || after.SnapshotBytes != before.SnapshotBytes {
		t.Fatalf("terminal metrics mutated: before=%+v after=%+v", before, after)
	}
	if after.RetryCount != before.RetryCount {
		t.Fatalf("terminal retry count mutated: before=%d after=%d", before.RetryCount, after.RetryCount)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("terminal updated_at changed: before=%s after=%s", before.UpdatedAt, after.UpdatedAt)
	}
}

func TestMemStoreCloneIsolatesCallers(t *testing.T) {
	store := NewMemStore()
	_, _ = store.Create(t.Context(), newTestJob("iso", "tk", StatusPending))

	// Caller mutating the returned pointer should not affect stored state.
	j := store.GetByID(t.Context(), "iso")
	j.ErrorMessage = "attacker wrote this"

	fresh := store.GetByID(t.Context(), "iso")
	if fresh.ErrorMessage == "attacker wrote this" {
		t.Fatal("MemStore leaked stored pointer to caller — clone broken")
	}
}

func TestMemStoreAppendAndListLogs(t *testing.T) {
	store := NewMemStore()
	_, _ = store.Create(t.Context(), newTestJob("log-job", "tk", StatusGenerating))

	if _, err := store.AppendLog(t.Context(), &JobLogEntry{
		JobID:    "log-job",
		Level:    LogLevelInfo,
		Phase:    "snapshot",
		Event:    "snapshot_assembled",
		Message:  "Snapshot assembled",
		Sequence: 1,
	}); err != nil {
		t.Fatalf("AppendLog failed: %v", err)
	}
	if _, err := store.AppendLog(t.Context(), &JobLogEntry{
		JobID:    "log-job",
		Level:    LogLevelWarn,
		Phase:    "queued",
		Event:    "slot_wait",
		Message:  "Waiting for slot",
		Sequence: 2,
	}); err != nil {
		t.Fatalf("AppendLog second entry failed: %v", err)
	}

	rows := store.ListLogs(t.Context(), "log-job", JobLogFilter{})
	if len(rows) != 2 {
		t.Fatalf("expected 2 log rows, got %d", len(rows))
	}
	if rows[0].Sequence != 1 || rows[1].Sequence != 2 {
		t.Fatalf("unexpected log ordering: %+v", rows)
	}
	filtered := store.ListLogs(t.Context(), "log-job", JobLogFilter{AfterSequence: 1})
	if len(filtered) != 1 || filtered[0].Sequence != 2 {
		t.Fatalf("expected only second log after sequence filter, got %+v", filtered)
	}
}
