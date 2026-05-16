// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

//go:build integration

package db

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// TestSurrealStore_SetRepoSettingsIfVersion_RoundTrip verifies the
// SurrealQL path of SetRepoSettingsIfVersion against a real SurrealDB
// container. The MemStore unit tests in internal/settings/livingwiki cover the
// in-memory mirror; this test exercises the WHERE version = $expected_version
// guard and the empty-result → ErrLWikiSettingsVersionConflict detection at
// livingwiki_repo_settings_store.go:646-657 which the MemStore cannot reach.
func TestSurrealStore_SetRepoSettingsIfVersion_RoundTrip(t *testing.T) {
	surreal := startSurrealContainer(t)
	store := NewLivingWikiRepoSettingsStore(surreal, WithLivingWikiRepoAllowUnencrypted(true))
	ctx := t.Context()

	const tenantID = "test-tenant"

	// ── Sub-test 1: matching version succeeds and increments the version ──────
	t.Run("version_increment_on_success", func(t *testing.T) {
		repoID := "lw-ifver-v1"
		base := livingwiki.RepositoryLivingWikiSettings{
			TenantID:       tenantID,
			RepoID:         repoID,
			Enabled:        false,
			Mode:           livingwiki.RepoWikiModePRReview,
			Sinks:          []livingwiki.RepoWikiSink{{Kind: livingwiki.RepoWikiSinkGitRepo}},
			MaxPagesPerJob: 50,
		}
		// Seed the row via the unconditional SetRepoSettings path; version becomes 1.
		if err := store.SetRepoSettings(ctx, base); err != nil {
			t.Fatalf("seed SetRepoSettings: %v", err)
		}
		seeded, err := store.GetRepoSettings(ctx, tenantID, repoID)
		if err != nil || seeded == nil {
			t.Fatalf("seed read: err=%v, row=%v", err, seeded)
		}
		if seeded.Version != 1 {
			t.Fatalf("expected seeded version=1, got %d", seeded.Version)
		}

		// Now update using the versioned write — should succeed.
		update := *seeded
		update.Enabled = true
		if err := store.SetRepoSettingsIfVersion(ctx, update, seeded.Version); err != nil {
			t.Fatalf("SetRepoSettingsIfVersion(matching): %v", err)
		}

		got, err := store.GetRepoSettings(ctx, tenantID, repoID)
		if err != nil || got == nil {
			t.Fatalf("post-update read: err=%v, row=%v", err, got)
		}
		if !got.Enabled {
			t.Error("expected Enabled=true after successful versioned write")
		}
		if got.Version != 2 {
			t.Errorf("expected version=2 after versioned write, got %d", got.Version)
		}
	})

	// ── Sub-test 2: version mismatch returns ErrLWikiSettingsVersionConflict ──
	t.Run("version_mismatch_returns_conflict", func(t *testing.T) {
		repoID := "lw-ifver-v2"
		base := livingwiki.RepositoryLivingWikiSettings{
			TenantID:       tenantID,
			RepoID:         repoID,
			Enabled:        false,
			Mode:           livingwiki.RepoWikiModePRReview,
			Sinks:          []livingwiki.RepoWikiSink{{Kind: livingwiki.RepoWikiSinkGitRepo}},
			MaxPagesPerJob: 50,
		}
		if err := store.SetRepoSettings(ctx, base); err != nil {
			t.Fatalf("seed SetRepoSettings: %v", err)
		}
		seeded, err := store.GetRepoSettings(ctx, tenantID, repoID)
		if err != nil || seeded == nil {
			t.Fatalf("seed read: err=%v, row=%v", err, seeded)
		}

		// Advance the stored version by doing a successful write first.
		advance := *seeded
		advance.Enabled = true
		if err := store.SetRepoSettingsIfVersion(ctx, advance, seeded.Version); err != nil {
			t.Fatalf("advance write: %v", err)
		}

		// Attempt to write using the stale (pre-advance) version — must conflict.
		stale := *seeded
		stale.Enabled = false
		err = store.SetRepoSettingsIfVersion(ctx, stale, seeded.Version)
		if err == nil {
			t.Fatal("expected ErrLWikiSettingsVersionConflict for stale version, got nil")
		}
		if !isVersionConflict(err) {
			t.Errorf("expected ErrLWikiSettingsVersionConflict, got %v", err)
		}

		// The row should still reflect the advance write, not the stale write.
		got, err := store.GetRepoSettings(ctx, tenantID, repoID)
		if err != nil || got == nil {
			t.Fatalf("post-conflict read: err=%v, row=%v", err, got)
		}
		if !got.Enabled {
			t.Error("stale write must not have overwritten the row; expected Enabled=true")
		}
	})

	// ── Sub-test 3: version=0 takes the unconditional write path ─────────────
	t.Run("version_zero_unconditional", func(t *testing.T) {
		repoID := "lw-ifver-v3"
		settings := livingwiki.RepositoryLivingWikiSettings{
			TenantID:       tenantID,
			RepoID:         repoID,
			Enabled:        true,
			Mode:           livingwiki.RepoWikiModePRReview,
			Sinks:          []livingwiki.RepoWikiSink{{Kind: livingwiki.RepoWikiSinkGitRepo}},
			MaxPagesPerJob: 50,
			Version:        0, // no prior row
		}
		// Version=0 delegates to SetRepoSettings (UPSERT); must not return conflict.
		if err := store.SetRepoSettingsIfVersion(ctx, settings, 0); err != nil {
			t.Fatalf("SetRepoSettingsIfVersion(version=0): %v", err)
		}

		got, err := store.GetRepoSettings(ctx, tenantID, repoID)
		if err != nil || got == nil {
			t.Fatalf("post-unconditional read: err=%v, row=%v", err, got)
		}
		if !got.Enabled {
			t.Error("expected Enabled=true after unconditional write")
		}
	})

	// ── Sub-test 4: concurrent writers with same expected_version — one wins ─
	t.Run("concurrent_writers_one_wins", func(t *testing.T) {
		repoID := "lw-ifver-v4"
		base := livingwiki.RepositoryLivingWikiSettings{
			TenantID:       tenantID,
			RepoID:         repoID,
			Enabled:        false,
			Mode:           livingwiki.RepoWikiModePRReview,
			Sinks:          []livingwiki.RepoWikiSink{{Kind: livingwiki.RepoWikiSinkGitRepo}},
			MaxPagesPerJob: 50,
		}
		if err := store.SetRepoSettings(ctx, base); err != nil {
			t.Fatalf("seed SetRepoSettings: %v", err)
		}
		seeded, err := store.GetRepoSettings(ctx, tenantID, repoID)
		if err != nil || seeded == nil {
			t.Fatalf("seed read: err=%v, row=%v", err, seeded)
		}
		expectedVersion := seeded.Version

		const goroutines = 8
		var (
			wins      atomic.Int32
			conflicts atomic.Int32
			wg        sync.WaitGroup
			start     = make(chan struct{})
		)

		for i := range goroutines {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				<-start // wait for all goroutines to be ready before firing
				update := *seeded
				update.MaxPagesPerJob = 50 + n
				switch store.SetRepoSettingsIfVersion(ctx, update, expectedVersion) {
				case nil:
					wins.Add(1)
				default:
					conflicts.Add(1)
				}
			}(i)
		}

		close(start) // release all goroutines simultaneously
		wg.Wait()

		w := int(wins.Load())
		c := int(conflicts.Load())
		if w != 1 {
			t.Errorf("expected exactly 1 winner, got wins=%d conflicts=%d", w, c)
		}
		if w+c != goroutines {
			t.Errorf("wins (%d) + conflicts (%d) != goroutines (%d)", w, c, goroutines)
		}

		// The stored version must be exactly expectedVersion+1 (one write landed).
		got, err := store.GetRepoSettings(ctx, tenantID, repoID)
		if err != nil || got == nil {
			t.Fatalf("post-race read: err=%v, row=%v", err, got)
		}
		if got.Version != expectedVersion+1 {
			t.Errorf("expected version=%d after one concurrent winner, got %d", expectedVersion+1, got.Version)
		}
	})
}

// isVersionConflict reports whether err is or wraps ErrLWikiSettingsVersionConflict.
func isVersionConflict(err error) bool {
	return errors.Is(err, livingwiki.ErrLWikiSettingsVersionConflict)
}
