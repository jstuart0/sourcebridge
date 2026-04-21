// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package trash

// SurrealStore is the production implementation of Store. It mutates
// the soft-delete bookkeeping columns (deleted_at, trash_batch_id,
// original_*, etc.) added by migration 031 directly, via the existing
// db.SurrealDB handle.
//
// The store is deliberately thin. Tombstone-key rewrite happens in
// Go — SurrealDB updates set the new value; restore reverses it. All
// cascade operations share a trash_batch_id and run inside a
// SurrealDB transaction; a post-commit reconciler (future work) keeps
// partial cascades honest if transactions flake. For now the
// transaction is load-bearing.
//
// The store must match memstore.go's observable behaviour exactly —
// tests target the trash.Store interface and will run against both
// implementations.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"
	"github.com/surrealdb/surrealdb.go"
	"github.com/surrealdb/surrealdb.go/pkg/models"

	"github.com/sourcebridge/sourcebridge/internal/db"
)

// surrealTrashRow is the typed projection of a trashable row across
// any of the three Phase-1 tables. Fields not relevant to a given
// type simply stay zero. Using a typed struct over map[string]any
// avoids the landmine of SurrealDB returning RecordID and datetime
// values as SDK types that don't assert to string / time.Time
// cleanly via the map path.
type surrealTrashRow struct {
	ID            *models.RecordID `json:"id,omitempty" cbor:"id,omitempty"`
	RepoID        string           `json:"repo_id,omitempty" cbor:"repo_id,omitempty"`
	DeletedAt     *surrealTime     `json:"deleted_at,omitempty" cbor:"deleted_at,omitempty"`
	DeletedBy     string           `json:"deleted_by,omitempty" cbor:"deleted_by,omitempty"`
	DeletedReason string           `json:"deleted_reason,omitempty" cbor:"deleted_reason,omitempty"`
	TrashBatchID  string           `json:"trash_batch_id,omitempty" cbor:"trash_batch_id,omitempty"`

	// Requirement-only.
	ExternalID           string `json:"external_id,omitempty" cbor:"external_id,omitempty"`
	OriginalExternalID   string `json:"original_external_id,omitempty" cbor:"original_external_id,omitempty"`
	Title                string `json:"title,omitempty" cbor:"title,omitempty"`

	// Link-only.
	RequirementID string `json:"requirement_id,omitempty" cbor:"requirement_id,omitempty"`
	SymbolID      string `json:"symbol_id,omitempty" cbor:"symbol_id,omitempty"`

	// Knowledge-artifact-only.
	ArtifactType     string `json:"type,omitempty" cbor:"type,omitempty"`
	ScopeKey         string `json:"scope_key,omitempty" cbor:"scope_key,omitempty"`
	OriginalScopeKey string `json:"original_scope_key,omitempty" cbor:"original_scope_key,omitempty"`
}

// surrealTime mirrors db.surrealTime but is package-local so we don't
// have to export it. CBOR unmarshal handles both the SurrealDB native
// datetime (CBOR tag 12) and legacy string values.
type surrealTime struct {
	time.Time
}

func (st *surrealTime) UnmarshalCBOR(data []byte) error {
	var dt models.CustomDateTime
	if err := dt.UnmarshalCBOR(data); err == nil {
		st.Time = dt.Time
		return nil
	}
	var s string
	if err := cbor.Unmarshal(data, &s); err == nil && s != "" {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			st.Time = t
			return nil
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			st.Time = t
			return nil
		}
	}
	st.Time = time.Time{}
	return nil
}

// recordIDString extracts the id portion of a SurrealDB record id,
// returning "" for nil.
func recordIDString(rid *models.RecordID) string {
	if rid == nil {
		return ""
	}
	return fmt.Sprintf("%v", rid.ID)
}

// SurrealStore persists trash state via SurrealDB.
type SurrealStore struct {
	sur *db.SurrealDB
}

// NewSurrealStore constructs the store. The caller owns sur's lifecycle.
func NewSurrealStore(sur *db.SurrealDB) *SurrealStore {
	return &SurrealStore{sur: sur}
}

// --- helpers -----------------------------------------------------------

// tableFor maps a TrashableType to its SurrealDB table name. The
// mapping is intentionally hard-coded; new types must be added here
// and the migration must match.
func tableFor(t TrashableType) (string, error) {
	switch t {
	case TypeRequirement:
		return "ca_requirement", nil
	case TypeRequirementLink:
		return "ca_link", nil
	case TypeKnowledgeArtifact:
		return "ca_knowledge_artifact", nil
	default:
		return "", fmt.Errorf("unknown trashable type %q", t)
	}
}

// naturalKeyColumnFor returns the column subject to tombstone-key
// rewrite for the given type. Empty if the type has no natural key
// (links).
func naturalKeyColumnFor(t TrashableType) (col, originalCol string) {
	switch t {
	case TypeRequirement:
		return "external_id", "original_external_id"
	case TypeKnowledgeArtifact:
		return "scope_key", "original_scope_key"
	default:
		return "", ""
	}
}

// fetchRow loads a single typed row. Returns (nil, nil) on miss.
func (s *SurrealStore) fetchRow(ctx context.Context, t TrashableType, id string) (*surrealTrashRow, error) {
	table, err := tableFor(t)
	if err != nil {
		return nil, err
	}
	d := s.sur.DB()
	if d == nil {
		return nil, errors.New("database not connected")
	}
	rows, err := surrealdb.Query[[]surrealTrashRow](ctx, d,
		fmt.Sprintf("SELECT * FROM type::thing('%s', $id)", table),
		map[string]any{"id": id})
	if err != nil {
		return nil, fmt.Errorf("fetch row: %w", err)
	}
	if rows == nil || len(*rows) == 0 {
		return nil, nil
	}
	first := (*rows)[0]
	if len(first.Result) == 0 {
		return nil, nil
	}
	row := first.Result[0]
	return &row, nil
}

// describeRow derives the user-facing label and natural key for a
// typed trash row.
func describeRow(t TrashableType, row *surrealTrashRow) (label, naturalKey string) {
	switch t {
	case TypeRequirement:
		extID := row.ExternalID
		if row.OriginalExternalID != "" {
			extID = row.OriginalExternalID
		}
		label = strings.TrimSpace(extID + " — " + row.Title)
		naturalKey = extID
	case TypeRequirementLink:
		label = row.RequirementID + " → " + row.SymbolID
	case TypeKnowledgeArtifact:
		scope := row.ScopeKey
		if row.OriginalScopeKey != "" {
			scope = row.OriginalScopeKey
		}
		label = row.ArtifactType + " · " + scope
		naturalKey = scope
	}
	return
}

// --- MoveToTrash --------------------------------------------------------

// MoveToTrash marks the row and any cascade children tombstoned in a
// single SurrealDB transaction. Children are found per type (artifact
// → sections → evidence chains).
func (s *SurrealStore) MoveToTrash(ctx context.Context, t TrashableType, id string, opts MoveOptions) (Entry, error) {
	if !t.Valid() {
		return Entry{}, fmt.Errorf("invalid trashable type %q", t)
	}
	table, err := tableFor(t)
	if err != nil {
		return Entry{}, err
	}
	row, err := s.fetchRow(ctx, t, id)
	if err != nil {
		return Entry{}, err
	}
	if row == nil {
		return Entry{}, fmt.Errorf("%s %s not found", t, id)
	}
	if row.DeletedAt != nil && !row.DeletedAt.IsZero() {
		return Entry{}, fmt.Errorf("%s %s already in trash", t, id)
	}

	batchID := uuid.NewString()
	now := time.Now().UTC()

	// Build the update statement per type. Natural-key types also get
	// a tombstone-key rewrite.
	natCol, natOrigCol := naturalKeyColumnFor(t)
	sets := []string{
		"deleted_at = $now",
		"deleted_by = $user",
		"deleted_reason = $reason",
		"restored_at = NONE",
		"restored_by = NONE",
		"trash_batch_id = $batch_id",
	}
	params := map[string]any{
		"id":       id,
		"now":      now,
		"user":     opts.UserID,
		"reason":   opts.Reason,
		"batch_id": batchID,
	}
	if natCol != "" {
		var current string
		switch t {
		case TypeRequirement:
			current = row.ExternalID
		case TypeKnowledgeArtifact:
			current = row.ScopeKey
		}
		rewritten := current + TombstoneKeyPrefix + uuid.NewString()[:8]
		sets = append(sets,
			natCol+" = $rewritten_key",
			natOrigCol+" = $original_key",
		)
		params["rewritten_key"] = rewritten
		params["original_key"] = current
	}

	err = s.sur.RunInTx(ctx, func(ctx context.Context) error {
		// Tombstone the primary row.
		stmt := fmt.Sprintf(
			"UPDATE type::thing('%s', $id) SET %s",
			table, strings.Join(sets, ", "),
		)
		if _, qerr := s.sur.Query(ctx, stmt, params); qerr != nil {
			return fmt.Errorf("tombstone primary: %w", qerr)
		}
		// Cascade for artifacts. Requirements have their own link
		// cascade — deleting a requirement does NOT auto-delete its
		// links today (the plan says cascade follows ownership; we
		// opted against that for now since links are independently
		// meaningful and often survive their requirement's removal).
		if t == TypeKnowledgeArtifact {
			if err := s.cascadeArtifactChildren(ctx, id, batchID, now, opts); err != nil {
				return fmt.Errorf("cascade children: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return Entry{}, err
	}

	// Re-read the row so the returned Entry reflects what's persisted.
	updatedRow, err := s.fetchRow(ctx, t, id)
	if err != nil || updatedRow == nil {
		return Entry{}, fmt.Errorf("reload after move: %w", err)
	}
	return s.entryFromRow(t, id, updatedRow), nil
}

// cascadeArtifactChildren tombstones every section + evidence +
// dependency + refinement row owned by the given artifact. Callers
// hold the transaction.
func (s *SurrealStore) cascadeArtifactChildren(ctx context.Context, artifactID, batchID string, now time.Time, opts MoveOptions) error {
	child := func(table, where string, params map[string]any) error {
		stmt := fmt.Sprintf(
			`UPDATE %s
			 SET deleted_at = $now, trash_batch_id = $batch_id
			 WHERE %s AND deleted_at IS NONE`,
			table, where,
		)
		if _, err := s.sur.Query(ctx, stmt, params); err != nil {
			return fmt.Errorf("cascade %s: %w", table, err)
		}
		return nil
	}
	common := map[string]any{
		"artifact_id": artifactID,
		"now":         now,
		"batch_id":    batchID,
	}
	if err := child("ca_knowledge_section", "artifact_id = $artifact_id", common); err != nil {
		return err
	}
	if err := child("ca_knowledge_dependency", "artifact_id = $artifact_id", common); err != nil {
		return err
	}
	if err := child("ca_knowledge_refinement", "artifact_id = $artifact_id", common); err != nil {
		return err
	}
	// Evidence is keyed by section_id. Query sections (trashed this
	// batch) to collect their ids, then tombstone evidence.
	sectionRows, err := surrealdb.Query[[]surrealTrashRow](ctx, s.sur.DB(),
		"SELECT id FROM ca_knowledge_section WHERE artifact_id = $artifact_id AND trash_batch_id = $batch_id",
		common)
	if err != nil {
		return fmt.Errorf("collect sections for evidence cascade: %w", err)
	}
	if sectionRows == nil || len(*sectionRows) == 0 {
		return nil
	}
	first := (*sectionRows)[0]
	if len(first.Result) == 0 {
		return nil
	}
	sectionIDs := make([]string, 0, len(first.Result))
	for i := range first.Result {
		id := recordIDString(first.Result[i].ID)
		if id != "" {
			sectionIDs = append(sectionIDs, id)
		}
	}
	if len(sectionIDs) == 0 {
		return nil
	}
	eviParams := map[string]any{
		"section_ids": sectionIDs,
		"now":         now,
		"batch_id":    batchID,
	}
	return child("ca_knowledge_evidence", "section_id IN $section_ids", eviParams)
}

// --- RestoreFromTrash --------------------------------------------------

// RestoreFromTrash reverses a move. If the natural key conflicts and
// the caller didn't opt in to rename, returns *ConflictError.
func (s *SurrealStore) RestoreFromTrash(ctx context.Context, t TrashableType, id string, opts RestoreOptions) (RestoreResult, error) {
	if !t.Valid() {
		return RestoreResult{}, fmt.Errorf("invalid trashable type %q", t)
	}
	table, err := tableFor(t)
	if err != nil {
		return RestoreResult{}, err
	}
	row, err := s.fetchRow(ctx, t, id)
	if err != nil {
		return RestoreResult{}, err
	}
	if row == nil {
		return RestoreResult{}, fmt.Errorf("%s %s not found", t, id)
	}
	if row.DeletedAt == nil || row.DeletedAt.IsZero() {
		return RestoreResult{}, fmt.Errorf("%s %s is not in trash", t, id)
	}
	batchID := row.TrashBatchID
	repoID := row.RepoID

	natCol, natOrigCol := naturalKeyColumnFor(t)
	desiredKey := ""
	renamed := false
	if natCol != "" {
		var original string
		switch t {
		case TypeRequirement:
			original = row.OriginalExternalID
		case TypeKnowledgeArtifact:
			original = row.OriginalScopeKey
		}
		desiredKey = original

		if s.keyIsTaken(ctx, t, repoID, desiredKey, id) {
			switch opts.Resolve {
			case RestoreRename:
				if strings.TrimSpace(opts.NewKey) == "" {
					return RestoreResult{}, errors.New("RestoreRename requires NewKey")
				}
				if s.keyIsTaken(ctx, t, repoID, opts.NewKey, id) {
					return RestoreResult{}, &ConflictError{
						TrashEntryID: id,
						OriginalKey:  original,
						Reason:       fmt.Sprintf("new key %q is also taken", opts.NewKey),
					}
				}
				desiredKey = opts.NewKey
				renamed = true
			default:
				return RestoreResult{}, &ConflictError{
					TrashEntryID: id,
					OriginalKey:  original,
					Reason:       fmt.Sprintf("natural key %q is already taken", original),
				}
			}
		}
	}

	now := time.Now().UTC()
	batchSize := 0
	err = s.sur.RunInTx(ctx, func(ctx context.Context) error {
		// Restore primary row, possibly with a renamed key.
		sets := []string{
			"deleted_at = NONE",
			"deleted_reason = NONE",
			"trash_batch_id = NONE",
			"restored_at = $now",
			"restored_by = $user",
		}
		params := map[string]any{
			"id":   id,
			"now":  now,
			"user": opts.UserID,
		}
		if natCol != "" {
			sets = append(sets,
				natCol+" = $final_key",
				natOrigCol+" = NONE",
			)
			params["final_key"] = desiredKey
		}
		stmt := fmt.Sprintf(
			"UPDATE type::thing('%s', $id) SET %s",
			table, strings.Join(sets, ", "),
		)
		if _, qerr := s.sur.Query(ctx, stmt, params); qerr != nil {
			return fmt.Errorf("restore primary: %w", qerr)
		}
		batchSize++

		// Restore all cascade siblings by batch id. These inherit the
		// primary's original_* for their own key. The tables we care
		// about are the artifact's children.
		if t == TypeKnowledgeArtifact {
			n, err := s.restoreChildrenByBatch(ctx, batchID, now, opts.UserID)
			if err != nil {
				return err
			}
			batchSize += n
		}
		return nil
	})
	if err != nil {
		return RestoreResult{}, err
	}

	return RestoreResult{
		RestoredID: id,
		BatchSize:  batchSize,
		Renamed:    renamed,
		NewKey: func() string {
			if renamed {
				return desiredKey
			}
			return ""
		}(),
	}, nil
}

// restoreChildrenByBatch un-tombstones every row in any of the
// artifact's four child tables whose trash_batch_id matches.
func (s *SurrealStore) restoreChildrenByBatch(ctx context.Context, batchID string, now time.Time, userID string) (int, error) {
	total := 0
	for _, table := range []string{
		"ca_knowledge_section",
		"ca_knowledge_dependency",
		"ca_knowledge_refinement",
		"ca_knowledge_evidence",
	} {
		stmt := fmt.Sprintf(
			`UPDATE %s
			 SET deleted_at = NONE, trash_batch_id = NONE
			 WHERE trash_batch_id = $batch_id`,
			table,
		)
		res, err := surrealdb.Query[[]map[string]any](ctx, s.sur.DB(), stmt,
			map[string]any{"batch_id": batchID})
		if err != nil {
			return total, fmt.Errorf("restore children %s: %w", table, err)
		}
		if res != nil && len(*res) > 0 && (*res)[0].Result != nil {
			total += len((*res)[0].Result)
		}
	}
	_ = now    // reserved: restored_at/by on child tables will be added in a follow-up migration when we surface per-row audit.
	_ = userID // reserved: same.
	return total, nil
}

// keyIsTaken reports whether another non-tombstoned row of the same
// type + repo already holds the given natural key.
func (s *SurrealStore) keyIsTaken(ctx context.Context, t TrashableType, repoID, key, excludeID string) bool {
	if key == "" {
		return false
	}
	table, err := tableFor(t)
	if err != nil {
		return false
	}
	natCol, _ := naturalKeyColumnFor(t)
	if natCol == "" {
		return false
	}
	stmt := fmt.Sprintf(
		`SELECT count() AS n FROM %s
		 WHERE repo_id = $repo_id
		   AND %s = $key
		   AND deleted_at IS NONE
		   AND id != type::thing('%s', $exclude)
		 GROUP ALL`,
		table, natCol, table,
	)
	rows, err := surrealdb.Query[[]map[string]any](ctx, s.sur.DB(), stmt,
		map[string]any{"repo_id": repoID, "key": key, "exclude": excludeID})
	if err != nil {
		slog.Warn("trash keyIsTaken query failed", "error", err, "type", t)
		return false
	}
	if rows == nil || len(*rows) == 0 || (*rows)[0].Result == nil || len((*rows)[0].Result) == 0 {
		return false
	}
	return countFromRow((*rows)[0].Result[0]) > 0
}

func countFromRow(row map[string]any) int {
	v, ok := row["n"]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case uint64:
		return int(n)
	case int64:
		return int(n)
	}
	return 0
}

// --- PermanentlyDelete --------------------------------------------------

// PermanentlyDelete hard-deletes a tombstoned row. Refuses to touch
// a live row. Called by both the user-initiated mutation and the
// retention worker.
func (s *SurrealStore) PermanentlyDelete(ctx context.Context, t TrashableType, id string) error {
	if !t.Valid() {
		return fmt.Errorf("invalid trashable type %q", t)
	}
	row, err := s.fetchRow(ctx, t, id)
	if err != nil {
		return err
	}
	if row == nil {
		return fmt.Errorf("%s %s not found", t, id)
	}
	if row.DeletedAt == nil || row.DeletedAt.IsZero() {
		return fmt.Errorf("%s %s is live; cannot permanently delete without first moving to trash", t, id)
	}
	table, err := tableFor(t)
	if err != nil {
		return err
	}
	return s.sur.RunInTx(ctx, func(ctx context.Context) error {
		if t == TypeKnowledgeArtifact {
			for _, child := range []string{
				"ca_knowledge_evidence",
				"ca_knowledge_refinement",
				"ca_knowledge_dependency",
				"ca_knowledge_section",
			} {
				stmt := fmt.Sprintf("DELETE %s WHERE trash_batch_id = $batch_id", child)
				if _, qerr := s.sur.Query(ctx, stmt, map[string]any{"batch_id": row.TrashBatchID}); qerr != nil {
					return fmt.Errorf("delete %s: %w", child, qerr)
				}
			}
		}
		stmt := fmt.Sprintf("DELETE type::thing('%s', $id)", table)
		_, qerr := s.sur.Query(ctx, stmt, map[string]any{"id": id})
		return qerr
	})
}

// --- List ---------------------------------------------------------------

// List returns trashed entries matching the filter. This is the read
// path for the trash view itself; unlike the normal read paths it
// selects rows WITH deleted_at set.
func (s *SurrealStore) List(ctx context.Context, filter ListFilter) ([]Entry, int, error) {
	types := filter.Types
	if len(types) == 0 {
		types = []TrashableType{TypeRequirement, TypeRequirementLink, TypeKnowledgeArtifact}
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}

	var allEntries []Entry
	total := 0
	for _, t := range types {
		entries, count, err := s.listType(ctx, t, filter)
		if err != nil {
			return nil, 0, err
		}
		total += count
		allEntries = append(allEntries, entries...)
	}
	// Most-recent first.
	sortEntriesByDeletedAtDesc(allEntries)

	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= len(allEntries) {
		return nil, total, nil
	}
	end := offset + limit
	if end > len(allEntries) {
		end = len(allEntries)
	}
	return allEntries[offset:end], total, nil
}

func (s *SurrealStore) listType(ctx context.Context, t TrashableType, filter ListFilter) ([]Entry, int, error) {
	table, err := tableFor(t)
	if err != nil {
		return nil, 0, err
	}
	where := []string{"deleted_at IS NOT NONE"}
	params := map[string]any{}
	if filter.RepositoryID != "" {
		where = append(where, "repo_id = $repo_id")
		params["repo_id"] = filter.RepositoryID
	}
	clause := strings.Join(where, " AND ")
	stmt := fmt.Sprintf(
		"SELECT * FROM %s WHERE %s ORDER BY deleted_at DESC",
		table, clause,
	)
	rows, err := surrealdb.Query[[]surrealTrashRow](ctx, s.sur.DB(), stmt, params)
	if err != nil {
		return nil, 0, fmt.Errorf("list %s: %w", table, err)
	}
	if rows == nil || len(*rows) == 0 {
		return nil, 0, nil
	}
	first := (*rows)[0]
	if len(first.Result) == 0 {
		return nil, 0, nil
	}
	entries := make([]Entry, 0, len(first.Result))
	for i := range first.Result {
		row := &first.Result[i]
		id := recordIDString(row.ID)
		if id == "" {
			continue
		}
		entry := s.entryFromRow(t, id, row)
		if filter.Search != "" && !entryMatchesSearch(entry, filter.Search) {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, len(entries), nil
}

// --- SweepExpired -------------------------------------------------------

// SweepExpired hard-deletes every tombstoned row older than the
// retention window, capped at maxBatch per type per call. Returns the
// total count of rows purged.
func (s *SurrealStore) SweepExpired(ctx context.Context, retention time.Duration, maxBatch int) (int, error) {
	if retention <= 0 {
		return 0, errors.New("retention must be positive")
	}
	cutoff := time.Now().UTC().Add(-retention)
	total := 0
	for _, t := range []TrashableType{TypeRequirement, TypeRequirementLink, TypeKnowledgeArtifact} {
		table, err := tableFor(t)
		if err != nil {
			continue
		}
		rows, err := surrealdb.Query[[]surrealTrashRow](ctx, s.sur.DB(),
			fmt.Sprintf("SELECT id FROM %s WHERE deleted_at IS NOT NONE AND deleted_at < $cutoff LIMIT %d",
				table, maxBatch),
			map[string]any{"cutoff": cutoff})
		if err != nil {
			return total, fmt.Errorf("sweep select %s: %w", table, err)
		}
		if rows == nil || len(*rows) == 0 || len((*rows)[0].Result) == 0 {
			continue
		}
		for i := range (*rows)[0].Result {
			id := recordIDString((*rows)[0].Result[i].ID)
			if id == "" {
				continue
			}
			if err := s.PermanentlyDelete(ctx, t, id); err != nil {
				slog.Warn("sweep permanent delete failed", "type", t, "id", id, "error", err)
				continue
			}
			total++
		}
	}
	return total, nil
}

// --- Entry assembly -----------------------------------------------------

func (s *SurrealStore) entryFromRow(t TrashableType, id string, row *surrealTrashRow) Entry {
	label, naturalKey := describeRow(t, row)
	var deletedAt time.Time
	if row.DeletedAt != nil {
		deletedAt = row.DeletedAt.Time
	}
	entry := Entry{
		ID:            id,
		Type:          t,
		RepositoryID:  row.RepoID,
		Label:         label,
		OriginalKey:   naturalKey,
		DeletedAt:     deletedAt,
		DeletedBy:     row.DeletedBy,
		DeletedReason: row.DeletedReason,
		TrashBatchID:  row.TrashBatchID,
		CanRestore:    true,
	}
	// Advisory conflict check. Best-effort; restore itself re-checks.
	if naturalKey != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if s.keyIsTaken(ctx, t, row.RepoID, naturalKey, id) {
			entry.CanRestore = false
			entry.RestoreConflict = fmt.Sprintf("%q is now in use by another entity", naturalKey)
		}
	}
	return entry
}

func entryMatchesSearch(e Entry, q string) bool {
	needle := strings.ToLower(q)
	return strings.Contains(strings.ToLower(e.Label), needle) ||
		strings.Contains(strings.ToLower(e.OriginalKey), needle) ||
		strings.Contains(strings.ToLower(e.ID), needle)
}

func sortEntriesByDeletedAtDesc(entries []Entry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].DeletedAt.After(entries[j-1].DeletedAt); j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}
