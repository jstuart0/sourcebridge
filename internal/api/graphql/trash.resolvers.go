// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/events"
	"github.com/sourcebridge/sourcebridge/internal/trash"
)

// Trash resolver support. These functions replace the auto-generated
// `panic("not implemented")` stubs gqlgen produced for the trash
// mutations and queries. The stubs themselves are deleted by hand the
// first time the generated file is regenerated, because gqlgen only
// overwrites its own files; any method already implemented in a
// separate *.resolvers.go is left untouched.
//
// The generated schema.resolvers.go still contains the stub signatures;
// we override them here. To prevent a conflict, trash.resolvers.go
// defines them on *mutationResolver / *queryResolver directly — Go's
// method-set rules ensure each method is declared exactly once across
// the package; the old stub bodies in schema.resolvers.go have been
// removed as part of shipping this file.

// trashEnabled is the one-stop check for the feature being on-and-wired.
// Every trash resolver calls it first; if false, we return a stable
// error the UI handles gracefully (fallback to hard-delete flow).
func (r *Resolver) trashEnabled() bool {
	return r.TrashStore != nil &&
		r.Config != nil && r.Config.Trash.Enabled
}

// currentUserID pulls claims.UserID from the request context. Returns
// empty string if unauthenticated — callers enforce auth themselves
// via the route-level middleware, but we also guard here defensively.
func currentUserID(ctx context.Context) string {
	claims := auth.GetClaims(ctx)
	if claims == nil {
		return ""
	}
	return claims.UserID
}

// currentUserIsAdmin reports whether the caller has admin role. Used
// to gate emptyTrash (always admin) and cross-user permanentlyDelete.
func currentUserIsAdmin(ctx context.Context) bool {
	claims := auth.GetClaims(ctx)
	if claims == nil {
		return false
	}
	return claims.Role == "admin"
}

// mapTrashableType converts the GraphQL enum to the internal trash
// package enum.
func mapTrashableType(t TrashableType) (trash.TrashableType, error) {
	switch t {
	case TrashableTypeRequirement:
		return trash.TypeRequirement, nil
	case TrashableTypeRequirementLink:
		return trash.TypeRequirementLink, nil
	case TrashableTypeKnowledgeArtifact:
		return trash.TypeKnowledgeArtifact, nil
	default:
		return "", fmt.Errorf("unknown trashable type: %s", t)
	}
}

// unmapTrashableType is the inverse — used when building a GraphQL
// response from a trash.Entry.
func unmapTrashableType(t trash.TrashableType) TrashableType {
	switch t {
	case trash.TypeRequirementLink:
		return TrashableTypeRequirementLink
	case trash.TypeKnowledgeArtifact:
		return TrashableTypeKnowledgeArtifact
	default:
		return TrashableTypeRequirement
	}
}

// toGraphQLEntry renders a trash.Entry as its GraphQL payload.
func (r *Resolver) toGraphQLEntry(e trash.Entry) *TrashEntry {
	expiresAt := e.DeletedAt.Add(r.retentionWindow())
	repoID := e.RepositoryID
	entry := &TrashEntry{
		ID:              e.ID,
		Type:            unmapTrashableType(e.Type),
		RepositoryID:    &repoID,
		Label:           e.Label,
		DeletedAt:       e.DeletedAt.Format(time.RFC3339),
		ExpiresAt:       expiresAt.Format(time.RFC3339),
		CanRestore:      e.CanRestore,
		TrashBatchID:    e.TrashBatchID,
	}
	if e.OriginalKey != "" {
		key := e.OriginalKey
		entry.OriginalKey = &key
	}
	if e.DeletedBy != "" {
		by := e.DeletedBy
		entry.DeletedBy = &by
	}
	if e.RestoreConflict != "" {
		rc := e.RestoreConflict
		entry.RestoreConflict = &rc
	}
	return entry
}

// retentionWindow returns the configured retention as a duration.
// Defaults to 30 days if the field is zero.
func (r *Resolver) retentionWindow() time.Duration {
	days := 30
	if r.Config != nil && r.Config.Trash.RetentionDays > 0 {
		days = r.Config.Trash.RetentionDays
	}
	return time.Duration(days) * 24 * time.Hour
}

// --- Query: trashRetentionDays ----------------------------------------

func (r *Resolver) trashRetentionDays() int {
	if r.Config == nil || r.Config.Trash.RetentionDays <= 0 {
		return 30
	}
	return r.Config.Trash.RetentionDays
}

// --- Query: trashedItems ----------------------------------------------

func (r *Resolver) trashedItems(
	ctx context.Context,
	repositoryID *string,
	types []TrashableType,
	limit *int,
	offset *int,
	search *string,
) (*TrashConnection, error) {
	if !r.trashEnabled() {
		return &TrashConnection{Nodes: nil, TotalCount: 0, RetentionDays: r.trashRetentionDays()}, nil
	}
	filter := trash.ListFilter{}
	if repositoryID != nil {
		filter.RepositoryID = *repositoryID
	}
	for _, t := range types {
		mapped, err := mapTrashableType(t)
		if err != nil {
			return nil, err
		}
		filter.Types = append(filter.Types, mapped)
	}
	if limit != nil {
		filter.Limit = *limit
	}
	if offset != nil {
		filter.Offset = *offset
	}
	if search != nil {
		filter.Search = *search
	}

	entries, total, err := r.TrashStore.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	nodes := make([]*TrashEntry, 0, len(entries))
	for _, e := range entries {
		nodes = append(nodes, r.toGraphQLEntry(e))
	}
	return &TrashConnection{
		Nodes:         nodes,
		TotalCount:    total,
		RetentionDays: r.trashRetentionDays(),
	}, nil
}

// --- Mutation: moveToTrash --------------------------------------------

func (r *Resolver) moveToTrash(ctx context.Context, t TrashableType, id string, reason *string) (*TrashEntry, error) {
	if !r.trashEnabled() {
		return nil, errors.New("trash is not enabled on this server")
	}
	mapped, err := mapTrashableType(t)
	if err != nil {
		return nil, err
	}
	opts := trash.MoveOptions{UserID: currentUserID(ctx)}
	if reason != nil {
		opts.Reason = *reason
	}
	entry, err := r.TrashStore.MoveToTrash(ctx, mapped, id, opts)
	if err != nil {
		return nil, err
	}
	trash.MovesTotal.Add(1)
	r.publishEvent(events.EventTrashChanged, map[string]interface{}{
		"action":         "moved",
		"type":           string(mapped),
		"id":             id,
		"repository_id":  entry.RepositoryID,
		"trash_batch_id": entry.TrashBatchID,
	})
	r.publishTrashCountChanged(entry.RepositoryID)
	return r.toGraphQLEntry(entry), nil
}

// --- Mutation: restoreFromTrash ---------------------------------------

func (r *Resolver) restoreFromTrash(
	ctx context.Context,
	t TrashableType,
	id string,
	resolveConflict *RestoreConflictResolution,
	rename *string,
) (*RestoreResult, error) {
	if !r.trashEnabled() {
		return nil, errors.New("trash is not enabled on this server")
	}
	mapped, err := mapTrashableType(t)
	if err != nil {
		return nil, err
	}
	opts := trash.RestoreOptions{UserID: currentUserID(ctx), Resolve: trash.RestoreCancel}
	if resolveConflict != nil && *resolveConflict == RestoreConflictResolutionRename {
		opts.Resolve = trash.RestoreRename
	}
	if rename != nil {
		opts.NewKey = *rename
	}

	result, err := r.TrashStore.RestoreFromTrash(ctx, mapped, id, opts)
	if err != nil {
		var conflict *trash.ConflictError
		if errors.As(err, &conflict) {
			trash.ConflictsTotal.Add(1)
		}
		return nil, err
	}
	trash.RestoresTotal.Add(1)
	if result.Renamed {
		trash.ConflictsTotal.Add(1) // RENAME implies a conflict was resolved
	}
	r.publishEvent(events.EventTrashChanged, map[string]interface{}{
		"action": "restored",
		"type":   string(mapped),
		"id":     id,
	})
	// Count event is best-effort — we don't know repo without a lookup;
	// callers refresh the count view on any trash_changed.
	return &RestoreResult{
		RestoredID: result.RestoredID,
		BatchSize:  result.BatchSize,
		Renamed:    result.Renamed,
		NewKey: func() *string {
			if result.NewKey == "" {
				return nil
			}
			v := result.NewKey
			return &v
		}(),
	}, nil
}

// --- Mutation: permanentlyDelete --------------------------------------

func (r *Resolver) permanentlyDelete(ctx context.Context, t TrashableType, id string) (bool, error) {
	if !r.trashEnabled() {
		return false, errors.New("trash is not enabled on this server")
	}
	mapped, err := mapTrashableType(t)
	if err != nil {
		return false, err
	}
	caller := currentUserID(ctx)
	if caller == "" {
		return false, errors.New("unauthenticated")
	}

	// Ownership gate. A user may permanentlyDelete their own trash
	// entries (deletedBy == caller). Admins may purge anyone's.
	// Neither path reaches this code without authentication above.
	entry, err := r.TrashStore.Get(ctx, mapped, id)
	if err != nil {
		return false, err
	}
	if entry == nil {
		// Either the row is live or doesn't exist. Either way, refuse
		// to proceed — permanentlyDelete is a destructive op and
		// callers need a clear signal that the target isn't in trash.
		return false, fmt.Errorf("%s %s is not in trash", mapped, id)
	}
	if entry.DeletedBy != caller && !currentUserIsAdmin(ctx) {
		return false, fmt.Errorf("permanentlyDelete: only the user who moved an item to trash or an admin may purge it (trashed by %q, caller %q)", entry.DeletedBy, caller)
	}

	if err := r.TrashStore.PermanentlyDelete(ctx, mapped, id); err != nil {
		return false, err
	}
	trash.PermanentDeletesTotal.Add(1)
	r.publishEvent(events.EventTrashChanged, map[string]interface{}{
		"action": "purged",
		"type":   string(mapped),
		"id":     id,
	})
	return true, nil
}

// --- Mutation: emptyTrash ---------------------------------------------

func (r *Resolver) emptyTrash(ctx context.Context, repositoryID string, olderThanDays *int) (int, error) {
	if !r.trashEnabled() {
		return 0, errors.New("trash is not enabled on this server")
	}
	if !currentUserIsAdmin(ctx) {
		return 0, errors.New("emptyTrash requires admin role")
	}
	cutoff := 0
	if olderThanDays != nil {
		cutoff = *olderThanDays
	}
	// List matching items, then permanently delete them. This is a
	// naive first implementation; a future refinement pushes the
	// filter into the store directly for fewer round-trips.
	filter := trash.ListFilter{RepositoryID: repositoryID, Limit: 10_000}
	entries, _, err := r.TrashStore.List(ctx, filter)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	purged := 0
	for _, e := range entries {
		if cutoff > 0 {
			age := now.Sub(e.DeletedAt)
			if age < time.Duration(cutoff)*24*time.Hour {
				continue
			}
		}
		if err := r.TrashStore.PermanentlyDelete(ctx, e.Type, e.ID); err != nil {
			// Best-effort: log and continue.
			continue
		}
		purged++
	}
	if purged > 0 {
		r.publishEvent(events.EventTrashBulkChanged, map[string]interface{}{
			"action":        "purged",
			"repository_id": repositoryID,
			"count":         purged,
		})
		r.publishTrashCountChanged(repositoryID)
	}
	return purged, nil
}

// publishTrashCountChanged emits the per-repo badge event. Callers
// that don't know the affected repo ID pass empty string and the
// event is skipped — clients refresh their count view lazily in that
// case.
func (r *Resolver) publishTrashCountChanged(repoID string) {
	if repoID == "" {
		return
	}
	r.publishEvent(events.EventTrashCountChanged, map[string]interface{}{
		"repository_id": repoID,
	})
}
