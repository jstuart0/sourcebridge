// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/search"
)

func TestHandleSearch_BasicShape(t *testing.T) {
	store := graphstore.NewStore()
	repo, _ := store.CreateRepository(t.Context(), "test-repo", "/tmp/test")
	store.InjectSymbolForTest(repo.ID, &graphstore.StoredSymbol{
		ID: uuid.New().String(), Name: "parseUser", QualifiedName: "auth.parseUser",
		Kind: "function", Language: "go", FilePath: "auth/user.go", StartLine: 10,
	})

	svc := search.NewService(store)
	srv := &Server{
		store:     store,
		searchSvc: svc,
	}

	body, _ := json.Marshal(map[string]any{
		"repo":  repo.ID,
		"query": "parseUser",
		"limit": 5,
	})
	req := httptest.NewRequest("POST", "/api/v1/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleSearch(w, req)

	if w.Code != 200 {
		t.Fatalf("status: want 200 got %d body=%s", w.Code, w.Body.String())
	}
	var resp searchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected at least one result")
	}
	if resp.Results[0].Title != "parseUser" {
		t.Errorf("expected parseUser first, got %q", resp.Results[0].Title)
	}
	if resp.Results[0].Signals.Exact == 0 {
		t.Errorf("exact signal missing")
	}
}

func TestHandleSearch_MissingFields(t *testing.T) {
	srv := &Server{searchSvc: search.NewService(graphstore.NewStore())}
	body := []byte(`{"query": "x"}`)
	req := httptest.NewRequest("POST", "/api/v1/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleSearch(w, req)
	if w.Code != 400 {
		t.Errorf("want 400 for missing repo, got %d", w.Code)
	}
}

func TestHandleSearch_UnknownRepoReturnsNotFound(t *testing.T) {
	srv := &Server{
		store:     graphstore.NewStore(),
		searchSvc: search.NewService(graphstore.NewStore()),
	}
	body, _ := json.Marshal(map[string]any{
		"repo": "nope", "query": "x",
	})
	req := httptest.NewRequest("POST", "/api/v1/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleSearch(w, req)
	if w.Code != 404 {
		t.Errorf("want 404 for unknown repo, got %d", w.Code)
	}
}

func TestHandleSearch_NoServiceReturns503(t *testing.T) {
	srv := &Server{}
	body, _ := json.Marshal(map[string]any{"repo": "x", "query": "y"})
	req := httptest.NewRequest("POST", "/api/v1/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleSearch(w, req)
	if w.Code != 503 {
		t.Errorf("want 503 when service unavailable, got %d", w.Code)
	}
}
