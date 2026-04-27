// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	surrealdb "github.com/surrealdb/surrealdb.go"
	"github.com/surrealdb/surrealdb.go/pkg/models"

	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// LivingWikiJobResultStore persists per-run job outcome records in SurrealDB.
// The lw_job_results table is created by migration 036. No secret fields exist
// on this record — it carries only counts, statuses, and page title lists.
//
// Implements [livingwiki.JobResultStore].
type LivingWikiJobResultStore struct {
	client *SurrealDB
}

// NewLivingWikiJobResultStore creates a store backed by the given SurrealDB client.
func NewLivingWikiJobResultStore(client *SurrealDB) *LivingWikiJobResultStore {
	return &LivingWikiJobResultStore{client: client}
}

// Compile-time interface check.
var _ livingwiki.JobResultStore = (*LivingWikiJobResultStore)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// SurrealDB DTO
// ─────────────────────────────────────────────────────────────────────────────

type surrealLWJobResult struct {
	ID                  *models.RecordID `json:"id,omitempty"`
	TenantID            string           `json:"tenant_id"`
	RepoID              string           `json:"repo_id"`
	JobID               string           `json:"job_id"`
	StartedAt           surrealTime      `json:"started_at"`
	CompletedAt         *surrealTime     `json:"completed_at,omitempty"`
	PagesPlanned        int              `json:"pages_planned"`
	PagesGenerated      int              `json:"pages_generated"`
	PagesExcluded       int              `json:"pages_excluded"`
	ExcludedPageIDs     string           `json:"excluded_page_ids"`     // JSON-encoded []string
	GeneratedPageTitles string           `json:"generated_page_titles"` // JSON-encoded []string
	ExclusionReasons    string           `json:"exclusion_reasons"`     // JSON-encoded []string
	Status              string           `json:"status"`
	ErrorMessage        string           `json:"error_message"`
}

func (r *surrealLWJobResult) toResult() (*livingwiki.LivingWikiJobResult, error) {
	result := &livingwiki.LivingWikiJobResult{
		JobID:        r.JobID,
		StartedAt:    r.StartedAt.Time,
		PagesPlanned: r.PagesPlanned,
		PagesGenerated: r.PagesGenerated,
		PagesExcluded: r.PagesExcluded,
		Status:       r.Status,
		ErrorMessage: r.ErrorMessage,
	}
	if r.CompletedAt != nil && !r.CompletedAt.IsZero() {
		t := r.CompletedAt.Time
		result.CompletedAt = &t
	}

	if r.ExcludedPageIDs != "" && r.ExcludedPageIDs != "[]" {
		_ = json.Unmarshal([]byte(r.ExcludedPageIDs), &result.ExcludedPageIDs)
	}
	if result.ExcludedPageIDs == nil {
		result.ExcludedPageIDs = []string{}
	}

	if r.GeneratedPageTitles != "" && r.GeneratedPageTitles != "[]" {
		_ = json.Unmarshal([]byte(r.GeneratedPageTitles), &result.GeneratedPageTitles)
	}
	if result.GeneratedPageTitles == nil {
		result.GeneratedPageTitles = []string{}
	}

	if r.ExclusionReasons != "" && r.ExclusionReasons != "[]" {
		_ = json.Unmarshal([]byte(r.ExclusionReasons), &result.ExclusionReasons)
	}
	if result.ExclusionReasons == nil {
		result.ExclusionReasons = []string{}
	}

	return result, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// JobResultStore interface implementation
// ─────────────────────────────────────────────────────────────────────────────

// Save persists result. Creates a new row; results are append-only (no update).
func (s *LivingWikiJobResultStore) Save(ctx context.Context, tenantID string, result *livingwiki.LivingWikiJobResult) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("livingwiki job result store: database not connected")
	}

	excludedJSON, err := json.Marshal(result.ExcludedPageIDs)
	if err != nil {
		return fmt.Errorf("encode excluded_page_ids: %w", err)
	}
	titlesJSON, err := json.Marshal(result.GeneratedPageTitles)
	if err != nil {
		return fmt.Errorf("encode generated_page_titles: %w", err)
	}
	reasonsJSON, err := json.Marshal(result.ExclusionReasons)
	if err != nil {
		return fmt.Errorf("encode exclusion_reasons: %w", err)
	}

	vars := map[string]any{
		"tenant_id":             tenantID,
		"repo_id":               result.RepoID,
		"job_id":                result.JobID,
		"started_at":            result.StartedAt.UTC().Format(time.RFC3339Nano),
		"pages_planned":         result.PagesPlanned,
		"pages_generated":       result.PagesGenerated,
		"pages_excluded":        result.PagesExcluded,
		"excluded_page_ids":     string(excludedJSON),
		"generated_page_titles": string(titlesJSON),
		"exclusion_reasons":     string(reasonsJSON),
		"status":                result.Status,
		"error_message":         result.ErrorMessage,
	}

	var completedAt interface{}
	if result.CompletedAt != nil {
		completedAt = result.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	vars["completed_at"] = completedAt

	sql := `
		CREATE lw_job_results
			SET tenant_id             = $tenant_id,
			    repo_id               = $repo_id,
			    job_id                = $job_id,
			    started_at            = $started_at,
			    completed_at          = $completed_at,
			    pages_planned         = $pages_planned,
			    pages_generated       = $pages_generated,
			    pages_excluded        = $pages_excluded,
			    excluded_page_ids     = $excluded_page_ids,
			    generated_page_titles = $generated_page_titles,
			    exclusion_reasons     = $exclusion_reasons,
			    status                = $status,
			    error_message         = $error_message
	`
	_, err = surrealdb.Query[interface{}](ctx, db, sql, vars)
	return err
}

// GetByJobID returns the result record for the given job_id, or nil if not found.
func (s *LivingWikiJobResultStore) GetByJobID(ctx context.Context, jobID string) (*livingwiki.LivingWikiJobResult, error) {
	db := s.client.DB()
	if db == nil {
		return nil, nil
	}

	sql := `SELECT * FROM lw_job_results WHERE job_id = $job_id LIMIT 1`
	rows, err := queryOne[[]surrealLWJobResult](ctx, db, sql, map[string]any{
		"job_id": jobID,
	})
	if err != nil || len(rows) == 0 {
		return nil, nil
	}
	return rows[0].toResult()
}

// LastResultForRepo returns the most recently started job result for the given
// tenant and repo, or nil if no results have been recorded yet.
func (s *LivingWikiJobResultStore) LastResultForRepo(ctx context.Context, tenantID, repoID string) (*livingwiki.LivingWikiJobResult, error) {
	db := s.client.DB()
	if db == nil {
		return nil, nil
	}

	sql := `SELECT * FROM lw_job_results WHERE tenant_id = $tenant_id AND repo_id = $repo_id ORDER BY started_at DESC LIMIT 1`
	rows, err := queryOne[[]surrealLWJobResult](ctx, db, sql, map[string]any{
		"tenant_id": tenantID,
		"repo_id":   repoID,
	})
	if err != nil || len(rows) == 0 {
		return nil, nil
	}
	return rows[0].toResult()
}
