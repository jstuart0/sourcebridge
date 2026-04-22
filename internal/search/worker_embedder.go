// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package search

import (
	"context"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	"github.com/sourcebridge/sourcebridge/internal/worker"
)

// WorkerEmbedder adapts the gRPC worker.Client to the Embedder
// interface used by CachedEmbedder / VectorAdapter.
//
// The default model string follows the plan's Phase 2 recommendation:
// nomic-embed-text for Ollama, text-embedding-3-small for
// OpenAI-compatible cloud backends. Operators override via config.
type WorkerEmbedder struct {
	Client *worker.Client
	// ModelID is the embedding model identifier forwarded to the
	// worker. The same ID is tagged onto stored embeddings via
	// UpsertSymbolEmbedding so cache keys and index generations
	// invalidate on model change.
	ModelID string
}

// NewWorkerEmbedder returns a WorkerEmbedder with safe defaults. A
// nil client produces an embedder that always reports unavailable,
// so call sites can hand it directly into NewCachedEmbedder without
// special-casing OSS / no-worker deployments.
func NewWorkerEmbedder(c *worker.Client, modelID string) *WorkerEmbedder {
	if modelID == "" {
		modelID = "nomic-embed-text"
	}
	return &WorkerEmbedder{Client: c, ModelID: modelID}
}

func (e *WorkerEmbedder) Model() string {
	if e == nil {
		return ""
	}
	return e.ModelID
}

func (e *WorkerEmbedder) Embed(ctx context.Context, query string) ([]float32, bool) {
	if e == nil || e.Client == nil {
		return nil, false
	}
	resp, err := e.Client.GenerateEmbedding(ctx, &reasoningv1.GenerateEmbeddingRequest{
		Text:  query,
		Model: e.ModelID,
	})
	if err != nil {
		return nil, false
	}
	if resp == nil || resp.Embedding == nil {
		return nil, false
	}
	vec := resp.Embedding.Vector
	if len(vec) == 0 {
		return nil, false
	}
	return vec, true
}
