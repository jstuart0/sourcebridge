// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package llm

import (
	"testing"
)

// TestMemStore_LLMProviderRoundTrip verifies that the MemStore preserves
// Job.LLMProvider through Create + GetByID + List, and JobLogEntry.
// LLMProvider through AppendLog + ListLogs. R3 slice 3.
func TestMemStore_LLMProviderRoundTrip(t *testing.T) {
	s := NewMemStore()

	job := &Job{
		ID:          "job-rt-1",
		Subsystem:   SubsystemKnowledge,
		JobType:     "cliff_notes",
		TargetKey:   "tk:1",
		LLMProvider: "anthropic",
		Status:      StatusPending,
	}
	if _, err := s.Create(job); err != nil {
		t.Fatalf("create: %v", err)
	}

	got := s.GetByID("job-rt-1")
	if got == nil {
		t.Fatalf("GetByID returned nil")
	}
	if got.LLMProvider != "anthropic" {
		t.Errorf("Job.LLMProvider round-trip: got %q, want anthropic", got.LLMProvider)
	}

	entry, err := s.AppendLog(&JobLogEntry{
		JobID:       "job-rt-1",
		LLMProvider: "anthropic",
		Level:       LogLevelInfo,
		Event:       "test",
		Message:     "test",
		Sequence:    1,
	})
	if err != nil {
		t.Fatalf("append log: %v", err)
	}
	if entry.LLMProvider != "anthropic" {
		t.Errorf("JobLogEntry.LLMProvider on append: got %q, want anthropic", entry.LLMProvider)
	}

	logs := s.ListLogs("job-rt-1", JobLogFilter{})
	if len(logs) != 1 || logs[0].LLMProvider != "anthropic" {
		t.Errorf("JobLogEntry.LLMProvider round-trip via ListLogs failed: %+v", logs)
	}
}
