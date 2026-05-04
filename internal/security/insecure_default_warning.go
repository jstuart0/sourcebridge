// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package security provides runtime security posture checks for the
// SourceBridge API server.
package security

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// InsecureSentinel is the value that docker-compose.hub.yml injects as the
// default for SURREAL_PASS, SOURCEBRIDGE_GRPC_SECRET, and
// SOURCEBRIDGE_JWT_SECRET when no .env file is present.  It is intentionally
// unmistakable so operators notice it immediately.
const InsecureSentinel = "INSECURE-DEFAULT-CHANGE-ME-NOW"

// CredentialCheck pairs a human-readable label with the runtime value to
// inspect.
type CredentialCheck struct {
	Label string
	Value string
}

// InsecureCredentials returns the labels of any credentials whose value equals
// the sentinel.  An empty slice means all credentials are properly configured.
func InsecureCredentials(checks []CredentialCheck) []string {
	var bad []string
	for _, c := range checks {
		if c.Value == InsecureSentinel {
			bad = append(bad, c.Label)
		}
	}
	return bad
}

// WarnInsecureDefaults checks whether any of the supplied credentials are
// still at the sentinel value.  If so it:
//  1. Emits a banner-style structured warning immediately via slog.Warn and
//     os.Stderr.
//  2. Starts a background goroutine that re-emits the warning every interval
//     until ctx is cancelled.
//
// The goroutine is a no-op when all credentials are properly set.
// WarnInsecureDefaults does not block; it returns as soon as the goroutine is
// started (or immediately if there is nothing to warn about).
func WarnInsecureDefaults(ctx context.Context, checks []CredentialCheck, interval time.Duration) {
	bad := InsecureCredentials(checks)
	if len(bad) == 0 {
		return
	}

	emitWarning(bad)

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				emitWarning(bad)
			}
		}
	}()
}

func emitWarning(bad []string) {
	msg := fmt.Sprintf(
		"INSECURE BOOT: %s still on default sentinel — replace immediately. "+
			"See README and scripts/init-hub-secrets.sh.",
		strings.Join(bad, ", "),
	)

	slog.Warn("insecure_default_credentials",
		"affected", strings.Join(bad, ","),
		"advice", "run scripts/init-hub-secrets.sh or set env vars before deploying",
	)

	// Write to stderr directly so log-level filters and structured-log routing
	// cannot mute this warning (per INFRA-1 xander M-PLAN-5 requirement).
	fmt.Fprintf(os.Stderr, "[SECURITY WARNING] %s\n", msg)
}
