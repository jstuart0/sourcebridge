package graphql

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
)

type stubComprehensionStore struct {
	workspace *comprehension.Settings
}

func (s stubComprehensionStore) GetSettings(scope comprehension.Scope) (*comprehension.Settings, error) {
	if scope == comprehension.WorkspaceScope && s.workspace != nil {
		return s.workspace, nil
	}
	return nil, nil
}

func (s stubComprehensionStore) SetSettings(settings *comprehension.Settings) error {
	return nil
}

func (s stubComprehensionStore) DeleteSettings(scope comprehension.Scope) error {
	return nil
}

func (s stubComprehensionStore) ListSettings() ([]comprehension.Settings, error) {
	return nil, nil
}

func (s stubComprehensionStore) GetModelCapabilities(modelID string) (*comprehension.ModelCapabilities, error) {
	return nil, nil
}

func (s stubComprehensionStore) SetModelCapabilities(m *comprehension.ModelCapabilities) error {
	return nil
}

func (s stubComprehensionStore) DeleteModelCapabilities(modelID string) error {
	return nil
}

func (s stubComprehensionStore) ListModelCapabilities() ([]comprehension.ModelCapabilities, error) {
	return nil, nil
}

func TestTopLevelModuleScopesFallsBackToFilesWhenModulesMissing(t *testing.T) {
	store := graph.NewStore()
	result := &indexer.IndexResult{
		RepoName: "fallback-repo",
		RepoPath: "/tmp/fallback-repo",
		Files: []indexer.FileResult{
			{Path: "main.go", Language: "go", LineCount: 20},
			{Path: "internal/api/auth.go", Language: "go", LineCount: 40},
			{Path: "web/app/page.tsx", Language: "typescript", LineCount: 80},
		},
	}
	repo, err := store.StoreIndexResult(result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	children := buildScopeChildren(store, repo.ID, knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository})
	if len(children) == 0 {
		t.Fatal("expected repository children from file fallback")
	}

	foundRootFile := false
	foundInternalModule := false
	for _, child := range children {
		if child.ScopeType == knowledgepkg.ScopeFile && child.ScopePath == "main.go" {
			foundRootFile = true
		}
		if child.ScopeType == knowledgepkg.ScopeModule && child.ScopePath == "internal" {
			foundInternalModule = true
		}
	}
	if !foundRootFile {
		t.Fatal("expected top-level file child")
	}
	if !foundInternalModule {
		t.Fatal("expected top-level module child from file paths")
	}
}

func TestResolvedKnowledgeGenerationModePrecedence(t *testing.T) {
	repo := &graph.Repository{GenerationModeDefault: "classic"}
	store := stubComprehensionStore{
		workspace: &comprehension.Settings{
			ScopeType:                      comprehension.ScopeWorkspace,
			ScopeKey:                       comprehension.WorkspaceScope.Key,
			KnowledgeGenerationModeDefault: "understanding_first",
		},
	}

	mode := resolvedKnowledgeGenerationMode(store, repo, nil)
	if mode != knowledgepkg.GenerationModeClassic {
		t.Fatalf("expected repo default to win, got %q", mode)
	}

	requested := KnowledgeGenerationModeUnderstandingFirst
	mode = resolvedKnowledgeGenerationMode(store, repo, &requested)
	if mode != knowledgepkg.GenerationModeUnderstandingFirst {
		t.Fatalf("expected request override to win, got %q", mode)
	}
}
