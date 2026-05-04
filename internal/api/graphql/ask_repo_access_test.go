// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// SEC-5 test suite — GraphQL Ask and DiscussCode repo-access gating.
//
// Codex r2 finding C-2: Slice 6 (ce682ed) gated REST /api/v1/ask but missed
// the GraphQL Ask mutation and dispatchDiscussThroughOrchestrator. Both pass
// a user-controlled repositoryID directly to qa.Orchestrator.Ask without an
// access check.
//
// Fix: checkRepoAccessGraphQL is called before qa.Orchestrator.Ask in both
// paths.  This file tests:
//
//  1. The checkRepoAccessGraphQL helper itself (unit test of the security
//     primitive).  Repository absent → forbidden error.  Repository present →
//     nil error.
//
//  2. The Ask mutation resolver path: when QA is non-nil and the repository
//     is absent from the store, the mutation must return a forbidden error
//     before touching QA.Ask (proved by returning nil QA — the nil dereference
//     would panic if the access check were skipped).
//
//  3. The dispatchDiscussThroughOrchestrator path: same logic.

package graphql

import (
	"context"
	"strings"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/qa"
)

// ---------------------------------------------------------------------------
// Helper: isForbiddenError
// ---------------------------------------------------------------------------

func isForbiddenError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "forbidden")
}

// ---------------------------------------------------------------------------
// checkRepoAccessGraphQL unit tests (the security primitive)
// ---------------------------------------------------------------------------

// TestCheckRepoAccessGraphQLForbiddenWhenAbsent verifies the helper returns a
// forbidden-flavoured error when the repository is not in the store.
func TestCheckRepoAccessGraphQLForbiddenWhenAbsent(t *testing.T) {
	r := &Resolver{Store: graph.NewStore()}
	err := r.checkRepoAccessGraphQL(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("expected forbidden error, got nil")
	}
	if !isForbiddenError(err) {
		t.Errorf("expected 'forbidden' in error text, got: %v", err)
	}
}

// TestCheckRepoAccessGraphQLNilWhenPresent verifies the helper returns nil
// when the repository is present in the (tenant-filtered) store.
func TestCheckRepoAccessGraphQLNilWhenPresent(t *testing.T) {
	s := graph.NewStore()
	repo, err := s.CreateRepository("test-repo", "/tmp/test-repo")
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	r := &Resolver{Store: s}
	if err := r.checkRepoAccessGraphQL(context.Background(), repo.ID); err != nil {
		t.Errorf("expected nil for accessible repo, got: %v", err)
	}
}

// TestCheckRepoAccessGraphQLForbiddenOnNilStore verifies that a nil store
// (should not happen in production, but defensive) returns a forbidden error
// rather than panicking.
func TestCheckRepoAccessGraphQLForbiddenOnNilStore(t *testing.T) {
	r := &Resolver{Store: nil}
	err := r.checkRepoAccessGraphQL(context.Background(), "any-repo")
	if err == nil {
		t.Fatal("expected forbidden error with nil store, got nil")
	}
	if !isForbiddenError(err) {
		t.Errorf("expected 'forbidden' in error text, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Ask mutation — access gate fires before QA.Ask
// ---------------------------------------------------------------------------

// TestAskMutationForbiddenBeforeQA verifies that the Ask mutation returns a
// forbidden error when the repository is absent, and that QA.Ask is never
// reached.  We prove this by passing a non-nil *qa.Orchestrator whose Ask
// method would panic on a nil-dereference if the access check were skipped
// (the orchestrator has no synthesizer wired, so Ask would panic or error).
// We only care that the returned error is the forbidden one from the access
// check — not an orchestrator failure.
//
// Design note: because *qa.Orchestrator is a concrete type (not an interface)
// we cannot use a mock here.  Instead we rely on the fact that the forbidden
// error from checkRepoAccessGraphQL is returned before any QA.Ask call.
// A nil *qa.Orchestrator dereference would cause a panic — if the test
// does not panic and returns the forbidden error, the gate fired correctly.
func TestAskMutationForbiddenBeforeQA(t *testing.T) {
	emptyStore := graph.NewStore()
	// A zero-value *qa.Orchestrator: Ask would panic (nil receiver fields)
	// if it were ever reached.
	r := &mutationResolver{&Resolver{
		Store: emptyStore,
		QA:    &qa.Orchestrator{},
	}}

	_, err := r.Ask(context.Background(), AskInput{
		RepositoryID: "nonexistent",
		Question:     "q",
	})

	if err == nil {
		t.Fatal("expected forbidden error, got nil")
	}
	if !isForbiddenError(err) {
		t.Errorf("expected forbidden access error before QA.Ask, got: %v", err)
	}
}

// TestAskMutationPassesWhenRepoPresent verifies that when the repository IS
// accessible, the Ask mutation gets past the access check. The QA orchestrator
// will return an error (no synthesizer/worker wired), but it must NOT be the
// "forbidden" access-denial error.
func TestAskMutationPassesWhenRepoPresent(t *testing.T) {
	s := graph.NewStore()
	repo, err := s.CreateRepository("test-repo", "/tmp/test-repo")
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	r := &mutationResolver{&Resolver{
		Store: s,
		QA:    &qa.Orchestrator{},
	}}

	_, err = r.Ask(context.Background(), AskInput{
		RepositoryID: repo.ID,
		Question:     "q",
	})

	// The QA orchestrator may fail (no worker), but it must not be a forbidden error.
	if isForbiddenError(err) {
		t.Errorf("access check should have passed for accessible repo; got forbidden: %v", err)
	}
}

// ---------------------------------------------------------------------------
// dispatchDiscussThroughOrchestrator — access gate fires before QA.Ask
// ---------------------------------------------------------------------------

// TestDiscussForbiddenBeforeQA verifies that dispatchDiscussThroughOrchestrator
// returns a forbidden error for an inaccessible repository before touching QA.
func TestDiscussForbiddenBeforeQA(t *testing.T) {
	emptyStore := graph.NewStore()
	r := &mutationResolver{&Resolver{
		Store: emptyStore,
		QA:    &qa.Orchestrator{},
	}}

	_, err := r.dispatchDiscussThroughOrchestrator(context.Background(), DiscussCodeInput{
		RepositoryID: "nonexistent",
		Question:     "q",
	})

	if err == nil {
		t.Fatal("expected forbidden error, got nil")
	}
	if !isForbiddenError(err) {
		t.Errorf("expected forbidden access error before QA.Ask, got: %v", err)
	}
}

// TestDiscussPassesWhenRepoPresent verifies that dispatchDiscussThroughOrchestrator
// gets past the access check when the repository is accessible.
func TestDiscussPassesWhenRepoPresent(t *testing.T) {
	s := graph.NewStore()
	repo, err := s.CreateRepository("test-repo", "/tmp/test-repo")
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	r := &mutationResolver{&Resolver{
		Store: s,
		QA:    &qa.Orchestrator{},
	}}

	_, err = r.dispatchDiscussThroughOrchestrator(context.Background(), DiscussCodeInput{
		RepositoryID: repo.ID,
		Question:     "q",
	})

	if isForbiddenError(err) {
		t.Errorf("access check should have passed for accessible repo; got forbidden: %v", err)
	}
}
