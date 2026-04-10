// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
)

// ---------------------------------------------------------------------------
// Strategy settings handlers
// ---------------------------------------------------------------------------

// handleListComprehensionSettings returns all settings records.
func (s *Server) handleListComprehensionSettings(w http.ResponseWriter, r *http.Request) {
	if s.comprehensionStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "comprehension settings not configured"})
		return
	}
	settings, err := s.comprehensionStore.ListSettings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// handleGetEffectiveComprehensionSettings resolves settings with inheritance for a given scope.
// Query params: scope_type (required), scope_key (optional, defaults to "").
func (s *Server) handleGetEffectiveComprehensionSettings(w http.ResponseWriter, r *http.Request) {
	if s.comprehensionStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "comprehension settings not configured"})
		return
	}
	scopeType := r.URL.Query().Get("scope_type")
	scopeKey := r.URL.Query().Get("scope_key")
	if scopeType == "" {
		scopeType = "workspace"
		scopeKey = "default"
	}
	scope := comprehension.Scope{
		Type: comprehension.ScopeType(scopeType),
		Key:  scopeKey,
	}
	eff, err := comprehension.Resolve(s.comprehensionStore, scope)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, eff)
}

// handleUpdateComprehensionSettings creates or updates settings for a scope.
func (s *Server) handleUpdateComprehensionSettings(w http.ResponseWriter, r *http.Request) {
	if s.comprehensionStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "comprehension settings not configured"})
		return
	}
	var settings comprehension.Settings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if settings.ScopeType == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "scopeType is required"})
		return
	}

	userID, _ := currentActorIdentity(r)
	settings.UpdatedBy = userID

	if err := s.comprehensionStore.SetSettings(&settings); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Return the effective settings for the saved scope
	scope := comprehension.Scope{Type: settings.ScopeType, Key: settings.ScopeKey}
	eff, err := comprehension.Resolve(s.comprehensionStore, scope)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, eff)
}

// handleResetComprehensionSettings deletes the settings for a scope,
// reverting it to pure inheritance.
func (s *Server) handleResetComprehensionSettings(w http.ResponseWriter, r *http.Request) {
	if s.comprehensionStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "comprehension settings not configured"})
		return
	}
	scopeType := r.URL.Query().Get("scope_type")
	scopeKey := r.URL.Query().Get("scope_key")
	if scopeType == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "scope_type is required"})
		return
	}
	scope := comprehension.Scope{
		Type: comprehension.ScopeType(scopeType),
		Key:  scopeKey,
	}
	if err := s.comprehensionStore.DeleteSettings(scope); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
}

// ---------------------------------------------------------------------------
// Model capabilities handlers
// ---------------------------------------------------------------------------

// handleListModelCapabilities returns all model capability profiles.
func (s *Server) handleListModelCapabilities(w http.ResponseWriter, r *http.Request) {
	if s.comprehensionStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "comprehension settings not configured"})
		return
	}
	models, err := s.comprehensionStore.ListModelCapabilities()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, models)
}

// handleGetModelCapabilities returns the capability profile for a specific model.
func (s *Server) handleGetModelCapabilities(w http.ResponseWriter, r *http.Request) {
	if s.comprehensionStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "comprehension settings not configured"})
		return
	}
	modelID := chi.URLParam(r, "modelId")
	mc, err := s.comprehensionStore.GetModelCapabilities(modelID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if mc == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "model not found"})
		return
	}
	writeJSON(w, http.StatusOK, mc)
}

// handleUpdateModelCapabilities creates or updates a model capability profile.
func (s *Server) handleUpdateModelCapabilities(w http.ResponseWriter, r *http.Request) {
	if s.comprehensionStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "comprehension settings not configured"})
		return
	}
	var mc comprehension.ModelCapabilities
	if err := json.NewDecoder(r.Body).Decode(&mc); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if mc.ModelID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "modelId is required"})
		return
	}
	if err := s.comprehensionStore.SetModelCapabilities(&mc); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, mc)
}

// handleDeleteModelCapabilities removes a model from the capability registry.
func (s *Server) handleDeleteModelCapabilities(w http.ResponseWriter, r *http.Request) {
	if s.comprehensionStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "comprehension settings not configured"})
		return
	}
	modelID := chi.URLParam(r, "modelId")
	if err := s.comprehensionStore.DeleteModelCapabilities(modelID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
