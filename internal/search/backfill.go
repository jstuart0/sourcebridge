// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// BackfillConfig controls the throughput of the embedding backfill
// loop. The goal is to protect both the embedding worker and, on
// self-hosted deployments, the shared GPU.
type BackfillConfig struct {
	// RPS throttles the rate at which symbols are embedded, measured
	// as symbols per second. 0 → 5 (self-hosted default).
	RPS float64
	// Batch is the embedding-call batch size. We currently embed one
	// at a time; batching is a follow-up once the worker RPC supports
	// it natively. Included here so the config is stable.
	Batch int
	// MaxPerRun caps the number of symbols embedded in a single
	// Run() invocation. 0 → unlimited.
	MaxPerRun int
}

// BackfillProgress is a snapshot of an in-flight backfill loop. It is
// safe to read from any goroutine.
type BackfillProgress struct {
	mu        sync.RWMutex
	Processed int64
	Embedded  int64
	Skipped   int64
	Errored   int64
	StartedAt time.Time
	LastError string
}

// Snapshot returns a copy of the progress state.
func (p *BackfillProgress) Snapshot() BackfillProgress {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return BackfillProgress{
		Processed: p.Processed,
		Embedded:  p.Embedded,
		Skipped:   p.Skipped,
		Errored:   p.Errored,
		StartedAt: p.StartedAt,
		LastError: p.LastError,
	}
}

// Backfiller walks a repo's symbols, embeds the ones that need it,
// and upserts the vectors onto the symbol row. Designed to be run
// either inline at index time or in the background by an admin
// trigger.
type Backfiller struct {
	Store    graph.GraphStore
	Embedder Embedder
	Dim      int
	Config   BackfillConfig
}

// NewBackfiller returns a Backfiller with safe defaults. Passing a
// nil embedder or store produces a Backfiller that is a no-op — useful
// when OSS deployments don't wire the worker in.
func NewBackfiller(store graph.GraphStore, emb Embedder, dim int, cfg BackfillConfig) *Backfiller {
	if cfg.RPS <= 0 {
		cfg.RPS = 5
	}
	if cfg.Batch <= 0 {
		cfg.Batch = 1
	}
	if dim <= 0 {
		dim = 768
	}
	return &Backfiller{Store: store, Embedder: emb, Dim: dim, Config: cfg}
}

// Run embeds all symbols in `repoID` that do not yet have an up-to-
// date embedding for the active model. It respects ctx cancellation
// and the configured RPS. Reports errors but does not abort the full
// run on a single failure — per-symbol errors are recorded in
// progress.LastError.
//
// The `hashFor` callback produces a stable hash for a symbol's
// embedding input so the backfiller can skip work when the hash
// already matches what's stored. If nil, every symbol is re-embedded.
func (b *Backfiller) Run(ctx context.Context, repoID string, progress *BackfillProgress, hashFor func(*graph.StoredSymbol) string) error {
	if b == nil || b.Store == nil || b.Embedder == nil {
		return fmt.Errorf("backfiller not configured")
	}
	upserter, ok := b.Store.(graph.SymbolEmbeddingUpsert)
	if !ok {
		return fmt.Errorf("store does not support symbol embedding upsert")
	}
	if progress != nil {
		progress.mu.Lock()
		progress.StartedAt = time.Now()
		progress.mu.Unlock()
	}

	// Rate limiter: one token every 1/RPS seconds.
	interval := time.Duration(float64(time.Second) / b.Config.RPS)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	syms, _ := b.Store.GetSymbols(repoID, nil, nil, 0, 0)
	slog.Info("search: embedding backfill start", "repo", repoID, "total", len(syms), "model", b.Embedder.Model())

	var done atomic.Int64
	for _, s := range syms {
		if s == nil {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		if b.Config.MaxPerRun > 0 && int(done.Load()) >= b.Config.MaxPerRun {
			break
		}

		wantHash := ""
		if hashFor != nil {
			wantHash = hashFor(s)
		}
		// Skip if the store already has an up-to-date embedding.
		if wantHash != "" && symbolEmbeddingUpToDate(s, b.Embedder.Model(), wantHash) {
			if progress != nil {
				progress.mu.Lock()
				progress.Skipped++
				progress.Processed++
				progress.mu.Unlock()
			}
			done.Add(1)
			continue
		}

		text := SymbolEmbeddingText(s)
		vec, ok := b.Embedder.Embed(ctx, text)
		if progress != nil {
			progress.mu.Lock()
			progress.Processed++
			progress.mu.Unlock()
		}
		if !ok {
			if progress != nil {
				progress.mu.Lock()
				progress.Errored++
				progress.LastError = "embedder unavailable"
				progress.mu.Unlock()
			}
			continue
		}
		if wantHash == "" {
			wantHash = hashString(text)
		}
		if err := upserter.UpsertSymbolEmbedding(repoID, s.ID, vec, b.Embedder.Model(), b.Dim, wantHash); err != nil {
			if progress != nil {
				progress.mu.Lock()
				progress.Errored++
				progress.LastError = err.Error()
				progress.mu.Unlock()
			}
			continue
		}
		if progress != nil {
			progress.mu.Lock()
			progress.Embedded++
			progress.mu.Unlock()
		}
		done.Add(1)
	}
	slog.Info("search: embedding backfill done", "repo", repoID, "processed", done.Load())
	return nil
}

// SymbolEmbeddingText returns the canonical string fed to the
// embedder for a given symbol. Stable — changing this string changes
// the embedding and forces re-embedding of every symbol in the repo.
// That is acceptable at a major generation swap but should not happen
// casually.
func SymbolEmbeddingText(s *graph.StoredSymbol) string {
	if s == nil {
		return ""
	}
	sig := s.Signature
	doc := s.DocComment
	return s.QualifiedName + " " + sig + " " + doc
}

// symbolEmbeddingUpToDate is a placeholder that will be extended once
// the in-memory store exposes the stored hash. For the Surreal path
// the row's embedding_hash is already available via GetSymbolsByIDs.
func symbolEmbeddingUpToDate(_ *graph.StoredSymbol, _ string, _ string) bool {
	// Intentionally conservative: always embed. The delta pipeline
	// already narrows the set of symbols we reach for. A later pass
	// can plug a real hash comparison here once the store surfaces
	// embedding_hash per symbol.
	return false
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
