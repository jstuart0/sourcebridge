// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
)

// monitorActivityResponse is the top-level payload for the Monitor page.
// It bundles the four things the UI needs in one round trip: the system
// health banner, active jobs, recent terminal jobs, and rollup metrics.
type monitorActivityResponse struct {
	Health  monitorHealth         `json:"health"`
	Active  []monitorJobView      `json:"active"`
	Recent  []monitorJobView      `json:"recent"`
	Metrics orchestrator.Snapshot `json:"metrics"`
	Control monitorQueueControl   `json:"control"`
	// Stats is derived queue state that tests and the Monitor header
	// rely on (max concurrency, current in-flight, pending queue depth).
	Stats monitorStats `json:"stats"`
}

// monitorHealth is the traffic-light summary at the top of the Monitor
// page. The Summary field is a one-sentence plain-English description
// that the frontend can render without any further transformation.
type monitorHealth struct {
	Status          string `json:"status"`  // "healthy" | "degraded" | "unhealthy"
	Summary         string `json:"summary"` // plain-english banner text
	WorkerConnected bool   `json:"worker_connected"`
	ActiveCount     int    `json:"active_count"`
	RecentFailed    int    `json:"recent_failed"`
	RecentSucceeded int    `json:"recent_succeeded"`
}

type monitorStats struct {
	InFlight              int `json:"in_flight"`
	QueueDepth            int `json:"queue_depth"`
	GateWaiting           int `json:"gate_waiting"`
	TotalWaiting          int `json:"total_waiting"`
	MaxConcurrency        int `json:"max_concurrency"`
	RecentReusedSummaries int `json:"recent_reused_summaries"`
	ActiveClassic         int `json:"active_classic"`
	ActiveUnderstanding   int `json:"active_understanding_first"`
	RecentClassic         int `json:"recent_classic"`
	RecentUnderstanding   int `json:"recent_understanding_first"`
	PendingInteractive    int `json:"pending_interactive"`
	PendingMaintenance    int `json:"pending_maintenance"`
	PendingPrewarm        int `json:"pending_prewarm"`
}

type monitorQueueControl struct {
	IntakePaused bool `json:"intake_paused"`
}

// monitorJobView is the serialization of an llm.Job for the Monitor page.
// It mirrors llm.Job but pre-computes the elapsed duration and a couple
// of human-readable fields so the frontend doesn't have to duplicate
// any of the formatting logic.
type monitorJobView struct {
	ID               string     `json:"id"`
	Subsystem        string     `json:"subsystem"`
	JobType          string     `json:"job_type"`
	TargetKey        string     `json:"target_key"`
	Strategy         string     `json:"strategy,omitempty"`
	Model            string     `json:"model,omitempty"`
	Priority         string     `json:"priority,omitempty"`
	GenerationMode   string     `json:"generation_mode,omitempty"`
	Status           string     `json:"status"`
	Progress         float64    `json:"progress"`
	ProgressPhase    string     `json:"progress_phase,omitempty"`
	ProgressMessage  string     `json:"progress_message,omitempty"`
	ErrorCode        string     `json:"error_code,omitempty"`
	ErrorMessage     string     `json:"error_message,omitempty"`
	ErrorTitle       string     `json:"error_title,omitempty"` // human-readable title derived from error_code
	ErrorHint        string     `json:"error_hint,omitempty"`  // one-sentence remediation
	RetryCount       int        `json:"retry_count"`
	MaxAttempts      int        `json:"max_attempts"`
	AttachedRequests int        `json:"attached_requests"`
	InputTokens      int        `json:"input_tokens"`
	OutputTokens     int        `json:"output_tokens"`
	SnapshotBytes    int        `json:"snapshot_bytes"`
	ReusedSummaries  int        `json:"reused_summaries"`
	LeafCacheHits    int        `json:"leaf_cache_hits"`
	FileCacheHits    int        `json:"file_cache_hits"`
	PackageCacheHits int        `json:"package_cache_hits"`
	RootCacheHits    int        `json:"root_cache_hits"`
	ArtifactID       string     `json:"artifact_id,omitempty"`
	RepoID           string     `json:"repo_id,omitempty"`
	ElapsedMs        int64      `json:"elapsed_ms"`
	QueuePosition    int        `json:"queue_position,omitempty"`
	QueueDepth       int        `json:"queue_depth,omitempty"`
	EstimatedWaitMs  int64      `json:"estimated_wait_ms,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	UpdatedAt        time.Time  `json:"updated_at"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
}

type monitorJobLogView struct {
	ID          string    `json:"id"`
	JobID       string    `json:"job_id"`
	RepoID      string    `json:"repo_id,omitempty"`
	ArtifactID  string    `json:"artifact_id,omitempty"`
	Subsystem   string    `json:"subsystem,omitempty"`
	JobType     string    `json:"job_type,omitempty"`
	Level       string    `json:"level"`
	Phase       string    `json:"phase,omitempty"`
	Event       string    `json:"event"`
	Message     string    `json:"message"`
	PayloadJSON string    `json:"payload_json,omitempty"`
	Sequence    int64     `json:"sequence"`
	CreatedAt   time.Time `json:"created_at"`
}

// errorTitleForCode maps a classified error code to a plain-English
// title + remediation hint. The Monitor page renders these directly —
// no code → user-facing string translation happens on the frontend.
// This mirrors the principle in the plan: "errors are a UX surface,
// not a log entry."
func errorTitleForCode(code string) (title, hint string) {
	switch code {
	case "LLM_EMPTY":
		return "Model returned nothing",
			"The LLM responded with empty content — typically because the prompt exceeded its context window. Try a smaller scope, a summary depth, or a larger-context model."
	case "SNAPSHOT_TOO_LARGE":
		return "Too much content for this model",
			"The pre-flight budget guard refused to send an oversized prompt. Switch to hierarchical strategy, reduce the scope, or select a larger-context model."
	case "DEADLINE_EXCEEDED":
		return "Took too long",
			"The worker did not finish within its timeout. The LLM may be overloaded — check the worker pod and retry."
	case "WORKER_UNAVAILABLE":
		return "Worker not reachable",
			"The worker gRPC channel was not ready. Check that sourcebridge-worker is running and accepting connections."
	case "PROVIDER_COMPUTE":
		return "Model backend compute failure",
			"The upstream model backend returned a compute error. This is usually transient overload or an unstable model runtime — queued retries may recover automatically."
	case "INVALID_ARGUMENT":
		return "Bad request",
			"The worker rejected the request as malformed. This is usually a code bug and should not auto-retry."
	case "ORCHESTRATOR_SHUTDOWN":
		return "Server shutting down",
			"The orchestrator stopped accepting new jobs during graceful shutdown. Retry once the server is back."
	case "NOT_FOUND":
		return "Target not found",
			"The worker could not find the referenced resource. Check that the target still exists and retry."
	case "UNAUTHORIZED":
		return "Unauthorized",
			"The worker rejected the call as unauthenticated or forbidden. Check credentials."
	case "INTERNAL":
		return "Worker error",
			"The worker reported an internal error. Check the worker logs for details."
	case "":
		return "", ""
	default:
		return "Error", ""
	}
}

func toMonitorJobView(j *llm.Job) monitorJobView {
	if j == nil {
		return monitorJobView{}
	}
	title, hint := errorTitleForCode(j.ErrorCode)
	return monitorJobView{
		ID:               j.ID,
		Subsystem:        string(j.Subsystem),
		JobType:          j.JobType,
		TargetKey:        j.TargetKey,
		Strategy:         j.Strategy,
		Model:            j.Model,
		Priority:         string(j.Priority),
		GenerationMode:   j.GenerationMode,
		Status:           string(j.Status),
		Progress:         j.Progress,
		ProgressPhase:    j.ProgressPhase,
		ProgressMessage:  j.ProgressMessage,
		ErrorCode:        j.ErrorCode,
		ErrorMessage:     j.ErrorMessage,
		ErrorTitle:       title,
		ErrorHint:        hint,
		RetryCount:       j.RetryCount,
		MaxAttempts:      j.MaxAttempts,
		AttachedRequests: j.AttachedRequests,
		InputTokens:      j.InputTokens,
		OutputTokens:     j.OutputTokens,
		SnapshotBytes:    j.SnapshotBytes,
		ReusedSummaries:  j.ReusedSummaries,
		LeafCacheHits:    j.LeafCacheHits,
		FileCacheHits:    j.FileCacheHits,
		PackageCacheHits: j.PackageCacheHits,
		RootCacheHits:    j.RootCacheHits,
		ArtifactID:       j.ArtifactID,
		RepoID:           j.RepoID,
		ElapsedMs:        j.Elapsed().Milliseconds(),
		CreatedAt:        j.CreatedAt,
		StartedAt:        j.StartedAt,
		UpdatedAt:        j.UpdatedAt,
		CompletedAt:      j.CompletedAt,
	}
}

func toMonitorJobLogView(entry *llm.JobLogEntry) monitorJobLogView {
	if entry == nil {
		return monitorJobLogView{}
	}
	return monitorJobLogView{
		ID:          entry.ID,
		JobID:       entry.JobID,
		RepoID:      entry.RepoID,
		ArtifactID:  entry.ArtifactID,
		Subsystem:   string(entry.Subsystem),
		JobType:     entry.JobType,
		Level:       string(entry.Level),
		Phase:       entry.Phase,
		Event:       entry.Event,
		Message:     entry.Message,
		PayloadJSON: entry.PayloadJSON,
		Sequence:    entry.Sequence,
		CreatedAt:   entry.CreatedAt,
	}
}

// parseListFilter extracts the common query parameters used by the
// activity and stream endpoints. All fields are optional.
func parseListFilter(r *http.Request) llm.ListFilter {
	q := r.URL.Query()
	filter := llm.ListFilter{
		Subsystem:  llm.Subsystem(q.Get("subsystem")),
		JobType:    q.Get("job_type"),
		RepoID:     q.Get("repo_id"),
		ArtifactID: q.Get("artifact_id"),
		TargetKey:  q.Get("target_key"),
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			filter.Limit = n
		}
	}
	return filter
}

// handleLLMActivity is the primary Monitor endpoint. It returns a
// single JSON bundle with everything the page needs to render:
// health banner, active jobs, recent jobs, metrics, stats.
//
// Query params (all optional):
//
//	subsystem  — filter to one subsystem (knowledge, reasoning, ...)
//	job_type   — filter to one job_type (cliff_notes, learning_path, ...)
//	repo_id    — filter to jobs tied to one repo
//	target_key — exact-match lookup
//	limit      — cap on recent history (default 50)
//	since      — only include recent jobs updated on/after this ISO8601 time
func (s *Server) handleLLMActivity(w http.ResponseWriter, r *http.Request) {
	if s.orchestrator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "llm orchestrator not configured",
		})
		return
	}

	filter := parseListFilter(r)
	if filter.Limit == 0 {
		filter.Limit = 50
	}

	since := time.Now().Add(-1 * time.Hour)
	if v := r.URL.Query().Get("since"); v != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, v); err == nil {
			since = parsed
		} else if parsed, err := time.Parse(time.RFC3339, v); err == nil {
			since = parsed
		}
	}

	// Active = pending + generating. Recent = terminal within since.
	activeFilter := filter
	activeFilter.Limit = 0 // no cap on active; there are never many
	active := s.orchestrator.ListActive(activeFilter)
	recent := s.orchestrator.ListRecent(filter, since)

	activeViews := make([]monitorJobView, 0, len(active))
	for _, j := range active {
		activeViews = append(activeViews, toMonitorJobView(j))
	}
	pending := s.orchestrator.PendingSnapshot(activeFilter)
	enrichQueueMetadata(activeViews, pending, s.orchestrator.Metrics(), s.orchestrator.MaxConcurrency())
	recentViews := make([]monitorJobView, 0, len(recent))
	for _, j := range recent {
		recentViews = append(recentViews, toMonitorJobView(j))
	}

	// Worker reachability is orthogonal to the orchestrator — a
	// healthy orchestrator with an unreachable worker is "degraded".
	workerConnected := s.worker != nil && s.worker.IsAvailable()

	health := computeMonitorHealth(workerConnected, len(activeViews), recentViews)

	resp := monitorActivityResponse{
		Health:  health,
		Active:  activeViews,
		Recent:  recentViews,
		Metrics: s.orchestrator.Metrics(),
		Control: monitorQueueControl{
			IntakePaused: s.orchestrator.IntakePaused(),
		},
		Stats: monitorStats{
			InFlight:              len(active), // DB-backed count — consistent across pods
			QueueDepth:            s.orchestrator.QueueDepth(),
			GateWaiting:           gateWaitingCount(activeViews),
			TotalWaiting:          s.orchestrator.QueueDepth() + gateWaitingCount(activeViews),
			MaxConcurrency:        s.orchestrator.MaxConcurrency(),
			RecentReusedSummaries: totalReusedSummaries(recentViews),
			ActiveClassic:         countGenerationMode(activeViews, "classic"),
			ActiveUnderstanding:   countGenerationMode(activeViews, "understanding_first"),
			RecentClassic:         countGenerationMode(recentViews, "classic"),
			RecentUnderstanding:   countGenerationMode(recentViews, "understanding_first"),
			PendingInteractive:    countPendingPriority(pending, llm.PriorityInteractive),
			PendingMaintenance:    countPendingPriority(pending, llm.PriorityMaintenance),
			PendingPrewarm:        countPendingPriority(pending, llm.PriorityPrewarm),
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

func totalReusedSummaries(jobs []monitorJobView) int {
	total := 0
	for _, job := range jobs {
		total += job.ReusedSummaries
	}
	return total
}

func countGenerationMode(jobs []monitorJobView, mode string) int {
	total := 0
	for _, job := range jobs {
		if job.GenerationMode == mode {
			total++
		}
	}
	return total
}

func countPendingPriority(jobs []*llm.Job, priority llm.JobPriority) int {
	total := 0
	for _, job := range jobs {
		if job != nil && job.Priority == priority {
			total++
		}
	}
	return total
}

func gateWaitingCount(jobs []monitorJobView) int {
	total := 0
	for _, job := range jobs {
		if isGateQueued(job) {
			total++
		}
	}
	return total
}

func isGateQueued(job monitorJobView) bool {
	return job.Status == string(llm.StatusGenerating) && job.ProgressPhase == "queued"
}

func enrichQueueMetadata(active []monitorJobView, pending []*llm.Job, metrics orchestrator.Snapshot, maxConcurrency int) {
	if len(active) == 0 {
		return
	}
	positions := make(map[string]int, len(pending))
	queueDepth := len(pending)
	for idx, job := range pending {
		positions[job.ID] = idx + 1
	}
	defaultWait := metrics.Overall.P50LatencyMs
	if defaultWait <= 0 {
		defaultWait = 30000
	}
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}
	gateQueued := make([]*monitorJobView, 0, len(active))
	for i := range active {
		if active[i].Status == string(llm.StatusPending) {
			pos := positions[active[i].ID]
			active[i].QueuePosition = pos
			active[i].QueueDepth = queueDepth
			waitBase := defaultWait
			if stats, ok := metrics.ByJobType[active[i].Subsystem+"/"+active[i].JobType]; ok && stats.P50LatencyMs > 0 {
				waitBase = stats.P50LatencyMs
			} else if stats, ok := metrics.BySubsystem[active[i].Subsystem]; ok && stats.P50LatencyMs > 0 {
				waitBase = stats.P50LatencyMs
			}
			wavesAhead := pos - 1
			if wavesAhead < 0 {
				wavesAhead = 0
			}
			active[i].EstimatedWaitMs = int64((wavesAhead / maxConcurrency)) * waitBase
			continue
		}
		if isGateQueued(active[i]) {
			gateQueued = append(gateQueued, &active[i])
		}
	}
	if len(gateQueued) == 0 {
		return
	}
	gateDepth := len(gateQueued)
	waitBase := defaultWait
	for idx, job := range gateQueued {
		job.QueuePosition = idx + 1
		job.QueueDepth = gateDepth
		if stats, ok := metrics.ByJobType[job.Subsystem+"/"+job.JobType]; ok && stats.P50LatencyMs > 0 {
			waitBase = stats.P50LatencyMs
		} else if stats, ok := metrics.BySubsystem[job.Subsystem]; ok && stats.P50LatencyMs > 0 {
			waitBase = stats.P50LatencyMs
		}
		wavesAhead := idx
		job.EstimatedWaitMs = int64((wavesAhead / maxConcurrency)) * waitBase
	}
}

// computeMonitorHealth derives the traffic-light health banner from
// the current worker state and recent terminal jobs.
//
// Rollouts can create short bursts of WORKER_UNAVAILABLE /
// ORCHESTRATOR_SHUTDOWN failures that do not reflect steady-state model
// health. Those are tracked separately so the banner can warn about
// recent churn without declaring the whole system unhealthy when the
// worker is currently reachable and other jobs are succeeding.
func computeMonitorHealth(workerConnected bool, activeCount int, recent []monitorJobView) monitorHealth {
	var succeeded, failed int
	var transientInfraFailures int
	for _, j := range recent {
		switch j.Status {
		case string(llm.StatusReady):
			succeeded++
		case string(llm.StatusFailed):
			failed++
			switch j.ErrorCode {
			case "WORKER_UNAVAILABLE", "ORCHESTRATOR_SHUTDOWN":
				transientInfraFailures++
			}
		}
	}
	actionableFailures := failed - transientInfraFailures

	h := monitorHealth{
		WorkerConnected: workerConnected,
		ActiveCount:     activeCount,
		RecentFailed:    failed,
		RecentSucceeded: succeeded,
	}

	total := succeeded + failed
	switch {
	case !workerConnected:
		h.Status = "unhealthy"
		h.Summary = "Worker not reachable — AI jobs cannot run until the worker comes back."
	case total >= 5 && actionableFailures*2 > total:
		h.Status = "unhealthy"
		h.Summary = fmt.Sprintf("Majority of recent jobs are failing — %d actionable failures of %d jobs in the last hour. Check the Monitor detail view for error codes.", actionableFailures, total)
	case transientInfraFailures > 0 && actionableFailures == 0:
		h.Status = "degraded"
		h.Summary = fmt.Sprintf("Recent worker restarts interrupted %d job(s), but the worker is reachable again and new jobs can run.", transientInfraFailures)
	case actionableFailures > 0 && total >= 2 && float64(actionableFailures)/float64(total) >= 0.2:
		h.Status = "degraded"
		h.Summary = fmt.Sprintf("Some recent jobs have failed — %d actionable failures of %d in the last hour. Jobs are still completing.", actionableFailures, total)
	case activeCount > 0:
		h.Status = "healthy"
		h.Summary = fmt.Sprintf("Running smoothly — %d job(s) active, %d completed in the last hour, %d failures.", activeCount, succeeded, failed)
	case total == 0:
		h.Status = "healthy"
		h.Summary = "Idle — no AI jobs in the last hour. The system is ready to run work."
	default:
		h.Status = "healthy"
		h.Summary = fmt.Sprintf("Running smoothly — %d jobs completed in the last hour, %d failures.", succeeded, failed)
	}
	return h
}

// handleLLMJobDetail returns the full job record for a single id.
func (s *Server) handleLLMJobDetail(w http.ResponseWriter, r *http.Request) {
	if s.orchestrator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "llm orchestrator not configured",
		})
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}
	job := s.orchestrator.GetJob(id)
	if job == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	writeJSON(w, http.StatusOK, toMonitorJobView(job))
}

func (s *Server) handleLLMJobCancel(w http.ResponseWriter, r *http.Request) {
	if s.orchestrator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "llm orchestrator not configured",
		})
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}
	if err := s.orchestrator.Cancel(id); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	job := s.orchestrator.GetJob(id)
	if s.knowledgeStore != nil && job != nil && job.ArtifactID != "" {
		_ = s.knowledgeStore.SetArtifactFailed(job.ArtifactID, "CANCELLED", "Generation was cancelled before completion.")
		job.Progress = 0
		job.ProgressPhase = ""
		job.ProgressMessage = ""
	}
	if job == nil {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancellation_requested"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "cancellation_requested",
		"job":    toMonitorJobView(job),
	})
}

// handleLLMJobRetry is a convenience endpoint. Today it does not
// actually re-enqueue (that requires recreating the original EnqueueRequest,
// which only the originating resolver has). Instead it surfaces a
// helpful "retry by calling the original mutation again" response so
// callers don't silently assume a retry happened. A future phase can
// wire this to per-subsystem retry factories.
func (s *Server) handleLLMJobRetry(w http.ResponseWriter, r *http.Request) {
	if s.orchestrator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "llm orchestrator not configured",
		})
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}
	job := s.orchestrator.GetJob(id)
	if job == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	// Only terminal failures can be retried from the Monitor.
	if job.Status != llm.StatusFailed && job.Status != llm.StatusCancelled {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "job is not in a retryable state",
			"status": string(job.Status),
		})
		return
	}
	// Return guidance. When subsystem-specific retry factories land,
	// this becomes a real Enqueue call.
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"status":  "manual_retry_required",
		"message": "Open the related artifact and click Refresh to re-run this job.",
		"job":     toMonitorJobView(job),
	})
}

// handleLLMJobLogs returns persisted structured log entries for one job.
func (s *Server) handleLLMJobLogs(w http.ResponseWriter, r *http.Request) {
	if s.orchestrator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "llm orchestrator not configured",
		})
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	var afterSequence int64
	if v := r.URL.Query().Get("after_sequence"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			afterSequence = n
		}
	}
	rows := s.orchestrator.ListJobLogs(id, llm.JobLogFilter{
		Limit:         limit,
		AfterSequence: afterSequence,
	})
	logs := make([]monitorJobLogView, 0, len(rows))
	for _, row := range rows {
		logs = append(logs, toMonitorJobLogView(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs})
}

// handleLLMJobLogStream streams structured log entries for one job via SSE.
func (s *Server) handleLLMJobLogStream(w http.ResponseWriter, r *http.Request) {
	if s.orchestrator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "llm orchestrator not configured",
		})
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "streaming not supported by this connection",
		})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

	events, unsubscribe := s.orchestrator.SubscribeLogs()
	defer unsubscribe()
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ev.JobID != id {
				continue
			}
			payload, err := json.Marshal(toMonitorJobLogView(&ev))
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: log\ndata: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// handleLLMStream is an SSE endpoint that streams JobEvents to the
// Monitor page in real time. It uses Server-Sent Events rather than
// websockets to keep the client-side trivial (the browser EventSource
// API handles reconnect, and no framing is required).
//
// Each event is encoded as:
//
//	event: <kind>
//	data:  <json job>
//
// The connection stays open until the client disconnects or the
// request context is cancelled.
func (s *Server) handleLLMStream(w http.ResponseWriter, r *http.Request) {
	if s.orchestrator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "llm orchestrator not configured",
		})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "streaming not supported by this connection",
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering

	// Send an initial comment so the client knows the connection is open.
	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

	events, unsubscribe := s.orchestrator.Subscribe()
	defer unsubscribe()

	// Optional filter by subsystem / repo so the repo-scoped popover
	// can subscribe without being drowned by other subsystems' events.
	filter := parseListFilter(r)

	// Heartbeat keeps intermediate proxies from dropping the connection.
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			if !eventMatchesFilter(ev, filter) {
				continue
			}
			payload, err := json.Marshal(toMonitorJobView(ev.Job))
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Kind, payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// eventMatchesFilter returns true when the event should be forwarded
// to a subscriber using the supplied filter. An empty filter matches
// every event.
func eventMatchesFilter(ev llm.JobEvent, filter llm.ListFilter) bool {
	if ev.Job == nil {
		return false
	}
	if filter.Subsystem != "" && ev.Job.Subsystem != filter.Subsystem {
		return false
	}
	if filter.JobType != "" && ev.Job.JobType != filter.JobType {
		return false
	}
	if filter.RepoID != "" && ev.Job.RepoID != filter.RepoID {
		return false
	}
	if filter.TargetKey != "" && ev.Job.TargetKey != filter.TargetKey {
		return false
	}
	return true
}
