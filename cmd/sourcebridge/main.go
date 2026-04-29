// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package main is the entry point for the SourceBridge binary.
package main

import "github.com/sourcebridge/sourcebridge/cli"

// version is overridden at build time via -ldflags="-X main.version=vX.Y.Z".
// The release workflow at .github/workflows/oss-release.yml already passes
// this flag; before this slice the symbol did not exist so the flag was a
// silent no-op. Local/dev builds keep the literal "dev" value.
var version = "dev"

func main() {
	cli.SetVersion(version)
	cli.Execute()
}
