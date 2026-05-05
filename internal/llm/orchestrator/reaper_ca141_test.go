// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Tests added during CA-141 stage-11 reconciliation to satisfy Valerie's
// punch-list items 1–3 (plan steps 1.8d, 1.8e, 1.8f).

package orchestrator

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/llm"
)

// syncBuffer wraps bytes.Buffer with a mutex so concurrent slog writes
// (from background goroutines) and test reads don't race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestReaper_NonHeartbeatSubsystem_ReapedAt10Min — a knowledge job whose
// UpdatedAt is staleGeneratingThreshold+1s (11 min) stale IS reaped and
// transitions to StatusFailed. Companion to the "not reaped at 6 min" test
// above it; together they bracket the exact threshold.
//
// Plan step 1.8d (CA-141 stage-11 punch-list item 1).
func TestReaper_NonHeartbeatSubsystem_ReapedAt10Min(t *testing.T) {
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })

	orch := newTestOrchestrator(t, Config{MaxConcurrency: 1})

	job, err := orch.Enqueue(&llm.EnqueueRequest{
		Subsystem:   llm.SubsystemKnowledge,
		LLMProvider: "test",
		JobType:     "cliff_notes",
		TargetKey:   "ca141-knowledge-11min",
		RunWithContext: func(ctx context.Context, _ llm.Runtime) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-hold:
				return nil
			}
		},
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Wait for the worker to set StatusGenerating before backdating.
	waitFor(t, 2*time.Second, func() bool {
		j := orch.GetJob(job.ID)
		return j != nil && j.Status == llm.StatusGenerating
	})

	// Backdate to staleGeneratingThreshold + 1s so we're just past the 10-min wall.
	backdate(t, orch, job.ID, staleGeneratingThreshold+time.Second)

	orch.reapStaleJobs()

	got := orch.GetJob(job.ID)
	if got == nil {
		t.Fatal("expected job to exist after reap")
	}
	if got.Status != llm.StatusFailed {
		t.Fatalf("expected knowledge job reaped at 11 min → StatusFailed; got status=%v", got.Status)
	}
}

// TestReaper_TickCadence_15s locks the reaperTickInterval constant to 15 s.
// A regression to a longer interval would silently degrade stuck-job detection
// back toward the pre-CA-141 ~32 min worst case.
//
// Plan step 1.8e (CA-141 stage-11 punch-list item 2).
func TestReaper_TickCadence_15s(t *testing.T) {
	if reaperTickInterval != 15*time.Second {
		t.Fatalf("reaperTickInterval=%v; want 15s — changing this degrades stuck-job detection, see CA-141", reaperTickInterval)
	}
}

// TestReaper_LogsThresholdKind verifies that reapStaleJobs emits the
// threshold_kind structured field with the correct value for each code path:
//   - "heartbeat_stale" for a living_wiki job (heartbeat allow-list path)
//   - "generating_wall" for a knowledge job (non-heartbeat wall-clock path)
//
// Plan step 1.8f (CA-141 stage-11 punch-list item 3).
func TestReaper_LogsThresholdKind(t *testing.T) {
	cases := []struct {
		name       string
		subsystem  llm.Subsystem
		backdateBy time.Duration
		wantKind   string
	}{
		{
			name:       "heartbeat_stale for living_wiki",
			subsystem:  "living_wiki",
			backdateBy: heartbeatStaleThreshold + time.Second,
			wantKind:   "heartbeat_stale",
		},
		{
			name:       "generating_wall for knowledge",
			subsystem:  llm.SubsystemKnowledge,
			backdateBy: staleGeneratingThreshold + time.Second,
			wantKind:   "generating_wall",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Use a channel to hold the Run goroutine alive until the test is done.
			// This prevents the worker from resetting UpdatedAt after backdate.
			hold := make(chan struct{})
			t.Cleanup(func() { close(hold) })

			orch := newTestOrchestrator(t, Config{MaxConcurrency: 1})

			job, err := orch.Enqueue(&llm.EnqueueRequest{
				Subsystem:   tc.subsystem,
				LLMProvider: "test",
				JobType:     "test_job",
				TargetKey:   "ca141-threshold-kind-" + string(tc.subsystem),
				RunWithContext: func(ctx context.Context, _ llm.Runtime) error {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-hold:
						return nil
					}
				},
			})
			if err != nil {
				t.Fatalf("enqueue: %v", err)
			}

			// Wait for the worker to transition the job to StatusGenerating before
			// backdating. This avoids a race where the worker's SetStatus call
			// resets UpdatedAt after our backdate.
			waitFor(t, 2*time.Second, func() bool {
				j := orch.GetJob(job.ID)
				return j != nil && j.Status == llm.StatusGenerating
			})

			backdate(t, orch, job.ID, tc.backdateBy)

			// Use a mutex-protected buffer to avoid data races on writes from
			// background goroutines that share the global slog logger.
			logBuf := &syncBuffer{}
			prev := slog.Default()
			t.Cleanup(func() { slog.SetDefault(prev) })
			slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))

			orch.reapStaleJobs()

			output := logBuf.String()
			wantField := "threshold_kind=" + tc.wantKind
			if !strings.Contains(output, wantField) {
				t.Errorf("expected log to contain %q; got:\n%s", wantField, output)
			}
		})
	}
}
