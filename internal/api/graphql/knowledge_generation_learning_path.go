package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	commonv1 "github.com/sourcebridge/sourcebridge/gen/go/common/v1"
	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
	"github.com/sourcebridge/sourcebridge/internal/worker"
)

type learningPathGenerationService struct {
	resolver *Resolver
	input    GenerateLearningPathInput
}

// learningPathRunParams bundles all inputs that runGenerationPipeline needs
// beyond the receiver. Defined here so both Generate and (eventually)
// RefreshFromExisting can build it with appropriate values.
type learningPathRunParams struct {
	baseRunParams
	focusArea string
}

func (s learningPathGenerationService) Generate(ctx context.Context) (*KnowledgeArtifact, error) {
	r := s.resolver
	input := s.input

	if err := r.requireKnowledgeGenerationSupport(); err != nil {
		return nil, err
	}
	repo, err := r.loadKnowledgeRepository(ctx, input.RepositoryID)
	if err != nil {
		return nil, err
	}

	audience := "developer"
	if input.Audience != nil {
		audience = strings.ToLower(string(*input.Audience))
	}
	depth := "medium"
	if input.Depth != nil {
		depth = strings.ToLower(string(*input.Depth))
	}
	focusArea := ""
	if input.FocusArea != nil {
		focusArea = *input.FocusArea
	}
	generationMode := resolvedKnowledgeGenerationMode(r.ComprehensionStore, repo, input.GenerationMode)

	assembler := knowledgepkg.NewAssembler(r.getStore(ctx))
	repoRoot, repoRootErr := resolveRepoSourcePath(repo)
	if repoRootErr != nil {
		slog.Warn("repo source unavailable, docs will be omitted", "repo_id", repo.ID, "error", repoRootErr)
	}
	snap, err := assembler.Assemble(ctx, repo.ID, repoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to assemble knowledge snapshot: %w", err)
	}

	snapJSON, err := json.Marshal(snap)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize snapshot: %w", err)
	}
	key := knowledgepkg.ArtifactKey{
		RepositoryID: repo.ID,
		Type:         knowledgepkg.ArtifactLearningPath,
		Audience:     knowledgepkg.Audience(audience),
		Depth:        knowledgepkg.Depth(depth),
		Scope:        knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository},
	}.Normalized()
	if existing := r.KnowledgeStore.GetArtifactByKeyAndMode(ctx, key, generationMode); existing != nil {
		if existing.Status == knowledgepkg.StatusReady && !existing.Stale {
			return mapKnowledgeArtifact(existing), nil
		}
		if isInFlightGeneration(existing) {
			slog.Info("learning_path_generation_deduped",
				"artifact_id", existing.ID,
				"elapsed_ms", time.Since(existing.UpdatedAt).Milliseconds())
			return mapKnowledgeArtifact(existing), nil
		}
		if existing.Status == knowledgepkg.StatusFailed || existing.Stale ||
			existing.Status == knowledgepkg.StatusGenerating || existing.Status == knowledgepkg.StatusPending {
			_ = r.KnowledgeStore.DeleteKnowledgeArtifact(ctx, existing.ID)
		}
	}
	artifact, created, err := r.KnowledgeStore.ClaimArtifactWithMode(ctx, key, snap.SourceRevision, generationMode)
	if err != nil {
		return nil, fmt.Errorf("failed to claim knowledge artifact: %w", err)
	}
	if !created {
		return mapKnowledgeArtifact(artifact), nil
	}
	artifact.GenerationMode = generationMode
	syncArtifactExecutionMetadata(r.KnowledgeStore, artifact)

	params := learningPathRunParams{
		baseRunParams: baseRunParams{
			repo:           repo,
			artifact:       artifact,
			snap:           snap,
			snapJSON:       snapJSON,
			generationMode: generationMode,
			audience:       audience,
			depth:          depth,
		},
		focusArea: focusArea,
	}
	capturedStore := r.getStore(ctx)
	err = r.enqueueKnowledgeJob(ctx, artifact, "learning_path", len(snapJSON), func(runCtx context.Context, rt llm.Runtime) error {
		return s.runGenerationPipeline(runCtx, rt, capturedStore, params)
	})
	if err != nil {
		return nil, fmt.Errorf("enqueue learning path job: %w", err)
	}

	return mapKnowledgeArtifact(artifact), nil
}

// runGenerationPipeline executes the learning-path LLM call and persistence
// steps for the given artifact. It is called from the enqueueKnowledgeJob
// closure in Generate and will be called from RefreshFromExisting once
// Phase 1 Slice 5d lands.
func (s learningPathGenerationService) runGenerationPipeline(
	runCtx context.Context,
	rt llm.Runtime,
	store graphstore.GraphStore,
	p learningPathRunParams,
) error {
	focusArea := p.focusArea
	return runKnowledgePipeline(runCtx, rt, store, s.resolver, p.baseRunParams, knowledgePipelineConfig{
		artifactLabel:          "learning path",
		rpcBucket:              rpcBucketCollapsed,
		progressPersistMessage: "LLM completed, persisting steps",
		readyMessage:           "Learning path ready",
		rpcFn: func(ctx context.Context, enrichedSnapJSON []byte, rt llm.Runtime, r *Resolver, base baseRunParams, onProgress func(worker.KnowledgeStreamEvent)) (any, *commonv1.LLMUsage, error) {
			resp, err := r.LLMCaller.GenerateLearningPathWithJob(ctx, base.repo.ID, resolution.OpKnowledge,
				llmJobMetadataWithProgress(rt, base.artifact.ID, "learning_path", onProgress),
				&knowledgev1.GenerateLearningPathRequest{
					RepositoryId:   base.repo.ID,
					RepositoryName: base.repo.Name,
					Audience:       base.audience,
					AudienceEnum:   protoAudience(knowledgepkg.Audience(base.audience)),
					Depth:          base.depth,
					DepthEnum:      protoDepth(knowledgepkg.Depth(base.depth)),
					SnapshotJson:   string(enrichedSnapJSON),
					FocusArea:      focusArea,
				})
			if err != nil {
				return nil, nil, err
			}
			return resp, resp.Usage, nil
		},
		mapSections: func(raw any) ([]knowledgepkg.Section, [][]knowledgepkg.Evidence) {
			resp := raw.(*knowledgev1.GenerateLearningPathResponse)
			sections := make([]knowledgepkg.Section, len(resp.Steps))
			evidences := make([][]knowledgepkg.Evidence, len(resp.Steps))
			for i, step := range resp.Steps {
				metaRaw, _ := json.Marshal(map[string]any{
					"prerequisite_steps": step.PrerequisiteSteps,
					"difficulty":         step.Difficulty,
					"exercises":          step.Exercises,
					"checkpoint":         step.Checkpoint,
				})
				sections[i] = knowledgepkg.Section{
					Title:            step.Title,
					Content:          step.Content,
					Summary:          step.Objective,
					Metadata:         string(metaRaw),
					Confidence:       mapProtoConfidence(step.Confidence),
					RefinementStatus: step.RefinementStatus,
				}
				var ev []knowledgepkg.Evidence
				for _, fp := range step.FilePaths {
					ev = append(ev, knowledgepkg.Evidence{
						SourceType: knowledgepkg.EvidenceFile,
						FilePath:   fp,
						Rationale:  "Referenced in learning step",
					})
				}
				for _, sid := range step.SymbolIds {
					ev = append(ev, knowledgepkg.Evidence{
						SourceType: knowledgepkg.EvidenceSymbol,
						SourceID:   sid,
						Rationale:  "Referenced in learning step",
					})
				}
				evidences[i] = ev
			}
			return sections, evidences
		},
	})
}

// RefreshFromExisting re-runs the generation pipeline against an existing
// artifact, replacing its sections in place (Phase 1 Slice 5d).
//
// audience and depth are read from the existing artifact's persisted fields.
// focusArea is not persisted on the artifact, so it is omitted on refresh
// (equivalent to the original generation default of no focus area).
func (s learningPathGenerationService) RefreshFromExisting(ctx context.Context, existing *knowledgepkg.Artifact) (*KnowledgeArtifact, error) {
	r := s.resolver

	repo := r.getStore(ctx).GetRepository(ctx, existing.RepositoryID)
	if repo == nil {
		return nil, fmt.Errorf("repository %s not found", existing.RepositoryID)
	}

	audience := string(existing.Audience)
	depth := string(existing.Depth)
	generationMode := existing.GenerationMode

	assembler := knowledgepkg.NewAssembler(r.getStore(ctx))
	repoRoot, repoRootErr := resolveRepoSourcePath(repo)
	if repoRootErr != nil {
		slog.Warn("learning path refresh: repo source unavailable, docs will be omitted",
			"repo_id", repo.ID, "error", repoRootErr)
	}

	capturedStore := r.getStore(ctx)
	err := r.enqueueKnowledgeJob(ctx, existing, "refresh:learning_path", 0, func(runCtx context.Context, rt llm.Runtime) error {
		snap, err := assembler.Assemble(ctx, repo.ID, repoRoot)
		if err != nil {
			slog.Error("learning path refresh assemble failed", "artifact_id", existing.ID, "error", err)
			return err
		}
		snapJSON, err := json.Marshal(snap)
		if err != nil {
			slog.Error("learning path refresh serialize failed", "artifact_id", existing.ID, "error", err)
			return err
		}
		rt.ReportSnapshotBytes(len(snapJSON))

		params := learningPathRunParams{
			baseRunParams: baseRunParams{
				repo:           repo,
				artifact:       existing,
				snap:           snap,
				snapJSON:       snapJSON,
				generationMode: generationMode,
				audience:       audience,
				depth:          depth,
			},
			focusArea: "",
		}
		return s.runGenerationPipeline(runCtx, rt, capturedStore, params)
	})
	if err != nil {
		return nil, fmt.Errorf("enqueue learning path refresh job: %w", err)
	}

	updated := r.KnowledgeStore.GetKnowledgeArtifact(ctx, existing.ID)
	if updated == nil {
		return nil, fmt.Errorf("artifact %s not found after refresh", existing.ID)
	}
	return mapKnowledgeArtifact(updated), nil
}
