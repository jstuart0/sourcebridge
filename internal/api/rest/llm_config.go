// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/sourcebridge/sourcebridge/internal/capabilities"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
)

// LLMConfigStore persists LLM configuration so it survives server restarts.
type LLMConfigStore interface {
	LoadLLMConfig() (*LLMConfigRecord, error)
	SaveLLMConfig(rec *LLMConfigRecord) error
}

// LLMConfigRecord mirrors db.LLMConfigRecord to avoid circular imports.
type LLMConfigRecord struct {
	Provider                 string `json:"provider"`
	BaseURL                  string `json:"base_url"`
	APIKey                   string `json:"api_key"`
	SummaryModel             string `json:"summary_model"`
	ReviewModel              string `json:"review_model"`
	AskModel                 string `json:"ask_model"`
	KnowledgeModel           string `json:"knowledge_model"`
	ArchitectureDiagramModel string `json:"architecture_diagram_model"`
	ReportModel              string `json:"report_model,omitempty"`
	DraftModel               string `json:"draft_model"`
	TimeoutSecs              int    `json:"timeout_secs"`
	AdvancedMode             bool   `json:"advanced_mode"`
}

type llmConfigResponse struct {
	Provider                 string `json:"provider"`
	BaseURL                  string `json:"base_url"`
	APIKeySet                bool   `json:"api_key_set"`
	APIKeyHint               string `json:"api_key_hint,omitempty"`
	SummaryModel             string `json:"summary_model"`
	ReviewModel              string `json:"review_model"`
	AskModel                 string `json:"ask_model"`
	KnowledgeModel           string `json:"knowledge_model"`
	ArchitectureDiagramModel string `json:"architecture_diagram_model"`
	ReportModel              string `json:"report_model,omitempty"`
	DraftModel               string `json:"draft_model"`
	TimeoutSecs              int    `json:"timeout_secs"`
	AdvancedMode             bool   `json:"advanced_mode"`
}

type updateLLMConfigRequest struct {
	Provider                 *string `json:"provider,omitempty"`
	BaseURL                  *string `json:"base_url,omitempty"`
	APIKey                   *string `json:"api_key,omitempty"`
	SummaryModel             *string `json:"summary_model,omitempty"`
	ReviewModel              *string `json:"review_model,omitempty"`
	AskModel                 *string `json:"ask_model,omitempty"`
	KnowledgeModel           *string `json:"knowledge_model,omitempty"`
	ArchitectureDiagramModel *string `json:"architecture_diagram_model,omitempty"`
	ReportModel              *string `json:"report_model,omitempty"`
	DraftModel               *string `json:"draft_model,omitempty"`
	TimeoutSecs              *int    `json:"timeout_secs,omitempty"`
	AdvancedMode             *bool   `json:"advanced_mode,omitempty"`
}

// effectiveLLM is the snapshot returned by handleGetLLMConfig / used by
// handleListLLMModels when no query params are supplied. It reads through
// the resolver when available (so saved DB values are reflected in the
// admin UI on every replica) and falls through to s.cfg.LLM (env
// bootstrap) only when the resolver is not configured (test/embedded
// mode).
//
// IMPORTANT: this is the post-slice-1 contract. Before slice 1 the admin
// UI was reading s.cfg.LLM, which was mutated at boot from a buggy DB→cfg
// merge. After slice 1 cfg.LLM is env-bootstrap only and the resolver
// owns runtime values.
type effectiveLLM struct {
	Provider                 string
	BaseURL                  string
	APIKey                   string
	SummaryModel             string
	ReviewModel              string
	AskModel                 string
	KnowledgeModel           string
	ArchitectureDiagramModel string
	ReportModel              string
	DraftModel               string
	TimeoutSecs              int
	AdvancedMode             bool
}

func (s *Server) effectiveLLMConfig() effectiveLLM {
	// Start from env-bootstrap (cfg.LLM); workspace store overrides
	// non-empty fields when available.
	eff := effectiveLLM{
		Provider:                 s.cfg.LLM.Provider,
		BaseURL:                  s.cfg.LLM.BaseURL,
		APIKey:                   s.cfg.LLM.APIKey,
		SummaryModel:             s.cfg.LLM.SummaryModel,
		ReviewModel:              s.cfg.LLM.ReviewModel,
		AskModel:                 s.cfg.LLM.AskModel,
		KnowledgeModel:           s.cfg.LLM.KnowledgeModel,
		ArchitectureDiagramModel: s.cfg.LLM.ArchitectureDiagramModel,
		ReportModel:              s.cfg.LLM.ReportModel,
		DraftModel:               s.cfg.LLM.DraftModel,
		TimeoutSecs:              s.cfg.LLM.TimeoutSecs,
		AdvancedMode:             s.cfg.LLM.AdvancedMode,
	}
	// Workspace overlay via the persisted store. We read directly through
	// the LLMConfigStore (not the resolver) because the admin UI wants
	// the *saved* per-op model fields (not just the model picked for one
	// op group). The resolver's narrow Snapshot collapses per-op fields
	// into one Model; that's intentional for the gRPC code path but
	// wrong for the admin UI which wants to render every per-op slot.
	if s.llmConfigStore != nil {
		if rec, err := s.llmConfigStore.LoadLLMConfig(); err == nil && rec != nil {
			if rec.Provider != "" {
				eff.Provider = rec.Provider
			}
			if rec.BaseURL != "" {
				eff.BaseURL = rec.BaseURL
			}
			if rec.APIKey != "" {
				eff.APIKey = rec.APIKey
			}
			if rec.SummaryModel != "" {
				eff.SummaryModel = rec.SummaryModel
			}
			if rec.ReviewModel != "" {
				eff.ReviewModel = rec.ReviewModel
			}
			if rec.AskModel != "" {
				eff.AskModel = rec.AskModel
			}
			if rec.KnowledgeModel != "" {
				eff.KnowledgeModel = rec.KnowledgeModel
			}
			if rec.ArchitectureDiagramModel != "" {
				eff.ArchitectureDiagramModel = rec.ArchitectureDiagramModel
			}
			if rec.ReportModel != "" {
				eff.ReportModel = rec.ReportModel
			}
			if rec.DraftModel != "" {
				eff.DraftModel = rec.DraftModel
			}
			if rec.TimeoutSecs > 0 {
				eff.TimeoutSecs = rec.TimeoutSecs
			}
			eff.AdvancedMode = rec.AdvancedMode
		}
	}
	return eff
}

func (s *Server) handleGetLLMConfig(w http.ResponseWriter, r *http.Request) {
	eff := s.effectiveLLMConfig()
	resp := llmConfigResponse{
		Provider:                 eff.Provider,
		BaseURL:                  eff.BaseURL,
		APIKeySet:                eff.APIKey != "",
		SummaryModel:             eff.SummaryModel,
		ReviewModel:              eff.ReviewModel,
		AskModel:                 eff.AskModel,
		KnowledgeModel:           eff.KnowledgeModel,
		ArchitectureDiagramModel: eff.ArchitectureDiagramModel,
		DraftModel:               eff.DraftModel,
		TimeoutSecs:              eff.TimeoutSecs,
		AdvancedMode:             eff.AdvancedMode,
	}
	if eff.APIKey != "" {
		resp.APIKeyHint = maskToken(eff.APIKey)
	}
	if capabilities.IsAvailable("per_op_models", capabilities.NormalizeEdition(s.cfg.Edition)) {
		resp.ReportModel = eff.ReportModel
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
	}

	// Partial-update merge against the *existing DB record*, not s.cfg.LLM.
	// Pre-slice-1 we mutated cfg.LLM and then UPSERTed the whole thing,
	// which leaked configmap env values into the saved row whenever a
	// field was unset in the request. Now we load → merge → save so the
	// request only touches the fields it explicitly provides.
	rec := &LLMConfigRecord{}
	if s.llmConfigStore != nil {
		if existing, err := s.llmConfigStore.LoadLLMConfig(); err == nil && existing != nil {
			rec = existing
		}
	}
	// If no DB row exists yet, seed from the env bootstrap so a
	// brand-new install's first save preserves whatever was already in
	// the configmap. Subsequent saves operate purely on saved state.
	if rec.Provider == "" && rec.APIKey == "" && rec.SummaryModel == "" {
		rec.Provider = s.cfg.LLM.Provider
		rec.BaseURL = s.cfg.LLM.BaseURL
		rec.APIKey = s.cfg.LLM.APIKey
		rec.SummaryModel = s.cfg.LLM.SummaryModel
		rec.ReviewModel = s.cfg.LLM.ReviewModel
		rec.AskModel = s.cfg.LLM.AskModel
		rec.KnowledgeModel = s.cfg.LLM.KnowledgeModel
		rec.ArchitectureDiagramModel = s.cfg.LLM.ArchitectureDiagramModel
		rec.ReportModel = s.cfg.LLM.ReportModel
		rec.DraftModel = s.cfg.LLM.DraftModel
		rec.TimeoutSecs = s.cfg.LLM.TimeoutSecs
		rec.AdvancedMode = s.cfg.LLM.AdvancedMode
	}

	if req.Provider != nil {
		rec.Provider = *req.Provider
	}
	if req.BaseURL != nil {
		rec.BaseURL = *req.BaseURL
	}
	if req.APIKey != nil {
		rec.APIKey = *req.APIKey
	}
	if req.SummaryModel != nil {
		rec.SummaryModel = *req.SummaryModel
	}
	if req.ReviewModel != nil {
		rec.ReviewModel = *req.ReviewModel
	}
	if req.AskModel != nil {
		rec.AskModel = *req.AskModel
	}
	if req.KnowledgeModel != nil {
		rec.KnowledgeModel = *req.KnowledgeModel
	}
	if req.ArchitectureDiagramModel != nil {
		rec.ArchitectureDiagramModel = *req.ArchitectureDiagramModel
	}
	if req.ReportModel != nil && capabilities.IsAvailable("per_op_models", capabilities.NormalizeEdition(s.cfg.Edition)) {
		rec.ReportModel = *req.ReportModel
	}
	if req.DraftModel != nil {
		rec.DraftModel = *req.DraftModel
	}
	if req.TimeoutSecs != nil {
		rec.TimeoutSecs = *req.TimeoutSecs
	}
	if req.AdvancedMode != nil {
		rec.AdvancedMode = *req.AdvancedMode
	}

	// Persist to database. Failure is fatal: pre-slice-1 we silently
	// kept the in-memory mutation even when the save failed, which is
	// exactly the kind of thing that produced multi-replica drift.
	if s.llmConfigStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "LLM config persistence not available (running in embedded mode without external SurrealDB)",
		})
		return
	}
	if err := s.llmConfigStore.SaveLLMConfig(rec); err != nil {
		// Slice 3 introduces a typed encryption-key-missing error
		// (ErrEncryptionKeyRequired). Detect it via errors.Is so the
		// admin UI can show a 422 with a clear message.
		if errors.Is(err, ErrLLMEncryptionKeyRequired) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error": "Cannot save API key: SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY is not set on the server. " +
					"Set the encryption key (32+ random bytes, base64-encoded) and restart, or unset the api_key field to save other settings only.",
			})
			return
		}
		slog.Error("failed to persist llm config", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Failed to save LLM config: " + err.Error(),
		})
		return
	}

	// Nudge the local resolver cache so the very next Resolve on this
	// replica fetches the new values. Cross-replica freshness still
	// relies on the version stamp the Save bumps in the DB.
	if s.llmResolver != nil {
		s.llmResolver.InvalidateLocal()
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "saved",
		"provider": rec.Provider,
		"note":     "LLM settings saved. The API and worker will use these on new requests immediately.",
	})
}

// ErrLLMEncryptionKeyRequired is the sentinel returned (via wrap) by
// llmConfigAdapter.SaveLLMConfig in cli/serve.go when the underlying
// store rejects an api_key save because cfg.Security.EncryptionKey is
// unset and the OSS escape hatch (SOURCEBRIDGE_ALLOW_UNENCRYPTED_LLM_KEY)
// is off. handleUpdateLLMConfig matches via errors.Is and returns 422
// with a clear admin-facing message.
var ErrLLMEncryptionKeyRequired = errors.New("llm api key cannot be saved without an encryption key")

// ResolveLLMSnapshot exposes the runtime LLM-config resolver to handlers
// in this package that need to inspect the current effective settings
// (e.g. handleListLLMModels). Returns the zero Snapshot when the
// resolver is unavailable.
func (s *Server) ResolveLLMSnapshot(ctx context.Context, op string) resolution.Snapshot {
	if s.llmResolver == nil {
		return resolution.Snapshot{
			Provider:    s.cfg.LLM.Provider,
			BaseURL:     s.cfg.LLM.BaseURL,
			APIKey:      s.cfg.LLM.APIKey,
			Model:       s.cfg.LLM.SummaryModel,
			TimeoutSecs: s.cfg.LLM.TimeoutSecs,
		}
	}
	snap, err := s.llmResolver.Resolve(ctx, "", op)
	if err != nil {
		slog.Warn("llm resolver: resolve failed in admin handler", "op", op, "error", err)
		return resolution.Snapshot{
			Provider: s.cfg.LLM.Provider,
			BaseURL:  s.cfg.LLM.BaseURL,
		}
	}
	return snap
}
