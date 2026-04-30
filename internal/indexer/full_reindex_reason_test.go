// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package indexer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestRepoIndexFullReason_GuardRefusesUnspecified is half of Phase 1.A
// done-definition test #10 (the runtime-assertion half). The other
// half — "the router has no code path that reaches IndexRepository or
// IndexRepositoryIncremental" — lands in Phase 1.C alongside the
// router itself; documented in this file's
// TestIndexRepository_RouterPathDeferred placeholder.
//
// Both halves prove the same invariant: an accidental change-event
// invocation of the whole-tree reindex cannot succeed. This test
// covers the runtime-side assertion: IndexRepository refuses to run
// without one of the two valid RepoIndexFullReason values.
func TestRepoIndexFullReason_GuardRefusesUnspecified(t *testing.T) {
	idx := NewIndexer(nil)
	_, err := idx.IndexRepository(context.Background(), t.TempDir(), ReasonUnspecified)
	if !errors.Is(err, ErrInvalidFullReindexReason) {
		t.Fatalf("IndexRepository(ReasonUnspecified) = %v, want errors.Is ErrInvalidFullReindexReason", err)
	}
}

// TestRepoIndexFullReason_GuardRefusesUnknown covers the case where a
// caller hands us a RepoIndexFullReason value outside the documented
// constant set (e.g. via int conversion or a future enum value that
// hasn't been audited yet). The guard refuses the same as
// ReasonUnspecified.
func TestRepoIndexFullReason_GuardRefusesUnknown(t *testing.T) {
	idx := NewIndexer(nil)
	_, err := idx.IndexRepository(context.Background(), t.TempDir(), RepoIndexFullReason(999))
	if !errors.Is(err, ErrInvalidFullReindexReason) {
		t.Fatalf("IndexRepository(999) = %v, want errors.Is ErrInvalidFullReindexReason", err)
	}
}

// TestRepoIndexFullReason_GuardAcceptsValid pins that both named
// reasons pass validation. We don't actually need the repository to
// be a real one for the guard to fire — the guard runs first; if it
// passed we move on to the scan, which fails for an empty tempdir
// with a non-validation error. So "guard passed" == "we got past the
// validation error and into the actual scan logic" (which produces
// either a different error or a result).
func TestRepoIndexFullReason_GuardAcceptsValid(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	idx := NewIndexer(nil)
	repo := t.TempDir()
	// Seed a tiny .go file so the scanner has something legitimate
	// to index — that way the function actually completes rather
	// than failing mid-scan and confusing the assertion below.
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, reason := range []RepoIndexFullReason{ReasonInitialOnboard, ReasonOperatorRebuild} {
		t.Run(reason.String(), func(t *testing.T) {
			_, err := idx.IndexRepository(context.Background(), repo, reason)
			// We tolerate any error EXCEPT
			// ErrInvalidFullReindexReason — anything else means
			// the guard let us through and the failure (if any)
			// is downstream.
			if errors.Is(err, ErrInvalidFullReindexReason) {
				t.Fatalf("guard rejected valid reason %s: %v", reason.String(), err)
			}
		})
	}
}

// TestRepoIndexFullReason_StringStability locks the log-friendly
// string representation. The audit trail (structured logs and Plane
// tickets) references these names directly; an inadvertent rename
// breaks downstream tooling that filters on them.
func TestRepoIndexFullReason_StringStability(t *testing.T) {
	cases := []struct {
		reason RepoIndexFullReason
		want   string
	}{
		{ReasonUnspecified, "unspecified"},
		{ReasonInitialOnboard, "initial_onboard"},
		{ReasonOperatorRebuild, "operator_rebuild"},
		{RepoIndexFullReason(99), "unspecified"}, // any unknown value falls back
	}
	for _, c := range cases {
		if got := c.reason.String(); got != c.want {
			t.Fatalf("reason %d.String() = %q, want %q", c.reason, got, c.want)
		}
	}
}

// TestIndexRepository_RouterPathDeferred is a placeholder marker for
// the second half of Phase 1.A done-definition test #10: "the router
// has no code path that reaches IndexRepositoryIncremental or
// IndexRepository." The router does not exist yet (it ships in
// Phase 1.C, package internal/changewatch), so the assertion is
// deferred to that slice's test suite. Documented here so a reviewer
// reading the test file in 1.A sees what's covered and what isn't.
//
// Per Phase 1.A scope: only the runtime-side guard is exercised; the
// build-time import-graph assertion is part of Phase 1.C.
func TestIndexRepository_RouterPathDeferred(t *testing.T) {
	t.Skip("router does not exist yet; the no-call-paths-from-router half of test #10 lands in Phase 1.C alongside internal/changewatch")
}
