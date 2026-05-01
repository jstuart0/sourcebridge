// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package indexer

import (
	"context"
	"errors"
	"go/ast"
	goparser "go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRepoIndexFullReason_GuardRefusesUnspecified is half of Phase 1.A
// done-definition test #10 (the runtime-assertion half). The other
// half — "the router has no code path that reaches IndexRepository or
// IndexRepositoryIncremental" — lands as
// TestIndexRepository_RouterHasNoFullReindexCallPath in this file
// (Phase 1.C) and exercises an AST scan over internal/changewatch.
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

// TestIndexRepository_RouterHasNoFullReindexCallPath is the second half
// of Phase 1 done-definition test #10: the router (`internal/changewatch`,
// shipped in Phase 1.C) has no code path that reaches
// IndexRepositoryIncremental or IndexRepository.
//
// Implementation: AST-walk every non-test Go file in
// internal/changewatch, looking for any selector expression whose Sel
// name matches one of the disallowed entry-points. We allow
// "IndexFiles" since that's the contract method, and we ignore comments
// and string literals (only real identifier references count).
//
// This is mechanical proof, not a runtime check: a future refactor that
// wires the full-reindex paths into the router turns this red on the
// next test run, before any production traffic reaches the change.
func TestIndexRepository_RouterHasNoFullReindexCallPath(t *testing.T) {
	disallowed := map[string]bool{
		"IndexRepository":            true,
		"IndexRepositoryIncremental": true,
	}

	// Locate the changewatch package source. The test runs with the
	// indexer package as the working directory, so we walk relative.
	pkgDir := filepath.Join("..", "changewatch")
	if _, err := os.Stat(pkgDir); err != nil {
		t.Fatalf("cannot locate internal/changewatch package source at %q: %v (Phase 1.C must ship it)", pkgDir, err)
	}

	fset := token.NewFileSet()
	pkgs, err := goparser.ParseDir(fset, pkgDir, func(fi os.FileInfo) bool {
		// Production-side files only. Skip _test.go because the test
		// harness's stubIndexer interface assertion is a deliberate
		// surface check, not a router call path.
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, goparser.ParseComments)
	if err != nil {
		t.Fatalf("goparser.ParseDir: %v", err)
	}

	type violation struct {
		File string
		Line int
		Name string
	}
	var violations []violation

	for _, pkg := range pkgs {
		for fname, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil {
					return true
				}
				if disallowed[sel.Sel.Name] {
					pos := fset.Position(sel.Pos())
					violations = append(violations, violation{
						File: fname,
						Line: pos.Line,
						Name: sel.Sel.Name,
					})
				}
				return true
			})
		}
	}

	if len(violations) > 0 {
		for _, v := range violations {
			t.Errorf("internal/changewatch %s:%d references %q — the router must NOT reach the full-reindex paths. See plan v5 audit of latent full-reindex paths.", v.File, v.Line, v.Name)
		}
	}
}
