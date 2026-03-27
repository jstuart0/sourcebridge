// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// LLMConfigStore persists LLM configuration so it survives server restarts.
type LLMConfigStore interface {
	LoadLLMConfig() (*LLMConfigRecord, error)
	SaveLLMConfig(rec *LLMConfigRecord) error
}

// LLMConfigRecord mirrors db.LLMConfigRecord to avoid circular imports.
type LLMConfigRecord struct {
	Provider       string `json:"provider"`
	BaseURL        string `json:"base_url"`
	APIKey         string `json:"api_key"`
	SummaryModel   string `json:"summary_model"`
	ReviewModel    string `json:"review_model"`
	AskModel       string `json:"ask_model"`
	KnowledgeModel string `json:"knowledge_model"`
	DraftModel     string `json:"draft_model"`
	TimeoutSecs    int    `json:"timeout_secs"`
	AdvancedMode   bool   `json:"advanced_mode"`
}

type llmConfigResponse struct {
	Provider       string `json:"provider"`
	BaseURL        string `json:"base_url"`
	APIKeySet      bool   `json:"api_key_set"`
	APIKeyHint     string `json:"api_key_hint,omitempty"`
	SummaryModel   string `json:"summary_model"`
	ReviewModel    string `json:"review_model"`
	AskModel       string `json:"ask_model"`
	KnowledgeModel string `json:"knowledge_model"`
	DraftModel     string `json:"draft_model"`
	TimeoutSecs    int    `json:"timeout_secs"`
	AdvancedMode   bool   `json:"advanced_mode"`
}

type updateLLMConfigRequest struct {
	Provider       *string `json:"provider,omitempty"`
	BaseURL        *string `json:"base_url,omitempty"`
	APIKey         *string `json:"api_key,omitempty"`
	SummaryModel   *string `json:"summary_model,omitempty"`
	ReviewModel    *string `json:"review_model,omitempty"`
	AskModel       *string `json:"ask_model,omitempty"`
	KnowledgeModel *string `json:"knowledge_model,omitempty"`
	DraftModel     *string `json:"draft_model,omitempty"`
	TimeoutSecs    *int    `json:"timeout_secs,omitempty"`
	AdvancedMode   *bool   `json:"advanced_mode,omitempty"`
}

func (s *Server) handleGetLLMConfig(w http.ResponseWriter, r *http.Request) {
	resp := llmConfigResponse{
		Provider:       s.cfg.LLM.Provider,
		BaseURL:        s.cfg.LLM.BaseURL,
		APIKeySet:      s.cfg.LLM.APIKey != "",
		SummaryModel:   s.cfg.LLM.SummaryModel,
		ReviewModel:    s.cfg.LLM.ReviewModel,
		AskModel:       s.cfg.LLM.AskModel,
		KnowledgeModel: s.cfg.LLM.KnowledgeModel,
		DraftModel:     s.cfg.LLM.DraftModel,
		TimeoutSecs:    s.cfg.LLM.TimeoutSecs,
		AdvancedMode:   s.cfg.LLM.AdvancedMode,
	}
	if s.cfg.LLM.APIKey != "" {
		resp.APIKeyHint = maskToken(s.cfg.LLM.APIKey)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleUpdateLLMConfig(w http.ResponseWriter, r *http.Request) {
	var req updateLLMConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	// Validate provider if provided
	if req.Provider != nil {
		valid := map[string]bool{"anthropic": true, "openai": true, "ollama": true, "vllm": true, "llama-cpp": true, "sglang": true, "lmstudio": true, "gemini": true, "openrouter": true}
		if !valid[*req.Provider] {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "Invalid provider. Must be one of: anthropic, openai, ollama, vllm, llama-cpp, sglang, lmstudio, gemini, openrouter",
			})
			return
		}
		s.cfg.LLM.Provider = *req.Provider
	}
	if req.BaseURL != nil {
		s.cfg.LLM.BaseURL = *req.BaseURL
	}
	if req.APIKey != nil {
		s.cfg.LLM.APIKey = *req.APIKey
	}
	if req.SummaryModel != nil {
		s.cfg.LLM.SummaryModel = *req.SummaryModel
	}
	if req.ReviewModel != nil {
		s.cfg.LLM.ReviewModel = *req.ReviewModel
	}
	if req.AskModel != nil {
		s.cfg.LLM.AskModel = *req.AskModel
	}
	if req.KnowledgeModel != nil {
		s.cfg.LLM.KnowledgeModel = *req.KnowledgeModel
	}
	if req.DraftModel != nil {
		s.cfg.LLM.DraftModel = *req.DraftModel
	}
	if req.TimeoutSecs != nil {
		s.cfg.LLM.TimeoutSecs = *req.TimeoutSecs
	}
	if req.AdvancedMode != nil {
		s.cfg.LLM.AdvancedMode = *req.AdvancedMode
	}

	// Persist to database if available
	if s.llmConfigStore != nil {
		rec := &LLMConfigRecord{
			Provider:       s.cfg.LLM.Provider,
			BaseURL:        s.cfg.LLM.BaseURL,
			APIKey:         s.cfg.LLM.APIKey,
			SummaryModel:   s.cfg.LLM.SummaryModel,
			ReviewModel:    s.cfg.LLM.ReviewModel,
			AskModel:       s.cfg.LLM.AskModel,
			KnowledgeModel: s.cfg.LLM.KnowledgeModel,
			DraftModel:     s.cfg.LLM.DraftModel,
			TimeoutSecs:    s.cfg.LLM.TimeoutSecs,
			AdvancedMode:   s.cfg.LLM.AdvancedMode,
		}
		if err := s.llmConfigStore.SaveLLMConfig(rec); err != nil {
			slog.Warn("failed to persist llm config", "error", err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "saved",
		"provider": s.cfg.LLM.Provider,
		"note":     "LLM settings saved. The API server will use these immediately. Worker services may need a restart to pick up changes.",
	})
}
