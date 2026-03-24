//go:build !enterprise

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import "github.com/go-chi/chi/v5"

// registerEnterpriseRoutes is a no-op in OSS builds.
func (s *Server) registerEnterpriseRoutes(r chi.Router) {}
