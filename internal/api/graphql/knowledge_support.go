package graphql

import (
	"fmt"
	"path"
	"sort"
	"strings"

	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
)

func understandingScopeForArtifact(scope knowledgepkg.ArtifactScope) knowledgepkg.ArtifactScope {
	return scope.Normalize()
}

func updateUnderstandingForCliffNotes(
	store knowledgepkg.KnowledgeStore,
	artifact *knowledgepkg.Artifact,
	scope knowledgepkg.ArtifactScope,
	sourceRevision knowledgepkg.SourceRevision,
	resp *knowledgev1.GenerateCliffNotesResponse,
	stage knowledgepkg.RepositoryUnderstandingStage,
) (*knowledgepkg.RepositoryUnderstanding, error) {
	if store == nil || resp == nil {
		return nil, nil
	}
	if artifact == nil {
		return nil, fmt.Errorf("artifact is required")
	}
	understanding := &knowledgepkg.RepositoryUnderstanding{
		RepositoryID: artifact.RepositoryID,
		Scope:        understandingScopeForArtifact(scope).NormalizePtr(),
		RevisionFP:   knowledgepkg.RevisionFingerprint(sourceRevision),
		Stage:        stage,
		TreeStatus:   knowledgepkg.UnderstandingTreeMissing,
	}
	if resp.Diagnostics != nil {
		understanding.CorpusID = resp.Diagnostics.CorpusId
		if resp.Diagnostics.RevisionFp != "" {
			understanding.RevisionFP = resp.Diagnostics.RevisionFp
		}
		understanding.Strategy = resp.Diagnostics.Strategy
		understanding.CachedNodes = int(resp.Diagnostics.CachedNodes)
		understanding.TotalNodes = int(resp.Diagnostics.TotalNodes)
		understanding.ModelUsed = resp.Diagnostics.ModelUsed
		switch {
		case resp.Diagnostics.TotalNodes > 0:
			understanding.TreeStatus = knowledgepkg.UnderstandingTreeComplete
		case resp.Diagnostics.CachedNodes > 0:
			understanding.TreeStatus = knowledgepkg.UnderstandingTreePartial
		}
	}
	if understanding.Strategy == "" {
		understanding.Strategy = "hierarchical"
	}
	if understanding.ModelUsed == "" && resp.Usage != nil {
		understanding.ModelUsed = resp.Usage.Model
	}
	stored, err := store.StoreRepositoryUnderstanding(understanding)
	if err != nil {
		return nil, err
	}
	if stored != nil && artifact.ID != "" {
		_ = store.AttachArtifactUnderstanding(artifact.ID, stored.ID, stored.RevisionFP)
	}
	return stored, nil
}

func seedRepositoryUnderstanding(
	store knowledgepkg.KnowledgeStore,
	artifact *knowledgepkg.Artifact,
	scope knowledgepkg.ArtifactScope,
	sourceRevision knowledgepkg.SourceRevision,
	stage knowledgepkg.RepositoryUnderstandingStage,
) (*knowledgepkg.RepositoryUnderstanding, error) {
	if store == nil || artifact == nil {
		return nil, nil
	}
	u := &knowledgepkg.RepositoryUnderstanding{
		RepositoryID: artifact.RepositoryID,
		Scope:        understandingScopeForArtifact(scope).NormalizePtr(),
		RevisionFP:   knowledgepkg.RevisionFingerprint(sourceRevision),
		Stage:        stage,
		TreeStatus:   knowledgepkg.UnderstandingTreeMissing,
	}
	stored, err := store.StoreRepositoryUnderstanding(u)
	if err != nil {
		return nil, err
	}
	if stored != nil {
		_ = store.AttachArtifactUnderstanding(artifact.ID, stored.ID, stored.RevisionFP)
	}
	return stored, nil
}

func markRepositoryUnderstandingFailed(
	store knowledgepkg.KnowledgeStore,
	artifact *knowledgepkg.Artifact,
	scope knowledgepkg.ArtifactScope,
	sourceRevision knowledgepkg.SourceRevision,
	err error,
) {
	if store == nil || artifact == nil {
		return
	}
	u := &knowledgepkg.RepositoryUnderstanding{
		RepositoryID: artifact.RepositoryID,
		Scope:        understandingScopeForArtifact(scope).NormalizePtr(),
		RevisionFP:   knowledgepkg.RevisionFingerprint(sourceRevision),
		Stage:        knowledgepkg.UnderstandingFailed,
		TreeStatus:   knowledgepkg.UnderstandingTreePartial,
	}
	if err != nil {
		u.ErrorMessage = err.Error()
	}
	stored, storeErr := store.StoreRepositoryUnderstanding(u)
	if storeErr == nil && stored != nil {
		_ = store.AttachArtifactUnderstanding(artifact.ID, stored.ID, stored.RevisionFP)
	}
}

func attachFreshUnderstanding(
	store knowledgepkg.KnowledgeStore,
	artifact *knowledgepkg.Artifact,
	scope knowledgepkg.ArtifactScope,
	sourceRevision knowledgepkg.SourceRevision,
) (*knowledgepkg.RepositoryUnderstanding, bool) {
	if store == nil || artifact == nil {
		return nil, false
	}
	u := store.GetRepositoryUnderstanding(artifact.RepositoryID, understandingScopeForArtifact(scope))
	if u == nil {
		return nil, false
	}
	revisionFP := knowledgepkg.RevisionFingerprint(sourceRevision)
	if revisionFP == "" || u.RevisionFP == "" || revisionFP != u.RevisionFP {
		return u, false
	}
	_ = store.AttachArtifactUnderstanding(artifact.ID, u.ID, u.RevisionFP)
	return u, u.TreeStatus == knowledgepkg.UnderstandingTreeComplete
}

func knowledgeAudienceValue(audience *KnowledgeAudience) knowledgepkg.Audience {
	if audience == nil {
		return knowledgepkg.AudienceDeveloper
	}
	return knowledgepkg.Audience(strings.ToLower(string(*audience)))
}

func knowledgeDepthValue(depth *KnowledgeDepth) knowledgepkg.Depth {
	if depth == nil {
		return knowledgepkg.DepthMedium
	}
	return knowledgepkg.Depth(strings.ToLower(string(*depth)))
}

func artifactScopeFromInput(scopeType *KnowledgeScopeType, scopePath *string) (knowledgepkg.ArtifactScope, error) {
	scope := knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository}
	if scopeType != nil {
		switch *scopeType {
		case KnowledgeScopeTypeModule:
			scope.ScopeType = knowledgepkg.ScopeModule
		case KnowledgeScopeTypeFile:
			scope.ScopeType = knowledgepkg.ScopeFile
		case KnowledgeScopeTypeSymbol:
			scope.ScopeType = knowledgepkg.ScopeSymbol
		case KnowledgeScopeTypeRequirement:
			scope.ScopeType = knowledgepkg.ScopeRequirement
		default:
			scope.ScopeType = knowledgepkg.ScopeRepository
		}
	}
	if scopePath != nil {
		scope.ScopePath = *scopePath
	}
	scope = scope.Normalize()
	if scope.ScopeType != knowledgepkg.ScopeRepository && scope.ScopePath == "" {
		return knowledgepkg.ArtifactScope{}, fmt.Errorf("scopePath is required for non-repository scopes")
	}
	return scope, nil
}

func artifactKeyFromCliffNotesInput(input GenerateCliffNotesInput) (knowledgepkg.ArtifactKey, error) {
	scope, err := artifactScopeFromInput(input.ScopeType, input.ScopePath)
	if err != nil {
		return knowledgepkg.ArtifactKey{}, err
	}
	return knowledgepkg.ArtifactKey{
		RepositoryID: input.RepositoryID,
		Type:         knowledgepkg.ArtifactCliffNotes,
		Audience:     knowledgeAudienceValue(input.Audience),
		Depth:        knowledgeDepthValue(input.Depth),
		Scope:        scope,
	}.Normalized(), nil
}

func artifactKeyFromWorkflowStoryInput(input GenerateWorkflowStoryInput) (knowledgepkg.ArtifactKey, error) {
	scope, err := artifactScopeFromInput(input.ScopeType, input.ScopePath)
	if err != nil {
		return knowledgepkg.ArtifactKey{}, err
	}
	return knowledgepkg.ArtifactKey{
		RepositoryID: input.RepositoryID,
		Type:         knowledgepkg.ArtifactWorkflowStory,
		Audience:     knowledgeAudienceValue(input.Audience),
		Depth:        knowledgeDepthValue(input.Depth),
		Scope:        scope,
	}.Normalized(), nil
}

func artifactKeyForStoredArtifact(artifact *knowledgepkg.Artifact) knowledgepkg.ArtifactKey {
	scope := knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository}
	if artifact.Scope != nil {
		scope = artifact.Scope.Normalize()
	}
	return knowledgepkg.ArtifactKey{
		RepositoryID: artifact.RepositoryID,
		Type:         artifact.Type,
		Audience:     artifact.Audience,
		Depth:        artifact.Depth,
		Scope:        scope,
	}.Normalized()
}

func scopeTypeToGraph(scopeType KnowledgeScopeType) knowledgepkg.ScopeType {
	switch scopeType {
	case KnowledgeScopeTypeModule:
		return knowledgepkg.ScopeModule
	case KnowledgeScopeTypeFile:
		return knowledgepkg.ScopeFile
	case KnowledgeScopeTypeSymbol:
		return knowledgepkg.ScopeSymbol
	case KnowledgeScopeTypeRequirement:
		return knowledgepkg.ScopeRequirement
	default:
		return knowledgepkg.ScopeRepository
	}
}

func buildScopeChildren(store graphstore.GraphStore, repoID string, scope knowledgepkg.ArtifactScope) []knowledgepkg.ArtifactScope {
	scope = scope.Normalize()
	switch scope.ScopeType {
	case knowledgepkg.ScopeRepository:
		return topLevelModuleScopes(store, repoID)
	case knowledgepkg.ScopeModule:
		return moduleChildScopes(store, repoID, scope.ScopePath)
	case knowledgepkg.ScopeFile:
		return fileChildScopes(store, repoID, scope.ScopePath)
	case knowledgepkg.ScopeRequirement:
		return []knowledgepkg.ArtifactScope{}
	default:
		return []knowledgepkg.ArtifactScope{}
	}
}

func topLevelModuleScopes(store graphstore.GraphStore, repoID string) []knowledgepkg.ArtifactScope {
	modules := store.GetModules(repoID)
	seen := map[string]bool{}
	var results []knowledgepkg.ArtifactScope
	for _, mod := range modules {
		root := strings.Split(strings.Trim(mod.Path, "/"), "/")[0]
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		results = append(results, knowledgepkg.ArtifactScope{
			ScopeType: knowledgepkg.ScopeModule,
			ScopePath: root,
		}.Normalize())
	}
	if len(results) > 0 {
		sort.Slice(results, func(i, j int) bool { return results[i].ScopePath < results[j].ScopePath })
		return results
	}

	topLevelFiles := map[string]bool{}
	for _, file := range store.GetFiles(repoID) {
		dir := path.Dir(file.Path)
		if dir == "." || dir == "" {
			topLevelFiles[file.Path] = true
			continue
		}
		root := strings.Split(strings.Trim(dir, "/"), "/")[0]
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		results = append(results, knowledgepkg.ArtifactScope{
			ScopeType: knowledgepkg.ScopeModule,
			ScopePath: root,
		}.Normalize())
	}
	for filePath := range topLevelFiles {
		results = append(results, knowledgepkg.ArtifactScope{
			ScopeType: knowledgepkg.ScopeFile,
			ScopePath: filePath,
		}.Normalize())
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ScopePath < results[j].ScopePath })
	return results
}

func moduleChildScopes(store graphstore.GraphStore, repoID, modulePath string) []knowledgepkg.ArtifactScope {
	modulePath = strings.Trim(modulePath, "/")
	childModules := map[string]bool{}
	var results []knowledgepkg.ArtifactScope
	for _, mod := range store.GetModules(repoID) {
		trimmed := strings.Trim(mod.Path, "/")
		if trimmed == modulePath || !strings.HasPrefix(trimmed, modulePath+"/") {
			continue
		}
		rest := strings.TrimPrefix(trimmed, modulePath+"/")
		if rest == "" {
			continue
		}
		if part, _, found := strings.Cut(rest, "/"); found {
			childPath := modulePath + "/" + part
			if !childModules[childPath] {
				childModules[childPath] = true
				results = append(results, knowledgepkg.ArtifactScope{
					ScopeType: knowledgepkg.ScopeModule,
					ScopePath: childPath,
				}.Normalize())
			}
		}
	}
	for _, file := range store.GetFiles(repoID) {
		dir := path.Dir(file.Path)
		if dir == "." {
			dir = ""
		}
		if dir != modulePath {
			continue
		}
		results = append(results, knowledgepkg.ArtifactScope{
			ScopeType: knowledgepkg.ScopeFile,
			ScopePath: file.Path,
		}.Normalize())
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ScopePath < results[j].ScopePath })
	return results
}

func fileChildScopes(store graphstore.GraphStore, repoID, filePath string) []knowledgepkg.ArtifactScope {
	symbols := store.GetSymbolsByFile(repoID, filePath)
	results := make([]knowledgepkg.ArtifactScope, 0, len(symbols))
	for _, sym := range symbols {
		results = append(results, knowledgepkg.ArtifactScope{
			ScopeType: knowledgepkg.ScopeSymbol,
			ScopePath: filePath + "#" + sym.Name,
		}.Normalize())
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ScopePath < results[j].ScopePath })
	return results
}

func scopeLabel(scope knowledgepkg.ArtifactScope) string {
	scope = scope.Normalize()
	switch scope.ScopeType {
	case knowledgepkg.ScopeModule:
		return scope.ScopePath + "/"
	case knowledgepkg.ScopeFile:
		return path.Base(scope.ScopePath)
	case knowledgepkg.ScopeSymbol:
		if scope.SymbolName != "" {
			return scope.SymbolName
		}
		_, symbol, _ := strings.Cut(scope.ScopePath, "#")
		return symbol
	case knowledgepkg.ScopeRequirement:
		if scope.SymbolName != "" {
			return scope.SymbolName
		}
		return scope.ScopePath
	default:
		return "Repository"
	}
}

func mapProtoEvidence(evidence []*knowledgev1.KnowledgeEvidence) []knowledgepkg.Evidence {
	if len(evidence) == 0 {
		return nil
	}
	out := make([]knowledgepkg.Evidence, len(evidence))
	for i, ev := range evidence {
		out[i] = knowledgepkg.Evidence{
			SourceType: knowledgepkg.EvidenceSourceType(ev.SourceType),
			SourceID:   ev.SourceId,
			FilePath:   ev.FilePath,
			LineStart:  int(ev.LineStart),
			LineEnd:    int(ev.LineEnd),
			Rationale:  ev.Rationale,
		}
	}
	return out
}
