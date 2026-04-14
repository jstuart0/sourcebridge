// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
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
	Priority         string           `json:"priority"`
	GenerationMode   string           `json:"generation_mode"`
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
	ReusedSummaries  int              `json:"reused_summaries"`
	LeafCacheHits    int              `json:"leaf_cache_hits"`
	FileCacheHits    int              `json:"file_cache_hits"`
	PackageCacheHits int              `json:"package_cache_hits"`
	RootCacheHits    int              `json:"root_cache_hits"`
	CachedNodesLoaded int             `json:"cached_nodes_loaded"`
	TotalNodes        int             `json:"total_nodes"`
	ResumeStage       string          `json:"resume_stage"`
	SkippedLeafUnits  int             `json:"skipped_leaf_units"`
	SkippedFileUnits  int             `json:"skipped_file_units"`
	SkippedPackageUnits int           `json:"skipped_package_units"`
	SkippedRootUnits  int             `json:"skipped_root_units"`
	ArtifactID       string           `json:"artifact_id"`
	RepoID           string           `json:"repo_id"`
	CreatedAt        surrealTime      `json:"created_at"`
	StartedAt        surrealTime      `json:"started_at"`
	UpdatedAt        surrealTime      `json:"updated_at"`
	CompletedAt      surrealTime      `json:"completed_at"`
}

type surrealLLMJobLog struct {
	ID          *models.RecordID `json:"id,omitempty"`
	JobID       string           `json:"job_id"`
	RepoID      string           `json:"repo_id"`
	ArtifactID  string           `json:"artifact_id"`
	Subsystem   string           `json:"subsystem"`
	JobType     string           `json:"job_type"`
	Level       string           `json:"level"`
	Phase       string           `json:"phase"`
	Event       string           `json:"event"`
	Message     string           `json:"message"`
	PayloadJSON string           `json:"payload_json"`
	Sequence    int64            `json:"sequence"`
	CreatedAt   surrealTime      `json:"created_at"`
}

func (r *surrealLLMJob) toJob() *llm.Job {
	job := &llm.Job{
		ID:               recordIDString(r.ID),
		Subsystem:        llm.Subsystem(r.Subsystem),
		JobType:          r.JobType,
		TargetKey:        r.TargetKey,
		Strategy:         r.Strategy,
		Model:            r.Model,
		Priority:         llm.JobPriority(r.Priority),
		GenerationMode:   r.GenerationMode,
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
		ReusedSummaries:  r.ReusedSummaries,
		LeafCacheHits:    r.LeafCacheHits,
		FileCacheHits:    r.FileCacheHits,
		PackageCacheHits: r.PackageCacheHits,
		RootCacheHits:    r.RootCacheHits,
		CachedNodesLoaded: r.CachedNodesLoaded,
		TotalNodes:        r.TotalNodes,
		ResumeStage:       r.ResumeStage,
		SkippedLeafUnits:  r.SkippedLeafUnits,
		SkippedFileUnits:  r.SkippedFileUnits,
		SkippedPackageUnits: r.SkippedPackageUnits,
		SkippedRootUnits:  r.SkippedRootUnits,
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

func (r *surrealLLMJobLog) toJobLog() *llm.JobLogEntry {
	if r == nil {
		return nil
	}
	return &llm.JobLogEntry{
		ID:          recordIDString(r.ID),
		JobID:       r.JobID,
		RepoID:      r.RepoID,
		ArtifactID:  r.ArtifactID,
		Subsystem:   llm.Subsystem(r.Subsystem),
		JobType:     r.JobType,
		Level:       llm.JobLogLevel(r.Level),
		Phase:       r.Phase,
		Event:       r.Event,
		Message:     r.Message,
		PayloadJSON: r.PayloadJSON,
		Sequence:    r.Sequence,
		CreatedAt:   r.CreatedAt.Time,
	}
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
		priority = $priority,
		generation_mode = $generation_mode,
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
		reused_summaries = $reused_summaries,
		leaf_cache_hits = $leaf_cache_hits,
		file_cache_hits = $file_cache_hits,
		package_cache_hits = $package_cache_hits,
		root_cache_hits = $root_cache_hits,
		cached_nodes_loaded = $cached_nodes_loaded,
		total_nodes = $total_nodes,
		resume_stage = $resume_stage,
		skipped_leaf_units = $skipped_leaf_units,
		skipped_file_units = $skipped_file_units,
		skipped_package_units = $skipped_package_units,
		skipped_root_units = $skipped_root_units,
		artifact_id = $artifact_id,
		repo_id = $repo_id,
		created_at = time::now(),
		updated_at = time::now()`

	vars := map[string]any{
		"id":                 job.ID,
		"subsystem":          string(job.Subsystem),
		"job_type":           job.JobType,
		"target_key":         job.TargetKey,
		"strategy":           job.Strategy,
		"model":              job.Model,
		"priority":           string(job.Priority),
		"generation_mode":    job.GenerationMode,
		"status":             string(status),
		"progress":           job.Progress,
		"progress_phase":     job.ProgressPhase,
		"progress_message":   job.ProgressMessage,
		"error_code":         job.ErrorCode,
		"error_message":      job.ErrorMessage,
		"retry_count":        job.RetryCount,
		"max_attempts":       job.MaxAttempts,
		"timeout_sec":        job.TimeoutSec,
		"attached_requests":  job.AttachedRequests,
		"input_tokens":       job.InputTokens,
		"output_tokens":      job.OutputTokens,
		"snapshot_bytes":     job.SnapshotBytes,
		"reused_summaries":   job.ReusedSummaries,
		"leaf_cache_hits":    job.LeafCacheHits,
		"file_cache_hits":    job.FileCacheHits,
		"package_cache_hits": job.PackageCacheHits,
		"root_cache_hits":    job.RootCacheHits,
		"cached_nodes_loaded": job.CachedNodesLoaded,
		"total_nodes":         job.TotalNodes,
		"resume_stage":        job.ResumeStage,
		"skipped_leaf_units":  job.SkippedLeafUnits,
		"skipped_file_units":  job.SkippedFileUnits,
		"skipped_package_units": job.SkippedPackageUnits,
		"skipped_root_units":  job.SkippedRootUnits,
		"artifact_id":        job.ArtifactID,
		"repo_id":            job.RepoID,
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
		priority = $priority,
		generation_mode = $generation_mode,
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
		reused_summaries = $reused_summaries,
		leaf_cache_hits = $leaf_cache_hits,
		file_cache_hits = $file_cache_hits,
		package_cache_hits = $package_cache_hits,
		root_cache_hits = $root_cache_hits,
		cached_nodes_loaded = $cached_nodes_loaded,
		total_nodes = $total_nodes,
		resume_stage = $resume_stage,
		skipped_leaf_units = $skipped_leaf_units,
		skipped_file_units = $skipped_file_units,
		skipped_package_units = $skipped_package_units,
		skipped_root_units = $skipped_root_units,
		artifact_id = $artifact_id,
		repo_id = $repo_id,
		updated_at = time::now()`
	vars := map[string]any{
		"id":                 job.ID,
		"subsystem":          string(job.Subsystem),
		"job_type":           job.JobType,
		"target_key":         job.TargetKey,
		"strategy":           job.Strategy,
		"model":              job.Model,
		"priority":           string(job.Priority),
		"generation_mode":    job.GenerationMode,
		"status":             string(job.Status),
		"progress":           job.Progress,
		"progress_phase":     job.ProgressPhase,
		"progress_message":   job.ProgressMessage,
		"error_code":         job.ErrorCode,
		"error_message":      job.ErrorMessage,
		"retry_count":        job.RetryCount,
		"max_attempts":       job.MaxAttempts,
		"timeout_sec":        job.TimeoutSec,
		"attached_requests":  job.AttachedRequests,
		"input_tokens":       job.InputTokens,
		"output_tokens":      job.OutputTokens,
		"snapshot_bytes":     job.SnapshotBytes,
		"reused_summaries":   job.ReusedSummaries,
		"leaf_cache_hits":    job.LeafCacheHits,
		"file_cache_hits":    job.FileCacheHits,
		"package_cache_hits": job.PackageCacheHits,
		"root_cache_hits":    job.RootCacheHits,
		"cached_nodes_loaded": job.CachedNodesLoaded,
		"total_nodes":         job.TotalNodes,
		"resume_stage":        job.ResumeStage,
		"skipped_leaf_units":  job.SkippedLeafUnits,
		"skipped_file_units":  job.SkippedFileUnits,
		"skipped_package_units": job.SkippedPackageUnits,
		"skipped_root_units":  job.SkippedRootUnits,
		"artifact_id":        job.ArtifactID,
		"repo_id":            job.RepoID,
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

// SetReuseStats records structured summary reuse/cache-hit counts.
func (s *SurrealStore) SetReuseStats(id string, reused, leafHits, fileHits, packageHits, rootHits int) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	_, err := queryOne[interface{}](ctx(), db,
		`UPDATE type::thing('ca_llm_job', $id) SET
			reused_summaries = $reused,
			leaf_cache_hits = $leaf_hits,
			file_cache_hits = $file_hits,
			package_cache_hits = $package_hits,
			root_cache_hits = $root_hits,
			updated_at = time::now()
		  WHERE status = 'pending' OR status = 'generating' OR status = 'ready'`,
		map[string]any{
			"id":           id,
			"reused":       reused,
			"leaf_hits":    leafHits,
			"file_hits":    fileHits,
			"package_hits": packageHits,
			"root_hits":    rootHits,
		})
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

// AppendLog persists a structured job log record.
func (s *SurrealStore) AppendLog(entry *llm.JobLogEntry) (*llm.JobLogEntry, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	if entry == nil || strings.TrimSpace(entry.JobID) == "" {
		return nil, fmt.Errorf("job log job_id is required")
	}
	id := entry.ID
	if id == "" {
		id = uuid.NewString()
	}
	sql := `CREATE ca_llm_job_log SET
		id = type::thing('ca_llm_job_log', $id),
		job_id = $job_id,
		repo_id = $repo_id,
		artifact_id = $artifact_id,
		subsystem = $subsystem,
		job_type = $job_type,
		level = $level,
		phase = $phase,
		event = $event,
		message = $message,
		payload_json = $payload_json,
		sequence = $sequence,
		created_at = time::now()`
	vars := map[string]any{
		"id":           id,
		"job_id":       entry.JobID,
		"repo_id":      entry.RepoID,
		"artifact_id":  entry.ArtifactID,
		"subsystem":    string(entry.Subsystem),
		"job_type":     entry.JobType,
		"level":        string(entry.Level),
		"phase":        entry.Phase,
		"event":        entry.Event,
		"message":      entry.Message,
		"payload_json": entry.PayloadJSON,
		"sequence":     entry.Sequence,
	}
	if _, err := surrealdb.Query[interface{}](ctx(), db, sql, vars); err != nil {
		return nil, fmt.Errorf("create llm job log: %w", err)
	}
	rows, err := queryOne[[]surrealLLMJobLog](ctx(), db,
		`SELECT * FROM type::thing('ca_llm_job_log', $id)`,
		map[string]any{"id": id})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0].toJobLog(), nil
}

// ListLogs returns persisted logs for a job ordered by sequence ascending.
func (s *SurrealStore) ListLogs(jobID string, filter llm.JobLogFilter) []*llm.JobLogEntry {
	db := s.client.DB()
	if db == nil || strings.TrimSpace(jobID) == "" {
		return nil
	}
	if filter.Limit <= 0 {
		filter.Limit = 200
	}
	sql := `SELECT * FROM ca_llm_job_log WHERE job_id = $job_id`
	vars := map[string]any{
		"job_id": jobID,
		"limit":  filter.Limit,
	}
	if filter.AfterSequence > 0 {
		sql += ` AND sequence > $after_sequence`
		vars["after_sequence"] = filter.AfterSequence
	}
	sql += ` ORDER BY sequence ASC LIMIT $limit`
	rows, err := queryOne[[]surrealLLMJobLog](ctx(), db, sql, vars)
	if err != nil || len(rows) == 0 {
		return nil
	}
	out := make([]*llm.JobLogEntry, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].toJobLog())
	}
	return out
}
