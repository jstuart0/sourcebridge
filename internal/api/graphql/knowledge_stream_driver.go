// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"log/slog"
	"sync"

	commonv1 "github.com/sourcebridge/sourcebridge/gen/go/common/v1"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/worker"
)

// CA-122 / Phase 6: real-progress stream-to-DB bridge.
//
// runStreamProgressDriver replaces the synthetic-curve ticker
// (runProgressTicker) for streaming knowledge RPCs. The driver
// receives KnowledgeStreamEvent values from the worker via the
// llmcall.JobMetadata.OnProgress callback, maps them to per-RPC
// bucketed progress fractions, and writes BOTH rt.ReportProgress
// (which also feeds the LLM job's UpdatedAt heartbeat for the
// reaper's liveness check, codex r1 H2) AND the artifact store row.
//
// Lifecycle:
//   1. Caller spawns the driver via newStreamProgressDriver(...).
//   2. The driver returns a function suitable for
//      worker.WithProgressHandler(...) — install it via
//      llmcall.JobMetadata.OnProgress before invoking the streaming
//      Generate*WithJob method.
//   3. The handler runs on the gRPC stream.Recv() goroutine and
//      pushes events onto a small bounded channel; a dedicated
//      writer goroutine drains the channel and performs the writes.
//      DB-write latency therefore cannot become gRPC backpressure.
//   4. After the streaming RPC returns (success or error), the
//      caller calls Close() to drain remaining events and stop the
//      writer goroutine. The terminal-state writers run AFTER
//      Close() returns so a late event cannot resurrect a terminal
//      artifact (codex r1b M5).

// rpcBucketKind selects the per-RPC bucket map applied at driver
// instantiation time (codex r1c H2). Hierarchical for cliff notes
// repo-scope; collapsed for single-call RPCs and non-repo cliff
// notes scopes; report for the engine-fraction RPC.
type rpcBucketKind int

const (
	rpcBucketHierarchical rpcBucketKind = iota
	rpcBucketCollapsed
	rpcBucketReport
)

// bucketRange is the (min, max) pair the driver maps a phase to.
type bucketRange struct{ min, max float64 }

// hierarchicalBuckets is the bucket map for cliff notes repo-scope.
// Decision 4a in the plan.
var hierarchicalBuckets = map[commonv1.KnowledgePhase]bucketRange{
	commonv1.KnowledgePhase_KNOWLEDGE_PHASE_SNAPSHOT:          {0.05, 0.10},
	commonv1.KnowledgePhase_KNOWLEDGE_PHASE_LEAF_SUMMARIES:    {0.10, 0.40},
	commonv1.KnowledgePhase_KNOWLEDGE_PHASE_FILE_SUMMARIES:    {0.40, 0.60},
	commonv1.KnowledgePhase_KNOWLEDGE_PHASE_PACKAGE_SUMMARIES: {0.60, 0.80},
	commonv1.KnowledgePhase_KNOWLEDGE_PHASE_ROOT_SYNTHESIS:    {0.80, 0.90},
	commonv1.KnowledgePhase_KNOWLEDGE_PHASE_RENDER:            {0.90, 0.97},
	commonv1.KnowledgePhase_KNOWLEDGE_PHASE_FINALIZING:        {0.97, 1.00},
}

// collapsedBuckets is the bucket map for single-call RPCs and
// cliff notes file/symbol/module-scope. Phases collapse to
// snapshot -> render -> finalize.
var collapsedBuckets = map[commonv1.KnowledgePhase]bucketRange{
	commonv1.KnowledgePhase_KNOWLEDGE_PHASE_SNAPSHOT:   {0.05, 0.20},
	commonv1.KnowledgePhase_KNOWLEDGE_PHASE_RENDER:     {0.20, 0.97},
	commonv1.KnowledgePhase_KNOWLEDGE_PHASE_FINALIZING: {0.97, 1.00},
}

// reportBuckets matches collapsedBuckets shape but the engine's
// fraction lands inside RENDER for fine-grained motion.
var reportBuckets = map[commonv1.KnowledgePhase]bucketRange{
	commonv1.KnowledgePhase_KNOWLEDGE_PHASE_SNAPSHOT:   {0.05, 0.20},
	commonv1.KnowledgePhase_KNOWLEDGE_PHASE_RENDER:     {0.20, 0.97},
	commonv1.KnowledgePhase_KNOWLEDGE_PHASE_FINALIZING: {0.97, 1.00},
}

// phaseLabel maps a proto phase to the user-facing message string
// the GraphQL UI consumes via knowledgeArtifact.progressMessage. The
// "Building understanding · ..." prefix matches the synthetic curve's
// label format so existing UI strings continue to work.
func phaseLabel(p commonv1.KnowledgePhase) string {
	switch p {
	case commonv1.KnowledgePhase_KNOWLEDGE_PHASE_SNAPSHOT:
		return "Building understanding · assembling snapshot"
	case commonv1.KnowledgePhase_KNOWLEDGE_PHASE_LEAF_SUMMARIES:
		return "Building understanding · summarising leaves"
	case commonv1.KnowledgePhase_KNOWLEDGE_PHASE_FILE_SUMMARIES:
		return "Building understanding · summarising files"
	case commonv1.KnowledgePhase_KNOWLEDGE_PHASE_PACKAGE_SUMMARIES:
		return "Building understanding · summarising packages"
	case commonv1.KnowledgePhase_KNOWLEDGE_PHASE_ROOT_SYNTHESIS:
		return "Building understanding · synthesising root"
	case commonv1.KnowledgePhase_KNOWLEDGE_PHASE_RENDER:
		return "Building understanding · rendering"
	case commonv1.KnowledgePhase_KNOWLEDGE_PHASE_FINALIZING:
		return "Building understanding · finalising"
	default:
		return "Building understanding"
	}
}

// streamProgressDriver consumes KnowledgeStreamEvent values from the
// gRPC stream.Recv() goroutine and writes progress to the LLM
// runtime + artifact store row.
type streamProgressDriver struct {
	rt          llm.Runtime
	write       progressWriter
	buckets     map[commonv1.KnowledgePhase]bucketRange
	logFields   []any
	events      chan streamEvent
	wg          sync.WaitGroup
	closed      sync.Once
	currentMu   sync.Mutex
	curPhase    commonv1.KnowledgePhase
	curComplete int
	curTotal    int
}

// streamEvent is the internal queue payload — exactly one of
// (Phase, Progress) is set.
type streamEvent struct {
	Phase    *commonv1.KnowledgeStreamPhaseMarker
	Progress *commonv1.KnowledgeStreamProgress
}

// newStreamProgressDriver spawns the writer goroutine and returns
// the driver. Call OnProgress() to get a worker.KnowledgeStreamEvent
// handler suitable for worker.WithProgressHandler. Call Close() once
// the streaming RPC has returned to drain and stop the writer.
func newStreamProgressDriver(
	rt llm.Runtime,
	write progressWriter,
	kind rpcBucketKind,
	logFields ...any,
) *streamProgressDriver {
	var buckets map[commonv1.KnowledgePhase]bucketRange
	switch kind {
	case rpcBucketHierarchical:
		buckets = hierarchicalBuckets
	case rpcBucketReport:
		buckets = reportBuckets
	default:
		buckets = collapsedBuckets
	}
	d := &streamProgressDriver{
		rt:        rt,
		write:     write,
		buckets:   buckets,
		logFields: logFields,
		// 64 is enough to absorb a burst of progress events while
		// the writer goroutine is blocked on a slow Surreal write.
		// Older events get dropped under sustained pressure (progress
		// is monotonic — dropping a stale tick is safe; the next
		// fresh tick supersedes it).
		events: make(chan streamEvent, 64),
	}
	d.wg.Add(1)
	go d.run()
	return d
}

// OnProgress returns the handler suitable for
// worker.WithProgressHandler. The handler runs on the gRPC
// stream.Recv() goroutine; it MUST be nonblocking. We push events
// onto the bounded channel; under sustained pressure the oldest
// event is dropped (channel-full case is handled by select-default).
func (d *streamProgressDriver) OnProgress() func(worker.KnowledgeStreamEvent) {
	return func(ev worker.KnowledgeStreamEvent) {
		select {
		case d.events <- streamEvent{Phase: ev.Phase, Progress: ev.Progress}:
		default:
			// Channel full: best-effort drop. Progress is monotonic;
			// the next event supersedes the dropped one. We do not
			// log here — gRPC stream.Recv goroutine doesn't want
			// extra slog calls; the writer-queue depth metric in
			// Phase 9 is the right surface for catching backpressure.
		}
	}
}

// Close drains pending events, runs final writes, and stops the
// writer goroutine. Safe to call multiple times. Caller MUST call
// before writing any terminal status to the artifact (codex r1b M5
// driver-drain rule).
func (d *streamProgressDriver) Close() {
	d.closed.Do(func() {
		close(d.events)
		d.wg.Wait()
	})
}

// run is the writer goroutine. Reads events until the channel
// closes, then exits.
func (d *streamProgressDriver) run() {
	defer d.wg.Done()
	for ev := range d.events {
		switch {
		case ev.Phase != nil:
			d.handlePhase(ev.Phase)
		case ev.Progress != nil:
			d.handleProgress(ev.Progress)
		}
	}
}

func (d *streamProgressDriver) handlePhase(pm *commonv1.KnowledgeStreamPhaseMarker) {
	d.currentMu.Lock()
	d.curPhase = pm.GetPhase()
	d.curComplete = 0
	d.curTotal = 0
	pct := d.bucketMin(d.curPhase)
	d.currentMu.Unlock()

	msg := phaseLabel(pm.GetPhase())
	d.rt.ReportProgress(pct, "generating", msg)
	if err := d.write(pct, "generating", msg); err != nil {
		d.logWriteErr(err, "phase")
	}
}

func (d *streamProgressDriver) handleProgress(p *commonv1.KnowledgeStreamProgress) {
	d.currentMu.Lock()
	if p.GetPhase() != commonv1.KnowledgePhase_KNOWLEDGE_PHASE_UNSPECIFIED {
		d.curPhase = p.GetPhase()
	}
	d.curComplete = int(p.GetCompletedUnits())
	d.curTotal = int(p.GetTotalUnits())

	br, ok := d.buckets[d.curPhase]
	if !ok {
		// Unknown phase for this RPC kind — fall back to bucket-min
		// of the closest mapped phase (or RENDER as a sensible
		// default for unknown).
		br = d.buckets[commonv1.KnowledgePhase_KNOWLEDGE_PHASE_RENDER]
	}
	pct := br.min
	if d.curTotal > 0 {
		ratio := float64(d.curComplete) / float64(d.curTotal)
		if ratio > 1 {
			ratio = 1
		}
		if ratio < 0 {
			ratio = 0
		}
		pct = br.min + ratio*(br.max-br.min)
	}
	d.currentMu.Unlock()

	// Prefer the message the worker emitted; fall back to the
	// human-readable phase label.
	msg := p.GetMessage()
	if msg == "" {
		msg = phaseLabel(d.curPhase)
	}
	d.rt.ReportProgress(pct, "generating", msg)
	if err := d.write(pct, "generating", msg); err != nil {
		d.logWriteErr(err, "progress")
	}
}

// bucketMin returns the minimum percentage for the given phase, or
// 0 if the phase has no bucket in the active map (defensive).
func (d *streamProgressDriver) bucketMin(p commonv1.KnowledgePhase) float64 {
	if br, ok := d.buckets[p]; ok {
		return br.min
	}
	return 0
}

func (d *streamProgressDriver) logWriteErr(err error, kind string) {
	knowledgeProgressWriteErrorsTotal.Add(1)
	args := append([]any{
		"event", "knowledge_progress_write_failed",
		"phase", "generating",
		"event_kind", kind,
		"error", err,
	}, d.logFields...)
	slog.Warn("knowledge_progress_write_failed", args...)
}

// runStreamProgressDriver is the resolver-facing helper that wraps
// driver creation and supplies an artifact-store-backed progressWriter.
// Each *WithJob caller obtains a JobMetadata.OnProgress handler from
// this and calls Close() on the returned driver after the streaming
// RPC returns.
//
// Embedding the *Resolver as a method receiver matches the existing
// startProgressTicker / startUnderstandingProgressTicker pattern.
func (r *Resolver) runStreamProgressDriver(
	ctx context.Context,
	rt llm.Runtime,
	artifactID string,
	kind rpcBucketKind,
) *streamProgressDriver {
	_ = ctx // reserved for future per-driver cancellation; the
	// streaming RPC's own ctx already governs its lifetime.
	return newStreamProgressDriver(rt,
		func(p float64, phase, msg string) error {
			return r.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(artifactID, p, phase, msg)
		},
		kind,
		"artifact_id", artifactID,
	)
}

// runUnderstandingStreamDriver is the build_repository_understanding
// counterpart to runStreamProgressDriver — same semantics, writes to
// the understanding row instead of the artifact row.
func (r *Resolver) runUnderstandingStreamDriver(
	ctx context.Context,
	rt llm.Runtime,
	understandingID string,
	kind rpcBucketKind,
) *streamProgressDriver {
	_ = ctx
	return newStreamProgressDriver(rt,
		func(p float64, phase, msg string) error {
			return r.KnowledgeStore.UpdateRepositoryUnderstandingProgress(understandingID, p, phase, msg)
		},
		kind,
		"understanding_id", understandingID,
	)
}

// rpcBucketForArtifact picks the right bucket map for an artifact
// based on its type and scope. Cliff notes at repository scope use
// hierarchical; everything else uses collapsed. The enterprise
// report path picks rpcBucketReport directly.
func rpcBucketForArtifact(artifact *knowledgepkg.Artifact) rpcBucketKind {
	if artifact == nil {
		return rpcBucketCollapsed
	}
	if artifact.Type == knowledgepkg.ArtifactCliffNotes &&
		artifact.Scope != nil &&
		artifact.Scope.ScopeType == knowledgepkg.ScopeRepository {
		return rpcBucketHierarchical
	}
	return rpcBucketCollapsed
}
