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

// InsecureSentinel is the value that docker-compose.hub.yml historically
// injected as the default for SURREAL_PASS, SOURCEBRIDGE_GRPC_SECRET, and
// SOURCEBRIDGE_JWT_SECRET when no .env file was present.  It is intentionally
// unmistakable so operators notice it immediately.  CA-311 (2026-05-08)
// added the publicly-known 64-hex JWT placeholder and several historical
// dev-default literals to the recognized set; see KnownInsecureSentinels.
const InsecureSentinel = "INSECURE-DEFAULT-CHANGE-ME-NOW"

// KnownInsecureSentinels is the set of credential values that have ever been
// shipped as docker-compose / OSS quickstart defaults.  Any of them appearing
// at runtime indicates an operator who didn't override the default — warn
// loudly until they do.  The 64-hex placeholder is syntactically valid (it
// passes the ≥32-byte JWT gate added in CA-311) but is publicly known and
// therefore weaker than a per-deployment random secret.
var KnownInsecureSentinels = map[string]struct{}{
	InsecureSentinel:                  {},
	"dev-secret-change-in-production": {}, // CA-311: pre-r2 JWT fallback
	"dev-jwt-secret-change-me":        {}, // CA-311: docker-compose.yml dev default (pre-r2)
	"dev-shared-secret":               {}, // historical gRPC dev default
	// CA-311: 64-hex publicly-known JWT placeholder shipped in docker-compose
	// after the r2 length-gate fix. Syntactically valid but weak.
	"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef": {},
	// CA-219 (X-L4): SurrealDB ships "root" as its default root user
	// password. Embedded mode never uses it; external mode does, and an
	// operator who left it at the default is exposing their DB. The
	// caller in cli/serve.go gates the SURREAL_PASS check on external
	// mode so embedded-mode operators don't see a noisy warning about
	// a meaningless credential.
	"root": {},
}

// CredentialCheck pairs a human-readable label with the runtime value to
// inspect.
type CredentialCheck struct {
	Label string
	Value string
}

// InsecureCredentials returns the labels of any credentials whose value
// matches a known insecure default sentinel. An empty slice means all
// credentials are properly configured (or non-empty but unrecognized as a
// shipped default).
//
// Note (CA-311 / codex r2 Low): length-based weakness for JWT secrets
// specifically is enforced HARDER by config.Validate() (which rejects
// secrets shorter than 32 bytes at startup, blocking the server from
// booting). This helper is for runtime warning of recognizable shipped
// defaults that pass the length gate but are publicly known. A short
// non-sentinel secret reaches Validate() before reaching this helper, so
// length-based warning here would be redundant for the JWT-secret path
// and mis-targeted for the other credentials this helper inspects (gRPC
// secret, surreal pass) which have no separate length contract.
func InsecureCredentials(checks []CredentialCheck) []string {
	var bad []string
	for _, c := range checks {
		if c.Value == "" {
			continue
		}
		if _, hit := KnownInsecureSentinels[c.Value]; hit {
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
