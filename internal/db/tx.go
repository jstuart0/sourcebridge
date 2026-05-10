// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	surrealdb "github.com/surrealdb/surrealdb.go"
)

// RunInTx runs fn inside what SHOULD be a SurrealDB transaction.
//
// Status note. Early deployment found that the SurrealDB version
// currently used by SourceBridge (checked against the thor cluster's
// surrealdb/surrealdb image) rejects standalone `COMMIT TRANSACTION`
// statements sent over the Go SDK's Query path — the server returns
// "Unexpected statement type encountered: Commit(CommitStatement)".
// Multi-statement batching (BEGIN / body / COMMIT in a single Query
// call) is supported — see RunInTxBatch for the safe multi-step write
// helper.
//
// RunInTx itself runs fn *without* a transaction wrapper. Callers in
// the trash package pair it with a shared trash_batch_id so restore can
// reverse partial cascades cleanly; the design's "post-commit
// reconciler" pattern is therefore the primary correctness mechanism
// today, not a backup.
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

// RunInTxBatch executes statements as an atomic SurrealDB transaction by
// wrapping them in a single Query call prefixed with BEGIN TRANSACTION and
// suffixed with COMMIT TRANSACTION.
//
// SurrealDB v2.6.5 honours multi-statement transactions when all statements
// are sent in a single Query call (verified by TestRunInTxBatch_MultiStatementTransaction
// in tx_integration_test.go). Separate BEGIN and COMMIT calls are NOT supported
// over the Go SDK's Query path.
//
// vars is a map of bound parameters shared across all statements. Returns nil
// on success. Any statement-level error (including THROW) causes the
// transaction to roll back and the error to be returned.
func (s *SurrealDB) RunInTxBatch(ctx context.Context, statements []string, vars map[string]any) error {
	db := s.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	if len(statements) == 0 {
		return nil
	}

	sql := "BEGIN TRANSACTION;\n" +
		strings.Join(statements, ";\n") +
		";\nCOMMIT TRANSACTION"

	if vars == nil {
		vars = map[string]any{}
	}

	_, err := surrealdb.Query[any](ctx, db, sql, vars)
	return err
}
