// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/api/graphql"
	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/version"
)

// TestGraphQLVersionParityWithREST verifies that GraphQL Query.version
// returns the same 7 fields as REST /api/v1/version, sourced from the
// same workerVersionLookup. CA-138 added 4 fields to VersionInfo
// (goVersion/edition/buildEdition/workerVersion) to reach API parity.
//
// The parity contract:
//   - version.Version, version.Commit, version.BuildDate — identical (literal var reads)
//   - version.GoRuntime() — identical (runtime.Version() call)
//   - edition resolved from cfg.Edition — identical when both reference the same Config
//   - version.Edition (build-time) — identical (literal var read)
//   - workerVersion via the SAME cached lookup — identical across calls within TTL
//
// This is a unit test that uses the resolver's internal types directly
// rather than spinning up an HTTP+GraphQL stack; that keeps the test
// fast and focused on the parity contract. The full HTTP path is
// covered by the existing handleVersion tests + gqlgen's generated
// schema validation.
func TestGraphQLVersionParityWithREST(t *testing.T) {
	probe := func(ctx context.Context) (string, error) {
		return "worker-v0.42.0+gtestbeef", nil
	}
	lookup := newVersionLookup(30*time.Second, probe)
	cfg := &config.Config{Edition: "oss"}

	// Construct the GraphQL resolver with the same Config and lookup
	// that REST uses. WorkerVersion closure mirrors router.go's wiring.
	r := &graphql.Resolver{
		Config: cfg,
		WorkerVersion: func(ctx context.Context) string {
			return lookup.get(ctx)
		},
	}

	// REST: build the response inline (mirroring handleVersion's logic
	// without the HTTP layer).
	restBody := map[string]string{
		"version":       version.Version,
		"commit":        version.Commit,
		"buildDate":     version.BuildDate,
		"goVersion":     runtime.Version(),
		"edition":       cfg.Edition,
		"buildEdition":  version.Edition,
		"workerVersion": lookup.get(context.Background()),
	}

	// GraphQL: call the resolver.
	gqlInfo, err := r.Query().Version(context.Background())
	if err != nil {
		t.Fatalf("graphql resolver returned error: %v", err)
	}
	if gqlInfo == nil {
		t.Fatal("graphql resolver returned nil VersionInfo")
	}

	// Field-by-field comparison.
	gqlBody := map[string]string{
		"version":       gqlInfo.Version,
		"commit":        gqlInfo.Commit,
		"buildDate":     gqlInfo.BuildDate,
		"goVersion":     gqlInfo.GoVersion,
		"edition":       gqlInfo.Edition,
		"buildEdition":  gqlInfo.BuildEdition,
		"workerVersion": gqlInfo.WorkerVersion,
	}

	for field, restVal := range restBody {
		if gqlBody[field] != restVal {
			t.Errorf("parity violation on %q: REST=%q, GraphQL=%q", field, restVal, gqlBody[field])
		}
	}
}

// TestGraphQLVersionParityWithREST_NilWorker covers the fallback case:
// no workerVersionLookup wired → both surfaces return empty string for
// workerVersion. Other fields still parity-match.
func TestGraphQLVersionParityWithREST_NilWorker(t *testing.T) {
	cfg := &config.Config{Edition: "enterprise"}

	r := &graphql.Resolver{
		Config: cfg,
		WorkerVersion: func(ctx context.Context) string {
			// Mirror the rest.NewServer wiring that nil-guards the
			// lookup; in this test no lookup is wired, so the
			// closure short-circuits.
			return ""
		},
	}

	restBody := map[string]string{
		"version":       version.Version,
		"commit":        version.Commit,
		"buildDate":     version.BuildDate,
		"goVersion":     runtime.Version(),
		"edition":       "enterprise",
		"buildEdition":  version.Edition,
		"workerVersion": "",
	}

	gqlInfo, err := r.Query().Version(context.Background())
	if err != nil {
		t.Fatalf("graphql resolver returned error: %v", err)
	}

	gqlBody := map[string]string{
		"version":       gqlInfo.Version,
		"commit":        gqlInfo.Commit,
		"buildDate":     gqlInfo.BuildDate,
		"goVersion":     gqlInfo.GoVersion,
		"edition":       gqlInfo.Edition,
		"buildEdition":  gqlInfo.BuildEdition,
		"workerVersion": gqlInfo.WorkerVersion,
	}

	for field, restVal := range restBody {
		if gqlBody[field] != restVal {
			t.Errorf("parity violation on %q: REST=%q, GraphQL=%q", field, restVal, gqlBody[field])
		}
	}
}

// TestGraphQLVersionParityWithREST_NilConfig verifies the resolver's
// nil-Config nil-safety: tests that construct Resolver{} without
// wiring Config must still get edition="unknown", matching the REST
// handler when cfg.Edition is empty.
func TestGraphQLVersionParityWithREST_NilConfig(t *testing.T) {
	r := &graphql.Resolver{
		// Config: nil — tests sometimes construct Resolver this way.
		WorkerVersion: func(ctx context.Context) string { return "" },
	}

	gqlInfo, err := r.Query().Version(context.Background())
	if err != nil {
		t.Fatalf("nil-Config: graphql resolver returned error: %v", err)
	}
	if gqlInfo.Edition != "unknown" {
		t.Errorf("nil-Config: edition=%q, want %q", gqlInfo.Edition, "unknown")
	}
}
