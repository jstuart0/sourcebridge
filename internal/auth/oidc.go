// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/sourcebridge/sourcebridge/internal/config"
)

type OIDCStateStore interface {
	SaveState(ctx context.Context, state string, expiresAt time.Time) error
	ConsumeState(ctx context.Context, state string) (bool, error)
}

type MemoryOIDCStateStore struct {
	mu     sync.Mutex
	states map[string]time.Time
}

func NewMemoryOIDCStateStore() *MemoryOIDCStateStore {
	return &MemoryOIDCStateStore{states: make(map[string]time.Time)}
}

func (s *MemoryOIDCStateStore) SaveState(_ context.Context, state string, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[state] = expiresAt
	return nil
}

func (s *MemoryOIDCStateStore) ConsumeState(_ context.Context, state string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	expiresAt, ok := s.states[state]
	if !ok {
		return false, nil
	}
	delete(s.states, state)
	return time.Now().Before(expiresAt), nil
}

// OIDCProvider handles OpenID Connect authentication via an external IdP.
type OIDCProvider struct {
	oauth2Config oauth2.Config
	verifier     *oidc.IDTokenVerifier
	jwtManager   *JWTManager
	stateStore   OIDCStateStore
}

// NewOIDCProvider creates a provider using OIDC discovery against the issuer.
func NewOIDCProvider(ctx context.Context, cfg config.OIDCConfig, jwtMgr *JWTManager, stateStore OIDCStateStore) (*OIDCProvider, error) {
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery failed for %s: %w", cfg.IssuerURL, err)
	}

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	if stateStore == nil {
		stateStore = NewMemoryOIDCStateStore()
	}

	o := &OIDCProvider{
		oauth2Config: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       scopes,
		},
		verifier:   provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		jwtManager: jwtMgr,
		stateStore: stateStore,
	}

	slog.Info("OIDC provider initialized", "issuer", cfg.IssuerURL, "client_id", cfg.ClientID)
	return o, nil
}

// AuthorizationURL generates the URL to redirect the user to for login.
func (o *OIDCProvider) AuthorizationURL(ctx context.Context) (string, string, error) {
	state, err := generateState()
	if err != nil {
		return "", "", fmt.Errorf("generating state: %w", err)
	}
	if err := o.stateStore.SaveState(ctx, state, time.Now().Add(10*time.Minute)); err != nil {
		return "", "", fmt.Errorf("saving state: %w", err)
	}
	url := o.oauth2Config.AuthCodeURL(state)
	return url, state, nil
}

// Exchange validates the state, exchanges the authorization code for tokens,
// verifies the ID token, and returns an internal JWT for the session.
func (o *OIDCProvider) Exchange(ctx context.Context, state, code string) (string, error) {
	valid, err := o.stateStore.ConsumeState(ctx, state)
	if err != nil {
		return "", fmt.Errorf("consuming state: %w", err)
	}
	if !valid {
		return "", fmt.Errorf("invalid or expired state parameter")
	}

	oauth2Token, err := o.oauth2Config.Exchange(ctx, code)
	if err != nil {
		return "", fmt.Errorf("code exchange failed: %w", err)
	}

	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		return "", fmt.Errorf("no id_token in token response")
	}

	idToken, err := o.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return "", fmt.Errorf("id_token verification failed: %w", err)
	}

	var claims struct {
		Email string `json:"email"`
		Name  string `json:"name"`
		Sub   string `json:"sub"`
		OrgID string `json:"org_id"`
		Role  string `json:"role"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return "", fmt.Errorf("extracting claims: %w", err)
	}
	if claims.Email == "" {
		claims.Email = claims.Sub + "@oidc"
	}

	slog.Info("OIDC login successful", "email", claims.Email, "sub", claims.Sub)
	return o.jwtManager.GenerateToken(claims.Sub, claims.Email, claims.OrgID, claims.Role)
}

func generateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}
