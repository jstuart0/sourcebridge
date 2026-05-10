// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"testing"

	commonv1 "github.com/sourcebridge/sourcebridge/gen/go/common/v1"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
)

func TestProviderFromUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		usage    *commonv1.LLMUsage
		expected string
	}{
		{
			name:     "nil usage",
			usage:    nil,
			expected: "unknown",
		},
		{
			name:     "provider explicitly set",
			usage:    &commonv1.LLMUsage{Provider: "anthropic", Model: "claude-3-opus"},
			expected: "anthropic",
		},
		{
			name:     "provider set takes precedence over model heuristic",
			usage:    &commonv1.LLMUsage{Provider: "ollama", Model: "openai/gpt-4o"},
			expected: "ollama",
		},
		{
			name:     "vendor-prefixed model openai",
			usage:    &commonv1.LLMUsage{Model: "openai/gpt-4o"},
			expected: "openai",
		},
		{
			name:     "vendor-prefixed model anthropic",
			usage:    &commonv1.LLMUsage{Model: "anthropic/claude-3-opus"},
			expected: "anthropic",
		},
		{
			name:     "model name heuristic gpt-",
			usage:    &commonv1.LLMUsage{Model: "gpt-4o"},
			expected: "openai",
		},
		{
			name:     "model name heuristic o1-",
			usage:    &commonv1.LLMUsage{Model: "o1-mini"},
			expected: "openai",
		},
		{
			name:     "model name heuristic o3-",
			usage:    &commonv1.LLMUsage{Model: "o3-mini"},
			expected: "openai",
		},
		{
			name:     "model name heuristic o4-",
			usage:    &commonv1.LLMUsage{Model: "o4-mini"},
			expected: "openai",
		},
		{
			name:     "model name heuristic claude-",
			usage:    &commonv1.LLMUsage{Model: "claude-3-opus"},
			expected: "anthropic",
		},
		{
			name:     "model name heuristic gemini-",
			usage:    &commonv1.LLMUsage{Model: "gemini-1.5-pro"},
			expected: "google",
		},
		{
			name:     "empty model",
			usage:    &commonv1.LLMUsage{Model: ""},
			expected: "unknown",
		},
		{
			name:     "unknown model no heuristic match",
			usage:    &commonv1.LLMUsage{Model: "llama3-8b"},
			expected: "unknown",
		},
		{
			name:     "corrupt legacy sentinel never returned",
			usage:    &commonv1.LLMUsage{Model: "some-model"},
			expected: "unknown", // not "llm"
		},
		{
			// Defense-in-depth: if Python missed a callsite and "llm" is set
			// explicitly in Provider, Go falls through to model heuristic.
			name:     "explicit llm sentinel falls through to model heuristic openai",
			usage:    &commonv1.LLMUsage{Provider: "llm", Model: "gpt-4o"},
			expected: "openai",
		},
		{
			name:     "explicit llm sentinel falls through to model heuristic anthropic",
			usage:    &commonv1.LLMUsage{Provider: "llm", Model: "claude-3-opus"},
			expected: "anthropic",
		},
		{
			name:     "explicit llm sentinel with unknown model yields unknown not llm",
			usage:    &commonv1.LLMUsage{Provider: "llm", Model: "qwen3:32b"},
			expected: "unknown",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := providerFromUsage(tc.usage)
			if got != tc.expected {
				t.Errorf("providerFromUsage(%v) = %q, want %q", tc.usage, got, tc.expected)
			}
		})
	}
}

// TestStoreLLMUsage_ProviderResolution verifies the three key scenarios from
// the Slice 1b spec: explicit provider, model-heuristic fallback, and empty.
func TestStoreLLMUsage_ProviderResolution(t *testing.T) {
	t.Parallel()

	const repoID = "test-repo-123"
	const opLabel = "test_operation"

	tests := []struct {
		name             string
		usage            *commonv1.LLMUsage
		operation        string
		wantProvider     string
		wantModel        string
		wantOperation    string
		wantInputTokens  int
		wantOutputTokens int
	}{
		{
			name: "explicit provider flows through",
			usage: &commonv1.LLMUsage{
				Provider:     "openai",
				Model:        "gpt-4o",
				InputTokens:  100,
				OutputTokens: 50,
				Operation:    "ask",
			},
			operation:        "",
			wantProvider:     "openai",
			wantModel:        "gpt-4o",
			wantOperation:    "ask",
			wantInputTokens:  100,
			wantOutputTokens: 50,
		},
		{
			name: "empty provider + gpt-4o model resolves to openai via heuristic",
			usage: &commonv1.LLMUsage{
				Provider:     "",
				Model:        "gpt-4o",
				InputTokens:  200,
				OutputTokens: 75,
				Operation:    "review",
			},
			operation:        "",
			wantProvider:     "openai",
			wantModel:        "gpt-4o",
			wantOperation:    "review",
			wantInputTokens:  200,
			wantOutputTokens: 75,
		},
		{
			name: "empty provider + unknown model resolves to unknown",
			usage: &commonv1.LLMUsage{
				Provider:     "",
				Model:        "",
				InputTokens:  10,
				OutputTokens: 5,
				Operation:    "",
			},
			operation:        opLabel,
			wantProvider:     "unknown",
			wantModel:        "",
			wantOperation:    opLabel,
			wantInputTokens:  10,
			wantOutputTokens: 5,
		},
		{
			name: "operation override takes precedence over usage.Operation",
			usage: &commonv1.LLMUsage{
				Provider:  "anthropic",
				Model:     "claude-3-opus",
				Operation: "original_op",
			},
			operation:     "overridden_op",
			wantProvider:  "anthropic",
			wantModel:     "claude-3-opus",
			wantOperation: "overridden_op",
		},
		{
			name:  "nil usage is a no-op",
			usage: nil,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := graphstore.NewStore()
			storeLLMUsage(store, repoID, tc.usage, tc.operation)

			records := store.GetLLMUsage(t.Context(), repoID, 10)
			if tc.usage == nil {
				if len(records) != 0 {
					t.Errorf("nil usage: expected 0 records, got %d", len(records))
				}
				return
			}
			if len(records) != 1 {
				t.Fatalf("expected 1 record, got %d", len(records))
			}
			r := records[0]
			if r.RepoID != repoID {
				t.Errorf("RepoID = %q, want %q", r.RepoID, repoID)
			}
			if r.Provider != tc.wantProvider {
				t.Errorf("Provider = %q, want %q", r.Provider, tc.wantProvider)
			}
			if r.Model != tc.wantModel {
				t.Errorf("Model = %q, want %q", r.Model, tc.wantModel)
			}
			if r.Operation != tc.wantOperation {
				t.Errorf("Operation = %q, want %q", r.Operation, tc.wantOperation)
			}
			if r.InputTokens != tc.wantInputTokens {
				t.Errorf("InputTokens = %d, want %d", r.InputTokens, tc.wantInputTokens)
			}
			if r.OutputTokens != tc.wantOutputTokens {
				t.Errorf("OutputTokens = %d, want %d", r.OutputTokens, tc.wantOutputTokens)
			}
			// Verify the corrupt "llm" sentinel is never stored.
			if r.Provider == "llm" {
				t.Errorf("Provider stored the corrupt sentinel %q — this is the bug we fixed", r.Provider)
			}
		})
	}
}
