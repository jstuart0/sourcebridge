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
	enrichedSnapJSON := snapJSON
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
	store := r.getStore(ctx)

	err = r.enqueueKnowledgeJob(ctx, artifact, "learning_path", len(enrichedSnapJSON), func(runCtx context.Context, rt llm.Runtime) error {
		rt.ReportProgress(0.1, "snapshot", "Snapshot assembled")
		_ = r.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(artifact.ID, 0.1, "snapshot", "Snapshot assembled")
		if artifactUsesUnderstanding(generationMode) {
			if understanding, reused, err := r.ensureFreshRepositoryUnderstanding(runCtx, rt, repo, artifact, snap.SourceRevision, snapJSON); err != nil {
				return err
			} else {
				if reused {
					rt.ReportProgress(0.12, "understanding", "Using cached repository understanding")
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

		stopProgress := r.startProgressTicker(rt, artifact.ID)
		resp, err := r.LLMCaller.GenerateLearningPathWithJob(runCtx, repo.ID, resolution.OpKnowledge, llmJobMetadata(rt, artifact.ID, "learning_path"), &knowledgev1.GenerateLearningPathRequest{
			RepositoryId:   repo.ID,
			RepositoryName: repo.Name,
			Audience:       audience,
			AudienceEnum:   protoAudience(knowledgepkg.Audience(audience)),
			Depth:          depth,
			DepthEnum:      protoDepth(knowledgepkg.Depth(depth)),
			SnapshotJson:   string(enrichedSnapJSON),
			FocusArea:      focusArea,
		})
		stopProgress()
		if err != nil {
			slog.Error("learning path generation failed", "artifact_id", artifact.ID, "error", err)
			return err
		}

		rt.ReportProgress(0.96, "llm", "LLM completed, persisting steps")
		_ = r.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(artifact.ID, 0.8, "llm", "LLM completed, persisting")

		if resp.Usage != nil {
			store.StoreLLMUsage(&graphstore.LLMUsageRecord{
				RepoID:       repo.ID,
				Provider:     "llm",
				Model:        resp.Usage.Model,
				Operation:    resp.Usage.Operation,
				InputTokens:  int(resp.Usage.InputTokens),
				OutputTokens: int(resp.Usage.OutputTokens),
			})
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
		rt.ReportProgress(1.0, "ready", "Learning path ready")
		slog.Info("learning path generation complete", "artifact_id", artifact.ID)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("enqueue learning path job: %w", err)
	}

	return mapKnowledgeArtifact(artifact), nil
}
