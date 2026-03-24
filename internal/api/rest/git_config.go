// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// GitConfigStore persists git configuration (tokens, SSH paths) so they
// survive server restarts.  When nil, settings are in-memory only.
type GitConfigStore interface {
	LoadGitConfig() (token, sshKeyPath string, err error)
	SaveGitConfig(token, sshKeyPath string) error
}

type gitConfigResponse struct {
	DefaultTokenSet  bool   `json:"default_token_set"`
	DefaultTokenHint string `json:"default_token_hint,omitempty"`
	SSHKeyPath       string `json:"ssh_key_path"`
}

type updateGitConfigRequest struct {
	DefaultToken *string `json:"default_token,omitempty"`
	SSHKeyPath   *string `json:"ssh_key_path,omitempty"`
}

// maskToken returns the first 4 and last 4 characters with dots in between.
func maskToken(token string) string {
	if len(token) <= 8 {
		return "****"
	}
	return token[:4] + "..." + token[len(token)-4:]
}

func (s *Server) handleGetGitConfig(w http.ResponseWriter, r *http.Request) {
	resp := gitConfigResponse{
		DefaultTokenSet: s.cfg.Git.DefaultToken != "",
		SSHKeyPath:      s.cfg.Git.SSHKeyPath,
	}
	if s.cfg.Git.DefaultToken != "" {
		resp.DefaultTokenHint = maskToken(s.cfg.Git.DefaultToken)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleUpdateGitConfig(w http.ResponseWriter, r *http.Request) {
	var req updateGitConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	if req.DefaultToken != nil {
		s.cfg.Git.DefaultToken = *req.DefaultToken
	}
	if req.SSHKeyPath != nil {
		s.cfg.Git.SSHKeyPath = *req.SSHKeyPath
	}

	// Persist to database if available
	if s.gitConfigStore != nil {
		if err := s.gitConfigStore.SaveGitConfig(s.cfg.Git.DefaultToken, s.cfg.Git.SSHKeyPath); err != nil {
			slog.Warn("failed to persist git config", "error", err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":            "saved",
		"default_token_set": s.cfg.Git.DefaultToken != "",
		"ssh_key_path":      s.cfg.Git.SSHKeyPath,
		"note":              "Settings saved and will persist across restarts.",
	})
}
