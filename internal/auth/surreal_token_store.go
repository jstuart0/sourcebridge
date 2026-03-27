// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package auth

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/surrealdb/surrealdb.go"
	"github.com/surrealdb/surrealdb.go/pkg/models"
)

type tokenDBProvider interface {
	DB() *surrealdb.DB
}

type SurrealAPITokenStore struct {
	dbp            tokenDBProvider
	mu             sync.Mutex
	lastUsedWrites map[string]time.Time
}

type surrealAPIToken struct {
	ID          *models.RecordID `json:"id,omitempty"`
	Name        string           `json:"name"`
	Prefix      string           `json:"prefix"`
	TokenHash   string           `json:"token_hash"`
	UserID      string           `json:"user_id"`
	TenantID    string           `json:"tenant_id,omitempty"`
	TokenKind   string           `json:"token_kind"`
	ClientType  string           `json:"client_type,omitempty"`
	AuthMethod  string           `json:"auth_method,omitempty"`
	DeviceLabel string           `json:"device_label,omitempty"`
	Metadata    string           `json:"metadata,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
	ExpiresAt   *time.Time       `json:"expires_at,omitempty"`
	LastUsedAt  *time.Time       `json:"last_used_at,omitempty"`
	RevokedAt   *time.Time       `json:"revoked_at,omitempty"`
}

func NewSurrealAPITokenStore(dbp tokenDBProvider) *SurrealAPITokenStore {
	return &SurrealAPITokenStore{
		dbp:            dbp,
		lastUsedWrites: make(map[string]time.Time),
	}
}

func (s *SurrealAPITokenStore) CreateToken(ctx context.Context, input CreateTokenInput) (string, *APIToken, error) {
	db := s.dbp.DB()
	if db == nil {
		return "", nil, fmt.Errorf("database not connected")
	}
	tokenStr, prefix, hashStr, err := generateTokenSecret()
	if err != nil {
		return "", nil, err
	}
	id, err := generateID()
	if err != nil {
		return "", nil, err
	}
	now := time.Now()

	// SurrealDB SCHEMAFULL tables reject Go nil (serialised as JSON null)
	// for option<T> fields — they require the SurrealQL literal NONE.
	// Only include optional fields as parameters when they have real values;
	// otherwise substitute NONE directly in the query string.
	params := map[string]any{
		"id":         id,
		"name":       input.Name,
		"prefix":     prefix,
		"token_hash": hashStr,
		"token_kind": string(normalizeTokenKind(input.Kind)),
		"created_at": now,
	}
	setParam := func(key, value string) string {
		if value == "" {
			return key + ": NONE"
		}
		params[key] = value
		return key + ": $" + key
	}
	userClause := "user_id: $user_id"
	params["user_id"] = input.UserID
	tenantClause := setParam("tenant_id", input.TenantID)
	clientClause := setParam("client_type", input.ClientType)
	authClause := setParam("auth_method", string(input.AuthMethod))
	deviceClause := setParam("device_label", input.DeviceLabel)
	metadataClause := setParam("metadata", input.Metadata)
	expiresClause := "expires_at: NONE"
	if input.ExpiresAt != nil {
		expiresClause = "expires_at: $expires_at"
		params["expires_at"] = *input.ExpiresAt
	}

	_, err = surrealdb.Query[interface{}](ctx, db, `
		CREATE type::thing('ca_api_token', $id) CONTENT {
			name: $name,
			prefix: $prefix,
			token_hash: $token_hash,
			`+userClause+`,
			`+tenantClause+`,
			token_kind: $token_kind,
			`+clientClause+`,
			`+authClause+`,
			`+deviceClause+`,
			`+metadataClause+`,
			created_at: $created_at,
			`+expiresClause+`,
			last_used_at: NONE,
			revoked_at: NONE
		}
	`, params)
	if err != nil {
		return "", nil, fmt.Errorf("persisting token: %w", err)
	}

	return tokenStr, &APIToken{
		ID:          id,
		Name:        input.Name,
		Prefix:      prefix,
		UserID:      input.UserID,
		TenantID:    input.TenantID,
		Kind:        normalizeTokenKind(input.Kind),
		ClientType:  input.ClientType,
		AuthMethod:  input.AuthMethod,
		DeviceLabel: input.DeviceLabel,
		Metadata:    input.Metadata,
		CreatedAt:   now,
		ExpiresAt:   input.ExpiresAt,
	}, nil
}

func (s *SurrealAPITokenStore) ValidateToken(ctx context.Context, rawToken string) (*APIToken, error) {
	db := s.dbp.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	hashStr := hashToken(rawToken)
	raw, err := surrealdb.Query[[]surrealAPIToken](ctx, db, `
		SELECT * FROM ca_api_token
		WHERE token_hash = $token_hash AND revoked_at = NONE
		LIMIT 1
	`, map[string]any{"token_hash": hashStr})
	if err != nil {
		return nil, fmt.Errorf("validating token: %w", err)
	}
	if raw == nil || len(*raw) == 0 || (*raw)[0].Error != nil || len((*raw)[0].Result) == 0 {
		return nil, nil
	}
	token := tokenFromSurreal((*raw)[0].Result[0])
	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) {
		return nil, nil
	}
	s.maybeTouchLastUsed(token.ID)
	return token, nil
}

func (s *SurrealAPITokenStore) ListTokens(ctx context.Context) ([]*APIToken, error) {
	db := s.dbp.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	raw, err := surrealdb.Query[[]surrealAPIToken](ctx, db, `SELECT * FROM ca_api_token ORDER BY created_at DESC`, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("listing tokens: %w", err)
	}
	if raw == nil || len(*raw) == 0 || (*raw)[0].Error != nil {
		return nil, nil
	}
	result := make([]*APIToken, 0, len((*raw)[0].Result))
	for _, record := range (*raw)[0].Result {
		result = append(result, tokenFromSurreal(record))
	}
	return result, nil
}

func (s *SurrealAPITokenStore) RevokeToken(ctx context.Context, id string) (bool, error) {
	db := s.dbp.DB()
	if db == nil {
		return false, fmt.Errorf("database not connected")
	}
	now := time.Now()
	raw, err := surrealdb.Query[[]map[string]any](ctx, db, `
		UPDATE type::thing('ca_api_token', $id)
		SET revoked_at = $revoked_at
		WHERE revoked_at = NONE
		RETURN AFTER
	`, map[string]any{
		"id":         id,
		"revoked_at": now,
	})
	if err != nil {
		return false, fmt.Errorf("revoking token: %w", err)
	}
	if raw == nil || len(*raw) == 0 || (*raw)[0].Error != nil || len((*raw)[0].Result) == 0 {
		return false, nil
	}
	return true, nil
}

func (s *SurrealAPITokenStore) maybeTouchLastUsed(id string) {
	now := time.Now()
	s.mu.Lock()
	last, ok := s.lastUsedWrites[id]
	if ok && now.Sub(last) < time.Minute {
		s.mu.Unlock()
		return
	}
	s.lastUsedWrites[id] = now
	s.mu.Unlock()

	go func(ts time.Time) {
		db := s.dbp.DB()
		if db == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := surrealdb.Query[interface{}](ctx, db, `
			UPDATE type::thing('ca_api_token', $id) SET last_used_at = $last_used_at
		`, map[string]any{
			"id":           id,
			"last_used_at": ts,
		})
		if err != nil {
			slog.Debug("failed to update token last_used_at", "token_id", id, "error", err)
		}
	}(now)
}

func (s *SurrealAPITokenStore) Cleanup(ctx context.Context) (int, error) {
	db := s.dbp.DB()
	if db == nil {
		return 0, fmt.Errorf("database not connected")
	}
	now := time.Now()
	raw, err := surrealdb.Query[[]map[string]any](ctx, db, `
		DELETE ca_api_token
		WHERE (revoked_at != NONE AND revoked_at < $revoked_cutoff)
		   OR (expires_at != NONE AND expires_at < $expired_cutoff)
		RETURN BEFORE
	`, map[string]any{
		"revoked_cutoff": now.Add(-30 * 24 * time.Hour),
		"expired_cutoff": now.Add(-7 * 24 * time.Hour),
	})
	if err != nil {
		return 0, fmt.Errorf("cleaning api tokens: %w", err)
	}
	if raw == nil || len(*raw) == 0 || (*raw)[0].Error != nil {
		return 0, nil
	}
	return len((*raw)[0].Result), nil
}

func tokenFromSurreal(record surrealAPIToken) *APIToken {
	id := ""
	if record.ID != nil {
		id = fmt.Sprint(record.ID.ID)
	}
	return &APIToken{
		ID:          id,
		Name:        record.Name,
		Prefix:      record.Prefix,
		UserID:      record.UserID,
		TenantID:    record.TenantID,
		Kind:        TokenKind(record.TokenKind),
		ClientType:  record.ClientType,
		AuthMethod:  AuthMethod(record.AuthMethod),
		DeviceLabel: record.DeviceLabel,
		Metadata:    record.Metadata,
		CreatedAt:   record.CreatedAt,
		ExpiresAt:   record.ExpiresAt,
		LastUsedAt:  record.LastUsedAt,
		RevokedAt:   record.RevokedAt,
	}
}
