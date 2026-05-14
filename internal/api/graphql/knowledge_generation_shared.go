// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"log/slog"

	commonv1 "github.com/sourcebridge/sourcebridge/gen/go/common/v1"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/usage"
	"github.com/sourcebridge/sourcebridge/internal/worker"
)

// baseRunParams contains the fields common to all knowledge artifact
// runParams structs. Each artifact type embeds this and adds its own
// type-specific fields (focusArea, theme, anchorLabel, etc.).
type baseRunParams struct {
	repo           *graphstore.Repository
	artifact       *knowledgepkg.Artifact
	snap           *knowledgepkg.KnowledgeSnapshot
	snapJSON       []byte
	generationMode knowledgepkg.GenerationMode
	audience       string
	depth          string
}

// knowledgePipelineConfig carries the artifact-specific callables that
// runKnowledgePipeline plugs into the common scaffold. This covers the
// three "simple" artifact types (learning_path, workflow_story, code_tour)
// whose runGenerationPipeline bodies follow an identical structure:
// understanding-enrich → stream RPC → map response → persist sections.
//
// CliffNotes is intentionally excluded — its pipeline drives a hierarchical
// rendering strategy chain with phase markers and render-plan logic that
// cannot be cleanly expressed through this config.
type knowledgePipelineConfig struct {
	// artifactLabel is used in slog key names and progress messages
	// (e.g. "learning path", "workflow story", "code tour").
	artifactLabel string

	// rpcBucket selects the stream-progress bucket map to use.
	rpcBucket rpcBucketKind

	// rpcFn is the type-specific LLM call. It receives the enriched snapshot
	// JSON and the stream-progress callback, and must return the response
	// proto and LLM usage. The function signature uses any for the response
	// because each RPC returns a different concrete proto type; the caller
	// casts inside mapSections.
	//
	// onProgress is wired from the streamProgressDriver opened by
	// runKnowledgePipeline before rpcFn is called. The driver is closed
	// after rpcFn returns, following the codex r1b M5 driver-drain rule.
	rpcFn func(
		ctx context.Context,
		enrichedSnapJSON []byte,
		rt llm.Runtime,
		r *Resolver,
		p baseRunParams,
		onProgress func(worker.KnowledgeStreamEvent),
	) (any, *commonv1.LLMUsage, error)

	// mapSections maps the RPC response into Knowledge sections and
	// per-section evidence slices. The evidence slice may be nil if the
	// artifact type provides no evidence.
	mapSections func(resp any) ([]knowledgepkg.Section, [][]knowledgepkg.Evidence)

	// progressPersistMessage is the message emitted at progress 0.96 after
	// the LLM call returns (e.g. "LLM completed, persisting steps").
	progressPersistMessage string

	// readyMessage is the message emitted at progress 1.0 (e.g. "Learning
	// path ready").
	readyMessage string
}

// runKnowledgePipeline executes the common scaffold for learning_path,
// workflow_story, and code_tour runGenerationPipeline:
//
//  1. Report snapshot progress
//  2. Optionally build and cache repository understanding
//  3. Optionally enrich snapshot with cliff-notes analysis (deep depth)
//  4. Open stream-progress driver
//  5. Call the type-specific RPC (cfg.rpcFn) with driver's OnProgress
//  6. Close stream driver (MUST precede any terminal artifact writes)
//  7. Store LLM usage
//  8. Map response to sections + evidence (cfg.mapSections)
//  9. Persist sections and per-section evidence
//  10. Mark artifact ready
//  11. Report final progress
//
// CliffNotes generation must NOT use this function — it drives a
// hierarchical strategy chain with phase markers and render-plan logic
// that is not expressible through this config.
func runKnowledgePipeline(
	runCtx context.Context,
	rt llm.Runtime,
	store graphstore.GraphStore,
	r *Resolver,
	p baseRunParams,
	cfg knowledgePipelineConfig,
) error {
	artifact := p.artifact
	repo := p.repo
	snap := p.snap
	snapJSON := p.snapJSON
	generationMode := p.generationMode
	audience := p.audience
	depth := p.depth

	enrichedSnapJSON := snapJSON
	rt.ReportProgress(0.1, "snapshot", "Snapshot assembled", 0)
	_ = r.Deps.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(runCtx, artifact.ID, 0.1, "snapshot", "Snapshot assembled")
	if artifactUsesUnderstanding(generationMode) {
		if understanding, reused, err := r.ensureFreshRepositoryUnderstanding(runCtx, rt, repo, artifact, snap.SourceRevision, snapJSON); err != nil {
			return err
		} else {
			if reused {
				rt.ReportProgress(0.12, "understanding", "Using cached repository understanding", 0)
				_ = r.Deps.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(runCtx, artifact.ID, 0.12, "understanding", "Using cached repository understanding")
			}
			if understanding != nil {
				if enriched, ok := enrichSnapshotWithUnderstanding(snapJSON, understanding); ok {
					enrichedSnapJSON = enriched
				}
			}
		}
	}
	if knowledgepkg.Depth(depth) == knowledgepkg.DepthDeep {
		if enriched, ok := enrichSnapshotWithCliffNotesAnalysis(r.Deps.KnowledgeStore, repo.ID, knowledgepkg.Audience(audience), enrichedSnapJSON); ok {
			enrichedSnapJSON = enriched
		}
	}

	// Open the stream-progress driver before the RPC and close it BEFORE
	// any terminal artifact-state writes (codex r1b M5 driver-drain rule).
	streamDriver := r.runStreamProgressDriver(runCtx, rt, artifact.ID, cfg.rpcBucket)
	resp, usage, err := cfg.rpcFn(runCtx, enrichedSnapJSON, rt, r, p, streamDriver.OnProgress())
	streamDriver.Close()
	if err != nil {
		slog.Error(cfg.artifactLabel+" generation failed", "artifact_id", artifact.ID, "error", err)
		return err
	}

	rt.ReportProgress(0.96, "llm", cfg.progressPersistMessage, 0)
	_ = r.Deps.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(runCtx, artifact.ID, 0.8, "llm", "LLM completed, persisting")

	if usage != nil {
		storeLLMUsage(store, repo.ID, usage, "")
		rt.ReportTokens(int(usage.InputTokens), int(usage.OutputTokens))
	}

	sections, evidences := cfg.mapSections(resp)
	if err := r.Deps.KnowledgeStore.StoreKnowledgeSections(runCtx, artifact.ID, sections); err != nil {
		slog.Error("failed to store "+cfg.artifactLabel+" sections", "artifact_id", artifact.ID, "error", err)
		return err
	}

	if len(evidences) > 0 {
		storedSections := r.Deps.KnowledgeStore.GetKnowledgeSections(runCtx, artifact.ID)
		for i, ev := range evidences {
			if i >= len(storedSections) || len(ev) == 0 {
				continue
			}
			_ = r.Deps.KnowledgeStore.StoreKnowledgeEvidence(runCtx, storedSections[i].ID, ev)
		}
	}

	if err := r.markArtifactReady(runCtx, artifact.ID); err != nil {
		slog.Error("failed to mark "+cfg.artifactLabel+" ready", "artifact_id", artifact.ID, "error", err)
	}
	rt.ReportProgress(1.0, "ready", cfg.readyMessage, 0)
	slog.Info(cfg.artifactLabel+" generation complete", "artifact_id", artifact.ID)
	return nil
}

// markArtifactReady transitions an artifact from GENERATING to READY and,
// on success, increments the rolling 30-day artifacts-generated counter for
// telemetry. It is the single instrumentation point for user-requested
// artifact generation.
//
// The SupersedeArtifact paths (knowledge_seed.go and cliff-note deepening in
// knowledge_support.go) are intentionally NOT routed through this wrapper —
// they represent seed/refresh operations, not new user-requested generation.
// See Decision 2 in the CA-400 plan for the full rationale.
func (r *Resolver) markArtifactReady(ctx context.Context, artifactID string) error {
	if err := r.Deps.KnowledgeStore.UpdateKnowledgeArtifactStatus(ctx, artifactID, knowledgepkg.StatusReady); err != nil {
		return err
	}
	usage.ArtifactsCounter.Inc()
	return nil
}
