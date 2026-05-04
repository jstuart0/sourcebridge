// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"testing"

	commonv1 "github.com/sourcebridge/sourcebridge/gen/go/common/v1"
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
