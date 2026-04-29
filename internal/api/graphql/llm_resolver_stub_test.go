// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

// This file is _test.go-suffixed so it only compiles into the test
// binary. It exposes a stub LLM resolver other tests in this package
// wire into the graphql.Resolver so production-shape code (which calls
// resolveLLMProviderForOp) sees a non-empty provider during tests.
//
// R3 followups B1: the orchestrator now hard-blocks LLM-backed
// enqueues with empty provider. Tests that exercise enqueue paths
// through the resolver helper must wire a non-empty provider; this
// helper centralizes that wiring.

import (
	"context"

	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
)

// stubLLMResolver returns a fixed Snapshot for every Resolve call.
// Sufficient for tests that need the orchestrator's empty-provider
// guard to pass; tests that exercise resolver behavior itself should
// build a real DefaultResolver against a fake store.
type stubLLMResolver struct {
	provider string
	model    string
}

// newStubLLMResolver returns a resolver that always returns
// {Provider: "test", Model: "test-model"}.
func newStubLLMResolver() *stubLLMResolver {
	return &stubLLMResolver{provider: "test", model: "test-model"}
}

func (s *stubLLMResolver) Resolve(_ context.Context, _ string, _ string) (resolution.Snapshot, error) {
	return resolution.Snapshot{
		Provider:    s.provider,
		Model:       s.model,
		BaseURL:     "",
		APIKey:      "",
		TimeoutSecs: 60,
	}, nil
}

func (s *stubLLMResolver) InvalidateLocal() {}
