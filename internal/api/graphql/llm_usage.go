// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"strings"

	commonv1 "github.com/sourcebridge/sourcebridge/gen/go/common/v1"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
)

// providerFromUsage returns the LLM provider name for a usage record, with fallback chain:
//  1. usage.Provider if set (the authoritative source — set by the worker)
//  2. heuristic: prefix of usage.Model before the first '/' (e.g. "openai/gpt-4o" → "openai")
//  3. model-name prefix heuristics for well-known vendors
//  4. "unknown" — never returns the literal "llm" (which was the corrupt sentinel before).
//
// This is the SINGLE source of truth for resolving the provider string for metrics persistence.
// Every site that previously hardcoded Provider: "llm" or Provider: usage.Model should
// use this helper instead.
func providerFromUsage(usage *commonv1.LLMUsage) string {
	if usage == nil {
		return "unknown"
	}
	// Defense-in-depth: treat the historic "llm" sentinel as missing.
	// Well-behaved workers now emit "" or the real provider name; this guard
	// protects against any callsite that was missed in the Python cleanup.
	if usage.Provider != "" && usage.Provider != "llm" {
		return usage.Provider
	}
	if idx := strings.Index(usage.Model, "/"); idx > 0 {
		return usage.Model[:idx]
	}
	// Known model-name prefixes without a vendor-prefix in the model string:
	if strings.HasPrefix(usage.Model, "gpt-") || strings.HasPrefix(usage.Model, "o1-") ||
		strings.HasPrefix(usage.Model, "o3-") || strings.HasPrefix(usage.Model, "o4-") {
		return "openai"
	}
	if strings.HasPrefix(usage.Model, "claude-") {
		return "anthropic"
	}
	if strings.HasPrefix(usage.Model, "gemini-") {
		return "google"
	}
	return "unknown"
}

// storeLLMUsage persists an LLM usage record using the canonical provider resolution.
// This is the helper that Slice 1b's call sites will delegate to, replacing all
// ad-hoc StoreLLMUsage calls that previously hardcoded Provider: "llm" or
// Provider: usage.Model.
func storeLLMUsage(store graphstore.GraphStore, repoID string, usage *commonv1.LLMUsage, operation string) {
	if store == nil || usage == nil {
		return
	}
	op := operation
	if op == "" {
		op = usage.Operation
	}
	store.StoreLLMUsage(context.Background(), &graphstore.LLMUsageRecord{
		RepoID:       repoID,
		Operation:    op,
		Provider:     providerFromUsage(usage),
		Model:        usage.Model,
		InputTokens:  int(usage.InputTokens),
		OutputTokens: int(usage.OutputTokens),
	})
}
