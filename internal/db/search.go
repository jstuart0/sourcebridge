// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/surrealdb/surrealdb.go/pkg/models"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// Compile-time assertions that SurrealStore implements the optional
// ranked-search capability interfaces. If the FTS or vector indexes
// aren't yet migrated the runtime query simply returns no rows, which
// the search service treats as "backend available but empty".
var (
	_ graph.FTSSymbolSearch       = (*SurrealStore)(nil)
	_ graph.VectorSymbolSearch    = (*SurrealStore)(nil)
	_ graph.SymbolEmbeddingUpsert = (*SurrealStore)(nil)
)

// ftsSymbolRow is the shape returned by the ranked-search query. It
// extends the normal surrealSymbol projection with the fused score.
type ftsSymbolRow struct {
	surrealSymbol
	Score float64 `json:"__score"`
}

// sanitizeFTSQuery strips a small set of characters that are safe to
// drop from user queries before handing them to SurrealDB's search
// operators. The query is already passed as a bound parameter so
// injection via SQL is not possible; this is about denying users the
// ability to accidentally weaponize analyzer-level operators.
//
// Chars removed: backslash, double-quote, and the angle-bracket
// operator pair (<| |>) which would otherwise look like KNN syntax.
func sanitizeFTSQuery(q string) string {
	q = strings.TrimSpace(q)
	// Cap length (see plan Security section — user query length is
	// capped by the service; this is belt-and-braces for the DB).
	if len(q) > 512 {
		q = q[:512]
	}
	replacer := strings.NewReplacer(
		"\\", " ",
		"\"", " ",
		"<|", " ",
		"|>", " ",
	)
	return strings.TrimSpace(replacer.Replace(q))
}

// SearchSymbolsFTS runs a BM25-ranked full-text query against the FTS
// index defined in migration 033. Returns results sorted by combined
// BM25 score across name / qualified_name / doc_comment / signature,
// with rank populated 0..N-1.
//
// If the FTS index is missing or the query fails the result is nil —
// callers must treat nil as "backend unavailable" and degrade to the
// lexical substring path.
func (s *SurrealStore) SearchSymbolsFTS(ctx context.Context,
	repoID, query string,
	filters graph.SymbolSearchFilters,
	limit int,
) []*graph.RankedSymbol {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	q := sanitizeFTSQuery(query)
	if q == "" || repoID == "" {
		return nil
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	vars := map[string]any{
		"repo_id": repoID,
		"q":       q,
	}
	where := "repo_id = $repo_id AND (name @1@ $q OR qualified_name @2@ $q OR doc_comment @3@ $q OR signature @4@ $q)"
	if filters.Kind != nil && *filters.Kind != "" {
		where += " AND kind = $kind"
		vars["kind"] = *filters.Kind
	}
	if filters.Language != nil && *filters.Language != "" {
		where += " AND language = $lang"
		vars["lang"] = *filters.Language
	}
	if filters.FilePath != nil && *filters.FilePath != "" {
		where += " AND file_path = $fp"
		vars["fp"] = *filters.FilePath
	}

	sql := fmt.Sprintf(
		`SELECT *,
		        (search::score(1) + search::score(2) + search::score(3) + search::score(4)) AS __score
		 FROM ca_symbol
		 WHERE %s
		 ORDER BY __score DESC
		 LIMIT %d`,
		where, limit)

	rows, err := queryOne[[]ftsSymbolRow](ctx, db, sql, vars)
	if err != nil {
		return nil
	}

	out := make([]*graph.RankedSymbol, 0, len(rows))
	for i := range rows {
		sym := rows[i].toStoredSymbol()
		out = append(out, &graph.RankedSymbol{
			Symbol: sym,
			Score:  rows[i].Score,
			Rank:   i,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Vector search (Phase 2) — defined here so the FTS + vector capability
// surface lives in one file. When migration 034 is not yet applied the
// query falls through to an empty result and the router skips vector.
// ---------------------------------------------------------------------------

// vectorSymbolRow is the projection used by SearchSymbolsVector.
//
// Flat (NOT anonymous-embedded) layout: fxamacker/cbor — used by the
// SurrealDB Go SDK — does not promote fields from anonymously-embedded
// structs the way encoding/json does. An anonymous `surrealSymbol`
// embed left every promoted field empty after unmarshal, producing
// "shape-empty" hits with valid similarity scores but no entity_id /
// title / file_path. Flattening fixes the unmarshal.
type vectorSymbolRow struct {
	ID             *models.RecordID `json:"id,omitempty"`
	RepoID         string           `json:"repo_id"`
	FileID         string           `json:"file_id"`
	Name           string           `json:"name"`
	QualifiedName  string           `json:"qualified_name"`
	Kind           string           `json:"kind"`
	Language       string           `json:"language"`
	FilePath       string           `json:"file_path"`
	StartLine      int              `json:"start_line"`
	EndLine        int              `json:"end_line"`
	Signature      string           `json:"signature"`
	DocComment     string           `json:"doc_comment"`
	IsTest         bool             `json:"is_test"`
	EmbeddingModel string           `json:"embedding_model,omitempty"`
	EmbeddingDim   int              `json:"embedding_dim,omitempty"`
	EmbeddingHash  string           `json:"embedding_hash,omitempty"`
	Similarity     float64          `json:"__similarity"`
}

func (r *vectorSymbolRow) toStoredSymbol() *graph.StoredSymbol {
	return &graph.StoredSymbol{
		ID:            recordIDString(r.ID),
		RepoID:        r.RepoID,
		FileID:        r.FileID,
		Name:          r.Name,
		QualifiedName: r.QualifiedName,
		Kind:          r.Kind,
		Language:      r.Language,
		FilePath:      r.FilePath,
		StartLine:     r.StartLine,
		EndLine:       r.EndLine,
		Signature:     r.Signature,
		DocComment:    r.DocComment,
		IsTest:        r.IsTest,
	}
}

// SearchSymbolsVector runs an HNSW KNN query against ca_symbol.embedding
// and returns matches ordered by cosine similarity. The `queryVec`
// dimension must match the index dimension defined in migration 034;
// mismatched dimensions return nil so the service can degrade.
func (s *SurrealStore) SearchSymbolsVector(ctx context.Context,
	repoID string,
	queryVec []float32,
	filters graph.SymbolSearchFilters,
	limit int,
) []*graph.VectorSymbolMatch {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	if repoID == "" || len(queryVec) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	// Convert []float32 to []float64 for SurrealDB (its number type is
	// float64 on the wire; sending float32 causes decode issues).
	vec := make([]float64, len(queryVec))
	for i, v := range queryVec {
		vec[i] = float64(v)
	}

	vars := map[string]any{
		"repo_id": repoID,
		"qv":      vec,
	}
	where := "repo_id = $repo_id AND embedding IS NOT NONE"
	if filters.Kind != nil && *filters.Kind != "" {
		where += " AND kind = $kind"
		vars["kind"] = *filters.Kind
	}
	if filters.Language != nil && *filters.Language != "" {
		where += " AND language = $lang"
		vars["lang"] = *filters.Language
	}
	if filters.FilePath != nil && *filters.FilePath != "" {
		where += " AND file_path = $fp"
		vars["fp"] = *filters.FilePath
	}

	// Explicit field list (NOT `SELECT *`): the embedding column is a 768-dim
	// vector; including it in the response payload bloats CBOR decode by
	// ~6 KB per row and was empirically observed to leave the surrealSymbol
	// fields (id, name, file_path, etc.) empty after unmarshal — the
	// resulting candidates were "shape-empty" with only the similarity
	// score populated, breaking search hydration and QA evidence.
	sql := fmt.Sprintf(
		`SELECT id, repo_id, file_id, name, qualified_name, kind, language,
		        file_path, start_line, end_line, signature, doc_comment, is_test,
		        embedding_model, embedding_dim, embedding_hash,
		        vector::similarity::cosine(embedding, $qv) AS __similarity
		 FROM ca_symbol
		 WHERE %s
		 ORDER BY __similarity DESC
		 LIMIT %d`,
		where, limit)

	rows, err := queryOne[[]vectorSymbolRow](ctx, db, sql, vars)
	if err != nil {
		return nil
	}

	out := make([]*graph.VectorSymbolMatch, 0, len(rows))
	for i := range rows {
		sym := rows[i].toStoredSymbol()
		out = append(out, &graph.VectorSymbolMatch{
			Symbol:     sym,
			Similarity: rows[i].Similarity,
			Rank:       i,
		})
	}
	return out
}

// UpsertSymbolEmbedding writes the vector onto the ca_symbol row. The
// HNSW index defined in migration 034 is maintained by SurrealDB on
// write. Mismatched dimensions return an error — callers must refuse
// to mix models (see plan §Data Consistency Rules).
func (s *SurrealStore) UpsertSymbolEmbedding(ctx context.Context,
	repoID, symbolID string,
	vector []float32,
	model string,
	dim int,
	textHash string,
) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("surrealdb not connected")
	}
	if symbolID == "" || repoID == "" {
		return fmt.Errorf("repoID and symbolID required")
	}
	if dim <= 0 || len(vector) != dim {
		return fmt.Errorf("embedding dimension mismatch: got %d want %d", len(vector), dim)
	}

	vec := make([]float64, len(vector))
	for i, v := range vector {
		vec[i] = float64(v)
	}

	// The ID may already be a RecordID string like "ca_symbol:xyz" or
	// a raw uuid. UPSERT ... WHERE scopes the update to the right row.
	vars := map[string]any{
		"id":        symbolID,
		"repo_id":   repoID,
		"embedding": vec,
		"model":     model,
		"dim":       dim,
		"hash":      textHash,
	}
	sql := `UPDATE ca_symbol
	        SET embedding = $embedding,
	            embedding_model = $model,
	            embedding_dim = $dim,
	            embedding_hash = $hash
	        WHERE repo_id = $repo_id AND id = type::thing('ca_symbol', $id)`
	if _, err := queryOne[[]map[string]interface{}](ctx, db, sql, vars); err != nil {
		return fmt.Errorf("upsert symbol embedding: %w", err)
	}
	return nil
}
