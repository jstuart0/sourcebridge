// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/qa"
)

// checkRepoAccess returns an error if the caller lacks access to repoID.
// It uses the tenant-filtered store injected by RepoAccessMiddleware when
// enterprise tenant filtering is active, falling back to the base store.
// Returns non-nil when repoID is empty or the store cannot resolve the repo.
func (s *Server) checkRepoAccess(r *http.Request, repoID string) error {
	if repoID == "" {
		return errRepoNotFound
	}
	store := s.getStore(r)
	if store == nil || store.GetRepository(r.Context(), repoID) == nil {
		return errRepoNotFound
	}
	return nil
}

// errRepoNotFound is the sentinel returned by checkRepoAccess when access is denied.
// Using a package-level value avoids allocating on every forbidden request.
var errRepoNotFound = &repoAccessError{"repository not found or access denied"}

type repoAccessError struct{ msg string }

func (e *repoAccessError) Error() string { return e.msg }

// askRequest mirrors qa.AskInput on the REST wire. Field names are
// camelCase to match the GraphQL input shape so callers can use the
// same payload against either transport.
type askRequest struct {
	RepositoryID   string   `json:"repositoryId"`
	Question       string   `json:"question"`
	Mode           string   `json:"mode,omitempty"`
	ConversationID string   `json:"conversationId,omitempty"`
	PriorMessages  []string `json:"priorMessages,omitempty"`
	FilePath       string   `json:"filePath,omitempty"`
	Code           string   `json:"code,omitempty"`
	Language       string   `json:"language,omitempty"`
	ArtifactID     string   `json:"artifactId,omitempty"`
	SymbolID       string   `json:"symbolId,omitempty"`
	RequirementID  string   `json:"requirementId,omitempty"`
	IncludeDebug   bool     `json:"includeDebug,omitempty"`
}

func (r askRequest) toAskInput() qa.AskInput {
	return qa.AskInput{
		RepositoryID:   r.RepositoryID,
		Question:       r.Question,
		Mode:           qa.Mode(strings.ToLower(r.Mode)),
		ConversationID: r.ConversationID,
		PriorMessages:  r.PriorMessages,
		FilePath:       r.FilePath,
		Code:           r.Code,
		Language:       r.Language,
		ArtifactID:     r.ArtifactID,
		SymbolID:       r.SymbolID,
		RequirementID:  r.RequirementID,
		IncludeDebug:   r.IncludeDebug,
	}
}

// handleAsk is the unary server-side QA endpoint. Streaming continues
// to live on POST /api/v1/discuss/stream through the migration; a
// dedicated ask streaming adapter is a follow-up (see plan §Not Goals).
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.QA.ServerSideEnabled || s.qaOrchestrator == nil {
		writeAskJSONErr(w, http.StatusServiceUnavailable, "server-side QA is disabled on this deployment")
		return
	}
	var req askRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAskJSONErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	in := req.toAskInput()
	// SEC-5: verify the caller has access to the requested repository before
	// allowing the QA orchestrator to reason over its indexed content.
	if err := s.checkRepoAccess(r, in.RepositoryID); err != nil {
		writeAskJSONErr(w, http.StatusForbidden, "forbidden: no access to repository")
		return
	}
	res, err := s.qaOrchestrator.Ask(r.Context(), in)
	if err != nil {
		if qa.IsInvalidInput(err) {
			writeAskJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeAskJSONErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func writeAskJSONErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
