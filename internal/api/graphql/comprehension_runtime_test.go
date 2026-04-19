// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/featureflags"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
)

func newComprehensionMutationResolver(t *testing.T, flags featureflags.Flags) *mutationResolver {
	t.Helper()
	jobStore := llm.NewMemStore()
	orch := orchestrator.New(jobStore, orchestrator.Config{MaxConcurrency: 2})
	t.Cleanup(func() { _ = orch.Shutdown(time.Second) })
	return &mutationResolver{
		&Resolver{
			Orchestrator:       orch,
			ComprehensionStore: comprehension.NewMemStore(),
			Flags:              flags,
		},
	}
}

func TestUpdateComprehensionSettingsReconfiguresOrchestratorWhenEnabled(t *testing.T) {
	resolver := newComprehensionMutationResolver(t, featureflags.Flags{RuntimeReconfigure: true})
	maxConcurrency := 5
	scopeKey := "default"

	_, err := resolver.UpdateComprehensionSettings(context.Background(), UpdateComprehensionSettingsInput{
		ScopeType:      "workspace",
		ScopeKey:       &scopeKey,
		MaxConcurrency: &maxConcurrency,
	})
	if err != nil {
		t.Fatalf("update comprehension settings failed: %v", err)
	}
	if got := resolver.Orchestrator.MaxConcurrency(); got != 5 {
		t.Fatalf("expected orchestrator max concurrency 5, got %d", got)
	}
}

func TestUpdateComprehensionSettingsLeavesOrchestratorUnchangedWhenDisabled(t *testing.T) {
	resolver := newComprehensionMutationResolver(t, featureflags.Flags{})
	maxConcurrency := 5
	scopeKey := "default"

	_, err := resolver.UpdateComprehensionSettings(context.Background(), UpdateComprehensionSettingsInput{
		ScopeType:      "workspace",
		ScopeKey:       &scopeKey,
		MaxConcurrency: &maxConcurrency,
	})
	if err != nil {
		t.Fatalf("update comprehension settings failed: %v", err)
	}
	if got := resolver.Orchestrator.MaxConcurrency(); got != 2 {
		t.Fatalf("expected orchestrator max concurrency to remain 2, got %d", got)
	}
}
