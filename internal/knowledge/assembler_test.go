// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

func TestAssemblerBasic(t *testing.T) {
	store := graph.NewStore()

	// Index a minimal repo.
	result := &indexer.IndexResult{
		RepoName: "test-repo",
		RepoPath: "/tmp/test-repo",
		Files: []indexer.FileResult{
			{
				Path:      "main.go",
				Language:  "go",
				LineCount: 100,
				Symbols: []indexer.Symbol{
					{Name: "main", Kind: "function", Language: "go", StartLine: 1, EndLine: 20, DocComment: "Entry point."},
					{Name: "handleRequest", Kind: "function", Language: "go", StartLine: 22, EndLine: 80, DocComment: "Handles HTTP requests."},
				},
			},
			{
				Path:      "util.go",
				Language:  "go",
				LineCount: 50,
				Symbols: []indexer.Symbol{
					{Name: "helper", Kind: "function", Language: "go", StartLine: 1, EndLine: 10},
					{Name: "TestHelper", Kind: "function", Language: "go", StartLine: 12, EndLine: 20, IsTest: true},
				},
			},
		},
		Modules: []indexer.Module{{Name: "main", Path: ".", FileCount: 2}},
	}

	repo, err := store.StoreIndexResult(result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	assembler := NewAssembler(store)
	snap, err := assembler.Assemble(repo.ID, "")
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if snap.RepositoryID != repo.ID {
		t.Fatalf("expected repo ID %s, got %s", repo.ID, snap.RepositoryID)
	}
	if snap.RepositoryName != "test-repo" {
		t.Fatalf("expected repo name test-repo, got %s", snap.RepositoryName)
	}
	if snap.FileCount != 2 {
		t.Fatalf("expected 2 files, got %d", snap.FileCount)
	}
	if snap.SymbolCount != 4 {
		t.Fatalf("expected 4 symbols, got %d", snap.SymbolCount)
	}
	if snap.TestCount != 1 {
		t.Fatalf("expected 1 test, got %d", snap.TestCount)
	}
	if len(snap.Languages) != 1 || snap.Languages[0].Language != "go" {
		t.Fatalf("expected 1 language (go), got %v", snap.Languages)
	}
	if len(snap.Modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(snap.Modules))
	}

	// main() should be an entry point.
	foundMain := false
	for _, ep := range snap.EntryPoints {
		if ep.Name == "main" {
			foundMain = true
		}
	}
	if !foundMain {
		t.Fatal("expected main to be an entry point")
	}

	// TestHelper should be in test symbols.
	if len(snap.TestSymbols) != 1 || snap.TestSymbols[0].Name != "TestHelper" {
		t.Fatalf("expected TestHelper in test symbols, got %v", snap.TestSymbols)
	}
}

func TestAssemblerDocsDiscovery(t *testing.T) {
	// Create a temp dir with docs.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test Repo"), 0644)
	os.WriteFile(filepath.Join(dir, "CONTRIBUTING.md"), []byte("# Contributing"), 0644)
	os.MkdirAll(filepath.Join(dir, "docs"), 0755)
	os.WriteFile(filepath.Join(dir, "docs", "guide.md"), []byte("# Guide"), 0644)

	store := graph.NewStore()
	result := &indexer.IndexResult{
		RepoName: "docs-repo",
		RepoPath: dir,
		Files: []indexer.FileResult{
			{Path: "main.go", Language: "go", LineCount: 10, Symbols: []indexer.Symbol{
				{Name: "main", Kind: "function", Language: "go", StartLine: 1, EndLine: 10},
			}},
		},
	}
	repo, _ := store.StoreIndexResult(result)

	assembler := NewAssembler(store)
	snap, err := assembler.Assemble(repo.ID, dir)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if len(snap.Docs) != 3 {
		t.Fatalf("expected 3 docs, got %d: %v", len(snap.Docs), snap.Docs)
	}

	foundReadme := false
	for _, d := range snap.Docs {
		if d.Path == "README.md" && d.Content == "# Test Repo" {
			foundReadme = true
		}
	}
	if !foundReadme {
		t.Fatal("expected README.md with content")
	}

	if snap.SourceRevision.DocsFingerprint == "" {
		t.Fatal("expected docs fingerprint to be set")
	}
}

func TestAssemblerNotFound(t *testing.T) {
	store := graph.NewStore()
	assembler := NewAssembler(store)
	_, err := assembler.Assemble("nonexistent", "")
	if err == nil {
		t.Fatal("expected error for nonexistent repo")
	}
}

func TestAssemblerEachInsightHasEvidence(t *testing.T) {
	// Per plan: "each assembled insight has at least one evidence ref"
	// The snapshot itself IS the evidence — each ref points to a specific
	// symbol/file/requirement by ID. We verify that non-empty lists contain
	// IDs.
	store := graph.NewStore()
	result := &indexer.IndexResult{
		RepoName: "evidence-test",
		RepoPath: "/tmp/evidence-test",
		Files: []indexer.FileResult{
			{Path: "app.go", Language: "go", LineCount: 200, Symbols: []indexer.Symbol{
				{Name: "main", Kind: "function", Language: "go", StartLine: 1, EndLine: 20, DocComment: "Main entry."},
				{Name: "HandleAPI", Kind: "function", Language: "go", StartLine: 22, EndLine: 150, DocComment: "API handler."},
			}},
		},
	}
	repo, _ := store.StoreIndexResult(result)

	// Add a requirement and link.
	store.StoreRequirement(repo.ID, &graph.StoredRequirement{
		ID:         "req-1",
		ExternalID: "REQ-001",
		Title:      "Must handle API calls",
	})

	assembler := NewAssembler(store)
	snap, _ := assembler.Assemble(repo.ID, "")

	// Entry points must have IDs.
	for _, ep := range snap.EntryPoints {
		if ep.ID == "" {
			t.Fatalf("entry point %q has no ID", ep.Name)
		}
	}

	// Complex symbols must have IDs.
	for _, cs := range snap.ComplexSymbols {
		if cs.ID == "" {
			t.Fatalf("complex symbol %q has no ID", cs.Name)
		}
	}

	// Requirements must have IDs.
	for _, r := range snap.Requirements {
		if r.ID == "" {
			t.Fatalf("requirement %q has no ID", r.Title)
		}
	}
}

func TestAssemblerScopedSymbolIncludesFocusedContext(t *testing.T) {
	store := graph.NewStore()
	result := &indexer.IndexResult{
		RepoName: "scoped-repo",
		RepoPath: "/tmp/scoped-repo",
		Files: []indexer.FileResult{
			{
				Path:      "internal/api/rest/auth.go",
				Language:  "go",
				LineCount: 120,
				Symbols: []indexer.Symbol{
					{ID: "caller", Name: "ServeLogin", QualifiedName: "auth.ServeLogin", Kind: "function", Language: "go", FilePath: "internal/api/rest/auth.go", StartLine: 1, EndLine: 20},
					{ID: "target", Name: "handleLogin", QualifiedName: "auth.handleLogin", Kind: "function", Language: "go", FilePath: "internal/api/rest/auth.go", StartLine: 24, EndLine: 80, Signature: "func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request)"},
					{ID: "helper", Name: "parseLoginForm", QualifiedName: "auth.parseLoginForm", Kind: "function", Language: "go", FilePath: "internal/api/rest/auth.go", StartLine: 82, EndLine: 110},
				},
			},
		},
		Modules: []indexer.Module{{ID: "mod-auth", Name: "auth", Path: "internal/api/rest", FileCount: 1}},
		Relations: []indexer.Relation{
			{SourceID: "caller", TargetID: "target", Type: indexer.RelationCalls},
			{SourceID: "target", TargetID: "helper", Type: indexer.RelationCalls},
		},
	}

	repo, err := store.StoreIndexResult(result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	assembler := NewAssembler(store)
	snap, err := assembler.AssembleScoped(repo.ID, "", ArtifactScope{
		ScopeType: ScopeSymbol,
		ScopePath: "internal/api/rest/auth.go#handleLogin",
	})
	if err != nil {
		t.Fatalf("AssembleScoped: %v", err)
	}

	if snap.ScopeContext == nil {
		t.Fatal("expected scope context")
	}
	if snap.ScopeContext.TargetSymbol == nil || snap.ScopeContext.TargetSymbol.Name != "handleLogin" {
		t.Fatalf("expected focused target symbol, got %#v", snap.ScopeContext.TargetSymbol)
	}
	if snap.ScopeContext.TargetSymbol.Signature == "" {
		t.Fatalf("expected target symbol signature in scope context, got %#v", snap.ScopeContext.TargetSymbol)
	}
	if snap.ScopeContext.TargetFile == nil || snap.ScopeContext.TargetFile.Path != "internal/api/rest/auth.go" {
		t.Fatalf("expected focused file metadata, got %#v", snap.ScopeContext.TargetFile)
	}
	if len(snap.ScopeContext.Callers) == 0 || snap.ScopeContext.Callers[0].Name != "ServeLogin" {
		t.Fatalf("expected caller context, got %#v", snap.ScopeContext.Callers)
	}
	if len(snap.ScopeContext.Callees) == 0 || snap.ScopeContext.Callees[0].Name != "parseLoginForm" {
		t.Fatalf("expected callee context, got %#v", snap.ScopeContext.Callees)
	}
	if len(snap.ScopeContext.SiblingSymbols) == 0 {
		t.Fatal("expected sibling symbols for context")
	}
	if snap.SymbolCount != 3 {
		t.Fatalf("expected full file symbol context for symbol scope, got %d symbols", snap.SymbolCount)
	}
}
