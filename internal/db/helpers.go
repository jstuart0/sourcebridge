// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/fxamacker/cbor/v2"
	surrealdb "github.com/surrealdb/surrealdb.go"
	"github.com/surrealdb/surrealdb.go/pkg/models"

	"github.com/sourcebridge/sourcebridge/internal/clustering"
	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// surrealTime handles SurrealDB datetime deserialization from CBOR.
// It accepts both native SurrealDB datetime (CBOR tag 12) and legacy
// string values (from records created before the time::now() migration).
type surrealTime struct {
	time.Time
}

func (st *surrealTime) UnmarshalCBOR(data []byte) error {
	// Try the SDK's CustomDateTime first (handles CBOR tag 12)
	var dt models.CustomDateTime
	if err := dt.UnmarshalCBOR(data); err == nil {
		st.Time = dt.Time
		return nil
	}
	// Fall back to plain string (old records stored datetime as RFC3339 text)
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
	// Zero time for empty/null
	st.Time = time.Time{}
	return nil
}

// SurrealStore implements graph-store-compatible operations backed by SurrealDB.
// It fulfills the same role as graph.Store but persists data to a real database.
type SurrealStore struct {
	client *SurrealDB
}

// Verify at compile time that *SurrealStore satisfies graph.GraphStore.
var _ graph.GraphStore = (*SurrealStore)(nil)

// Verify at compile time that *SurrealStore satisfies clustering.ClusterStore.
var _ clustering.ClusterStore = (*SurrealStore)(nil)

// NewSurrealStore creates a SurrealStore wrapping an already-connected SurrealDB client.
func NewSurrealStore(client *SurrealDB) *SurrealStore {
	return &SurrealStore{client: client}
}

// helper to run a query and return the result; logs errors.
func queryOne[T any](ctx context.Context, db *surrealdb.DB, sql string, vars map[string]any) (T, error) {
	var zero T
	results, err := surrealdb.Query[T](ctx, db, sql, vars)
	if err != nil {
		return zero, err
	}
	if results == nil || len(*results) == 0 {
		return zero, fmt.Errorf("empty query result")
	}
	qr := (*results)[0]
	if qr.Error != nil {
		return zero, qr.Error
	}
	return qr.Result, nil
}

// recordIDString extracts the key portion from a SurrealDB *models.RecordID.
// Returns the raw ID value as a string (e.g. the UUID), stripping the table prefix.
func recordIDString(rid *models.RecordID) string {
	if rid == nil {
		return ""
	}
	return fmt.Sprintf("%v", rid.ID)
}

// sortedKeys returns the keys of a string set in sorted order.
// Returns nil (not an empty slice) for an empty map to match the
// callers' nil-comparison patterns in internal/db/index_result.go.
func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	return slices.Sorted(maps.Keys(m))
}

// linkID builds a deterministic ID from (repoID, requirementID, symbolID)
// so that re-running auto-link produces an UPSERT instead of duplicates.
func linkID(repoID, requirementID, symbolID string) string {
	h := sha256.Sum256([]byte(repoID + "|" + requirementID + "|" + symbolID))
	return hex.EncodeToString(h[:16]) // 128-bit, collision-safe for this use
}

// coerceInt extracts an integer from a loosely-typed SurrealDB result value.
// SurrealDB's CBOR driver may return numeric fields as float64, int, int64,
// or uint64 depending on the schema type and the value magnitude. Returns 0
// for nil or unrecognised types.
func coerceInt(v any) int {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case uint64:
		return int(n)
	}
	return 0
}

// coerceUint64 extracts a uint64 from a loosely-typed SurrealDB result value.
// Negative values are clamped to 0. Returns 0 for nil or unrecognised types.
func coerceUint64(v any) uint64 {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		if n < 0 {
			return 0
		}
		return uint64(n)
	case uint64:
		return n
	case int:
		if n < 0 {
			return 0
		}
		return uint64(n)
	case int64:
		if n < 0 {
			return 0
		}
		return uint64(n)
	}
	return 0
}

// ---------------------------------------------------------------------------
// Cross-domain row types (used by more than one domain file)
// ---------------------------------------------------------------------------

// surrealRepo is the SurrealDB representation of a repository record.
type surrealRepo struct {
	ID                    *models.RecordID `json:"id,omitempty"`
	Name                  string           `json:"name"`
	Path                  string           `json:"path"`
	ClonePath             string           `json:"clone_path,omitempty"`
	RemoteURL             string           `json:"remote_url,omitempty"`
	CommitSHA             string           `json:"commit_sha,omitempty"`
	Branch                string           `json:"branch,omitempty"`
	GenerationModeDefault string           `json:"generation_mode_default,omitempty"`
	Status                string           `json:"status"`
	FileCount             int              `json:"file_count"`
	FunctionCount         int              `json:"function_count"`
	ClassCount            int              `json:"class_count"`
	LastIndexedAt         surrealTime      `json:"last_indexed_at"`
	CreatedAt             surrealTime      `json:"created_at"`
	IndexError            *string          `json:"index_error,omitempty"`
	UnderstandingScore    *float64         `json:"understanding_score,omitempty"`
	UnderstandingScoreAt  *surrealTime     `json:"understanding_score_at,omitempty"`
}

func (r *surrealRepo) toRepository() *graph.Repository {
	repo := &graph.Repository{
		ID:                    recordIDString(r.ID),
		Name:                  r.Name,
		Path:                  r.Path,
		ClonePath:             r.ClonePath,
		RemoteURL:             r.RemoteURL,
		CommitSHA:             r.CommitSHA,
		Branch:                r.Branch,
		GenerationModeDefault: r.GenerationModeDefault,
		Status:                r.Status,
		FileCount:             r.FileCount,
		FunctionCount:         r.FunctionCount,
		ClassCount:            r.ClassCount,
	}
	if r.IndexError != nil {
		repo.IndexError = *r.IndexError
	}
	if r.UnderstandingScore != nil {
		repo.UnderstandingScoreVal = r.UnderstandingScore
	}
	if r.UnderstandingScoreAt != nil {
		t := r.UnderstandingScoreAt.Time
		repo.UnderstandingScoreAt = &t
	}
	repo.LastIndexedAt = r.LastIndexedAt.Time
	repo.CreatedAt = r.CreatedAt.Time
	return repo
}

type surrealFile struct {
	ID        *models.RecordID `json:"id,omitempty"`
	RepoID    string           `json:"repo_id"`
	Path      string           `json:"path"`
	Language  string           `json:"language"`
	LineCount int              `json:"line_count"`
	AIScore   float64          `json:"ai_score"`
	AISignals []string         `json:"ai_signals,omitempty"`
}

func (f *surrealFile) toFile() *graph.File {
	return &graph.File{
		ID:        recordIDString(f.ID),
		RepoID:    f.RepoID,
		Path:      f.Path,
		Language:  f.Language,
		LineCount: f.LineCount,
		AIScore:   f.AIScore,
		AISignals: f.AISignals,
	}
}

type surrealSymbol struct {
	ID            *models.RecordID `json:"id,omitempty"`
	RepoID        string           `json:"repo_id"`
	FileID        string           `json:"file_id"`
	Name          string           `json:"name"`
	QualifiedName string           `json:"qualified_name"`
	Kind          string           `json:"kind"`
	Language      string           `json:"language"`
	FilePath      string           `json:"file_path"`
	StartLine     int              `json:"start_line"`
	EndLine       int              `json:"end_line"`
	Signature     string           `json:"signature"`
	DocComment    string           `json:"doc_comment"`
	IsTest        bool             `json:"is_test"`
	// Search / vector fields (added by migration 034). Kept optional so
	// rows written before the migration survive the round trip.
	Embedding      []float64 `json:"embedding,omitempty"`
	EmbeddingModel string    `json:"embedding_model,omitempty"`
	EmbeddingDim   int       `json:"embedding_dim,omitempty"`
	EmbeddingHash  string    `json:"embedding_hash,omitempty"`
}

func (s *surrealSymbol) toStoredSymbol() *graph.StoredSymbol {
	return &graph.StoredSymbol{
		ID:            recordIDString(s.ID),
		RepoID:        s.RepoID,
		FileID:        s.FileID,
		Name:          s.Name,
		QualifiedName: s.QualifiedName,
		Kind:          s.Kind,
		Language:      s.Language,
		FilePath:      s.FilePath,
		StartLine:     s.StartLine,
		EndLine:       s.EndLine,
		Signature:     s.Signature,
		DocComment:    s.DocComment,
		IsTest:        s.IsTest,
	}
}

type surrealModule struct {
	ID        *models.RecordID `json:"id,omitempty"`
	RepoID    string           `json:"repo_id"`
	Name      string           `json:"name"`
	Path      string           `json:"path"`
	FileCount int              `json:"file_count"`
}

func (m *surrealModule) toStoredModule() *graph.StoredModule {
	return &graph.StoredModule{
		ID:        recordIDString(m.ID),
		RepoID:    m.RepoID,
		Name:      m.Name,
		Path:      m.Path,
		FileCount: m.FileCount,
	}
}

type surrealRequirement struct {
	ID                 *models.RecordID `json:"id,omitempty"`
	RepoID             string           `json:"repo_id"`
	ExternalID         string           `json:"external_id"`
	Title              string           `json:"title"`
	Description        string           `json:"description"`
	Source             string           `json:"source"`
	Priority           string           `json:"priority"`
	Tags               []string         `json:"tags"`
	AcceptanceCriteria []string         `json:"acceptance_criteria"`
	CreatedAt          surrealTime      `json:"created_at"`
	UpdatedAt          surrealTime      `json:"updated_at"`
}

func (r *surrealRequirement) toStoredRequirement() *graph.StoredRequirement {
	return &graph.StoredRequirement{
		ID:                 recordIDString(r.ID),
		RepoID:             r.RepoID,
		ExternalID:         r.ExternalID,
		Title:              r.Title,
		Description:        r.Description,
		Source:             r.Source,
		Priority:           r.Priority,
		Tags:               r.Tags,
		AcceptanceCriteria: r.AcceptanceCriteria,
		CreatedAt:          r.CreatedAt.Time,
		UpdatedAt:          r.UpdatedAt.Time,
	}
}

type surrealLink struct {
	ID            *models.RecordID `json:"id,omitempty"`
	RepoID        string           `json:"repo_id"`
	RequirementID string           `json:"requirement_id"`
	SymbolID      string           `json:"symbol_id"`
	Confidence    float64          `json:"confidence"`
	Source        string           `json:"source"`
	LinkType      string           `json:"link_type"`
	Rationale     string           `json:"rationale"`
	Verified      bool             `json:"verified"`
	VerifiedBy    string           `json:"verified_by"`
	Rejected      bool             `json:"rejected"`
	CreatedAt     surrealTime      `json:"created_at"`
}

func (l *surrealLink) toStoredLink() *graph.StoredLink {
	return &graph.StoredLink{
		ID:            recordIDString(l.ID),
		RepoID:        l.RepoID,
		RequirementID: l.RequirementID,
		SymbolID:      l.SymbolID,
		Confidence:    l.Confidence,
		Source:        l.Source,
		LinkType:      l.LinkType,
		Rationale:     l.Rationale,
		Verified:      l.Verified,
		VerifiedBy:    l.VerifiedBy,
		Rejected:      l.Rejected,
		CreatedAt:     l.CreatedAt.Time,
	}
}
