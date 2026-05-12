// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	surrealdb "github.com/surrealdb/surrealdb.go"

	"github.com/sourcebridge/sourcebridge/internal/db/sqlbuild"
	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// ---------------------------------------------------------------------------
// Requirement operations
// ---------------------------------------------------------------------------

// StoreRequirement adds a requirement to the store.
func (s *SurrealStore) StoreRequirement(ctx context.Context, repoID string, req *graph.StoredRequirement) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
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

	_, err := surrealdb.Query[interface{}](ctx, db,
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
		return err
	}

	req.ID = reqID
	req.RepoID = repoID
	req.CreatedAt = time.Now().UTC()
	req.UpdatedAt = req.CreatedAt
	return nil
}

// StoreRequirements adds multiple requirements and returns the count stored.
func (s *SurrealStore) StoreRequirements(ctx context.Context, repoID string, reqs []*graph.StoredRequirement) (int, error) {
	count := 0
	for _, req := range reqs {
		if err := s.StoreRequirement(ctx, repoID, req); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// GetRequirements returns requirements for a repository with pagination.
func (s *SurrealStore) GetRequirements(ctx context.Context, repoID string, limit, offset int) ([]*graph.StoredRequirement, int) {
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
	totalCnt, err := queryOne[int](ctx, db,
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

	rows, err := queryOne[[]surrealRequirement](ctx, db, sql, vars)
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
func (s *SurrealStore) GetRequirement(ctx context.Context, id string) *graph.StoredRequirement {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealRequirement](ctx, db,
		"SELECT * FROM type::thing('ca_requirement', $id) WHERE deleted_at IS NONE",
		map[string]any{"id": id})
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rows[0].toStoredRequirement()
}

// GetRequirementsByIDs returns requirements for a batch of IDs in a single query.
func (s *SurrealStore) GetRequirementsByIDs(ctx context.Context, ids []string) map[string]*graph.StoredRequirement {
	db := s.client.DB()
	if db == nil || len(ids) == 0 {
		return nil
	}

	rows, err := queryOne[[]surrealRequirement](ctx, db,
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
func (s *SurrealStore) GetRequirementByExternalID(ctx context.Context, repoID, externalID string) *graph.StoredRequirement {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealRequirement](ctx, db,
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

// StoreLink adds or updates a requirement-code link.
func (s *SurrealStore) StoreLink(ctx context.Context, repoID string, link *graph.StoredLink) *graph.StoredLink {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	lid := linkID(repoID, link.RequirementID, link.SymbolID)

	_, err := surrealdb.Query[interface{}](ctx, db,
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
func (s *SurrealStore) StoreLinks(ctx context.Context, repoID string, links []*graph.StoredLink) int {
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

		_, err := surrealdb.Query[interface{}](ctx, db,
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
func (s *SurrealStore) GetLink(ctx context.Context, id string) *graph.StoredLink {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealLink](ctx, db,
		"SELECT * FROM type::thing('ca_link', $id) WHERE deleted_at IS NONE",
		map[string]any{"id": id})
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rows[0].toStoredLink()
}

// GetLinksForRequirement returns links for a requirement ID.
func (s *SurrealStore) GetLinksForRequirement(ctx context.Context, reqID string, includeRejected bool) []*graph.StoredLink {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	sql := "SELECT * FROM ca_link WHERE requirement_id = $req_id AND deleted_at IS NONE"
	if !includeRejected {
		sql += " AND rejected = false"
	}
	sql += " ORDER BY confidence DESC"

	rows, err := queryOne[[]surrealLink](ctx, db, sql,
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
func (s *SurrealStore) GetLinksForSymbol(ctx context.Context, symID string, includeRejected bool) []*graph.StoredLink {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	sql := "SELECT * FROM ca_link WHERE symbol_id = $sym_id AND deleted_at IS NONE"
	if !includeRejected {
		sql += " AND rejected = false"
	}

	rows, err := queryOne[[]surrealLink](ctx, db, sql,
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
func (s *SurrealStore) GetLinksForFile(ctx context.Context, fileID string, startLine, endLine int, minConfidence float64) []*graph.StoredLink {
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

	symRows, err := queryOne[[]surrealSymbol](ctx, db,
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
	linkRows, err := queryOne[[]surrealLink](ctx, db,
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
func (s *SurrealStore) VerifyLink(ctx context.Context, linkID string, verified bool, verifiedBy string) *graph.StoredLink {
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

	_, err := surrealdb.Query[interface{}](ctx, db, sql,
		map[string]any{"id": linkID, "by": verifiedBy})
	if err != nil {
		slog.Warn("verify link failed", "error", err)
		return nil
	}

	return s.GetLink(ctx, linkID)
}

// GetLinksForRepo returns all non-rejected links for a repository.
func (s *SurrealStore) GetLinksForRepo(ctx context.Context, repoID string) []*graph.StoredLink {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealLink](ctx, db,
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
func (s *SurrealStore) GetSymbol(ctx context.Context, id string) *graph.StoredSymbol {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealSymbol](ctx, db,
		"SELECT * FROM ca_symbol WHERE id = type::thing('ca_symbol', $id) LIMIT 1",
		map[string]any{"id": id})
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rows[0].toStoredSymbol()
}

// GetSymbolsByIDs returns symbols for a batch of IDs in a single query.
//
// CA-171 followup: chunked to 500 IDs per query. The single-query form with
// `id IN $ids.map(|$v| type::thing(...))` over ~7-10k IDs (sourcebridge-sized
// repos) overwhelms SurrealDB's per-statement timeout and returns a context-
// deadline-exceeded warning, defeating the N+1 fix's intent. Chunked at 500
// the array map-and-IN comparison stays well inside Surreal's budget while
// the total round-trip cost (~16 sequential queries instead of ~7000) is
// still vastly cheaper than the original per-id N+1.
func (s *SurrealStore) GetSymbolsByIDs(ctx context.Context, ids []string) map[string]*graph.StoredSymbol {
	db := s.client.DB()
	if db == nil || len(ids) == 0 {
		return nil
	}

	const chunk = 500
	result := make(map[string]*graph.StoredSymbol, len(ids))
	for start := 0; start < len(ids); start += chunk {
		end := start + chunk
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		rows, err := queryOne[[]surrealSymbol](ctx, db,
			"SELECT * FROM ca_symbol WHERE id IN $ids.map(|$v| type::thing('ca_symbol', $v))",
			map[string]any{"ids": batch})
		if err != nil {
			slog.Warn("failed to batch fetch symbols",
				"error", err, "chunk_size", len(batch), "total_ids", len(ids))
			continue // partial-result better than nothing on transient errors
		}
		for i := range rows {
			sym := rows[i].toStoredSymbol()
			result[sym.ID] = sym
		}
	}
	return result
}

// GetSymbolsByFile returns all symbols in a repository for a given file path.
func (s *SurrealStore) GetSymbolsByFile(ctx context.Context, repoID string, filePath string) []*graph.StoredSymbol {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealSymbol](ctx, db,
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

// UpdateRequirementFields applies a partial update, preserving any
// field the caller leaves nil. Enforces externalId uniqueness per-repo
// via a pre-check against non-trashed rows.
func (s *SurrealStore) UpdateRequirementFields(ctx context.Context, id string, fields graph.RequirementUpdate) *graph.StoredRequirement {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	// Load current row to preserve non-modified fields and to scope the
	// uniqueness check to the same repo. GetRequirement already filters
	// trashed rows.
	current := s.GetRequirement(ctx, id)
	if current == nil {
		return nil
	}
	if fields.ExternalID != nil && *fields.ExternalID != "" && *fields.ExternalID != current.ExternalID {
		// Soft-delete aware uniqueness — see plan §1.4 on read-path filters.
		existing, err := queryOne[int](ctx, db,
			"RETURN array::len((SELECT id FROM ca_requirement WHERE repo_id = $repo AND external_id = $eid AND deleted_at IS NONE));",
			map[string]any{"repo": current.RepoID, "eid": *fields.ExternalID})
		if err == nil && existing > 0 {
			return nil
		}
	}

	// Build a SET clause from the non-nil fields. updated_at is always
	// stamped; Len()==1 after the Add* calls means no substantive change.
	b := sqlbuild.New()
	b.AddRaw("updated_at = time::now()")
	b.AddStringPtr("external_id", fields.ExternalID)
	b.AddStringPtr("title", fields.Title)
	b.AddStringPtr("description", fields.Description)
	b.AddStringPtr("priority", fields.Priority)
	b.AddStringPtr("source", fields.Source)
	b.AddStringsPtr("tags", fields.Tags)
	b.AddStringsPtr("acceptance_criteria", fields.AcceptanceCriteria)

	if b.Len() == 1 {
		// Nothing substantive changed — return the current row.
		return current
	}

	vars := b.Vars()
	vars["id"] = id
	stmt := "UPDATE type::thing('ca_requirement', $id) SET " + b.Clause() + " WHERE deleted_at IS NONE RETURN AFTER"
	rows, err := queryOne[[]surrealRequirement](ctx, db, stmt, vars)
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rows[0].toStoredRequirement()
}

// ---------------------------------------------------------------------------
// Discovered Requirement operations (spec extraction)
// ---------------------------------------------------------------------------

func (s *SurrealStore) StoreDiscoveredRequirement(ctx context.Context, repoID string, req *graph.DiscoveredRequirement) {
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

	_, err := surrealdb.Query[interface{}](ctx, db,
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

func (s *SurrealStore) StoreDiscoveredRequirements(ctx context.Context, repoID string, reqs []*graph.DiscoveredRequirement) int {
	count := 0
	for _, req := range reqs {
		s.StoreDiscoveredRequirement(ctx, repoID, req)
		if req.ID != "" {
			count++
		}
	}
	return count
}

func (s *SurrealStore) GetDiscoveredRequirements(ctx context.Context, repoID string, status *string, confidence *string, limit, offset int) ([]*graph.DiscoveredRequirement, int) {
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
	countRows, err := queryOne[[]map[string]interface{}](ctx, db,
		"SELECT count() AS total FROM ca_discovered_requirement "+where+" GROUP ALL", vars)
	total := 0
	if err == nil && len(countRows) > 0 {
		if v, ok := countRows[0]["total"]; ok {
			total = coerceInt(v)
		}
	}

	q := "SELECT * FROM ca_discovered_requirement " + where + " ORDER BY confidence DESC, created_at DESC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	if offset > 0 {
		q += fmt.Sprintf(" START %d", offset)
	}

	rows, err := queryOne[[]discRow](ctx, db, q, vars)
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

func (s *SurrealStore) GetDiscoveredRequirement(ctx context.Context, id string) *graph.DiscoveredRequirement {
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
	rows, err := queryOne[[]discRow](ctx, db, "SELECT * FROM $id", map[string]any{"id": id})
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

func (s *SurrealStore) PromoteDiscoveredRequirement(ctx context.Context, id string, requirementID string) *graph.DiscoveredRequirement {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	_, err := surrealdb.Query[interface{}](ctx, db,
		`UPDATE $id SET status = 'promoted', promoted_to = $req_id, promoted_at = time::now()`,
		map[string]any{"id": id, "req_id": requirementID})
	if err != nil {
		slog.Error("promote_discovered_requirement", "error", err)
		return nil
	}
	return s.GetDiscoveredRequirement(ctx, id)
}

func (s *SurrealStore) DismissDiscoveredRequirement(ctx context.Context, id string, dismissedBy string, reason string) *graph.DiscoveredRequirement {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	_, err := surrealdb.Query[interface{}](ctx, db,
		`UPDATE $id SET status = 'dismissed', dismissed_by = $by, dismissed_reason = $reason, dismissed_at = time::now()`,
		map[string]any{"id": id, "by": dismissedBy, "reason": reason})
	if err != nil {
		slog.Error("dismiss_discovered_requirement", "error", err)
		return nil
	}
	return s.GetDiscoveredRequirement(ctx, id)
}

func (s *SurrealStore) DeleteDiscoveredRequirementsByRepo(ctx context.Context, repoID string) int {
	db := s.client.DB()
	if db == nil {
		return 0
	}
	_, err := surrealdb.Query[interface{}](ctx, db,
		`DELETE FROM ca_discovered_requirement WHERE repo_id = $repo_id AND status = 'discovered'`,
		map[string]any{"repo_id": repoID})
	if err != nil {
		slog.Error("delete_discovered_requirements", "error", err)
		return 0
	}
	return -1 // SurrealDB DELETE doesn't return count easily
}
