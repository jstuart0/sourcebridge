// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/version"
)

var serverStartTime = time.Now()

func (s *Server) handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	workerStatus := "unavailable"
	if s.worker != nil {
		healthy, err := s.worker.CheckHealth(context.Background())
		if err == nil && healthy {
			workerStatus = "healthy"
		} else if err == nil {
			workerStatus = "degraded"
		}
	}

	dbStatus := "healthy"
	if s.cfg.Storage.SurrealMode == "external" && s.store == nil {
		dbStatus = "unavailable"
	}

	// Knowledge stats.
	knowledgeStats := map[string]interface{}{
		"configured": s.knowledgeStore != nil,
	}
	if s.knowledgeStore != nil {
		knowledgeStats["artifacts"] = s.collectKnowledgeStats(s.getStore(r))
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"version":   version.Version,
		"commit":    version.Commit,
		"uptime":    time.Since(serverStartTime).String(),
		"database":  dbStatus,
		"worker":    workerStatus,
		"env":       s.cfg.Env,
		"knowledge": knowledgeStats,
	})
}

func (s *Server) handleAdminConfig(w http.ResponseWriter, r *http.Request) {
	// Sanitized config — never return secrets
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"server": map[string]interface{}{
			"http_port":       s.cfg.Server.HTTPPort,
			"public_base_url": s.cfg.Server.PublicBaseURL,
		},
		"storage": map[string]interface{}{
			"surreal_mode": s.cfg.Storage.SurrealMode,
			"surreal_url":  s.cfg.Storage.SurrealURL,
			"redis_mode":   s.cfg.Storage.RedisMode,
		},
		"llm": map[string]interface{}{
			"provider":      s.cfg.LLM.Provider,
			"base_url":      s.cfg.LLM.BaseURL,
			"api_key_set":   s.cfg.LLM.APIKey != "",
			"summary_model": s.cfg.LLM.SummaryModel,
			"review_model":  s.cfg.LLM.ReviewModel,
			"ask_model":     s.cfg.LLM.AskModel,
		},
		"security": map[string]interface{}{
			"csrf_enabled":    s.cfg.Security.CSRFEnabled,
			"mode":            s.cfg.Security.Mode,
			"oidc_configured": s.cfg.Security.OIDC.IssuerURL != "",
		},
		"worker": map[string]interface{}{
			"address": s.cfg.Worker.Address,
		},
		"git": map[string]interface{}{
			"default_token_set": s.cfg.Git.DefaultToken != "",
			"ssh_key_path":      s.cfg.Git.SSHKeyPath,
		},
	})
}

func (s *Server) handleAdminUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var updates map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	// For OSS, runtime config updates are limited.
	// In production, these would persist to ca_config:runtime in SurrealDB.
	// For now, acknowledge the request.
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "accepted",
		"note":   "Runtime config updates require server restart to take effect in OSS mode. Set environment variables for persistent changes.",
	})
}

func (s *Server) handleAdminTestWorker(w http.ResponseWriter, r *http.Request) {
	if s.worker == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status": "unavailable",
			"error":  "worker not configured",
		})
		return
	}

	healthy, err := s.worker.CheckHealth(context.Background())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status": "error",
			"error":  err.Error(),
		})
		return
	}

	status := "healthy"
	if !healthy {
		status = "unhealthy"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": status,
	})
}

func (s *Server) handleAdminTestLLM(w http.ResponseWriter, r *http.Request) {
	if s.worker == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status": "unavailable",
			"error":  "worker not configured — LLM tests require the worker service",
		})
		return
	}

	// A lightweight health check is the best we can do without sending a real prompt.
	healthy, err := s.worker.CheckHealth(context.Background())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status": "error",
			"error":  err.Error(),
		})
		return
	}
	if !healthy {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status": "degraded",
			"note":   "worker is reachable but not serving",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "ok",
		"note":   "Worker is healthy. LLM availability depends on worker's connection to the LLM provider.",
	})
}

// --- API Token endpoints ---

type createTokenRequest struct {
	Name string `json:"name"`
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req createTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, `{"error":"name is required"}`, http.StatusBadRequest)
		return
	}

	userID, tenantID := currentActorIdentity(r)
	tokenStr, record, err := s.tokenStore.CreateToken(r.Context(), auth.CreateTokenInput{
		Name:       req.Name,
		UserID:     userID,
		TenantID:   tenantID,
		Kind:       auth.TokenKindAdminAPI,
		ClientType: "web_admin",
		AuthMethod: auth.AuthMethodManual,
	})
	if err != nil {
		http.Error(w, `{"error":"failed to create token"}`, http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":         record.ID,
		"name":       record.Name,
		"prefix":     record.Prefix,
		"token":      tokenStr, // only returned on creation
		"created_at": record.CreatedAt,
	})
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.tokenStore.ListTokens(r.Context())
	if err != nil {
		http.Error(w, `{"error":"failed to list tokens"}`, http.StatusInternalServerError)
		return
	}
	tokens = filterTokens(tokens, r)
	writeJSON(w, http.StatusOK, tokens)
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, `{"error":"token ID required"}`, http.StatusBadRequest)
		return
	}

	if ok, err := s.tokenStore.RevokeToken(r.Context(), id); err != nil {
		http.Error(w, `{"error":"failed to revoke token"}`, http.StatusInternalServerError)
	} else if ok {
		writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
	} else {
		http.Error(w, `{"error":"token not found"}`, http.StatusNotFound)
	}
}

func (s *Server) handleRevokeUserTokens(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID     string `json:"user_id"`
		Kind       string `json:"kind"`
		ClientType string `json:"client_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.UserID) == "" {
		http.Error(w, `{"error":"user_id is required"}`, http.StatusBadRequest)
		return
	}

	tokens, err := s.tokenStore.ListTokens(r.Context())
	if err != nil {
		http.Error(w, `{"error":"failed to list tokens"}`, http.StatusInternalServerError)
		return
	}
	revoked := 0
	for _, token := range tokens {
		if token.UserID != req.UserID {
			continue
		}
		if req.Kind != "" && string(token.Kind) != req.Kind {
			continue
		}
		if req.ClientType != "" && token.ClientType != req.ClientType {
			continue
		}
		ok, err := s.tokenStore.RevokeToken(r.Context(), token.ID)
		if err != nil {
			http.Error(w, `{"error":"failed to revoke user tokens"}`, http.StatusInternalServerError)
			return
		}
		if ok {
			revoked++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "revoked",
		"user_id": req.UserID,
		"count":   revoked,
	})
}

func filterTokens(tokens []*auth.APIToken, r *http.Request) []*auth.APIToken {
	kind := r.URL.Query().Get("kind")
	clientType := r.URL.Query().Get("client_type")
	authMethod := r.URL.Query().Get("auth_method")
	userID := r.URL.Query().Get("user_id")
	tenantID := r.URL.Query().Get("tenant_id")
	activeOnly := r.URL.Query().Get("active_only") == "true"
	if kind == "" && clientType == "" && authMethod == "" && userID == "" && tenantID == "" && !activeOnly {
		return tokens
	}
	now := time.Now()
	filtered := make([]*auth.APIToken, 0, len(tokens))
	for _, token := range tokens {
		if kind != "" && string(token.Kind) != kind {
			continue
		}
		if clientType != "" && token.ClientType != clientType {
			continue
		}
		if authMethod != "" && string(token.AuthMethod) != authMethod {
			continue
		}
		if userID != "" && token.UserID != userID {
			continue
		}
		if tenantID != "" && token.TenantID != tenantID {
			continue
		}
		if activeOnly {
			if token.RevokedAt != nil {
				continue
			}
			if token.ExpiresAt != nil && now.After(*token.ExpiresAt) {
				continue
			}
		}
		filtered = append(filtered, token)
	}
	return filtered
}
