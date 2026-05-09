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

type workflowStoryGenerationService struct {
	resolver *Resolver
	input    GenerateWorkflowStoryInput
}

// workflowStoryRunParams bundles all inputs that runGenerationPipeline needs
// beyond the receiver. Defined here so both Generate and (eventually)
// RefreshFromExisting can build it with appropriate values.
type workflowStoryRunParams struct {
	baseRunParams
	scope             knowledgepkg.ArtifactScope
	anchorLabel       string
	executionPathJSON string
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

	params := workflowStoryRunParams{
		baseRunParams: baseRunParams{
			repo:           repo,
			artifact:       artifact,
			snap:           snap,
			snapJSON:       snapJSON,
			generationMode: generationMode,
			audience:       audience,
			depth:          depth,
		},
		scope:             scope,
		anchorLabel:       anchorLabel,
		executionPathJSON: executionPathJSON,
	}
	capturedStore := r.getStore(ctx)
	err = r.enqueueKnowledgeJob(ctx, artifact, "workflow_story", len(snapJSON), func(runCtx context.Context, rt llm.Runtime) error {
		return s.runGenerationPipeline(runCtx, rt, capturedStore, params)
	})
	if err != nil {
		return nil, fmt.Errorf("enqueue workflow story job: %w", err)
	}

	return mapKnowledgeArtifact(artifact), nil
}

// runGenerationPipeline executes the workflow-story LLM call and persistence
// steps for the given artifact. It is called from the enqueueKnowledgeJob
// closure in Generate and will be called from RefreshFromExisting once
// Phase 1 Slice 5f lands.
func (s workflowStoryGenerationService) runGenerationPipeline(
	runCtx context.Context,
	rt llm.Runtime,
	store graphstore.GraphStore,
	p workflowStoryRunParams,
) error {
	scope := p.scope
	anchorLabel := p.anchorLabel
	executionPathJSON := p.executionPathJSON
	return runKnowledgePipeline(runCtx, rt, store, s.resolver, p.baseRunParams, knowledgePipelineConfig{
		artifactLabel:          "workflow story",
		rpcBucket:              rpcBucketCollapsed,
		progressPersistMessage: "LLM completed, persisting sections",
		readyMessage:           "Workflow story ready",
		rpcFn: func(ctx context.Context, enrichedSnapJSON []byte, rt llm.Runtime, r *Resolver, base baseRunParams, onProgress func(worker.KnowledgeStreamEvent)) (any, *commonv1.LLMUsage, error) {
			resp, err := r.LLMCaller.GenerateWorkflowStoryWithJob(ctx, base.repo.ID, resolution.OpKnowledge,
				llmJobMetadataWithProgress(rt, base.artifact.ID, "workflow_story", onProgress),
				&knowledgev1.GenerateWorkflowStoryRequest{
					RepositoryId:      base.repo.ID,
					RepositoryName:    base.repo.Name,
					Audience:          base.audience,
					AudienceEnum:      protoAudience(knowledgepkg.Audience(base.audience)),
					Depth:             base.depth,
					DepthEnum:         protoDepth(knowledgepkg.Depth(base.depth)),
					ScopeType:         string(scope.ScopeType),
					ScopePath:         scope.ScopePath,
					AnchorLabel:       anchorLabel,
					ExecutionPathJson: executionPathJSON,
					SnapshotJson:      string(enrichedSnapJSON),
				})
			if err != nil {
				return nil, nil, err
			}
			return resp, resp.Usage, nil
		},
		mapSections: func(raw any) ([]knowledgepkg.Section, [][]knowledgepkg.Evidence) {
			resp := raw.(*knowledgev1.GenerateWorkflowStoryResponse)
			sections := make([]knowledgepkg.Section, len(resp.Sections))
			evidences := make([][]knowledgepkg.Evidence, len(resp.Sections))
			for i, sec := range resp.Sections {
				sections[i] = knowledgepkg.Section{
					Title:      sec.Title,
					Content:    sec.Content,
					Summary:    sec.Summary,
					Confidence: mapProtoConfidence(sec.Confidence),
					Inferred:   sec.Inferred,
				}
				if len(sec.Evidence) > 0 {
					evidences[i] = mapProtoEvidence(sec.Evidence)
				}
			}
			return sections, evidences
		},
	})
}

// RefreshFromExisting re-runs the generation pipeline against an existing
// artifact, replacing its sections in place (Phase 1 Slice 5f).
//
// audience, depth, and scope are read from the existing artifact's persisted
// fields. anchorLabel and executionPathJSON are not persisted on the artifact
// and are omitted on refresh (same as the original generation defaults).
func (s workflowStoryGenerationService) RefreshFromExisting(ctx context.Context, existing *knowledgepkg.Artifact) (*KnowledgeArtifact, error) {
	r := s.resolver

	repo := r.getStore(ctx).GetRepository(existing.RepositoryID)
	if repo == nil {
		return nil, fmt.Errorf("repository %s not found", existing.RepositoryID)
	}

	scope := knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository}
	if existing.Scope != nil {
		scope = existing.Scope.Normalize()
	}
	audience := string(existing.Audience)
	depth := string(existing.Depth)
	generationMode := existing.GenerationMode

	assembler := knowledgepkg.NewAssembler(r.getStore(ctx))
	repoRoot, repoRootErr := resolveRepoSourcePath(repo)
	if repoRootErr != nil {
		slog.Warn("workflow story refresh: repo source unavailable, docs will be omitted",
			"repo_id", repo.ID, "error", repoRootErr)
	}

	capturedStore := r.getStore(ctx)
	err := r.enqueueKnowledgeJob(ctx, existing, "refresh:workflow_story", 0, func(runCtx context.Context, rt llm.Runtime) error {
		var (
			snap *knowledgepkg.KnowledgeSnapshot
			err  error
		)
		if scope.ScopeType == knowledgepkg.ScopeRepository {
			snap, err = assembler.Assemble(repo.ID, repoRoot)
		} else {
			snap, err = assembler.AssembleScoped(repo.ID, repoRoot, scope)
		}
		if err != nil {
			slog.Error("workflow story refresh assemble failed", "artifact_id", existing.ID, "error", err)
			return err
		}
		snapJSON, err := json.Marshal(snap)
		if err != nil {
			slog.Error("workflow story refresh serialize failed", "artifact_id", existing.ID, "error", err)
			return err
		}
		rt.ReportSnapshotBytes(len(snapJSON))

		params := workflowStoryRunParams{
			baseRunParams: baseRunParams{
				repo:           repo,
				artifact:       existing,
				snap:           snap,
				snapJSON:       snapJSON,
				generationMode: generationMode,
				audience:       audience,
				depth:          depth,
			},
			scope:             scope,
			anchorLabel:       "",
			executionPathJSON: "",
		}
		return s.runGenerationPipeline(runCtx, rt, capturedStore, params)
	})
	if err != nil {
		return nil, fmt.Errorf("enqueue workflow story refresh job: %w", err)
	}

	updated := r.KnowledgeStore.GetKnowledgeArtifact(existing.ID)
	if updated == nil {
		return nil, fmt.Errorf("artifact %s not found after refresh", existing.ID)
	}
	return mapKnowledgeArtifact(updated), nil
}
