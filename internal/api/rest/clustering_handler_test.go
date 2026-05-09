// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sourcebridge/sourcebridge/internal/clustering"
	"github.com/sourcebridge/sourcebridge/internal/config"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
)

// fakeClusterStore embeds *graph.Store (satisfying GraphStore) and adds
// ClusterStore methods backed by in-memory state. This avoids SurrealDB in
// unit tests while exercising the real handler logic.
type fakeClusterStore struct {
	*graphstore.Store
	clusters map[string][]clustering.Cluster // repoID → clusters
}

func newFakeClusterStore() *fakeClusterStore {
	return &fakeClusterStore{
		Store:    graphstore.NewStore(),
		clusters: make(map[string][]clustering.Cluster),
	}
}

func (f *fakeClusterStore) GetClusters(_ context.Context, repoID string) ([]clustering.Cluster, error) {
	return f.clusters[repoID], nil
}

func (f *fakeClusterStore) GetClusterByID(_ context.Context, clusterID string) (*clustering.Cluster, error) {
	for _, cs := range f.clusters {
		for _, c := range cs {
			if c.ID == clusterID {
				cp := c
				return &cp, nil
			}
		}
	}
	return nil, nil
}

func (f *fakeClusterStore) GetClusterForSymbol(_ context.Context, repoID, symbolID string) (*clustering.Cluster, error) {
	for _, c := range f.clusters[repoID] {
		for _, m := range c.Members {
			if m.SymbolID == symbolID {
				cp := c
				return &cp, nil
			}
		}
	}
	return nil, nil
}

func (f *fakeClusterStore) SaveClusters(_ context.Context, repoID string, clusters []clustering.Cluster) error {
	f.clusters[repoID] = append(f.clusters[repoID], clusters...)
	return nil
}

func (f *fakeClusterStore) ReplaceClusters(_ context.Context, repoID string, clusters []clustering.Cluster) error {
	f.clusters[repoID] = clusters
	return nil
}

func (f *fakeClusterStore) DeleteClusters(_ context.Context, repoID string) error {
	delete(f.clusters, repoID)
	return nil
}

func (f *fakeClusterStore) SetClusterLLMLabel(_ context.Context, clusterID, label string) error {
	for repoID, cs := range f.clusters {
		for i, c := range cs {
			if c.ID == clusterID {
				f.clusters[repoID][i].LLMLabel = &label
				return nil
			}
		}
	}
	return clustering.ErrClusterNotFound
}

func (f *fakeClusterStore) GetRepoEdgeHash(_ context.Context, repoID string) (string, error) {
	return "", nil
}

func (f *fakeClusterStore) SetRepoEdgeHash(_ context.Context, repoID, hash string) error {
	return nil
}

// TestHandleListClusters_PackagesAndWarnings verifies that the REST handler
// computes and returns packages and warnings server-side using the symbol
// metadata and call edges available in the store.
func TestHandleListClusters_PackagesAndWarnings(t *testing.T) {
	store := newFakeClusterStore()
	const repoID = "repo-abc"

	// Inject two symbols in different files/packages.
	// auth/token.go → package "auth"
	// api/handler.go → package "api"
	store.InjectSymbolForTest(repoID, &graphstore.StoredSymbol{
		ID:            "sym-token",
		Name:          "TokenStore.Rotate",
		QualifiedName: "TokenStore.Rotate",
		FilePath:      "internal/auth/token.go",
	})
	store.InjectSymbolForTest(repoID, &graphstore.StoredSymbol{
		ID:            "sym-session",
		Name:          "Session.Validate",
		QualifiedName: "Session.Validate",
		FilePath:      "internal/auth/session.go",
	})
	store.InjectSymbolForTest(repoID, &graphstore.StoredSymbol{
		ID:            "sym-api",
		Name:          "API.Handle",
		QualifiedName: "API.Handle",
		FilePath:      "api/handler.go",
	})
	store.InjectSymbolForTest(repoID, &graphstore.StoredSymbol{
		ID:            "sym-worker",
		Name:          "Worker.Run",
		QualifiedName: "Worker.Run",
		FilePath:      "worker/job.go",
	})
	store.InjectSymbolForTest(repoID, &graphstore.StoredSymbol{
		ID:            "sym-middleware",
		Name:          "Middleware.Wrap",
		QualifiedName: "Middleware.Wrap",
		FilePath:      "internal/middleware/wrap.go",
	})

	// Inject call edges:
	// sym-api → sym-token (from api package to auth cluster)
	// sym-worker → sym-token (from worker package to auth cluster)
	// sym-middleware → sym-token (from middleware package to auth cluster)
	// → TokenStore.Rotate has 3 distinct caller packages (api, worker, middleware)
	// → should get cross-package-callers warning
	//
	// sym-api → sym-session (one caller)
	// → Session.Validate has 1 caller; hot-path if highest in cluster
	store.InjectCallEdgesForTest(repoID, []graphstore.CallEdge{
		{CallerID: "sym-api", CalleeID: "sym-token"},
		{CallerID: "sym-worker", CalleeID: "sym-token"},
		{CallerID: "sym-middleware", CalleeID: "sym-token"},
		{CallerID: "sym-api", CalleeID: "sym-session"},
	})

	// Create the auth cluster with members.
	now := time.Now()
	authCluster := clustering.Cluster{
		ID:     "cluster:auth",
		RepoID: repoID,
		Label:  "auth",
		Size:   2,
		Members: []clustering.ClusterMember{
			{ClusterID: "cluster:auth", SymbolID: "sym-token", RepoID: repoID},
			{ClusterID: "cluster:auth", SymbolID: "sym-session", RepoID: repoID},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	_ = store.ReplaceClusters(context.Background(), repoID, []clustering.Cluster{authCluster})

	// Set up the server with the OSS edition (subsystem_clustering is registered
	// globally in registry_data.go for OSS + Enterprise).
	cfg := &config.Config{Edition: "oss"}
	srv := &Server{cfg: cfg, store: store}

	// Wire up chi routing so URL params work.
	r := chi.NewRouter()
	r.Get("/api/v1/repositories/{repo_id}/clusters", srv.handleListClusters)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/repositories/"+repoID+"/clusters", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp clustersResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if len(resp.Clusters) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(resp.Clusters))
	}

	c := resp.Clusters[0]
	if c.Label != "auth" {
		t.Errorf("expected label=auth, got %q", c.Label)
	}

	// Packages should include "auth" derived from internal/auth/*.go files.
	if len(c.Packages) == 0 {
		t.Error("expected packages to be non-empty")
	}
	containsAuth := false
	for _, p := range c.Packages {
		if p == "auth" {
			containsAuth = true
		}
	}
	if !containsAuth {
		t.Errorf("expected packages to include 'auth'; got %v", c.Packages)
	}

	// Warnings should include a cross-package-callers warning for TokenStore.Rotate
	// (called from 3 distinct packages: api, worker, middleware).
	if len(c.Warnings) == 0 {
		t.Error("expected non-empty warnings")
	}
	var hasCrossPackage bool
	for _, w := range c.Warnings {
		if w.Kind == "cross-package-callers" && strings.Contains(w.Detail, "TokenStore.Rotate") {
			hasCrossPackage = true
		}
	}
	if !hasCrossPackage {
		t.Errorf("expected cross-package-callers warning for TokenStore.Rotate; got %v", c.Warnings)
	}
}

// ---------------------------------------------------------------------------
// handleRelabelClusters tests (CA-283)
// ---------------------------------------------------------------------------

// newRelabelTestServer builds a minimal Server for relabel handler tests.
// It returns the server and the repoID inserted into the store.
// When withOrch is true, a real in-memory orchestrator is wired.
func newRelabelTestServer(t *testing.T, withOrch bool) (srv *Server, repoID string) {
	t.Helper()

	store := newFakeClusterStore()
	repo, err := store.Store.CreateRepository("test-repo", "/test")
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	repoID = repo.ID

	// Seed one cluster so the handler has something to relabel.
	now := time.Now()
	_ = store.SaveClusters(context.Background(), repoID, []clustering.Cluster{
		{ID: "cluster:test", RepoID: repoID, Label: "test", Size: 1, CreatedAt: now, UpdatedAt: now},
	})

	cfg := &config.Config{Edition: "oss"}
	// Wire a frozen resolver that returns a non-empty provider so the
	// orchestrator's LLMProvider guard (ErrLLMProviderRequired) doesn't
	// fire on relabel_clusters enqueues.
	resolver := resolution.NewFrozenResolver(resolution.Snapshot{Provider: "test-provider"})
	srv = &Server{cfg: cfg, store: store, llmResolver: resolver}

	if withOrch {
		orch := orchestrator.New(llm.NewMemStore(), orchestrator.Config{
			MaxConcurrency:            1,
			SkipStartupReconciliation: true,
		})
		srv.orchestrator = orch
	}
	return srv, repoID
}

// relabelChiRouter wraps the handler in a chi.Router so chi.URLParam("repo_id") works.
func relabelChiRouter(srv *Server) *chi.Mux {
	r := chi.NewRouter()
	r.Post("/api/v1/repositories/{repo_id}/clusters/relabel", srv.handleRelabelClusters)
	return r
}

// TestHandleRelabelClusters_HappyPath verifies that a valid request returns 202
// with a job_id field.
func TestHandleRelabelClusters_HappyPath_Returns202WithJobID(t *testing.T) {
	srv, repoID := newRelabelTestServer(t, true /* withOrch */)
	r := relabelChiRouter(srv)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/repositories/"+repoID+"/clusters/relabel", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	var resp relabelResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.JobID == "" {
		t.Errorf("expected non-empty job_id in response")
	}
}

// TestHandleRelabelClusters_OrchestratorUnavailable verifies that a nil orchestrator
// returns 503 with a diagnostic message.
func TestHandleRelabelClusters_OrchestratorUnavailable_Returns503(t *testing.T) {
	srv, repoID := newRelabelTestServer(t, false /* withOrch */)
	r := relabelChiRouter(srv)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/repositories/"+repoID+"/clusters/relabel", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503 (orchestrator nil)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "orchestrator") {
		t.Errorf("body %q should mention orchestrator", rec.Body.String())
	}
}

// TestHandleRelabelClusters_RepoNotFound verifies that a non-existent repoID
// returns 404.
func TestHandleRelabelClusters_RepoNotFound_Returns404(t *testing.T) {
	srv, _ := newRelabelTestServer(t, true /* withOrch */)
	r := relabelChiRouter(srv)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/repositories/does-not-exist/clusters/relabel", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
}

// TestHandleRelabelClusters_InvalidJSON_Returns400 verifies that a malformed
// JSON body returns 400.
func TestHandleRelabelClusters_InvalidJSON_Returns400(t *testing.T) {
	srv, repoID := newRelabelTestServer(t, true /* withOrch */)
	r := relabelChiRouter(srv)

	// A non-empty body that is not valid JSON.
	body := bytes.NewReader([]byte("not-valid-json"))
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/repositories/"+repoID+"/clusters/relabel", body)
	req.ContentLength = int64(body.Len())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

// TestHandleRelabelClusters_WithExplicitClusterIDs verifies that passing
// cluster_ids in the body scopes the relabel to those clusters.
func TestHandleRelabelClusters_WithExplicitClusterIDs_Returns202(t *testing.T) {
	srv, repoID := newRelabelTestServer(t, true /* withOrch */)
	r := relabelChiRouter(srv)

	body, _ := json.Marshal(map[string]interface{}{
		"cluster_ids": []string{"cluster:test"},
	})
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/repositories/"+repoID+"/clusters/relabel",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	var resp relabelResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.JobID == "" {
		t.Errorf("expected non-empty job_id")
	}
}
