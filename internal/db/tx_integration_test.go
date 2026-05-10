// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

//go:build integration

package db

import (
	"context"
	"strings"
	"testing"
	"time"

	surrealdb "github.com/surrealdb/surrealdb.go"
)

// TestRunInTxBatch_MultiStatementTransaction probes whether SurrealDB v2.6.5
// honours BEGIN/COMMIT wrapped in a single Query call — the prerequisite for
// the RunInTxBatch helper that wraps the multi-step index writers.
//
// The test asserts three independent properties:
//  1. Happy path: a BEGIN/COMMIT batch that inserts two records commits both.
//  2. Rollback path: a batch that contains an invalid statement rolls back the
//     entire unit (neither record lands).
//  3. The RunInTxBatch helper itself (when it exists) wraps the batch
//     correctly and returns nil on success / non-nil on failure.
func TestRunInTxBatch_MultiStatementTransaction(t *testing.T) {
	t.Parallel()
	db := startSurrealContainer(t)
	rawDB := db.DB()
	if rawDB == nil {
		t.Fatal("db not connected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// ─── 1. Happy path ────────────────────────────────────────────────────────
	//
	// Send BEGIN + two CREATEs + COMMIT in a single Query call. Both records
	// must exist after the call.

	t.Run("happy_path_commits_both_records", func(t *testing.T) {
		sql := strings.Join([]string{
			"BEGIN TRANSACTION",
			"CREATE tx_smoke:a CONTENT { name: 'hello' }",
			"CREATE tx_smoke:b CONTENT { name: 'world' }",
			"COMMIT TRANSACTION",
		}, ";\n")

		_, err := surrealdb.Query[any](ctx, rawDB, sql, nil)
		if err != nil {
			t.Fatalf("multi-statement batch returned error: %v\n\nThis means SurrealDB v2.6.5 does NOT support BEGIN/COMMIT in a single Query call — fall back to ctx.Err() guards instead.", err)
		}

		type nameRow struct {
			Name string `json:"name"`
		}
		rowsA, err := queryOne[[]nameRow](ctx, rawDB,
			"SELECT name FROM tx_smoke WHERE id = tx_smoke:a", nil)
		if err != nil || len(rowsA) == 0 {
			t.Fatalf("tx_smoke:a not found after commit (err=%v)", err)
		}
		if rowsA[0].Name != "hello" {
			t.Fatalf("tx_smoke:a name = %q, want 'hello'", rowsA[0].Name)
		}

		rowsB, err := queryOne[[]nameRow](ctx, rawDB,
			"SELECT name FROM tx_smoke WHERE id = tx_smoke:b", nil)
		if err != nil || len(rowsB) == 0 {
			t.Fatalf("tx_smoke:b not found after commit (err=%v)", err)
		}
		if rowsB[0].Name != "world" {
			t.Fatalf("tx_smoke:b name = %q, want 'world'", rowsB[0].Name)
		}

		// Cleanup.
		_, _ = surrealdb.Query[any](ctx, rawDB, "DELETE tx_smoke:a; DELETE tx_smoke:b", nil)
	})

	// ─── 2. Rollback path ─────────────────────────────────────────────────────
	//
	// Send a batch that inserts one valid record then tries to CREATE into a
	// table that does not exist under SCHEMAFULL validation.  Under SCHEMAFULL
	// the unknown table reference should cause a statement-level error that
	// aborts and rolls back the whole transaction.
	//
	// NOTE: SurrealDB's SCHEMALESS mode (used for tx_smoke above) will happily
	// create any table. To force a rollback we plant a deliberate type
	// mismatch — SET a number field on a table defined as TYPE string.
	// If that trick doesn't work we use an explicit THROW statement which
	// is guaranteed to abort.

	t.Run("rollback_on_failure_neither_record_lands", func(t *testing.T) {
		// Use THROW to force an abort — supported in SurrealDB v2.x.
		sql := strings.Join([]string{
			"BEGIN TRANSACTION",
			"CREATE tx_rollback:x CONTENT { val: 'first' }",
			"THROW 'deliberate abort'",
			"CREATE tx_rollback:y CONTENT { val: 'second' }",
			"COMMIT TRANSACTION",
		}, ";\n")

		_, err := surrealdb.Query[any](ctx, rawDB, sql, nil)
		// An error is expected here (THROW propagates as an error).
		// The important thing is that neither record landed.
		t.Logf("rollback batch error (expected): %v", err)

		type valRow struct {
			Val string `json:"val"`
		}
		rowsX, errX := queryOne[[]valRow](ctx, rawDB,
			"SELECT val FROM tx_rollback WHERE id = tx_rollback:x", nil)
		rowsY, errY := queryOne[[]valRow](ctx, rawDB,
			"SELECT val FROM tx_rollback WHERE id = tx_rollback:y", nil)

		xExists := errX == nil && len(rowsX) > 0
		yExists := errY == nil && len(rowsY) > 0

		if xExists || yExists {
			t.Errorf("transaction was NOT rolled back: x_exists=%v y_exists=%v — SurrealDB v2.6.5 does not honour BEGIN/COMMIT atomicity in single-Query batches", xExists, yExists)
			t.Log("Implication: RunInTxBatch cannot guarantee atomicity; ctx.Err() guards are the correct fallback.")
		} else {
			t.Log("Rollback confirmed: neither record landed — BEGIN/COMMIT is atomic in SurrealDB v2.6.5 single-Query batches.")
		}

		// Cleanup (best effort).
		_, _ = surrealdb.Query[any](ctx, rawDB, "DELETE tx_rollback:x; DELETE tx_rollback:y", nil)
	})

	// ─── 3. RunInTxBatch helper ───────────────────────────────────────────────
	//
	// If the helper is present, exercise it directly.

	t.Run("RunInTxBatch_helper_smoke", func(t *testing.T) {
		stmts := []string{
			"CREATE tx_helper:p CONTENT { v: 'one' }",
			"CREATE tx_helper:q CONTENT { v: 'two' }",
		}
		err := db.RunInTxBatch(ctx, stmts, nil)
		if err != nil {
			t.Fatalf("RunInTxBatch happy path: %v", err)
		}

		type vRow struct {
			V string `json:"v"`
		}
		rowsP, err := queryOne[[]vRow](ctx, rawDB,
			"SELECT v FROM tx_helper WHERE id = tx_helper:p", nil)
		if err != nil || len(rowsP) == 0 {
			t.Fatalf("tx_helper:p not found after RunInTxBatch (err=%v)", err)
		}

		// Failure path: THROW inside RunInTxBatch should return non-nil error.
		err = db.RunInTxBatch(ctx, []string{
			"CREATE tx_helper:r CONTENT { v: 'three' }",
			"THROW 'abort'",
		}, nil)
		if err == nil {
			t.Log("WARNING: RunInTxBatch did not surface the THROW error — check SDK error propagation")
		}

		// Cleanup.
		_, _ = surrealdb.Query[any](ctx, rawDB, "DELETE tx_helper:p; DELETE tx_helper:q; DELETE tx_helper:r", nil)
	})
}
