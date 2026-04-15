// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package architecture

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

func TestModuleFromPath(t *testing.T) {
	tests := []struct {
		path  string
		depth int
		want  string
	}{
		{"internal/api/graphql/resolvers.go", 1, "internal"},
		{"internal/api/graphql/resolvers.go", 2, "internal/api"},
		{"internal/api/graphql/resolvers.go", 3, "internal/api/graphql"},
		{"main.go", 1, "(root)"},
		{"web/src/components/App.tsx", 1, "web"},
		{"web/src/components/App.tsx", 2, "web/src"},
		{"a/b/c/d/e/f.go", 10, "a/b/c/d/e"},
	}
	for _, tt := range tests {
		got := ModuleFromPath(tt.path, tt.depth)
		if got != tt.want {
			t.Errorf("ModuleFromPath(%q, %d) = %q, want %q", tt.path, tt.depth, got, tt.want)
		}
	}
}

// mockStore implements a minimal graph.GraphStore for testing.
type mockStore struct {
	symbols []*graph.StoredSymbol
	files   []*graph.File
	edges   []graph.CallEdge
	links   []*graph.StoredLink
}

func (m *mockStore) GetSymbols(repoID string, query *string, kind *string, limit, offset int) ([]*graph.StoredSymbol, int) {
	var result []*graph.StoredSymbol
	for _, s := range m.symbols {
		if s.RepoID == repoID {
			result = append(result, s)
		}
	}
	return result, len(result)
}
func (m *mockStore) GetCallEdges(repoID string) []graph.CallEdge {
	return append([]graph.CallEdge(nil), m.edges...)
}
func (m *mockStore) GetFiles(repoID string) []*graph.File {
	var result []*graph.File
	for _, f := range m.files {
		if f.RepoID == repoID {
			result = append(result, f)
		}
	}
	return result
}
func (m *mockStore) GetLinksForRepo(repoID string) []*graph.StoredLink {
	var result []*graph.StoredLink
	for _, l := range m.links {
		if l.RepoID == repoID {
			result = append(result, l)
		}
	}
	return result
}

func TestBuildModuleLevelDiagram_Empty(t *testing.T) {
	store := &mockStore{}
	result, err := BuildDiagram(store, DiagramOpts{
		RepoID:      "repo1",
		Level:       "MODULE",
		ModuleDepth: 1,
		MaxNodes:    30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Level != "MODULE" {
		t.Errorf("expected level MODULE, got %s", result.Level)
	}
	if len(result.Modules) != 0 {
		t.Errorf("expected 0 modules, got %d", len(result.Modules))
	}
	if result.Truncated {
		t.Error("expected not truncated")
	}
}

func TestBuildModuleLevelDiagram_SingleModule(t *testing.T) {
	store := &mockStore{
		symbols: []*graph.StoredSymbol{
			{ID: "s1", RepoID: "r1", FilePath: "internal/api/handler.go"},
			{ID: "s2", RepoID: "r1", FilePath: "internal/api/router.go"},
		},
		files: []*graph.File{
			{ID: "f1", RepoID: "r1", Path: "internal/api/handler.go"},
			{ID: "f2", RepoID: "r1", Path: "internal/api/router.go"},
		},
	}

	result, err := BuildDiagram(store, DiagramOpts{
		RepoID:      "r1",
		Level:       "MODULE",
		ModuleDepth: 1,
		MaxNodes:    30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(result.Modules))
	}
	if result.Modules[0].Path != "internal" {
		t.Errorf("expected module path 'internal', got %q", result.Modules[0].Path)
	}
	if result.Modules[0].SymbolCount != 2 {
		t.Errorf("expected 2 symbols, got %d", result.Modules[0].SymbolCount)
	}
}

func TestBuildModuleLevelDiagram_MultiModule(t *testing.T) {
	store := &mockStore{
		symbols: []*graph.StoredSymbol{
			{ID: "s1", RepoID: "r1", FilePath: "internal/api/handler.go"},
			{ID: "s2", RepoID: "r1", FilePath: "internal/db/store.go"},
			{ID: "s3", RepoID: "r1", FilePath: "web/src/App.tsx"},
		},
		files: []*graph.File{
			{ID: "f1", RepoID: "r1", Path: "internal/api/handler.go"},
			{ID: "f2", RepoID: "r1", Path: "internal/db/store.go"},
			{ID: "f3", RepoID: "r1", Path: "web/src/App.tsx"},
		},
		edges: []graph.CallEdge{
			{CallerID: "s1", CalleeID: "s2"},
		},
	}

	result, err := BuildDiagram(store, DiagramOpts{
		RepoID:      "r1",
		Level:       "MODULE",
		ModuleDepth: 2,
		MaxNodes:    30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Modules) != 3 {
		t.Fatalf("expected 3 modules, got %d", len(result.Modules))
	}

	// Verify cross-module edge exists
	foundEdge := false
	for _, mod := range result.Modules {
		if mod.Path == "internal/api" {
			for _, e := range mod.OutboundEdges {
				if e.TargetPath == "internal/db" && e.CallCount == 1 {
					foundEdge = true
				}
			}
		}
	}
	if !foundEdge {
		t.Error("expected edge from internal/api to internal/db")
	}
}

func TestBuildModuleLevelDiagram_SelfEdgesExcluded(t *testing.T) {
	store := &mockStore{
		symbols: []*graph.StoredSymbol{
			{ID: "s1", RepoID: "r1", FilePath: "internal/api/handler.go"},
			{ID: "s2", RepoID: "r1", FilePath: "internal/api/router.go"},
		},
		files: []*graph.File{
			{ID: "f1", RepoID: "r1", Path: "internal/api/handler.go"},
			{ID: "f2", RepoID: "r1", Path: "internal/api/router.go"},
		},
		edges: []graph.CallEdge{
			{CallerID: "s1", CalleeID: "s2"}, // same module at depth 1
		},
	}

	result, err := BuildDiagram(store, DiagramOpts{
		RepoID:      "r1",
		Level:       "MODULE",
		ModuleDepth: 1,
		MaxNodes:    30,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, mod := range result.Modules {
		if len(mod.OutboundEdges) > 0 {
			t.Errorf("expected no outbound edges for module %q (self-edge should be excluded)", mod.Path)
		}
	}
}

func TestBuildModuleLevelDiagram_Truncation(t *testing.T) {
	var symbols []*graph.StoredSymbol
	var files []*graph.File
	for i := 0; i < 40; i++ {
		path := "mod" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "/file.go"
		id := "s" + string(rune('0'+i%10)) + string(rune('0'+i/10))
		symbols = append(symbols, &graph.StoredSymbol{ID: id, RepoID: "r1", FilePath: path})
		files = append(files, &graph.File{ID: "f" + id, RepoID: "r1", Path: path})
	}

	store := &mockStore{symbols: symbols, files: files}
	result, err := BuildDiagram(store, DiagramOpts{
		RepoID:      "r1",
		Level:       "MODULE",
		ModuleDepth: 1,
		MaxNodes:    10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated {
		t.Error("expected truncated=true")
	}
	if result.ShownModules > 10 {
		t.Errorf("expected <= 10 shown modules, got %d", result.ShownModules)
	}
	if result.TotalModules != 40 {
		t.Errorf("expected 40 total modules, got %d", result.TotalModules)
	}
}

func TestBuildFileLevelDiagram(t *testing.T) {
	store := &mockStore{
		symbols: []*graph.StoredSymbol{
			{ID: "s1", RepoID: "r1", FilePath: "internal/db/store.go"},
			{ID: "s2", RepoID: "r1", FilePath: "internal/db/surreal.go"},
			{ID: "s3", RepoID: "r1", FilePath: "internal/api/resolver.go"},
		},
		files: []*graph.File{
			{ID: "f1", RepoID: "r1", Path: "internal/db/store.go"},
			{ID: "f2", RepoID: "r1", Path: "internal/db/surreal.go"},
			{ID: "f3", RepoID: "r1", Path: "internal/api/resolver.go"},
		},
		edges: []graph.CallEdge{
			{CallerID: "s1", CalleeID: "s2"},
			{CallerID: "s3", CalleeID: "s1"},
		},
	}

	filter := "internal/db"
	result, err := BuildDiagram(store, DiagramOpts{
		RepoID:       "r1",
		Level:        "FILE",
		ModuleFilter: &filter,
		ModuleDepth:  2,
		MaxNodes:     50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Level != "FILE" {
		t.Errorf("expected level FILE, got %s", result.Level)
	}
	if len(result.Modules) < 2 {
		t.Fatalf("expected at least 2 file nodes, got %d", len(result.Modules))
	}
	if result.MermaidSource == "" {
		t.Error("expected non-empty Mermaid source")
	}
}

func TestModuleNodeMetadata(t *testing.T) {
	store := &mockStore{
		symbols: []*graph.StoredSymbol{
			{ID: "s1", RepoID: "r1", FilePath: "pkg/foo.go"},
			{ID: "s2", RepoID: "r1", FilePath: "pkg/bar.go"},
			{ID: "s3", RepoID: "r1", FilePath: "pkg/bar.go"},
		},
		files: []*graph.File{
			{ID: "f1", RepoID: "r1", Path: "pkg/foo.go"},
			{ID: "f2", RepoID: "r1", Path: "pkg/bar.go"},
		},
		links: []*graph.StoredLink{
			{ID: "l1", RepoID: "r1", SymbolID: "s1"},
		},
	}

	result, err := BuildDiagram(store, DiagramOpts{
		RepoID:      "r1",
		Level:       "MODULE",
		ModuleDepth: 1,
		MaxNodes:    30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(result.Modules))
	}
	mod := result.Modules[0]
	if mod.SymbolCount != 3 {
		t.Errorf("expected 3 symbols, got %d", mod.SymbolCount)
	}
	if mod.FileCount != 2 {
		t.Errorf("expected 2 files, got %d", mod.FileCount)
	}
	if mod.RequirementLinkCount != 1 {
		t.Errorf("expected 1 requirement link, got %d", mod.RequirementLinkCount)
	}
}
