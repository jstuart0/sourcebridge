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

// tokenRoleDefault is the role assigned to newly minted tokens when no role is
// explicitly requested. Kept package-private because callers should never need
// to reference it directly — use RoleAdmin / RoleUser from roles.go.
const tokenRoleDefault = RoleUser

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
	// Role is the effective role this token grants when used as a Bearer
	// credential.  New tokens default to RoleUser (least privilege).
	// Pre-existing tokens were backfilled to RoleAdmin by migration 056 to
	// preserve the behavior that existed before SEC-2 was fixed.
	Role      string     `json:"role"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
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
	// Role is the effective role this token should carry.  Callers that do not
	// set this field receive the default (RoleUser / "user").  Only callers with
	// an admin claim may supply RoleAdmin here; the REST handler enforces this.
	Role string `json:"role,omitempty"`
}

type APITokenStore interface {
	CreateToken(ctx context.Context, input CreateTokenInput) (token string, record *APIToken, err error)
	ValidateToken(ctx context.Context, rawToken string) (*APIToken, error)
	ListTokens(ctx context.Context) ([]*APIToken, error)
	RevokeToken(ctx context.Context, id string) (bool, error)
}

// MemoryAPITokenStore keeps API tokens in memory for embedded/dev mode.
//
// tokenHashKey is the HMAC key used for new-format token hashes (CA-220).
// When empty, the store falls back to the legacy bare-SHA-256 format on
// both writes and reads. When non-empty, new writes use HMAC-SHA256 and
// reads transparently accept both formats (with opportunistic legacy →
// HMAC migration on legacy-format hits).
type MemoryAPITokenStore struct {
	mu           sync.RWMutex
	tokens       map[string]*APIToken
	byHash       map[string]string
	nextID       int
	tokenHashKey []byte
}

// NewAPITokenStore returns a memory token store with HMAC token hashing
// disabled (legacy SHA-256). Test-only convenience; production callers
// use NewMemoryAPITokenStoreWithKey.
func NewAPITokenStore() APITokenStore {
	return &MemoryAPITokenStore{
		tokens: make(map[string]*APIToken),
		byHash: make(map[string]string),
	}
}

// NewMemoryAPITokenStoreWithKey returns a memory token store whose
// new-format hashes are HMAC-SHA256 keyed with key. Pass nil/empty to
// preserve the legacy SHA-256-only behavior.
func NewMemoryAPITokenStoreWithKey(key []byte) *MemoryAPITokenStore {
	return &MemoryAPITokenStore{
		tokens:       make(map[string]*APIToken),
		byHash:       make(map[string]string),
		tokenHashKey: append([]byte(nil), key...),
	}
}

func (s *MemoryAPITokenStore) CreateToken(_ context.Context, input CreateTokenInput) (string, *APIToken, error) {
	tokenStr, prefix, _, err := generateTokenSecret()
	if err != nil {
		return "", nil, err
	}
	// CA-220: new writes always go through the active hash function
	// (HMAC when key is set, legacy SHA-256 otherwise). generateTokenSecret
	// pre-computed a legacy hash for backward-compat callers; ignore it.
	hashStr := s.activeHash(tokenStr)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	id := fmt.Sprintf("%04x", s.nextID)
	now := time.Now()
	role := input.Role
	if role == "" {
		role = tokenRoleDefault
	}
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
		Role:        role,
		CreatedAt:   now,
		ExpiresAt:   input.ExpiresAt,
	}
	s.tokens[id] = record
	s.byHash[hashStr] = id
	return tokenStr, cloneToken(record), nil
}

func (s *MemoryAPITokenStore) ValidateToken(_ context.Context, rawToken string) (*APIToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// CA-220: try the active-format hash first, then the legacy format
	// as fallback. On a legacy hit, opportunistically rewrite the byHash
	// index + stored TokenHash to the active format so subsequent
	// lookups go through one DB query, not two.
	primary := s.activeHash(rawToken)
	id, ok := s.byHash[primary]
	migratedFromLegacy := false
	if !ok && len(s.tokenHashKey) > 0 {
		legacy := legacyHashToken(rawToken)
		id, ok = s.byHash[legacy]
		if ok {
			migratedFromLegacy = true
			delete(s.byHash, legacy)
			s.byHash[primary] = id
		}
	}
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
	if migratedFromLegacy {
		token.TokenHash = primary
	}
	return cloneToken(token), nil
}

// activeHash returns the current write-format hash. HMAC when a key is
// configured; legacy SHA-256 otherwise.
func (s *MemoryAPITokenStore) activeHash(rawToken string) string {
	return hmacHashToken(rawToken, s.tokenHashKey)
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
