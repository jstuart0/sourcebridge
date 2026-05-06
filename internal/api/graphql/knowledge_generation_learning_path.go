package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
)

type learningPathGenerationService struct {
	resolver *Resolver
	input    GenerateLearningPathInput
}

// learningPathRunParams bundles all inputs that runGenerationPipeline needs
// beyond the receiver. Defined here so both Generate and (eventually)
// RefreshFromExisting can build it with appropriate values.
type learningPathRunParams struct {
	repo           *graphstore.Repository
	artifact       *knowledgepkg.Artifact
	snap           *knowledgepkg.KnowledgeSnapshot
	snapJSON       []byte
	generationMode knowledgepkg.GenerationMode
	audience       string
	depth          string
	focusArea      string
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
	snap, err := assembler.Assemble(repo.ID, repoRoot)
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
	if existing := r.KnowledgeStore.GetArtifactByKeyAndMode(key, generationMode); existing != nil {
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
			_ = r.KnowledgeStore.DeleteKnowledgeArtifact(existing.ID)
		}
	}
	artifact, created, err := r.KnowledgeStore.ClaimArtifactWithMode(key, snap.SourceRevision, generationMode)
	if err != nil {
		return nil, fmt.Errorf("failed to claim knowledge artifact: %w", err)
	}
	if !created {
		return mapKnowledgeArtifact(artifact), nil
	}
	artifact.GenerationMode = generationMode
	syncArtifactExecutionMetadata(r.KnowledgeStore, artifact)

	params := learningPathRunParams{
		repo:           repo,
		artifact:       artifact,
		snap:           snap,
		snapJSON:       snapJSON,
		generationMode: generationMode,
		audience:       audience,
		depth:          depth,
		focusArea:      focusArea,
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
	r := s.resolver
	artifact := p.artifact
	repo := p.repo
	snap := p.snap
	snapJSON := p.snapJSON
	generationMode := p.generationMode
	audience := p.audience
	depth := p.depth
	focusArea := p.focusArea

	enrichedSnapJSON := snapJSON
	rt.ReportProgress(0.1, "snapshot", "Snapshot assembled", 0)
	_ = r.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(artifact.ID, 0.1, "snapshot", "Snapshot assembled")
	if artifactUsesUnderstanding(generationMode) {
		if understanding, reused, err := r.ensureFreshRepositoryUnderstanding(runCtx, rt, repo, artifact, snap.SourceRevision, snapJSON); err != nil {
			return err
		} else {
			if reused {
				rt.ReportProgress(0.12, "understanding", "Using cached repository understanding", 0)
				_ = r.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(artifact.ID, 0.12, "understanding", "Using cached repository understanding")
			}
			if understanding != nil {
				if enriched, ok := enrichSnapshotWithUnderstanding(snapJSON, understanding); ok {
					enrichedSnapJSON = enriched
				}
			}
		}
	}
	if knowledgepkg.Depth(depth) == knowledgepkg.DepthDeep {
		if enriched, ok := enrichSnapshotWithCliffNotesAnalysis(r.KnowledgeStore, repo.ID, knowledgepkg.Audience(audience), enrichedSnapJSON); ok {
			enrichedSnapJSON = enriched
		}
	}

	streamDriver := r.runStreamProgressDriver(runCtx, rt, artifact.ID, rpcBucketCollapsed)
	resp, err := r.LLMCaller.GenerateLearningPathWithJob(runCtx, repo.ID, resolution.OpKnowledge,
		llmJobMetadataWithProgress(rt, artifact.ID, "learning_path", streamDriver.OnProgress()),
		&knowledgev1.GenerateLearningPathRequest{
			RepositoryId:   repo.ID,
			RepositoryName: repo.Name,
			Audience:       audience,
			AudienceEnum:   protoAudience(knowledgepkg.Audience(audience)),
			Depth:          depth,
			DepthEnum:      protoDepth(knowledgepkg.Depth(depth)),
			SnapshotJson:   string(enrichedSnapJSON),
			FocusArea:      focusArea,
		})
	streamDriver.Close()
	if err != nil {
		slog.Error("learning path generation failed", "artifact_id", artifact.ID, "error", err)
		return err
	}

	rt.ReportProgress(0.96, "llm", "LLM completed, persisting steps", 0)
	_ = r.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(artifact.ID, 0.8, "llm", "LLM completed, persisting")

	if resp.Usage != nil {
		storeLLMUsage(store, repo.ID, resp.Usage, "")
		rt.ReportTokens(int(resp.Usage.InputTokens), int(resp.Usage.OutputTokens))
	}

	sections := make([]knowledgepkg.Section, len(resp.Steps))
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
	}
	if err := r.KnowledgeStore.StoreKnowledgeSections(artifact.ID, sections); err != nil {
		slog.Error("failed to store learning path sections", "artifact_id", artifact.ID, "error", err)
		return err
	}

	storedSections := r.KnowledgeStore.GetKnowledgeSections(artifact.ID)
	for i, step := range resp.Steps {
		if i >= len(storedSections) {
			break
		}
		var evidence []knowledgepkg.Evidence
		for _, fp := range step.FilePaths {
			evidence = append(evidence, knowledgepkg.Evidence{
				SourceType: knowledgepkg.EvidenceFile,
				FilePath:   fp,
				Rationale:  "Referenced in learning step",
			})
		}
		for _, sid := range step.SymbolIds {
			evidence = append(evidence, knowledgepkg.Evidence{
				SourceType: knowledgepkg.EvidenceSymbol,
				SourceID:   sid,
				Rationale:  "Referenced in learning step",
			})
		}
		if len(evidence) > 0 {
			_ = r.KnowledgeStore.StoreKnowledgeEvidence(storedSections[i].ID, evidence)
		}
	}

	if err := r.KnowledgeStore.UpdateKnowledgeArtifactStatus(artifact.ID, knowledgepkg.StatusReady); err != nil {
		slog.Error("failed to mark learning path ready", "artifact_id", artifact.ID, "error", err)
	}
	rt.ReportProgress(1.0, "ready", "Learning path ready", 0)
	slog.Info("learning path generation complete", "artifact_id", artifact.ID)
	return nil
}

// RefreshFromExisting re-runs the generation pipeline against an existing
// artifact, replacing its sections in place (Phase 1 Slice 5d).
//
// audience and depth are read from the existing artifact's persisted fields.
// focusArea is not persisted on the artifact, so it is omitted on refresh
// (equivalent to the original generation default of no focus area).
func (s learningPathGenerationService) RefreshFromExisting(ctx context.Context, existing *knowledgepkg.Artifact) (*KnowledgeArtifact, error) {
	r := s.resolver

	repo := r.getStore(ctx).GetRepository(existing.RepositoryID)
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
		snap, err := assembler.Assemble(repo.ID, repoRoot)
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
			repo:           repo,
			artifact:       existing,
			snap:           snap,
			snapJSON:       snapJSON,
			generationMode: generationMode,
			audience:       audience,
			depth:          depth,
			focusArea:      "",
		}
		return s.runGenerationPipeline(runCtx, rt, capturedStore, params)
	})
	if err != nil {
		return nil, fmt.Errorf("enqueue learning path refresh job: %w", err)
	}

	updated := r.KnowledgeStore.GetKnowledgeArtifact(existing.ID)
	if updated == nil {
		return nil, fmt.Errorf("artifact %s not found after refresh", existing.ID)
	}
	return mapKnowledgeArtifact(updated), nil
}
