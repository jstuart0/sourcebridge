// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package db — index_result.go contains the three active multi-step write
// methods that persist or replace an indexer.IndexResult into SurrealDB, plus
// RecomputePackageDependencies which rebuilds the package-dep table.
// MergeIndexResult is fail-closed (returns ErrMergeNotSupported) — it is a
// deliberate stub pending per-file merge primitives; it is not a multi-step
// writer and does not issue any SurrealDB queries.
//
// StoreIndexResult, ReplaceIndexResult, and RecomputePackageDependencies are
// atomic: all statements are wrapped in a single BEGIN/COMMIT transaction via
// RunInTxBatch (CA-TBD-store-multi-step-write-atomicity). Context cancellation
// before the batch fires leaves the DB unchanged; cancellation during network
// transmission is handled by the SurrealDB server-side transaction abort.

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

// buildIndexBatch assembles the SQL statements and parameter map for inserting
// all files, symbols, imports, relations, and modules from result into the DB
// under repoID.  The returned statements do NOT include the repository row
// itself — that is appended by the callers.
//
// Parameter names are deduplicated by index so a single flat map[string]any
// covers every statement.
func buildIndexBatch(repoID string, result *indexer.IndexResult) (stmts []string, vars map[string]any, idMap map[string]string, funcCount, classCount int) {
	vars = make(map[string]any)
	vars["repo_id"] = repoID
	idMap = make(map[string]string)

	for fi, fr := range result.Files {
		fileID := uuid.New().String()
		fidKey := fmt.Sprintf("fid_%d", fi)
		pathKey := fmt.Sprintf("fpath_%d", fi)
		langKey := fmt.Sprintf("flang_%d", fi)
		linesKey := fmt.Sprintf("flines_%d", fi)
		hashKey := fmt.Sprintf("fhash_%d", fi)
		scoreKey := fmt.Sprintf("fscore_%d", fi)
		signalsKey := fmt.Sprintf("fsignals_%d", fi)

		vars[fidKey] = fileID
		vars[pathKey] = fr.Path
		vars[langKey] = fr.Language
		vars[linesKey] = fr.LineCount
		vars[hashKey] = fr.ContentHash
		vars[scoreKey] = fr.AIScore
		vars[signalsKey] = fr.AISignals

		stmts = append(stmts, fmt.Sprintf(`CREATE ca_file SET
			id = type::thing('ca_file', $%s),
			repo_id = $repo_id,
			path = $%s,
			language = $%s,
			line_count = $%s,
			content_hash = $%s,
			ai_score = $%s,
			ai_signals = $%s`,
			fidKey, pathKey, langKey, linesKey, hashKey, scoreKey, signalsKey))

		for si, sym := range fr.Symbols {
			symID := uuid.New().String()
			idMap[sym.ID] = symID

			sidKey := fmt.Sprintf("sid_%d_%d", fi, si)
			sfidKey := fmt.Sprintf("sfid_%d_%d", fi, si)
			snameKey := fmt.Sprintf("sname_%d_%d", fi, si)
			sqnameKey := fmt.Sprintf("sqname_%d_%d", fi, si)
			skindKey := fmt.Sprintf("skind_%d_%d", fi, si)
			slangKey := fmt.Sprintf("slang_%d_%d", fi, si)
			sfpathKey := fmt.Sprintf("sfpath_%d_%d", fi, si)
			sstartKey := fmt.Sprintf("sstart_%d_%d", fi, si)
			sendKey := fmt.Sprintf("send_%d_%d", fi, si)
			ssigKey := fmt.Sprintf("ssig_%d_%d", fi, si)
			sdocKey := fmt.Sprintf("sdoc_%d_%d", fi, si)
			sistKey := fmt.Sprintf("sist_%d_%d", fi, si)

			vars[sidKey] = symID
			vars[sfidKey] = fileID
			vars[snameKey] = sym.Name
			vars[sqnameKey] = sym.QualifiedName
			vars[skindKey] = string(sym.Kind)
			vars[slangKey] = sym.Language
			vars[sfpathKey] = sym.FilePath
			vars[sstartKey] = sym.StartLine
			vars[sendKey] = sym.EndLine
			vars[ssigKey] = sym.Signature
			vars[sdocKey] = sym.DocComment
			vars[sistKey] = sym.IsTest

			stmts = append(stmts, fmt.Sprintf(`CREATE ca_symbol SET
				id = type::thing('ca_symbol', $%s),
				repo_id = $repo_id,
				file_id = $%s,
				name = $%s,
				qualified_name = $%s,
				kind = $%s,
				language = $%s,
				file_path = $%s,
				start_line = $%s,
				end_line = $%s,
				signature = $%s,
				doc_comment = $%s,
				is_test = $%s`,
				sidKey, sfidKey, snameKey, sqnameKey, skindKey, slangKey, sfpathKey,
				sstartKey, sendKey, ssigKey, sdocKey, sistKey))

			switch sym.Kind {
			case indexer.SymbolFunction, indexer.SymbolMethod:
				funcCount++
			case indexer.SymbolClass, indexer.SymbolStruct, indexer.SymbolInterface, indexer.SymbolEnum, indexer.SymbolTrait:
				classCount++
			}
		}

		for ii, imp := range fr.Imports {
			impFidKey := fmt.Sprintf("impfid_%d_%d", fi, ii)
			impPathKey := fmt.Sprintf("imppath_%d_%d", fi, ii)
			impLineKey := fmt.Sprintf("impline_%d_%d", fi, ii)

			vars[impFidKey] = fileID
			vars[impPathKey] = imp.Path
			vars[impLineKey] = imp.Line

			stmts = append(stmts, fmt.Sprintf(`CREATE ca_import SET
				file_id = $%s,
				path = $%s,
				line = $%s`,
				impFidKey, impPathKey, impLineKey))
		}
	}

	for ri, rel := range result.Relations {
		sourceID := idMap[rel.SourceID]
		targetID := idMap[rel.TargetID]
		if sourceID == "" || targetID == "" {
			continue
		}

		srcKey := fmt.Sprintf("rsrc_%d", ri)
		tgtKey := fmt.Sprintf("rtgt_%d", ri)
		vars[srcKey] = sourceID
		vars[tgtKey] = targetID

		switch rel.Type {
		case indexer.RelationCalls:
			stmts = append(stmts, fmt.Sprintf(`CREATE ca_calls SET
				caller_id = $%s,
				callee_id = $%s,
				repo_id = $repo_id`, srcKey, tgtKey))
		case indexer.RelationTests:
			stmts = append(stmts, fmt.Sprintf(`CREATE ca_tests SET
				source_id = $%s,
				target_id = $%s,
				repo_id = $repo_id`, srcKey, tgtKey))
		}
	}

	for mi, mod := range result.Modules {
		modID := uuid.New().String()
		midKey := fmt.Sprintf("mid_%d", mi)
		mnameKey := fmt.Sprintf("mname_%d", mi)
		mpathKey := fmt.Sprintf("mpath_%d", mi)
		mfcountKey := fmt.Sprintf("mfc_%d", mi)

		vars[midKey] = modID
		vars[mnameKey] = mod.Name
		vars[mpathKey] = mod.Path
		vars[mfcountKey] = mod.FileCount

		stmts = append(stmts, fmt.Sprintf(`CREATE ca_module SET
			id = type::thing('ca_module', $%s),
			repo_id = $repo_id,
			name = $%s,
			path = $%s,
			file_count = $%s`,
			midKey, mnameKey, mpathKey, mfcountKey))
	}

	return stmts, vars, idMap, funcCount, classCount
}

// StoreIndexResult persists a full indexing result atomically.
func (s *SurrealStore) StoreIndexResult(ctx context.Context, result *indexer.IndexResult) (*graph.Repository, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}

	repoID := uuid.New().String()

	stmts, vars, _, funcCount, classCount := buildIndexBatch(repoID, result)

	// Append the repository CREATE as the final statement in the batch.
	// Use time::now() so SurrealDB generates a native datetime value
	// (passing a Go-formatted string is rejected by SCHEMAFULL datetime fields).
	vars["repo_name"] = result.RepoName
	vars["repo_path"] = result.RepoPath
	vars["repo_file_count"] = result.TotalFiles
	vars["repo_func_count"] = funcCount
	vars["repo_class_count"] = classCount
	vars["repo_id_val"] = repoID

	stmts = append(stmts, `CREATE ca_repository SET
		id = type::thing('ca_repository', $repo_id_val),
		name = $repo_name,
		path = $repo_path,
		status = 'ready',
		file_count = $repo_file_count,
		function_count = $repo_func_count,
		class_count = $repo_class_count,
		last_indexed_at = time::now(),
		created_at = time::now()`)

	if err := s.client.RunInTxBatch(ctx, stmts, vars); err != nil {
		return nil, fmt.Errorf("storing index result: %w", err)
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

	// Mark as indexing so the UI shows progress even if the process is interrupted.
	// This runs outside the main transaction intentionally: the status update is a
	// best-effort progress signal, not a correctness requirement. If it fails the
	// replace still proceeds.
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

	stmts, vars, _, funcCount, classCount := buildIndexBatch(repoID, result)

	// DELETE block comes before the CREATEs so it runs atomically inside the same
	// transaction.  CA-304: delete BOTH ca_calls AND ca_tests so re-indexing
	// does not leave orphan test-linkage rows whose source_id / target_id point
	// at deleted symbols.
	deleteBatch := strings.Join([]string{
		"DELETE ca_import WHERE file_id IN (SELECT VALUE id FROM ca_file WHERE repo_id = $repo_id)",
		"DELETE ca_calls WHERE repo_id = $repo_id",
		"DELETE ca_tests WHERE repo_id = $repo_id",
		"DELETE ca_symbol WHERE repo_id = $repo_id",
		"DELETE ca_module WHERE repo_id = $repo_id",
		"DELETE ca_file WHERE repo_id = $repo_id",
	}, ";\n")

	// Build the full ordered list: deletes first, then creates.
	allStmts := []string{deleteBatch}
	allStmts = append(allStmts, stmts...)

	// Append the repository UPDATE as the final statement.
	vars["repo_file_count"] = result.TotalFiles
	vars["repo_func_count"] = funcCount
	vars["repo_class_count"] = classCount

	allStmts = append(allStmts, `UPDATE type::thing('ca_repository', $repo_id) SET
		status = 'ready',
		file_count = $repo_file_count,
		function_count = $repo_func_count,
		class_count = $repo_class_count,
		last_indexed_at = time::now(),
		index_error = NONE`)

	if err := s.client.RunInTxBatch(ctx, allStmts, vars); err != nil {
		return nil, fmt.Errorf("replacing index result: %w", err)
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
// upserting one package_dep record per package atomically. It is idempotent.
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

	if len(allPkgs) == 0 {
		return
	}

	// Build one UPSERT statement per package, all in a single transaction.
	now := time.Now().UTC()
	stmts := make([]string, 0, len(allPkgs))
	vars := map[string]any{"repo_id": repoID, "updated_at": now}

	i := 0
	for pkg := range allPkgs {
		importList := sortedKeys(imports[pkg])
		importedByList := sortedKeys(importedBy[pkg])

		pkgKey := fmt.Sprintf("pkg_%d", i)
		importsKey := fmt.Sprintf("imports_%d", i)
		importedByKey := fmt.Sprintf("importedBy_%d", i)

		vars[pkgKey] = pkg
		vars[importsKey] = importList
		vars[importedByKey] = importedByList

		stmts = append(stmts, fmt.Sprintf(
			`UPSERT package_dep:[type::string($repo_id), type::string($%s)] SET
			   repo_id = $repo_id,
			   package = $%s,
			   imports = $%s,
			   imported_by = $%s,
			   updated_at = $updated_at`,
			pkgKey, pkgKey, importsKey, importedByKey))
		i++
	}

	if err := s.client.RunInTxBatch(ctx, stmts, vars); err != nil {
		slog.Warn("RecomputePackageDependencies: transaction failed", "repo_id", repoID, "error", err)
	}
}
