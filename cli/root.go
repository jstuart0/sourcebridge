// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "sourcebridge",
	Short: "Understand your codebase. Connect requirements to code.",
	Long: `SourceBridge.ai is a requirement-aware code comprehension platform.

It connects Requirements → Design Intent → Code → Tests → Review → Architecture,
providing bidirectional traceability, multi-level code comprehension, structured
reviews, and architecture awareness.`,
}

// SetVersion populates the cobra Version field so `sourcebridge --version`
// works. Called from cmd/sourcebridge/main.go with the value of
// internal/version.Version, which is the single source of truth across the
// CLI flag, GraphQL Query.version, REST /api/v1/admin/status, REST
// /api/v1/version, and the telemetry ping. The value is overridden at
// build time via:
//
//	-ldflags "-X github.com/sourcebridge/sourcebridge/internal/version.Version=vX.Y.Z"
//
// See scripts/version.sh and the Makefile for the build-time wiring that
// computes and injects this value.
//
// We use SetVersionTemplate to format as "sourcebridge version <X>" so the
// installer's `awk '{print $NF}'` upgrade-detection works without parsing
// JSON.
func SetVersion(v string) {
	rootCmd.Version = v
	rootCmd.SetVersionTemplate("sourcebridge version {{.Version}}\n")
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(indexCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(reviewImplCmd)
	rootCmd.AddCommand(traceReqCmd)
	rootCmd.AddCommand(askImplCmd)
	rootCmd.AddCommand(importCmd)
	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(mcpProxyCmd)
}
