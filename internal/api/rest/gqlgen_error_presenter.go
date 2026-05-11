// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"

	"github.com/99designs/gqlgen/graphql"
	"github.com/google/uuid"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// scrubGraphQLError replaces low-level storage error strings reaching the
// browser with a sanitized, correlation-ID-tagged message. The full error is
// logged server-side at WARN with the correlation ID so operators can trace
// the report back to the source.
//
// Motivation (xander Phase 2 mid-build review on CA-320): the default
// gqlgen presenter passes resolver errors verbatim into the response's
// `errors[]` array. Several resolver paths (StoreRequirement, knowledge
// generation, etc.) wrap raw SurrealDB errors with %w. Those errors
// reference table names (`ca_requirement`, `ca_knowledge_artifact`),
// column constraints, and SurrealDB version hints — schema-shape leaks
// to any authenticated GraphQL caller who triggers a constraint violation.
//
// This is INFORMATION disclosure, not credential disclosure, so it lands
// Medium. Pre-existing — not introduced by CA-320 or its follow-ups —
// but the audit retro flagged it explicitly and the right place to fix
// it is at the GraphQL boundary, not site-by-site in every resolver.
//
// Errors that are already typed as *gqlerror.Error pass through unchanged
// (resolvers can still return user-facing validation messages via
// gqlerror.WrapPath / gqlerror.Errorf). Sentinel errors that have a clean
// public-safe message also pass through. Only opaque wrapped errors get
// scrubbed.
func scrubGraphQLError(ctx context.Context, err error) *gqlerror.Error {
	if err == nil {
		return nil
	}
	// Pass through gqlerror.Error verbatim — those are intentionally
	// user-facing (resolver validation messages, custom error codes).
	var gErr *gqlerror.Error
	if errors.As(err, &gErr) {
		return gErr
	}

	msg := err.Error()
	if !looksLikeStorageLeak(msg) {
		// Non-storage errors: wrap with path so the response still cites
		// the failing field. Same shape gqlgen's DefaultErrorPresenter
		// returns for non-gqlerror errors.
		return gqlerror.WrapPath(graphql.GetPath(ctx), err)
	}

	// Storage-leak path: log the full error server-side with a correlation
	// ID, return a sanitized message to the client.
	corrID := uuid.NewString()
	slog.WarnContext(ctx, "graphql resolver leaked storage error; scrubbed before return",
		"correlation_id", corrID,
		"path", graphql.GetPath(ctx).String(),
		"error", msg,
	)
	scrubbed := gqlerror.WrapPath(graphql.GetPath(ctx), errors.New("internal error"))
	if scrubbed.Extensions == nil {
		scrubbed.Extensions = map[string]any{}
	}
	scrubbed.Extensions["correlation_id"] = corrID
	scrubbed.Extensions["code"] = "INTERNAL"
	return scrubbed
}

// looksLikeStorageLeak returns true when the error message contains
// substrings that almost certainly originated from a low-level storage
// layer and should never reach a browser. The matcher is intentionally
// over-inclusive — false positives mean "internal error" instead of a
// (potentially noisier) error string, false negatives let schema details
// leak. The list below is built from the actual patterns observed in
// SurrealDB error strings + gRPC transport errors during CA-320 E2E.
//
// This is content-shape detection. A resolver that wants a specific
// public-facing error message must return a *gqlerror.Error explicitly;
// that branch above passes through untouched.
func looksLikeStorageLeak(msg string) bool {
	if msg == "" {
		return false
	}
	lower := strings.ToLower(msg)
	for _, marker := range storageLeakMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return storageTableNameRe.MatchString(lower)
}

// storageLeakMarkers are substrings that, if present in an error string,
// indicate the error originated from a low-level layer that should not
// surface to authenticated GraphQL clients verbatim. Keep this list
// conservative — adding a new marker silently scrubs more errors.
var storageLeakMarkers = []string{
	"surrealdb",       // SDK error strings
	"surreal db",      // alternate render
	"thing(",          // SurrealDB record-id thing(...) syntax
	"query failed",    // SDK query wrapper
	"transaction",     // tx-related leaks
	"db: ",            // db-package wrappers
	"rpc error: code", // gRPC transport
	"rocksdb",         // SurrealDB storage layer
	"cbor",            // codec details
}

// storageTableNameRe catches references to ca_-prefixed tables which
// are exclusively internal storage names (per migration 001 onwards).
// A resolver intentionally exposing a table name in a user-facing
// message should not have a ca_ prefix; user-visible labels use
// human-readable names.
var storageTableNameRe = regexp.MustCompile(`\bca_[a-z_]+\b`)
