// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package main is the GIT_ASKPASS helper binary for SourceBridge.
//
// Git invokes $GIT_ASKPASS once for the username prompt and once for the
// password prompt. The helper reads the token from SOURCEBRIDGE_GIT_TOKEN
// (set in cmd.Env, never on a shell command line) and responds to the
// password prompt with "password=<token>".
//
// The username prompt ("Username for ...") is answered with an empty line —
// for token-based HTTPS auth the username is ignored by the remote.
//
// Security properties:
//   - The token is passed via an environment variable, not via a shell
//     command line. No shell quoting or string interpolation occurs at any
//     layer, making shell-injection (e.g. via a token containing '; rm -rf')
//     structurally impossible: the token is never handed to a shell.
//   - The binary is built alongside cmd/sourcebridge and shipped in the
//     same container image (see deploy/docker/Dockerfile.sourcebridge).
package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	// Git passes the prompt text as the first argument.
	// "Username" prompts receive an empty reply; password prompts receive
	// the token. Any other prompt also receives an empty reply (safe default).
	prompt := ""
	if len(os.Args) > 1 {
		prompt = strings.ToLower(os.Args[1])
	}

	if strings.Contains(prompt, "password") {
		token := os.Getenv("SOURCEBRIDGE_GIT_TOKEN")
		fmt.Println(token)
	} else {
		// Username prompt: empty string is correct for token-based auth.
		fmt.Println("")
	}
}
