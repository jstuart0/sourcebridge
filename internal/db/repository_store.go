// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	surrealdb "github.com/surrealdb/surrealdb.go"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// ---------------------------------------------------------------------------
// Repository operations
// ---------------------------------------------------------------------------

// CreateRepository creates a placeholder repository with PENDING status.
func (s *SurrealStore) CreateRepository(ctx context.Context, name, path string) (*graph.Repository, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}

	repoID := uuid.New().String()

	_, err := surrealdb.Query[interface{}](ctx, db,
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

// UpdateRepositoryMeta updates mutable metadata fields on a repository.
func (s *SurrealStore) UpdateRepositoryMeta(ctx context.Context, id string, meta graph.RepositoryMeta) {
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
	_, _ = surrealdb.Query[interface{}](ctx, db, sql, vars)
}

// ListRepositories returns all repositories.
func (s *SurrealStore) ListRepositories(ctx context.Context) []*graph.Repository {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealRepo](ctx, db,
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
func (s *SurrealStore) GetRepository(ctx context.Context, id string) *graph.Repository {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealRepo](ctx, db,
		"SELECT * FROM type::thing('ca_repository', $id)",
		map[string]any{"id": id})
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rows[0].toRepository()
}

// GetRepositoryByPath returns a repository by its path.
func (s *SurrealStore) GetRepositoryByPath(ctx context.Context, path string) *graph.Repository {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealRepo](ctx, db,
		"SELECT * FROM ca_repository WHERE path = $path LIMIT 1",
		map[string]any{"path": path})
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rows[0].toRepository()
}

// RemoveRepository removes a repository and all its data.
func (s *SurrealStore) RemoveRepository(ctx context.Context, id string) bool {
	db := s.client.DB()
	if db == nil {
		return false
	}

	// Remove cluster data first (stale invalidation). Logged on failure; a
	// failed delete here means orphaned cluster records, not a missing repo.
	if err := s.DeleteClusters(ctx, id); err != nil {
		slog.Warn("RemoveRepository: failed to delete clusters; orphaned records may remain",
			"repo_id", id, "error", err)
	}

	_, err := surrealdb.Query[interface{}](ctx, db,
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
func (s *SurrealStore) GetFiles(ctx context.Context, repoID string) []*graph.File {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealFile](ctx, db,
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
func (s *SurrealStore) GetFilesPaginated(ctx context.Context, repoID string, pathPrefix *string, limit, offset int) ([]*graph.File, int) {
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
	countRows, err := queryOne[[]map[string]interface{}](ctx, db,
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

	rows, err := queryOne[[]surrealFile](ctx, db, sql, vars)
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
func (s *SurrealStore) GetSymbols(ctx context.Context, repoID string, query *string, kind *string, limit, offset int) ([]*graph.StoredSymbol, int) {
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
	countRows, err := queryOne[[]map[string]interface{}](ctx, db,
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

	rows, err := queryOne[[]surrealSymbol](ctx, db, sql, vars)
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
func (s *SurrealStore) GetFileSymbols(ctx context.Context, fileID string) []*graph.StoredSymbol {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealSymbol](ctx, db,
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
func (s *SurrealStore) GetModules(ctx context.Context, repoID string) []*graph.StoredModule {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealModule](ctx, db,
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
func (s *SurrealStore) GetCallers(ctx context.Context, symbolID string) []string {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]string](ctx, db,
		"SELECT VALUE caller_id FROM ca_calls WHERE callee_id = $id",
		map[string]any{"id": symbolID})
	if err != nil {
		return nil
	}
	return rows
}

// GetCallees returns the IDs of symbols called by the given symbol.
func (s *SurrealStore) GetCallees(ctx context.Context, symbolID string) []string {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]string](ctx, db,
		"SELECT VALUE callee_id FROM ca_calls WHERE caller_id = $id",
		map[string]any{"id": symbolID})
	if err != nil {
		return nil
	}
	return rows
}

// GetTestsForSymbolPersisted returns the IDs of test symbols that
// exercise the given target symbol, from the ca_tests edge table.
// Parallels GetCallees — edge shape is source_id=test, target_id=tested.
func (s *SurrealStore) GetTestsForSymbolPersisted(ctx context.Context, symbolID string) []string {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	rows, err := queryOne[[]string](ctx, db,
		"SELECT VALUE source_id FROM ca_tests WHERE target_id = $id",
		map[string]any{"id": symbolID})
	if err != nil {
		return nil
	}
	return rows
}

// GetCallEdges returns all call edges for a repository in a single batch.
func (s *SurrealStore) GetCallEdges(ctx context.Context, repoID string) []graph.CallEdge {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	type edgeRow struct {
		CallerID string `json:"caller_id"`
		CalleeID string `json:"callee_id"`
	}

	rows, err := queryOne[[]edgeRow](ctx, db,
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
func (s *SurrealStore) GetImports(ctx context.Context, repoID string) []*graph.StoredImport {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	type importRow struct {
		FileID string `json:"file_id"`
		Path   string `json:"path"`
		Line   int    `json:"line"`
	}
	rows, err := queryOne[[]importRow](ctx, db,
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

// GetPackageDependencies returns all pre-computed package dependency records
// for the given repository from the package_dep table.
func (s *SurrealStore) GetPackageDependencies(ctx context.Context, repoID string) []*graph.StoredPackageDependencies {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	type depRow struct {
		RepoID     string      `json:"repo_id"`
		Package    string      `json:"package"`
		Imports    []string    `json:"imports"`
		ImportedBy []string    `json:"imported_by"`
		UpdatedAt  surrealTime `json:"updated_at"`
	}
	rows, err := queryOne[[]depRow](ctx, db,
		`SELECT * FROM package_dep WHERE repo_id = $repo_id`,
		map[string]any{"repo_id": repoID})
	if err != nil {
		return nil
	}

	result := make([]*graph.StoredPackageDependencies, 0, len(rows))
	for _, r := range rows {
		result = append(result, &graph.StoredPackageDependencies{
			RepoID:     r.RepoID,
			Package:    r.Package,
			Imports:    r.Imports,
			ImportedBy: r.ImportedBy,
			UpdatedAt:  r.UpdatedAt.Time,
		})
	}
	return result
}

// SearchContent searches for symbols and files matching a query string.
func (s *SurrealStore) SearchContent(ctx context.Context, repoID, query string, limit int) []graph.SearchResult {
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
	symRows, err := queryOne[[]surrealSymbol](ctx, db,
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
	fileRows, err := queryOne[[]surrealFile](ctx, db,
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
func (s *SurrealStore) Stats(ctx context.Context) map[string]int {
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
		rows, err := queryOne[[]map[string]interface{}](ctx, db,
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
func (s *SurrealStore) SetRepositoryError(ctx context.Context, id string, repoErr error) {
	db := s.client.DB()
	if db == nil {
		return
	}

	_, _ = surrealdb.Query[interface{}](ctx, db,
		`UPDATE type::thing('ca_repository', $id) SET status = 'error', index_error = $err`,
		map[string]any{
			"id":  id,
			"err": fmt.Sprintf("%v", repoErr),
		})
}

// CacheUnderstandingScore stores the precomputed overall score on the repository.
func (s *SurrealStore) CacheUnderstandingScore(ctx context.Context, id string, overall float64) {
	db := s.client.DB()
	if db == nil {
		return
	}
	_, _ = surrealdb.Query[interface{}](ctx, db,
		`UPDATE type::thing('ca_repository', $id) SET
			understanding_score = $score,
			understanding_score_at = time::now()`,
		map[string]any{
			"id":    id,
			"score": overall,
		})
}
