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

type workflowStoryGenerationService struct {
	resolver *Resolver
	input    GenerateWorkflowStoryInput
}

func (s workflowStoryGenerationService) Generate(ctx context.Context) (*KnowledgeArtifact, error) {
	r := s.resolver
	input := s.input

	if err := r.requireKnowledgeGenerationSupport(); err != nil {
		return nil, err
	}
	repo, err := r.loadKnowledgeRepository(ctx, input.RepositoryID)
	if err != nil {
		return nil, err
	}
	key, err := artifactKeyFromWorkflowStoryInput(input)
	if err != nil {
		return nil, err
	}
	audience := string(key.Audience)
	depth := string(key.Depth)
	scope := key.Scope.Normalize()
	generationMode := resolvedKnowledgeGenerationMode(r.ComprehensionStore, repo, input.GenerationMode)

	existing := r.KnowledgeStore.GetArtifactByKeyAndMode(key, generationMode)
	if existing != nil {
		if existing.Status == knowledgepkg.StatusReady && !existing.Stale {
			return mapKnowledgeArtifact(existing), nil
		}
		if isInFlightGeneration(existing) {
			slog.Info("workflow_story_generation_deduped",
				"artifact_id", existing.ID,
				"elapsed_ms", time.Since(existing.UpdatedAt).Milliseconds())
			return mapKnowledgeArtifact(existing), nil
		}
		if existing.Status == knowledgepkg.StatusFailed || existing.Stale ||
			existing.Status == knowledgepkg.StatusGenerating || existing.Status == knowledgepkg.StatusPending {
			_ = r.KnowledgeStore.DeleteKnowledgeArtifact(existing.ID)
		}
	}

	assembler := knowledgepkg.NewAssembler(r.getStore(ctx))
	repoRoot, repoRootErr := resolveRepoSourcePath(repo)
	if repoRootErr != nil {
		slog.Warn("workflow story: repo source unavailable, docs will be omitted from snapshot",
			"repo_id", repo.ID, "error", repoRootErr)
	}
	var snap *knowledgepkg.KnowledgeSnapshot
	if scope.ScopeType == knowledgepkg.ScopeRepository {
		snap, err = assembler.Assemble(repo.ID, repoRoot)
	} else {
		snap, err = assembler.AssembleScoped(repo.ID, repoRoot, scope)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to assemble knowledge snapshot: %w", err)
	}

	snapJSON, err := json.Marshal(snap)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize snapshot: %w", err)
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

	anchorLabel := ""
	if input.AnchorLabel != nil {
		anchorLabel = strings.TrimSpace(*input.AnchorLabel)
	}
	executionPathJSON := ""
	if input.ExecutionPathJSON != nil {
		executionPathJSON = strings.TrimSpace(*input.ExecutionPathJSON)
	}

	enrichedSnapJSON := snapJSON
	store := r.getStore(ctx)
	err = r.enqueueKnowledgeJob(ctx, artifact, "workflow_story", len(enrichedSnapJSON), func(runCtx context.Context, rt llm.Runtime) error {
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
		resp, err := r.LLMCaller.GenerateWorkflowStoryWithJob(runCtx, repo.ID, resolution.OpKnowledge, llmJobMetadata(rt, artifact.ID, "workflow_story"), &knowledgev1.GenerateWorkflowStoryRequest{
			RepositoryId:      repo.ID,
			RepositoryName:    repo.Name,
			Audience:          audience,
			AudienceEnum:      protoAudience(knowledgepkg.Audience(audience)),
			Depth:             depth,
			DepthEnum:         protoDepth(knowledgepkg.Depth(depth)),
			ScopeType:         string(scope.ScopeType),
			ScopePath:         scope.ScopePath,
			AnchorLabel:       anchorLabel,
			ExecutionPathJson: executionPathJSON,
			SnapshotJson:      string(enrichedSnapJSON),
		})
		stopProgress()
		if err != nil {
			slog.Error("workflow story generation failed", "artifact_id", artifact.ID, "error", err)
			return err
		}

		rt.ReportProgress(0.96, "llm", "LLM completed, persisting sections")
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

		sections := make([]knowledgepkg.Section, len(resp.Sections))
		for i, sec := range resp.Sections {
			sections[i] = knowledgepkg.Section{
				Title:      sec.Title,
				Content:    sec.Content,
				Summary:    sec.Summary,
				Confidence: mapProtoConfidence(sec.Confidence),
				Inferred:   sec.Inferred,
			}
		}
		if err := r.KnowledgeStore.StoreKnowledgeSections(artifact.ID, sections); err != nil {
			slog.Error("failed to store workflow story sections", "artifact_id", artifact.ID, "error", err)
			return err
		}

		storedSections := r.KnowledgeStore.GetKnowledgeSections(artifact.ID)
		for i, sec := range resp.Sections {
			if i >= len(storedSections) {
				break
			}
			if len(sec.Evidence) > 0 {
				_ = r.KnowledgeStore.StoreKnowledgeEvidence(storedSections[i].ID, mapProtoEvidence(sec.Evidence))
			}
		}

		if err := r.KnowledgeStore.UpdateKnowledgeArtifactStatus(artifact.ID, knowledgepkg.StatusReady); err != nil {
			slog.Error("failed to mark workflow story ready", "artifact_id", artifact.ID, "error", err)
		}
		rt.ReportProgress(1.0, "ready", "Workflow story ready")
		slog.Info("workflow story generation complete", "artifact_id", artifact.ID)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("enqueue workflow story job: %w", err)
	}

	return mapKnowledgeArtifact(artifact), nil
}
