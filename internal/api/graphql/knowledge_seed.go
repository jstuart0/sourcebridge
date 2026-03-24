package graphql

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
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
	existing := r.KnowledgeStore.GetArtifactByKey(key)
	if existing != nil {
		if existing.Status == knowledgepkg.StatusReady && !existing.Stale {
			return
		}
		if existing.Status == knowledgepkg.StatusGenerating || existing.Status == knowledgepkg.StatusPending {
			return
		}
		_ = r.KnowledgeStore.DeleteKnowledgeArtifact(existing.ID)
	}

	artifact, created, err := r.KnowledgeStore.ClaimArtifact(key, sourceRevision)
	if err != nil || !created {
		return
	}

	switch key.Type {
	case knowledgepkg.ArtifactCliffNotes:
		resp, err := r.Worker.GenerateCliffNotes(context.Background(), &knowledgev1.GenerateCliffNotesRequest{
			RepositoryId:   repo.ID,
			RepositoryName: repo.Name,
			Audience:       string(key.Audience),
			Depth:          string(key.Depth),
			ScopeType:      string(key.Scope.ScopeType),
			ScopePath:      key.Scope.ScopePath,
			SnapshotJson:   snapshotJSON,
		})
		if err != nil {
			_ = r.KnowledgeStore.UpdateKnowledgeArtifactStatus(artifact.ID, knowledgepkg.StatusFailed)
			return
		}
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
			_ = r.KnowledgeStore.UpdateKnowledgeArtifactStatus(artifact.ID, knowledgepkg.StatusFailed)
		}
	case knowledgepkg.ArtifactLearningPath:
		resp, err := r.Worker.GenerateLearningPath(context.Background(), &knowledgev1.GenerateLearningPathRequest{
			RepositoryId:   repo.ID,
			RepositoryName: repo.Name,
			Audience:       string(key.Audience),
			Depth:          string(key.Depth),
			SnapshotJson:   snapshotJSON,
		})
		if err != nil {
			_ = r.KnowledgeStore.UpdateKnowledgeArtifactStatus(artifact.ID, knowledgepkg.StatusFailed)
			return
		}
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
			_ = r.KnowledgeStore.UpdateKnowledgeArtifactStatus(artifact.ID, knowledgepkg.StatusFailed)
		}
	case knowledgepkg.ArtifactCodeTour:
		resp, err := r.Worker.GenerateCodeTour(context.Background(), &knowledgev1.GenerateCodeTourRequest{
			RepositoryId:   repo.ID,
			RepositoryName: repo.Name,
			Audience:       string(key.Audience),
			Depth:          string(key.Depth),
			SnapshotJson:   snapshotJSON,
		})
		if err != nil {
			_ = r.KnowledgeStore.UpdateKnowledgeArtifactStatus(artifact.ID, knowledgepkg.StatusFailed)
			return
		}
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
			_ = r.KnowledgeStore.UpdateKnowledgeArtifactStatus(artifact.ID, knowledgepkg.StatusFailed)
		}
	case knowledgepkg.ArtifactWorkflowStory:
		resp, err := r.Worker.GenerateWorkflowStory(context.Background(), &knowledgev1.GenerateWorkflowStoryRequest{
			RepositoryId:   repo.ID,
			RepositoryName: repo.Name,
			Audience:       string(key.Audience),
			Depth:          string(key.Depth),
			ScopeType:      string(key.Scope.ScopeType),
			ScopePath:      key.Scope.ScopePath,
			SnapshotJson:   snapshotJSON,
		})
		if err != nil {
			_ = r.KnowledgeStore.UpdateKnowledgeArtifactStatus(artifact.ID, knowledgepkg.StatusFailed)
			return
		}
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
			_ = r.KnowledgeStore.UpdateKnowledgeArtifactStatus(artifact.ID, knowledgepkg.StatusFailed)
		}
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
