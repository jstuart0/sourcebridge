// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/sourcebridge/sourcebridge/internal/db"
	gitres "github.com/sourcebridge/sourcebridge/internal/git/resolution"
	"github.com/sourcebridge/sourcebridge/internal/maskutil"
	"github.com/sourcebridge/sourcebridge/internal/secretcipher"
)

// GitConfigStore persists git configuration (default PAT, SSH key path)
// so they survive server restarts. R3 slice 2: every method takes ctx,
// LoadGitConfig returns version, SaveGitConfig encrypts via the cipher
// and bumps the version cell. When nil, the REST handler falls back to
// the env-bootstrap layer of cfg.Git only.
type GitConfigStore interface {
	LoadGitConfig(ctx context.Context) (token, sshKeyPath string, version uint64, err error)
	SaveGitConfig(ctx context.Context, token, sshKeyPath string) error
}

// gitConfigLoaderAdapter narrows a ctx-aware GitConfigStore to the
// legacy GitConfigLoader shape the GraphQL package uses for backward-
// compatible test wiring. Production wiring sets Resolver.GitResolver,
// which takes precedence over the legacy loader.
type gitConfigLoaderAdapter struct{ s GitConfigStore }

func (a *gitConfigLoaderAdapter) LoadGitConfig() (string, string, error) {
	if a == nil || a.s == nil {
		return "", "", nil
	}
	t, p, _, err := a.s.LoadGitConfig(context.Background())
	return t, p, err
}

// gitConfigLoaderFromStore wraps a ctx-aware store as a legacy loader.
// Returns nil when the input is nil so the GraphQL Resolver's GitConfig
// stays nil-safe.
func gitConfigLoaderFromStore(s GitConfigStore) *gitConfigLoaderAdapter {
	if s == nil {
		return nil
	}
	return &gitConfigLoaderAdapter{s: s}
}

type gitConfigResponse struct {
	DefaultTokenSet  bool   `json:"default_token_set"`
	DefaultTokenHint string `json:"default_token_hint,omitempty"`
	SSHKeyPath       string `json:"ssh_key_path"`
	Stale            bool   `json:"stale,omitempty"`
	IntegrityError   string `json:"integrity_error,omitempty"`
}

type updateGitConfigRequest struct {
	DefaultToken *string `json:"default_token,omitempty"`
	SSHKeyPath   *string `json:"ssh_key_path,omitempty"`
}

// handleGetGitConfig returns the resolved view of the git credentials.
// R3 slice 2: reads the resolver's snapshot rather than s.cfg.Git
// (which is env-only after R3). Surfaces Stale (DB outage) and
// IntegrityError (corrupt envelope / missing key) so the admin UI can
// show a banner.
func (s *Server) handleGetGitConfig(w http.ResponseWriter, r *http.Request) {
	if s.gitResolver == nil {
		// Embedded/test mode without a workspace resolver: report
		// env-bootstrap only.
		resp := gitConfigResponse{
			DefaultTokenSet: s.cfg.Git.DefaultToken != "",
			SSHKeyPath:      s.cfg.Git.SSHKeyPath,
		}
		if s.cfg.Git.DefaultToken != "" {
			resp.DefaultTokenHint = maskutil.Token(s.cfg.Git.DefaultToken)
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	snap, err := s.gitResolver.Resolve(r.Context())
	if err != nil {
		http.Error(w, `{"error":"resolve git config failed"}`, http.StatusServiceUnavailable)
		return
	}
	resp := gitConfigResponse{
		DefaultTokenSet: snap.Token != "",
		SSHKeyPath:      snap.SSHKeyPath,
		Stale:           snap.Stale,
	}
	if snap.Token != "" {
		resp.DefaultTokenHint = maskutil.Token(snap.Token)
	}
	if snap.IntegrityError != nil {
		// Admin-facing message — the UI shows a banner instructing the
		// operator to re-save or restore the encryption key.
		resp.IntegrityError = snap.IntegrityError.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleUpdateGitConfig persists git credentials with a strict
// load-then-merge-then-save shape (mirrors the LLM PUT handler). A load
// failure aborts with 503 — we cannot do a partial-update merge without
// the current row, and silently using empty values would clobber the
// other field.
//
// Validates the SSH key path server-side using the resolver package's
// SSHKeyPathValidator. Empty allowed; otherwise absolute, no traversal,
// no shell metachars, must reside under the configured allow-root.
//
// Calls InvalidateLocal on the resolver after a successful save so this
// replica sees the new value on the very next Resolve (peer replicas
// pick it up via the version-cell read).
func (s *Server) handleUpdateGitConfig(w http.ResponseWriter, r *http.Request) {
	var req updateGitConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	if s.gitConfigStore == nil {
		// Embedded/test mode: no persistence available. Refuse rather
		// than silently keep an in-memory mutation that disappears at
		// restart (the legacy behavior, which led to admins thinking a
		// save took when it hadn't).
		http.Error(w, `{"error":"git config persistence unavailable"}`, http.StatusServiceUnavailable)
		return
	}

	// 1. Load current persisted record. A LOAD failure is fatal — we
	//    cannot do a partial-update merge without it.
	curT, curS, _, loadErr := s.gitConfigStore.LoadGitConfig(ctx)
	if loadErr != nil {
		// Special case: an integrity-error on the existing row means
		// "the workspace was corrupted, but the operator is now
		// supplying a fresh value — let them save through". We accept
		// the save when the request body explicitly sets default_token
		// to a non-empty string (effectively a re-key); otherwise we
		// fail with 503 so the operator sees what's wrong.
		if errors.Is(loadErr, db.ErrGitTokenDecryptFailed) && req.DefaultToken != nil && *req.DefaultToken != "" {
			slog.Warn("git config: existing row failed to decrypt; accepting save as a re-key")
			curT = ""
			curS = ""
		} else {
			slog.Warn("git config: load before save failed; refusing to save partial update", "error", loadErr)
			http.Error(w, `{"error":"git config: cannot load current record; refusing to save partial update"}`, http.StatusServiceUnavailable)
			return
		}
	}

	// 2. Apply only non-nil request fields (pointer semantics).
	newT, newS := curT, curS
	if req.DefaultToken != nil {
		newT = *req.DefaultToken
	}
	if req.SSHKeyPath != nil {
		newS = *req.SSHKeyPath
	}

	// 3. Validate (server-side validation is the authoritative gate).
	allowRoot := ""
	if s.cfg != nil {
		allowRoot = s.cfg.Git.SSHKeyPathRoot
	}
	if err := gitres.NewSSHKeyPathValidator(allowRoot).Validate(newS); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}

	// 4. Save (encrypts + bumps version atomically).
	if err := s.gitConfigStore.SaveGitConfig(ctx, newT, newS); err != nil {
		// Differentiate the missing-encryption-key case (operator
		// remediation: set SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY) from
		// other failures.
		if errors.Is(err, secretcipher.ErrEncryptionKeyRequired) || errors.Is(err, db.ErrGitTokenEncryptionKeyRequired) {
			slog.Warn("git config: save refused — encryption key missing; admin must set SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY")
			http.Error(w, `{"error":"git token cannot be saved without an encryption key (set SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY or enable SOURCEBRIDGE_ALLOW_UNENCRYPTED_GIT_TOKEN for OSS development)"}`, http.StatusUnprocessableEntity)
			return
		}
		slog.Warn("failed to persist git config", "error", err)
		http.Error(w, `{"error":"failed to persist git config"}`, http.StatusInternalServerError)
		return
	}

	// 5. Nudge the resolver cache so this replica sees the change
	//    immediately (peers pick it up via the version-cell read).
	if s.gitResolver != nil {
		s.gitResolver.InvalidateLocal()
	}

	// 6. Return masked view (load fresh; never echo the raw token).
	freshT, freshS, _, _ := s.gitConfigStore.LoadGitConfig(ctx)
	resp := map[string]interface{}{
		"status":            "saved",
		"default_token_set": freshT != "",
		"ssh_key_path":      freshS,
		"note":              "Settings saved and will persist across restarts.",
	}
	if freshT != "" {
		resp["default_token_hint"] = maskutil.Token(freshT)
	}
	writeJSON(w, http.StatusOK, resp)
}
