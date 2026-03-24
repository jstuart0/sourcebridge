// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

type TokenKind string

const (
	TokenKindIDESession TokenKind = "ide_session"
	TokenKindAdminAPI   TokenKind = "admin_api"
	TokenKindCIAPI      TokenKind = "ci_api"
)

type AuthMethod string

const (
	AuthMethodLocalPassword AuthMethod = "local_password"
	AuthMethodOIDC          AuthMethod = "oidc"
	AuthMethodManual        AuthMethod = "manual"
)

// APIToken represents a persisted API token/session record.
type APIToken struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Prefix      string     `json:"prefix"`
	TokenHash   string     `json:"-"`
	UserID      string     `json:"user_id"`
	TenantID    string     `json:"tenant_id,omitempty"`
	Kind        TokenKind  `json:"token_kind"`
	ClientType  string     `json:"client_type,omitempty"`
	AuthMethod  AuthMethod `json:"auth_method,omitempty"`
	DeviceLabel string     `json:"device_label,omitempty"`
	Metadata    string     `json:"metadata,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

type CreateTokenInput struct {
	Name        string
	UserID      string
	TenantID    string
	Kind        TokenKind
	ClientType  string
	AuthMethod  AuthMethod
	DeviceLabel string
	Metadata    string
	ExpiresAt   *time.Time
}

type APITokenStore interface {
	CreateToken(ctx context.Context, input CreateTokenInput) (token string, record *APIToken, err error)
	ValidateToken(ctx context.Context, rawToken string) (*APIToken, error)
	ListTokens(ctx context.Context) ([]*APIToken, error)
	RevokeToken(ctx context.Context, id string) (bool, error)
}

// MemoryAPITokenStore keeps API tokens in memory for embedded/dev mode.
type MemoryAPITokenStore struct {
	mu     sync.RWMutex
	tokens map[string]*APIToken
	byHash map[string]string
	nextID int
}

func NewAPITokenStore() APITokenStore {
	return &MemoryAPITokenStore{
		tokens: make(map[string]*APIToken),
		byHash: make(map[string]string),
	}
}

func (s *MemoryAPITokenStore) CreateToken(_ context.Context, input CreateTokenInput) (string, *APIToken, error) {
	tokenStr, prefix, hashStr, err := generateTokenSecret()
	if err != nil {
		return "", nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	id := fmt.Sprintf("%04x", s.nextID)
	now := time.Now()
	record := &APIToken{
		ID:          id,
		Name:        input.Name,
		Prefix:      prefix,
		TokenHash:   hashStr,
		UserID:      input.UserID,
		TenantID:    input.TenantID,
		Kind:        normalizeTokenKind(input.Kind),
		ClientType:  input.ClientType,
		AuthMethod:  input.AuthMethod,
		DeviceLabel: input.DeviceLabel,
		Metadata:    input.Metadata,
		CreatedAt:   now,
		ExpiresAt:   input.ExpiresAt,
	}
	s.tokens[id] = record
	s.byHash[hashStr] = id
	return tokenStr, cloneToken(record), nil
}

func (s *MemoryAPITokenStore) ValidateToken(_ context.Context, rawToken string) (*APIToken, error) {
	hashStr := hashToken(rawToken)

	s.mu.Lock()
	defer s.mu.Unlock()

	id, ok := s.byHash[hashStr]
	if !ok {
		return nil, nil
	}
	token := s.tokens[id]
	if token == nil || token.RevokedAt != nil {
		return nil, nil
	}
	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) {
		return nil, nil
	}
	now := time.Now()
	token.LastUsedAt = &now
	return cloneToken(token), nil
}

func (s *MemoryAPITokenStore) ListTokens(_ context.Context) ([]*APIToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*APIToken, 0, len(s.tokens))
	for _, t := range s.tokens {
		result = append(result, cloneToken(t))
	}
	return result, nil
}

func (s *MemoryAPITokenStore) RevokeToken(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	token, ok := s.tokens[id]
	if !ok {
		return false, nil
	}
	now := time.Now()
	token.RevokedAt = &now
	return true, nil
}

func normalizeTokenKind(kind TokenKind) TokenKind {
	switch kind {
	case TokenKindIDESession, TokenKindAdminAPI, TokenKindCIAPI:
		return kind
	default:
		return TokenKindAdminAPI
	}
}

func generateTokenSecret() (token, prefix, hashStr string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", fmt.Errorf("crypto/rand failed: %w", err)
	}
	token = "ca_" + hex.EncodeToString(raw)
	prefix = token[:11]
	hashStr = hashToken(token)
	return token, prefix, hashStr, nil
}

func hashToken(rawToken string) string {
	hash := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(hash[:])
}

func cloneToken(token *APIToken) *APIToken {
	if token == nil {
		return nil
	}
	cp := *token
	return &cp
}
