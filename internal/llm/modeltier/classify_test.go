// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package modeltier_test

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
)

// classifyCase is one row in the ClassifyByPattern test table.
type classifyCase struct {
	provider string
	model    string
	wantTier modeltier.QualityGateTier
	// wantSource is optional — empty string skips the source assertion.
	wantSource string
}

// TestClassifyByPattern is the canonical table-driven test for
// ClassifyByPattern. It covers all cases called out in D10 of the CA-150
// plan, including overlapping prefix ordering and Ollama size parsing.
func TestClassifyByPattern(t *testing.T) {
	cases := []classifyCase{
		// ── OpenAI hosted ────────────────────────────────────────────────────
		// gpt-4o-mini BEFORE gpt-4o (prefix ordering)
		{"openai", "gpt-4o-mini", modeltier.TierMid, "pattern"},
		{"openai", "gpt-4o", modeltier.TierFrontier, "pattern"},
		{"openai", "gpt-4-turbo", modeltier.TierFrontier, "pattern"},
		// GPT-4.1 family
		{"openai", "gpt-4.1", modeltier.TierFrontier, "pattern"},
		{"openai", "gpt-4.1-mini", modeltier.TierMid, "pattern"},
		{"openai", "gpt-4.1-nano", modeltier.TierMid, "pattern"},
		// Reasoning models — mini/nano before base
		{"openai", "o1-mini", modeltier.TierMid, "pattern"},
		{"openai", "o1", modeltier.TierFrontier, "pattern"},
		{"openai", "o3-mini", modeltier.TierMid, "pattern"},
		{"openai", "o3", modeltier.TierFrontier, "pattern"},
		{"openai", "o4-mini", modeltier.TierMid, "pattern"},

		// ── Anthropic hosted (fast path — all frontier) ───────────────────
		{"anthropic", "claude-opus-4-7", modeltier.TierFrontier, "pattern"},
		{"anthropic", "claude-sonnet-4", modeltier.TierFrontier, "pattern"},
		{"anthropic", "claude-haiku-4", modeltier.TierFrontier, "pattern"},
		{"anthropic", "claude-opus-3-5", modeltier.TierFrontier, "pattern"},

		// ── Gemini provider ───────────────────────────────────────────────
		{"gemini", "gemini-1.5-pro", modeltier.TierFrontier, "pattern"},
		{"gemini", "gemini-1.5-flash", modeltier.TierMid, "pattern"},
		{"gemini", "gemini-2.0-flash", modeltier.TierMid, "pattern"},
		{"gemini", "gemini-ultra", modeltier.TierFrontier, "pattern"},
		{"gemini", "gemini-nano", modeltier.TierLocal, "pattern"},

		// ── Ollama: size-parser cases (D10 spec) ──────────────────────────
		// qwen3:32b → mid (32 ≥ 30, < 70)
		{"ollama", "qwen3:32b", modeltier.TierMid, "pattern"},
		// qwen3:7b → local (7 < 30)
		{"ollama", "qwen3:7b", modeltier.TierLocal, "pattern"},
		// qwen3.5:4b → local
		{"ollama", "qwen3.5:4b", modeltier.TierLocal, "pattern"},
		// llama3.1:70b → frontier (70 ≥ 70)
		{"ollama", "llama3.1:70b", modeltier.TierFrontier, "pattern"},
		// Quantized suffix stripped before parse: qwen3:32b-q4_K_M → 32b → mid
		{"ollama", "qwen3:32b-q4_K_M", modeltier.TierMid, "pattern"},
		// fp16 suffix: llama3.1:70b-instruct-fp16 → 70b → frontier
		{"ollama", "llama3.1:70b-instruct-fp16", modeltier.TierFrontier, "pattern"},
		// phi4:14b-q6_K → 14b → local
		{"ollama", "phi4:14b-q6_K", modeltier.TierLocal, "pattern"},
		// MoE model qwen3:30b-a3b — total params 30B → mid
		{"ollama", "qwen3:30b-a3b", modeltier.TierMid, "pattern"},
		// Embedding model — no size token, embedding fast path
		{"ollama", "nomic-embed-text", modeltier.TierLocal, "pattern"},
		// Bare "llama" (no size token) → local
		{"ollama", "llama", modeltier.TierLocal, "pattern"},

		// ── vLLM, llama-cpp, sglang, lmstudio (same size logic) ──────────
		{"vllm", "meta-llama/llama-3-70b-instruct", modeltier.TierFrontier, "pattern"},
		{"llama-cpp", "phi4:14b", modeltier.TierLocal, "pattern"},
		{"sglang", "qwen3:32b", modeltier.TierMid, "pattern"},
		{"lmstudio", "llama3.1:8b", modeltier.TierLocal, "pattern"},

		// ── OpenRouter prefix-strip ───────────────────────────────────────
		// google/ → gemini classifier
		{"openrouter", "google/gemini-1.5-pro", modeltier.TierFrontier, "pattern"},
		{"openrouter", "google/gemini-1.5-flash", modeltier.TierMid, "pattern"},
		// anthropic/ → frontier fast path
		{"openrouter", "anthropic/claude-opus-4-7", modeltier.TierFrontier, "pattern"},
		// openai/ → openai classifier
		{"openrouter", "openai/gpt-4o-mini", modeltier.TierMid, "pattern"},
		{"openrouter", "openai/gpt-4o", modeltier.TierFrontier, "pattern"},
		// meta/ → generic (no size tag → fallback)
		{"openrouter", "meta/llama3", modeltier.TierLocal, ""},

		// ── Unknown provider ──────────────────────────────────────────────
		// Unknown provider + known claude family name
		{"custom", "claude-opus-4-7", modeltier.TierFrontier, "pattern"},
		// Completely unknown → fallback
		{"unknown-provider", "some-proprietary-model", modeltier.TierLocal, "fallback"},
	}

	for _, c := range cases {
		t.Run(c.provider+"/"+c.model, func(t *testing.T) {
			got := modeltier.ClassifyByPattern(c.provider, c.model)
			if got.Tier != c.wantTier {
				t.Errorf("ClassifyByPattern(%q, %q).Tier = %q, want %q",
					c.provider, c.model, got.Tier, c.wantTier)
			}
			if c.wantSource != "" && got.Source != c.wantSource {
				t.Errorf("ClassifyByPattern(%q, %q).Source = %q, want %q",
					c.provider, c.model, got.Source, c.wantSource)
			}
			// Source must never be empty for any classified result.
			if got.Source == "" {
				t.Errorf("ClassifyByPattern(%q, %q).Source is empty",
					c.provider, c.model)
			}
		})
	}
}

// TestClassifyByPattern_OverlappingPrefixOrdering ensures the most-specific
// prefix wins in the OpenAI model family. These are the exact cases that
// were broken in the r1 plan (D10 rationale).
func TestClassifyByPattern_OverlappingPrefixOrdering(t *testing.T) {
	// gpt-4o-mini must be mid, not frontier (would fail if "gpt-4o" matched first)
	got := modeltier.ClassifyByPattern("openai", "gpt-4o-mini")
	if got.Tier != modeltier.TierMid {
		t.Errorf("gpt-4o-mini: want TierMid, got %q (prefix ordering bug)", got.Tier)
	}

	// o1-mini must be mid, not frontier (would fail if "o1" matched first)
	got = modeltier.ClassifyByPattern("openai", "o1-mini")
	if got.Tier != modeltier.TierMid {
		t.Errorf("o1-mini: want TierMid, got %q (prefix ordering bug)", got.Tier)
	}

	// o3-mini must be mid, not frontier
	got = modeltier.ClassifyByPattern("openai", "o3-mini")
	if got.Tier != modeltier.TierMid {
		t.Errorf("o3-mini: want TierMid, got %q (prefix ordering bug)", got.Tier)
	}

	// gpt-4.1-mini must be mid, not frontier
	got = modeltier.ClassifyByPattern("openai", "gpt-4.1-mini")
	if got.Tier != modeltier.TierMid {
		t.Errorf("gpt-4.1-mini: want TierMid, got %q (prefix ordering bug)", got.Tier)
	}
}

// TestClassifyByPattern_OpenRouterPrefixStrip verifies that the vendor prefix
// is stripped and the inner family is reclassified correctly.
func TestClassifyByPattern_OpenRouterPrefixStrip(t *testing.T) {
	cases := []classifyCase{
		// Stripped → OpenAI classifier
		{"openrouter", "openai/gpt-4o-mini", modeltier.TierMid, "pattern"},
		{"openrouter", "openai/gpt-4o", modeltier.TierFrontier, "pattern"},
		// Stripped → Anthropic fast path
		{"openrouter", "anthropic/claude-sonnet-4", modeltier.TierFrontier, "pattern"},
		// Stripped → Gemini classifier
		{"openrouter", "google/gemini-2.0-flash", modeltier.TierMid, "pattern"},
	}
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			got := modeltier.ClassifyByPattern(c.provider, c.model)
			if got.Tier != c.wantTier {
				t.Errorf("ClassifyByPattern(%q, %q).Tier = %q, want %q",
					c.provider, c.model, got.Tier, c.wantTier)
			}
			if c.wantSource != "" && got.Source != c.wantSource {
				t.Errorf("ClassifyByPattern(%q, %q).Source = %q, want %q",
					c.provider, c.model, got.Source, c.wantSource)
			}
		})
	}
}

// TestClassifyByPattern_OllamaSizeParser exercises the extractOllamaBillions
// logic for the edge cases in D10 spec.
func TestClassifyByPattern_OllamaSizeParser(t *testing.T) {
	cases := []classifyCase{
		// Quantized suffix stripped: 32b is the size, q4_K_M is the quant
		{"ollama", "qwen3:32b-q4_K_M", modeltier.TierMid, "pattern"},
		// MoE: 30b-a3b — "a3b" is NOT a second size token; total params = 30b
		{"ollama", "qwen3:30b-a3b", modeltier.TierMid, "pattern"},
		// phi4:14b-q6_K → 14b → local
		{"ollama", "phi4:14b-q6_K", modeltier.TierLocal, "pattern"},
		// Embedding model (no size token)
		{"ollama", "nomic-embed-text", modeltier.TierLocal, "pattern"},
		// Bare model name (no ":" tag)
		{"ollama", "llama", modeltier.TierLocal, "pattern"},
		// Decimal billion count (hypothetical future model)
		{"ollama", "some-model:3.5b", modeltier.TierLocal, "pattern"},
		// Large model 70b boundary
		{"ollama", "llama3.1:70b", modeltier.TierFrontier, "pattern"},
		// Just below 70b
		{"ollama", "llama3.1:65b", modeltier.TierMid, "pattern"},
		// Just below 30b
		{"ollama", "qwen3:29b", modeltier.TierLocal, "pattern"},
	}
	for _, c := range cases {
		t.Run(c.provider+"/"+c.model, func(t *testing.T) {
			got := modeltier.ClassifyByPattern(c.provider, c.model)
			if got.Tier != c.wantTier {
				t.Errorf("ClassifyByPattern(%q, %q).Tier = %q, want %q",
					c.provider, c.model, got.Tier, c.wantTier)
			}
		})
	}
}
