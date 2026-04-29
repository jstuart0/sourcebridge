package graphql

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
)

func (r *mutationResolver) seedRepositoryFieldGuide(repoID string) {
	if r.Worker == nil || r.KnowledgeStore == nil || r.Store == nil {
		return
	}
	repo := r.Store.GetRepository(repoID)
	if repo == nil {
		return
	}

	repoRoot, repoRootErr := resolveRepoSourcePath(repo)
	if repoRootErr != nil {
		slog.Warn("knowledge seed: repo source unavailable, docs omitted", "repo_id", repo.ID, "error", repoRootErr)
	}

	assembler := knowledgepkg.NewAssembler(r.Store)
	repoSnapshot, err := assembler.Assemble(repo.ID, repoRoot)
	if err != nil {
		slog.Warn("knowledge seed: assemble repo snapshot failed", "repo_id", repo.ID, "error", err)
		return
	}
	repoJSON, err := json.Marshal(repoSnapshot)
	if err != nil {
		slog.Warn("knowledge seed: serialize repo snapshot failed", "repo_id", repo.ID, "error", err)
		return
	}

	r.ensureKnowledgeArtifact(repo, knowledgepkg.ArtifactKey{
		RepositoryID: repo.ID,
		Type:         knowledgepkg.ArtifactCliffNotes,
		Audience:     knowledgepkg.AudienceDeveloper,
		Depth:        knowledgepkg.DepthMedium,
		Scope:        knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository},
	}, repoSnapshot.SourceRevision, string(repoJSON))

	r.ensureKnowledgeArtifact(repo, knowledgepkg.ArtifactKey{
		RepositoryID: repo.ID,
		Type:         knowledgepkg.ArtifactLearningPath,
		Audience:     knowledgepkg.AudienceDeveloper,
		Depth:        knowledgepkg.DepthMedium,
		Scope:        knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository},
	}, repoSnapshot.SourceRevision, string(repoJSON))

	r.ensureKnowledgeArtifact(repo, knowledgepkg.ArtifactKey{
		RepositoryID: repo.ID,
		Type:         knowledgepkg.ArtifactCodeTour,
		Audience:     knowledgepkg.AudienceDeveloper,
		Depth:        knowledgepkg.DepthMedium,
		Scope:        knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository},
	}, repoSnapshot.SourceRevision, string(repoJSON))

	r.ensureKnowledgeArtifact(repo, knowledgepkg.ArtifactKey{
		RepositoryID: repo.ID,
		Type:         knowledgepkg.ArtifactWorkflowStory,
		Audience:     knowledgepkg.AudienceDeveloper,
		Depth:        knowledgepkg.DepthMedium,
		Scope:        knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository},
	}, repoSnapshot.SourceRevision, string(repoJSON))

	for _, filePath := range repositoryFieldGuideSeedFiles(repoSnapshot) {
		fileScope := knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeFile, ScopePath: filePath}.Normalize()
		fileSnapshot, err := assembler.AssembleScoped(repo.ID, repoRoot, fileScope)
		if err != nil {
			slog.Warn("knowledge seed: assemble file snapshot failed", "repo_id", repo.ID, "file", filePath, "error", err)
			continue
		}
		fileJSON, err := json.Marshal(fileSnapshot)
		if err != nil {
			slog.Warn("knowledge seed: serialize file snapshot failed", "repo_id", repo.ID, "file", filePath, "error", err)
			continue
		}
		r.ensureKnowledgeArtifact(repo, knowledgepkg.ArtifactKey{
			RepositoryID: repo.ID,
			Type:         knowledgepkg.ArtifactCliffNotes,
			Audience:     knowledgepkg.AudienceDeveloper,
			Depth:        knowledgepkg.DepthMedium,
			Scope:        fileScope,
		}, fileSnapshot.SourceRevision, string(fileJSON))
	}
}

func (r *mutationResolver) ensureKnowledgeArtifact(repo *graphstore.Repository, key knowledgepkg.ArtifactKey, sourceRevision knowledgepkg.SourceRevision, snapshotJSON string) {
	key = key.Normalized()
	existing := r.KnowledgeStore.GetArtifactByKeyAndMode(key, knowledgepkg.GenerationModeUnderstandingFirst)
	if existing != nil {
		if existing.Status == knowledgepkg.StatusReady && !existing.Stale {
			return
		}
		if existing.Status == knowledgepkg.StatusGenerating || existing.Status == knowledgepkg.StatusPending {
			return
		}
		_ = r.KnowledgeStore.DeleteKnowledgeArtifact(existing.ID)
	}

	artifact, created, err := r.KnowledgeStore.ClaimArtifactWithMode(key, sourceRevision, knowledgepkg.GenerationModeUnderstandingFirst)
	if err != nil || !created {
		return
	}

	run := func(runCtx context.Context, rt llm.Runtime) error {
		snapshotBytes := []byte(snapshotJSON)
		enrichedSnapshotJSON := snapshotJSON
		rt.ReportProgress(0.1, "snapshot", "Seed snapshot assembled")
		_ = r.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(artifact.ID, 0.1, "snapshot", "Seed snapshot assembled")
		stopProgress := r.startProgressTicker(rt, artifact.ID)
		defer stopProgress()

		jm := llmJobMetadata(rt, artifact.ID, string(key.Type))
		switch key.Type {
		case knowledgepkg.ArtifactCliffNotes:
			resp, err := r.LLMCaller.GenerateCliffNotesWithJob(runCtx, repo.ID, resolution.OpKnowledge, jm, &knowledgev1.GenerateCliffNotesRequest{
				RepositoryId:   repo.ID,
				RepositoryName: repo.Name,
				Audience:       string(key.Audience),
				AudienceEnum:   protoAudience(key.Audience),
				Depth:          string(key.Depth),
				DepthEnum:      protoDepth(key.Depth),
				ScopeType:      string(key.Scope.ScopeType),
				ScopePath:      key.Scope.ScopePath,
				SnapshotJson:   snapshotJSON,
			})
			if err != nil {
				return err
			}
			if _, err := updateUnderstandingForCliffNotes(r.KnowledgeStore, artifact, key.Scope, sourceRevision, resp, knowledgepkg.UnderstandingFirstPassReady); err != nil {
				slog.Warn("failed to update repository understanding from seed cliff notes", "artifact_id", artifact.ID, "error", err)
			}
			rt.ReportProgress(0.96, "llm", "Seed LLM completed, persisting")
			sections := make([]knowledgepkg.Section, len(resp.Sections))
			for i, sec := range resp.Sections {
				sections[i] = knowledgepkg.Section{
					Title:      sec.Title,
					Content:    sec.Content,
					Summary:    sec.Summary,
					Confidence: mapProtoConfidence(sec.Confidence),
					Inferred:   sec.Inferred,
					Evidence:   mapProtoEvidence(sec.Evidence),
				}
			}
			if err := r.KnowledgeStore.SupersedeArtifact(artifact.ID, sections); err != nil {
				return err
			}
		case knowledgepkg.ArtifactLearningPath:
			if understanding, reused, err := r.ensureFreshRepositoryUnderstanding(runCtx, rt, repo, artifact, sourceRevision, snapshotBytes); err != nil {
				return err
			} else {
				if reused {
					rt.ReportProgress(0.12, "understanding", "Using cached repository understanding")
					_ = r.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(artifact.ID, 0.12, "understanding", "Using cached repository understanding")
				}
				if understanding != nil {
					if enriched, ok := enrichSnapshotWithUnderstanding(snapshotBytes, understanding); ok {
						enrichedSnapshotJSON = string(enriched)
					}
				}
			}
			resp, err := r.LLMCaller.GenerateLearningPathWithJob(runCtx, repo.ID, resolution.OpKnowledge, jm, &knowledgev1.GenerateLearningPathRequest{
				RepositoryId:   repo.ID,
				RepositoryName: repo.Name,
				Audience:       string(key.Audience),
				AudienceEnum:   protoAudience(key.Audience),
				Depth:          string(key.Depth),
				DepthEnum:      protoDepth(key.Depth),
				SnapshotJson:   enrichedSnapshotJSON,
			})
			if err != nil {
				return err
			}
			rt.ReportProgress(0.96, "llm", "Seed LLM completed, persisting")
			sections := make([]knowledgepkg.Section, len(resp.Steps))
			for i, step := range resp.Steps {
				sections[i] = knowledgepkg.Section{
					Title:      step.Title,
					Content:    step.Content,
					Summary:    step.Objective,
					Confidence: knowledgepkg.ConfidenceMedium,
				}
			}
			if err := r.KnowledgeStore.SupersedeArtifact(artifact.ID, sections); err != nil {
				return err
			}
		case knowledgepkg.ArtifactCodeTour:
			if understanding, reused, err := r.ensureFreshRepositoryUnderstanding(runCtx, rt, repo, artifact, sourceRevision, snapshotBytes); err != nil {
				return err
			} else {
				if reused {
					rt.ReportProgress(0.12, "understanding", "Using cached repository understanding")
					_ = r.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(artifact.ID, 0.12, "understanding", "Using cached repository understanding")
				}
				if understanding != nil {
					if enriched, ok := enrichSnapshotWithUnderstanding(snapshotBytes, understanding); ok {
						enrichedSnapshotJSON = string(enriched)
					}
				}
			}
			resp, err := r.LLMCaller.GenerateCodeTourWithJob(runCtx, repo.ID, resolution.OpKnowledge, jm, &knowledgev1.GenerateCodeTourRequest{
				RepositoryId:   repo.ID,
				RepositoryName: repo.Name,
				Audience:       string(key.Audience),
				AudienceEnum:   protoAudience(key.Audience),
				Depth:          string(key.Depth),
				DepthEnum:      protoDepth(key.Depth),
				SnapshotJson:   enrichedSnapshotJSON,
			})
			if err != nil {
				return err
			}
			rt.ReportProgress(0.96, "llm", "Seed LLM completed, persisting")
			sections := make([]knowledgepkg.Section, len(resp.Stops))
			for i, stop := range resp.Stops {
				sections[i] = knowledgepkg.Section{
					Title:      stop.Title,
					Content:    stop.Description,
					Summary:    stop.FilePath,
					Confidence: knowledgepkg.ConfidenceMedium,
					Evidence: []knowledgepkg.Evidence{{
						SourceType: knowledgepkg.EvidenceFile,
						FilePath:   stop.FilePath,
						LineStart:  int(stop.LineStart),
						LineEnd:    int(stop.LineEnd),
					}},
				}
			}
			if err := r.KnowledgeStore.SupersedeArtifact(artifact.ID, sections); err != nil {
				return err
			}
		case knowledgepkg.ArtifactWorkflowStory:
			if understanding, reused, err := r.ensureFreshRepositoryUnderstanding(runCtx, rt, repo, artifact, sourceRevision, snapshotBytes); err != nil {
				return err
			} else {
				if reused {
					rt.ReportProgress(0.12, "understanding", "Using cached repository understanding")
					_ = r.KnowledgeStore.UpdateKnowledgeArtifactProgressWithPhase(artifact.ID, 0.12, "understanding", "Using cached repository understanding")
				}
				if understanding != nil {
					if enriched, ok := enrichSnapshotWithUnderstanding(snapshotBytes, understanding); ok {
						enrichedSnapshotJSON = string(enriched)
					}
				}
			}
			resp, err := r.LLMCaller.GenerateWorkflowStoryWithJob(runCtx, repo.ID, resolution.OpKnowledge, jm, &knowledgev1.GenerateWorkflowStoryRequest{
				RepositoryId:   repo.ID,
				RepositoryName: repo.Name,
				Audience:       string(key.Audience),
				AudienceEnum:   protoAudience(key.Audience),
				Depth:          string(key.Depth),
				DepthEnum:      protoDepth(key.Depth),
				ScopeType:      string(key.Scope.ScopeType),
				ScopePath:      key.Scope.ScopePath,
				SnapshotJson:   enrichedSnapshotJSON,
			})
			if err != nil {
				return err
			}
			rt.ReportProgress(0.96, "llm", "Seed LLM completed, persisting")
			sections := make([]knowledgepkg.Section, len(resp.Sections))
			for i, sec := range resp.Sections {
				sections[i] = knowledgepkg.Section{
					Title:      sec.Title,
					Content:    sec.Content,
					Summary:    sec.Summary,
					Confidence: mapProtoConfidence(sec.Confidence),
					Inferred:   sec.Inferred,
					Evidence:   mapProtoEvidence(sec.Evidence),
				}
			}
			if err := r.KnowledgeStore.SupersedeArtifact(artifact.ID, sections); err != nil {
				return err
			}
		default:
			return nil
		}
		rt.ReportProgress(1.0, "ready", "Seed artifact ready")
		return nil
	}

	jobType := "seed:" + string(key.Type)
	if r.Orchestrator == nil {
		if err := run(context.Background(), noopRuntime{}); err != nil {
			persistArtifactFailure(r.KnowledgeStore, artifact.ID, err)
		}
		return
	}
	if err := r.enqueueKnowledgeJob(artifact, jobType, len(snapshotJSON), run); err != nil {
		persistArtifactFailure(r.KnowledgeStore, artifact.ID, err)
	}
}

func repositoryFieldGuideSeedFiles(snapshot *knowledgepkg.KnowledgeSnapshot) []string {
	if snapshot == nil {
		return nil
	}
	seen := map[string]bool{}
	files := make([]string, 0, 5)
	add := func(path string) {
		if path == "" || seen[path] || strings.HasSuffix(path, "_test.go") {
			return
		}
		seen[path] = true
		files = append(files, path)
	}

	for _, ref := range snapshot.EntryPoints {
		add(ref.FilePath)
	}
	for _, ref := range snapshot.PublicAPI {
		if len(files) >= 5 {
			break
		}
		add(ref.FilePath)
	}
	for _, ref := range snapshot.HighFanOutSymbols {
		if len(files) >= 5 {
			break
		}
		add(ref.FilePath)
	}
	for _, ref := range snapshot.ComplexSymbols {
		if len(files) >= 5 {
			break
		}
		add(ref.FilePath)
	}

	if len(files) > 5 {
		return files[:5]
	}
	return files
}
