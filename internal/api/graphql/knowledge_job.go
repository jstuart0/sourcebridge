// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/llm"
)

// startProgressTicker launches a goroutine that steadily advances both
// the llm.Job progress (via rt) and the knowledge artifact progress
// (via the store) while a long-running gRPC call is blocking. Without
// this, the progress bar stays at 10% for the entire hierarchical build.
//
// Returns a cancel function that stops the ticker.
func (r *Resolver) startProgressTicker(rt llm.Runtime, artifactID string) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		p := 0.15
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if p < 0.75 {
					p += 0.02
				}
				rt.ReportProgress(p, "generating", "Building summary tree")
				_ = r.KnowledgeStore.UpdateKnowledgeArtifactProgress(artifactID, p)
			}
		}
	}()
	return cancel
}

// knowledgeJobTargetKey returns the canonical dedupe key the orchestrator
// uses for a knowledge artifact generation job. Matching keys collapse to
// a single in-flight job so rapid duplicate requests (the original thor
// "3 calls in 3 seconds" thrash) turn into a single run.
//
// The key shape is intentionally verbose and deterministic:
//
//	knowledge:<repo_id>:<artifact_type>:<audience>:<depth>:<scope_key>
//
// Callers pass a normalized ArtifactKey so the scope section matches the
// exact lookup key used by ca_knowledge_artifact.
func knowledgeJobTargetKey(key knowledgepkg.ArtifactKey) string {
	norm := key.Normalized()
	return fmt.Sprintf("knowledge:%s:%s:%s:%s:%s",
		norm.RepositoryID,
		string(norm.Type),
		string(norm.Audience),
		string(norm.Depth),
		norm.ScopeKey(),
	)
}

// enqueueKnowledgeJob is the shared wrapper that routes knowledge
// artifact generation through the LLM orchestrator. It performs three
// jobs:
//
//  1. Build an EnqueueRequest that describes the work.
//  2. Wrap the caller's run closure so any error path also persists
//     failure state onto the ca_knowledge_artifact row (keeping the
//     artifact and the llm.Job records consistent).
//  3. Call Orchestrator.Enqueue. On a synchronous enqueue failure —
//     e.g. the orchestrator is shutting down — we also mirror the
//     failure onto the artifact so the user sees an error instead of
//     a perpetual spinner.
//
// The helper accepts the already-claimed artifact so the caller keeps
// ownership of artifact dedupe / claim logic (that's where Phase 1's
// staleness window lives).
func (r *Resolver) enqueueKnowledgeJob(
	artifact *knowledgepkg.Artifact,
	jobType string,
	snapshotBytes int,
	run func(rt llm.Runtime) error,
) error {
	if r.Orchestrator == nil {
		return fmt.Errorf("llm orchestrator is not configured")
	}
	if artifact == nil {
		return fmt.Errorf("artifact is required")
	}

	// Build the target key from the artifact's persisted scope. Using
	// the artifact as the source of truth guarantees dedupe matches the
	// DB-level knowledge dedupe regardless of how the caller built the
	// ArtifactKey.
	scope := knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository}
	if artifact.Scope != nil {
		scope = artifact.Scope.Normalize()
	}
	key := knowledgepkg.ArtifactKey{
		RepositoryID: artifact.RepositoryID,
		Type:         artifact.Type,
		Audience:     artifact.Audience,
		Depth:        artifact.Depth,
		Scope:        scope,
	}

	req := &llm.EnqueueRequest{
		Subsystem:  llm.SubsystemKnowledge,
		JobType:    jobType,
		TargetKey:  knowledgeJobTargetKey(key),
		ArtifactID: artifact.ID,
		RepoID:     artifact.RepositoryID,
		Run: func(rt llm.Runtime) error {
			if snapshotBytes > 0 {
				rt.ReportSnapshotBytes(snapshotBytes)
			}
			err := run(rt)
			if err != nil {
				// Mirror the failure onto the artifact so the existing
				// knowledgeArtifact GraphQL type shows the error. The
				// orchestrator will independently persist the error on
				// the llm.Job record via its finalizeFailed path.
				persistArtifactFailure(r.KnowledgeStore, artifact.ID, err)
				return err
			}
			return nil
		},
	}

	if _, err := r.Orchestrator.Enqueue(req); err != nil {
		// Synchronous enqueue failure — keep the artifact's failure
		// state in sync with the user-visible error.
		persistArtifactFailure(r.KnowledgeStore, artifact.ID, err)
		slog.Error("knowledge_job_enqueue_failed",
			"artifact_id", artifact.ID,
			"job_type", jobType,
			"error", err)
		return err
	}
	return nil
}
