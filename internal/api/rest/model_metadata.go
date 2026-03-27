// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import "strings"

// knownModelMeta provides context window and pricing info for well-known models
// where the provider's listing API doesn't return metadata. Updated periodically.
type modelMeta struct {
	ContextWindow int
	MaxOutput     int
	PriceTier     string // "free", "low", "medium", "high", "premium"
}

var knownModels = map[string]modelMeta{
	// Anthropic
	"claude-opus-4":   {ContextWindow: 200000, MaxOutput: 32000, PriceTier: "premium"},
	"claude-sonnet-4": {ContextWindow: 200000, MaxOutput: 16000, PriceTier: "high"},
	"claude-haiku-4":  {ContextWindow: 200000, MaxOutput: 8192, PriceTier: "low"},

	// OpenAI
	"gpt-4o":      {ContextWindow: 128000, MaxOutput: 16384, PriceTier: "high"},
	"gpt-4o-mini": {ContextWindow: 128000, MaxOutput: 16384, PriceTier: "low"},
	"gpt-4.1":     {ContextWindow: 1047576, MaxOutput: 32768, PriceTier: "high"},
	"gpt-4.1-mini": {ContextWindow: 1047576, MaxOutput: 32768, PriceTier: "medium"},
	"gpt-4.1-nano": {ContextWindow: 1047576, MaxOutput: 32768, PriceTier: "low"},
	"o3":          {ContextWindow: 200000, MaxOutput: 100000, PriceTier: "premium"},
	"o3-mini":     {ContextWindow: 200000, MaxOutput: 100000, PriceTier: "medium"},
	"o4-mini":     {ContextWindow: 200000, MaxOutput: 100000, PriceTier: "medium"},

	// Gemini
	"gemini-2.5-pro":   {ContextWindow: 1048576, MaxOutput: 65536, PriceTier: "high"},
	"gemini-2.5-flash": {ContextWindow: 1048576, MaxOutput: 65536, PriceTier: "low"},
	"gemini-2.0-flash": {ContextWindow: 1048576, MaxOutput: 8192, PriceTier: "low"},
}

// enrichModelMeta fills in metadata from the lookup table when the provider
// API didn't return it. Matches by prefix so "claude-sonnet-4-20250514" hits
// the "claude-sonnet-4" entry.
func enrichModelMeta(models []llmModelInfo, providerPriceTier string) {
	for i := range models {
		if models[i].ContextWindow > 0 {
			continue // provider already gave us metadata
		}
		if meta, ok := lookupModel(models[i].ID); ok {
			models[i].ContextWindow = meta.ContextWindow
			models[i].MaxOutput = meta.MaxOutput
			if models[i].PriceTier == "" {
				models[i].PriceTier = meta.PriceTier
			}
		} else if providerPriceTier != "" {
			models[i].PriceTier = providerPriceTier
		}
	}
}

// lookupModel tries exact match first, then prefix match.
func lookupModel(id string) (modelMeta, bool) {
	if m, ok := knownModels[id]; ok {
		return m, true
	}
	// Prefix match: "claude-sonnet-4-20250514" → "claude-sonnet-4"
	for prefix, m := range knownModels {
		if strings.HasPrefix(id, prefix) {
			return m, true
		}
	}
	return modelMeta{}, false
}
