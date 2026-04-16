// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/sourcebridge/sourcebridge/internal/architecture"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

func TestHandleExportDiagramMermaidBuildsDeterministicFallback(t *testing.T) {
	diagStore = &diagramDocumentStore{docs: make(map[string]*architecture.DiagramDocument)}
	store, repo := newDiagramTestServerAndRepo(t)
	server := &Server{store: store}

	req := withRepoRouteParam(httptest.NewRequest(http.MethodGet, "/api/v1/diagrams/"+repo.ID+"/export/mermaid", nil), repo.ID)
	rec := httptest.NewRecorder()

	server.handleExportDiagramMermaid(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "flowchart") {
		t.Fatalf("expected Mermaid flowchart, got %q", body)
	}
	if !strings.Contains(body, "internal") {
		t.Fatalf("expected deterministic module output, got %q", body)
	}
}

func TestHandleGetStructuredDiagramHonorsDepthQueryParam(t *testing.T) {
	diagStore = &diagramDocumentStore{docs: make(map[string]*architecture.DiagramDocument)}
	store, repo := newDiagramTestServerAndRepo(t)
	server := &Server{store: store}

	req := withRepoRouteParam(httptest.NewRequest(http.MethodGet, "/api/v1/diagrams/"+repo.ID+"/structured?depth=2", nil), repo.ID)
	rec := httptest.NewRecorder()

	server.handleGetStructuredDiagram(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var doc struct {
		Nodes []struct {
			Label string `json:"label"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	labels := make(map[string]bool, len(doc.Nodes))
	for _, node := range doc.Nodes {
		labels[node.Label] = true
	}
	if !labels["internal/api"] {
		t.Fatalf("expected depth-2 node internal/api, got %#v", labels)
	}
	if !labels["internal/db"] {
		t.Fatalf("expected depth-2 node internal/db, got %#v", labels)
	}
	if labels["internal"] {
		t.Fatalf("did not expect depth-1 collapsed node internal, got %#v", labels)
	}
}

func TestHandlePutAndDeleteDiagramDocumentPersistsUserEditedDocument(t *testing.T) {
	diagStore = &diagramDocumentStore{docs: make(map[string]*architecture.DiagramDocument)}
	store, repo := newDiagramTestServerAndRepo(t)
	server := &Server{store: store}

	body := `{
		"id":"manual-1",
		"repository_id":"` + repo.ID + `",
		"source_kind":"deterministic",
		"view_type":"system",
		"title":"Manual Diagram",
		"nodes":[{"id":"ui","label":"UI","kind":"interface","provenance":"user_added","position_x":100,"position_y":120}],
		"edges":[],
		"groups":[]
	}`

	putReq := withRepoRouteParam(httptest.NewRequest(http.MethodPut, "/api/v1/diagrams/"+repo.ID, strings.NewReader(body)), repo.ID)
	putRec := httptest.NewRecorder()
	server.handlePutDiagramDocument(putRec, putReq)

	if putRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from put, got %d: %s", putRec.Code, putRec.Body.String())
	}

	getReq := withRepoRouteParam(httptest.NewRequest(http.MethodGet, "/api/v1/diagrams/"+repo.ID+"/structured", nil), repo.ID)
	getRec := httptest.NewRecorder()
	server.handleGetStructuredDiagram(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from get, got %d: %s", getRec.Code, getRec.Body.String())
	}
	var doc architecture.DiagramDocument
	if err := json.Unmarshal(getRec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if doc.SourceKind != architecture.SourceUserEdited {
		t.Fatalf("expected user-edited source kind, got %q", doc.SourceKind)
	}
	if len(doc.Nodes) != 1 || doc.Nodes[0].Label != "UI" {
		t.Fatalf("expected saved node, got %#v", doc.Nodes)
	}

	deleteReq := withRepoRouteParam(httptest.NewRequest(http.MethodDelete, "/api/v1/diagrams/"+repo.ID, nil), repo.ID)
	deleteRec := httptest.NewRecorder()
	server.handleDeleteDiagramDocument(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 from delete, got %d: %s", deleteRec.Code, deleteRec.Body.String())
	}

	fallbackRec := httptest.NewRecorder()
	server.handleGetStructuredDiagram(fallbackRec, getReq)
	if fallbackRec.Code != http.StatusOK {
		t.Fatalf("expected deterministic fallback after delete, got %d: %s", fallbackRec.Code, fallbackRec.Body.String())
	}
	var fallbackDoc architecture.DiagramDocument
	if err := json.Unmarshal(fallbackRec.Body.Bytes(), &fallbackDoc); err != nil {
		t.Fatalf("json.Unmarshal fallback: %v", err)
	}
	if fallbackDoc.SourceKind != architecture.SourceDeterministic {
		t.Fatalf("expected deterministic fallback after delete, got %q", fallbackDoc.SourceKind)
	}
}

func newDiagramTestServerAndRepo(t *testing.T) (*graphstore.Store, *graphstore.Repository) {
	t.Helper()
	store := graphstore.NewStore()
	repo, err := store.StoreIndexResult(&indexer.IndexResult{
		RepoName: "diagram-rest-repo",
		RepoPath: "/tmp/diagram-rest-repo",
		Files: []indexer.FileResult{
			{Path: "cmd/server/main.go", Language: "go", LineCount: 20},
			{Path: "internal/api/auth.go", Language: "go", LineCount: 30},
			{Path: "internal/api/rest/handler.go", Language: "go", LineCount: 50},
			{Path: "internal/db/store.go", Language: "go", LineCount: 40},
		},
	})
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}
	return store, repo
}

func withRepoRouteParam(req *http.Request, repoID string) *http.Request {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("repoId", repoID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}
