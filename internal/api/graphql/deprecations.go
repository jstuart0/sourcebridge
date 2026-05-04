// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/sourcebridge/sourcebridge/internal/auth"
)

var deprecatedFieldReads sync.Map

// recordDeprecatedFieldReadCtx records a deprecated-field read with tenant and
// user attribution extracted from ctx. Use this variant in resolver code where
// a context is available so that multi-tenant deployments can identify which
// caller is still using deprecated fields.
func recordDeprecatedFieldReadCtx(ctx context.Context, field string) {
	if field == "" {
		return
	}
	counterAny, _ := deprecatedFieldReads.LoadOrStore(field, &atomic.Int64{})
	counter := counterAny.(*atomic.Int64)
	counter.Add(1)

	args := []any{"event", "schema", "field", field}
	if claims := auth.GetClaims(ctx); claims != nil {
		if claims.UserID != "" {
			args = append(args, "user_id", claims.UserID)
		}
		if claims.OrgID != "" {
			args = append(args, "tenant_id", claims.OrgID)
		}
	}
	slog.Warn("deprecated_field_read", args...)
}

// recordDeprecatedFieldRead records a deprecated-field read without caller
// attribution. It is preserved as a zero-context wrapper so that any external
// tests or extensions that call this function by name continue to compile and
// behave correctly.
func recordDeprecatedFieldRead(field string) {
	recordDeprecatedFieldReadCtx(context.Background(), field)
}

// DeprecatedFieldReadsTotal returns a snapshot of per-field deprecation counters.
func DeprecatedFieldReadsTotal() map[string]int64 {
	out := map[string]int64{}
	deprecatedFieldReads.Range(func(key, value any) bool {
		name, ok := key.(string)
		if !ok {
			return true
		}
		counter, ok := value.(*atomic.Int64)
		if !ok {
			return true
		}
		out[name] = counter.Load()
		return true
	})
	return out
}
