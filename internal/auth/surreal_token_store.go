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
	// CA-220: HMAC key for the new token-hash format. Empty = legacy
	// bare-SHA-256 only (backward-compat for installs with no encryption
	// key configured). When non-empty, new writes use HMAC and validation
	// transparently accepts both formats with opportunistic legacy →
	// HMAC migration.
	tokenHashKey []byte
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
	// Role is written on every new token (migration 056 backfills existing rows
	// to "admin" to preserve pre-SEC-2 behaviour). Reads may return empty string
	// only during the brief window between the schema-add and data-backfill
	// within a single migration apply; tokenFromSurreal normalises that to
	// tokenRoleDefault.
	Role       string     `json:"role,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

func NewSurrealAPITokenStore(dbp tokenDBProvider) *SurrealAPITokenStore {
	return &SurrealAPITokenStore{
		dbp:            dbp,
		lastUsedWrites: make(map[string]time.Time),
	}
}

// NewSurrealAPITokenStoreWithKey is the production constructor: configures
// the HMAC key for the new token-hash format (CA-220). Pass nil/empty to
// preserve legacy SHA-256-only behavior — useful for OSS installs that
// don't have an encryption key set yet.
func NewSurrealAPITokenStoreWithKey(dbp tokenDBProvider, key []byte) *SurrealAPITokenStore {
	return &SurrealAPITokenStore{
		dbp:            dbp,
		lastUsedWrites: make(map[string]time.Time),
		tokenHashKey:   append([]byte(nil), key...),
	}
}

// activeHash returns the current write-format hash. HMAC when a key is
// configured; legacy SHA-256 otherwise.
func (s *SurrealAPITokenStore) activeHash(rawToken string) string {
	return hmacHashToken(rawToken, s.tokenHashKey)
}

func (s *SurrealAPITokenStore) CreateToken(ctx context.Context, input CreateTokenInput) (string, *APIToken, error) {
	db := s.dbp.DB()
	if db == nil {
		return "", nil, fmt.Errorf("database not connected")
	}
	tokenStr, prefix, err := generateTokenSecret()
	if err != nil {
		return "", nil, err
	}
	// CA-220: new writes always go through the active hash function
	// (HMAC when key is set, legacy SHA-256 otherwise).
	hashStr := s.activeHash(tokenStr)
	id, err := generateID()
	if err != nil {
		return "", nil, err
	}
	now := time.Now()

	// SurrealDB SCHEMAFULL tables reject Go nil (serialised as JSON null)
	// for option<T> fields — they require the SurrealQL literal NONE.
	// Only include optional fields as parameters when they have real values;
	// otherwise substitute NONE directly in the query string.
	role := input.Role
	if role == "" {
		role = tokenRoleDefault
	}
	params := map[string]any{
		"id":         id,
		"name":       input.Name,
		"prefix":     prefix,
		"token_hash": hashStr,
		"token_kind": string(normalizeTokenKind(input.Kind)),
		"role":       role,
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
			role: $role,
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
		Role:        role,
		CreatedAt:   now,
		ExpiresAt:   input.ExpiresAt,
	}, nil
}

func (s *SurrealAPITokenStore) ValidateToken(ctx context.Context, rawToken string) (*APIToken, error) {
	db := s.dbp.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	// CA-220: try the active-format hash first. On miss (and only when
	// HMAC is in use), retry with the legacy SHA-256 format and
	// opportunistically migrate the row to HMAC on a hit.
	activeHash := s.activeHash(rawToken)
	token, found, err := s.lookupTokenByHash(ctx, db, activeHash)
	if err != nil {
		return nil, err
	}
	if !found && len(s.tokenHashKey) > 0 {
		legacyHash := legacyHashToken(rawToken)
		token, found, err = s.lookupTokenByHash(ctx, db, legacyHash)
		if err != nil {
			return nil, err
		}
		if found {
			// Async row migration: bump token_hash from legacy → HMAC.
			// Errors are logged but never fail validation. The row still
			// validates correctly via legacy on subsequent calls until
			// migration succeeds.
			go s.migrateTokenHash(token.ID, legacyHash, activeHash)
		}
	}
	if !found {
		return nil, nil
	}
	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) {
		return nil, nil
	}
	s.maybeTouchLastUsed(token.ID)
	return token, nil
}

// lookupTokenByHash queries ca_api_token for a non-revoked row whose
// token_hash matches the supplied hash. Returns (token, true, nil) on
// hit, (nil, false, nil) on miss, (nil, false, err) on transport error.
func (s *SurrealAPITokenStore) lookupTokenByHash(ctx context.Context, db *surrealdb.DB, hash string) (*APIToken, bool, error) {
	raw, err := surrealdb.Query[[]surrealAPIToken](ctx, db, `
		SELECT * FROM ca_api_token
		WHERE token_hash = $token_hash AND revoked_at = NONE
		LIMIT 1
	`, map[string]any{"token_hash": hash})
	if err != nil {
		return nil, false, fmt.Errorf("validating token: %w", err)
	}
	if raw == nil || len(*raw) == 0 || (*raw)[0].Error != nil || len((*raw)[0].Result) == 0 {
		return nil, false, nil
	}
	return tokenFromSurreal((*raw)[0].Result[0]), true, nil
}

// migrateTokenHash rewrites a token row's token_hash from legacy
// SHA-256 to HMAC-SHA256. Runs in a background goroutine so the
// validation hot-path stays single-round-trip on the next call.
// Failures are logged and silently dropped — the row remains
// validatable via the legacy fallback until migration succeeds.
func (s *SurrealAPITokenStore) migrateTokenHash(id, legacyHash, newHash string) {
	db := s.dbp.DB()
	if db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := surrealdb.Query[interface{}](ctx, db, `
		UPDATE type::thing('ca_api_token', $id)
		SET token_hash = $new_hash
		WHERE token_hash = $legacy_hash
	`, map[string]any{
		"id":          id,
		"new_hash":    newHash,
		"legacy_hash": legacyHash,
	})
	if err != nil {
		slog.Warn("ca_api_token hash migration failed; row remains on legacy SHA-256 until next validation",
			"id", id, "err", err)
		return
	}
	slog.Info("ca_api_token hash migrated legacy SHA-256 → HMAC-SHA256", "id", id)
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
	// Preserve the raw DB role exactly as stored. Empty-role tokens
	// (pre-migration 056 rows that were not yet backfilled) must arrive at
	// rolesFromAPIToken with Role == "" so the legacyAdminDefault flag can
	// fire its operator escape-hatch path. If we normalised to tokenRoleDefault
	// here, the flag would never see an empty role and the SEC-2 rollback
	// mechanism would be silently broken for Surreal-backed tokens.
	// rolesFromAPIToken is the single place that converts "" → "user" or "admin".
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
		Role:        record.Role, // raw — may be "" for pre-migration rows
		CreatedAt:   record.CreatedAt,
		ExpiresAt:   record.ExpiresAt,
		LastUsedAt:  record.LastUsedAt,
		RevokedAt:   record.RevokedAt,
	}
}
