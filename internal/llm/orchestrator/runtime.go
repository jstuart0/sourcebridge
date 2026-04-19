// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator

import (
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	"github.com/sourcebridge/sourcebridge/internal/llm"
)

// runtime is the orchestrator's implementation of llm.Runtime. It
// debounces progress updates (so a fast-streaming LLM does not hammer
// the store with writes) and funnels token / snapshot-size metrics
// into the same write path.
//
// Each job run gets a fresh runtime instance; callers do not share.
type runtime struct {
	orch  *Orchestrator
	jobID string

	mu                sync.Mutex
	lastProgress      float64
	lastPhase         string
	lastMessage       string
	lastWrite         time.Time
	pendingProgress   bool
	lastLoggedPhase   string
	lastLoggedMessage string

	// Token and byte metrics are buffered until flush() so a runaway
	// job cannot produce a storm of single-field updates.
	pendingTokensInput  int
	pendingTokensOutput int
	pendingTokensSet    bool
	pendingBytes        int
	pendingBytesSet     bool
}

func newRuntime(orch *Orchestrator, jobID string) *runtime {
	return &runtime{orch: orch, jobID: jobID}
}

// JobID satisfies llm.Runtime.
func (r *runtime) JobID() string { return r.jobID }

// ReportProgress records a progress update. The update is written to
// the store immediately if the debounce window has elapsed, or buffered
// and written on the next tick / flush otherwise.
func (r *runtime) ReportProgress(progress float64, phase, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	r.lastProgress = progress
	r.lastPhase = phase
	r.lastMessage = message
	r.pendingProgress = true

	now := time.Now()
	if now.Sub(r.lastWrite) >= r.orch.cfg.ProgressDebounce {
		r.writeProgressLocked(now)
	}
}

// ReportTokens records input/output token counts. Buffered until flush.
func (r *runtime) ReportTokens(input, output int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pendingTokensInput = input
	r.pendingTokensOutput = output
	r.pendingTokensSet = true
}

// ReportSnapshotBytes records the serialized input size. Buffered until flush.
func (r *runtime) ReportSnapshotBytes(bytes int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pendingBytes = bytes
	r.pendingBytesSet = true
}

// flush writes any pending metrics to the store. Called by the
// orchestrator once the job's run closure returns (success or failure)
// so the final state is always persisted.
func (r *runtime) flush() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.pendingProgress {
		r.writeProgressLocked(time.Now())
	}
	if r.pendingTokensSet {
		_ = r.orch.store.SetTokens(r.jobID, r.pendingTokensInput, r.pendingTokensOutput)
		r.pendingTokensSet = false
	}
	if r.pendingBytesSet {
		_ = r.orch.store.SetSnapshotBytes(r.jobID, r.pendingBytes)
		r.pendingBytesSet = false
	}
}

// writeProgressLocked persists the current buffered progress values. The
// caller must hold r.mu.
func (r *runtime) writeProgressLocked(now time.Time) {
	if err := r.orch.store.SetProgress(r.jobID, r.lastProgress, r.lastPhase, r.lastMessage); err != nil {
		return
	}
	r.lastWrite = now
	r.pendingProgress = false
	if r.lastPhase != r.lastLoggedPhase || r.lastMessage != r.lastLoggedMessage {
		_ = r.orch.AppendJobLog(r.jobID, llm.LogLevelInfo, r.lastPhase, "progress_update", r.lastMessage, map[string]any{
			"progress": r.lastProgress,
		})
		r.lastLoggedPhase = r.lastPhase
		r.lastLoggedMessage = r.lastMessage
	}

	if job := r.orch.store.GetByID(r.jobID); job != nil {
		r.orch.publish(llm.JobEvent{Kind: llm.EventProgress, Job: job})
	}
}

// ClassifyError maps a worker error into the same structured error codes
// used by internal/api/graphql/schema.resolvers.go:classifyError. This
// lets the orchestrator persist a consistent code into ca_llm_job and the
// Monitor page render the same human-readable titles everywhere.
//
// The classifier is intentionally a copy rather than a direct reference
// to keep the orchestrator package free of graphql imports.
func ClassifyError(err error) string {
	if err == nil {
		return ""
	}
	if st, ok := grpcstatus.FromError(err); ok {
		switch st.Code() {
		case codes.DeadlineExceeded:
			return "DEADLINE_EXCEEDED"
		case codes.Unavailable:
			return "WORKER_UNAVAILABLE"
		case codes.InvalidArgument:
			return "INVALID_ARGUMENT"
		case codes.NotFound:
			return "NOT_FOUND"
		case codes.PermissionDenied, codes.Unauthenticated:
			return "UNAUTHORIZED"
		}
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "llm returned empty content"):
		return "LLM_EMPTY"
	case strings.Contains(msg, "compute error"), strings.Contains(msg, "server_error"):
		return "PROVIDER_COMPUTE"
	case strings.Contains(msg, "snapshot too large"), strings.Contains(msg, "exceeds budget"):
		return "SNAPSHOT_TOO_LARGE"
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "context deadline"):
		return "DEADLINE_EXCEEDED"
	case strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "transport is closing"),
		strings.Contains(msg, "unavailable"):
		return "WORKER_UNAVAILABLE"
	case strings.Contains(msg, "orchestrator is shutting down"):
		return "ORCHESTRATOR_SHUTDOWN"
	}
	return "INTERNAL"
}
