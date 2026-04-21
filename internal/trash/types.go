// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package trash implements the soft-delete recycle bin feature.
//
// Every user-editable entity (requirements, requirement links, knowledge
// artifacts) can be moved to trash with moveToTrash. Trashed rows are
// hidden from every normal read path by a deleted_at filter and remain
// recoverable with restoreFromTrash until a retention window lapses,
// after which a background worker purges them permanently.
//
// The feature relies on a tombstone-key rewrite strategy for entities
// whose natural keys (external_id on requirements, scope_key on
// artifacts) have a uniqueness constraint. On move-to-trash the natural
// key is rewritten to `<original>::trashed::<uuid>` so a new row with
// the same key can coexist. Restore reverses the rewrite and prompts
// the user to rename when the original key is now taken.
//
// See thoughts/shared/plans/2026-04-20-soft-delete-recycle-bin.md for
// the full design.
package trash

import (
	"context"
	"time"
)

// TrashableType names an entity that can enter the recycle bin.
// Values map 1:1 to the GraphQL enum of the same name.
type TrashableType string

const (
	TypeRequirement       TrashableType = "requirement"
	TypeRequirementLink   TrashableType = "requirement_link"
	TypeKnowledgeArtifact TrashableType = "knowledge_artifact"
)

// Valid reports whether t is a known trashable type.
func (t TrashableType) Valid() bool {
	switch t {
	case TypeRequirement, TypeRequirementLink, TypeKnowledgeArtifact:
		return true
	default:
		return false
	}
}

// TombstoneKeyPrefix is the marker sentinel appended to natural-key
// columns when a row is tombstoned. Keep stable — the unwind script
// and the restore path both key off it. Never change without a
// migration.
const TombstoneKeyPrefix = "::trashed::"

// Entry is the canonical trash row returned by the store. It pairs
// the persisted soft-delete bookkeeping with a human-readable label
// suitable for UI rendering, plus an advisory canRestore flag the UI
// uses to pre-warn about conflicts.
type Entry struct {
	// ID is the underlying row's native ID (the requirement id, the
	// link id, etc.). NOT a trash-specific id — restoreFromTrash
	// uses this directly.
	ID           string        `json:"id"`
	Type         TrashableType `json:"type"`
	RepositoryID string        `json:"repository_id"`

	// Label is a short, user-facing description assembled by the
	// store. For requirements this is "<external_id> — <title>", for
	// links "<external_id> → <symbol_name>", etc. Pre-computed so the
	// UI never has to fetch the underlying row.
	Label string `json:"label"`

	// OriginalKey is the natural-key value before tombstone rewriting
	// (external_id for requirements, scope_key for artifacts). Empty
	// for types without a tombstone-key rewrite (links).
	OriginalKey string `json:"original_key,omitempty"`

	DeletedAt     time.Time `json:"deleted_at"`
	DeletedBy     string    `json:"deleted_by,omitempty"`
	DeletedReason string    `json:"deleted_reason,omitempty"`
	ExpiresAt     time.Time `json:"expires_at"`
	TrashBatchID  string    `json:"trash_batch_id"`

	// CanRestore is ADVISORY. The authoritative check happens at
	// restore-time; another user may take the natural key between
	// list and restore invocations.
	CanRestore      bool   `json:"can_restore"`
	RestoreConflict string `json:"restore_conflict,omitempty"`
}

// ListFilter scopes the trashedItems query.
type ListFilter struct {
	RepositoryID string
	Types        []TrashableType
	Search       string // substring match against Label / OriginalKey
	Limit        int    // 0 = server default (50)
	Offset       int
}

// MoveOptions configures a moveToTrash call.
type MoveOptions struct {
	UserID string // records as deleted_by
	Reason string // optional; stored in deleted_reason for future audit use
}

// RestoreConflictResolution tells the store how to handle a
// natural-key collision at restore time.
type RestoreConflictResolution string

const (
	// RestoreCancel aborts restore when the original key is taken;
	// the caller must surface the conflict to the user.
	RestoreCancel RestoreConflictResolution = "CANCEL"
	// RestoreRename restores under a new caller-provided natural key.
	// Only valid for types that have a natural key (requirements,
	// artifacts); ignored for links.
	RestoreRename RestoreConflictResolution = "RENAME"
)

// RestoreOptions drives a restoreFromTrash call.
type RestoreOptions struct {
	UserID string
	// Resolve is how to handle a natural-key conflict. Default is
	// RestoreCancel.
	Resolve RestoreConflictResolution
	// NewKey is required when Resolve == RestoreRename; ignored
	// otherwise.
	NewKey string
}

// RestoreResult reports the outcome of a successful restore.
type RestoreResult struct {
	RestoredID string
	BatchSize  int    // rows un-tombstoned, including cascade children
	Renamed    bool   // true when RestoreRename was used
	NewKey     string // present when Renamed=true
}

// ConflictError is returned by RestoreFromTrash when the original
// natural key is taken and the caller didn't opt in to rename.
type ConflictError struct {
	TrashEntryID string
	OriginalKey  string
	Reason       string
}

func (e *ConflictError) Error() string { return e.Reason }

// Store is the persistence interface for the recycle bin. Backed by a
// memory store in tests and by SurrealDB in production.
type Store interface {
	MoveToTrash(ctx context.Context, t TrashableType, id string, opts MoveOptions) (Entry, error)
	RestoreFromTrash(ctx context.Context, t TrashableType, id string, opts RestoreOptions) (RestoreResult, error)
	PermanentlyDelete(ctx context.Context, t TrashableType, id string) error
	// Get returns the trash entry for a tombstoned row, or (nil, nil) when
	// the row is not in the trash. Used by the permanentlyDelete resolver
	// to enforce ownership before destroying data.
	Get(ctx context.Context, t TrashableType, id string) (*Entry, error)
	List(ctx context.Context, filter ListFilter) ([]Entry, int, error)
	SweepExpired(ctx context.Context, retention time.Duration, maxBatch int) (int, error)
}
