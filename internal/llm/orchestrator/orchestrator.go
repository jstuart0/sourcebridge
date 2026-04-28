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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
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
	// ComputeErrorThreshold opens the subsystem breaker after this many
	// consecutive provider compute failures. Default 3.
	ComputeErrorThreshold int
	// ComputeCooldown is how long the breaker stays open once tripped.
	// Default 20s.
	ComputeCooldown time.Duration
	// OnStaleJob is called when the reaper marks a stuck job as failed.
	// The orchestrator passes the job so the caller can clean up related
	// state (e.g. mark the linked knowledge artifact as failed too).
	// Nil means no callback.
	OnStaleJob func(job *llm.Job)
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
	if c.ComputeErrorThreshold <= 0 {
		c.ComputeErrorThreshold = 3
	}
	if c.ComputeCooldown <= 0 {
		c.ComputeCooldown = 20 * time.Second
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
	breaker  *subsystemBreaker

	// Separate lanes keep interactive work ahead of maintenance/prewarm.
	interactiveQ chan *workItem
	maintenanceQ chan *workItem
	prewarmQ     chan *workItem
	workers      sync.WaitGroup

	runMu        sync.Mutex
	runCancels   map[string]context.CancelFunc
	cancelled    map[string]struct{}
	intakeMu     sync.RWMutex
	intakePaused bool

	// shutdown lifecycle
	ctx    context.Context
	cancel context.CancelFunc

	// subscribers for JobEvent delivery.
	subMu       sync.RWMutex
	subscribers map[int64]chan llm.JobEvent
	nextSubID   int64

	logMu           sync.RWMutex
	logSubscribers  map[int64]chan llm.JobLogEntry
	nextLogSubID    int64
	lastLogSequence map[string]int64

	workerMu          sync.Mutex
	nextWorkerID      int
	configuredWorkers int
	activeWorkers     int
	retiringWorkers   int
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
		cfg:               cfg,
		store:             store,
		inflight:          newInflightRegistry(),
		metrics:           newMetrics(cfg.MetricsCapacity),
		breaker:           newSubsystemBreaker(cfg.ComputeErrorThreshold, cfg.ComputeCooldown),
		interactiveQ:      make(chan *workItem, cfg.QueueCapacity),
		maintenanceQ:      make(chan *workItem, cfg.QueueCapacity),
		prewarmQ:          make(chan *workItem, cfg.QueueCapacity),
		ctx:               ctx,
		cancel:            cancel,
		subscribers:       make(map[int64]chan llm.JobEvent),
		logSubscribers:    make(map[int64]chan llm.JobLogEntry),
		lastLogSequence:   make(map[string]int64),
		runCancels:        make(map[string]context.CancelFunc),
		cancelled:         make(map[string]struct{}),
		configuredWorkers: cfg.MaxConcurrency,
	}
	o.workerMu.Lock()
	for i := 0; i < cfg.MaxConcurrency; i++ {
		o.startWorkerLocked()
	}
	o.workerMu.Unlock()
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

// staleJobThreshold is how long an active job can go without any store update
// before the reaper marks it failed. Active jobs are expected to heartbeat
// through progress writes while they run. Pending jobs are only reaped after a
// much longer delay to avoid interfering with legitimate queue backlogs.
//
// Living-wiki cold-start runs use the longer threshold because a single LLM
// call inside one page (especially architecture pages with full source bodies
// embedded in the prompt) can legitimately take several minutes, during which
// no per-page progress update fires. 10 min was killing real jobs mid-flight.
const (
	staleGeneratingThreshold          = 10 * time.Minute
	staleGeneratingThresholdLivingWiki = 30 * time.Minute
	stalePendingThreshold             = 45 * time.Minute
)

// generatingThresholdFor returns the per-job stall threshold based on the
// job's subsystem. Living-wiki cold-start runs are explicitly long-running.
func generatingThresholdFor(job *llm.Job) time.Duration {
	if job.Subsystem == "living_wiki" {
		return staleGeneratingThresholdLivingWiki
	}
	return staleGeneratingThreshold
}

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

// isTerminalJob reports whether the job has finalized in any state
// that makes it safe for the inflight registry to release. Active
// pending / generating jobs return false.
func isTerminalJob(job *llm.Job) bool {
	switch job.Status {
	case llm.StatusReady, llm.StatusFailed, llm.StatusCancelled:
		return true
	default:
		return false
	}
}

func (o *Orchestrator) reapStaleJobs() {
	active := o.store.ListActive(llm.ListFilter{})
	now := time.Now()
	for _, job := range active {
		age := now.Sub(job.UpdatedAt)
		threshold := generatingThresholdFor(job)
		if job.Status == llm.StatusPending {
			threshold = stalePendingThreshold
		}
		// Jobs waiting for a knowledge generation slot show phase
		// "queued" or "backoff". They are alive and heartbeating but
		// blocked behind other jobs. Use the longer pending threshold
		// so they aren't reaped while legitimately waiting in line.
		if job.ProgressPhase == "queued" || job.ProgressPhase == "backoff" {
			threshold = stalePendingThreshold
		}
		if age < threshold {
			continue
		}
		slog.Warn("reaping stale job",
			"job_id", job.ID,
			"target_key", job.TargetKey,
			"status", job.Status,
			"age", age.Round(time.Second).String(),
			"threshold", threshold.String())
		if err := o.store.SetStatus(job.ID, llm.StatusFailed); err != nil {
			slog.Warn("failed to mark stale job failed", "job_id", job.ID, "error", err)
		}
		if err := o.store.SetError(
			job.ID,
			"DEADLINE_EXCEEDED",
			"Job reaped: stuck in "+string(job.Status)+" for "+age.Round(time.Second).String(),
		); err != nil {
			slog.Warn("failed to persist stale job error", "job_id", job.ID, "error", err)
		}
		o.inflight.release(job.TargetKey)
		if o.cfg.OnStaleJob != nil {
			o.cfg.OnStaleJob(job)
		}
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
		_ = o.store.IncrementAttachedRequests(existing.ID)
		existing = o.store.GetByID(existing.ID)
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
	// wins the race, defer to them — unless their claimed job has
	// already terminally ended (failed / ready / cancelled). In that
	// case the registry is stale: release and retry so we don't hand
	// the caller a dead job reference. Without this, an API pod that
	// failed a job in memory (e.g. during a worker-pod startup race)
	// will dedupe every future request with the same target key to
	// that same dead job, until the pod restarts.
	id := uuid.New().String()
	const maxClaimAttempts = 2
	for attempt := 0; attempt < maxClaimAttempts; attempt++ {
		winner, ok := o.inflight.claim(req.TargetKey, id)
		if ok {
			break
		}
		existing := o.store.GetByID(winner)
		if existing != nil && !isTerminalJob(existing) {
			_ = o.store.IncrementAttachedRequests(winner)
			existing = o.store.GetByID(winner)
			slog.Info("llm_job_dedupe_hit_inflight",
				"job_id", winner,
				"target_key", req.TargetKey)
			return existing, nil
		}
		existingStatus := "missing"
		if existing != nil {
			existingStatus = string(existing.Status)
		}
		slog.Info("llm_inflight_stale_release",
			"stale_job_id", winner,
			"target_key", req.TargetKey,
			"existing_status", existingStatus,
			"attempt", attempt)
		o.inflight.release(req.TargetKey)
		// Loop re-claims with the same id on next iteration.
	}
	if o.IntakePaused() {
		o.inflight.release(req.TargetKey)
		return nil, fmt.Errorf("llm queue intake is paused")
	}

	// Materialize the job and persist it.
	job := &llm.Job{
		ID:               id,
		Subsystem:        req.Subsystem,
		JobType:          req.JobType,
		TargetKey:        req.TargetKey,
		Strategy:         req.Strategy,
		Model:            req.Model,
		ArtifactID:       req.ArtifactID,
		RepoID:           req.RepoID,
		Priority:         req.Priority,
		GenerationMode:   req.GenerationMode,
		Status:           llm.StatusPending,
		MaxAttempts:      req.MaxAttempts,
		TimeoutSec:       req.TimeoutSec,
		AttachedRequests: 1,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	if job.MaxAttempts <= 0 {
		job.MaxAttempts = o.cfg.Retry.MaxAttempts
	}
	if _, err := o.store.Create(job); err != nil {
		o.inflight.release(req.TargetKey)
		return nil, fmt.Errorf("persist job: %w", err)
	}

	o.publish(llm.JobEvent{Kind: llm.EventCreated, Job: job})
	_ = o.AppendJobLog(job.ID, llm.LogLevelInfo, "queued", "job_created", "Job created and enqueued", map[string]any{
		"target_key":  job.TargetKey,
		"subsystem":   string(job.Subsystem),
		"job_type":    job.JobType,
		"artifact_id": job.ArtifactID,
	})

	// Hand off to the worker pool. Enqueue is non-blocking when there
	// is queue capacity; otherwise the caller's goroutine parks here.
	select {
	case o.queueFor(req.Priority) <- &workItem{jobID: id, req: req}:
		return job, nil
	case <-o.ctx.Done():
		o.inflight.release(req.TargetKey)
		_ = o.store.SetError(id, "ORCHESTRATOR_SHUTDOWN", "orchestrator is shutting down")
		return nil, fmt.Errorf("orchestrator shutting down")
	}
}

func (o *Orchestrator) queueFor(priority llm.JobPriority) chan *workItem {
	switch priority {
	case llm.PriorityMaintenance:
		return o.maintenanceQ
	case llm.PriorityPrewarm:
		return o.prewarmQ
	default:
		return o.interactiveQ
	}
}

// SetIntakePaused toggles whether the orchestrator accepts new work. It does
// not affect already-active jobs.
func (o *Orchestrator) SetIntakePaused(paused bool) {
	o.intakeMu.Lock()
	defer o.intakeMu.Unlock()
	o.intakePaused = paused
}

// IntakePaused reports whether new enqueue requests are currently blocked.
func (o *Orchestrator) IntakePaused() bool {
	o.intakeMu.RLock()
	defer o.intakeMu.RUnlock()
	return o.intakePaused
}

// DrainPending cancels every currently-pending job while allowing running jobs
// to finish normally. It returns the number of jobs it transitioned.
func (o *Orchestrator) DrainPending() (int, error) {
	pending := o.PendingSnapshot(llm.ListFilter{})
	cancelled := 0
	var firstErr error
	for _, job := range pending {
		if err := o.Cancel(job.ID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		cancelled++
	}
	return cancelled, firstErr
}

// GetJob returns the current state of a job by id, or nil.
func (o *Orchestrator) GetJob(id string) *llm.Job {
	return o.store.GetByID(id)
}

// SetReuseStats records structured summary reuse/cache-hit counts on a job.
func (o *Orchestrator) SetReuseStats(id string, reused, leafHits, fileHits, packageHits, rootHits int) error {
	return o.store.SetReuseStats(id, reused, leafHits, fileHits, packageHits, rootHits)
}

// AppendJobLog persists and publishes a structured job-scoped log entry.
func (o *Orchestrator) AppendJobLog(jobID string, level llm.JobLogLevel, phase, event, message string, payload map[string]any) error {
	if strings.TrimSpace(jobID) == "" {
		return fmt.Errorf("job id is required")
	}
	job := o.store.GetByID(jobID)
	if job == nil {
		return fmt.Errorf("job %s not found", jobID)
	}
	sequence := time.Now().UnixNano()
	o.logMu.Lock()
	if last := o.lastLogSequence[jobID]; sequence <= last {
		sequence = last + 1
	}
	o.lastLogSequence[jobID] = sequence
	o.logMu.Unlock()

	payloadJSON := ""
	if len(payload) > 0 {
		if encoded, err := json.Marshal(payload); err == nil {
			payloadJSON = string(encoded)
		}
	}
	entry, err := o.store.AppendLog(&llm.JobLogEntry{
		JobID:       job.ID,
		RepoID:      job.RepoID,
		ArtifactID:  job.ArtifactID,
		Subsystem:   job.Subsystem,
		JobType:     job.JobType,
		Level:       level,
		Phase:       phase,
		Event:       event,
		Message:     message,
		PayloadJSON: payloadJSON,
		Sequence:    sequence,
	})
	if err != nil {
		return err
	}
	o.publishLog(*entry)
	return nil
}

// ListJobLogs returns persisted log lines for a job.
func (o *Orchestrator) ListJobLogs(jobID string, filter llm.JobLogFilter) []*llm.JobLogEntry {
	return o.store.ListLogs(jobID, filter)
}

// Cancel requests cancellation of an active job. Pending jobs are cancelled
// immediately; generating jobs are cancelled through their run context.
func (o *Orchestrator) Cancel(jobID string) error {
	job := o.store.GetByID(jobID)
	if job == nil {
		return fmt.Errorf("job not found")
	}
	if job.Status.IsTerminal() {
		return fmt.Errorf("job is already %s", job.Status)
	}

	o.runMu.Lock()
	o.cancelled[jobID] = struct{}{}
	cancelFn := o.runCancels[jobID]
	o.runMu.Unlock()

	if cancelFn != nil {
		cancelFn()
		return nil
	}

	o.inflight.release(job.TargetKey)
	o.finalizeCancelled(jobID)
	return nil
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
	return len(o.interactiveQ) + len(o.maintenanceQ) + len(o.prewarmQ)
}

// InFlightCount returns the number of distinct target keys currently
// tracked as active — includes both queued and running jobs.
func (o *Orchestrator) InFlightCount() int {
	return o.inflight.size()
}

// MaxConcurrency returns the configured worker pool size.
func (o *Orchestrator) MaxConcurrency() int {
	o.workerMu.Lock()
	defer o.workerMu.Unlock()
	return o.configuredWorkers
}

// ActiveWorkerCount returns the number of currently running worker goroutines.
func (o *Orchestrator) ActiveWorkerCount() int {
	o.workerMu.Lock()
	defer o.workerMu.Unlock()
	return o.activeWorkers
}

// ReconfigureMaxConcurrency updates the desired worker-pool size without
// cancelling in-flight jobs. New workers start immediately on scale-up;
// scale-down retires workers after they finish their current job.
func (o *Orchestrator) ReconfigureMaxConcurrency(max int) (oldConfigured int, newConfigured int) {
	if max <= 0 {
		max = 1
	}

	o.workerMu.Lock()
	defer o.workerMu.Unlock()

	oldConfigured = o.configuredWorkers
	if oldConfigured <= 0 {
		oldConfigured = o.cfg.MaxConcurrency
	}
	newConfigured = max
	if oldConfigured == newConfigured {
		return oldConfigured, newConfigured
	}

	o.configuredWorkers = newConfigured
	o.cfg.MaxConcurrency = newConfigured
	if newConfigured > oldConfigured {
		for i := 0; i < newConfigured-oldConfigured; i++ {
			o.startWorkerLocked()
		}
		return oldConfigured, newConfigured
	}

	o.retiringWorkers += oldConfigured - newConfigured
	return oldConfigured, newConfigured
}

// PendingSnapshot returns pending jobs ordered by queue order, oldest first.
func (o *Orchestrator) PendingSnapshot(filter llm.ListFilter) []*llm.Job {
	filter.Statuses = []llm.JobStatus{llm.StatusPending}
	pending := o.store.ListActive(filter)
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].Priority != pending[j].Priority {
			return jobPriorityRank(pending[i].Priority) < jobPriorityRank(pending[j].Priority)
		}
		return pending[i].CreatedAt.Before(pending[j].CreatedAt)
	})
	return pending
}

func jobPriorityRank(priority llm.JobPriority) int {
	switch priority {
	case llm.PriorityInteractive:
		return 0
	case llm.PriorityMaintenance:
		return 1
	case llm.PriorityPrewarm:
		return 2
	default:
		return 3
	}
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

// SubscribeLogs returns a channel that receives structured job log entries.
func (o *Orchestrator) SubscribeLogs() (<-chan llm.JobLogEntry, func()) {
	o.logMu.Lock()
	o.nextLogSubID++
	id := o.nextLogSubID
	ch := make(chan llm.JobLogEntry, 128)
	o.logSubscribers[id] = ch
	o.logMu.Unlock()

	unsubscribe := func() {
		o.logMu.Lock()
		defer o.logMu.Unlock()
		if existing, ok := o.logSubscribers[id]; ok {
			close(existing)
			delete(o.logSubscribers, id)
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

func (o *Orchestrator) publishLog(entry llm.JobLogEntry) {
	o.logMu.RLock()
	defer o.logMu.RUnlock()
	for id, ch := range o.logSubscribers {
		select {
		case ch <- entry:
		default:
			slog.Warn("llm_job_log_event_dropped",
				"subscriber_id", id,
				"job_id", entry.JobID,
				"event", entry.Event)
		}
	}
}

// worker is the loop that drains the queue and runs jobs under the
// bounded concurrency budget. One goroutine per MaxConcurrency.
func (o *Orchestrator) worker(idx int) {
	defer func() {
		o.workerMu.Lock()
		if o.activeWorkers > 0 {
			o.activeWorkers--
		}
		o.workerMu.Unlock()
		o.workers.Done()
	}()
	for {
		if o.shouldWorkerRetire() {
			return
		}
		item, ok := o.dequeue()
		if !ok {
			return
		}
		o.runJob(item)
		_ = idx // reserved for future per-worker instrumentation
	}
}

func (o *Orchestrator) startWorkerLocked() {
	idx := o.nextWorkerID
	o.nextWorkerID++
	o.activeWorkers++
	o.workers.Add(1)
	go o.worker(idx)
}

func (o *Orchestrator) shouldWorkerRetire() bool {
	o.workerMu.Lock()
	defer o.workerMu.Unlock()
	if o.retiringWorkers <= 0 {
		return false
	}
	if o.activeWorkers <= o.configuredWorkers {
		return false
	}
	o.retiringWorkers--
	return true
}

func (o *Orchestrator) dequeue() (*workItem, bool) {
	for {
		select {
		case <-o.ctx.Done():
			return nil, false
		default:
		}

		select {
		case item := <-o.interactiveQ:
			return item, true
		default:
		}
		select {
		case item := <-o.maintenanceQ:
			return item, true
		default:
		}
		select {
		case item := <-o.prewarmQ:
			return item, true
		default:
		}

		select {
		case item := <-o.interactiveQ:
			return item, true
		case item := <-o.maintenanceQ:
			return item, true
		case item := <-o.prewarmQ:
			return item, true
		case <-o.ctx.Done():
			return nil, false
		}
	}
}

// runJob executes the work with retries, progress publication, and
// proper terminal-state bookkeeping. Every exit path releases the
// in-flight registry slot.
func (o *Orchestrator) runJob(item *workItem) {
	jobID := item.jobID
	req := item.req

	defer o.inflight.release(req.TargetKey)
	defer o.clearRunState(jobID)

	if o.isCancelled(jobID) {
		if job := o.store.GetByID(jobID); job != nil && !job.Status.IsTerminal() {
			o.finalizeCancelled(jobID)
		}
		return
	}

	// Transition pending -> generating.
	if err := o.store.SetStatus(jobID, llm.StatusGenerating); err != nil {
		slog.Warn("llm_job_set_generating_failed", "job_id", jobID, "error", err)
	}
	job := o.store.GetByID(jobID)
	if job != nil {
		o.publish(llm.JobEvent{Kind: llm.EventStarted, Job: job})
	}
	_ = o.AppendJobLog(jobID, llm.LogLevelInfo, "generating", "job_started", "Worker claimed the job", nil)

	// Run with retry loop.
	var lastErr error
	maxAttempts := req.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = o.cfg.Retry.MaxAttempts
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if cooldown := o.breaker.waitDuration(req.Subsystem); cooldown > 0 {
			_ = o.store.SetProgress(jobID, 0.02, "backoff", "Waiting for model backend to recover")
			if job := o.store.GetByID(jobID); job != nil {
				o.publish(llm.JobEvent{Kind: llm.EventProgress, Job: job})
			}
			_ = o.AppendJobLog(jobID, llm.LogLevelWarn, "backoff", "breaker_backoff", "Waiting for model backend to recover", map[string]any{
				"cooldown_ms": cooldown.Milliseconds(),
			})
			select {
			case <-time.After(cooldown):
			case <-o.ctx.Done():
				o.finalizeCancelled(jobID)
				return
			}
		}
		if attempt > 1 {
			backoff := o.cfg.Retry.BackoffFor(attempt)
			_ = o.AppendJobLog(jobID, llm.LogLevelWarn, "retry", "retry_scheduled", "Retrying job after transient failure", map[string]any{
				"attempt":      attempt,
				"max_attempts": maxAttempts,
				"backoff_ms":   backoff.Milliseconds(),
			})
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
		runCtx, cancel := context.WithCancel(o.ctx)
		o.setRunCancel(jobID, cancel)
		var err error
		if req.RunWithContext != nil {
			err = req.RunWithContext(runCtx, rt)
		} else {
			err = req.Run(rt)
		}
		cancel()
		o.clearRunCancel(jobID)
		rt.flush()

		if err == nil {
			if o.isCancelled(jobID) {
				o.finalizeCancelled(jobID)
				return
			}
			o.finalizeReady(jobID, req)
			return
		}
		lastErr = err

		if errors.Is(err, ErrJobCancelled) || errors.Is(err, context.Canceled) || o.isCancelled(jobID) {
			o.finalizeCancelled(jobID)
			return
		}
		if !o.cfg.Retry.ShouldRetryWithMax(attempt, maxAttempts, err) {
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
	o.breaker.recordSuccess(req.Subsystem)
	job := o.store.GetByID(jobID)
	if job != nil {
		o.metrics.record(req.Subsystem, req.JobType, job.Elapsed(), llm.StatusReady)
		o.publish(llm.JobEvent{Kind: llm.EventCompleted, Job: job})
	}
	_ = o.AppendJobLog(jobID, llm.LogLevelInfo, "ready", "job_ready", "Job completed successfully", nil)
}

// finalizeFailed classifies the error, persists it, records metrics,
// and emits an event.
func (o *Orchestrator) finalizeFailed(jobID string, req *llm.EnqueueRequest, err error) {
	code := ClassifyError(err)
	o.breaker.recordFailure(req.Subsystem, code)
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
	_ = o.AppendJobLog(jobID, llm.LogLevelError, "failed", "job_failed", msg, map[string]any{
		"error_code": code,
	})
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
	_ = o.AppendJobLog(jobID, llm.LogLevelWarn, "cancelled", "job_cancelled", "Job cancelled before completion", nil)
}

func (o *Orchestrator) setRunCancel(jobID string, cancel context.CancelFunc) {
	o.runMu.Lock()
	defer o.runMu.Unlock()
	o.runCancels[jobID] = cancel
}

func (o *Orchestrator) clearRunCancel(jobID string) {
	o.runMu.Lock()
	defer o.runMu.Unlock()
	delete(o.runCancels, jobID)
}

func (o *Orchestrator) clearRunState(jobID string) {
	o.runMu.Lock()
	defer o.runMu.Unlock()
	delete(o.runCancels, jobID)
	delete(o.cancelled, jobID)
}

func (o *Orchestrator) isCancelled(jobID string) bool {
	o.runMu.Lock()
	defer o.runMu.Unlock()
	_, ok := o.cancelled[jobID]
	return ok
}
