package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
)

type architectureDiagramGenerationService struct {
	resolver *Resolver
	input    GenerateArchitectureDiagramInput
}

func (s architectureDiagramGenerationService) Generate(ctx context.Context) (*KnowledgeArtifact, error) {
	r := s.resolver
	input := s.input

	if err := r.requireKnowledgeGenerationSupport(); err != nil {
		return nil, err
	}
	repo, err := r.loadKnowledgeRepository(ctx, input.RepositoryID)
	if err != nil {
		return nil, err
	}

	audience := knowledgeAudienceValue(input.Audience)
	depth := knowledgeDepthValue(input.Depth)
	key := knowledgepkg.ArtifactKey{
		RepositoryID: repo.ID,
		Type:         knowledgepkg.ArtifactArchitectureDiagram,
		Audience:     audience,
		Depth:        depth,
		Scope:        knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository},
	}.Normalized()
	generationMode := knowledgepkg.GenerationModeUnderstandingFirst

	if existing := r.KnowledgeStore.GetArtifactByKeyAndMode(key, generationMode); existing != nil {
		if existing.Status == knowledgepkg.StatusReady && !existing.Stale {
			return mapKnowledgeArtifact(existing), nil
		}
		if isInFlightGeneration(existing) {
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
		slog.Warn("architecture diagram: repo source unavailable, docs will be omitted from snapshot",
			"repo_id", repo.ID, "error", repoRootErr)
	}
	snap, err := assembler.Assemble(repo.ID, repoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to assemble knowledge snapshot: %w", err)
	}
	snapJSON, err := json.Marshal(snap)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize snapshot: %w", err)
	}
	scaffoldJSON, err := buildArchitectureDiagramScaffold(r.getStore(ctx), repo.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to build architecture scaffold: %w", err)
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

	err = r.enqueueKnowledgeJob(artifact, "architecture_diagram", len(snapJSON), func(runCtx context.Context, rt llm.Runtime) error {
		rt.ReportProgress(0.1, "snapshot", "Snapshot assembled")
		_ = r.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(artifact.ID, 0.1, "snapshot", "Snapshot assembled")

		var architectureBundle architectureDiagramPromptBundle
		var architecturePromptJSON []byte
		var understandingForDiagram *knowledgepkg.RepositoryUnderstanding
		if understanding, reused, err := r.ensureFreshRepositoryUnderstanding(runCtx, rt, repo, artifact, snap.SourceRevision, snapJSON); err != nil {
			return err
		} else {
			understandingForDiagram = understanding
			if reused {
				rt.ReportProgress(0.12, "understanding", "Using cached repository understanding")
				_ = r.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(artifact.ID, 0.12, "understanding", "Using cached repository understanding")
			}
		}
		if promptJSON, err := buildArchitectureDiagramPromptBundle(r.KnowledgeStore, repo.ID, knowledgepkg.Audience(audience), snap, understandingForDiagram, scaffoldJSON); err != nil {
			return err
		} else {
			architecturePromptJSON = promptJSON
			if err := json.Unmarshal(promptJSON, &architectureBundle); err != nil {
				return fmt.Errorf("failed to unmarshal architecture prompt bundle: %w", err)
			}
		}

		stopProgress := r.startProgressTicker(rt, artifact.ID)
		resp, err := r.LLMCaller.GenerateArchitectureDiagramWithJob(
			runCtx,
			repo.ID,
			resolution.OpArchitectureDiagram,
			llmJobMetadata(rt, artifact.ID, "architecture_diagram"),
			&knowledgev1.GenerateArchitectureDiagramRequest{
				RepositoryId:             repo.ID,
				RepositoryName:           repo.Name,
				Audience:                 string(audience),
				AudienceEnum:             protoAudience(audience),
				Depth:                    string(depth),
				DepthEnum:                protoDepth(depth),
				SnapshotJson:             string(architecturePromptJSON),
				DeterministicDiagramJson: string(scaffoldJSON),
			},
		)
		stopProgress()
		if err != nil {
			slog.Error("architecture diagram generation failed", "artifact_id", artifact.ID, "error", err)
			return err
		}

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

		rt.ReportProgress(0.96, "llm", "LLM completed, persisting diagram")
		_ = r.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(artifact.ID, 0.8, "llm", "LLM completed, persisting diagram")

		sections := []knowledgepkg.Section{{
			Title:            "AI Architecture Diagram",
			SectionKey:       "ai_architecture_diagram",
			Content:          resp.MermaidSource,
			Summary:          resp.DiagramSummary,
			Metadata:         architectureDiagramMetadataJSON(resp, &architectureBundle),
			Confidence:       knowledgepkg.ConfidenceMedium,
			Inferred:         len(resp.InferredEdges) > 0,
			RefinementStatus: "light",
		}}
		if strings.TrimSpace(resp.GetDetailMermaidSource()) != "" {
			sections = append(sections, knowledgepkg.Section{
				Title:            "AI Architecture Diagram Detail",
				SectionKey:       "ai_architecture_diagram_detail",
				Content:          resp.GetDetailMermaidSource(),
				Summary:          resp.GetDetailDiagramSummary(),
				Metadata:         architectureDiagramDetailMetadataJSON(resp),
				Confidence:       knowledgepkg.ConfidenceMedium,
				Inferred:         false,
				RefinementStatus: "deep",
			})
		}
		if err := r.KnowledgeStore.StoreKnowledgeSections(artifact.ID, sections); err != nil {
			return err
		}
		storedSections := r.KnowledgeStore.GetKnowledgeSections(artifact.ID)
		if len(storedSections) > 0 && len(resp.Evidence) > 0 {
			if err := r.KnowledgeStore.StoreKnowledgeEvidence(storedSections[0].ID, mapProtoEvidence(resp.Evidence)); err != nil {
				return err
			}
		}
		if len(storedSections) > 1 && len(resp.DetailEvidence) > 0 {
			if err := r.KnowledgeStore.StoreKnowledgeEvidence(storedSections[1].ID, mapProtoEvidence(resp.DetailEvidence)); err != nil {
				return err
			}
		}
		if err := r.KnowledgeStore.UpdateKnowledgeArtifactStatus(artifact.ID, knowledgepkg.StatusReady); err != nil {
			return err
		}
		rt.ReportProgress(1.0, "ready", "AI architecture diagram ready")
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("enqueue architecture diagram job: %w", err)
	}

	return mapKnowledgeArtifact(artifact), nil
}
