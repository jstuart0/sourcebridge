// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/gqlerror"
)

func TestScrubGraphQLError_PassesThroughGQLError(t *testing.T) {
	original := gqlerror.Errorf("custom validation message")
	got := scrubGraphQLError(context.Background(), original)
	if got == nil {
		t.Fatal("expected gqlerror passthrough, got nil")
	}
	if got.Message != "custom validation message" {
		t.Errorf("gqlerror message altered: got %q, want %q", got.Message, "custom validation message")
	}
}

func TestScrubGraphQLError_ScrubsSurrealDBError(t *testing.T) {
	original := errors.New(`surrealdb: query failed: There was a problem with the database: An error occurred: query: SELECT * FROM ca_requirement WHERE id = 'foo'`)
	got := scrubGraphQLError(context.Background(), original)
	if got == nil {
		t.Fatal("expected scrubbed error, got nil")
	}
	if got.Message != "internal error" {
		t.Errorf("expected 'internal error', got %q", got.Message)
	}
	if strings.Contains(got.Message, "surrealdb") || strings.Contains(got.Message, "ca_requirement") {
		t.Errorf("scrubbed message still contains storage details: %q", got.Message)
	}
	if got.Extensions["correlation_id"] == "" {
		t.Error("scrubbed error missing correlation_id extension")
	}
	if got.Extensions["code"] != "INTERNAL" {
		t.Errorf("scrubbed error missing code=INTERNAL extension; got %v", got.Extensions["code"])
	}
}

func TestScrubGraphQLError_ScrubsGRPCError(t *testing.T) {
	original := errors.New("rpc error: code = DeadlineExceeded desc = context deadline exceeded")
	got := scrubGraphQLError(context.Background(), original)
	if got == nil {
		t.Fatal("expected scrubbed error, got nil")
	}
	if got.Message != "internal error" {
		t.Errorf("expected gRPC transport error scrubbed; got %q", got.Message)
	}
}

func TestScrubGraphQLError_PassesThroughCleanError(t *testing.T) {
	original := errors.New("repository foo not found")
	got := scrubGraphQLError(context.Background(), original)
	if got == nil {
		t.Fatal("expected wrapped error, got nil")
	}
	if got.Message != "repository foo not found" {
		t.Errorf("clean error altered: got %q, want %q", got.Message, "repository foo not found")
	}
	if got.Extensions["correlation_id"] != nil {
		t.Error("clean error should not carry correlation_id (it's not scrubbed)")
	}
}

func TestScrubGraphQLError_NilReturnsNil(t *testing.T) {
	got := scrubGraphQLError(context.Background(), nil)
	if got != nil {
		t.Errorf("expected nil for nil input, got %+v", got)
	}
}

func TestLooksLikeStorageLeak_TableNameRegex(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"surrealdb query failed", true},
		{"surreal db returned null", true},
		{"thing(ca_requirement:foo)", true},
		{"query failed: constraint violation", true},
		{"failed to scan ca_local_auth record", true},
		{"rpc error: code = Unavailable", true},
		{"rocksdb: CompactionFiltered", true},
		{"cbor: cannot decode", true},
		{"BEGIN TRANSACTION failed: deadlock", true},
		{"db: read error at offset 42", true},
		{"repository not found", false},
		{"invalid input", false},
		{"requirement title required", false},
	}
	for _, c := range cases {
		if got := looksLikeStorageLeak(strings.ToLower(c.msg)); got != c.want {
			t.Errorf("looksLikeStorageLeak(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

// TestStorageLeakMarkers_CountCanary pins the length of storageLeakMarkers
// (CA-394 / T-M3). If this test fails, a maintainer added or removed a marker.
// Intent must be signalled by updating this count AND adding or removing the
// corresponding test case in TestLooksLikeStorageLeak_TableNameRegex above.
//
// Rule from CLAUDE.md: "a new storage layer (Cassandra, etc.) needs a new
// marker added to storageLeakMarkers."
func TestStorageLeakMarkers_CountCanary(t *testing.T) {
	const expected = 9
	if got := len(storageLeakMarkers); got != expected {
		t.Fatalf(
			"storageLeakMarkers count changed from %d to %d — "+
				"if intentional, update this canary AND add/remove the "+
				"matching test case in TestLooksLikeStorageLeak_TableNameRegex",
			expected, got,
		)
	}
}
