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

type cliffNotesGenerationService struct {
	resolver *Resolver
	input    GenerateCliffNotesInput
}

func (s cliffNotesGenerationService) Generate(ctx context.Context) (*KnowledgeArtifact, error) {
	r := s.resolver
	input := s.input

	if err := r.requireKnowledgeGenerationSupport(); err != nil {
		return nil, err
	}

	repo, err := r.loadKnowledgeRepository(ctx, input.RepositoryID)
	if err != nil {
		return nil, err
	}
	key, err := artifactKeyFromCliffNotesInput(input)
	if err != nil {
		return nil, err
	}
	generationMode := resolvedKnowledgeGenerationMode(r.ComprehensionStore, repo, input.GenerationMode)
	audience := string(key.Audience)
	depth := string(key.Depth)
	scope := key.Scope.Normalize()

	if scope.ScopeType == knowledgepkg.ScopeRequirement {
		if req := r.getStore(ctx).GetRequirement(scope.ScopePath); req != nil {
			scope.SymbolName = req.ExternalID
			if scope.SymbolName == "" {
				scope.SymbolName = req.Title
			}
			key.Scope = scope
		}
	}

	existing := r.KnowledgeStore.GetArtifactByKeyAndMode(key, generationMode)
	if existing != nil {
		if shouldRefreshScopedCliffNotes(existing) {
			_ = r.KnowledgeStore.DeleteKnowledgeArtifact(existing.ID)
			existing = nil
		}
	}
	if existing != nil {
		existing.GenerationMode = generationMode
		syncArtifactExecutionMetadata(r.KnowledgeStore, existing)
		scopeUnderstanding := (*knowledgepkg.RepositoryUnderstanding)(nil)
		if artifactUsesUnderstanding(generationMode) {
			scopeUnderstanding, _ = attachFreshUnderstanding(r.KnowledgeStore, existing, scope, knowledgepkg.SourceRevision{})
		}
		renderPlan := cliffNotesRenderPlanForArtifact(r.KnowledgeStore, existing, knowledgepkg.SourceRevision{}, scopeUnderstanding)
		if existing.Status == knowledgepkg.StatusReady && !existing.Stale && !renderPlan.RenderOnly {
			return mapKnowledgeArtifact(existing), nil
		}
		if isInFlightGeneration(existing) {
			slog.Info("cliff_notes_generation_deduped",
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
		slog.Warn("cliff notes: repo source unavailable, docs will be omitted from snapshot",
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
	understanding := (*knowledgepkg.RepositoryUnderstanding)(nil)
	reusedUnderstanding := false
	renderPlan := cliffNotesRenderPlan{}
	if artifactUsesUnderstanding(generationMode) {
		understanding, reusedUnderstanding = attachFreshUnderstanding(r.KnowledgeStore, artifact, scope, snap.SourceRevision)
		if understanding == nil {
			if _, err := seedRepositoryUnderstanding(r.KnowledgeStore, artifact, scope, snap.SourceRevision, knowledgepkg.UnderstandingBuildingTree); err != nil {
				slog.Warn("failed to seed repository understanding", "artifact_id", artifact.ID, "error", err)
			}
		} else {
			renderPlan = cliffNotesRenderPlanForArtifact(r.KnowledgeStore, artifact, snap.SourceRevision, understanding)
		}
	}

	store := r.getStore(ctx)
	snapshotSizeBytes := len(snapJSON)
	truncated := scope.ScopeType == knowledgepkg.ScopeRequirement && snap.SymbolCount >= 200
	clientType := clientTypeFromContext(ctx)
	slog.Info("cliff_notes_generation_started",
		"artifact_id", artifact.ID,
		"scope_type", string(scope.ScopeType),
		"scope_path", scope.ScopePath,
		"client_type", clientType,
		"linked_symbol_count", snap.SymbolCount,
		"snapshot_size_bytes", snapshotSizeBytes,
		"truncated", truncated,
	)

	enrichedCliffSnapJSON := snapJSON
	if depth == "deep" && scope.ScopeType != knowledgepkg.ScopeRepository && r.KnowledgeStore != nil {
		repoCliffKey := knowledgepkg.ArtifactKey{
			RepositoryID: repo.ID,
			Type:         knowledgepkg.ArtifactCliffNotes,
			Audience:     knowledgepkg.Audience(audience),
			Depth:        knowledgepkg.DepthMedium,
			Scope:        knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository},
		}.Normalized()
		if repoCliff := r.KnowledgeStore.GetArtifactByKey(repoCliffKey); repoCliff != nil && repoCliff.Status == knowledgepkg.StatusReady {
			sections := r.KnowledgeStore.GetKnowledgeSections(repoCliff.ID)
			if len(sections) > 0 {
				var analysis []map[string]string
				for _, sec := range sections {
					analysis = append(analysis, map[string]string{
						"title":   sec.Title,
						"content": sec.Content,
						"summary": sec.Summary,
					})
				}
				var snapMap map[string]interface{}
				if err := json.Unmarshal(snapJSON, &snapMap); err == nil {
					snapMap["_pre_analysis"] = analysis
					if enriched, err := json.Marshal(snapMap); err == nil {
						enrichedCliffSnapJSON = enriched
						slog.Info("cliff_notes_deep_enriched",
							"artifact_id", artifact.ID,
							"repo_cliff_sections", len(sections))
					}
				}
			}
		}
	}

	err = r.enqueueKnowledgeJob(artifact, "cliff_notes", len(enrichedCliffSnapJSON), func(runCtx context.Context, rt llm.Runtime) (runErr error) {
		defer func() {
			if runErr != nil {
				markRepositoryUnderstandingFailed(r.KnowledgeStore, artifact, scope, snap.SourceRevision, runErr)
			}
		}()
		genStart := time.Now()
		rt.ReportProgress(0.1, "snapshot", "Snapshot assembled")
		_ = r.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(artifact.ID, 0.1, "snapshot", "Snapshot assembled")
		if reusedUnderstanding {
			rt.ReportProgress(0.12, "understanding", "Using cached repository understanding")
			_ = r.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(artifact.ID, 0.12, "understanding", "Using cached repository understanding")
		}
		appendJobLog(r.Orchestrator, rt, llm.LogLevelInfo, "snapshot", "snapshot_assembled", "Snapshot assembled", map[string]any{
			"snapshot_bytes": len(enrichedCliffSnapJSON),
			"scope_type":     string(scope.ScopeType),
			"scope_path":     scope.ScopePath,
			"depth":          depth,
			"audience":       audience,
		})

		stopProgress := r.startProgressTicker(rt, artifact.ID)
		bgCtx := runCtx
		if renderPlan.RenderOnly {
			bgCtx = withCliffNotesRenderMetadata(
				bgCtx,
				true,
				renderPlan.SelectedSectionTitles,
				renderPlan.UnderstandingDepth,
				renderPlan.RelevanceProfile,
			)
		}
		appendJobLog(r.Orchestrator, rt, llm.LogLevelInfo, "llm", "worker_dispatch", "Dispatching cliff notes request to worker", map[string]any{
			"repository_id":   repo.ID,
			"repository_name": repo.Name,
			"scope_type":      string(scope.ScopeType),
			"scope_path":      scope.ScopePath,
			"depth":           depth,
			"generation_mode": string(generationMode),
			"render_only":     renderPlan.RenderOnly,
			"selected_titles": renderPlan.SelectedSectionTitles,
		})
		resp, err := r.LLMCaller.GenerateCliffNotesWithJob(bgCtx, repo.ID, resolution.OpKnowledge, llmJobMetadata(rt, artifact.ID, "cliff_notes"), &knowledgev1.GenerateCliffNotesRequest{
			RepositoryId:   repo.ID,
			RepositoryName: repo.Name,
			Audience:       audience,
			AudienceEnum:   protoAudience(knowledgepkg.Audience(audience)),
			Depth:          depth,
			DepthEnum:      protoDepth(knowledgepkg.Depth(depth)),
			ScopeType:      string(scope.ScopeType),
			ScopePath:      scope.ScopePath,
			SnapshotJson:   string(enrichedCliffSnapJSON),
		})
		stopProgress()
		if err != nil {
			appendJobLog(r.Orchestrator, rt, llm.LogLevelError, "llm", "worker_failed", "Worker cliff notes request failed", map[string]any{
				"duration_ms": time.Since(genStart).Milliseconds(),
			})
			slog.Error("cliff_notes_generation_failed",
				"artifact_id", artifact.ID,
				"scope_type", string(scope.ScopeType),
				"scope_path", scope.ScopePath,
				"client_type", clientType,
				"duration_ms", time.Since(genStart).Milliseconds(),
				"error", err,
			)
			return err
		}
		understanding, err := updateUnderstandingForCliffNotes(r.KnowledgeStore, artifact, scope, snap.SourceRevision, resp, knowledgepkg.UnderstandingReady)
		if err != nil {
			slog.Warn("failed to update repository understanding", "artifact_id", artifact.ID, "error", err)
		}

		reusedSummaries := 0
		leafCacheHits := 0
		fileCacheHits := 0
		packageCacheHits := 0
		rootCacheHits := 0
		if resp.Diagnostics != nil {
			leafCacheHits = int(resp.Diagnostics.LeafCacheHits)
			fileCacheHits = int(resp.Diagnostics.FileCacheHits)
			packageCacheHits = int(resp.Diagnostics.PackageCacheHits)
			rootCacheHits = int(resp.Diagnostics.RootCacheHits)
			reusedSummaries = leafCacheHits + fileCacheHits + packageCacheHits + rootCacheHits
			if err := r.Orchestrator.SetReuseStats(rt.JobID(), reusedSummaries, leafCacheHits, fileCacheHits, packageCacheHits, rootCacheHits); err != nil {
				slog.Warn("failed to persist cliff notes reuse stats",
					"job_id", rt.JobID(),
					"artifact_id", artifact.ID,
					"error", err)
			}
		}
		appendJobLog(r.Orchestrator, rt, llm.LogLevelInfo, "llm", "worker_response_received", "Worker returned cliff notes response", map[string]any{
			"duration_ms":        time.Since(genStart).Milliseconds(),
			"section_count":      len(resp.Sections),
			"reused_summaries":   reusedSummaries,
			"leaf_cache_hits":    leafCacheHits,
			"file_cache_hits":    fileCacheHits,
			"package_cache_hits": packageCacheHits,
			"root_cache_hits":    rootCacheHits,
			"provider_compute_errors": func() int32 {
				if resp.Diagnostics == nil {
					return 0
				}
				return resp.Diagnostics.ProviderComputeErrors
			}(),
			"fallback_count": func() int32 {
				if resp.Diagnostics == nil {
					return 0
				}
				return resp.Diagnostics.FallbackCount
			}(),
		})
		llmMessage := "LLM completed, persisting sections"
		if reusedSummaries > 0 {
			llmMessage = fmt.Sprintf("LLM completed, reused %d summaries, persisting sections", reusedSummaries)
		}
		rt.ReportProgress(0.96, "llm", llmMessage)
		_ = r.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(artifact.ID, 0.8, "llm", llmMessage)

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
		appendJobLog(r.Orchestrator, rt, llm.LogLevelInfo, "persist", "persist_sections_started", "Persisting generated cliff note sections", map[string]any{
			"section_count": len(resp.Sections),
		})

		sections := make([]knowledgepkg.Section, len(resp.Sections))
		for i, sec := range resp.Sections {
			refinementStatus := strings.TrimSpace(sec.RefinementStatus)
			if refinementStatus == "" {
				refinementStatus = "light"
			}
			sections[i] = knowledgepkg.Section{
				Title:            sec.Title,
				Content:          sec.Content,
				Summary:          sec.Summary,
				Metadata:         cliffNotesSectionMetadataJSON(knowledgepkg.ArtifactCliffNotes, understanding, refinementStatus, sec.Title, len(sec.Evidence) > 0),
				Confidence:       mapProtoConfidence(sec.Confidence),
				Inferred:         sec.Inferred,
				SectionKey:       knowledgepkg.SectionKeyForTitle(sec.Title),
				RefinementStatus: refinementStatus,
			}
		}
		if renderPlan.RenderOnly && len(renderPlan.SelectedSectionTitles) > 0 {
			selected := make(map[string]struct{}, len(renderPlan.SelectedSectionTitles))
			for _, title := range renderPlan.SelectedSectionTitles {
				selected[title] = struct{}{}
			}
			sections = knowledgepkg.MergeSectionsByTitle(r.KnowledgeStore.GetKnowledgeSections(artifact.ID), sections, selected)
		}
		if err := r.KnowledgeStore.StoreKnowledgeSections(artifact.ID, sections); err != nil {
			slog.Error("failed to store cliff notes sections", "artifact_id", artifact.ID, "error", err)
			return err
		}
		syncCliffNotesRefinementUnits(r.KnowledgeStore, artifact, sections, understanding)
		appendJobLog(r.Orchestrator, rt, llm.LogLevelInfo, "persist", "persist_sections_completed", "Stored cliff note sections", map[string]any{
			"section_count": len(sections),
		})

		storedSections := r.KnowledgeStore.GetKnowledgeSections(artifact.ID)
		for i, sec := range resp.Sections {
			if i >= len(storedSections) {
				break
			}
			if len(sec.Evidence) > 0 {
				evidence := make([]knowledgepkg.Evidence, len(sec.Evidence))
				for j, ev := range sec.Evidence {
					evidence[j] = knowledgepkg.Evidence{
						SourceType: knowledgepkg.EvidenceSourceType(ev.SourceType),
						SourceID:   ev.SourceId,
						FilePath:   ev.FilePath,
						LineStart:  int(ev.LineStart),
						LineEnd:    int(ev.LineEnd),
						Rationale:  ev.Rationale,
					}
				}
				_ = r.KnowledgeStore.StoreKnowledgeEvidence(storedSections[i].ID, evidence)
			}
		}

		if err := r.KnowledgeStore.UpdateKnowledgeArtifactStatus(artifact.ID, knowledgepkg.StatusReady); err != nil {
			slog.Error("failed to mark cliff notes ready", "artifact_id", artifact.ID, "error", err)
		}
		if artifactUsesUnderstanding(generationMode) && depth != string(knowledgepkg.DepthSummary) {
			targets := cliffNotesDeepeningTargets(r.KnowledgeStore, artifact)
			slog.Info("cliff_notes_deepening_targets_evaluated",
				"artifact_id", artifact.ID,
				"target_count", len(targets),
				"targets", targets,
			)
			if len(targets) > 0 {
				if err := r.enqueueCliffNotesDeepening(repo, artifact, scope, snap.SourceRevision, enrichedCliffSnapJSON, targets); err != nil {
					slog.Warn("failed to enqueue cliff notes deepening", "artifact_id", artifact.ID, "error", err)
				}
			}
		}
		appendJobLog(r.Orchestrator, rt, llm.LogLevelInfo, "ready", "artifact_ready", "Cliff notes artifact marked ready", map[string]any{
			"section_count": len(sections),
		})
		readyMessage := "Cliff notes ready"
		if reusedSummaries > 0 {
			readyMessage = fmt.Sprintf("Cliff notes ready · reused %d summaries", reusedSummaries)
		}
		rt.ReportProgress(1.0, "ready", readyMessage)
		slog.Info("cliff_notes_generation_completed",
			"artifact_id", artifact.ID,
			"scope_type", string(scope.ScopeType),
			"scope_path", scope.ScopePath,
			"client_type", clientType,
			"duration_ms", time.Since(genStart).Milliseconds(),
			"snapshot_size_bytes", snapshotSizeBytes,
			"section_count", len(resp.Sections),
			"reused_summaries", reusedSummaries,
		)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("enqueue cliff notes job: %w", err)
	}

	return mapKnowledgeArtifact(artifact), nil
}
