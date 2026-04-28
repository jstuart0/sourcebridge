// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package rest re-exports health.Checker as HealthChecker so router.go and
// health.go can use a single short name without repeating the import alias
// everywhere. All logic lives in internal/health.
package rest

import "github.com/sourcebridge/sourcebridge/internal/health"

// HealthChecker is an alias for health.Checker. Exposed here so the Server
// options and handler code can reference it without a separate import alias.
type HealthChecker = health.Checker
