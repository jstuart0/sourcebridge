// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package trash

// The memory store is the canonical reference implementation of the
// soft-delete recycle bin. It is the test target for every behaviour
// covered by Phase 1 of the plan: tombstone-key rewrite, cascade via
// trash_batch_id, conflict-aware restore, retention sweep. The
// SurrealDB store (surrealstore.go) must match this store's observable
// behaviour row-for-row; matching tests run against both.
//
// The store maintains its own entity tables in-memory. It does not
// plug into the live graph / knowledge stores — callers wire it up in
// tests only. Production uses the SurrealDB store.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// RegisterOptions describes an entity the memory store should pretend
// to own. Tests register a row before they can MoveToTrash it.
type RegisterOptions struct {
	Type         TrashableType
	ID           string
	RepositoryID string
	// NaturalKey is the value in the column subject to tombstone-key
	// rewrite (external_id for requirements, scope_key for artifacts).
	// Empty for types without a natural key (links).
	NaturalKey string
	// Label is the human-readable description stored on the trash
	// entry when the row is later moved.
	Label string
	// Parent links cascade children together; when a parent is moved,
	// every row whose ParentID matches its ID is cascaded too.
	ParentID string
}

// memEntity is the in-memory row tracked by the memory store.
type memEntity struct {
	Type         TrashableType
	ID           string
	RepositoryID string
	// Key is the *current* natural-key value (post-rewrite when
	// trashed, original when active).
	Key string
	// OriginalKey is the pre-rewrite value; populated only while
	// trashed. Persists so restore can reverse the rewrite.
	OriginalKey string
	Label       string
	ParentID    string

	DeletedAt     *time.Time
	DeletedBy     string
	DeletedReason string
	RestoredAt    *time.Time
	RestoredBy    string
	TrashBatchID  string
}

// MemStore is the memory implementation of trash.Store.
type MemStore struct {
	mu       sync.RWMutex
	entities map[entityKey]*memEntity
	now      func() time.Time
}

type entityKey struct {
	Type TrashableType
	ID   string
}

// NewMemStore returns an empty memory store.
func NewMemStore() *MemStore {
	return &MemStore{
		entities: make(map[entityKey]*memEntity),
		now:      time.Now,
	}
}

// SetClock replaces the store's clock (tests only). Never call from
// production code.
func (s *MemStore) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// Register inserts a row the store should track. Tests only.
func (s *MemStore) Register(opts RegisterOptions) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := entityKey{Type: opts.Type, ID: opts.ID}
	s.entities[k] = &memEntity{
		Type:         opts.Type,
		ID:           opts.ID,
		RepositoryID: opts.RepositoryID,
		Key:          opts.NaturalKey,
		Label:        opts.Label,
		ParentID:     opts.ParentID,
	}
}

// IsTrashed reports whether the entity of the given type+id is
// currently tombstoned. Tests only.
func (s *MemStore) IsTrashed(t TrashableType, id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entities[entityKey{Type: t, ID: id}]
	return ok && e.DeletedAt != nil
}

// LookupKey returns the *current* natural-key value for an entity.
// Tests use this to assert that tombstone-key rewrites land correctly
// and that restore reverses them.
func (s *MemStore) LookupKey(t TrashableType, id string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entities[entityKey{Type: t, ID: id}]
	if !ok {
		return "", false
	}
	return e.Key, true
}

// MoveToTrash marks the entity and all its children tombstoned.
func (s *MemStore) MoveToTrash(_ context.Context, t TrashableType, id string, opts MoveOptions) (Entry, error) {
	if !t.Valid() {
		return Entry{}, fmt.Errorf("invalid trashable type %q", t)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entities[entityKey{Type: t, ID: id}]
	if !ok {
		return Entry{}, fmt.Errorf("%s %s not found", t, id)
	}
	if e.DeletedAt != nil {
		return Entry{}, fmt.Errorf("%s %s already in trash", t, id)
	}

	now := s.now()
	batchID := uuid.NewString()
	s.tombstone(e, now, opts.UserID, opts.Reason, batchID)

	// Cascade: children of this entity, and children of any entity
	// recursively linked via ParentID. We walk once per pass because
	// in practice cascades are one level deep (artifact → sections,
	// sections → evidence).
	s.cascadeFrom(id, now, opts.UserID, opts.Reason, batchID)

	return s.toEntry(e), nil
}

// cascadeFrom walks ParentID links. Safe under lock.
func (s *MemStore) cascadeFrom(parentID string, now time.Time, user, reason, batchID string) {
	// Iterate stable: collect then mutate.
	var queue []*memEntity
	for _, candidate := range s.entities {
		if candidate.ParentID == parentID && candidate.DeletedAt == nil {
			queue = append(queue, candidate)
		}
	}
	for _, child := range queue {
		s.tombstone(child, now, user, reason, batchID)
		s.cascadeFrom(child.ID, now, user, reason, batchID)
	}
}

// tombstone is the shared rewrite used by MoveToTrash and its cascade.
// Callers hold the lock.
func (s *MemStore) tombstone(e *memEntity, now time.Time, user, reason, batchID string) {
	e.DeletedAt = timePtr(now)
	e.DeletedBy = user
	e.DeletedReason = reason
	e.TrashBatchID = batchID
	e.RestoredAt = nil
	e.RestoredBy = ""
	// Tombstone-key rewrite: only for types with a natural key.
	if e.Key != "" {
		e.OriginalKey = e.Key
		e.Key = e.Key + TombstoneKeyPrefix + uuid.NewString()[:8]
	}
}

// RestoreFromTrash reverses a moveToTrash (and its cascade). If a
// natural-key conflict exists and the caller did not opt in to RENAME,
// returns a *ConflictError.
func (s *MemStore) RestoreFromTrash(_ context.Context, t TrashableType, id string, opts RestoreOptions) (RestoreResult, error) {
	if !t.Valid() {
		return RestoreResult{}, fmt.Errorf("invalid trashable type %q", t)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entities[entityKey{Type: t, ID: id}]
	if !ok {
		return RestoreResult{}, fmt.Errorf("%s %s not found", t, id)
	}
	if e.DeletedAt == nil {
		return RestoreResult{}, fmt.Errorf("%s %s is not in trash", t, id)
	}

	desiredKey := e.OriginalKey
	renamed := false
	if e.OriginalKey != "" {
		// Conflict check: look for any non-trashed entity of the same
		// type with the desired key in the same repo.
		if s.keyIsTaken(e.Type, e.RepositoryID, desiredKey, e.ID) {
			switch opts.Resolve {
			case RestoreRename:
				if strings.TrimSpace(opts.NewKey) == "" {
					return RestoreResult{}, errors.New("RestoreRename requires NewKey")
				}
				if s.keyIsTaken(e.Type, e.RepositoryID, opts.NewKey, e.ID) {
					return RestoreResult{}, &ConflictError{
						TrashEntryID: id,
						OriginalKey:  e.OriginalKey,
						Reason:       fmt.Sprintf("new key %q is also taken", opts.NewKey),
					}
				}
				desiredKey = opts.NewKey
				renamed = true
			default:
				return RestoreResult{}, &ConflictError{
					TrashEntryID: id,
					OriginalKey:  e.OriginalKey,
					Reason:       fmt.Sprintf("natural key %q is already taken", e.OriginalKey),
				}
			}
		}
	}

	// Restore this entity plus any cascade-linked children.
	now := s.now()
	batchRows := s.entitiesInBatch(e.TrashBatchID)
	for _, row := range batchRows {
		if row.ID == e.ID {
			// Apply the (possibly renamed) key to the parent.
			row.Key = desiredKey
		} else if row.OriginalKey != "" {
			row.Key = row.OriginalKey
		}
		row.OriginalKey = ""
		row.DeletedAt = nil
		row.DeletedReason = ""
		row.TrashBatchID = ""
		row.RestoredAt = timePtr(now)
		row.RestoredBy = opts.UserID
	}

	return RestoreResult{
		RestoredID: e.ID,
		BatchSize:  len(batchRows),
		Renamed:    renamed,
		NewKey: func() string {
			if renamed {
				return desiredKey
			}
			return ""
		}(),
	}, nil
}

func (s *MemStore) keyIsTaken(t TrashableType, repoID, key, excludeID string) bool {
	for _, candidate := range s.entities {
		if candidate.Type != t || candidate.RepositoryID != repoID {
			continue
		}
		if candidate.ID == excludeID {
			continue
		}
		if candidate.DeletedAt != nil {
			continue
		}
		if candidate.Key == key {
			return true
		}
	}
	return false
}

func (s *MemStore) entitiesInBatch(batchID string) []*memEntity {
	var rows []*memEntity
	for _, e := range s.entities {
		if e.TrashBatchID == batchID {
			rows = append(rows, e)
		}
	}
	return rows
}

// PermanentlyDelete removes the entity from the store entirely.
func (s *MemStore) PermanentlyDelete(_ context.Context, t TrashableType, id string) error {
	if !t.Valid() {
		return fmt.Errorf("invalid trashable type %q", t)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := entityKey{Type: t, ID: id}
	e, ok := s.entities[k]
	if !ok {
		return fmt.Errorf("%s %s not found", t, id)
	}
	if e.DeletedAt == nil {
		return fmt.Errorf("%s %s is not in trash; refuse to permanently delete live row", t, id)
	}
	delete(s.entities, k)
	return nil
}

// Get returns the Entry for a trashed row, or (nil, nil) when the row
// isn't in the trash. Returns an error only for invalid input.
func (s *MemStore) Get(_ context.Context, t TrashableType, id string) (*Entry, error) {
	if !t.Valid() {
		return nil, fmt.Errorf("invalid trashable type %q", t)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entities[entityKey{Type: t, ID: id}]
	if !ok || e.DeletedAt == nil {
		return nil, nil
	}
	entry := s.toEntry(e)
	return &entry, nil
}

// List returns trashed entries matching the filter.
func (s *MemStore) List(_ context.Context, filter ListFilter) ([]Entry, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	typeSet := map[TrashableType]bool{}
	for _, t := range filter.Types {
		typeSet[t] = true
	}

	var entries []Entry
	for _, e := range s.entities {
		if e.DeletedAt == nil {
			continue
		}
		if filter.RepositoryID != "" && e.RepositoryID != filter.RepositoryID {
			continue
		}
		if len(typeSet) > 0 && !typeSet[e.Type] {
			continue
		}
		if filter.Search != "" && !matchesSearch(e, filter.Search) {
			continue
		}
		entries = append(entries, s.toEntry(e))
	}
	total := len(entries)

	// Most-recent first.
	sortByDeletedAtDesc(entries)

	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if offset >= len(entries) {
		return nil, total, nil
	}
	end := offset + limit
	if end > len(entries) {
		end = len(entries)
	}
	return entries[offset:end], total, nil
}

func matchesSearch(e *memEntity, q string) bool {
	needle := strings.ToLower(q)
	return strings.Contains(strings.ToLower(e.Label), needle) ||
		strings.Contains(strings.ToLower(e.OriginalKey), needle) ||
		strings.Contains(strings.ToLower(e.ID), needle)
}

// SweepExpired hard-deletes every tombstoned row older than retention.
// Returns the count of rows purged. Idempotent.
func (s *MemStore) SweepExpired(_ context.Context, retention time.Duration, maxBatch int) (int, error) {
	if retention <= 0 {
		return 0, errors.New("retention must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	threshold := now.Add(-retention)

	toRemove := make([]entityKey, 0)
	for k, e := range s.entities {
		if e.DeletedAt == nil {
			continue
		}
		if e.DeletedAt.After(threshold) {
			continue
		}
		toRemove = append(toRemove, k)
		if maxBatch > 0 && len(toRemove) >= maxBatch {
			break
		}
	}
	for _, k := range toRemove {
		delete(s.entities, k)
	}
	return len(toRemove), nil
}

func (s *MemStore) toEntry(e *memEntity) Entry {
	entry := Entry{
		ID:            e.ID,
		Type:          e.Type,
		RepositoryID:  e.RepositoryID,
		Label:         e.Label,
		OriginalKey:   e.OriginalKey,
		DeletedBy:     e.DeletedBy,
		DeletedReason: e.DeletedReason,
		TrashBatchID:  e.TrashBatchID,
	}
	if e.DeletedAt != nil {
		entry.DeletedAt = *e.DeletedAt
	}
	// Advisory canRestore: check if the original natural key is free.
	// Always true for types without a natural key.
	entry.CanRestore = true
	if e.OriginalKey != "" && s.keyIsTaken(e.Type, e.RepositoryID, e.OriginalKey, e.ID) {
		entry.CanRestore = false
		entry.RestoreConflict = fmt.Sprintf("%q is now in use by another entity", e.OriginalKey)
	}
	return entry
}

func timePtr(t time.Time) *time.Time { return &t }

func sortByDeletedAtDesc(entries []Entry) {
	// Tiny stable sort — avoids importing sort just for this.
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].DeletedAt.After(entries[j-1].DeletedAt); j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}
