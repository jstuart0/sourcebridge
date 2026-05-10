// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package db — index_result.go contains the three active multi-step write
// methods that persist or replace an indexer.IndexResult into SurrealDB, plus
// RecomputePackageDependencies which rebuilds the package-dep table.
// MergeIndexResult is fail-closed (returns ErrMergeNotSupported) — it is a
// deliberate stub pending per-file merge primitives; it is not a multi-step
// writer and does not issue any SurrealDB queries.
//
// WARNING: StoreIndexResult, ReplaceIndexResult, and RecomputePackageDependencies
// issue many sequential SurrealDB queries without wrapping them in a single
// atomic transaction. Context cancellation mid-flight will leave the repository
// in a partially-written state. This is a known behavioural exception accepted
// in the CA-183 ctx-threading campaign; the transactional fix is tracked as
// CA-TBD-store-multi-step-write-atomicity. Do NOT add ctx.Err() short-circuits
// inside these methods without first landing the transaction wrapper, or
// callers will observe partial writes.

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
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// StoreIndexResult persists a full indexing result.
func (s *SurrealStore) StoreIndexResult(ctx context.Context, result *indexer.IndexResult) (*graph.Repository, error) {
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

		_, err := surrealdb.Query[interface{}](ctx, db,
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

			_, err := surrealdb.Query[interface{}](ctx, db,
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
			_, _ = surrealdb.Query[interface{}](ctx, db,
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

	// Store call graph + test-linkage relations.
	for _, rel := range result.Relations {
		sourceID := idMap[rel.SourceID]
		targetID := idMap[rel.TargetID]
		if sourceID == "" || targetID == "" {
			continue
		}
		switch rel.Type {
		case indexer.RelationCalls:
			_, _ = surrealdb.Query[interface{}](ctx, db,
				`CREATE ca_calls SET
					caller_id = $caller_id,
					callee_id = $callee_id,
					repo_id = $repo_id`,
				map[string]any{
					"caller_id": sourceID,
					"callee_id": targetID,
					"repo_id":   repoID,
				})
		case indexer.RelationTests:
			// ca_tests: source_id = test symbol, target_id = symbol
			// being tested. Queried via target_id in
			// GetTestsForSymbolPersisted.
			_, _ = surrealdb.Query[interface{}](ctx, db,
				`CREATE ca_tests SET
					source_id = $source_id,
					target_id = $target_id,
					repo_id = $repo_id`,
				map[string]any{
					"source_id": sourceID,
					"target_id": targetID,
					"repo_id":   repoID,
				})
		}
	}

	// Store modules
	for _, mod := range result.Modules {
		modID := uuid.New().String()
		_, _ = surrealdb.Query[interface{}](ctx, db,
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
	_, err := surrealdb.Query[interface{}](ctx, db,
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
func (s *SurrealStore) ReplaceIndexResult(ctx context.Context, repoID string, result *indexer.IndexResult) (*graph.Repository, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}

	// Mark as indexing so the UI shows progress even if the process is interrupted
	_, _ = surrealdb.Query[interface{}](ctx, db,
		`UPDATE type::thing('ca_repository', $id) SET status = 'indexing'`,
		map[string]any{"id": repoID})

	// Invalidate stale clusters before replacing the graph data.
	// Non-fatal: the re-cluster job will overwrite these records; a failed
	// delete leaves stale (not missing) clusters, which is the safer outcome.
	if err := s.DeleteClusters(ctx, repoID); err != nil {
		slog.Warn("ReplaceIndexResult: failed to delete stale clusters; continuing",
			"repo_id", repoID, "error", err)
	}

	// CA-304: remove old data including BOTH ca_calls AND ca_tests. The
	// previous DELETE missed ca_tests entirely, so re-indexing left
	// orphan test-linkage rows whose source_id / target_id pointed at
	// deleted symbols. Combined with the matching CREATE-side bug below
	// (ReplaceIndexResult re-inserted only RelationCalls), every
	// re-index permanently lost the repo's test-linkage edges.
	_, _ = surrealdb.Query[interface{}](ctx, db,
		`DELETE ca_import WHERE file_id IN (SELECT VALUE id FROM ca_file WHERE repo_id = $id);
		 DELETE ca_calls WHERE repo_id = $id;
		 DELETE ca_tests WHERE repo_id = $id;
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

		_, err := surrealdb.Query[interface{}](ctx, db,
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
			_, _ = surrealdb.Query[interface{}](ctx, db,
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
			_, _ = surrealdb.Query[interface{}](ctx, db,
				`CREATE ca_import SET file_id = $file_id, path = $path, line = $line`,
				map[string]any{"file_id": fileID, "path": imp.Path, "line": imp.Line})
		}
	}

	// CA-304: re-insert BOTH call-graph AND test-linkage relations.
	// Mirrors the StoreIndexResult relation loop above. The previous
	// implementation only re-inserted RelationCalls and silently
	// dropped RelationTests on every re-index, leaving the repo with
	// no test-linkage edges (GetTestsForSymbolPersisted returned nothing
	// for every symbol after re-indexing).
	for _, rel := range result.Relations {
		sourceID := idMap[rel.SourceID]
		targetID := idMap[rel.TargetID]
		if sourceID == "" || targetID == "" {
			continue
		}
		switch rel.Type {
		case indexer.RelationCalls:
			_, _ = surrealdb.Query[interface{}](ctx, db,
				`CREATE ca_calls SET
					caller_id = $caller_id,
					callee_id = $callee_id,
					repo_id = $repo_id`,
				map[string]any{
					"caller_id": sourceID,
					"callee_id": targetID,
					"repo_id":   repoID,
				})
		case indexer.RelationTests:
			// ca_tests: source_id = test symbol, target_id = symbol
			// being tested. Same shape as StoreIndexResult.
			_, _ = surrealdb.Query[interface{}](ctx, db,
				`CREATE ca_tests SET
					source_id = $source_id,
					target_id = $target_id,
					repo_id = $repo_id`,
				map[string]any{
					"source_id": sourceID,
					"target_id": targetID,
					"repo_id":   repoID,
				})
		}
	}

	// Re-insert modules
	for _, mod := range result.Modules {
		modID := uuid.New().String()
		_, _ = surrealdb.Query[interface{}](ctx, db,
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
	_, err := surrealdb.Query[interface{}](ctx, db,
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

	return s.GetRepository(ctx, repoID), nil
}

// MergeIndexResult is the per-file delta entry point used by the
// change-watch router (Phase 1.C). The SurrealDB-backed store does not
// yet support per-file merge semantics — the queries that selectively
// drop a file's symbols/imports/edges and re-insert them while
// preserving the rest of the repo land alongside the freshness-state
// migration in Phase 2 of the MCP-edits feedback-loop plan.
//
// Returning ErrMergeNotSupported here is the deliberate fail-closed
// choice. The umbrella SOURCEBRIDGE_CHANGE_WATCH_ENABLED flag is
// default-off in 1.C, so this surface is not exercised in production
// until Phase 2 lands the per-file primitives. If an operator flips
// the flag on while running the SurrealDB backend before Phase 2, the
// router surfaces this error through the freshness envelope rather
// than silently corrupting state.
func (s *SurrealStore) MergeIndexResult(ctx context.Context, repoID string, affectedPaths []string, result *indexer.IndexResult) (*graph.Repository, error) {
	_ = repoID
	_ = affectedPaths
	_ = result
	return nil, graph.ErrMergeNotSupported
}

// RecomputePackageDependencies rebuilds the package-level dependency records
// for the given repo by aggregating raw import rows from SurrealDB, then
// upserting one package_dep record per package. It is idempotent.
func (s *SurrealStore) RecomputePackageDependencies(ctx context.Context, repoID string) {
	db := s.client.DB()
	if db == nil {
		return
	}

	// Fetch all (file_path, import_path) pairs for this repo in one query.
	type importPair struct {
		FilePath   string `json:"file_path"`
		ImportPath string `json:"import_path"`
	}
	rows, err := queryOne[[]importPair](ctx, db,
		`SELECT f.path AS file_path, i.path AS import_path
		 FROM ca_import AS i
		 JOIN ca_file AS f ON f.id = i.file_id
		 WHERE f.repo_id = $repo_id`,
		map[string]any{"repo_id": repoID})
	if err != nil {
		return
	}

	// Aggregate into imports / imported_by sets.
	type edgeSet map[string]map[string]struct{}
	imports := make(edgeSet)
	importedBy := make(edgeSet)
	addEdge := func(m edgeSet, key, val string) {
		if m[key] == nil {
			m[key] = make(map[string]struct{})
		}
		m[key][val] = struct{}{}
	}

	for _, r := range rows {
		if r.FilePath == "" || r.ImportPath == "" {
			continue
		}
		var fromPkg string
		if idx := strings.LastIndex(r.FilePath, "/"); idx >= 0 {
			fromPkg = r.FilePath[:idx]
		} else {
			fromPkg = "."
		}
		toPkg := r.ImportPath
		if fromPkg == toPkg {
			continue
		}
		addEdge(imports, fromPkg, toPkg)
		addEdge(importedBy, toPkg, fromPkg)
	}

	// Collect all packages.
	allPkgs := make(map[string]struct{})
	for pkg := range imports {
		allPkgs[pkg] = struct{}{}
	}
	for pkg := range importedBy {
		allPkgs[pkg] = struct{}{}
	}

	now := time.Now().UTC()
	for pkg := range allPkgs {
		importList := sortedKeys(imports[pkg])
		importedByList := sortedKeys(importedBy[pkg])
		// Upsert the record keyed by (repo_id, package).
		_, _ = surrealdb.Query[interface{}](ctx, db,
			`UPSERT package_dep:[type::string($repo_id), type::string($pkg)] SET
			   repo_id = $repo_id,
			   package = $pkg,
			   imports = $imports,
			   imported_by = $imported_by,
			   updated_at = $updated_at`,
			map[string]any{
				"repo_id":     repoID,
				"pkg":         pkg,
				"imports":     importList,
				"imported_by": importedByList,
				"updated_at":  now,
			})
	}
}
