// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package auth

import (
	"context"
	"net/http"
	"strings"

	apimiddleware "github.com/sourcebridge/sourcebridge/internal/api/middleware"
)

type contextKey string

const (
	// ClaimsKey is the context key for JWT claims.
	ClaimsKey contextKey = "claims"
	// APITokenKey stores the resolved API token/session record for the request.
	APITokenKey contextKey = "api_token"
)

// GetClaims retrieves JWT claims from request context.
func GetClaims(ctx context.Context) *Claims {
	claims, _ := ctx.Value(ClaimsKey).(*Claims)
	return claims
}

// GetAPIToken retrieves the resolved API token/session record from request context.
func GetAPIToken(ctx context.Context) *APIToken {
	token, _ := ctx.Value(APITokenKey).(*APIToken)
	return token
}

// MiddlewareWithTokens returns an HTTP middleware that validates JWT tokens
// and also accepts API tokens (ca_... prefix) when a token store is provided.
// New tokens default to RoleUser; existing tokens were backfilled to RoleAdmin
// by migration 056 so their effective role is preserved.
//
// The legacyAdminDefault flag (Security.APITokenLegacyAdminDefault in config)
// controls the fallback for tokens whose role field is still empty (e.g. a
// row inserted between schema-add and data-backfill within a single migration
// run). When false (the secure default), missing role → RoleUser. When true,
// missing role → RoleAdmin (recreates pre-SEC-2 behaviour as an operator
// escape hatch during migration).
func MiddlewareWithTokens(jwtMgr *JWTManager, tokenStore APITokenStore) func(http.Handler) http.Handler {
	return middlewareWithTokensAndFlag(jwtMgr, tokenStore, false)
}

// MiddlewareWithTokensAndLegacyAdmin is identical to MiddlewareWithTokens but
// honours the legacyAdminDefault flag from config. Use this variant when you
// have access to the server config; use MiddlewareWithTokens elsewhere (tests,
// embedded mode) where the flag is always false.
func MiddlewareWithTokensAndLegacyAdmin(jwtMgr *JWTManager, tokenStore APITokenStore, legacyAdminDefault bool) func(http.Handler) http.Handler {
	return middlewareWithTokensAndFlag(jwtMgr, tokenStore, legacyAdminDefault)
}

func middlewareWithTokensAndFlag(jwtMgr *JWTManager, tokenStore APITokenStore, legacyAdminDefault bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Try Bearer token from Authorization header
			token := extractBearerToken(r)

			// Try session cookie
			if token == "" {
				if cookie, err := r.Cookie(jwtMgr.SessionCookieName()); err == nil {
					token = cookie.Value
				}
			}

			if token == "" {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			// Check for API token (ca_ prefix)
			if strings.HasPrefix(token, "ca_") && tokenStore != nil {
				apiToken, err := tokenStore.ValidateToken(r.Context(), token)
				if err != nil || apiToken == nil {
					http.Error(w, `{"error":"invalid API token"}`, http.StatusUnauthorized)
					return
				}
				// Create claims from API token.  Role is read from the persisted
				// record (set by migration 056 for existing tokens, written at
				// creation time for new ones).  rolesFromAPIToken enforces
				// least-privilege on any empty-role edge case.
				claims := &Claims{
					UserID: apiToken.UserID,
					OrgID:  apiToken.TenantID,
					Role:   rolesFromAPIToken(apiToken, legacyAdminDefault),
				}
				ctx := context.WithValue(r.Context(), ClaimsKey, claims)
				ctx = context.WithValue(ctx, APITokenKey, apiToken)
				ctx = context.WithValue(ctx, apimiddleware.UserIDKey, claims.UserID)
				ctx = context.WithValue(ctx, apimiddleware.UserRoleKey, claims.Role)
				if claims.OrgID != "" {
					ctx = context.WithValue(ctx, apimiddleware.TenantIDKey, claims.OrgID)
				}
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			claims, err := jwtMgr.ValidateToken(token)
			if err != nil {
				msg := "invalid token"
				if strings.Contains(err.Error(), "expired") {
					msg = "session expired, please log in again"
				}
				http.Error(w, `{"error":"`+msg+`"}`, http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), ClaimsKey, claims)
			ctx = context.WithValue(ctx, apimiddleware.UserIDKey, claims.UserID)
			ctx = context.WithValue(ctx, apimiddleware.UserRoleKey, claims.Role)
			if claims.OrgID != "" {
				ctx = context.WithValue(ctx, apimiddleware.TenantIDKey, claims.OrgID)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Middleware returns an HTTP middleware that validates JWT tokens.
func Middleware(jwtMgr *JWTManager) func(http.Handler) http.Handler {
	return MiddlewareWithTokens(jwtMgr, nil)
}

// OptionalMiddleware extracts JWT claims if present but doesn't require them.
func OptionalMiddleware(jwtMgr *JWTManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearerToken(r)
			if token == "" {
				if cookie, err := r.Cookie(jwtMgr.SessionCookieName()); err == nil {
					token = cookie.Value
				}
			}

			if token != "" {
				if claims, err := jwtMgr.ValidateToken(token); err == nil {
					ctx := context.WithValue(r.Context(), ClaimsKey, claims)
					ctx = context.WithValue(ctx, apimiddleware.UserIDKey, claims.UserID)
					ctx = context.WithValue(ctx, apimiddleware.UserRoleKey, claims.Role)
					if claims.OrgID != "" {
						ctx = context.WithValue(ctx, apimiddleware.TenantIDKey, claims.OrgID)
					}
					r = r.WithContext(ctx)
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return parts[1]
}

// RequireRole returns middleware that requires the authenticated caller to carry
// the specified role. It must be applied AFTER MiddlewareWithTokens so that
// claims are already populated in the request context.
//
// Behaviour:
//   - Claims missing from context → 401 (should have been caught by
//     MiddlewareWithTokens, but we defend in depth).
//   - Claims present but role does not match → 403 with a descriptive message.
//   - Role matches → call passes through to next.
func RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := r.Context().Value(ClaimsKey).(*Claims)
			if !ok || claims == nil {
				http.Error(w, "unauthenticated", http.StatusUnauthorized)
				return
			}
			if claims.Role != role {
				http.Error(w, "forbidden: requires "+role+" role", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// rolesFromAPIToken resolves the effective role for an API token.
//
// Priority:
//  1. The token's own Role field (always set for tokens minted post-migration).
//  2. If Role is empty AND legacyAdminDefault is true → RoleAdmin (operator
//     escape hatch that recreates the pre-SEC-2 behaviour).
//  3. Otherwise → RoleUser (least privilege, the secure default).
//
// In practice branch 2/3 are only reachable during the very narrow window
// between migration 056's DEFINE FIELD and its UPDATE statement — the
// migration sets every pre-existing row to "admin" unconditionally.
func rolesFromAPIToken(t *APIToken, legacyAdminDefault bool) string {
	if t == nil {
		return RoleUser
	}
	if t.Role != "" {
		return t.Role
	}
	if legacyAdminDefault {
		return RoleAdmin
	}
	return RoleUser
}
