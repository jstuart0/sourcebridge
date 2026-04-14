// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package llm defines the shared LLM job model and orchestration primitives
// used by every subsystem in SourceBridge that makes LLM calls (knowledge,
// reasoning, requirements, linking). The types here intentionally stay free
// of gRPC/HTTP concerns so they can be consumed by resolvers, REST handlers,
// and the orchestrator package alike.
package llm

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// JobStatus tracks the lifecycle state of an LLM job.
type JobStatus string

const (
	// StatusPending means the job has been enqueued but has not yet claimed a
	// worker slot. Jobs sit in this state when the bounded queue is saturated.
	StatusPending JobStatus = "pending"

	// StatusGenerating means a worker goroutine has claimed the job and is
	// actively running it (building prompt, calling the LLM, parsing output).
	StatusGenerating JobStatus = "generating"

	// StatusReady means the job finished successfully. Terminal state.
	StatusReady JobStatus = "ready"

	// StatusFailed means the job finished with an error (after any retries
	// the policy allowed). Terminal state.
	StatusFailed JobStatus = "failed"

	// StatusCancelled means the job was cancelled before completing. Terminal.
	StatusCancelled JobStatus = "cancelled"
)

// IsTerminal reports whether this status represents a final state.
func (s JobStatus) IsTerminal() bool {
	return s == StatusReady || s == StatusFailed || s == StatusCancelled
}

// IsActive reports whether this status represents work in flight.
// Active jobs participate in dedupe and count against the concurrency budget.
func (s JobStatus) IsActive() bool {
	return s == StatusPending || s == StatusGenerating
}

// Subsystem identifies the SourceBridge subsystem that owns a job. The value
// is used for filtering on the Monitor page and for per-subsystem metrics.
type Subsystem string

const (
	SubsystemKnowledge    Subsystem = "knowledge"
	SubsystemReasoning    Subsystem = "reasoning"
	SubsystemRequirements Subsystem = "requirements"
	SubsystemLinking      Subsystem = "linking"
	SubsystemContracts    Subsystem = "contracts"
)

// JobPriority controls queue preference. Higher-priority jobs should be
// scheduled before background maintenance and prewarm work.
type JobPriority string

const (
	PriorityInteractive JobPriority = "interactive"
	PriorityMaintenance JobPriority = "maintenance"
	PriorityPrewarm     JobPriority = "prewarm"
)

// Job is the canonical record for any LLM-backed work unit in the system.
// It is persisted to ca_llm_job and streamed to the Monitor page.
//
// The field set is deliberately broad: every subsystem shares the same row
// shape, and subsystem-specific data lives in related tables (e.g. the
// actual knowledge artifact rows in ca_knowledge_artifact) linked by
// ArtifactID. This keeps the Monitor page simple (one table to render) and
// makes it trivial to add new subsystems later.
type Job struct {
	ID        string    `json:"id"`
	Subsystem Subsystem `json:"subsystem"`
	// JobType is a subsystem-specific label (e.g. "cliff_notes",
	// "learning_path", "review", "extract_requirements", "link_requirement").
	// Used by the Monitor page for filtering and for per-type metrics.
	JobType string `json:"job_type"`
	// TargetKey is the dedupe key for this job. Two jobs with the same
	// TargetKey that are both active will be coalesced into a single run.
	// Knowledge subsystems use the canonical ArtifactKey scope key.
	TargetKey string `json:"target_key"`

	Strategy       string      `json:"strategy,omitempty"`
	Model          string      `json:"model,omitempty"`
	Priority       JobPriority `json:"priority,omitempty"`
	GenerationMode string      `json:"generation_mode,omitempty"`

	Status          JobStatus `json:"status"`
	Progress        float64   `json:"progress"`
	ProgressPhase   string    `json:"progress_phase,omitempty"`
	ProgressMessage string    `json:"progress_message,omitempty"`

	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`

	RetryCount  int `json:"retry_count"`
	MaxAttempts int `json:"max_attempts"`
	TimeoutSec  int `json:"timeout_sec"`
	// AttachedRequests counts how many enqueue attempts coalesced into this
	// job through dedupe. The initial request sets it to 1.
	AttachedRequests int `json:"attached_requests"`

	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	SnapshotBytes    int `json:"snapshot_bytes"`
	ReusedSummaries  int `json:"reused_summaries"`
	LeafCacheHits    int `json:"leaf_cache_hits"`
	FileCacheHits    int `json:"file_cache_hits"`
	PackageCacheHits int `json:"package_cache_hits"`
	RootCacheHits    int `json:"root_cache_hits"`
	CachedNodesLoaded int    `json:"cached_nodes_loaded"`
	TotalNodes        int    `json:"total_nodes"`
	ResumeStage       string `json:"resume_stage,omitempty"`
	SkippedLeafUnits  int    `json:"skipped_leaf_units"`
	SkippedFileUnits  int    `json:"skipped_file_units"`
	SkippedPackageUnits int  `json:"skipped_package_units"`
	SkippedRootUnits  int    `json:"skipped_root_units"`

	// ArtifactID (optional) links the job back to a domain record — a
	// ca_knowledge_artifact row for knowledge jobs, a requirement id for
	// requirements jobs, etc. Used by the Monitor page to deep-link into
	// the owning resource view.
	ArtifactID string `json:"artifact_id,omitempty"`
	// RepoID (optional) enables per-repo filtering on the Monitor page.
	RepoID string `json:"repo_id,omitempty"`

	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// Elapsed returns how long the job has been running. For terminal jobs this
// is the wall-clock duration from StartedAt (or CreatedAt if the job was
// cancelled before claiming a slot) to CompletedAt. For active jobs it is
// the time since StartedAt or CreatedAt.
func (j *Job) Elapsed() time.Duration {
	if j == nil {
		return 0
	}
	start := j.CreatedAt
	if j.StartedAt != nil && !j.StartedAt.IsZero() {
		start = *j.StartedAt
	}
	end := time.Now()
	if j.CompletedAt != nil && !j.CompletedAt.IsZero() {
		end = *j.CompletedAt
	}
	if end.Before(start) {
		return 0
	}
	return end.Sub(start)
}

// EnqueueRequest describes the work to be done. Callers fill it in from
// whatever domain context they have and pass it to Orchestrator.Enqueue.
// The orchestrator materializes a Job from this request.
type EnqueueRequest struct {
	Subsystem      Subsystem
	JobType        string
	TargetKey      string
	Strategy       string
	Model          string
	Priority       JobPriority
	GenerationMode string
	ArtifactID     string
	RepoID         string
	TimeoutSec     int
	MaxAttempts    int
	// RunWithContext is the preferred execution hook. The orchestrator supplies
	// a cancellable context so queued/running jobs can be cancelled cleanly.
	// When nil, Run is used for backward compatibility.
	RunWithContext func(ctx context.Context, rt Runtime) error
	// Run is the closure that performs the actual work. It receives a
	// Runtime for reporting progress, tokens, and errors. The orchestrator
	// calls Run inside the bounded queue worker, so Run must be safe to
	// invoke from an arbitrary goroutine.
	Run func(rt Runtime) error
}

// Validate returns a non-nil error when the request is missing required fields.
func (r *EnqueueRequest) Validate() error {
	if r == nil {
		return fmt.Errorf("enqueue request is nil")
	}
	if strings.TrimSpace(string(r.Subsystem)) == "" {
		return fmt.Errorf("subsystem is required")
	}
	if strings.TrimSpace(r.JobType) == "" {
		return fmt.Errorf("job_type is required")
	}
	if strings.TrimSpace(r.TargetKey) == "" {
		return fmt.Errorf("target_key is required (used for dedupe)")
	}
	if r.Priority == "" {
		r.Priority = PriorityInteractive
	}
	if r.RunWithContext == nil && r.Run == nil {
		return fmt.Errorf("run function is required")
	}
	return nil
}

// Runtime is the callback surface an Enqueued job uses to report progress,
// token usage, and errors back to the orchestrator. Implementations are
// provided by the orchestrator package; tests can supply fakes.
type Runtime interface {
	// JobID returns the persisted id of the job currently running.
	JobID() string
	// ReportProgress updates the job's progress (0.0-1.0), phase label,
	// and human-readable message. Updates are debounced by the orchestrator
	// to avoid write amplification.
	ReportProgress(progress float64, phase, message string)
	// ReportTokens records the input/output token counts for billing and
	// metrics. Typically called once at the end of the job.
	ReportTokens(input, output int)
	// ReportSnapshotBytes records the serialized input size for debugging
	// (e.g. how big the knowledge snapshot was).
	ReportSnapshotBytes(bytes int)
}

// JobEvent is a change notification emitted by the orchestrator as jobs
// move through their lifecycle. The Monitor page subscribes to events
// over SSE; the resolvers consume them to map back to domain objects.
type JobEvent struct {
	Kind EventKind `json:"kind"`
	Job  *Job      `json:"job"`
}

// EventKind enumerates the lifecycle transitions.
type EventKind string

const (
	EventCreated   EventKind = "created"
	EventStarted   EventKind = "started"
	EventProgress  EventKind = "progress"
	EventCompleted EventKind = "completed"
	EventFailed    EventKind = "failed"
	EventCancelled EventKind = "cancelled"
)
