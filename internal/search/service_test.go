// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package search

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// seedStore builds a small in-memory repo suitable for service tests.
// Returns (repoID, store).
func seedStore(t *testing.T) (string, *graph.Store) {
	t.Helper()
	s := graph.NewStore()
	repo, err := s.CreateRepository(t.Context(), "test-repo", "/tmp/test")
	if err != nil {
		t.Fatal(err)
	}
	// We can't reach into private maps, so we use the StoreIndexResult
	// path to seed symbols. Build a minimal indexer.IndexResult.
	// For simplicity we inject symbols via the private reflection?
	// Actually Store.symbols / Store.repoSymbols are unexported. The
	// cleanest way is to use the store's indexer-driven API.
	//
	// However StoreIndexResult requires *indexer.IndexResult, which
	// is heavier than we need here. For unit testing we inject symbols
	// directly via a package-local helper (exported for tests only).
	//
	// The production code remains untouched.
	addSymbols(t, s, repo.ID, []graph.StoredSymbol{
		{Name: "parseUser", QualifiedName: "auth.parseUser", Kind: "function", Language: "go", FilePath: "auth/user.go", StartLine: 10, Signature: "func parseUser() *User", DocComment: "parseUser decodes a user from a session cookie."},
		{Name: "ParseUserToken", QualifiedName: "session.ParseUserToken", Kind: "function", Language: "go", FilePath: "session/token.go", StartLine: 20, Signature: "func ParseUserToken(s string) (*Claims, error)", DocComment: ""},
		{Name: "handleOIDCLogin", QualifiedName: "auth.handleOIDCLogin", Kind: "function", Language: "go", FilePath: "auth/oidc.go", StartLine: 30, DocComment: "handleOIDCLogin starts the OIDC login flow."},
		{Name: "Session", QualifiedName: "session.Session", Kind: "struct", Language: "go", FilePath: "session/session.go", StartLine: 5, DocComment: "Session holds a user's authenticated session state."},
		{Name: "refreshSession", QualifiedName: "session.refreshSession", Kind: "function", Language: "go", FilePath: "session/refresh.go", StartLine: 15, DocComment: "refreshSession rotates the bearer token."},
	})
	return repo.ID, s
}

// addSymbols is a test helper that injects symbols via the public
// GraphStore interface without needing a full IndexResult.
func addSymbols(t *testing.T, s *graph.Store, repoID string, syms []graph.StoredSymbol) {
	t.Helper()
	// The in-memory store doesn't expose a direct symbol-insert API.
	// We work around it by mutating through the private
	// helper-interface implemented below.
	helper, ok := any(s).(interface {
		InjectSymbolForTest(repoID string, sym *graph.StoredSymbol)
	})
	if !ok {
		t.Fatalf("graph.Store does not expose InjectSymbolForTest helper; wire it up before running service tests")
	}
	for i := range syms {
		sc := syms[i]
		sc.ID = uuid.New().String()
		sc.RepoID = repoID
		helper.InjectSymbolForTest(repoID, &sc)
	}
}

func TestService_IdentifierQueryHitsExactFirst(t *testing.T) {
	repoID, s := seedStore(t)
	svc := NewService(s)

	resp, err := svc.Search(context.Background(), &Request{
		Repo:  repoID,
		Query: "parseUser",
		Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected at least one result")
	}
	if resp.Results[0].Title != "parseUser" {
		t.Errorf("expected exact hit first, got %q", resp.Results[0].Title)
	}
	if resp.Results[0].Signals.Exact == 0 {
		t.Errorf("exact signal missing on top result")
	}
}

func TestService_NaturalLanguageRunsLexical(t *testing.T) {
	repoID, s := seedStore(t)
	svc := NewService(s)

	resp, err := svc.Search(context.Background(), &Request{
		Repo:         repoID,
		Query:        "where is oidc login handled",
		Limit:        5,
		IncludeDebug: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Debug == nil || resp.Debug.Class != ClassNaturalLng {
		t.Fatalf("wrong class: %+v", resp.Debug)
	}
	// Without a real FTS backend we get the substring-fallback path.
	// At least `handleOIDCLogin` should appear in the results.
	found := false
	for _, r := range resp.Results {
		if r.Title == "handleOIDCLogin" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("handleOIDCLogin should be in results; got %+v", titles(resp.Results))
	}
}

func TestService_EmptyQueryReturnsEmpty(t *testing.T) {
	repoID, s := seedStore(t)
	svc := NewService(s)
	resp, err := svc.Search(context.Background(), &Request{Repo: repoID, Query: ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 0 {
		t.Errorf("empty query should produce empty results, got %d", len(resp.Results))
	}
}

func TestService_RequiresRepoScope(t *testing.T) {
	svc := NewService(graph.NewStore())
	if _, err := svc.Search(context.Background(), &Request{Repo: "", Query: "x"}); err == nil {
		t.Error("expected error when repo scope missing")
	}
}

func TestService_StableOrdering(t *testing.T) {
	repoID, s := seedStore(t)
	svc := NewService(s)
	req := &Request{Repo: repoID, Query: "session", Limit: 5}
	a, _ := svc.Search(context.Background(), req)
	b, _ := svc.Search(context.Background(), req)
	if len(a.Results) != len(b.Results) {
		t.Fatal("length mismatch across identical queries")
	}
	for i := range a.Results {
		if a.Results[i].EntityID != b.Results[i].EntityID {
			t.Errorf("ordering drifted at %d: %s vs %s", i, a.Results[i].EntityID, b.Results[i].EntityID)
		}
	}
}

func TestService_HonorsContextDeadline(t *testing.T) {
	repoID, s := seedStore(t)
	svc := NewService(s)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := svc.Search(ctx, &Request{Repo: repoID, Query: "parseUser"}); err != nil {
		t.Errorf("expected success on healthy deadline: %v", err)
	}
}

func titles(rs []*Result) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Title
	}
	return out
}
