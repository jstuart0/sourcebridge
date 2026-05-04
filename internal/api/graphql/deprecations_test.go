// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"sync"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/sourcebridge/sourcebridge/internal/auth"
)

// claimsContext returns a context with the given claims attached, mirroring
// the pattern used by auth middleware in production.
func claimsContext(claims *auth.Claims) context.Context {
	return context.WithValue(context.Background(), auth.ClaimsKey, claims)
}

// resetDeprecatedFieldReads clears the package-level counter map between tests
// so counter assertions are not affected by earlier test runs.
func resetDeprecatedFieldReads() {
	deprecatedFieldReads.Range(func(key, _ any) bool {
		deprecatedFieldReads.Delete(key)
		return true
	})
}

func TestRecordDeprecatedFieldReadCtx_WithClaims(t *testing.T) {
	resetDeprecatedFieldReads()

	claims := &auth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{},
		UserID:           "user-abc",
		OrgID:            "tenant-xyz",
	}
	ctx := claimsContext(claims)

	const field = "Test.withClaims"
	recordDeprecatedFieldReadCtx(ctx, field)

	totals := DeprecatedFieldReadsTotal()
	if totals[field] != 1 {
		t.Errorf("expected counter 1 for %q, got %d", field, totals[field])
	}
}

func TestRecordDeprecatedFieldReadCtx_WithoutClaims(t *testing.T) {
	resetDeprecatedFieldReads()

	// background context — no claims attached
	const field = "Test.withoutClaims"
	recordDeprecatedFieldReadCtx(context.Background(), field)

	totals := DeprecatedFieldReadsTotal()
	if totals[field] != 1 {
		t.Errorf("expected counter 1 for %q, got %d", field, totals[field])
	}
}

func TestRecordDeprecatedFieldRead_LegacyWrapper(t *testing.T) {
	resetDeprecatedFieldReads()

	// The old signature must still compile and record correctly (no-attribution path).
	const field = "Test.legacyWrapper"
	recordDeprecatedFieldRead(field)

	totals := DeprecatedFieldReadsTotal()
	if totals[field] != 1 {
		t.Errorf("expected counter 1 for %q, got %d", field, totals[field])
	}
}

func TestRecordDeprecatedFieldRead_EmptyField(t *testing.T) {
	resetDeprecatedFieldReads()

	// Empty field is a no-op; counter map must remain empty.
	recordDeprecatedFieldReadCtx(context.Background(), "")
	recordDeprecatedFieldRead("")

	totals := DeprecatedFieldReadsTotal()
	if len(totals) != 0 {
		t.Errorf("expected empty totals for empty field, got %v", totals)
	}
}

func TestRecordDeprecatedFieldReadCtx_CounterAccumulates(t *testing.T) {
	resetDeprecatedFieldReads()

	const field = "Test.accumulate"
	const n = 5
	for i := 0; i < n; i++ {
		recordDeprecatedFieldReadCtx(context.Background(), field)
	}

	totals := DeprecatedFieldReadsTotal()
	if totals[field] != n {
		t.Errorf("expected counter %d for %q, got %d", n, field, totals[field])
	}
}

func TestRecordDeprecatedFieldReadCtx_Concurrent(t *testing.T) {
	resetDeprecatedFieldReads()

	const field = "Test.concurrent"
	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			recordDeprecatedFieldReadCtx(context.Background(), field)
		}()
	}
	wg.Wait()

	totals := DeprecatedFieldReadsTotal()
	if totals[field] != goroutines {
		t.Errorf("expected counter %d for %q after concurrent writes, got %d", goroutines, field, totals[field])
	}
}
