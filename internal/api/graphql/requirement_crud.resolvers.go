// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

// Resolver support for the new requirement CRUD mutations that ship
// alongside VSCode extension 0.3.0. The thin shims below back the
// gqlgen-generated stubs; the heavy lifting is in the graph store's
// StoreRequirement / UpdateRequirementFields methods.
//
// Validation policy:
//
//   - title is required (the GraphQL schema enforces non-null but we
//     guard for stripped whitespace)
//   - externalId auto-generates as "REQ-<8-char-uuid-prefix>" when
//     blank. The uniqueness check uses the store's existing
//     soft-delete-aware read paths.
//   - repositoryId must resolve to a real repo in the current store.
//   - trashed rows are invisible to this path (the store helpers
//     already filter deleted_at).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
)

// createRequirementImpl is called from the gqlgen stub in
// schema.resolvers.go. Wiring the stub is a one-line pass-through; the
// real behaviour lives here.
func (r *Resolver) createRequirementImpl(ctx context.Context, input CreateRequirementInput) (*Requirement, error) {
	store := r.getStore(ctx)
	if store == nil {
		return nil, errors.New("store not initialised")
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return nil, errors.New("title is required")
	}
	repo := store.GetRepository(input.RepositoryID)
	if repo == nil {
		return nil, fmt.Errorf("repository %q not found", input.RepositoryID)
	}

	externalID := strings.TrimSpace(deref(input.ExternalID))
	if externalID == "" {
		externalID = "REQ-" + uuid.NewString()[:8]
	}

	// Uniqueness against live rows. GetRequirementByExternalID already
	// filters soft-deleted entries. This precheck is a friendly UX layer;
	// migration 032's UNIQUE index on (repo_id, external_id) is the
	// authoritative race-proof backstop.
	if existing := store.GetRequirementByExternalID(input.RepositoryID, externalID); existing != nil {
		slog.Warn("requirement_create_duplicate_external_id",
			"repo_id", input.RepositoryID,
			"external_id", externalID,
		)
		return nil, fmt.Errorf("a requirement with externalId %q already exists in this repository", externalID)
	}

	now := time.Now().UTC()
	rec := &graphstore.StoredRequirement{
		ID:                 uuid.NewString(),
		RepoID:             input.RepositoryID,
		ExternalID:         externalID,
		Title:              title,
		Description:        deref(input.Description),
		Priority:           deref(input.Priority),
		Source:             defaultIfBlank(deref(input.Source), "manual"),
		Tags:               input.Tags,
		AcceptanceCriteria: []string{},
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	store.StoreRequirement(input.RepositoryID, rec)

	// Post-hoc verification: StoreRequirement currently swallows DB errors
	// and logs them (signature doesn't return an error). A rejected unique
	// constraint would therefore look like a silent no-op. Re-resolve by
	// external ID to confirm the row actually persisted.
	persisted := store.GetRequirementByExternalID(input.RepositoryID, externalID)
	if persisted == nil {
		slog.Warn("requirement_create_persist_failed",
			"repo_id", input.RepositoryID,
			"external_id", externalID,
			"hint", "likely unique-constraint violation from a concurrent create",
		)
		return nil, fmt.Errorf("failed to persist requirement with externalId %q — please retry", externalID)
	}

	slog.Info("requirement_created",
		"repo_id", persisted.RepoID,
		"requirement_id", persisted.ID,
		"external_id", persisted.ExternalID,
		"source", persisted.Source,
	)
	r.publishEvent("requirement.created", map[string]interface{}{
		"repository_id":  input.RepositoryID,
		"requirement_id": persisted.ID,
		"external_id":    persisted.ExternalID,
	})
	return mapRequirement(persisted), nil
}

func (r *Resolver) updateRequirementFieldsImpl(ctx context.Context, input UpdateRequirementFieldsInput) (*Requirement, error) {
	store := r.getStore(ctx)
	if store == nil {
		return nil, errors.New("store not initialised")
	}
	// Strip whitespace-only strings to nil so the store doesn't write them.
	patch := graphstore.RequirementUpdate{
		ExternalID:         trimmed(input.ExternalID),
		Title:              trimmed(input.Title),
		Description:        input.Description, // allow empty string to clear
		Priority:           input.Priority,
		Source:             trimmed(input.Source),
	}
	if input.Tags != nil {
		tags := input.Tags
		patch.Tags = &tags
	}
	if input.AcceptanceCriteria != nil {
		ac := input.AcceptanceCriteria
		patch.AcceptanceCriteria = &ac
	}

	updated := store.UpdateRequirementFields(input.ID, patch)
	if updated == nil {
		// Three reasons this returns nil:
		//   1. requirement not found (or trashed)
		//   2. externalId app-layer precheck collision
		//   3. DB-level unique-constraint rejection (migration 032 race backstop)
		// Disambiguate so the UI can render a specific error.
		current := store.GetRequirement(input.ID)
		if current == nil {
			return nil, fmt.Errorf("requirement %q not found", input.ID)
		}
		if input.ExternalID != nil && *input.ExternalID != "" && *input.ExternalID != current.ExternalID {
			slog.Warn("requirement_update_duplicate_external_id",
				"repo_id", current.RepoID,
				"requirement_id", input.ID,
				"attempted_external_id", *input.ExternalID,
			)
			return nil, fmt.Errorf("externalId %q is already taken by another requirement in this repository", *input.ExternalID)
		}
		slog.Warn("requirement_update_failed",
			"requirement_id", input.ID,
			"hint", "no field-level diagnosis; check DB logs for unique-constraint violations",
		)
		return nil, errors.New("update failed")
	}
	// Report which fields were actually in the patch — useful for audit /
	// debugging without logging PII-level content.
	var changedFields []string
	if patch.ExternalID != nil {
		changedFields = append(changedFields, "external_id")
	}
	if patch.Title != nil {
		changedFields = append(changedFields, "title")
	}
	if patch.Description != nil {
		changedFields = append(changedFields, "description")
	}
	if patch.Priority != nil {
		changedFields = append(changedFields, "priority")
	}
	if patch.Source != nil {
		changedFields = append(changedFields, "source")
	}
	if patch.Tags != nil {
		changedFields = append(changedFields, "tags")
	}
	if patch.AcceptanceCriteria != nil {
		changedFields = append(changedFields, "acceptance_criteria")
	}
	slog.Info("requirement_updated",
		"repo_id", updated.RepoID,
		"requirement_id", updated.ID,
		"changed_fields", changedFields,
	)
	r.publishEvent("requirement.updated", map[string]interface{}{
		"repository_id":  updated.RepoID,
		"requirement_id": updated.ID,
	})
	return mapRequirement(updated), nil
}

// trimmed returns nil when the trimmed input is empty, preserving
// "don't change this field" semantics. An empty string OTOH means
// "store blank" and is passed through by the non-trimming handlers
// (description, priority).
func trimmed(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil
	}
	return &t
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func defaultIfBlank(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// Pointer helpers for the Tags/AcceptanceCriteria fields. gqlgen
// generates `[]string` (non-nullable inner type) as `[]string`; the
// store wants a pointer-to-slice so it can distinguish "unset" from
// "empty". For the create/update paths we adopt the nil-means-unset
// convention by keeping them as `[]string` — empty slice means "clear
// tags", nil means "don't touch." That matches common JSON client
// semantics.
var _ = []string{} // package-use placeholder; avoids an "imported and not used" if the helpers above shift.
