// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package main is the entry point for the SourceBridge binary.
//
// The version surfaced via `sourcebridge --version` is read from the
// internal/version package, which is the single source of truth across
// the CLI flag, GraphQL Query.version, REST /api/v1/admin/status,
// REST /api/v1/version, and the telemetry ping. Values come from
// -ldflags -X github.com/sourcebridge/sourcebridge/internal/version.*
// at build time; see scripts/version.sh and the Makefile for wiring.
package main

import (
	"github.com/sourcebridge/sourcebridge/cli"
	"github.com/sourcebridge/sourcebridge/internal/version"
)

func main() {
	cli.SetVersion(version.Version)
	cli.Execute()
}
