// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	surrealdb "github.com/surrealdb/surrealdb.go"
	"github.com/surrealdb/surrealdb.go/pkg/models"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// ---------------------------------------------------------------------------
// LLM Usage tracking
// ---------------------------------------------------------------------------

// StoreLLMUsage records an LLM API call.
func (s *SurrealStore) StoreLLMUsage(ctx context.Context, record *graph.LLMUsageRecord) {
	db := s.client.DB()
	if db == nil {
		return
	}

	record.ID = uuid.New().String()

	_, _ = surrealdb.Query[interface{}](ctx, db,
		`CREATE ca_llm_usage SET
			id = type::thing('ca_llm_usage', $uid),
			repo_id = $repo_id,
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
func (s *SurrealStore) GetLLMUsage(ctx context.Context, repoID string, limit int) []graph.LLMUsageRecord {
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

	rows, err := queryOne[[]usageRow](ctx, db, sql, vars)
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
func (s *SurrealStore) StoreEmbedding(ctx context.Context, record *graph.EmbeddingRecord) {
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
	_, _ = surrealdb.Query[interface{}](ctx, db,
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
func (s *SurrealStore) GetEmbedding(ctx context.Context, targetID string) *graph.EmbeddingRecord {
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

	rows, err := queryOne[[]embRow](ctx, db,
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
func (s *SurrealStore) StoreReviewResult(ctx context.Context, record *graph.ReviewResultRecord) {
	db := s.client.DB()
	if db == nil {
		return
	}

	record.ID = uuid.New().String()

	_, _ = surrealdb.Query[interface{}](ctx, db,
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
func (s *SurrealStore) GetReviewResultsForRepo(ctx context.Context, repoID string) []*graph.ReviewResultRecord {
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

	rows, err := queryOne[[]reviewRow](ctx, db,
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
func (s *SurrealStore) GetPublicSymbolDocCoverage(ctx context.Context, repoID string) (withDocs int, total int) {
	db := s.client.DB()
	if db == nil {
		return 0, 0
	}

	// Fetch all symbols for the repo and apply visibility rules in Go
	rows, err := queryOne[[]surrealSymbol](ctx, db,
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
func (s *SurrealStore) GetTestSymbolRatio(ctx context.Context, repoID string) (tests int, total int) {
	db := s.client.DB()
	if db == nil {
		return 0, 0
	}

	type countRow struct {
		Total int `json:"total"`
	}

	// Total symbols
	totalRows, err := queryOne[[]countRow](ctx, db,
		"SELECT count() AS total FROM ca_symbol WHERE repo_id = $repo_id GROUP ALL",
		map[string]any{"repo_id": repoID})
	if err == nil && len(totalRows) > 0 {
		total = totalRows[0].Total
	}

	// Test symbols
	testRows, err := queryOne[[]countRow](ctx, db,
		"SELECT count() AS total FROM ca_symbol WHERE repo_id = $repo_id AND is_test = true GROUP ALL",
		map[string]any{"repo_id": repoID})
	if err == nil && len(testRows) > 0 {
		tests = testRows[0].Total
	}

	return
}

// GetAICodeFileRatio returns the count of AI-generated files (ai_score > 0.5) and total files.
func (s *SurrealStore) GetAICodeFileRatio(ctx context.Context, repoID string) (aiFiles int, totalFiles int) {
	db := s.client.DB()
	if db == nil {
		return 0, 0
	}

	type countRow struct {
		Total int `json:"total"`
	}

	// Total files
	rows, err := queryOne[[]countRow](ctx, db,
		"SELECT count() AS total FROM ca_file WHERE repo_id = $repo_id GROUP ALL",
		map[string]any{"repo_id": repoID})
	if err == nil && len(rows) > 0 {
		totalFiles = rows[0].Total
	}

	// AI files
	aiRows, err := queryOne[[]countRow](ctx, db,
		"SELECT count() AS total FROM ca_file WHERE repo_id = $repo_id AND ai_score > 0.5 GROUP ALL",
		map[string]any{"repo_id": repoID})
	if err == nil && len(aiRows) > 0 {
		aiFiles = aiRows[0].Total
	}

	return
}

// GetReviewResults returns review results for a target (symbol or file).
func (s *SurrealStore) GetReviewResults(ctx context.Context, targetID string) []*graph.ReviewResultRecord {
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

	rows, err := queryOne[[]reviewRow](ctx, db,
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

// ---------------------------------------------------------------------------
// Impact reports
// ---------------------------------------------------------------------------

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

// StoreImpactReport stores an impact report for a repository.
func (s *SurrealStore) StoreImpactReport(ctx context.Context, repoID string, report *graph.ImpactReport) {
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
	if _, err := surrealdb.Query[interface{}](ctx, db,
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

// GetLatestImpactReport returns the most recent impact report for a repository.
func (s *SurrealStore) GetLatestImpactReport(ctx context.Context, repoID string) *graph.ImpactReport {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	impRows, err := queryOne[[]impactReportRow](ctx, db,
		"SELECT * FROM ca_impact_report WHERE repo_id = $repo_id ORDER BY computed_at DESC LIMIT 1",
		map[string]any{"repo_id": repoID})
	if err != nil || len(impRows) == 0 {
		return nil
	}
	return impRows[0].toImpactReport()
}

// GetImpactReports returns impact reports for a repository, most recent first.
func (s *SurrealStore) GetImpactReports(ctx context.Context, repoID string, limit int) ([]*graph.ImpactReport, int) {
	db := s.client.DB()
	if db == nil {
		return nil, 0
	}
	if limit <= 0 {
		limit = 10
	}
	impRows, err := queryOne[[]impactReportRow](ctx, db,
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
// Understanding score helpers (analytics adjuncts)
// ---------------------------------------------------------------------------

// GetPublicSymbolDocCoverage, GetTestSymbolRatio, GetAICodeFileRatio above.
// These are grouped here because they feed understanding-score computation
// alongside LLM usage and review results.
