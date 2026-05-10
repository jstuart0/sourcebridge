// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

//go:build integration

package db

import (
	"testing"

	"github.com/google/uuid"
	surrealdb "github.com/surrealdb/surrealdb.go"
)

// seedSymbolRow inserts a minimal ca_symbol row and returns the raw symbol ID
// (without table prefix) that UpsertSymbolEmbedding expects.
func seedSymbolRow(t *testing.T, surreal *SurrealDB, repoID string) string {
	t.Helper()
	db := surreal.DB()
	if db == nil {
		t.Fatal("surrealdb not connected")
	}
	symID := uuid.New().String()
	_, err := surrealdb.Query[interface{}](ctx(), db,
		`CREATE ca_symbol SET
			id           = type::thing('ca_symbol', $sid),
			repo_id      = $repo_id,
			file_id      = $file_id,
			name         = $name,
			qualified_name = $qname,
			kind         = $kind,
			language     = $lang,
			file_path    = $fpath,
			start_line   = $start_line,
			end_line     = $end_line,
			signature    = $sig,
			doc_comment  = $doc,
			is_test      = false`,
		map[string]any{
			"sid":        symID,
			"repo_id":    repoID,
			"file_id":    "file-001",
			"name":       "TestFunc",
			"qname":      "pkg.TestFunc",
			"kind":       "function",
			"lang":       "go",
			"fpath":      "pkg/testfunc.go",
			"start_line": 10,
			"end_line":   20,
			"sig":        "func TestFunc()",
			"doc":        "TestFunc is a placeholder.",
		})
	if err != nil {
		t.Fatalf("seedSymbolRow: %v", err)
	}
	return symID
}

// readEmbeddingFields does a raw SELECT of the four option<…> embedding columns
// for the given symbol ID and returns them as a surrealSymbol (subset used).
func readEmbeddingFields(t *testing.T, surreal *SurrealDB, symID string) surrealSymbol {
	t.Helper()
	db := surreal.DB()
	if db == nil {
		t.Fatal("surrealdb not connected")
	}
	rows, err := queryOne[[]surrealSymbol](ctx(), db,
		`SELECT embedding, embedding_model, embedding_dim, embedding_hash
		 FROM ca_symbol
		 WHERE id = type::thing('ca_symbol', $id)
		 LIMIT 1`,
		map[string]any{"id": symID})
	if err != nil {
		t.Fatalf("readEmbeddingFields: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("readEmbeddingFields: no rows for symID=%q", symID)
	}
	return rows[0]
}

// makeVec returns a 768-element []float32 slice with a predictable pattern so
// round-trip values can be spot-checked without comparing all 768 elements.
func makeVec(dim int, seed float32) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = seed + float32(i)*0.001
	}
	return v
}

// TestIntegration_UpsertSymbolEmbedding_RoundTrip is the Phase 2 acceptance gate
// for the SurrealDB v2.6.5 upgrade campaign. It verifies that all four
// option<…> embedding columns (embedding, embedding_model, embedding_dim,
// embedding_hash) survive a full write + read-back cycle without rejection or
// silent truncation.
//
// The writer (UpsertSymbolEmbedding) uses an UPDATE … SET … WHERE pattern with
// explicit bound parameters; all four fields must land as their declared types
// (option<array<float>>, option<string>, option<int>, option<string>) and come
// back intact.
func TestIntegration_UpsertSymbolEmbedding_RoundTrip(t *testing.T) {
	surreal := startSurrealContainer(t)
	store := &SurrealStore{client: surreal}

	const dim = 768
	repoID := "repo-embed-test-" + uuid.New().String()
	symID := seedSymbolRow(t, surreal, repoID)

	vec := makeVec(dim, 0.1)
	const model = "nomic-embed-text"
	const textHash = "sha256:abcdef1234567890"

	if err := store.UpsertSymbolEmbedding(t.Context(), repoID, symID, vec, model, dim, textHash); err != nil {
		t.Fatalf("UpsertSymbolEmbedding: %v", err)
	}

	got := readEmbeddingFields(t, surreal, symID)

	// embedding: option<array<float>> — verify length and a spot check.
	if len(got.Embedding) != dim {
		t.Errorf("embedding length: want %d, got %d", dim, len(got.Embedding))
	} else {
		// Check first and last elements (float32 → float64 in writer, so compare
		// with float64 tolerance).
		wantFirst := float64(vec[0])
		wantLast := float64(vec[dim-1])
		const tol = 1e-6
		if diff := got.Embedding[0] - wantFirst; diff < -tol || diff > tol {
			t.Errorf("embedding[0]: want %v, got %v", wantFirst, got.Embedding[0])
		}
		if diff := got.Embedding[dim-1] - wantLast; diff < -tol || diff > tol {
			t.Errorf("embedding[%d]: want %v, got %v", dim-1, wantLast, got.Embedding[dim-1])
		}
	}

	// embedding_model: option<string>
	if got.EmbeddingModel != model {
		t.Errorf("embedding_model: want %q, got %q", model, got.EmbeddingModel)
	}

	// embedding_dim: option<int>
	if got.EmbeddingDim != dim {
		t.Errorf("embedding_dim: want %d, got %d", dim, got.EmbeddingDim)
	}

	// embedding_hash: option<string>
	if got.EmbeddingHash != textHash {
		t.Errorf("embedding_hash: want %q, got %q", textHash, got.EmbeddingHash)
	}
}

// TestIntegration_UpsertSymbolEmbedding_DimensionGate documents the
// length-gate guard: when the vector length does not match dim, the writer
// returns an error before touching the database. This keeps the HNSW index
// consistent (all rows either have no embedding or a correctly-dimensioned one).
func TestIntegration_UpsertSymbolEmbedding_DimensionGate(t *testing.T) {
	surreal := startSurrealContainer(t)
	store := &SurrealStore{client: surreal}

	repoID := "repo-gate-test-" + uuid.New().String()
	symID := seedSymbolRow(t, surreal, repoID)

	// vec has 3 elements, dim claims 768 → must error.
	shortVec := []float32{0.1, 0.2, 0.3}
	err := store.UpsertSymbolEmbedding(t.Context(), repoID, symID, shortVec, "nomic-embed-text", 768, "hash")
	if err == nil {
		t.Fatal("expected error for dimension mismatch, got nil")
	}

	// The row's embedding fields must remain unset (NONE / zero-value).
	got := readEmbeddingFields(t, surreal, symID)
	if len(got.Embedding) != 0 {
		t.Errorf("embedding should be unset after failed write, got len=%d", len(got.Embedding))
	}
	if got.EmbeddingModel != "" {
		t.Errorf("embedding_model should be empty after failed write, got %q", got.EmbeddingModel)
	}
}
