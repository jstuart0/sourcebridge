// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

//go:build integration

package db

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sourcebridge/sourcebridge/internal/clustering"
)

// clusterSyncBuffer is a mutex-protected buffer for capturing slog output
// from concurrent goroutines in cluster integration tests.
type clusterSyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *clusterSyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *clusterSyncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// stringPtr returns a pointer to s. Convenience helper for test literals.
func stringPtr(s string) *string { return &s }

// absDuration returns the absolute value of d.
func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// makeTestCluster returns a minimal clustering.Cluster with the given ID and
// optional LLMLabel. EdgeHash must be non-empty to satisfy the schema.
func makeTestCluster(id string, llmLabel *string) clustering.Cluster {
	return clustering.Cluster{
		ID:       "cluster:" + id,
		RepoID:   "repo-ca174-test",
		Label:    "test-label-" + id,
		LLMLabel: llmLabel,
		Size:     1,
		EdgeHash: "deadbeef" + id,
		Partial:  false,
	}
}

// rawQueryClusters fetches all cluster rows for the given repo as raw
// map[string]any values so we can assert field-absence (not just nil value).
func rawQueryClusters(t *testing.T, s *SurrealDB, repoID string) []map[string]any {
	t.Helper()
	db := s.DB()
	if db == nil {
		t.Fatal("raw query: database not connected")
	}
	rows, err := queryOne[[]map[string]any](context.Background(), db,
		`SELECT * FROM cluster WHERE repo_id = $repo_id`,
		map[string]any{"repo_id": repoID})
	if err != nil {
		t.Fatalf("rawQueryClusters: %v", err)
	}
	return rows
}

// TestReplaceClusters_NilLLMLabel_Succeeds verifies that writing a batch of
// clusters all with LLMLabel == nil does not return a SurrealDB error, and
// that each written row has no llm_label key (field-absent, not
// present-as-null) when read back via a raw SELECT.
//
// Regression guard for CA-174: SurrealDB v2.2+ rejects explicit null for
// option<string> in a transactional FOR-loop SET, so the fix switches
// ReplaceClusters to CONTENT $c.content with omitempty on LLMLabel.
func TestReplaceClusters_NilLLMLabel_Succeeds(t *testing.T) {
	s := startSurrealContainer(t)
	store := NewSurrealStore(s)
	ctx := context.Background()

	writeTime := time.Now()
	repoID := "repo-ca174-nil-" + uuid.NewString()
	clusters := []clustering.Cluster{
		makeTestCluster(uuid.NewString(), nil),
		makeTestCluster(uuid.NewString(), nil),
		makeTestCluster(uuid.NewString(), nil),
	}
	// Override repo_id on each cluster to use our unique repoID.
	for i := range clusters {
		clusters[i].RepoID = repoID
	}

	if err := store.ReplaceClusters(ctx, repoID, clusters); err != nil {
		t.Fatalf("ReplaceClusters with nil LLMLabel: %v", err)
	}

	// Round-trip via GetClusters: LLMLabel must be nil, not empty string.
	got, err := store.GetClusters(ctx, repoID)
	if err != nil {
		t.Fatalf("GetClusters: %v", err)
	}
	if len(got) != len(clusters) {
		t.Fatalf("GetClusters: want %d rows, got %d", len(clusters), len(got))
	}
	const tolerance = 5 * time.Second
	for _, c := range got {
		if c.LLMLabel != nil {
			t.Errorf("cluster %s: want LLMLabel nil, got %q", c.ID, *c.LLMLabel)
		}
		// Datetime round-trip: created_at/updated_at must survive the CONTENT
		// write path on v2.2.1 CBOR datetime handling with non-zero values.
		if c.CreatedAt.IsZero() {
			t.Errorf("cluster %s: CreatedAt is zero after CONTENT write", c.ID)
		}
		if c.UpdatedAt.IsZero() {
			t.Errorf("cluster %s: UpdatedAt is zero after CONTENT write", c.ID)
		}
		if d := absDuration(c.CreatedAt.Sub(writeTime)); d > tolerance {
			t.Errorf("cluster %s: CreatedAt %v is not within %v of write time %v", c.ID, c.CreatedAt, tolerance, writeTime)
		}
		if d := absDuration(c.UpdatedAt.Sub(writeTime)); d > tolerance {
			t.Errorf("cluster %s: UpdatedAt %v is not within %v of write time %v", c.ID, c.UpdatedAt, tolerance, writeTime)
		}
	}

	// Primary field-absence gate: raw-row check below. The GetClusters round-trip above
	// is round-trip validation; SurrealDB returning JSON null vs absent both decode to
	// nil pointer in surrealCluster, so the raw check is what catches the omitempty bug.
	rows := rawQueryClusters(t, s, repoID)
	if len(rows) != len(clusters) {
		t.Fatalf("raw query: want %d rows, got %d", len(clusters), len(rows))
	}
	for _, row := range rows {
		if _, ok := row["llm_label"]; ok {
			t.Errorf("raw row has llm_label key when it should be absent: %v", row)
		}
	}
}

// TestReplaceClusters_MixedLLMLabel_Succeeds verifies that a batch containing
// a mix of nil, non-empty, and empty-string LLMLabel values round-trips
// correctly: nil → absent key, "foo" → "foo", "" → "" (empty string is valid
// for option<string> and must not be omitted).
func TestReplaceClusters_MixedLLMLabel_Succeeds(t *testing.T) {
	s := startSurrealContainer(t)
	store := NewSurrealStore(s)
	ctx := context.Background()

	writeTime := time.Now()
	repoID := "repo-ca174-mixed-" + uuid.NewString()

	nilID := uuid.NewString()
	fooID := uuid.NewString()
	emptyID := uuid.NewString()

	clusters := []clustering.Cluster{
		makeTestCluster(nilID, nil),
		makeTestCluster(fooID, stringPtr("foo")),
		makeTestCluster(emptyID, stringPtr("")),
	}
	for i := range clusters {
		clusters[i].RepoID = repoID
	}

	if err := store.ReplaceClusters(ctx, repoID, clusters); err != nil {
		t.Fatalf("ReplaceClusters mixed LLMLabel: %v", err)
	}

	// GetClusters round-trip.
	got, err := store.GetClusters(ctx, repoID)
	if err != nil {
		t.Fatalf("GetClusters: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("GetClusters: want 3 rows, got %d", len(got))
	}

	// Build a label map by EdgeHash suffix (the unique part of EdgeHash we
	// constructed) for reliable lookup regardless of return order.
	byEdge := map[string]*string{}
	byEdgeCluster := map[string]clustering.Cluster{}
	for _, c := range got {
		byEdge[c.EdgeHash] = c.LLMLabel
		byEdgeCluster[c.EdgeHash] = c
	}

	if v, ok := byEdge["deadbeef"+nilID]; !ok {
		t.Errorf("nil cluster not found by EdgeHash")
	} else if v != nil {
		t.Errorf("nil cluster: want LLMLabel nil, got %q", *v)
	}

	if v, ok := byEdge["deadbeef"+fooID]; !ok {
		t.Errorf("foo cluster not found by EdgeHash")
	} else if v == nil || *v != "foo" {
		t.Errorf("foo cluster: want LLMLabel %q, got %v", "foo", v)
	}

	if v, ok := byEdge["deadbeef"+emptyID]; !ok {
		t.Errorf("empty-string cluster not found by EdgeHash")
	} else if v == nil || *v != "" {
		t.Errorf("empty-string cluster: want LLMLabel %q, got %v", "", v)
	}

	// Datetime round-trip: created_at/updated_at must survive the CONTENT
	// write path on v2.2.1 CBOR datetime handling with non-zero values.
	const tolerance = 5 * time.Second
	for _, c := range got {
		if c.CreatedAt.IsZero() {
			t.Errorf("cluster %s: CreatedAt is zero after CONTENT write", c.ID)
		}
		if c.UpdatedAt.IsZero() {
			t.Errorf("cluster %s: UpdatedAt is zero after CONTENT write", c.ID)
		}
		if d := absDuration(c.CreatedAt.Sub(writeTime)); d > tolerance {
			t.Errorf("cluster %s: CreatedAt %v not within %v of write time %v", c.ID, c.CreatedAt, tolerance, writeTime)
		}
		if d := absDuration(c.UpdatedAt.Sub(writeTime)); d > tolerance {
			t.Errorf("cluster %s: UpdatedAt %v not within %v of write time %v", c.ID, c.UpdatedAt, tolerance, writeTime)
		}
	}

	// Primary field-absence gate: raw-row check below. The GetClusters round-trip above
	// is round-trip validation; SurrealDB returning JSON null vs absent both decode to
	// nil pointer in surrealCluster, so the raw check is what catches the omitempty bug.
	rows := rawQueryClusters(t, s, repoID)
	if len(rows) != 3 {
		t.Fatalf("raw query: want 3 rows, got %d", len(rows))
	}
	nilCount := 0
	for _, row := range rows {
		edgeHash, _ := row["edge_hash"].(string)
		if edgeHash == "deadbeef"+nilID {
			nilCount++
			if _, ok := row["llm_label"]; ok {
				t.Errorf("nil cluster raw row has llm_label key: %v", row)
			}
		}
	}
	if nilCount != 1 {
		t.Errorf("expected exactly 1 nil-cluster raw row, found %d", nilCount)
	}
}

// TestSaveClusters_NilLLMLabel_Succeeds verifies that the non-transactional
// SaveClusters path does not emit the "clustering: failed to save cluster"
// warning when LLMLabel is nil, and that each written row has no llm_label
// key when read back via a raw SELECT.
//
// Regression guard for CA-174: the SaveClusters vars map previously passed
// c.LLMLabel (a nil *string) directly, which serialised as JSON null and
// was rejected by SurrealDB for option<string>.
func TestSaveClusters_NilLLMLabel_Succeeds(t *testing.T) {
	s := startSurrealContainer(t)
	store := NewSurrealStore(s)
	ctx := context.Background()

	writeTime := time.Now()
	repoID := "repo-ca174-save-" + uuid.NewString()
	clusters := []clustering.Cluster{
		makeTestCluster(uuid.NewString(), nil),
		makeTestCluster(uuid.NewString(), nil),
	}
	for i := range clusters {
		clusters[i].RepoID = repoID
	}

	// Capture slog output to assert the cluster-specific warn does not fire.
	logBuf := &clusterSyncBuffer{}
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	if err := store.SaveClusters(ctx, repoID, clusters); err != nil {
		t.Fatalf("SaveClusters: unexpected error: %v", err)
	}

	// Assert the cluster-save warn did NOT fire.
	if out := logBuf.String(); strings.Contains(out, "clustering: failed to save cluster") {
		t.Errorf("SaveClusters emitted cluster-save warn for nil LLMLabel:\n%s", out)
	}

	// Field-absence: each row must have no llm_label key.
	rows := rawQueryClusters(t, s, repoID)
	if len(rows) != len(clusters) {
		t.Fatalf("raw query: want %d rows, got %d", len(clusters), len(rows))
	}
	for _, row := range rows {
		if _, ok := row["llm_label"]; ok {
			t.Errorf("SaveClusters raw row has llm_label key when it should be absent: %v", row)
		}
	}

	// Datetime round-trip: created_at/updated_at must survive the SET write
	// path on v2.2.1 CBOR datetime handling with non-zero values.
	got, err := store.GetClusters(ctx, repoID)
	if err != nil {
		t.Fatalf("GetClusters after SaveClusters: %v", err)
	}
	if len(got) != len(clusters) {
		t.Fatalf("GetClusters: want %d rows, got %d", len(clusters), len(got))
	}
	const tolerance = 5 * time.Second
	for _, c := range got {
		if c.CreatedAt.IsZero() {
			t.Errorf("cluster %s: CreatedAt is zero after SET write", c.ID)
		}
		if c.UpdatedAt.IsZero() {
			t.Errorf("cluster %s: UpdatedAt is zero after SET write", c.ID)
		}
		if d := absDuration(c.CreatedAt.Sub(writeTime)); d > tolerance {
			t.Errorf("cluster %s: CreatedAt %v not within %v of write time %v", c.ID, c.CreatedAt, tolerance, writeTime)
		}
		if d := absDuration(c.UpdatedAt.Sub(writeTime)); d > tolerance {
			t.Errorf("cluster %s: UpdatedAt %v not within %v of write time %v", c.ID, c.UpdatedAt, tolerance, writeTime)
		}
	}
}
