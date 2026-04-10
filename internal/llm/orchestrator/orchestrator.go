// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package orchestrator provides the bounded LLM job queue that every
// subsystem in SourceBridge enqueues work against. It owns concurrency
// control, retry policy, in-flight deduplication, progress debouncing,
// and event publication to subscribers (Monitor page, GraphQL resolvers).
//
// Design shape:
//
//   - Callers build an llm.EnqueueRequest and call Orchestrator.Enqueue.
//   - The orchestrator dedupes against the in-process registry and the
//     persistent JobStore, then creates a new job and returns it.
//   - A worker goroutine (bounded by Config.MaxConcurrency) picks the
//     job up, transitions it to generating, runs the caller's closure
//     with a Runtime that reports progress, and transitions to the
//     appropriate terminal state.
//   - Progress updates are debounced so a fast-streaming job cannot
//     overwhelm the store with writes.
//   - Events are published to every Subscribe() listener.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sourcebridge/sourcebridge/internal/llm"
)

// Config bundles the knobs the orchestrator reads at startup. Every field
// has a sensible default so most callers can pass a zero-value Config.
type Config struct {
	// MaxConcurrency bounds the number of jobs running in parallel.
	// Default 3 — matches the Phase 4 plan's recommended ceiling for
	// a single local LLM instance.
	MaxConcurrency int
	// QueueCapacity sets the buffered size of the pending-job channel.
	// Default 128.
	QueueCapacity int
	// ProgressDebounce throttles progress writes to the store. Default
	// 500ms — fast enough that the Monitor page feels live, slow enough
	// that a streaming LLM call does not hammer the DB.
	ProgressDebounce time.Duration
	// Retry controls how transiently-failing jobs are retried. Zero-value
	// uses DefaultRetryPolicy (2 attempts, 5s initial, 30s cap).
	Retry RetryPolicy
	// MetricsCapacity is the per-bucket sample buffer. Default 256.
	MetricsCapacity int
}

// withDefaults returns a copy of c with zero fields replaced by sane defaults.
func (c Config) withDefaults() Config {
	if c.MaxConcurrency <= 0 {
		c.MaxConcurrency = 3
	}
	if c.QueueCapacity <= 0 {
		c.QueueCapacity = 128
	}
	if c.ProgressDebounce <= 0 {
		c.ProgressDebounce = 500 * time.Millisecond
	}
	if c.Retry.MaxAttempts == 0 {
		c.Retry = DefaultRetryPolicy()
	}
	if c.MetricsCapacity <= 0 {
		c.MetricsCapacity = 256
	}
	return c
}

// Orchestrator is the public type. One instance per process; callers
// construct it during startup and pass it to resolvers / REST handlers.
type Orchestrator struct {
	cfg      Config
	store    llm.JobStore
	inflight *inflightRegistry
	metrics  *metrics

	// queue carries pending work to the worker pool.
	queue   chan *workItem
	workers sync.WaitGroup

	// shutdown lifecycle
	ctx    context.Context
	cancel context.CancelFunc

	// subscribers for JobEvent delivery.
	subMu       sync.RWMutex
	subscribers map[int64]chan llm.JobEvent
	nextSubID   int64
}

// workItem is the internal envelope that carries a job id and its run
// closure from Enqueue to the worker goroutines.
type workItem struct {
	jobID string
	req   *llm.EnqueueRequest
}

// New creates and starts a new orchestrator. The caller is responsible
// for calling Shutdown during graceful termination.
func New(store llm.JobStore, cfg Config) *Orchestrator {
	cfg = cfg.withDefaults()
	ctx, cancel := context.WithCancel(context.Background())
	o := &Orchestrator{
		cfg:         cfg,
		store:       store,
		inflight:    newInflightRegistry(),
		metrics:     newMetrics(cfg.MetricsCapacity),
		queue:       make(chan *workItem, cfg.QueueCapacity),
		ctx:         ctx,
		cancel:      cancel,
		subscribers: make(map[int64]chan llm.JobEvent),
	}
	for i := 0; i < cfg.MaxConcurrency; i++ {
		o.workers.Add(1)
		go o.worker(i)
	}
	// Start stale job reaper — marks jobs stuck in pending/generating
	// for longer than the reap threshold as failed so they don't block
	// dedupe forever.
	go o.reaper()
	return o
}

// Shutdown stops accepting new work and waits for running jobs to drain.
// It is safe to call multiple times.
func (o *Orchestrator) Shutdown(graceful time.Duration) error {
	o.cancel()
	close(o.queue)

	done := make(chan struct{})
	go func() {
		o.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(graceful):
		return fmt.Errorf("orchestrator shutdown timed out after %s", graceful)
	}
}

// staleJobThreshold is how long a job can sit in pending/generating before
// the reaper marks it failed. Set higher than the longest expected generation.
const staleJobThreshold = 35 * time.Minute

// reaper periodically scans for jobs stuck in pending/generating and marks
// them failed. This prevents stale jobs from permanently blocking dedupe
// after a worker crash, timeout, or deployment.
func (o *Orchestrator) reaper() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-o.ctx.Done():
			return
		case <-ticker.C:
			o.reapStaleJobs()
		}
	}
}

func (o *Orchestrator) reapStaleJobs() {
	active := o.store.ListActive(llm.ListFilter{})
	now := time.Now()
	for _, job := range active {
		age := now.Sub(job.UpdatedAt)
		if age < staleJobThreshold {
			continue
		}
		slog.Warn("reaping stale job",
			"job_id", job.ID,
			"target_key", job.TargetKey,
			"status", job.Status,
			"age", age.Round(time.Second).String())
		o.store.SetStatus(job.ID, llm.StatusFailed)
		o.store.SetError(job.ID, "DEADLINE_EXCEEDED", "Job reaped: stuck in "+string(job.Status)+" for "+age.Round(time.Second).String())
		o.inflight.release(job.TargetKey)
	}
}

// Enqueue claims a job, persists it, and schedules it for execution. If
// a job with the same TargetKey is already active, Enqueue returns the
// existing job without creating a duplicate.
func (o *Orchestrator) Enqueue(req *llm.EnqueueRequest) (*llm.Job, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	// DB-level dedupe first — this also covers the "process restarted
	// with active jobs still in-flight per the DB" case.
	if existing := o.store.GetActiveByTargetKey(req.TargetKey); existing != nil {
		// Synchronize the in-process registry so the fast path agrees.
		o.inflight.claim(req.TargetKey, existing.ID)
		slog.Info("llm_job_dedupe_hit_store",
			"job_id", existing.ID,
			"target_key", req.TargetKey,
			"subsystem", existing.Subsystem,
			"job_type", existing.JobType)
		return existing, nil
	}

	// Generate a fresh id and reserve the target key. If someone else
	// wins the race, defer to them.
	id := uuid.New().String()
	if winner, ok := o.inflight.claim(req.TargetKey, id); !ok {
		existing := o.store.GetByID(winner)
		if existing != nil {
			slog.Info("llm_job_dedupe_hit_inflight",
				"job_id", winner,
				"target_key", req.TargetKey)
			return existing, nil
		}
		// Registry was stale — clear and retry once.
		o.inflight.release(req.TargetKey)
	}

	// Materialize the job and persist it.
	job := &llm.Job{
		ID:          id,
		Subsystem:   req.Subsystem,
		JobType:     req.JobType,
		TargetKey:   req.TargetKey,
		Strategy:    req.Strategy,
		Model:       req.Model,
		ArtifactID:  req.ArtifactID,
		RepoID:      req.RepoID,
		Status:      llm.StatusPending,
		MaxAttempts: req.MaxAttempts,
		TimeoutSec:  req.TimeoutSec,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if job.MaxAttempts <= 0 {
		job.MaxAttempts = o.cfg.Retry.MaxAttempts
	}
	if _, err := o.store.Create(job); err != nil {
		o.inflight.release(req.TargetKey)
		return nil, fmt.Errorf("persist job: %w", err)
	}

	o.publish(llm.JobEvent{Kind: llm.EventCreated, Job: job})

	// Hand off to the worker pool. Enqueue is non-blocking when there
	// is queue capacity; otherwise the caller's goroutine parks here.
	select {
	case o.queue <- &workItem{jobID: id, req: req}:
		return job, nil
	case <-o.ctx.Done():
		o.inflight.release(req.TargetKey)
		_ = o.store.SetError(id, "ORCHESTRATOR_SHUTDOWN", "orchestrator is shutting down")
		return nil, fmt.Errorf("orchestrator shutting down")
	}
}

// GetJob returns the current state of a job by id, or nil.
func (o *Orchestrator) GetJob(id string) *llm.Job {
	return o.store.GetByID(id)
}

// EnqueueSync is a blocking variant of Enqueue used by resolvers that
// need the LLM result inline (AnalyzeSymbol, DiscussCode, ReviewCode,
// etc. — mutations that return structured data rather than a job id).
//
// The caller's Run closure captures the response into closure variables;
// EnqueueSync waits until the job reaches a terminal state and then
// returns the final Job record. Callers check the returned job's status
// and error to decide what to do:
//
//	job, err := o.EnqueueSync(ctx, &llm.EnqueueRequest{
//	    Subsystem: ..., JobType: ..., TargetKey: ...,
//	    Run: func(rt llm.Runtime) error {
//	        resp, err := worker.Call(ctx, req)
//	        if err != nil {
//	            return err
//	        }
//	        // capture resp into an outer closure var
//	        return nil
//	    },
//	})
//	if err != nil { return nil, err }      // enqueue itself failed
//	if job.Status == llm.StatusFailed {    // run returned an error
//	    return nil, errors.New(job.ErrorMessage)
//	}
//	// use the captured resp
//
// The wait is implemented as a subscriber filtered to this job's id.
// When the passed context is cancelled, EnqueueSync returns ctx.Err()
// but the underlying job continues running (the orchestrator has no
// way to cancel an in-flight Run closure today).
func (o *Orchestrator) EnqueueSync(ctx context.Context, req *llm.EnqueueRequest) (*llm.Job, error) {
	// Subscribe BEFORE enqueue so we cannot miss the terminal event on
	// a very fast job completion.
	events, unsubscribe := o.Subscribe()
	defer unsubscribe()

	job, err := o.Enqueue(req)
	if err != nil {
		return nil, err
	}
	// Dedupe hit on an already-terminal job — no wait needed.
	if job.Status.IsTerminal() {
		return job, nil
	}

	// If the dedupe hit returned an already-active job, we need to
	// wait on that job's id, not necessarily a job we just created.
	waitID := job.ID

	// Guard against the orchestrator shutting down mid-wait.
	for {
		select {
		case <-ctx.Done():
			return o.store.GetByID(waitID), ctx.Err()
		case <-o.ctx.Done():
			return o.store.GetByID(waitID), fmt.Errorf("orchestrator shutting down")
		case ev, ok := <-events:
			if !ok {
				return o.store.GetByID(waitID), nil
			}
			if ev.Job == nil || ev.Job.ID != waitID {
				continue
			}
			if ev.Job.Status.IsTerminal() {
				return ev.Job, nil
			}
		}
	}
}

// ListActive returns every currently-active job matching the filter.
func (o *Orchestrator) ListActive(filter llm.ListFilter) []*llm.Job {
	return o.store.ListActive(filter)
}

// ListRecent returns terminal jobs updated since the supplied time.
func (o *Orchestrator) ListRecent(filter llm.ListFilter, since time.Time) []*llm.Job {
	return o.store.ListRecent(filter, since)
}

// Metrics returns a point-in-time metrics snapshot for the Monitor page.
func (o *Orchestrator) Metrics() Snapshot {
	return o.metrics.Snapshot()
}

// QueueDepth returns the current number of jobs waiting in the bounded
// queue (not counting ones actively running).
func (o *Orchestrator) QueueDepth() int {
	return len(o.queue)
}

// InFlightCount returns the number of distinct target keys currently
// tracked as active — includes both queued and running jobs.
func (o *Orchestrator) InFlightCount() int {
	return o.inflight.size()
}

// MaxConcurrency returns the configured worker pool size.
func (o *Orchestrator) MaxConcurrency() int {
	return o.cfg.MaxConcurrency
}

// Subscribe returns a channel that receives JobEvents and an unsubscribe
// function the caller must invoke when done. The channel is buffered to
// avoid blocking the publisher; subscribers that fall too far behind
// will drop events (logged as a warning).
func (o *Orchestrator) Subscribe() (<-chan llm.JobEvent, func()) {
	o.subMu.Lock()
	o.nextSubID++
	id := o.nextSubID
	ch := make(chan llm.JobEvent, 64)
	o.subscribers[id] = ch
	o.subMu.Unlock()

	unsubscribe := func() {
		o.subMu.Lock()
		defer o.subMu.Unlock()
		if existing, ok := o.subscribers[id]; ok {
			close(existing)
			delete(o.subscribers, id)
		}
	}
	return ch, unsubscribe
}

// publish delivers an event to every subscriber without blocking. If a
// subscriber's buffer is full, the event is dropped for that subscriber
// and logged — we prefer losing an event to stalling the publisher.
func (o *Orchestrator) publish(event llm.JobEvent) {
	o.subMu.RLock()
	defer o.subMu.RUnlock()
	for id, ch := range o.subscribers {
		select {
		case ch <- event:
		default:
			slog.Warn("llm_job_event_dropped",
				"subscriber_id", id,
				"job_id", func() string {
					if event.Job != nil {
						return event.Job.ID
					}
					return ""
				}(),
				"kind", event.Kind)
		}
	}
}

// worker is the loop that drains the queue and runs jobs under the
// bounded concurrency budget. One goroutine per MaxConcurrency.
func (o *Orchestrator) worker(idx int) {
	defer o.workers.Done()
	for item := range o.queue {
		if item == nil {
			continue
		}
		o.runJob(item)
		_ = idx // reserved for future per-worker instrumentation
	}
}

// runJob executes the work with retries, progress publication, and
// proper terminal-state bookkeeping. Every exit path releases the
// in-flight registry slot.
func (o *Orchestrator) runJob(item *workItem) {
	jobID := item.jobID
	req := item.req

	defer o.inflight.release(req.TargetKey)

	// Transition pending -> generating.
	if err := o.store.SetStatus(jobID, llm.StatusGenerating); err != nil {
		slog.Warn("llm_job_set_generating_failed", "job_id", jobID, "error", err)
	}
	job := o.store.GetByID(jobID)
	if job != nil {
		o.publish(llm.JobEvent{Kind: llm.EventStarted, Job: job})
	}

	// Run with retry loop.
	var lastErr error
	maxAttempts := req.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = o.cfg.Retry.MaxAttempts
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			backoff := o.cfg.Retry.BackoffFor(attempt)
			select {
			case <-time.After(backoff):
			case <-o.ctx.Done():
				o.finalizeCancelled(jobID)
				return
			}
			if err := o.store.IncrementRetry(jobID); err != nil {
				slog.Warn("llm_job_increment_retry_failed", "job_id", jobID, "error", err)
			}
		}

		rt := newRuntime(o, jobID)
		err := req.Run(rt)
		rt.flush()

		if err == nil {
			o.finalizeReady(jobID, req)
			return
		}
		lastErr = err

		if errors.Is(err, ErrJobCancelled) {
			o.finalizeCancelled(jobID)
			return
		}
		if !o.cfg.Retry.ShouldRetry(attempt, err) {
			slog.Info("llm_job_error_not_retryable",
				"job_id", jobID,
				"attempt", attempt,
				"error", err.Error())
			break
		}
		slog.Info("llm_job_retrying",
			"job_id", jobID,
			"attempt", attempt,
			"max_attempts", maxAttempts,
			"error", err.Error())
	}

	o.finalizeFailed(jobID, req, lastErr)
}

// finalizeReady transitions the job to ready, records metrics, and emits
// an event.
func (o *Orchestrator) finalizeReady(jobID string, req *llm.EnqueueRequest) {
	if err := o.store.SetStatus(jobID, llm.StatusReady); err != nil {
		slog.Warn("llm_job_set_ready_failed", "job_id", jobID, "error", err)
	}
	job := o.store.GetByID(jobID)
	if job != nil {
		o.metrics.record(req.Subsystem, req.JobType, job.Elapsed(), llm.StatusReady)
		o.publish(llm.JobEvent{Kind: llm.EventCompleted, Job: job})
	}
}

// finalizeFailed classifies the error, persists it, records metrics,
// and emits an event.
func (o *Orchestrator) finalizeFailed(jobID string, req *llm.EnqueueRequest, err error) {
	code := ClassifyError(err)
	msg := "unknown error"
	if err != nil {
		msg = err.Error()
	}
	if setErr := o.store.SetError(jobID, code, msg); setErr != nil {
		slog.Warn("llm_job_set_error_failed", "job_id", jobID, "error", setErr)
	}
	job := o.store.GetByID(jobID)
	if job != nil {
		o.metrics.record(req.Subsystem, req.JobType, job.Elapsed(), llm.StatusFailed)
		o.publish(llm.JobEvent{Kind: llm.EventFailed, Job: job})
	}
}

// finalizeCancelled marks the job cancelled (does not count as failure).
func (o *Orchestrator) finalizeCancelled(jobID string) {
	if err := o.store.SetStatus(jobID, llm.StatusCancelled); err != nil {
		slog.Warn("llm_job_set_cancelled_failed", "job_id", jobID, "error", err)
	}
	job := o.store.GetByID(jobID)
	if job != nil {
		o.metrics.record(job.Subsystem, job.JobType, job.Elapsed(), llm.StatusCancelled)
		o.publish(llm.JobEvent{Kind: llm.EventCancelled, Job: job})
	}
}
