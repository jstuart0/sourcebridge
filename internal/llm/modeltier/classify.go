// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package modeltier

import (
	"regexp"
	"strconv"
	"strings"
)

// sizeRe matches the first parameter-count token after the ":" delimiter in an
// Ollama-style model tag (e.g. "qwen3:32b", "llama3.1:70b-instruct-fp16").
// Group 1 captures the numeric value (integer or decimal); group 2 captures
// the "b" suffix. Everything after the first "-" is ignored, so "32b-q4_K_M"
// parses as 32b and "30b-a3b" (MoE total-params) parses as 30b.
// Note on MoE (e.g. qwen3:30b-a3b): we classify by total-parameter count
// (30B → mid) as a pragmatic default. If active-parameter semantics matter
// for a particular model, operators can set quality_gate_tier explicitly via
// the Model Registry. Filed as CA-150-followup-C if this proves insufficient.
var sizeRe = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)b`)

// extractOllamaBillions parses the parameter count from a model identifier.
// It handles two common formats:
//
//   - Ollama tag format: "qwen3:32b-q4_K_M" — looks after the ":" delimiter,
//     strips the quant suffix (everything after the first "-"), then matches.
//   - HuggingFace/path format: "meta-llama/llama-3-70b-instruct" — no colon;
//     scans the entire string for the first Nb token.
//
// Returns (billions, true) on success; (0, false) when no size token exists.
func extractOllamaBillions(model string) (float64, bool) {
	seg := model
	if idx := strings.Index(model, ":"); idx >= 0 {
		// Ollama tag: work only on the portion after ":"
		seg = model[idx+1:]
		// Strip quantization / instruction suffix (everything after first "-")
		if idx2 := strings.Index(seg, "-"); idx2 >= 0 {
			seg = seg[:idx2]
		}
	}
	// For HuggingFace-style paths ("meta-llama/llama-3-70b-instruct") the
	// sizeRe scan across the full seg finds the first NNb token.
	m := sizeRe.FindStringSubmatch(seg)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// ClassifyByPattern returns a Resolution for the given (provider, model)
// pair using purely local, deterministic pattern matching. It performs no I/O
// and has no store dependency. The returned Resolution.Source is "pattern"
// when a rule matched and "fallback" when no rule matched and the last-resort
// TierLocal default was used.
//
// Provider names use the canonical strings defined in
// internal/config/config.go's validProviders map:
//
//	anthropic, openai, gemini, openrouter, ollama, vllm, llama-cpp, sglang, lmstudio
//
// For the "openrouter" provider, model IDs often carry a vendor prefix
// (e.g. "google/gemini-pro", "anthropic/claude-opus-4-7"). This function
// strips the known prefix and reclassifies the inner family. Unrecognized
// OpenRouter prefixes fall through to the generic pattern table.
//
// Note: internal/api/rest/model_metadata.go maintains a parallel knownModels
// table for REST-layer pricing metadata (PriceTier, context window). That
// table serves a different layer; see Decision D8 in the CA-150 plan.
func ClassifyByPattern(provider, model string) Resolution {
	model = strings.ToLower(strings.TrimSpace(model))
	provider = strings.ToLower(strings.TrimSpace(provider))

	// Fast-path: some providers exclusively host one tier of models.
	switch provider {
	case "anthropic":
		return Resolution{Tier: TierFrontier, Source: "pattern"}
	case "gemini":
		return classifyGeminiModel(model)
	case "ollama", "vllm", "llama-cpp", "sglang", "lmstudio":
		return classifyLocalModel(model)
	case "openrouter":
		return classifyOpenRouterModel(model)
	case "openai":
		return classifyOpenAIModel(model)
	}

	// Unknown provider: fall through to generic model-name heuristics.
	return classifyGenericModel(model)
}

// openRouterPrefixes maps known vendor prefixes used in OpenRouter model IDs
// to their canonical provider name so the inner model family can be
// reclassified.
var openRouterPrefixes = []string{
	"google/",
	"anthropic/",
	"openai/",
	"meta/",
	"meta-llama/",
	"mistralai/",
	"cohere/",
	"x-ai/",
	"deepseek/",
	"qwen/",
	"microsoft/",
}

func classifyOpenRouterModel(model string) Resolution {
	inner := model
	innerProvider := ""
	for _, pfx := range openRouterPrefixes {
		if strings.HasPrefix(model, pfx) {
			inner = model[len(pfx):]
			switch pfx {
			case "anthropic/":
				innerProvider = "anthropic"
			case "google/":
				innerProvider = "gemini"
			case "openai/":
				innerProvider = "openai"
			default:
				// meta, mistralai, cohere, etc. — treat as local/generic
				innerProvider = "generic"
			}
			break
		}
	}

	switch innerProvider {
	case "anthropic":
		return Resolution{Tier: TierFrontier, Source: "pattern"}
	case "gemini":
		return classifyGeminiModel(inner)
	case "openai":
		return classifyOpenAIModel(inner)
	}
	// Generic open-weights served via OpenRouter
	return classifyGenericModel(inner)
}

// classifyOpenAIModel applies the OpenAI model pattern table.
//
// Pattern-match table: ORDER MATTERS. Each entry is checked in declaration
// order; the first match wins. Place narrower prefixes BEFORE broader ones
// (e.g. "gpt-4o-mini" before "gpt-4o"; "o3-mini" before "o3").
// Tests in classify_test.go include exact cases for every overlapping prefix
// to catch ordering bugs.
func classifyOpenAIModel(model string) Resolution {
	// Reasoning models (o-series) — check mini/nano before base.
	switch {
	case strings.HasPrefix(model, "o1-mini"),
		strings.HasPrefix(model, "o3-mini"),
		strings.HasPrefix(model, "o4-mini"):
		return Resolution{Tier: TierMid, Source: "pattern"}

	case strings.HasPrefix(model, "o1"),
		strings.HasPrefix(model, "o3"):
		return Resolution{Tier: TierFrontier, Source: "pattern"}

	// GPT-4.1 family — mini/nano before base.
	case strings.HasPrefix(model, "gpt-4.1-mini"),
		strings.HasPrefix(model, "gpt-4.1-nano"):
		return Resolution{Tier: TierMid, Source: "pattern"}

	case strings.HasPrefix(model, "gpt-4.1"):
		return Resolution{Tier: TierFrontier, Source: "pattern"}

	// GPT-4o family — mini before base.
	case strings.HasPrefix(model, "gpt-4o-mini"):
		return Resolution{Tier: TierMid, Source: "pattern"}

	case strings.HasPrefix(model, "gpt-4o"):
		return Resolution{Tier: TierFrontier, Source: "pattern"}

	// GPT-4 family (turbo, etc.)
	case strings.HasPrefix(model, "gpt-4"):
		return Resolution{Tier: TierFrontier, Source: "pattern"}

	// GPT-3.5 and below
	case strings.HasPrefix(model, "gpt-3.5"),
		strings.HasPrefix(model, "gpt-3"):
		return Resolution{Tier: TierLocal, Source: "pattern"}

	// Embedding / utility models — not for generation; treat as local.
	case strings.HasPrefix(model, "text-embedding"),
		strings.HasPrefix(model, "ada"),
		strings.HasPrefix(model, "babbage"),
		strings.HasPrefix(model, "davinci"):
		return Resolution{Tier: TierLocal, Source: "pattern"}
	}

	return Resolution{Tier: TierLocal, Source: "fallback"}
}

// classifyGeminiModel applies the Google Gemini model pattern table.
func classifyGeminiModel(model string) Resolution {
	switch {
	// Flash variants are mid-tier (efficient, not frontier-class).
	case strings.Contains(model, "flash"):
		return Resolution{Tier: TierMid, Source: "pattern"}

	// Pro and Ultra are frontier.
	case strings.Contains(model, "ultra"),
		strings.Contains(model, "pro"):
		return Resolution{Tier: TierFrontier, Source: "pattern"}

	// Nano / lite are local.
	case strings.Contains(model, "nano"),
		strings.Contains(model, "lite"):
		return Resolution{Tier: TierLocal, Source: "pattern"}
	}

	// Unknown Gemini variant — assume frontier (Google-hosted).
	return Resolution{Tier: TierFrontier, Source: "pattern"}
}

// classifyLocalModel classifies models served via local inference backends
// (Ollama, vLLM, llama-cpp, sglang, lmstudio) using the Ollama size-parser
// and a family-name fast path.
func classifyLocalModel(model string) Resolution {
	// Fast path for embedding models — no meaningful parameter size signal.
	// E.g. "nomic-embed-text", "all-minilm".
	if strings.Contains(model, "embed") {
		return Resolution{Tier: TierLocal, Source: "pattern"}
	}

	// Attempt to extract parameter count from the tag (e.g. ":32b").
	if billions, ok := extractOllamaBillions(model); ok {
		return classifyByBillions(billions)
	}

	// No size tag — fall back to family-name heuristics.
	return classifyGenericModel(model)
}

// classifyByBillions maps a raw parameter count (in billions) to a tier.
// Thresholds:
//   - ≥ 70B → frontier (llama3.1:70b, qwen3:72b, etc.)
//   - ≥ 30B → mid (qwen3:32b, qwen3:30b-a3b MoE, etc.)
//   - <  30B → local
func classifyByBillions(b float64) Resolution {
	switch {
	case b >= 70:
		return Resolution{Tier: TierFrontier, Source: "pattern"}
	case b >= 30:
		return Resolution{Tier: TierMid, Source: "pattern"}
	default:
		return Resolution{Tier: TierLocal, Source: "pattern"}
	}
}

// classifyGenericModel is the last-resort classifier applied when provider is
// unknown or the provider-specific classifier found no match. It inspects
// model family names for well-known patterns before giving up.
func classifyGenericModel(model string) Resolution {
	// Claude family served through a non-anthropic provider route.
	if strings.Contains(model, "claude") {
		return Resolution{Tier: TierFrontier, Source: "pattern"}
	}

	// Llama without a size tag (bare "llama", "llama2", etc.) — conservative.
	if strings.HasPrefix(model, "llama") {
		return Resolution{Tier: TierLocal, Source: "pattern"}
	}

	// Phi family from Microsoft — small models.
	if strings.HasPrefix(model, "phi") {
		// phi4 at 14b is local; phi-3-medium at 14b is also local.
		// Extract size if present.
		if b, ok := extractOllamaBillions(model); ok {
			return classifyByBillions(b)
		}
		return Resolution{Tier: TierLocal, Source: "pattern"}
	}

	// Qwen/Qwen2/Qwen3 without a tag.
	if strings.HasPrefix(model, "qwen") {
		if b, ok := extractOllamaBillions(model); ok {
			return classifyByBillions(b)
		}
		return Resolution{Tier: TierLocal, Source: "pattern"}
	}

	// Mistral / Mixtral family.
	if strings.HasPrefix(model, "mistral") || strings.HasPrefix(model, "mixtral") {
		if b, ok := extractOllamaBillions(model); ok {
			return classifyByBillions(b)
		}
		return Resolution{Tier: TierMid, Source: "pattern"} // Mixtral-8x7B ~mid
	}

	// Deepseek family.
	if strings.HasPrefix(model, "deepseek") {
		if b, ok := extractOllamaBillions(model); ok {
			return classifyByBillions(b)
		}
		return Resolution{Tier: TierMid, Source: "pattern"}
	}

	// No pattern matched — last-resort TierLocal with "fallback" source so
	// callers can log a warning.
	return Resolution{Tier: TierLocal, Source: "fallback"}
}
