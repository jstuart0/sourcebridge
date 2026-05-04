// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package auth

const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// normalizeOIDCRole maps an OIDC IdP-claimed role to a server-trusted role.
// Unknown values (including empty string) fall through to RoleUser — fail-closed.
// This is the only function in the codebase that should turn an externally-supplied
// role string into a server-trusted role.
func normalizeOIDCRole(role string) string {
	switch role {
	case RoleAdmin:
		return RoleAdmin
	default:
		return RoleUser
	}
}
