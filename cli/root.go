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
}
