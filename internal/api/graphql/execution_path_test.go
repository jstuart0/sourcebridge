package graphql

import (
	"os"
	"path/filepath"
	"testing"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

func TestExtractRouteEntryPoints(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal/api/rest"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	source := `package rest

func (s *Server) setupRouter() {
	r.Post("/auth/login", s.handleLogin)
	r.Get("/healthz", s.handleHealthz)
}`
	if err := os.WriteFile(filepath.Join(root, "internal/api/rest/router.go"), []byte(source), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	store := graphstore.NewStore()
	result := &indexer.IndexResult{
		RepoName: "router-repo",
		RepoPath: root,
		Files: []indexer.FileResult{
			{
				Path:      "internal/api/rest/router.go",
				Language:  "go",
				LineCount: 20,
				Symbols: []indexer.Symbol{
					{ID: "login", Name: "handleLogin", QualifiedName: "rest.handleLogin", Kind: "function", Language: "go", FilePath: "internal/api/rest/router.go", StartLine: 3, EndLine: 3},
					{ID: "health", Name: "handleHealthz", QualifiedName: "rest.handleHealthz", Kind: "function", Language: "go", FilePath: "internal/api/rest/router.go", StartLine: 4, EndLine: 4},
				},
			},
		},
	}
	repo, err := store.StoreIndexResult(result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	routes, err := extractRouteEntryPoints(store, repo.ID, root)
	if err != nil {
		t.Fatalf("extractRouteEntryPoints: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %#v", routes)
	}
	if routes[0].Handler == "" || routes[0].Symbol == nil {
		t.Fatalf("expected handler symbol resolution, got %#v", routes[0])
	}
}

func TestBuildExecutionPathResultAppliesTrustGate(t *testing.T) {
	result := buildExecutionPathResult(ExecutionEntryKindSymbol, "handleLogin", []*ExecutionPathStep{
		{Observed: true},
		{Observed: false},
		{Observed: false},
	})
	if result.TrustQualified {
		t.Fatal("expected trust gate to fail for weak path")
	}

	result = buildExecutionPathResult(ExecutionEntryKindSymbol, "handleLogin", []*ExecutionPathStep{
		{Observed: true},
		{Observed: true},
		{Observed: true},
	})
	if !result.TrustQualified {
		t.Fatal("expected trust gate to pass for three observed steps")
	}
}

func TestExecutionStepsFromSymbolPathInfersSameFileHelpers(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal/api/rest"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	source := `package rest

func (s *Server) handleLogin() {
	setSessionCookie()
	writeJSON()
}

func setSessionCookie() {}
func writeJSON() {}
`
	if err := os.WriteFile(filepath.Join(root, "internal/api/rest/auth.go"), []byte(source), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	store := graphstore.NewStore()
	result := &indexer.IndexResult{
		RepoName: "path-repo",
		RepoPath: root,
		Files: []indexer.FileResult{
			{
				Path:      "internal/api/rest/auth.go",
				Language:  "go",
				LineCount: 20,
				Symbols: []indexer.Symbol{
					{ID: "login", Name: "handleLogin", QualifiedName: "rest.handleLogin", Kind: "function", Language: "go", FilePath: "internal/api/rest/auth.go", StartLine: 3, EndLine: 6},
					{ID: "cookie", Name: "setSessionCookie", QualifiedName: "rest.setSessionCookie", Kind: "function", Language: "go", FilePath: "internal/api/rest/auth.go", StartLine: 8, EndLine: 8},
					{ID: "json", Name: "writeJSON", QualifiedName: "rest.writeJSON", Kind: "function", Language: "go", FilePath: "internal/api/rest/auth.go", StartLine: 9, EndLine: 9},
				},
			},
		},
	}
	repo, err := store.StoreIndexResult(result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	symbols, _ := store.GetSymbols(repo.ID, nil, nil, 0, 0)
	var loginID string
	for _, sym := range symbols {
		if sym.Name == "handleLogin" {
			loginID = sym.ID
			break
		}
	}
	if loginID == "" {
		t.Fatal("expected stored handleLogin symbol")
	}

	steps := executionStepsFromSymbolPath(store, repo.ID, root, loginID, 4)
	if len(steps) < 3 {
		t.Fatalf("expected focused step plus inferred same-file helpers, got %#v", steps)
	}
	if steps[1].Label != "setSessionCookie" {
		t.Fatalf("expected first helper after focused symbol, got %#v", steps[1])
	}
	if steps[2].Label != "writeJSON" {
		t.Fatalf("expected second helper after focused symbol, got %#v", steps[2])
	}
	if !steps[1].Observed || !steps[2].Observed {
		t.Fatalf("expected same-file helper steps to be observed, got %#v", steps)
	}
}

func TestExecutionStepsInfersCrossFileHelpers(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal/api/rest"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "internal/store"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Handler calls loadUser which is in a different file
	handlerSource := `package rest

func (s *Server) handleLogin() {
	user := loadUser()
	_ = user
}
`
	if err := os.WriteFile(filepath.Join(root, "internal/api/rest/auth.go"), []byte(handlerSource), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	storeSource := `package store

func loadUser() {}
`
	if err := os.WriteFile(filepath.Join(root, "internal/store/users.go"), []byte(storeSource), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	store := graphstore.NewStore()
	result := &indexer.IndexResult{
		RepoName: "crossfile-repo",
		RepoPath: root,
		Files: []indexer.FileResult{
			{
				Path:      "internal/api/rest/auth.go",
				Language:  "go",
				LineCount: 10,
				Symbols: []indexer.Symbol{
					{ID: "login", Name: "handleLogin", QualifiedName: "rest.handleLogin", Kind: "function", Language: "go", FilePath: "internal/api/rest/auth.go", StartLine: 3, EndLine: 6},
				},
			},
			{
				Path:      "internal/store/users.go",
				Language:  "go",
				LineCount: 5,
				Symbols: []indexer.Symbol{
					{ID: "loaduser", Name: "loadUser", QualifiedName: "store.loadUser", Kind: "function", Language: "go", FilePath: "internal/store/users.go", StartLine: 3, EndLine: 3},
				},
			},
		},
	}
	repo, err := store.StoreIndexResult(result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	symbols, _ := store.GetSymbols(repo.ID, nil, nil, 0, 0)
	var loginID string
	for _, sym := range symbols {
		if sym.Name == "handleLogin" {
			loginID = sym.ID
			break
		}
	}
	if loginID == "" {
		t.Fatal("expected stored handleLogin symbol")
	}

	steps := executionStepsFromSymbolPath(store, repo.ID, root, loginID, 4)
	if len(steps) < 2 {
		t.Fatalf("expected focused step plus cross-file helper, got %d steps", len(steps))
	}
	found := false
	for _, step := range steps {
		if step.Label == "loadUser" {
			found = true
			if !step.Observed {
				t.Fatal("expected cross-file helper to be observed")
			}
			if step.Reason == nil || *step.Reason != "Observed in source call (cross-file)" {
				t.Fatalf("expected cross-file reason, got %v", step.Reason)
			}
			break
		}
	}
	if !found {
		t.Fatal("expected loadUser cross-file helper in steps")
	}
}
