// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"
	surrealdb "github.com/surrealdb/surrealdb.go"
	"github.com/surrealdb/surrealdb.go/pkg/models"

	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
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

// recordIDString extracts the key portion from a SurrealDB *models.RecordID.
// Returns the raw ID value as a string (e.g. the UUID), stripping the table prefix.
func recordIDString(rid *models.RecordID) string {
	if rid == nil {
		return ""
	}
	return fmt.Sprintf("%v", rid.ID)
}

func ctx() context.Context { return context.Background() }

// ---------------------------------------------------------------------------
// Repository operations
// ---------------------------------------------------------------------------

// CreateRepository creates a placeholder repository with PENDING status.
func (s *SurrealStore) CreateRepository(name, path string) (*graph.Repository, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}

	repoID := uuid.New().String()

	_, err := surrealdb.Query[interface{}](ctx(), db,
		`CREATE ca_repository SET
			id = type::thing('ca_repository', $rid),
			name = $name,
			path = $path,
			status = 'pending',
			file_count = 0,
			function_count = 0,
			class_count = 0,
			last_indexed_at = time::now(),
			created_at = time::now()`,
		map[string]any{
			"rid":  repoID,
			"name": name,
			"path": path,
		})
	if err != nil {
		return nil, fmt.Errorf("creating repository: %w", err)
	}

	now := time.Now().UTC()
	return &graph.Repository{
		ID:        repoID,
		Name:      name,
		Path:      path,
		Status:    "pending",
		CreatedAt: now,
	}, nil
}

// StoreIndexResult persists a full indexing result.
func (s *SurrealStore) StoreIndexResult(result *indexer.IndexResult) (*graph.Repository, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}

	repoID := uuid.New().String()

	funcCount := 0
	classCount := 0

	// Map indexer symbol IDs → store symbol IDs for call graph resolution
	idMap := make(map[string]string)

	// Store files and symbols
	for _, fr := range result.Files {
		fileID := uuid.New().String()

		_, err := surrealdb.Query[interface{}](ctx(), db,
			`CREATE ca_file SET
				id = type::thing('ca_file', $fid),
				repo_id = $repo_id,
				path = $path,
				language = $language,
				line_count = $line_count,
				content_hash = $content_hash,
				ai_score = $ai_score,
				ai_signals = $ai_signals`,
			map[string]any{
				"fid":          fileID,
				"repo_id":      repoID,
				"path":         fr.Path,
				"language":     fr.Language,
				"line_count":   fr.LineCount,
				"content_hash": fr.ContentHash,
				"ai_score":     fr.AIScore,
				"ai_signals":   fr.AISignals,
			})
		if err != nil {
			slog.Warn("failed to store file", "path", fr.Path, "error", err)
			continue
		}

		for _, sym := range fr.Symbols {
			symID := uuid.New().String()
			idMap[sym.ID] = symID

			_, err := surrealdb.Query[interface{}](ctx(), db,
				`CREATE ca_symbol SET
					id = type::thing('ca_symbol', $sid),
					repo_id = $repo_id,
					file_id = $file_id,
					name = $name,
					qualified_name = $qname,
					kind = $kind,
					language = $language,
					file_path = $fpath,
					start_line = $start_line,
					end_line = $end_line,
					signature = $signature,
					doc_comment = $doc_comment,
					is_test = $is_test`,
				map[string]any{
					"sid":         symID,
					"repo_id":     repoID,
					"file_id":     fileID,
					"name":        sym.Name,
					"qname":       sym.QualifiedName,
					"kind":        string(sym.Kind),
					"language":    sym.Language,
					"fpath":       sym.FilePath,
					"start_line":  sym.StartLine,
					"end_line":    sym.EndLine,
					"signature":   sym.Signature,
					"doc_comment": sym.DocComment,
					"is_test":     sym.IsTest,
				})
			if err != nil {
				slog.Warn("failed to store symbol", "name", sym.Name, "error", err)
				continue
			}

			switch sym.Kind {
			case indexer.SymbolFunction, indexer.SymbolMethod:
				funcCount++
			case indexer.SymbolClass, indexer.SymbolStruct, indexer.SymbolInterface, indexer.SymbolEnum, indexer.SymbolTrait:
				classCount++
			}
		}

		for _, imp := range fr.Imports {
			_, _ = surrealdb.Query[interface{}](ctx(), db,
				`CREATE ca_import SET
					file_id = $file_id,
					path = $path,
					line = $line`,
				map[string]any{
					"file_id": fileID,
					"path":    imp.Path,
					"line":    imp.Line,
				})
		}
	}

	// Store call graph relations
	for _, rel := range result.Relations {
		if rel.Type != indexer.RelationCalls {
			continue
		}
		callerID := idMap[rel.SourceID]
		calleeID := idMap[rel.TargetID]
		if callerID == "" || calleeID == "" {
			continue
		}
		_, _ = surrealdb.Query[interface{}](ctx(), db,
			`CREATE ca_calls SET
				caller_id = $caller_id,
				callee_id = $callee_id,
				repo_id = $repo_id`,
			map[string]any{
				"caller_id": callerID,
				"callee_id": calleeID,
				"repo_id":   repoID,
			})
	}

	// Store modules
	for _, mod := range result.Modules {
		modID := uuid.New().String()
		_, _ = surrealdb.Query[interface{}](ctx(), db,
			`CREATE ca_module SET
				id = type::thing('ca_module', $mid),
				repo_id = $repo_id,
				name = $name,
				path = $path,
				file_count = $file_count`,
			map[string]any{
				"mid":        modID,
				"repo_id":    repoID,
				"name":       mod.Name,
				"path":       mod.Path,
				"file_count": mod.FileCount,
			})
	}

	// Store repository record
	// Use time::now() so SurrealDB generates a native datetime value
	// (passing a Go-formatted string is rejected by SCHEMAFULL datetime fields).
	_, err := surrealdb.Query[interface{}](ctx(), db,
		`CREATE ca_repository SET
			id = type::thing('ca_repository', $rid),
			name = $name,
			path = $path,
			status = 'ready',
			file_count = $file_count,
			function_count = $func_count,
			class_count = $class_count,
			last_indexed_at = time::now(),
			created_at = time::now()`,
		map[string]any{
			"rid":         repoID,
			"name":        result.RepoName,
			"path":        result.RepoPath,
			"file_count":  result.TotalFiles,
			"func_count":  funcCount,
			"class_count": classCount,
		})
	if err != nil {
		return nil, fmt.Errorf("storing repository: %w", err)
	}

	now := time.Now().UTC()
	return &graph.Repository{
		ID:            repoID,
		Name:          result.RepoName,
		Path:          result.RepoPath,
		Status:        "ready",
		FileCount:     result.TotalFiles,
		FunctionCount: funcCount,
		ClassCount:    classCount,
		LastIndexedAt: now,
		CreatedAt:     now,
	}, nil
}

// ReplaceIndexResult atomically replaces all files, symbols, modules, and relations
// for an existing repository with new index results.
func (s *SurrealStore) ReplaceIndexResult(repoID string, result *indexer.IndexResult) (*graph.Repository, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}

	// Mark as indexing so the UI shows progress even if the process is interrupted
	_, _ = surrealdb.Query[interface{}](ctx(), db,
		`UPDATE type::thing('ca_repository', $id) SET status = 'indexing'`,
		map[string]any{"id": repoID})

	// Remove old data (including call graph)
	_, _ = surrealdb.Query[interface{}](ctx(), db,
		`DELETE ca_import WHERE file_id IN (SELECT VALUE id FROM ca_file WHERE repo_id = $id);
		 DELETE ca_calls WHERE repo_id = $id;
		 DELETE ca_symbol WHERE repo_id = $id;
		 DELETE ca_module WHERE repo_id = $id;
		 DELETE ca_file WHERE repo_id = $id`,
		map[string]any{"id": repoID})

	funcCount := 0
	classCount := 0
	idMap := make(map[string]string)

	// Re-insert files and symbols
	for _, fr := range result.Files {
		fileID := uuid.New().String()

		_, err := surrealdb.Query[interface{}](ctx(), db,
			`CREATE ca_file SET
				id = type::thing('ca_file', $fid),
				repo_id = $repo_id,
				path = $path,
				language = $language,
				line_count = $line_count,
				content_hash = $content_hash,
				ai_score = $ai_score,
				ai_signals = $ai_signals`,
			map[string]any{
				"fid":          fileID,
				"repo_id":      repoID,
				"path":         fr.Path,
				"language":     fr.Language,
				"line_count":   fr.LineCount,
				"content_hash": fr.ContentHash,
				"ai_score":     fr.AIScore,
				"ai_signals":   fr.AISignals,
			})
		if err != nil {
			slog.Warn("failed to store file", "path", fr.Path, "error", err)
			continue
		}

		for _, sym := range fr.Symbols {
			symID := uuid.New().String()
			idMap[sym.ID] = symID
			_, _ = surrealdb.Query[interface{}](ctx(), db,
				`CREATE ca_symbol SET
					id = type::thing('ca_symbol', $sid),
					repo_id = $repo_id,
					file_id = $file_id,
					name = $name,
					qualified_name = $qname,
					kind = $kind,
					language = $language,
					file_path = $fpath,
					start_line = $start_line,
					end_line = $end_line,
					signature = $signature,
					doc_comment = $doc_comment,
					is_test = $is_test`,
				map[string]any{
					"sid":         symID,
					"repo_id":     repoID,
					"file_id":     fileID,
					"name":        sym.Name,
					"qname":       sym.QualifiedName,
					"kind":        string(sym.Kind),
					"language":    sym.Language,
					"fpath":       sym.FilePath,
					"start_line":  sym.StartLine,
					"end_line":    sym.EndLine,
					"signature":   sym.Signature,
					"doc_comment": sym.DocComment,
					"is_test":     sym.IsTest,
				})

			switch sym.Kind {
			case indexer.SymbolFunction, indexer.SymbolMethod:
				funcCount++
			case indexer.SymbolClass, indexer.SymbolStruct, indexer.SymbolInterface, indexer.SymbolEnum, indexer.SymbolTrait:
				classCount++
			}
		}

		for _, imp := range fr.Imports {
			_, _ = surrealdb.Query[interface{}](ctx(), db,
				`CREATE ca_import SET file_id = $file_id, path = $path, line = $line`,
				map[string]any{"file_id": fileID, "path": imp.Path, "line": imp.Line})
		}
	}

	// Re-insert call graph relations
	for _, rel := range result.Relations {
		if rel.Type != indexer.RelationCalls {
			continue
		}
		callerID := idMap[rel.SourceID]
		calleeID := idMap[rel.TargetID]
		if callerID == "" || calleeID == "" {
			continue
		}
		_, _ = surrealdb.Query[interface{}](ctx(), db,
			`CREATE ca_calls SET
				caller_id = $caller_id,
				callee_id = $callee_id,
				repo_id = $repo_id`,
			map[string]any{
				"caller_id": callerID,
				"callee_id": calleeID,
				"repo_id":   repoID,
			})
	}

	// Re-insert modules
	for _, mod := range result.Modules {
		modID := uuid.New().String()
		_, _ = surrealdb.Query[interface{}](ctx(), db,
			`CREATE ca_module SET
				id = type::thing('ca_module', $mid),
				repo_id = $repo_id,
				name = $name,
				path = $path,
				file_count = $file_count`,
			map[string]any{
				"mid":        modID,
				"repo_id":    repoID,
				"name":       mod.Name,
				"path":       mod.Path,
				"file_count": mod.FileCount,
			})
	}

	// Update repository record
	_, err := surrealdb.Query[interface{}](ctx(), db,
		`UPDATE type::thing('ca_repository', $id) SET
			status = 'ready',
			file_count = $file_count,
			function_count = $func_count,
			class_count = $class_count,
			last_indexed_at = time::now(),
			index_error = NONE`,
		map[string]any{
			"id":          repoID,
			"file_count":  result.TotalFiles,
			"func_count":  funcCount,
			"class_count": classCount,
		})
	if err != nil {
		return nil, fmt.Errorf("updating repository: %w", err)
	}

	return s.GetRepository(repoID), nil
}

// UpdateRepositoryMeta updates mutable metadata fields on a repository.
func (s *SurrealStore) UpdateRepositoryMeta(id string, meta graph.RepositoryMeta) {
	db := s.client.DB()
	if db == nil {
		return
	}

	sets := []string{}
	vars := map[string]any{"id": id}

	if meta.ClonePath != "" {
		sets = append(sets, "clone_path = $clone_path")
		vars["clone_path"] = meta.ClonePath
	}
	if meta.RemoteURL != "" {
		sets = append(sets, "remote_url = $remote_url")
		vars["remote_url"] = meta.RemoteURL
	}
	if meta.CommitSHA != "" {
		sets = append(sets, "commit_sha = $commit_sha")
		vars["commit_sha"] = meta.CommitSHA
	}
	if meta.Branch != "" {
		sets = append(sets, "branch = $branch")
		vars["branch"] = meta.Branch
	}
	if meta.GenerationModeDefault != "" {
		sets = append(sets, "generation_mode_default = $generation_mode_default")
		vars["generation_mode_default"] = meta.GenerationModeDefault
	}

	if len(sets) == 0 {
		return
	}

	sql := fmt.Sprintf("UPDATE type::thing('ca_repository', $id) SET %s", strings.Join(sets, ", "))
	_, _ = surrealdb.Query[interface{}](ctx(), db, sql, vars)
}

// ListRepositories returns all repositories.
func (s *SurrealStore) ListRepositories() []*graph.Repository {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealRepo](ctx(), db,
		"SELECT * FROM ca_repository", nil)
	if err != nil {
		slog.Warn("list repositories failed", "error", err)
		return nil
	}

	repos := make([]*graph.Repository, 0, len(rows))
	for i := range rows {
		repos = append(repos, rows[i].toRepository())
	}
	return repos
}

// GetRepository returns a repository by ID.
func (s *SurrealStore) GetRepository(id string) *graph.Repository {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealRepo](ctx(), db,
		"SELECT * FROM type::thing('ca_repository', $id)",
		map[string]any{"id": id})
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rows[0].toRepository()
}

// GetRepositoryByPath returns a repository by its path.
func (s *SurrealStore) GetRepositoryByPath(path string) *graph.Repository {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealRepo](ctx(), db,
		"SELECT * FROM ca_repository WHERE path = $path LIMIT 1",
		map[string]any{"path": path})
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rows[0].toRepository()
}

// RemoveRepository removes a repository and all its data.
func (s *SurrealStore) RemoveRepository(id string) bool {
	db := s.client.DB()
	if db == nil {
		return false
	}

	_, err := surrealdb.Query[interface{}](ctx(), db,
		`DELETE ca_link WHERE repo_id = $id;
		 DELETE ca_requirement WHERE repo_id = $id;
		 DELETE ca_import WHERE file_id IN (SELECT VALUE id FROM ca_file WHERE repo_id = $id);
		 DELETE ca_calls WHERE repo_id = $id;
		 DELETE ca_symbol WHERE repo_id = $id;
		 DELETE ca_module WHERE repo_id = $id;
		 DELETE ca_file WHERE repo_id = $id;
		 DELETE type::thing('ca_repository', $id)`,
		map[string]any{"id": id})
	if err != nil {
		slog.Warn("remove repository failed", "error", err)
		return false
	}
	return true
}

// GetFiles returns all files for a repository.
func (s *SurrealStore) GetFiles(repoID string) []*graph.File {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealFile](ctx(), db,
		"SELECT * FROM ca_file WHERE repo_id = $repo_id",
		map[string]any{"repo_id": repoID})
	if err != nil {
		return nil
	}

	files := make([]*graph.File, 0, len(rows))
	for i := range rows {
		files = append(files, rows[i].toFile())
	}
	return files
}

// GetFilesPaginated returns files for a repository with optional path prefix filtering and pagination.
func (s *SurrealStore) GetFilesPaginated(repoID string, pathPrefix *string, limit, offset int) ([]*graph.File, int) {
	db := s.client.DB()
	if db == nil {
		return nil, 0
	}

	where := "repo_id = $repo_id"
	vars := map[string]any{"repo_id": repoID}

	if pathPrefix != nil && *pathPrefix != "" {
		where += " AND string::starts_with(path, $prefix)"
		vars["prefix"] = *pathPrefix
	}

	// Get total count
	countRows, err := queryOne[[]map[string]interface{}](ctx(), db,
		fmt.Sprintf("SELECT count() AS total FROM ca_file WHERE %s GROUP ALL", where), vars)
	total := 0
	if err == nil && len(countRows) > 0 {
		if v, ok := countRows[0]["total"]; ok {
			switch vt := v.(type) {
			case float64:
				total = int(vt)
			case int:
				total = vt
			case uint64:
				total = int(vt)
			}
		}
	}

	sql := fmt.Sprintf("SELECT * FROM ca_file WHERE %s ORDER BY path", where)
	if limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", limit)
	}
	if offset > 0 {
		sql += fmt.Sprintf(" START %d", offset)
	}

	rows, err := queryOne[[]surrealFile](ctx(), db, sql, vars)
	if err != nil {
		return nil, total
	}

	files := make([]*graph.File, 0, len(rows))
	for i := range rows {
		files = append(files, rows[i].toFile())
	}
	return files, total
}

// GetSymbols returns symbols for a repository with optional filtering.
func (s *SurrealStore) GetSymbols(repoID string, query *string, kind *string, limit, offset int) ([]*graph.StoredSymbol, int) {
	db := s.client.DB()
	if db == nil {
		return nil, 0
	}

	// Build dynamic query
	where := "repo_id = $repo_id"
	vars := map[string]any{"repo_id": repoID}

	if kind != nil {
		where += " AND kind = $kind"
		vars["kind"] = *kind
	}
	if query != nil && *query != "" {
		where += " AND (string::lowercase(name) CONTAINS $q OR string::lowercase(qualified_name) CONTAINS $q)"
		vars["q"] = strings.ToLower(*query)
	}

	// Get total count
	countRows, err := queryOne[[]map[string]interface{}](ctx(), db,
		fmt.Sprintf("SELECT count() AS total FROM ca_symbol WHERE %s GROUP ALL", where), vars)
	total := 0
	if err == nil && len(countRows) > 0 {
		if v, ok := countRows[0]["total"]; ok {
			switch vt := v.(type) {
			case float64:
				total = int(vt)
			case int:
				total = vt
			case uint64:
				total = int(vt)
			}
		}
	}

	// Build paginated query
	sql := fmt.Sprintf("SELECT * FROM ca_symbol WHERE %s ORDER BY file_path ASC, name ASC", where)
	if limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", limit)
	}
	if offset > 0 {
		sql += fmt.Sprintf(" START %d", offset)
	}

	rows, err := queryOne[[]surrealSymbol](ctx(), db, sql, vars)
	if err != nil {
		return nil, total
	}

	syms := make([]*graph.StoredSymbol, 0, len(rows))
	for i := range rows {
		syms = append(syms, rows[i].toStoredSymbol())
	}
	return syms, total
}

// GetFileSymbols returns symbols for a specific file.
func (s *SurrealStore) GetFileSymbols(fileID string) []*graph.StoredSymbol {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealSymbol](ctx(), db,
		"SELECT * FROM ca_symbol WHERE file_id = $file_id",
		map[string]any{"file_id": fileID})
	if err != nil {
		return nil
	}

	syms := make([]*graph.StoredSymbol, 0, len(rows))
	for i := range rows {
		syms = append(syms, rows[i].toStoredSymbol())
	}
	return syms
}

// GetModules returns all modules for a repository.
func (s *SurrealStore) GetModules(repoID string) []*graph.StoredModule {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealModule](ctx(), db,
		"SELECT * FROM ca_module WHERE repo_id = $repo_id",
		map[string]any{"repo_id": repoID})
	if err != nil {
		return nil
	}

	mods := make([]*graph.StoredModule, 0, len(rows))
	for i := range rows {
		mods = append(mods, rows[i].toStoredModule())
	}
	return mods
}

// GetCallers returns the IDs of symbols that call the given symbol.
func (s *SurrealStore) GetCallers(symbolID string) []string {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]string](ctx(), db,
		"SELECT VALUE caller_id FROM ca_calls WHERE callee_id = $id",
		map[string]any{"id": symbolID})
	if err != nil {
		return nil
	}
	return rows
}

// GetCallees returns the IDs of symbols called by the given symbol.
func (s *SurrealStore) GetCallees(symbolID string) []string {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]string](ctx(), db,
		"SELECT VALUE callee_id FROM ca_calls WHERE caller_id = $id",
		map[string]any{"id": symbolID})
	if err != nil {
		return nil
	}
	return rows
}

// GetCallEdges returns all call edges for a repository in a single batch.
func (s *SurrealStore) GetCallEdges(repoID string) []graph.CallEdge {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	type edgeRow struct {
		CallerID string `json:"caller_id"`
		CalleeID string `json:"callee_id"`
	}

	rows, err := queryOne[[]edgeRow](ctx(), db,
		"SELECT caller_id, callee_id FROM ca_calls WHERE repo_id = $repo_id",
		map[string]any{"repo_id": repoID})
	if err != nil {
		return nil
	}

	edges := make([]graph.CallEdge, len(rows))
	for i, r := range rows {
		edges[i] = graph.CallEdge{CallerID: r.CallerID, CalleeID: r.CalleeID}
	}
	return edges
}

// GetImports returns all imports for a repository.
func (s *SurrealStore) GetImports(repoID string) []*graph.StoredImport {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	type importRow struct {
		FileID string `json:"file_id"`
		Path   string `json:"path"`
		Line   int    `json:"line"`
	}
	rows, err := queryOne[[]importRow](ctx(), db,
		`SELECT * FROM ca_import WHERE file_id IN (SELECT VALUE id FROM ca_file WHERE repo_id = $repo_id)`,
		map[string]any{"repo_id": repoID})
	if err != nil {
		return nil
	}

	imports := make([]*graph.StoredImport, 0, len(rows))
	for _, r := range rows {
		imports = append(imports, &graph.StoredImport{
			FileID: r.FileID,
			Path:   r.Path,
			Line:   r.Line,
		})
	}
	return imports
}

// SearchContent searches for symbols and files matching a query string.
func (s *SurrealStore) SearchContent(repoID, query string, limit int) []graph.SearchResult {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	q := strings.ToLower(query)
	vars := map[string]any{
		"repo_id": repoID,
		"q":       q,
	}

	var results []graph.SearchResult

	// Search symbols
	symLimit := limit
	if symLimit <= 0 {
		symLimit = 50
	}
	symRows, err := queryOne[[]surrealSymbol](ctx(), db,
		fmt.Sprintf(`SELECT * FROM ca_symbol
			WHERE repo_id = $repo_id
			  AND (string::lowercase(name) CONTAINS $q OR string::lowercase(qualified_name) CONTAINS $q)
			LIMIT %d`, symLimit),
		vars)
	if err == nil {
		for i := range symRows {
			sym := symRows[i]
			results = append(results, graph.SearchResult{
				Type:     "symbol",
				Name:     sym.Name,
				FilePath: sym.FilePath,
				Line:     sym.StartLine,
				Snippet:  sym.Signature,
				Kind:     sym.Kind,
			})
		}
	}

	// Search file paths
	fileRows, err := queryOne[[]surrealFile](ctx(), db,
		fmt.Sprintf(`SELECT * FROM ca_file
			WHERE repo_id = $repo_id
			  AND string::lowercase(path) CONTAINS $q
			LIMIT %d`, symLimit),
		vars)
	if err == nil {
		for i := range fileRows {
			f := fileRows[i]
			results = append(results, graph.SearchResult{
				Type:     "file",
				Name:     f.Path,
				FilePath: f.Path,
			})
		}
	}

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results
}

// Stats returns aggregate statistics.
func (s *SurrealStore) Stats() map[string]int {
	db := s.client.DB()
	if db == nil {
		return map[string]int{}
	}

	stats := map[string]int{}
	tables := map[string]string{
		"repositories": "ca_repository",
		"files":        "ca_file",
		"symbols":      "ca_symbol",
		"modules":      "ca_module",
		"requirements": "ca_requirement",
		"links":        "ca_link",
		"imports":      "ca_import",
	}

	for key, table := range tables {
		rows, err := queryOne[[]map[string]interface{}](ctx(), db,
			fmt.Sprintf("SELECT count() AS total FROM %s GROUP ALL", table), nil)
		if err == nil && len(rows) > 0 {
			if v, ok := rows[0]["total"]; ok {
				switch vt := v.(type) {
				case float64:
					stats[key] = int(vt)
				case int:
					stats[key] = vt
				case uint64:
					stats[key] = int(vt)
				}
			}
		} else {
			stats[key] = 0
		}
	}

	return stats
}

// SetRepositoryError marks a repository as having an error.
func (s *SurrealStore) SetRepositoryError(id string, repoErr error) {
	db := s.client.DB()
	if db == nil {
		return
	}

	_, _ = surrealdb.Query[interface{}](ctx(), db,
		`UPDATE type::thing('ca_repository', $id) SET status = 'error', index_error = $err`,
		map[string]any{
			"id":  id,
			"err": fmt.Sprintf("%v", repoErr),
		})
}

// CacheUnderstandingScore stores the precomputed overall score on the repository.
func (s *SurrealStore) CacheUnderstandingScore(id string, overall float64) {
	db := s.client.DB()
	if db == nil {
		return
	}
	_, _ = surrealdb.Query[interface{}](ctx(), db,
		`UPDATE type::thing('ca_repository', $id) SET
			understanding_score = $score,
			understanding_score_at = time::now()`,
		map[string]any{
			"id":    id,
			"score": overall,
		})
}

// ---------------------------------------------------------------------------
// Requirement operations
// ---------------------------------------------------------------------------

// StoreRequirement adds a requirement to the store.
func (s *SurrealStore) StoreRequirement(repoID string, req *graph.StoredRequirement) {
	db := s.client.DB()
	if db == nil {
		return
	}

	reqID := uuid.New().String()

	// Ensure array fields are never nil — SurrealDB rejects NULL for array fields
	// even when typed as option<array>.
	tags := req.Tags
	if tags == nil {
		tags = []string{}
	}
	ac := req.AcceptanceCriteria
	if ac == nil {
		ac = []string{}
	}

	_, err := surrealdb.Query[interface{}](ctx(), db,
		`CREATE ca_requirement SET
			id = type::thing('ca_requirement', $rid),
			repo_id = $repo_id,
			external_id = $external_id,
			title = $title,
			description = $description,
			source = $source,
			priority = $priority,
			tags = $tags,
			acceptance_criteria = $acceptance_criteria,
			created_at = time::now(),
			updated_at = time::now()`,
		map[string]any{
			"rid":                 reqID,
			"repo_id":             repoID,
			"external_id":         req.ExternalID,
			"title":               req.Title,
			"description":         req.Description,
			"source":              req.Source,
			"priority":            req.Priority,
			"tags":                tags,
			"acceptance_criteria": ac,
		})
	if err != nil {
		slog.Warn("failed to store requirement", "title", req.Title, "error", err)
		return
	}

	req.ID = reqID
	req.RepoID = repoID
	req.CreatedAt = time.Now().UTC()
	req.UpdatedAt = req.CreatedAt
}

// StoreRequirements adds multiple requirements and returns the count stored.
func (s *SurrealStore) StoreRequirements(repoID string, reqs []*graph.StoredRequirement) int {
	count := 0
	for _, req := range reqs {
		s.StoreRequirement(repoID, req)
		if req.ID != "" {
			count++
		}
	}
	return count
}

// GetRequirements returns requirements for a repository with pagination.
func (s *SurrealStore) GetRequirements(repoID string, limit, offset int) ([]*graph.StoredRequirement, int) {
	db := s.client.DB()
	if db == nil {
		return nil, 0
	}

	vars := map[string]any{"repo_id": repoID}

	// Count (trashed rows excluded).
	//
	// WORKAROUND for SurrealDB 2.2.1: `SELECT count() ... WHERE x = $y AND
	// deleted_at IS NONE GROUP ALL` silently ignores the IS NONE predicate
	// when combined with an equality filter — returns the full repo total.
	// `array::len((SELECT id FROM ...))` applies the filter correctly and
	// is fast enough on requirement-sized datasets.
	totalCnt, err := queryOne[int](ctx(), db,
		"RETURN array::len((SELECT id FROM ca_requirement WHERE repo_id = $repo_id AND deleted_at IS NONE));", vars)
	total := 0
	if err == nil {
		total = totalCnt
	}

	// Exclude trashed rows. See soft-delete plan §1.4 ("read-path audit").
	sql := "SELECT * FROM ca_requirement WHERE repo_id = $repo_id AND deleted_at IS NONE"
	if limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", limit)
	}
	if offset > 0 {
		sql += fmt.Sprintf(" START %d", offset)
	}

	rows, err := queryOne[[]surrealRequirement](ctx(), db, sql, vars)
	if err != nil {
		return nil, total
	}

	reqs := make([]*graph.StoredRequirement, 0, len(rows))
	for i := range rows {
		reqs = append(reqs, rows[i].toStoredRequirement())
	}
	return reqs, total
}

// GetRequirement returns a requirement by ID.
func (s *SurrealStore) GetRequirement(id string) *graph.StoredRequirement {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealRequirement](ctx(), db,
		"SELECT * FROM type::thing('ca_requirement', $id) WHERE deleted_at IS NONE",
		map[string]any{"id": id})
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rows[0].toStoredRequirement()
}

// GetRequirementsByIDs returns requirements for a batch of IDs in a single query.
func (s *SurrealStore) GetRequirementsByIDs(ids []string) map[string]*graph.StoredRequirement {
	db := s.client.DB()
	if db == nil || len(ids) == 0 {
		return nil
	}

	rows, err := queryOne[[]surrealRequirement](ctx(), db,
		"SELECT * FROM ca_requirement WHERE id IN $ids.map(|$v| type::thing('ca_requirement', $v)) AND deleted_at IS NONE",
		map[string]any{"ids": ids})
	if err != nil {
		slog.Warn("failed to batch fetch requirements", "error", err, "count", len(ids))
		return nil
	}

	result := make(map[string]*graph.StoredRequirement, len(rows))
	for i := range rows {
		req := rows[i].toStoredRequirement()
		result[req.ID] = req
	}
	return result
}

// GetRequirementByExternalID returns a requirement by external ID within a repo.
func (s *SurrealStore) GetRequirementByExternalID(repoID, externalID string) *graph.StoredRequirement {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealRequirement](ctx(), db,
		"SELECT * FROM ca_requirement WHERE repo_id = $repo_id AND external_id = $eid AND deleted_at IS NONE LIMIT 1",
		map[string]any{"repo_id": repoID, "eid": externalID})
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rows[0].toStoredRequirement()
}

// ---------------------------------------------------------------------------
// Link operations
// ---------------------------------------------------------------------------

// linkID builds a deterministic ID from (repoID, requirementID, symbolID)
// so that re-running auto-link produces an UPSERT instead of duplicates.
func linkID(repoID, requirementID, symbolID string) string {
	h := sha256.Sum256([]byte(repoID + "|" + requirementID + "|" + symbolID))
	return hex.EncodeToString(h[:16]) // 128-bit, collision-safe for this use
}

// StoreLink adds or updates a requirement-code link.
func (s *SurrealStore) StoreLink(repoID string, link *graph.StoredLink) *graph.StoredLink {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	lid := linkID(repoID, link.RequirementID, link.SymbolID)

	_, err := surrealdb.Query[interface{}](ctx(), db,
		`UPSERT type::thing('ca_link', $lid) SET
			repo_id = $repo_id,
			requirement_id = $req_id,
			symbol_id = $sym_id,
			confidence = $confidence,
			source = $source,
			link_type = $link_type,
			rationale = $rationale,
			verified = $verified,
			verified_by = $verified_by,
			rejected = $rejected,
			created_at = time::now()`,
		map[string]any{
			"lid":         lid,
			"repo_id":     repoID,
			"req_id":      link.RequirementID,
			"sym_id":      link.SymbolID,
			"confidence":  link.Confidence,
			"source":      link.Source,
			"link_type":   link.LinkType,
			"rationale":   link.Rationale,
			"verified":    link.Verified,
			"verified_by": link.VerifiedBy,
			"rejected":    link.Rejected,
		})
	if err != nil {
		slog.Warn("failed to store link", "error", err)
		return nil
	}

	link.ID = lid
	link.RepoID = repoID
	link.CreatedAt = time.Now().UTC()
	return link
}

// StoreLinks bulk-inserts links in batches using a single SurrealQL query per batch.
func (s *SurrealStore) StoreLinks(repoID string, links []*graph.StoredLink) int {
	db := s.client.DB()
	if db == nil {
		return 0
	}

	const batchSize = 500
	stored := 0

	for i := 0; i < len(links); i += batchSize {
		end := i + batchSize
		if end > len(links) {
			end = len(links)
		}
		batch := links[i:end]

		// Build an array of link objects and use FOR to upsert them.
		linkData := make([]map[string]any, 0, len(batch))
		for _, link := range batch {
			lid := linkID(repoID, link.RequirementID, link.SymbolID)
			linkData = append(linkData, map[string]any{
				"lid":         lid,
				"repo_id":     repoID,
				"req_id":      link.RequirementID,
				"sym_id":      link.SymbolID,
				"confidence":  link.Confidence,
				"source":      link.Source,
				"link_type":   link.LinkType,
				"rationale":   link.Rationale,
				"verified":    link.Verified,
				"verified_by": link.VerifiedBy,
				"rejected":    link.Rejected,
			})
		}

		_, err := surrealdb.Query[interface{}](ctx(), db,
			`FOR $item IN $links {
				UPSERT type::thing('ca_link', $item.lid) SET
					repo_id = $item.repo_id,
					requirement_id = $item.req_id,
					symbol_id = $item.sym_id,
					confidence = $item.confidence,
					source = $item.source,
					link_type = $item.link_type,
					rationale = $item.rationale,
					verified = $item.verified,
					verified_by = $item.verified_by,
					rejected = $item.rejected,
					created_at = time::now();
			}`,
			map[string]any{"links": linkData})
		if err != nil {
			slog.Warn("failed to store link batch", "error", err, "batch_start", i, "batch_size", len(batch))
			continue
		}
		stored += len(batch)

		if (i/batchSize+1)%10 == 0 {
			slog.Info("store_links_progress", "stored", stored, "total", len(links))
		}
	}

	slog.Info("store_links_complete", "stored", stored, "total", len(links))
	return stored
}

// GetLink returns a link by ID.
func (s *SurrealStore) GetLink(id string) *graph.StoredLink {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealLink](ctx(), db,
		"SELECT * FROM type::thing('ca_link', $id) WHERE deleted_at IS NONE",
		map[string]any{"id": id})
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rows[0].toStoredLink()
}

// GetLinksForRequirement returns links for a requirement ID.
func (s *SurrealStore) GetLinksForRequirement(reqID string, includeRejected bool) []*graph.StoredLink {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	sql := "SELECT * FROM ca_link WHERE requirement_id = $req_id AND deleted_at IS NONE"
	if !includeRejected {
		sql += " AND rejected = false"
	}
	sql += " ORDER BY confidence DESC"

	rows, err := queryOne[[]surrealLink](ctx(), db, sql,
		map[string]any{"req_id": reqID})
	if err != nil {
		return nil
	}

	links := make([]*graph.StoredLink, 0, len(rows))
	for i := range rows {
		links = append(links, rows[i].toStoredLink())
	}
	return links
}

// GetLinksForSymbol returns links for a symbol ID.
func (s *SurrealStore) GetLinksForSymbol(symID string, includeRejected bool) []*graph.StoredLink {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	sql := "SELECT * FROM ca_link WHERE symbol_id = $sym_id AND deleted_at IS NONE"
	if !includeRejected {
		sql += " AND rejected = false"
	}

	rows, err := queryOne[[]surrealLink](ctx(), db, sql,
		map[string]any{"sym_id": symID})
	if err != nil {
		return nil
	}

	links := make([]*graph.StoredLink, 0, len(rows))
	for i := range rows {
		links = append(links, rows[i].toStoredLink())
	}
	return links
}

// GetLinksForFile returns links for symbols in a file, optionally filtered by line range.
func (s *SurrealStore) GetLinksForFile(fileID string, startLine, endLine int, minConfidence float64) []*graph.StoredLink {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	// First get symbols in the file matching the line range
	symWhere := "file_id = $file_id"
	vars := map[string]any{"file_id": fileID}
	if startLine > 0 {
		symWhere += " AND end_line >= $start_line"
		vars["start_line"] = startLine
	}
	if endLine > 0 {
		symWhere += " AND start_line <= $end_line"
		vars["end_line"] = endLine
	}

	symRows, err := queryOne[[]surrealSymbol](ctx(), db,
		fmt.Sprintf("SELECT * FROM ca_symbol WHERE %s", symWhere), vars)
	if err != nil || len(symRows) == 0 {
		return nil
	}

	// Collect symbol IDs
	symIDs := make([]string, 0, len(symRows))
	for _, sym := range symRows {
		symIDs = append(symIDs, recordIDString(sym.ID))
	}

	// Get links for those symbols
	linkRows, err := queryOne[[]surrealLink](ctx(), db,
		"SELECT * FROM ca_link WHERE symbol_id IN $sym_ids AND rejected = false AND deleted_at IS NONE AND confidence >= $min_conf",
		map[string]any{
			"sym_ids":  symIDs,
			"min_conf": minConfidence,
		})
	if err != nil {
		return nil
	}

	links := make([]*graph.StoredLink, 0, len(linkRows))
	for i := range linkRows {
		links = append(links, linkRows[i].toStoredLink())
	}
	return links
}

// VerifyLink marks a link as verified or rejected.
func (s *SurrealStore) VerifyLink(linkID string, verified bool, verifiedBy string) *graph.StoredLink {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	// Guard against updating a trashed link. deleted_at IS NONE.
	var sql string
	if verified {
		sql = `UPDATE type::thing('ca_link', $id) SET verified = true, rejected = false, confidence = 1.0, verified_by = $by WHERE deleted_at IS NONE`
	} else {
		sql = `UPDATE type::thing('ca_link', $id) SET rejected = true, verified = false, verified_by = $by WHERE deleted_at IS NONE`
	}

	_, err := surrealdb.Query[interface{}](ctx(), db, sql,
		map[string]any{"id": linkID, "by": verifiedBy})
	if err != nil {
		slog.Warn("verify link failed", "error", err)
		return nil
	}

	return s.GetLink(linkID)
}

// GetLinksForRepo returns all non-rejected links for a repository.
func (s *SurrealStore) GetLinksForRepo(repoID string) []*graph.StoredLink {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealLink](ctx(), db,
		"SELECT * FROM ca_link WHERE repo_id = $repo_id AND rejected = false AND deleted_at IS NONE",
		map[string]any{"repo_id": repoID})
	if err != nil {
		return nil
	}

	links := make([]*graph.StoredLink, 0, len(rows))
	for i := range rows {
		links = append(links, rows[i].toStoredLink())
	}
	return links
}

// GetSymbol returns a single symbol by ID.
func (s *SurrealStore) GetSymbol(id string) *graph.StoredSymbol {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealSymbol](ctx(), db,
		"SELECT * FROM ca_symbol WHERE id = type::thing('ca_symbol', $id) LIMIT 1",
		map[string]any{"id": id})
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rows[0].toStoredSymbol()
}

// GetSymbolsByIDs returns symbols for a batch of IDs in a single query.
func (s *SurrealStore) GetSymbolsByIDs(ids []string) map[string]*graph.StoredSymbol {
	db := s.client.DB()
	if db == nil || len(ids) == 0 {
		return nil
	}

	// Use $ids.map(|$v| type::thing('ca_symbol', $v)) to convert string IDs to record IDs.
	rows, err := queryOne[[]surrealSymbol](ctx(), db,
		"SELECT * FROM ca_symbol WHERE id IN $ids.map(|$v| type::thing('ca_symbol', $v))",
		map[string]any{"ids": ids})
	if err != nil {
		slog.Warn("failed to batch fetch symbols", "error", err, "count", len(ids))
		return nil
	}

	result := make(map[string]*graph.StoredSymbol, len(rows))
	for i := range rows {
		sym := rows[i].toStoredSymbol()
		result[sym.ID] = sym
	}
	return result
}

// GetSymbolsByFile returns all symbols in a repository for a given file path.
func (s *SurrealStore) GetSymbolsByFile(repoID string, filePath string) []*graph.StoredSymbol {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealSymbol](ctx(), db,
		"SELECT * FROM ca_symbol WHERE repo_id = $repo_id AND file_path = $file_path ORDER BY start_line",
		map[string]any{"repo_id": repoID, "file_path": filePath})
	if err != nil {
		return nil
	}

	symbols := make([]*graph.StoredSymbol, 0, len(rows))
	for i := range rows {
		symbols = append(symbols, rows[i].toStoredSymbol())
	}
	return symbols
}

// UpdateRequirement updates the priority and tags on an existing requirement.
func (s *SurrealStore) UpdateRequirement(id string, priority string, tags []string) *graph.StoredRequirement {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	// Refuse to update a trashed requirement. The mutation returns nil
	// and the caller gets "requirement not found" — correct behaviour
	// because from the caller's perspective the row is deleted.
	rows, err := queryOne[[]surrealRequirement](ctx(), db,
		"UPDATE type::thing('ca_requirement', $id) SET priority = $priority, tags = $tags, updated_at = time::now() WHERE deleted_at IS NONE RETURN AFTER",
		map[string]any{"id": id, "priority": priority, "tags": tags})
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rows[0].toStoredRequirement()
}

// ---------------------------------------------------------------------------
// LLM Usage tracking
// ---------------------------------------------------------------------------

// StoreLLMUsage records an LLM API call.
func (s *SurrealStore) StoreLLMUsage(record *graph.LLMUsageRecord) {
	db := s.client.DB()
	if db == nil {
		return
	}

	record.ID = uuid.New().String()

	_, _ = surrealdb.Query[interface{}](ctx(), db,
		`CREATE ca_llm_usage SET
			id = type::thing('ca_llm_usage', $uid),
			repo_id = $repo_id,
			user_id = $user_id,
			provider = $provider,
			model = $model,
			operation = $operation,
			input_tokens = $input_tokens,
			output_tokens = $output_tokens,
			created_at = time::now()`,
		map[string]any{
			"uid":           record.ID,
			"repo_id":       record.RepoID,
			"user_id":       record.UserID,
			"provider":      record.Provider,
			"model":         record.Model,
			"operation":     record.Operation,
			"input_tokens":  record.InputTokens,
			"output_tokens": record.OutputTokens,
		})
}

// GetLLMUsage returns LLM usage records, optionally filtered by repoID.
func (s *SurrealStore) GetLLMUsage(repoID string, limit int) []graph.LLMUsageRecord {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	sql := "SELECT * FROM ca_llm_usage"
	vars := map[string]any{}
	if repoID != "" {
		sql += " WHERE repo_id = $repo_id"
		vars["repo_id"] = repoID
	}
	sql += " ORDER BY created_at DESC"
	if limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", limit)
	}

	type usageRow struct {
		ID           *models.RecordID `json:"id,omitempty"`
		RepoID       string           `json:"repo_id"`
		UserID       string           `json:"user_id"`
		Provider     string           `json:"provider"`
		Model        string           `json:"model"`
		Operation    string           `json:"operation"`
		InputTokens  int              `json:"input_tokens"`
		OutputTokens int              `json:"output_tokens"`
		CreatedAt    surrealTime      `json:"created_at"`
	}

	rows, err := queryOne[[]usageRow](ctx(), db, sql, vars)
	if err != nil {
		return nil
	}

	results := make([]graph.LLMUsageRecord, 0, len(rows))
	for _, r := range rows {
		rec := graph.LLMUsageRecord{
			ID:           recordIDString(r.ID),
			RepoID:       r.RepoID,
			UserID:       r.UserID,
			Provider:     r.Provider,
			Model:        r.Model,
			Operation:    r.Operation,
			InputTokens:  r.InputTokens,
			OutputTokens: r.OutputTokens,
			CreatedAt:    r.CreatedAt.Time,
		}
		results = append(results, rec)
	}
	return results
}

// ---------------------------------------------------------------------------
// Embedding cache
// ---------------------------------------------------------------------------

// StoreEmbedding caches an embedding vector.
func (s *SurrealStore) StoreEmbedding(record *graph.EmbeddingRecord) {
	db := s.client.DB()
	if db == nil {
		return
	}

	record.ID = uuid.New().String()

	// Convert float32 to float64 for SurrealDB
	vec64 := make([]float64, len(record.Vector))
	for i, v := range record.Vector {
		vec64[i] = float64(v)
	}

	// Upsert by target_id — only keep the latest embedding per target
	_, _ = surrealdb.Query[interface{}](ctx(), db,
		`DELETE ca_embedding WHERE target_id = $target_id;
		 CREATE ca_embedding SET
			id = type::thing('ca_embedding', $eid),
			target_id = $target_id,
			target_type = $target_type,
			vector = $vector,
			dimension = $dimension,
			model = $model,
			text_hash = $text_hash,
			created_at = time::now()`,
		map[string]any{
			"eid":         record.ID,
			"target_id":   record.TargetID,
			"target_type": record.TargetType,
			"vector":      vec64,
			"dimension":   record.Dimension,
			"model":       record.Model,
			"text_hash":   record.TextHash,
		})
}

// GetEmbedding retrieves a cached embedding by target ID.
func (s *SurrealStore) GetEmbedding(targetID string) *graph.EmbeddingRecord {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	type embRow struct {
		ID         *models.RecordID `json:"id,omitempty"`
		TargetID   string           `json:"target_id"`
		TargetType string           `json:"target_type"`
		Vector     []float64        `json:"vector"`
		Dimension  int              `json:"dimension"`
		Model      string           `json:"model"`
		TextHash   string           `json:"text_hash"`
		CreatedAt  surrealTime      `json:"created_at"`
	}

	rows, err := queryOne[[]embRow](ctx(), db,
		"SELECT * FROM ca_embedding WHERE target_id = $target_id LIMIT 1",
		map[string]any{"target_id": targetID})
	if err != nil || len(rows) == 0 {
		return nil
	}

	r := rows[0]
	vec32 := make([]float32, len(r.Vector))
	for i, v := range r.Vector {
		vec32[i] = float32(v)
	}
	return &graph.EmbeddingRecord{
		ID:         recordIDString(r.ID),
		TargetID:   r.TargetID,
		TargetType: r.TargetType,
		Vector:     vec32,
		Dimension:  r.Dimension,
		Model:      r.Model,
		TextHash:   r.TextHash,
		CreatedAt:  r.CreatedAt.Time,
	}
}

// ---------------------------------------------------------------------------
// Review results
// ---------------------------------------------------------------------------

// StoreReviewResult persists an AI code review result.
func (s *SurrealStore) StoreReviewResult(record *graph.ReviewResultRecord) {
	db := s.client.DB()
	if db == nil {
		return
	}

	record.ID = uuid.New().String()

	_, _ = surrealdb.Query[interface{}](ctx(), db,
		`CREATE ca_review_result SET
			id = type::thing('ca_review_result', $rid),
			repo_id = $repo_id,
			target_id = $target_id,
			template = $template,
			findings = $findings,
			score = $score,
			created_by = $created_by,
			created_at = time::now()`,
		map[string]any{
			"rid":        record.ID,
			"repo_id":    record.RepoID,
			"target_id":  record.TargetID,
			"template":   record.Template,
			"findings":   record.Findings,
			"score":      record.Score,
			"created_by": record.CreatedBy,
		})
}

// GetReviewResultsForRepo returns all review results for a given repository.
func (s *SurrealStore) GetReviewResultsForRepo(repoID string) []*graph.ReviewResultRecord {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	type reviewRow struct {
		ID        *models.RecordID `json:"id,omitempty"`
		RepoID    string           `json:"repo_id"`
		TargetID  string           `json:"target_id"`
		Template  string           `json:"template"`
		Score     *float64         `json:"score"`
		CreatedBy string           `json:"created_by"`
		CreatedAt surrealTime      `json:"created_at"`
	}

	rows, err := queryOne[[]reviewRow](ctx(), db,
		"SELECT * FROM ca_review_result WHERE repo_id = $repo_id ORDER BY created_at DESC",
		map[string]any{"repo_id": repoID})
	if err != nil {
		return nil
	}

	results := make([]*graph.ReviewResultRecord, 0, len(rows))
	for _, r := range rows {
		rec := &graph.ReviewResultRecord{
			ID:        recordIDString(r.ID),
			RepoID:    r.RepoID,
			TargetID:  r.TargetID,
			Template:  r.Template,
			Score:     r.Score,
			CreatedBy: r.CreatedBy,
			CreatedAt: r.CreatedAt.Time,
		}
		results = append(results, rec)
	}
	return results
}

// GetPublicSymbolDocCoverage returns the count of public symbols with doc comments
// and the total count of public symbols for a repository.
func (s *SurrealStore) GetPublicSymbolDocCoverage(repoID string) (withDocs int, total int) {
	db := s.client.DB()
	if db == nil {
		return 0, 0
	}

	// Fetch all symbols for the repo and apply visibility rules in Go
	rows, err := queryOne[[]surrealSymbol](ctx(), db,
		"SELECT * FROM ca_symbol WHERE repo_id = $repo_id",
		map[string]any{"repo_id": repoID})
	if err != nil {
		return 0, 0
	}

	for i := range rows {
		sym := rows[i].toStoredSymbol()
		if !graph.IsPublicSymbol(sym) {
			continue
		}
		total++
		if strings.TrimSpace(sym.DocComment) != "" {
			withDocs++
		}
	}
	return
}

// GetTestSymbolRatio returns the count of test symbols and total symbols for a repository.
func (s *SurrealStore) GetTestSymbolRatio(repoID string) (tests int, total int) {
	db := s.client.DB()
	if db == nil {
		return 0, 0
	}

	type countRow struct {
		Total int `json:"total"`
	}

	// Total symbols
	totalRows, err := queryOne[[]countRow](ctx(), db,
		"SELECT count() AS total FROM ca_symbol WHERE repo_id = $repo_id GROUP ALL",
		map[string]any{"repo_id": repoID})
	if err == nil && len(totalRows) > 0 {
		total = totalRows[0].Total
	}

	// Test symbols
	testRows, err := queryOne[[]countRow](ctx(), db,
		"SELECT count() AS total FROM ca_symbol WHERE repo_id = $repo_id AND is_test = true GROUP ALL",
		map[string]any{"repo_id": repoID})
	if err == nil && len(testRows) > 0 {
		tests = testRows[0].Total
	}

	return
}

// GetAICodeFileRatio returns the count of AI-generated files (ai_score > 0.5) and total files.
func (s *SurrealStore) GetAICodeFileRatio(repoID string) (aiFiles int, totalFiles int) {
	db := s.client.DB()
	if db == nil {
		return 0, 0
	}

	type countRow struct {
		Total int `json:"total"`
	}

	// Total files
	rows, err := queryOne[[]countRow](ctx(), db,
		"SELECT count() AS total FROM ca_file WHERE repo_id = $repo_id GROUP ALL",
		map[string]any{"repo_id": repoID})
	if err == nil && len(rows) > 0 {
		totalFiles = rows[0].Total
	}

	// AI files
	aiRows, err := queryOne[[]countRow](ctx(), db,
		"SELECT count() AS total FROM ca_file WHERE repo_id = $repo_id AND ai_score > 0.5 GROUP ALL",
		map[string]any{"repo_id": repoID})
	if err == nil && len(aiRows) > 0 {
		aiFiles = aiRows[0].Total
	}

	return
}

// GetReviewResults returns review results for a target (symbol or file).
func (s *SurrealStore) GetReviewResults(targetID string) []*graph.ReviewResultRecord {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	type reviewRow struct {
		ID        *models.RecordID `json:"id,omitempty"`
		RepoID    string           `json:"repo_id"`
		TargetID  string           `json:"target_id"`
		Template  string           `json:"template"`
		Findings  []interface{}    `json:"findings"`
		Score     *float64         `json:"score"`
		CreatedBy string           `json:"created_by"`
		CreatedAt surrealTime      `json:"created_at"`
	}

	rows, err := queryOne[[]reviewRow](ctx(), db,
		"SELECT * FROM ca_review_result WHERE target_id = $target_id ORDER BY created_at DESC",
		map[string]any{"target_id": targetID})
	if err != nil {
		return nil
	}

	results := make([]*graph.ReviewResultRecord, 0, len(rows))
	for _, r := range rows {
		rec := &graph.ReviewResultRecord{
			ID:        recordIDString(r.ID),
			RepoID:    r.RepoID,
			TargetID:  r.TargetID,
			Template:  r.Template,
			Score:     r.Score,
			CreatedBy: r.CreatedBy,
			CreatedAt: r.CreatedAt.Time,
		}
		results = append(results, rec)
	}
	return results
}

// StoreImpactReport stores an impact report for a repository.
func (s *SurrealStore) StoreImpactReport(repoID string, report *graph.ImpactReport) {
	db := s.client.DB()
	if db == nil {
		return
	}
	// stale_artifact_reasons is an additive, rollback-safe JSON mirror of the
	// rich StaleArtifactReasons slice. Old binaries that don't know about the
	// column still read the legacy stale_artifacts []string column unchanged.
	var reasonsJSON string
	if len(report.StaleArtifactReasons) > 0 {
		if b, err := json.Marshal(report.StaleArtifactReasons); err == nil {
			reasonsJSON = string(b)
		}
	}
	if _, err := surrealdb.Query[interface{}](ctx(), db,
		`CREATE ca_impact_report SET
			report_id = $report_id, repo_id = $repo_id,
			old_commit_sha = $old_sha, new_commit_sha = $new_sha,
			files_changed = $files, symbols_added = $sym_added,
			symbols_modified = $sym_modified, symbols_removed = $sym_removed,
			affected_links = $aff_links, affected_requirements = $aff_reqs,
			stale_artifacts = $stale, stale_artifact_reasons = $reasons,
			computed_at = time::now()`,
		map[string]any{
			"report_id": report.ID, "repo_id": repoID,
			"old_sha": report.OldCommitSHA, "new_sha": report.NewCommitSHA,
			"files": report.FilesChanged, "sym_added": report.SymbolsAdded,
			"sym_modified": report.SymbolsModified, "sym_removed": report.SymbolsRemoved,
			"aff_links": report.AffectedLinks, "aff_reqs": report.AffectedRequirements,
			"stale":   report.StaleArtifacts,
			"reasons": reasonsJSON,
		}); err != nil {
		return
	}
}

// impactReportRow is the shared row shape used by the impact-report read
// paths. stale_artifact_reasons is a JSON string column (additive in
// migration 030); old reports leave it empty.
type impactReportRow struct {
	ReportID             string                      `json:"report_id"`
	RepoID               string                      `json:"repo_id"`
	OldCommitSHA         string                      `json:"old_commit_sha"`
	NewCommitSHA         string                      `json:"new_commit_sha"`
	FilesChanged         []graph.ImpactFileDiff      `json:"files_changed"`
	SymbolsAdded         []graph.ImpactSymbolChange  `json:"symbols_added"`
	SymbolsModified      []graph.ImpactSymbolChange  `json:"symbols_modified"`
	SymbolsRemoved       []graph.ImpactSymbolChange  `json:"symbols_removed"`
	AffectedLinks        []graph.AffectedLink        `json:"affected_links"`
	AffectedRequirements []graph.AffectedRequirement `json:"affected_requirements"`
	StaleArtifacts       []string                    `json:"stale_artifacts"`
	StaleArtifactReasons string                      `json:"stale_artifact_reasons"`
	ComputedAt           surrealTime                 `json:"computed_at"`
}

func (r *impactReportRow) toImpactReport() *graph.ImpactReport {
	report := &graph.ImpactReport{
		ID:                   r.ReportID,
		RepositoryID:         r.RepoID,
		OldCommitSHA:         r.OldCommitSHA,
		NewCommitSHA:         r.NewCommitSHA,
		FilesChanged:         r.FilesChanged,
		SymbolsAdded:         r.SymbolsAdded,
		SymbolsModified:      r.SymbolsModified,
		SymbolsRemoved:       r.SymbolsRemoved,
		AffectedLinks:        r.AffectedLinks,
		AffectedRequirements: r.AffectedRequirements,
		StaleArtifacts:       r.StaleArtifacts,
		ComputedAt:           r.ComputedAt.Time,
	}
	if r.StaleArtifactReasons != "" {
		var parsed []graph.StaleArtifactReason
		if err := json.Unmarshal([]byte(r.StaleArtifactReasons), &parsed); err == nil {
			report.StaleArtifactReasons = parsed
		}
	}
	// Rollback-compat fallback: legacy reports only have the bare ID list.
	// Project those into the rich shape with Blanket=true so consumers see a
	// usable "stale for unknown reason" signal instead of nothing.
	if len(report.StaleArtifactReasons) == 0 && len(report.StaleArtifacts) > 0 {
		projected := make([]graph.StaleArtifactReason, 0, len(report.StaleArtifacts))
		for _, aid := range report.StaleArtifacts {
			if aid == "" {
				continue
			}
			projected = append(projected, graph.StaleArtifactReason{
				ArtifactID: aid,
				Blanket:    true,
				ReportID:   r.ReportID,
			})
		}
		report.StaleArtifactReasons = projected
	}
	return report
}

// GetLatestImpactReport returns the most recent impact report for a repository.
func (s *SurrealStore) GetLatestImpactReport(repoID string) *graph.ImpactReport {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	impRows, err := queryOne[[]impactReportRow](ctx(), db,
		"SELECT * FROM ca_impact_report WHERE repo_id = $repo_id ORDER BY computed_at DESC LIMIT 1",
		map[string]any{"repo_id": repoID})
	if err != nil || len(impRows) == 0 {
		return nil
	}
	return impRows[0].toImpactReport()
}

// GetImpactReports returns impact reports for a repository, most recent first.
func (s *SurrealStore) GetImpactReports(repoID string, limit int) ([]*graph.ImpactReport, int) {
	db := s.client.DB()
	if db == nil {
		return nil, 0
	}
	if limit <= 0 {
		limit = 10
	}
	impRows, err := queryOne[[]impactReportRow](ctx(), db,
		"SELECT * FROM ca_impact_report WHERE repo_id = $repo_id ORDER BY computed_at DESC LIMIT $lim",
		map[string]any{"repo_id": repoID, "lim": limit})
	if err != nil {
		return nil, 0
	}
	out := make([]*graph.ImpactReport, 0, len(impRows))
	for _, r := range impRows {
		out = append(out, r.toImpactReport())
	}
	return out, len(out)
}

// ---------------------------------------------------------------------------
// Discovered Requirement operations (spec extraction)
// ---------------------------------------------------------------------------

func (s *SurrealStore) StoreDiscoveredRequirement(repoID string, req *graph.DiscoveredRequirement) {
	db := s.client.DB()
	if db == nil {
		return
	}
	reqID := uuid.New().String()
	if req.Status == "" {
		req.Status = "discovered"
	}
	sourceFiles := req.SourceFiles
	if sourceFiles == nil {
		sourceFiles = []string{}
	}
	keywords := req.Keywords
	if keywords == nil {
		keywords = []string{}
	}

	_, err := surrealdb.Query[interface{}](ctx(), db,
		`CREATE ca_discovered_requirement SET
			id = type::thing('ca_discovered_requirement', $rid),
			repo_id = $repo_id,
			source = $source,
			source_file = $source_file,
			source_line = $source_line,
			source_files = $source_files,
			text = $text,
			raw_text = $raw_text,
			group_key = $group_key,
			language = $language,
			keywords = $keywords,
			confidence = $confidence,
			status = $status,
			llm_refined = $llm_refined,
			created_at = time::now()`,
		map[string]any{
			"rid": reqID, "repo_id": repoID,
			"source": req.Source, "source_file": req.SourceFile,
			"source_line": req.SourceLine, "source_files": sourceFiles,
			"text": req.Text, "raw_text": req.RawText,
			"group_key": req.GroupKey, "language": req.Language,
			"keywords": keywords, "confidence": req.Confidence,
			"status": req.Status, "llm_refined": req.LLMRefined,
		})
	if err != nil {
		slog.Error("store_discovered_requirement", "error", err)
		return
	}
	req.ID = "ca_discovered_requirement:" + reqID
}

func (s *SurrealStore) StoreDiscoveredRequirements(repoID string, reqs []*graph.DiscoveredRequirement) int {
	count := 0
	for _, req := range reqs {
		s.StoreDiscoveredRequirement(repoID, req)
		if req.ID != "" {
			count++
		}
	}
	return count
}

func (s *SurrealStore) GetDiscoveredRequirements(repoID string, status *string, confidence *string, limit, offset int) ([]*graph.DiscoveredRequirement, int) {
	db := s.client.DB()
	if db == nil {
		return nil, 0
	}

	type discRow struct {
		ID              string      `json:"id"`
		RepoID          string      `json:"repo_id"`
		Source          string      `json:"source"`
		SourceFile      string      `json:"source_file"`
		SourceLine      int         `json:"source_line"`
		SourceFiles     []string    `json:"source_files"`
		Text            string      `json:"text"`
		RawText         string      `json:"raw_text"`
		GroupKey        string      `json:"group_key"`
		Language        string      `json:"language"`
		Keywords        []string    `json:"keywords"`
		Confidence      string      `json:"confidence"`
		Status          string      `json:"status"`
		LLMRefined      bool        `json:"llm_refined"`
		PromotedTo      string      `json:"promoted_to"`
		DismissedBy     string      `json:"dismissed_by"`
		DismissedReason string      `json:"dismissed_reason"`
		CreatedAt       surrealTime `json:"created_at"`
	}

	where := "WHERE repo_id = $repo_id"
	vars := map[string]any{"repo_id": repoID}
	if status != nil {
		where += " AND status = $status"
		vars["status"] = *status
	}
	if confidence != nil {
		where += " AND confidence = $confidence"
		vars["confidence"] = *confidence
	}

	// Count
	countRows, err := queryOne[[]map[string]interface{}](ctx(), db,
		"SELECT count() AS total FROM ca_discovered_requirement "+where+" GROUP ALL", vars)
	total := 0
	if err == nil && len(countRows) > 0 {
		if v, ok := countRows[0]["total"]; ok {
			switch vt := v.(type) {
			case float64:
				total = int(vt)
			case int:
				total = vt
			case uint64:
				total = int(vt)
			}
		}
	}

	q := "SELECT * FROM ca_discovered_requirement " + where + " ORDER BY confidence DESC, created_at DESC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	if offset > 0 {
		q += fmt.Sprintf(" START %d", offset)
	}

	rows, err := queryOne[[]discRow](ctx(), db, q, vars)
	if err != nil {
		return nil, total
	}

	var result []*graph.DiscoveredRequirement
	for _, r := range rows {
		result = append(result, &graph.DiscoveredRequirement{
			ID: r.ID, RepoID: r.RepoID, Source: r.Source,
			SourceFile: r.SourceFile, SourceLine: r.SourceLine, SourceFiles: r.SourceFiles,
			Text: r.Text, RawText: r.RawText, GroupKey: r.GroupKey,
			Language: r.Language, Keywords: r.Keywords, Confidence: r.Confidence,
			Status: r.Status, LLMRefined: r.LLMRefined,
			PromotedTo: r.PromotedTo, DismissedBy: r.DismissedBy,
			DismissedReason: r.DismissedReason, CreatedAt: r.CreatedAt.Time,
		})
	}
	return result, total
}

func (s *SurrealStore) GetDiscoveredRequirement(id string) *graph.DiscoveredRequirement {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	type discRow struct {
		ID              string      `json:"id"`
		RepoID          string      `json:"repo_id"`
		Source          string      `json:"source"`
		SourceFile      string      `json:"source_file"`
		SourceLine      int         `json:"source_line"`
		SourceFiles     []string    `json:"source_files"`
		Text            string      `json:"text"`
		RawText         string      `json:"raw_text"`
		GroupKey        string      `json:"group_key"`
		Language        string      `json:"language"`
		Keywords        []string    `json:"keywords"`
		Confidence      string      `json:"confidence"`
		Status          string      `json:"status"`
		LLMRefined      bool        `json:"llm_refined"`
		PromotedTo      string      `json:"promoted_to"`
		DismissedBy     string      `json:"dismissed_by"`
		DismissedReason string      `json:"dismissed_reason"`
		CreatedAt       surrealTime `json:"created_at"`
	}
	rows, err := queryOne[[]discRow](ctx(), db, "SELECT * FROM $id", map[string]any{"id": id})
	if err != nil || len(rows) == 0 {
		return nil
	}
	r := rows[0]
	return &graph.DiscoveredRequirement{
		ID: r.ID, RepoID: r.RepoID, Source: r.Source,
		SourceFile: r.SourceFile, SourceLine: r.SourceLine, SourceFiles: r.SourceFiles,
		Text: r.Text, RawText: r.RawText, GroupKey: r.GroupKey,
		Language: r.Language, Keywords: r.Keywords, Confidence: r.Confidence,
		Status: r.Status, LLMRefined: r.LLMRefined,
		PromotedTo: r.PromotedTo, DismissedBy: r.DismissedBy,
		DismissedReason: r.DismissedReason, CreatedAt: r.CreatedAt.Time,
	}
}

func (s *SurrealStore) PromoteDiscoveredRequirement(id string, requirementID string) *graph.DiscoveredRequirement {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	_, err := surrealdb.Query[interface{}](ctx(), db,
		`UPDATE $id SET status = 'promoted', promoted_to = $req_id, promoted_at = time::now()`,
		map[string]any{"id": id, "req_id": requirementID})
	if err != nil {
		slog.Error("promote_discovered_requirement", "error", err)
		return nil
	}
	return s.GetDiscoveredRequirement(id)
}

func (s *SurrealStore) DismissDiscoveredRequirement(id string, dismissedBy string, reason string) *graph.DiscoveredRequirement {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	_, err := surrealdb.Query[interface{}](ctx(), db,
		`UPDATE $id SET status = 'dismissed', dismissed_by = $by, dismissed_reason = $reason, dismissed_at = time::now()`,
		map[string]any{"id": id, "by": dismissedBy, "reason": reason})
	if err != nil {
		slog.Error("dismiss_discovered_requirement", "error", err)
		return nil
	}
	return s.GetDiscoveredRequirement(id)
}

func (s *SurrealStore) DeleteDiscoveredRequirementsByRepo(repoID string) int {
	db := s.client.DB()
	if db == nil {
		return 0
	}
	_, err := surrealdb.Query[interface{}](ctx(), db,
		`DELETE FROM ca_discovered_requirement WHERE repo_id = $repo_id AND status = 'discovered'`,
		map[string]any{"repo_id": repoID})
	if err != nil {
		slog.Error("delete_discovered_requirements", "error", err)
		return 0
	}
	return -1 // SurrealDB DELETE doesn't return count easily
}
