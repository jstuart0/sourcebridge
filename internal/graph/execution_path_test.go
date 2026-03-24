package graph

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

func TestTraceLikelyExecutionPathBuildsCallerAndCalleeChain(t *testing.T) {
	store := NewStore()
	result := &indexer.IndexResult{
		RepoName: "path-repo",
		RepoPath: "/tmp/path-repo",
		Files: []indexer.FileResult{
			{
				Path:      "router.go",
				Language:  "go",
				LineCount: 120,
				Symbols: []indexer.Symbol{
					{ID: "route", Name: "handleLogin", QualifiedName: "rest.handleLogin", Kind: "function", Language: "go", FilePath: "router.go", StartLine: 10, EndLine: 30},
				},
			},
			{
				Path:      "service.go",
				Language:  "go",
				LineCount: 120,
				Symbols: []indexer.Symbol{
					{ID: "service", Name: "processLogin", QualifiedName: "service.processLogin", Kind: "function", Language: "go", FilePath: "service.go", StartLine: 10, EndLine: 40},
				},
			},
			{
				Path:      "store.go",
				Language:  "go",
				LineCount: 120,
				Symbols: []indexer.Symbol{
					{ID: "repo", Name: "loadUser", QualifiedName: "repo.loadUser", Kind: "function", Language: "go", FilePath: "store.go", StartLine: 10, EndLine: 20},
				},
			},
		},
		Relations: []indexer.Relation{
			{SourceID: "route", TargetID: "service", Type: indexer.RelationCalls},
			{SourceID: "service", TargetID: "repo", Type: indexer.RelationCalls},
		},
	}
	repo, err := store.StoreIndexResult(result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	symbols, _ := store.GetSymbols(repo.ID, nil, nil, 0, 0)
	var serviceID string
	for _, sym := range symbols {
		if sym.Name == "processLogin" {
			serviceID = sym.ID
			break
		}
	}
	if serviceID == "" {
		t.Fatal("expected stored service symbol")
	}

	nodes := TraceLikelyExecutionPath(store, repo.ID, serviceID, 4)
	if len(nodes) < 3 {
		t.Fatalf("expected caller + current + callee chain, got %#v", nodes)
	}
	if nodes[0].SymbolName != "handleLogin" {
		t.Fatalf("expected caller first, got %#v", nodes[0])
	}
	if nodes[1].SymbolName != "processLogin" {
		t.Fatalf("expected current symbol second, got %#v", nodes[1])
	}
	if nodes[2].SymbolName != "loadUser" {
		t.Fatalf("expected callee third, got %#v", nodes[2])
	}
}
