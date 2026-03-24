// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package middleware

import (
	"context"
	"net/http"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

type storeCtxKey struct{}

// RepoAccessChecker resolves which repositories a tenant may access.
type RepoAccessChecker interface {
	GetTenantRepos(tenantID string) ([]string, error)
}

// StoreFromContext returns a per-request tenant-filtered GraphStore if one
// was injected by RepoAccessMiddleware. Returns nil when running without
// tenant filtering (OSS single-tenant mode).
func StoreFromContext(ctx context.Context) graph.GraphStore {
	if s, ok := ctx.Value(storeCtxKey{}).(graph.GraphStore); ok {
		return s
	}
	return nil
}

// RepoAccessMiddleware reads the tenant ID from context (set by TenantMiddleware),
// looks up the tenant's allowed repositories via the checker, wraps the base
// store in a TenantFilteredStore, and injects it into the request context.
// Downstream handlers and GraphQL resolvers can retrieve it via StoreFromContext.
func RepoAccessMiddleware(baseStore graph.GraphStore, checker RepoAccessChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenantID := GetTenantID(r.Context())

			// Skip filtering for the default single-tenant
			if tenantID == "default" {
				next.ServeHTTP(w, r)
				return
			}

			repoIDs, err := checker.GetTenantRepos(tenantID)
			if err != nil {
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}

			filtered := graph.NewTenantFilteredStore(baseStore, repoIDs)
			ctx := context.WithValue(r.Context(), storeCtxKey{}, filtered)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
