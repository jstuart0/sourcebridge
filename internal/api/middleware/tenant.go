package middleware

import (
	"context"
	"net/http"
)

type contextKey string

const (
	TenantIDKey contextKey = "tenant_id"
	UserIDKey   contextKey = "user_id"
	UserRoleKey contextKey = "user_role"
)

// TenantExtractor is the OSS extension point for multi-tenant context.
// The enterprise edition implements this to extract tenant from JWT claims.
type TenantExtractor interface {
	ExtractTenant(r *http.Request) (tenantID string, userID string, role string, err error)
}

// DefaultTenantExtractor is the OSS default — single tenant, no extraction needed.
type DefaultTenantExtractor struct{}

func (d *DefaultTenantExtractor) ExtractTenant(r *http.Request) (string, string, string, error) {
	return "default", "anonymous", "owner", nil
}

// TenantMiddleware injects tenant context from the configured extractor.
func TenantMiddleware(extractor TenantExtractor) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenantID, userID, role, err := extractor.ExtractTenant(r)
			if err != nil {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), TenantIDKey, tenantID)
			ctx = context.WithValue(ctx, UserIDKey, userID)
			ctx = context.WithValue(ctx, UserRoleKey, role)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetTenantID retrieves tenant ID from context.
func GetTenantID(ctx context.Context) string {
	if v, ok := ctx.Value(TenantIDKey).(string); ok {
		return v
	}
	return "default"
}

// GetUserID retrieves user ID from context.
func GetUserID(ctx context.Context) string {
	if v, ok := ctx.Value(UserIDKey).(string); ok {
		return v
	}
	return "anonymous"
}

// GetUserRole retrieves user role from context.
func GetUserRole(ctx context.Context) string {
	if v, ok := ctx.Value(UserRoleKey).(string); ok {
		return v
	}
	return "viewer"
}
