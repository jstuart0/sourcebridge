// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"strings"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/events"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// newResolverWithRepo spins up an in-memory store, indexes a repo, and
// returns a Resolver wired around it. Mirrors the lightweight setup
// used by other resolver tests in this package.
func newResolverWithRepo(t *testing.T) (*Resolver, string) {
	t.Helper()
	store := graphstore.NewStore()
	result := &indexer.IndexResult{
		RepoName: "test-repo",
		RepoPath: "/tmp/test",
		Files:    []indexer.FileResult{{Path: "main.go", Language: "go", LineCount: 10}},
	}
	repo, _ := store.StoreIndexResult(result)
	return &Resolver{
		Store:    store,
		EventBus: events.NewBus(),
	}, repo.ID
}

func TestCreateRequirement_Happy(t *testing.T) {
	r, repoID := newResolverWithRepo(t)
	extID := "AUTH-001"
	req, err := r.createRequirementImpl(context.Background(), CreateRequirementInput{
		RepositoryID: repoID,
		ExternalID:   &extID,
		Title:        "Authenticate users",
		Priority:     strPtr("high"),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if req == nil || req.ID == "" {
		t.Fatalf("expected a populated Requirement, got %+v", req)
	}
	if req.Title != "Authenticate users" {
		t.Errorf("title: got %q", req.Title)
	}
	if req.ExternalID == nil || *req.ExternalID != "AUTH-001" {
		t.Errorf("externalId: got %+v", req.ExternalID)
	}
}

func TestCreateRequirement_AutoGenExternalID(t *testing.T) {
	r, repoID := newResolverWithRepo(t)
	req, err := r.createRequirementImpl(context.Background(), CreateRequirementInput{
		RepositoryID: repoID,
		Title:        "No external id supplied",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if req.ExternalID == nil || !strings.HasPrefix(*req.ExternalID, "REQ-") {
		t.Errorf("expected auto-gen REQ- prefix, got %+v", req.ExternalID)
	}
}

func TestCreateRequirement_RejectsBlankTitle(t *testing.T) {
	r, repoID := newResolverWithRepo(t)
	_, err := r.createRequirementImpl(context.Background(), CreateRequirementInput{
		RepositoryID: repoID,
		Title:        "   ",
	})
	if err == nil || !strings.Contains(err.Error(), "title is required") {
		t.Errorf("want blank-title error, got %v", err)
	}
}

func TestCreateRequirement_RejectsUnknownRepo(t *testing.T) {
	r, _ := newResolverWithRepo(t)
	_, err := r.createRequirementImpl(context.Background(), CreateRequirementInput{
		RepositoryID: "nope",
		Title:        "t",
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("want not-found error, got %v", err)
	}
}

func TestCreateRequirement_RejectsDuplicateExternalID(t *testing.T) {
	r, repoID := newResolverWithRepo(t)
	ext := "DUP-1"
	_, err := r.createRequirementImpl(context.Background(), CreateRequirementInput{
		RepositoryID: repoID,
		ExternalID:   &ext,
		Title:        "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.createRequirementImpl(context.Background(), CreateRequirementInput{
		RepositoryID: repoID,
		ExternalID:   &ext,
		Title:        "second",
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("want duplicate error, got %v", err)
	}
}

func TestUpdateRequirementFields_Happy(t *testing.T) {
	r, repoID := newResolverWithRepo(t)
	ext := "UPD-001"
	created, err := r.createRequirementImpl(context.Background(), CreateRequirementInput{
		RepositoryID: repoID,
		ExternalID:   &ext,
		Title:        "original title",
	})
	if err != nil {
		t.Fatal(err)
	}

	newTitle := "renamed"
	newDesc := "now has a description"
	updated, err := r.updateRequirementFieldsImpl(context.Background(), UpdateRequirementFieldsInput{
		ID:          created.ID,
		Title:       &newTitle,
		Description: &newDesc,
	})
	if err != nil {
		t.Fatalf("update err: %v", err)
	}
	if updated.Title != "renamed" {
		t.Errorf("title: %q", updated.Title)
	}
	if updated.Description != "now has a description" {
		t.Errorf("description: %q", updated.Description)
	}
}

func TestUpdateRequirementFields_ExternalIDConflict(t *testing.T) {
	r, repoID := newResolverWithRepo(t)
	aID := "A"
	bID := "B"
	if _, err := r.createRequirementImpl(context.Background(), CreateRequirementInput{
		RepositoryID: repoID, ExternalID: &aID, Title: "a",
	}); err != nil {
		t.Fatal(err)
	}
	created, err := r.createRequirementImpl(context.Background(), CreateRequirementInput{
		RepositoryID: repoID, ExternalID: &bID, Title: "b",
	})
	if err != nil {
		t.Fatal(err)
	}
	clash := "A"
	_, err = r.updateRequirementFieldsImpl(context.Background(), UpdateRequirementFieldsInput{
		ID:         created.ID,
		ExternalID: &clash,
	})
	if err == nil || !strings.Contains(err.Error(), "already taken") {
		t.Errorf("want externalId conflict, got %v", err)
	}
}

func TestUpdateRequirementFields_UnknownID(t *testing.T) {
	r, _ := newResolverWithRepo(t)
	newTitle := "t"
	_, err := r.updateRequirementFieldsImpl(context.Background(), UpdateRequirementFieldsInput{
		ID:    "missing",
		Title: &newTitle,
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("want not-found error, got %v", err)
	}
}

// Round-trip verifies the bug that motivated Phase 1.1: acceptanceCriteria
// written via updateRequirementFields must be readable via the GraphQL
// Requirement type. Before the fix, the mapper simply dropped the field.
func TestUpdateRequirementFields_AcceptanceCriteria_RoundTrip(t *testing.T) {
	r, repoID := newResolverWithRepo(t)
	ext := "AC-1"
	created, err := r.createRequirementImpl(context.Background(), CreateRequirementInput{
		RepositoryID: repoID,
		ExternalID:   &ext,
		Title:        "needs acceptance criteria",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Brand-new requirements default to an empty slice — not nil — so the
	// non-null GraphQL contract is honored even before the first update.
	if created.AcceptanceCriteria == nil {
		t.Errorf("expected empty slice, got nil")
	}
	if len(created.AcceptanceCriteria) != 0 {
		t.Errorf("expected empty on create, got %v", created.AcceptanceCriteria)
	}

	criteria := []string{"User can sign in", "Session persists across reload"}
	updated, err := r.updateRequirementFieldsImpl(context.Background(), UpdateRequirementFieldsInput{
		ID:                 created.ID,
		AcceptanceCriteria: criteria,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.AcceptanceCriteria) != 2 ||
		updated.AcceptanceCriteria[0] != "User can sign in" ||
		updated.AcceptanceCriteria[1] != "Session persists across reload" {
		t.Errorf("round-trip failed: got %v", updated.AcceptanceCriteria)
	}

	// Empty slice on update should clear, not preserve.
	cleared, err := r.updateRequirementFieldsImpl(context.Background(), UpdateRequirementFieldsInput{
		ID:                 created.ID,
		AcceptanceCriteria: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.AcceptanceCriteria) != 0 {
		t.Errorf("clear failed: got %v", cleared.AcceptanceCriteria)
	}
}

// strPtr is declared elsewhere in the package; no helper needed here.
