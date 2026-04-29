// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/sourcebridge/sourcebridge/internal/llm"
)

// hashString returns a short hex digest of the supplied input, used to
// build stable dedupe keys from free-form inputs like natural-language
// questions or code snippets. 12 hex chars is plenty to avoid collisions
// in practical orchestrator dedupe windows.
func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// runSyncLLMJob routes a synchronous LLM-backed mutation through the
// orchestrator while preserving the caller's inline result pattern.
//
// Use this for mutations that return structured data to the client
// (AnalyzeSymbol, DiscussCode, ReviewCode, EnrichRequirement, ...)
// rather than spawning a background artifact generation.
//
// The closure should capture its response into an outer variable the
// caller can read after runSyncLLMJob returns — the orchestrator waits
// for the job to reach a terminal state before returning, so the
// closure's writes are visible to the caller under the happens-before
// relationship established by the channel-based terminal event.
//
// When the orchestrator is unavailable (nil on the resolver, e.g. in
// some test fixtures) the closure is executed inline with a no-op
// runtime so the mutation still works — the only thing lost is Monitor
// visibility.
func (r *Resolver) runSyncLLMJob(
	ctx context.Context,
	subsystem llm.Subsystem,
	jobType string,
	targetKey string,
	repoID string,
	run func(rt llm.Runtime) error,
) error {
	if r.Orchestrator == nil {
		return run(noopRuntime{})
	}
	job, err := r.Orchestrator.EnqueueSync(ctx, &llm.EnqueueRequest{
		Subsystem: subsystem,
		JobType:   jobType,
		TargetKey: targetKey,
		RepoID:    repoID,
		Run:       run,
	})
	if err != nil {
		return err
	}
	if job != nil && job.Status == llm.StatusFailed {
		if job.ErrorMessage != "" {
			return errors.New(job.ErrorMessage)
		}
		return errors.New("llm job failed")
	}
	return nil
}

// noopRuntime is the fallback runtime used when the orchestrator is
// not wired up. It discards all progress/token/snapshot updates so
// callers can still run without conditional checks.
type noopRuntime struct{}

func (noopRuntime) JobID() string                                              { return "" }
func (noopRuntime) ReportProgress(progress float64, phase, message string)     {}
func (noopRuntime) ReportTokens(input, output int)                             {}
func (noopRuntime) ReportSnapshotBytes(bytes int)                              {}
func (noopRuntime) Heartbeat() error                                           { return nil }
