package graphql

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
)

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
