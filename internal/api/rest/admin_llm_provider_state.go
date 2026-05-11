// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// fetchProviderState probes the configured LLM provider for runtime state
// (currently: Ollama /api/ps) and compares the loaded models against the
// active profile's ask_model. The goal is to surface the CA-326 trap
// proactively: when a different model is pinned in VRAM at infinite
// keep_alive, every request for the configured ask_model triggers a
// multi-minute model swap, and the worker's gRPC ceiling fires before
// the swap completes.
//
// Returns nil when no probe is applicable (non-Ollama provider, no
// llmConfigStore, base_url unset) so the admin UI stays quiet on cloud
// installs. Returns a partial state with ProbeErrorMessage set when the
// probe target is identified but unreachable.
//
// Read-only and short-fused: 3-second timeout total. A hung Ollama
// (the exact symptom this probe is supposed to help diagnose) must NOT
// hang the monitor endpoint that the UI hits every 2 seconds.
func (s *Server) fetchProviderState(ctx context.Context) *monitorProviderState {
	if s.llmConfigStore == nil {
		return nil
	}

	rec, err := s.llmConfigStore.LoadLLMConfig(ctx)
	if err != nil || rec == nil {
		return nil
	}

	provider := strings.ToLower(strings.TrimSpace(rec.Provider))
	if provider != "ollama" {
		// Only Ollama exposes /api/ps. vLLM, llama.cpp, sglang, cloud
		// providers have different (or no) introspection surfaces. Add
		// per-provider probes as needed; until then surface nothing
		// rather than misleading partial data.
		return nil
	}

	baseURL := strings.TrimSpace(rec.BaseURL)
	if baseURL == "" {
		return nil
	}

	state := &monitorProviderState{
		Provider:           provider,
		BaseURL:            baseURL,
		ConfiguredAskModel: rec.AskModel,
	}

	loaded, probeErr := probeOllamaLoadedModels(ctx, baseURL)
	if probeErr != nil {
		state.ProbeErrorMessage = probeErr.Error()
		return state
	}
	state.LoadedModels = loaded

	if rec.AskModel != "" && len(loaded) > 0 {
		hit := false
		for _, m := range loaded {
			if m.Name == rec.AskModel {
				hit = true
				break
			}
		}
		if !hit {
			loadedNames := make([]string, 0, len(loaded))
			for _, m := range loaded {
				loadedNames = append(loadedNames, m.Name)
			}
			state.SwapWarning = fmt.Sprintf(
				"Configured ask_model %q is not currently loaded on Ollama. "+
					"Loaded: [%s]. Each request will trigger a full model swap "+
					"(unload current + load %q), which can take minutes and may "+
					"exceed Config.QA.SynthesisTimeoutSecs. Either align ask_model "+
					"with a loaded model, OR unload the resident model with "+
					"keep_alive=0, OR raise SOURCEBRIDGE_QA_SYNTHESIS_TIMEOUT_SECS "+
					"to absorb the one-time swap cost.",
				rec.AskModel, strings.Join(loadedNames, ", "), rec.AskModel,
			)
		}
	}

	return state
}

// probeOllamaLoadedModels calls Ollama's /api/ps and returns the loaded
// models, trimmed to the fields the UI needs. The Ollama base URL may
// have a `/v1` suffix (the OpenAI-compat shim path); strip it before
// composing the native-API path.
//
// 3-second timeout — a hung Ollama is precisely the case this probe
// exists to diagnose; the probe itself must not hang.
func probeOllamaLoadedModels(ctx context.Context, baseURL string) ([]monitorLoadedModel, error) {
	root := strings.TrimSuffix(strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1"), "/v1")
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, root+"/api/ps", nil)
	if err != nil {
		return nil, fmt.Errorf("build probe request: %w", err)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("probe /api/ps: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("probe /api/ps returned %d", resp.StatusCode)
	}

	var raw struct {
		Models []struct {
			Name      string    `json:"name"`
			SizeVRAM  int64     `json:"size_vram"`
			ExpiresAt time.Time `json:"expires_at"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode /api/ps: %w", err)
	}

	loaded := make([]monitorLoadedModel, 0, len(raw.Models))
	for _, m := range raw.Models {
		loaded = append(loaded, monitorLoadedModel{
			Name:       m.Name,
			SizeVRAMMB: m.SizeVRAM / 1024 / 1024,
			ExpiresAt:  m.ExpiresAt,
		})
	}
	return loaded, nil
}
