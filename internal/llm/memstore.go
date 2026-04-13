// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package llm

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemStore is an in-memory JobStore, used by tests and by the OSS runtime
// when no SurrealDB connection is configured. It is safe for concurrent use.
type MemStore struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

// Verify at compile time that *MemStore satisfies JobStore.
var _ JobStore = (*MemStore)(nil)

// NewMemStore creates a fresh in-memory job store.
func NewMemStore() *MemStore {
	return &MemStore{
		jobs: make(map[string]*Job),
	}
}

// Create inserts a copy of the supplied job. The caller's pointer is not
// retained — callers that want to mutate a persisted job should call
// GetByID to fetch a fresh copy.
func (s *MemStore) Create(job *Job) (*Job, error) {
	if job == nil {
		return nil, fmt.Errorf("job is nil")
	}
	if job.ID == "" {
		return nil, fmt.Errorf("job id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.ID]; exists {
		return nil, fmt.Errorf("job %s already exists", job.ID)
	}
	now := time.Now()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	if job.UpdatedAt.IsZero() {
		job.UpdatedAt = now
	}
	if job.Status == "" {
		job.Status = StatusPending
	}
	stored := cloneJob(job)
	s.jobs[job.ID] = stored
	return cloneJob(stored), nil
}

// Update replaces the stored record with a copy of the supplied job.
func (s *MemStore) Update(job *Job) error {
	if job == nil || job.ID == "" {
		return fmt.Errorf("job id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.ID]; !exists {
		return fmt.Errorf("job %s not found", job.ID)
	}
	job.UpdatedAt = time.Now()
	s.jobs[job.ID] = cloneJob(job)
	return nil
}

// GetByID returns a copy of the stored job, or nil.
func (s *MemStore) GetByID(id string) *Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	if !ok {
		return nil
	}
	return cloneJob(j)
}

// GetActiveByTargetKey returns the most recently updated active job for
// the supplied target key. The orchestrator uses this as its DB-level
// dedupe path — complementing the in-process registry so that a restart
// does not lose dedupe state.
func (s *MemStore) GetActiveByTargetKey(targetKey string) *Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best *Job
	for _, j := range s.jobs {
		if j.TargetKey != targetKey {
			continue
		}
		if !j.Status.IsActive() {
			continue
		}
		if best == nil || j.UpdatedAt.After(best.UpdatedAt) {
			best = j
		}
	}
	if best == nil {
		return nil
	}
	return cloneJob(best)
}

// ListActive returns every active (pending or generating) job matching
// the filter, newest-first.
func (s *MemStore) ListActive(filter ListFilter) []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	statuses := filter.Statuses
	if len(statuses) == 0 {
		statuses = []JobStatus{StatusPending, StatusGenerating}
	}
	return s.listLocked(filter, statuses, time.Time{})
}

// ListRecent returns terminal jobs whose updated_at is >= since.
func (s *MemStore) ListRecent(filter ListFilter, since time.Time) []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	statuses := filter.Statuses
	if len(statuses) == 0 {
		statuses = []JobStatus{StatusReady, StatusFailed, StatusCancelled}
	}
	return s.listLocked(filter, statuses, since)
}

func (s *MemStore) listLocked(filter ListFilter, statuses []JobStatus, since time.Time) []*Job {
	results := make([]*Job, 0)
	statusSet := make(map[JobStatus]struct{}, len(statuses))
	for _, st := range statuses {
		statusSet[st] = struct{}{}
	}
	for _, j := range s.jobs {
		if _, ok := statusSet[j.Status]; !ok {
			continue
		}
		if filter.Subsystem != "" && j.Subsystem != filter.Subsystem {
			continue
		}
		if filter.JobType != "" && j.JobType != filter.JobType {
			continue
		}
		if filter.RepoID != "" && j.RepoID != filter.RepoID {
			continue
		}
		if filter.ArtifactID != "" && j.ArtifactID != filter.ArtifactID {
			continue
		}
		if filter.TargetKey != "" && j.TargetKey != filter.TargetKey {
			continue
		}
		if !since.IsZero() && j.UpdatedAt.Before(since) {
			continue
		}
		results = append(results, cloneJob(j))
	}
	sort.Slice(results, func(i, k int) bool {
		return results[i].UpdatedAt.After(results[k].UpdatedAt)
	})
	if filter.Limit > 0 && len(results) > filter.Limit {
		results = results[:filter.Limit]
	}
	return results
}

// SetStatus transitions a job to a new status. It stamps StartedAt when
// moving to generating and CompletedAt when moving to a terminal state.
func (s *MemStore) SetStatus(id string, status JobStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("job %s not found", id)
	}
	now := time.Now()
	j.Status = status
	j.UpdatedAt = now
	if status == StatusGenerating && (j.StartedAt == nil || j.StartedAt.IsZero()) {
		started := now
		j.StartedAt = &started
	}
	if status.IsTerminal() {
		completed := now
		j.CompletedAt = &completed
		if status == StatusReady {
			j.Progress = 1.0
			j.ErrorCode = ""
			j.ErrorMessage = ""
		}
	}
	return nil
}

// SetProgress updates the progress fields without changing status.
func (s *MemStore) SetProgress(id string, progress float64, phase, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("job %s not found", id)
	}
	if j.Status.IsTerminal() {
		return nil
	}
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	j.Progress = progress
	j.ProgressPhase = phase
	j.ProgressMessage = message
	j.UpdatedAt = time.Now()
	return nil
}

// SetError marks the job failed with a classified error.
func (s *MemStore) SetError(id string, code, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("job %s not found", id)
	}
	now := time.Now()
	j.Status = StatusFailed
	j.ErrorCode = code
	j.ErrorMessage = message
	j.UpdatedAt = now
	completed := now
	j.CompletedAt = &completed
	return nil
}

// SetTokens records the final token usage.
func (s *MemStore) SetTokens(id string, input, output int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("job %s not found", id)
	}
	if j.Status.IsTerminal() {
		return nil
	}
	j.InputTokens = input
	j.OutputTokens = output
	j.UpdatedAt = time.Now()
	return nil
}

// SetSnapshotBytes records the serialized input size.
func (s *MemStore) SetSnapshotBytes(id string, bytes int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("job %s not found", id)
	}
	if j.Status.IsTerminal() {
		return nil
	}
	j.SnapshotBytes = bytes
	j.UpdatedAt = time.Now()
	return nil
}

// SetReuseStats records structured summary reuse/cache-hit counts.
func (s *MemStore) SetReuseStats(id string, reused, leafHits, fileHits, packageHits, rootHits int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("job %s not found", id)
	}
	j.ReusedSummaries = reused
	j.LeafCacheHits = leafHits
	j.FileCacheHits = fileHits
	j.PackageCacheHits = packageHits
	j.RootCacheHits = rootHits
	j.UpdatedAt = time.Now()
	return nil
}

// IncrementAttachedRequests bumps the deduped request count.
func (s *MemStore) IncrementAttachedRequests(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("job %s not found", id)
	}
	if j.AttachedRequests <= 0 {
		j.AttachedRequests = 1
	}
	j.AttachedRequests++
	j.UpdatedAt = time.Now()
	return nil
}

// IncrementRetry bumps the retry counter.
func (s *MemStore) IncrementRetry(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("job %s not found", id)
	}
	if j.Status.IsTerminal() {
		return nil
	}
	j.RetryCount++
	j.UpdatedAt = time.Now()
	return nil
}

// cloneJob returns a deep-enough copy of the job record. Pointer fields
// (timestamps) are independently allocated so callers cannot mutate the
// stored record via returned references.
func cloneJob(j *Job) *Job {
	if j == nil {
		return nil
	}
	out := *j
	if j.StartedAt != nil {
		t := *j.StartedAt
		out.StartedAt = &t
	}
	if j.CompletedAt != nil {
		t := *j.CompletedAt
		out.CompletedAt = &t
	}
	return &out
}
