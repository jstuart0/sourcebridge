// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
)

// llmModelInfo is a single model returned to the frontend with optional metadata.
type llmModelInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"` // max input tokens (0 = unknown)
	MaxOutput     int    `json:"max_output,omitempty"`     // max output tokens (0 = unknown)
	PriceTier     string `json:"price_tier,omitempty"`     // "free", "low", "medium", "high", "premium", "" (unknown)
}

// handleListLLMModels fetches the list of available models from the configured
// (or overridden) provider. Query params ?provider= and ?base_url= let the
// frontend preview models for a provider the user hasn't saved yet (e.g. the
// "switch provider" flow that needs to render the new provider's model list
// without committing the change).
//
// Defaults (no query params): the resolver's current snapshot is used so the
// admin UI sees the workspace-saved provider/api-key, not the env bootstrap.
// Explicit query params still preview an unsaved provider — the api-key for
// the preview comes from the saved snapshot if available, else env, so a user
// switching from "anthropic + saved key" to "openai" can still list openai
// models *if* they've also pasted an openai key (handled by a future
// query-param ?api_key= addition; today the preview falls back to the saved
// key only when the saved provider matches the requested provider).
func (s *Server) handleListLLMModels(w http.ResponseWriter, r *http.Request) {
	snap := s.ResolveLLMSnapshot(r.Context(), "models.list")

	provider := r.URL.Query().Get("provider")
	previewMode := provider != "" && provider != snap.Provider
	if provider == "" {
		provider = snap.Provider
	}
	baseURL := r.URL.Query().Get("base_url")
	if baseURL == "" {
		baseURL = snap.BaseURL
	}
	apiKey := snap.APIKey
	// In preview mode (user is exploring a different provider) we don't
	// reuse the saved api-key because it likely won't authenticate
	// against the new provider. The fetch will fail with a clear auth
	// error, which is the right UX — the user should paste the new
	// provider's key, save once, and re-list.
	if previewMode {
		apiKey = ""
	}

	models, err := fetchModels(provider, baseURL, apiKey)
	if err != nil {
		slog.Warn("llm model listing failed", "provider", provider, "error", err,
			"preview_mode", previewMode,
			"sources_provider", snap.Sources["provider"],
			"sources_api_key", snap.Sources["api_key"])
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"models": []llmModelInfo{},
			"error":  err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"models": models,
	})
}

func fetchModels(provider, baseURL, apiKey string) ([]llmModelInfo, error) {
	var models []llmModelInfo
	var err error
	var priceTier string // default price tier for local providers

	switch provider {
	case "openai":
		models, err = fetchOpenAIModels(baseURL, apiKey)
	case "anthropic":
		models, err = fetchAnthropicModels(baseURL, apiKey)
	case "ollama":
		models, err = fetchOllamaModels(baseURL)
		priceTier = "free"
	case "vllm":
		models, err = fetchOpenAIModels(baseURL, apiKey) // vLLM exposes OpenAI-compatible /v1/models
		priceTier = "free"
	case "gemini":
		models, err = fetchGeminiModels(baseURL, apiKey)
	case "openrouter":
		if baseURL == "" {
			baseURL = "https://openrouter.ai/api/v1"
		}
		models, err = fetchOpenAIModels(baseURL, apiKey) // OpenRouter is fully OpenAI-compatible
	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}
	if err != nil {
		return nil, err
	}

	// Fill in metadata from the lookup table for models where the provider
	// API didn't return context window / pricing info.
	enrichModelMeta(models, priceTier)

	return models, nil
}

// fetchOpenAIModels calls GET /v1/models (works for OpenAI and vLLM).
func fetchOpenAIModels(baseURL, apiKey string) ([]llmModelInfo, error) {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	// Ensure we hit /v1/models even if the base URL already includes /v1
	url := baseURL + "/models"

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := httpClientWithTimeout().Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var result struct {
		Data []struct {
			ID      string `json:"id"`
			Created int64  `json:"created"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("invalid response: %v", err)
	}

	models := make([]llmModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, llmModelInfo{ID: m.ID})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// fetchAnthropicModels calls GET /v1/models on the Anthropic API.
func fetchAnthropicModels(baseURL, apiKey string) ([]llmModelInfo, error) {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	url := baseURL + "/v1/models?limit=100"

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := httpClientWithTimeout().Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var result struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("invalid response: %v", err)
	}

	models := make([]llmModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, llmModelInfo{ID: m.ID, Name: m.DisplayName})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// fetchOllamaModels calls GET /api/tags on the Ollama server.
func fetchOllamaModels(baseURL string) ([]llmModelInfo, error) {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	// Ollama base URL may include /v1 for OpenAI compat; strip it for the native API
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	url := baseURL + "/api/tags"

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClientWithTimeout().Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("invalid response: %v", err)
	}

	models := make([]llmModelInfo, 0, len(result.Models))
	for _, m := range result.Models {
		models = append(models, llmModelInfo{ID: m.Name})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// fetchGeminiModels calls GET /v1beta/models on the Google Generative AI API.
func fetchGeminiModels(baseURL, apiKey string) ([]llmModelInfo, error) {
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	// Strip the OpenAI-compat suffix if present so we can use the native listing endpoint
	baseURL = strings.TrimSuffix(baseURL, "/v1beta/openai")
	baseURL = strings.TrimSuffix(baseURL, "/v1beta")
	url := baseURL + "/v1beta/models"
	if apiKey != "" {
		url += "?key=" + apiKey
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClientWithTimeout().Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var result struct {
		Models []struct {
			Name             string `json:"name"`
			DisplayName      string `json:"displayName"`
			InputTokenLimit  int    `json:"inputTokenLimit"`
			OutputTokenLimit int    `json:"outputTokenLimit"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("invalid response: %v", err)
	}

	models := make([]llmModelInfo, 0, len(result.Models))
	for _, m := range result.Models {
		// Gemini returns names like "models/gemini-2.5-flash" — strip the prefix
		id := strings.TrimPrefix(m.Name, "models/")
		models = append(models, llmModelInfo{
			ID:            id,
			Name:          m.DisplayName,
			ContextWindow: m.InputTokenLimit,
			MaxOutput:     m.OutputTokenLimit,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

func httpClientWithTimeout() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
