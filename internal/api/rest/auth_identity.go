// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"log/slog"
	"net/http"

	apimiddleware "github.com/sourcebridge/sourcebridge/internal/api/middleware"
	"github.com/sourcebridge/sourcebridge/internal/auth"
)

// currentActorIdentity returns the user-ID and tenant-ID for the caller.
//
// CA-342: the previous fallback to the literal string "admin" for
// unauthenticated callers was removed. An empty userID is now returned
// instead, and a WARN is logged. All call sites are on authenticated
// routes; the empty-string case signals a missing auth-context bug, not
// a legitimate anonymous request.
func currentActorIdentity(r *http.Request) (userID, tenantID string) {
	if claims := auth.GetClaims(r.Context()); claims != nil && claims.UserID != "" {
		userID = claims.UserID
	}
	if userID == "" {
		userID = apimiddleware.GetUserID(r.Context())
	}
	if userID == "anonymous" {
		userID = ""
	}
	if userID == "" {
		slog.WarnContext(r.Context(), "currentActorIdentity: no authenticated user in request context — audit log entry will have empty actor",
			"path", r.URL.Path,
			"method", r.Method,
		)
	}

	tenantID = apimiddleware.GetTenantID(r.Context())
	if tenantID == "default" || tenantID == "" {
		if claims := auth.GetClaims(r.Context()); claims != nil && claims.OrgID != "" {
			tenantID = claims.OrgID
		}
	}
	if tenantID == "default" {
		tenantID = ""
	}
	return userID, tenantID
}
