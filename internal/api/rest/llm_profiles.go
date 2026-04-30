// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sourcebridge/sourcebridge/internal/maskutil"
)

// ─────────────────────────────────────────────────────────────────────────
// Profile-aware admin REST contract (LLM provider profiles slice 1)
// ─────────────────────────────────────────────────────────────────────────
//
// New endpoints (additive; legacy /admin/llm-config remains as the
// active-profile back-compat surface):
//   GET    /api/v1/admin/llm-profiles
//   POST   /api/v1/admin/llm-profiles
//   GET    /api/v1/admin/llm-profiles/{id}
//   PUT    /api/v1/admin/llm-profiles/{id}
//   DELETE /api/v1/admin/llm-profiles/{id}
//   POST   /api/v1/admin/llm-profiles/{id}/activate
//
// All writes go through the BEGIN/COMMIT helpers in internal/db
// (writeActiveProfileWithLegacyMirror, writeNonActiveProfileWithWatermarkBump,
// activateProfileWithLegacyMirror, deleteNonActiveProfile) which CAS-guard
// on workspace.version, dual-write the legacy mirror row when the
// active profile is touched, and advance the active-profile watermark.

// LLMProfileStoreAdapter is the rest-package-facing interface for the
// profile store + write helpers. The cli/serve.go layer wires the
// concrete *db.SurrealLLMProfileStore + *db.SurrealLLMConfigStore
// behind this interface so the rest package doesn't import internal/db.
type LLMProfileStoreAdapter interface {
	ListProfiles(ctx context.Context) ([]ProfileResponse, error)
	GetProfile(ctx context.Context, id string) (*ProfileResponse, error)
	CreateProfile(ctx context.Context, req ProfileCreateRequest) (string, error)
	UpdateProfile(ctx context.Context, id string, req ProfileUpdateRequest) error
	DeleteProfile(ctx context.Context, id string) error
	ActivateProfile(ctx context.Context, id, by string) error
	ActiveProfileMissing() bool
	ActiveProfileID(ctx context.Context) (string, error)
}

// WithLLMProfileStore wires the profile-store adapter into the server.
func WithLLMProfileStore(adapter LLMProfileStoreAdapter) ServerOption {
	return func(s *Server) { s.llmProfileStore = adapter }
}

// ProfileResponse is the wire shape returned by GET endpoints. The
// api_key is NEVER returned — callers see api_key_set:bool +
// api_key_hint (mask) only.
type ProfileResponse struct {
	ID                       string `json:"id"`
	Name                     string `json:"name"`
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
	IsActive                 bool   `json:"is_active"`
	CreatedAt                string `json:"created_at,omitempty"`
	UpdatedAt                string `json:"updated_at,omitempty"`
}

// ProfileCreateRequest is the wire shape for POST. APIKey is plaintext;
// the adapter encrypts before persisting.
type ProfileCreateRequest struct {
	Name                     string `json:"name"`
	Provider                 string `json:"provider"`
	BaseURL                  string `json:"base_url"`
	APIKey                   string `json:"api_key,omitempty"`
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

// ProfileUpdateRequest is the wire shape for PUT. Pointer fields are
// pointer-patch (codex-M3): omitted = preserve, JSON null is ALSO
// preserve (legacy clear-via-empty is supported by sending an explicit
// empty string for the api_key field, OR by setting clearAPIKey=true).
//
// Name special-cases: omitted = preserve; empty string = 422
// (rejected; profile name cannot be cleared).
type ProfileUpdateRequest struct {
	Name                     *string `json:"name,omitempty"`
	Provider                 *string `json:"provider,omitempty"`
	BaseURL                  *string `json:"base_url,omitempty"`
	APIKey                   *string `json:"api_key,omitempty"`
	ClearAPIKey              bool    `json:"clear_api_key,omitempty"`
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

// ─────────────────────────────────────────────────────────────────────────
// Sentinel errors (cross-package matching via errors.Is)
// ─────────────────────────────────────────────────────────────────────────

// ErrProfileNotFound is the rest-package mirror for the profile-store
// not-found sentinel. The cli adapter translates db.ErrProfileNotFound
// into this so handlers can errors.Is without importing internal/db.
var ErrProfileNotFound = errors.New("llm profile not found")

// ErrDuplicateProfileName mirrors db.ErrDuplicateProfileName for the
// REST 409 response on CREATE / rename collision.
var ErrDuplicateProfileName = errors.New("llm profile with this name already exists")

// ErrProfileNameRequired mirrors db.ErrProfileNameRequired (422 on
// empty-name update).
var ErrProfileNameRequired = errors.New("llm profile name cannot be empty")

// ErrProfileNameTooLong mirrors db.ErrProfileNameTooLong.
var ErrProfileNameTooLong = errors.New("llm profile name exceeds 64 characters")

// ErrProfileActiveDeleteForbidden is returned when DELETE targets the
// active profile (D5: 409 with "switch active profile first").
var ErrProfileActiveDeleteForbidden = errors.New("cannot delete the active profile; switch active profile first")

// ErrProfileTargetNoLongerActive mirrors db.ErrTargetNoLongerActive
// (409 target_no_longer_active per codex-r1e Low).
var ErrProfileTargetNoLongerActive = errors.New("intended active profile is no longer active; another writer activated a different profile")

// ErrProfileVersionConflict mirrors db.ErrVersionConflict (409 with
// version_conflict body when the retry cap is exhausted).
var ErrProfileVersionConflict = errors.New("ca_llm_config version changed since read; retry cap exhausted")

// ─────────────────────────────────────────────────────────────────────────
// Handlers
// ─────────────────────────────────────────────────────────────────────────

func (s *Server) handleListLLMProfiles(w http.ResponseWriter, r *http.Request) {
	if s.llmProfileStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "LLM profiles unavailable (running in embedded mode without external SurrealDB)",
		})
		return
	}
	profiles, err := s.llmProfileStore.ListProfiles(r.Context())
	if err != nil {
		slog.Error("list llm profiles failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Failed to list profiles: " + err.Error(),
		})
		return
	}
	resp := map[string]interface{}{
		"profiles":               profiles,
		"active_profile_missing": s.llmProfileStore.ActiveProfileMissing(),
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetLLMProfile(w http.ResponseWriter, r *http.Request) {
	if s.llmProfileStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "LLM profiles unavailable",
		})
		return
	}
	id := chi.URLParam(r, "id")
	id = canonicalProfileID(id)
	profile, err := s.llmProfileStore.GetProfile(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrProfileNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Profile not found"})
			return
		}
		slog.Error("get llm profile failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Failed to load profile: " + err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func (s *Server) handleCreateLLMProfile(w http.ResponseWriter, r *http.Request) {
	if s.llmProfileStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "LLM profiles unavailable",
		})
		return
	}
	var req ProfileCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if err := validateProfileProvider(req.Provider); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	id, err := s.llmProfileStore.CreateProfile(r.Context(), req)
	if err != nil {
		mapProfileError(w, err, "create")
		return
	}
	// Nudge local resolver cache so the next Resolve sees the new
	// profile (it shouldn't be active yet, but the workspace version
	// bump means a re-fetch is cheap).
	if s.llmResolver != nil {
		s.llmResolver.InvalidateLocal()
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *Server) handleUpdateLLMProfile(w http.ResponseWriter, r *http.Request) {
	if s.llmProfileStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "LLM profiles unavailable",
		})
		return
	}
	id := canonicalProfileID(chi.URLParam(r, "id"))
	var req ProfileUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if req.Provider != nil {
		if err := validateProfileProvider(*req.Provider); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	if err := s.llmProfileStore.UpdateProfile(r.Context(), id, req); err != nil {
		mapProfileError(w, err, "update")
		return
	}
	if s.llmResolver != nil {
		s.llmResolver.InvalidateLocal()
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteLLMProfile(w http.ResponseWriter, r *http.Request) {
	if s.llmProfileStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "LLM profiles unavailable",
		})
		return
	}
	id := canonicalProfileID(chi.URLParam(r, "id"))
	if err := s.llmProfileStore.DeleteProfile(r.Context(), id); err != nil {
		mapProfileError(w, err, "delete")
		return
	}
	if s.llmResolver != nil {
		s.llmResolver.InvalidateLocal()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleActivateLLMProfile(w http.ResponseWriter, r *http.Request) {
	if s.llmProfileStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "LLM profiles unavailable",
		})
		return
	}
	id := canonicalProfileID(chi.URLParam(r, "id"))
	by := actorFromRequest(r)
	if err := s.llmProfileStore.ActivateProfile(r.Context(), id, by); err != nil {
		mapProfileError(w, err, "activate")
		return
	}
	if s.llmResolver != nil {
		s.llmResolver.InvalidateLocal()
	}
	w.WriteHeader(http.StatusNoContent)
}

// canonicalProfileID accepts either a bare record id ("default-migrated")
// or a full SurrealDB id ("ca_llm_profile:default-migrated") in URL
// params and normalizes to the full form expected by the store.
func canonicalProfileID(raw string) string {
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, ":") {
		return raw
	}
	return "ca_llm_profile:" + raw
}

func actorFromRequest(r *http.Request) string {
	// Reuse the existing actor-identity helper used by other admin
	// handlers; falls back to "admin" when no auth claim is present.
	if r == nil {
		return "admin"
	}
	userID, _ := currentActorIdentity(r)
	if userID == "" {
		return "admin"
	}
	return userID
}

func validateProfileProvider(provider string) error {
	if provider == "" {
		// Allow empty in the create path (the field is set via PUT
		// later); the store doesn't enforce a non-empty provider.
		return nil
	}
	valid := map[string]bool{
		"anthropic": true, "openai": true, "ollama": true, "vllm": true,
		"llama-cpp": true, "sglang": true, "lmstudio": true,
		"gemini": true, "openrouter": true,
	}
	if !valid[provider] {
		return errors.New("Invalid provider. Must be one of: anthropic, openai, ollama, vllm, llama-cpp, sglang, lmstudio, gemini, openrouter")
	}
	return nil
}

// mapProfileError translates store errors into HTTP responses. Keeps
// every handler's error path consistent.
func mapProfileError(w http.ResponseWriter, err error, op string) {
	switch {
	case errors.Is(err, ErrProfileNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Profile not found"})
	case errors.Is(err, ErrDuplicateProfileName):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "A profile with this name already exists (case-insensitive)"})
	case errors.Is(err, ErrProfileNameRequired):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "Profile name cannot be empty"})
	case errors.Is(err, ErrProfileNameTooLong):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "Profile name exceeds 64 characters"})
	case errors.Is(err, ErrProfileActiveDeleteForbidden):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Cannot delete the active profile. Switch to a different profile first."})
	case errors.Is(err, ErrProfileTargetNoLongerActive):
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "target_no_longer_active",
			"hint":  "Another writer activated a different profile during your edit. Refresh and try again.",
		})
	case errors.Is(err, ErrProfileVersionConflict):
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "version_conflict",
			"hint":  "The workspace state changed during your edit and retries were exhausted. Refresh and try again.",
		})
	case errors.Is(err, ErrLLMEncryptionKeyRequired):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "Cannot save API key: SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY is not set on the server. " +
				"Set the encryption key (32+ random bytes, base64-encoded) and restart, or unset the api_key field to save other settings only.",
		})
	default:
		slog.Error("llm profile op failed", "op", op, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Profile " + op + " failed: " + err.Error(),
		})
	}
}

// MaskAPIKeyHint exposes the masking helper for the cli/serve.go
// adapter without forcing the rest package to import maskutil from
// outside its callers' usual path.
func MaskAPIKeyHint(plaintext string) string {
	if plaintext == "" {
		return ""
	}
	return maskutil.Token(plaintext)
}

// FormatProfileTime formats a time.Time for the wire response. Empty
// time yields empty string.
func FormatProfileTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

