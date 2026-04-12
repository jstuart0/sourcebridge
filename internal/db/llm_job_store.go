// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"fmt"
	"time"

	surrealdb "github.com/surrealdb/surrealdb.go"
	"github.com/surrealdb/surrealdb.go/pkg/models"

	"github.com/sourcebridge/sourcebridge/internal/llm"
)

// Verify at compile time that *SurrealStore satisfies llm.JobStore.
var _ llm.JobStore = (*SurrealStore)(nil)

// surrealLLMJob is the CBOR-friendly DTO for the ca_llm_job table. It
// mirrors llm.Job but uses surrealTime for datetime fields so we can decode
// both native CBOR datetimes and legacy string values, and uses
// option-shaped string fields so absent rows decode cleanly.
type surrealLLMJob struct {
	ID               *models.RecordID `json:"id,omitempty"`
	Subsystem        string           `json:"subsystem"`
	JobType          string           `json:"job_type"`
	TargetKey        string           `json:"target_key"`
	Strategy         string           `json:"strategy"`
	Model            string           `json:"model"`
	Status           string           `json:"status"`
	Progress         float64          `json:"progress"`
	ProgressPhase    string           `json:"progress_phase"`
	ProgressMessage  string           `json:"progress_message"`
	ErrorCode        string           `json:"error_code"`
	ErrorMessage     string           `json:"error_message"`
	RetryCount       int              `json:"retry_count"`
	MaxAttempts      int              `json:"max_attempts"`
	TimeoutSec       int              `json:"timeout_sec"`
	AttachedRequests int              `json:"attached_requests"`
	InputTokens      int              `json:"input_tokens"`
	OutputTokens     int              `json:"output_tokens"`
	SnapshotBytes    int              `json:"snapshot_bytes"`
	ArtifactID       string           `json:"artifact_id"`
	RepoID           string           `json:"repo_id"`
	CreatedAt        surrealTime      `json:"created_at"`
	StartedAt        surrealTime      `json:"started_at"`
	UpdatedAt        surrealTime      `json:"updated_at"`
	CompletedAt      surrealTime      `json:"completed_at"`
}

func (r *surrealLLMJob) toJob() *llm.Job {
	job := &llm.Job{
		ID:               recordIDString(r.ID),
		Subsystem:        llm.Subsystem(r.Subsystem),
		JobType:          r.JobType,
		TargetKey:        r.TargetKey,
		Strategy:         r.Strategy,
		Model:            r.Model,
		Status:           llm.JobStatus(r.Status),
		Progress:         r.Progress,
		ProgressPhase:    r.ProgressPhase,
		ProgressMessage:  r.ProgressMessage,
		ErrorCode:        r.ErrorCode,
		ErrorMessage:     r.ErrorMessage,
		RetryCount:       r.RetryCount,
		MaxAttempts:      r.MaxAttempts,
		TimeoutSec:       r.TimeoutSec,
		AttachedRequests: r.AttachedRequests,
		InputTokens:      r.InputTokens,
		OutputTokens:     r.OutputTokens,
		SnapshotBytes:    r.SnapshotBytes,
		ArtifactID:       r.ArtifactID,
		RepoID:           r.RepoID,
		CreatedAt:        r.CreatedAt.Time,
		UpdatedAt:        r.UpdatedAt.Time,
	}
	if !r.StartedAt.Time.IsZero() {
		t := r.StartedAt.Time
		job.StartedAt = &t
	}
	if !r.CompletedAt.Time.IsZero() {
		t := r.CompletedAt.Time
		job.CompletedAt = &t
	}
	return job
}

// ---------------------------------------------------------------------------
// LLM job operations
// ---------------------------------------------------------------------------

// Create inserts a new job record. The job must have a non-empty ID; the
// orchestrator generates one before calling Create.
func (s *SurrealStore) Create(job *llm.Job) (*llm.Job, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	if job == nil || job.ID == "" {
		return nil, fmt.Errorf("job id is required")
	}
	status := job.Status
	if status == "" {
		status = llm.StatusPending
	}

	sql := `CREATE ca_llm_job SET
		id = type::thing('ca_llm_job', $id),
		subsystem = $subsystem,
		job_type = $job_type,
		target_key = $target_key,
		strategy = $strategy,
		model = $model,
		status = $status,
		progress = $progress,
		progress_phase = $progress_phase,
		progress_message = $progress_message,
		error_code = $error_code,
		error_message = $error_message,
		retry_count = $retry_count,
		max_attempts = $max_attempts,
		timeout_sec = $timeout_sec,
		attached_requests = $attached_requests,
		input_tokens = $input_tokens,
		output_tokens = $output_tokens,
		snapshot_bytes = $snapshot_bytes,
		artifact_id = $artifact_id,
		repo_id = $repo_id,
		created_at = time::now(),
		updated_at = time::now()`

	vars := map[string]any{
		"id":                job.ID,
		"subsystem":         string(job.Subsystem),
		"job_type":          job.JobType,
		"target_key":        job.TargetKey,
		"strategy":          job.Strategy,
		"model":             job.Model,
		"status":            string(status),
		"progress":          job.Progress,
		"progress_phase":    job.ProgressPhase,
		"progress_message":  job.ProgressMessage,
		"error_code":        job.ErrorCode,
		"error_message":     job.ErrorMessage,
		"retry_count":       job.RetryCount,
		"max_attempts":      job.MaxAttempts,
		"timeout_sec":       job.TimeoutSec,
		"attached_requests": job.AttachedRequests,
		"input_tokens":      job.InputTokens,
		"output_tokens":     job.OutputTokens,
		"snapshot_bytes":    job.SnapshotBytes,
		"artifact_id":       job.ArtifactID,
		"repo_id":           job.RepoID,
	}

	if _, err := surrealdb.Query[interface{}](ctx(), db, sql, vars); err != nil {
		return nil, fmt.Errorf("create llm job: %w", err)
	}
	return s.GetByID(job.ID), nil
}

// Update replaces the stored record. Callers should fetch, mutate, and
// write back the full job — the SetFoo helpers below are preferred for
// targeted updates to avoid write amplification.
func (s *SurrealStore) Update(job *llm.Job) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	if job == nil || job.ID == "" {
		return fmt.Errorf("job id is required")
	}
	sql := `UPDATE type::thing('ca_llm_job', $id) SET
		subsystem = $subsystem,
		job_type = $job_type,
		target_key = $target_key,
		strategy = $strategy,
		model = $model,
		status = $status,
		progress = $progress,
		progress_phase = $progress_phase,
		progress_message = $progress_message,
		error_code = $error_code,
		error_message = $error_message,
		retry_count = $retry_count,
		max_attempts = $max_attempts,
		timeout_sec = $timeout_sec,
		attached_requests = $attached_requests,
		input_tokens = $input_tokens,
		output_tokens = $output_tokens,
		snapshot_bytes = $snapshot_bytes,
		artifact_id = $artifact_id,
		repo_id = $repo_id,
		updated_at = time::now()`
	vars := map[string]any{
		"id":                job.ID,
		"subsystem":         string(job.Subsystem),
		"job_type":          job.JobType,
		"target_key":        job.TargetKey,
		"strategy":          job.Strategy,
		"model":             job.Model,
		"status":            string(job.Status),
		"progress":          job.Progress,
		"progress_phase":    job.ProgressPhase,
		"progress_message":  job.ProgressMessage,
		"error_code":        job.ErrorCode,
		"error_message":     job.ErrorMessage,
		"retry_count":       job.RetryCount,
		"max_attempts":      job.MaxAttempts,
		"timeout_sec":       job.TimeoutSec,
		"attached_requests": job.AttachedRequests,
		"input_tokens":      job.InputTokens,
		"output_tokens":     job.OutputTokens,
		"snapshot_bytes":    job.SnapshotBytes,
		"artifact_id":       job.ArtifactID,
		"repo_id":           job.RepoID,
	}
	_, err := queryOne[interface{}](ctx(), db, sql, vars)
	return err
}

// GetByID returns the job with the given id, or nil.
func (s *SurrealStore) GetByID(id string) *llm.Job {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	rows, err := queryOne[[]surrealLLMJob](ctx(), db,
		"SELECT * FROM type::thing('ca_llm_job', $id)",
		map[string]any{"id": id})
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rows[0].toJob()
}

// GetActiveByTargetKey returns the newest active job for a target key, or nil.
func (s *SurrealStore) GetActiveByTargetKey(targetKey string) *llm.Job {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	rows, err := queryOne[[]surrealLLMJob](ctx(), db,
		`SELECT * FROM ca_llm_job
		 WHERE target_key = $target_key
		   AND status IN ['pending', 'generating']
		 ORDER BY updated_at DESC
		 LIMIT 1`,
		map[string]any{"target_key": targetKey})
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rows[0].toJob()
}

// ListActive returns all active jobs matching the filter.
func (s *SurrealStore) ListActive(filter llm.ListFilter) []*llm.Job {
	statuses := filter.Statuses
	if len(statuses) == 0 {
		statuses = []llm.JobStatus{llm.StatusPending, llm.StatusGenerating}
	}
	return s.listJobs(filter, statuses, time.Time{})
}

// ListRecent returns terminal jobs updated on or after since.
func (s *SurrealStore) ListRecent(filter llm.ListFilter, since time.Time) []*llm.Job {
	statuses := filter.Statuses
	if len(statuses) == 0 {
		statuses = []llm.JobStatus{llm.StatusReady, llm.StatusFailed, llm.StatusCancelled}
	}
	return s.listJobs(filter, statuses, since)
}

func (s *SurrealStore) listJobs(filter llm.ListFilter, statuses []llm.JobStatus, since time.Time) []*llm.Job {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	statusStrings := make([]string, 0, len(statuses))
	for _, st := range statuses {
		statusStrings = append(statusStrings, string(st))
	}

	// Build WHERE clauses dynamically. We intentionally keep this simple —
	// SurrealDB supports IN with a bound parameter, and we append extra
	// clauses only when the corresponding filter is set.
	sql := "SELECT * FROM ca_llm_job WHERE status IN $statuses"
	vars := map[string]any{"statuses": statusStrings}

	if filter.Subsystem != "" {
		sql += " AND subsystem = $subsystem"
		vars["subsystem"] = string(filter.Subsystem)
	}
	if filter.JobType != "" {
		sql += " AND job_type = $job_type"
		vars["job_type"] = filter.JobType
	}
	if filter.RepoID != "" {
		sql += " AND repo_id = $repo_id"
		vars["repo_id"] = filter.RepoID
	}
	if filter.ArtifactID != "" {
		sql += " AND artifact_id = $artifact_id"
		vars["artifact_id"] = filter.ArtifactID
	}
	if filter.TargetKey != "" {
		sql += " AND target_key = $target_key"
		vars["target_key"] = filter.TargetKey
	}
	if !since.IsZero() {
		sql += " AND updated_at >= $since"
		vars["since"] = since.Format(time.RFC3339Nano)
	}
	sql += " ORDER BY updated_at DESC"
	if filter.Limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	rows, err := queryOne[[]surrealLLMJob](ctx(), db, sql, vars)
	if err != nil {
		return nil
	}
	results := make([]*llm.Job, 0, len(rows))
	for _, r := range rows {
		results = append(results, r.toJob())
	}
	return results
}

// SetStatus transitions the job state. Timestamps are stamped server-side
// so the database is authoritative for wall-clock measurement.
func (s *SurrealStore) SetStatus(id string, status llm.JobStatus) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	// Choose the SQL based on the destination status so we can stamp
	// started_at/completed_at atomically with the status write.
	switch status {
	case llm.StatusGenerating:
		_, err := queryOne[interface{}](ctx(), db,
			`UPDATE type::thing('ca_llm_job', $id) SET
				status = $status,
				started_at = time::now(),
				updated_at = time::now()`,
			map[string]any{"id": id, "status": string(status)})
		return err
	case llm.StatusReady:
		_, err := queryOne[interface{}](ctx(), db,
			`UPDATE type::thing('ca_llm_job', $id) SET
				status = $status,
				progress = 1.0,
				error_code = '',
				error_message = '',
				completed_at = time::now(),
				updated_at = time::now()`,
			map[string]any{"id": id, "status": string(status)})
		return err
	case llm.StatusFailed, llm.StatusCancelled:
		_, err := queryOne[interface{}](ctx(), db,
			`UPDATE type::thing('ca_llm_job', $id) SET
				status = $status,
				completed_at = time::now(),
				updated_at = time::now()`,
			map[string]any{"id": id, "status": string(status)})
		return err
	default:
		_, err := queryOne[interface{}](ctx(), db,
			`UPDATE type::thing('ca_llm_job', $id) SET
				status = $status,
				updated_at = time::now()`,
			map[string]any{"id": id, "status": string(status)})
		return err
	}
}

// SetProgress updates the progress fields.
func (s *SurrealStore) SetProgress(id string, progress float64, phase, message string) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	_, err := queryOne[interface{}](ctx(), db,
		`UPDATE type::thing('ca_llm_job', $id) SET
			progress = $progress,
			progress_phase = $phase,
			progress_message = $message,
			updated_at = time::now()
		  WHERE status = 'pending' OR status = 'generating'`,
		map[string]any{
			"id":       id,
			"progress": progress,
			"phase":    phase,
			"message":  message,
		})
	return err
}

// SetError marks the job failed with a classified error code.
func (s *SurrealStore) SetError(id string, code, message string) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	_, err := queryOne[interface{}](ctx(), db,
		`UPDATE type::thing('ca_llm_job', $id) SET
			status = $status,
			error_code = $code,
			error_message = $message,
			completed_at = time::now(),
			updated_at = time::now()`,
		map[string]any{
			"id":      id,
			"status":  string(llm.StatusFailed),
			"code":    code,
			"message": message,
		})
	return err
}

// SetTokens records input/output token usage.
func (s *SurrealStore) SetTokens(id string, input, output int) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	_, err := queryOne[interface{}](ctx(), db,
		`UPDATE type::thing('ca_llm_job', $id) SET
			input_tokens = $input,
			output_tokens = $output,
			updated_at = time::now()
		  WHERE status = 'pending' OR status = 'generating'`,
		map[string]any{"id": id, "input": input, "output": output})
	return err
}

// SetSnapshotBytes records the serialized input size.
func (s *SurrealStore) SetSnapshotBytes(id string, bytes int) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	_, err := queryOne[interface{}](ctx(), db,
		`UPDATE type::thing('ca_llm_job', $id) SET
			snapshot_bytes = $bytes,
			updated_at = time::now()
		  WHERE status = 'pending' OR status = 'generating'`,
		map[string]any{"id": id, "bytes": bytes})
	return err
}

// IncrementRetry bumps the retry counter.
func (s *SurrealStore) IncrementRetry(id string) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	_, err := queryOne[interface{}](ctx(), db,
		`UPDATE type::thing('ca_llm_job', $id) SET
			retry_count = retry_count + 1,
			updated_at = time::now()
		  WHERE status = 'pending' OR status = 'generating'`,
		map[string]any{"id": id})
	return err
}

// IncrementAttachedRequests bumps the deduped request count.
func (s *SurrealStore) IncrementAttachedRequests(id string) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	job := s.GetByID(id)
	if job == nil {
		return fmt.Errorf("job %s not found", id)
	}
	next := job.AttachedRequests
	if next <= 0 {
		next = 1
	}
	next++
	_, err := queryOne[interface{}](ctx(), db, `
		UPDATE type::thing('ca_llm_job', $id)
		SET attached_requests = $attached_requests,
		    updated_at = time::now()
	`, map[string]any{
		"id":                id,
		"attached_requests": next,
	})
	return err
}
