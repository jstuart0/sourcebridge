// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"net/http"

	apimiddleware "github.com/sourcebridge/sourcebridge/internal/api/middleware"
	"github.com/sourcebridge/sourcebridge/internal/auth"
)

func currentActorIdentity(r *http.Request) (userID, tenantID string) {
	if claims := auth.GetClaims(r.Context()); claims != nil && claims.UserID != "" {
		userID = claims.UserID
	}
	if userID == "" {
		userID = apimiddleware.GetUserID(r.Context())
	}
	if userID == "" || userID == "anonymous" {
		userID = "admin"
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
