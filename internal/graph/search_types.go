// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graph

import "context"

// RankedSymbol pairs a stored symbol with an opaque relevance score
// produced by a ranked-search backend (BM25, vector similarity, etc.).
//
// Scores from different backends are not directly comparable — they are
// consumed by reciprocal-rank fusion in internal/search, which only uses
// per-backend rank. Rank is the 0-based position within a single
// backend's result list.
type RankedSymbol struct {
	Symbol *StoredSymbol
	Score  float64
	Rank   int
}

// VectorSymbolMatch is the equivalent of RankedSymbol for vector search,
// carrying a cosine similarity (higher is more similar).
type VectorSymbolMatch struct {
	Symbol     *StoredSymbol
	Similarity float64
	Rank       int
}

// SymbolSearchFilters are optional attribute filters applied alongside
// any ranked symbol-search query. All filters are AND-composed.
type SymbolSearchFilters struct {
	Kind     *string // exact match on StoredSymbol.Kind
	Language *string // exact match on StoredSymbol.Language
	FilePath *string // exact match on StoredSymbol.FilePath
}

// FTSSymbolSearch is an optional capability interface. A GraphStore
// that implements it exposes BM25-ranked symbol search. The
// internal/search service treats the absence of this capability as
// "lexical backend not available" and degrades gracefully to
// substring matching via the existing GetSymbols path.
type FTSSymbolSearch interface {
	SearchSymbolsFTS(
		ctx context.Context,
		repoID, query string,
		filters SymbolSearchFilters,
		limit int,
	) []*RankedSymbol
}

// VectorSymbolSearch is an optional capability interface. A GraphStore
// that implements it exposes ANN vector similarity search over symbol
// embeddings. Absence of this capability is treated as "vector backend
// not available" and the router skips the vector arm entirely.
type VectorSymbolSearch interface {
	SearchSymbolsVector(
		ctx context.Context,
		repoID string,
		queryVec []float32,
		filters SymbolSearchFilters,
		limit int,
	) []*VectorSymbolMatch
}

// SymbolEmbeddingUpsert is an optional capability interface. A
// GraphStore that implements it accepts symbol-embedding dual-writes
// driven by the indexer / delta pipeline / backfill job. The embedding
// is stored on the symbol row (or an adjacent table) so it is
// queryable by the HNSW index.
type SymbolEmbeddingUpsert interface {
	UpsertSymbolEmbedding(
		ctx context.Context,
		repoID, symbolID string,
		vector []float32,
		model string,
		dim int,
		textHash string,
	) error
}
