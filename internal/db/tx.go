// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"fmt"
	"log/slog"
)

// RunInTx runs fn inside what SHOULD be a SurrealDB transaction.
//
// Status note. Early deployment found that the SurrealDB version
// currently used by SourceBridge (checked against the thor cluster's
// surrealdb/surrealdb image) rejects standalone `COMMIT TRANSACTION`
// statements sent over the Go SDK's Query path — the server returns
// "Unexpected statement type encountered: Commit(CommitStatement)".
// Multi-statement batching (BEGIN / body / COMMIT in a single Query
// call) is the correct SurrealDB transaction API, and should be
// adopted when we introduce a safer batching helper.
//
// Until that lands, RunInTx runs fn *without* a transaction wrapper.
// Callers in the trash package pair it with a shared trash_batch_id
// so restore can reverse partial cascades cleanly; the design's
// "post-commit reconciler" pattern is therefore the primary
// correctness mechanism today, not a backup.
//
// Panic semantics are preserved: a panic from fn is recovered and
// re-raised so tests and the runtime still see it; the caller's
// partial writes remain as they are.
func (s *SurrealDB) RunInTx(ctx context.Context, fn func(ctx context.Context) error) (txErr error) {
	db := s.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	defer func() {
		if r := recover(); r != nil {
			slog.Error("RunInTx caller panicked; any partial writes remain as-is (no transaction to cancel)", "panic", r)
			panic(r)
		}
	}()

	return fn(ctx)
}
