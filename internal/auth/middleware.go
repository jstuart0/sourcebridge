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
func MiddlewareWithTokens(jwtMgr *JWTManager, tokenStore APITokenStore) func(http.Handler) http.Handler {
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
				// Create claims from API token
				claims := &Claims{
					UserID: apiToken.UserID,
					OrgID:  apiToken.TenantID,
					Role:   "admin",
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
