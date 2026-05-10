// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package search

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// TestService_RepoScopeInvariants is the "lint-style" contract test
// called for in plan §Security & Abuse Surface / §Testing Strategy:
// every invocation of Search must carry a non-empty repo scope, and
// passing an empty one must fail loudly. This catches the class of
// bugs where a refactor routes a request through the service without
// first validating a repo ID.
func TestService_RepoScopeInvariants(t *testing.T) {
	store := graph.NewStore()
	svc := NewService(store)

	if _, err := svc.Search(context.Background(), &Request{Repo: "", Query: "x"}); err == nil {
		t.Error("expected error when Repo is empty")
	}
	if _, err := svc.Search(context.Background(), nil); err == nil {
		t.Error("expected error when Request is nil")
	}
}

// TestService_QueryLengthCapped is the companion invariant for the
// resource-exhaustion guard listed in plan §Security — queries longer
// than MaxQueryLen are truncated instead of DoS-ing the embedder.
func TestService_QueryLengthCapped(t *testing.T) {
	repo := uuid.New().String()
	store := graph.NewStore()
	r, _ := store.CreateRepository(context.Background(), "r", "/")
	store.InjectSymbolForTest(r.ID, &graph.StoredSymbol{
		ID: uuid.New().String(), Name: "X", QualifiedName: "X", Kind: "function",
		Language: "go", FilePath: "x.go", StartLine: 1,
	})
	_ = repo

	svc := NewService(store)
	long := make([]byte, MaxQueryLen*3)
	for i := range long {
		long[i] = 'a'
	}
	req := &Request{Repo: r.ID, Query: string(long)}
	if _, err := svc.Search(context.Background(), req); err != nil {
		t.Fatalf("long-query search should succeed (truncated), got %v", err)
	}
	if len(req.Query) > MaxQueryLen {
		t.Errorf("query should be truncated in-place; len=%d", len(req.Query))
	}
}
