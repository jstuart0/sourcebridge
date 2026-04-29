// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"

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
	// Native arrays on the wire (matches migration 041 TYPE array<string>).
	// Earlier these were JSON-encoded strings; SurrealDB SCHEMAFULL rejected
	// strings against array<string> with "expected a array<string>".
	ExcludedPageIDs            []string `json:"excluded_page_ids"`
	GeneratedPageTitles        []string `json:"generated_page_titles"`
	ExclusionReasons           []string `json:"exclusion_reasons"`
	ExclusionFailureCategories []string `json:"exclusion_failure_categories"`
	Status                     string   `json:"status"`
	ErrorMessage               string   `json:"error_message"`
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

	if r.ExcludedPageIDs == nil {
		result.ExcludedPageIDs = []string{}
	} else {
		result.ExcludedPageIDs = r.ExcludedPageIDs
	}
	if r.GeneratedPageTitles == nil {
		result.GeneratedPageTitles = []string{}
	} else {
		result.GeneratedPageTitles = r.GeneratedPageTitles
	}
	if r.ExclusionReasons == nil {
		result.ExclusionReasons = []string{}
	} else {
		result.ExclusionReasons = r.ExclusionReasons
	}
	if r.ExclusionFailureCategories == nil {
		result.ExclusionFailureCategories = []string{}
	} else {
		result.ExclusionFailureCategories = r.ExclusionFailureCategories
	}

	return result, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// JobResultStore interface implementation
// ─────────────────────────────────────────────────────────────────────────────

// Save persists result. Idempotent by JobID: a second Save with the same
// JobID overwrites the existing row's fields. No duplicate rows are produced.
//
// The deterministic record id (`type::thing('lw_job_results', $job_id)`) plus
// the unique index on `job_id` (migration 046) close the duplicate-row hazard
// at the store layer regardless of the caller path. The previous CREATE-only
// implementation was protected only by MaxAttempts=1 on the cold-start
// enqueue; this is the durable fix.
//
// CompletedAt semantics: the SET clause omits `completed_at` when nil so an
// option<datetime> column keeps NONE on the row. The cold-start runner only
// ever calls Save once per job (with CompletedAt set), so the "save running
// then save completed" pattern is not exercised in practice. If a future
// caller needs to clear a previously-set CompletedAt, the SQL would need an
// explicit NONE branch.
func (s *LivingWikiJobResultStore) Save(ctx context.Context, tenantID string, result *livingwiki.LivingWikiJobResult) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("livingwiki job result store: database not connected")
	}

	// SurrealDB schema (post-migration 041) types these fields as
	// `array<string>`. Pass native Go []string slices so the SDK marshals
	// them as native arrays at the wire level. The earlier JSON-string
	// encoding hit "expected a array<string>" schema violations.
	excludedIDs := result.ExcludedPageIDs
	if excludedIDs == nil {
		excludedIDs = []string{}
	}
	titles := result.GeneratedPageTitles
	if titles == nil {
		titles = []string{}
	}
	reasons := result.ExclusionReasons
	if reasons == nil {
		reasons = []string{}
	}
	categories := result.ExclusionFailureCategories
	if categories == nil {
		categories = []string{}
	}

	vars := map[string]any{
		"tenant_id":                    tenantID,
		"repo_id":                      result.RepoID,
		"job_id":                       result.JobID,
		"started_at":                   result.StartedAt.UTC().Format(time.RFC3339Nano),
		"pages_planned":                result.PagesPlanned,
		"pages_generated":              result.PagesGenerated,
		"pages_excluded":               result.PagesExcluded,
		"excluded_page_ids":            excludedIDs,
		"generated_page_titles":        titles,
		"exclusion_reasons":            reasons,
		"exclusion_failure_categories": categories,
		"status":                       result.Status,
		"error_message":                result.ErrorMessage,
	}

	// completed_at is option<datetime>. Include it in the SET clause only
	// when set; leaving it out lets SurrealDB default to NONE (acceptable
	// for option<datetime>). Trying to pass null/NONE through a Go variable
	// and compare against SurrealQL NONE failed — the SDK serializes nil
	// interface as JSON null, which SurrealQL did not equate with NONE,
	// falling through to type::datetime(null) and erroring "Expected a
	// datetime but cannot convert NULL".
	completedClause := ""
	if result.CompletedAt != nil {
		vars["completed_at"] = result.CompletedAt.UTC().Format(time.RFC3339Nano)
		completedClause = "completed_at          = type::datetime($completed_at),\n\t\t\t    "
	}

	// UPSERT keyed on a deterministic record id derived from job_id. A
	// second Save for the same job_id updates the existing row in place.
	// Mirrors the pattern in livingwiki_repo_settings_store.go.
	sql := `
		UPSERT type::thing('lw_job_results', $job_id)
			SET tenant_id                    = $tenant_id,
			    repo_id                      = $repo_id,
			    job_id                       = $job_id,
			    started_at                   = type::datetime($started_at),
			    ` + completedClause + `pages_planned                = $pages_planned,
			    pages_generated              = $pages_generated,
			    pages_excluded               = $pages_excluded,
			    excluded_page_ids            = $excluded_page_ids,
			    generated_page_titles        = $generated_page_titles,
			    exclusion_reasons            = $exclusion_reasons,
			    exclusion_failure_categories = $exclusion_failure_categories,
			    status                       = $status,
			    error_message                = $error_message
	`
	_, err := surrealdb.Query[interface{}](ctx, db, sql, vars)
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
//
// The query intentionally avoids `ORDER BY started_at DESC LIMIT 1` because
// SurrealDB silently drops the ordering clause when parameter substitution is
// used (`WHERE tenant_id = $tenant_id`), returning rows in insertion order
// instead of sorted order. The same SQL with inline values orders correctly,
// but the SDK forces parameterised execution. As a workaround we fetch every
// row for the (tenant, repo) tuple and pick the latest StartedAt in Go — the
// row count per repo is bounded (one per cold-start / retry-excluded run) so
// the cost is negligible.
func (s *LivingWikiJobResultStore) LastResultForRepo(ctx context.Context, tenantID, repoID string) (*livingwiki.LivingWikiJobResult, error) {
	db := s.client.DB()
	if db == nil {
		return nil, nil
	}

	sql := `SELECT * FROM lw_job_results WHERE tenant_id = $tenant_id AND repo_id = $repo_id`
	rows, err := queryOne[[]surrealLWJobResult](ctx, db, sql, map[string]any{
		"tenant_id": tenantID,
		"repo_id":   repoID,
	})
	if err != nil || len(rows) == 0 {
		return nil, nil
	}

	latest := &rows[0]
	for i := 1; i < len(rows); i++ {
		if rows[i].StartedAt.Time.After(latest.StartedAt.Time) {
			latest = &rows[i]
		}
	}
	return latest.toResult()
}
