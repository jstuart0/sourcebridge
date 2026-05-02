// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package version is the single source of truth for the running binary's
// version, commit, build date, and edition. The exported variables are
// overwritten at link time via -ldflags; the helper script that computes
// the values lives at scripts/version.sh and is consumed by the Makefile,
// every Dockerfile, and the GitHub Actions workflows.
//
// Build-time wiring example (from the Makefile):
//
//	-ldflags "-X github.com/sourcebridge/sourcebridge/internal/version.Version=v1.2.3 \
//	          -X github.com/sourcebridge/sourcebridge/internal/version.Commit=abc123 \
//	          -X github.com/sourcebridge/sourcebridge/internal/version.BuildDate=2026-05-01T00:00:00Z \
//	          -X github.com/sourcebridge/sourcebridge/internal/version.Edition=oss"
//
// When the binary is built without ldflags (rare; only happens via raw
// `go build` or `go run`), the defaults below identify it as a dev build.
package version

import "runtime"

var (
	// Version is the canonical SourceBridge version string. Format follows
	// scripts/version.sh — a SemVer-2 string with a prerelease component
	// (-dev.N, -pr<N>, -local) and a build-metadata component (+g<sha>[.dirty]).
	Version = "dev"

	// Commit is the full git SHA the binary was built from.
	Commit = "unknown"

	// BuildDate is the UTC timestamp of the build, RFC 3339 format.
	BuildDate = "unknown"

	// Edition is the build flavor — "oss" or "enterprise". The runtime
	// configured edition (cfg.Edition) is the source of truth for users;
	// this value is informational and surfaces as `buildEdition` in
	// /api/v1/version so misconfigurations are visible.
	Edition = "unknown"
)

// GoRuntime returns the Go runtime version of the running binary. It is
// kept as a function (not a var) so it cannot be ldflag-mutated to lie
// about the toolchain.
func GoRuntime() string {
	return runtime.Version()
}
