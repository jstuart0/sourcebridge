// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package llm

import "time"

// JobLogLevel classifies a structured job log entry for UI highlighting.
type JobLogLevel string

const (
	LogLevelDebug JobLogLevel = "debug"
	LogLevelInfo  JobLogLevel = "info"
	LogLevelWarn  JobLogLevel = "warn"
	LogLevelError JobLogLevel = "error"
)

// JobLogEntry is an application-level, job-scoped log line persisted in
// ca_llm_job_log and streamed to the in-app monitor. PayloadJSON is optional
// structured detail encoded as JSON so the UI can pretty-print it lazily.
type JobLogEntry struct {
	ID          string      `json:"id"`
	JobID       string      `json:"job_id"`
	RepoID      string      `json:"repo_id,omitempty"`
	ArtifactID  string      `json:"artifact_id,omitempty"`
	Subsystem   Subsystem   `json:"subsystem,omitempty"`
	JobType     string      `json:"job_type,omitempty"`
	// LLMProvider mirrors Job.LLMProvider so per-log-line filtering /
	// metrics can attribute work without joining back to the job row.
	// R3 slice 3.
	LLMProvider string      `json:"llm_provider,omitempty"`
	Level       JobLogLevel `json:"level"`
	Phase       string      `json:"phase,omitempty"`
	Event       string      `json:"event"`
	Message     string      `json:"message"`
	PayloadJSON string      `json:"payload_json,omitempty"`
	Sequence    int64       `json:"sequence"`
	CreatedAt   time.Time   `json:"created_at"`
}

// JobLogFilter narrows log listing for one job.
type JobLogFilter struct {
	Limit         int
	AfterSequence int64
}
