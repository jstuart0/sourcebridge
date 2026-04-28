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

// LivingWikiRepoSettingsStore persists per-repo living-wiki opt-in records
// in SurrealDB. No secret fields exist on this record — all credentials live
// on the global lw_settings row.
type LivingWikiRepoSettingsStore struct {
	client *SurrealDB
}

// NewLivingWikiRepoSettingsStore creates a store backed by the given SurrealDB client.
func NewLivingWikiRepoSettingsStore(client *SurrealDB) *LivingWikiRepoSettingsStore {
	return &LivingWikiRepoSettingsStore{client: client}
}

// Compile-time interface check.
var _ livingwiki.RepoSettingsStore = (*LivingWikiRepoSettingsStore)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// SurrealDB DTO
// ─────────────────────────────────────────────────────────────────────────────

type surrealLivingWikiRepoSettings struct {
	ID               *models.RecordID `json:"id,omitempty"`
	TenantID         string           `json:"tenant_id"`
	RepoID           string           `json:"repo_id"`
	Enabled          bool             `json:"enabled"`
	Mode             string           `json:"mode"`
	// SurrealDB stores these as native arrays (TYPE array). The struct
	// reflects the on-the-wire shape: a slice of typed records and a slice
	// of strings respectively. An earlier version stored them as JSON-encoded
	// strings, which SurrealDB rejected with a schema error
	// ("Found '[]' for field `exclude_paths` ... but expected a array").
	Sinks            []surrealRepoWikiSink `json:"sinks"`
	ExcludePaths     []string              `json:"exclude_paths"`
	StaleWhenStrategy string          `json:"stale_when_strategy"`
	MaxPagesPerJob   int              `json:"max_pages_per_job"`
	LastRunAt        *surrealTime     `json:"last_run_at,omitempty"`
	DisabledAt       *surrealTime     `json:"disabled_at,omitempty"`
	UpdatedAt        surrealTime      `json:"updated_at"`
	UpdatedBy        string           `json:"updated_by"`
}

type surrealRepoWikiSink struct {
	Kind            string `json:"kind"`
	IntegrationName string `json:"integration_name"`
	Audience        string `json:"audience"`
	EditPolicy      string `json:"edit_policy,omitempty"`
}

func (r *surrealLivingWikiRepoSettings) toSettings() (*livingwiki.RepositoryLivingWikiSettings, error) {
	s := &livingwiki.RepositoryLivingWikiSettings{
		TenantID:          r.TenantID,
		RepoID:            r.RepoID,
		Enabled:           r.Enabled,
		Mode:              livingwiki.RepoWikiMode(r.Mode),
		StaleWhenStrategy: livingwiki.StaleStrategy(r.StaleWhenStrategy),
		MaxPagesPerJob:    r.MaxPagesPerJob,
		UpdatedAt:         r.UpdatedAt.Time,
		UpdatedBy:         r.UpdatedBy,
	}
	if s.MaxPagesPerJob == 0 {
		s.MaxPagesPerJob = 50
	}
	if r.LastRunAt != nil && !r.LastRunAt.IsZero() {
		t := r.LastRunAt.Time
		s.LastRunAt = &t
	}
	if r.DisabledAt != nil && !r.DisabledAt.IsZero() {
		t := r.DisabledAt.Time
		s.DisabledAt = &t
	}

	// Sinks are decoded directly as native arrays (the on-disk shape).
	s.Sinks = make([]livingwiki.RepoWikiSink, 0, len(r.Sinks))
	for _, sr := range r.Sinks {
		s.Sinks = append(s.Sinks, livingwiki.RepoWikiSink{
			Kind:            livingwiki.RepoWikiSinkKind(sr.Kind),
			IntegrationName: sr.IntegrationName,
			Audience:        livingwiki.RepoWikiAudience(sr.Audience),
			EditPolicy:      livingwiki.RepoWikiEditPolicy(sr.EditPolicy),
		})
	}

	if r.ExcludePaths == nil {
		s.ExcludePaths = []string{}
	} else {
		s.ExcludePaths = r.ExcludePaths
	}

	return s, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// RepoSettingsStore interface implementation
// ─────────────────────────────────────────────────────────────────────────────

func (s *LivingWikiRepoSettingsStore) GetRepoSettings(c context.Context, tenantID, repoID string) (*livingwiki.RepositoryLivingWikiSettings, error) {
	db := s.client.DB()
	if db == nil {
		return nil, nil
	}

	sql := `SELECT * FROM lw_repo_settings WHERE tenant_id = $tenant_id AND repo_id = $repo_id LIMIT 1`
	result, err := queryOne[[]surrealLivingWikiRepoSettings](c, db, sql, map[string]any{
		"tenant_id": tenantID,
		"repo_id":   repoID,
	})
	if err != nil || len(result) == 0 {
		// No row = not yet configured; return nil without error (default-disabled).
		return nil, nil
	}
	return result[0].toSettings()
}

func (s *LivingWikiRepoSettingsStore) SetRepoSettings(c context.Context, settings livingwiki.RepositoryLivingWikiSettings) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	if settings.MaxPagesPerJob == 0 {
		settings.MaxPagesPerJob = 50
	}
	if string(settings.StaleWhenStrategy) == "" {
		settings.StaleWhenStrategy = livingwiki.StaleStrategyDirect
	}
	if string(settings.Mode) == "" {
		settings.Mode = livingwiki.RepoWikiModePRReview
	}

	// Materialize sinks + exclude paths as native Go slices. The SurrealDB
	// SDK marshals them as native arrays at the wire level so they satisfy
	// the schema's TYPE array constraint. ExcludePaths is normalized to a
	// non-nil empty slice so SurrealDB never sees null for a NOT-NULL field.
	rawSinks := make([]surrealRepoWikiSink, 0, len(settings.Sinks))
	for _, sink := range settings.Sinks {
		rawSinks = append(rawSinks, surrealRepoWikiSink{
			Kind:            string(sink.Kind),
			IntegrationName: sink.IntegrationName,
			Audience:        string(sink.Audience),
			EditPolicy:      string(sink.EditPolicy),
		})
	}
	excludePaths := settings.ExcludePaths
	if excludePaths == nil {
		excludePaths = []string{}
	}

	vars := map[string]any{
		"tenant_id":           settings.TenantID,
		"repo_id":             settings.RepoID,
		"enabled":             settings.Enabled,
		"mode":                string(settings.Mode),
		"sinks":               rawSinks,
		"exclude_paths":       excludePaths,
		"stale_when_strategy": string(settings.StaleWhenStrategy),
		"max_pages_per_job":   settings.MaxPagesPerJob,
		"updated_by":          settings.UpdatedBy,
	}

	var lastRunAt, disabledAt interface{}
	if settings.LastRunAt != nil {
		lastRunAt = settings.LastRunAt.UTC().Format(time.RFC3339Nano)
	}
	if settings.DisabledAt != nil {
		disabledAt = settings.DisabledAt.UTC().Format(time.RFC3339Nano)
	}
	vars["last_run_at"] = lastRunAt
	vars["disabled_at"] = disabledAt

	// SurrealDB's `UPSERT <table> SET ... WHERE ...` only updates pre-existing
	// rows that match WHERE — it does NOT insert when WHERE matches nothing
	// and the result is silently empty. That gotcha is why every Enable click
	// silently no-op'd: no row appeared, sink dispatch saw zero sinks and
	// skipped, no Confluence pages got pushed.
	//
	// Fix: address the row by a deterministic record ID derived from the
	// (tenant_id, repo_id) composite key via type::thing(). UPSERT on a
	// specific record either updates it or creates it. last_run_at and
	// disabled_at use type::datetime() to avoid the same option<datetime>
	// schema-validation rejection that bit lw_job_results.
	sql := `
		UPSERT type::thing('lw_repo_settings', [$tenant_id, $repo_id]) SET
			tenant_id           = $tenant_id,
			repo_id             = $repo_id,
			enabled             = $enabled,
			mode                = $mode,
			sinks               = $sinks,
			exclude_paths       = $exclude_paths,
			stale_when_strategy = $stale_when_strategy,
			max_pages_per_job   = $max_pages_per_job,
			last_run_at         = IF $last_run_at = NONE THEN NONE ELSE type::datetime($last_run_at) END,
			disabled_at         = IF $disabled_at = NONE THEN NONE ELSE type::datetime($disabled_at) END,
			updated_by          = $updated_by,
			updated_at          = time::now()
	`
	_, err := surrealdb.Query[interface{}](c, db, sql, vars)
	return err
}

func (s *LivingWikiRepoSettingsStore) ListEnabledRepos(c context.Context, tenantID string) ([]livingwiki.RepositoryLivingWikiSettings, error) {
	db := s.client.DB()
	if db == nil {
		return nil, nil
	}

	sql := `SELECT * FROM lw_repo_settings WHERE tenant_id = $tenant_id AND enabled = true`
	rows, err := queryOne[[]surrealLivingWikiRepoSettings](c, db, sql, map[string]any{
		"tenant_id": tenantID,
	})
	if err != nil {
		// queryOne returns an error on empty result sets; treat as empty.
		return []livingwiki.RepositoryLivingWikiSettings{}, nil
	}
	result := make([]livingwiki.RepositoryLivingWikiSettings, 0, len(rows))
	for i := range rows {
		s2, err := rows[i].toSettings()
		if err != nil {
			return nil, fmt.Errorf("decode row for repo %s: %w", rows[i].RepoID, err)
		}
		result = append(result, *s2)
	}
	return result, nil
}

func (s *LivingWikiRepoSettingsStore) DeleteRepoSettings(c context.Context, tenantID, repoID string) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	sql := `DELETE FROM lw_repo_settings WHERE tenant_id = $tenant_id AND repo_id = $repo_id`
	_, err := surrealdb.Query[interface{}](c, db, sql, map[string]any{
		"tenant_id": tenantID,
		"repo_id":   repoID,
	})
	return err
}

func (s *LivingWikiRepoSettingsStore) RepositoriesUsingSink(c context.Context, tenantID, integrationName string) ([]livingwiki.RepositoryLivingWikiSettings, error) {
	db := s.client.DB()
	if db == nil {
		return nil, nil
	}

	// SurrealDB does not support JSON-path array filtering natively for
	// string-encoded JSON fields, so we fetch all rows for the tenant and
	// filter in Go. The expected row count per tenant is small (< 1000).
	sql := `SELECT * FROM lw_repo_settings WHERE tenant_id = $tenant_id`
	rows, err := queryOne[[]surrealLivingWikiRepoSettings](c, db, sql, map[string]any{
		"tenant_id": tenantID,
	})
	if err != nil {
		return []livingwiki.RepositoryLivingWikiSettings{}, nil
	}

	var result []livingwiki.RepositoryLivingWikiSettings
	for i := range rows {
		s2, err := rows[i].toSettings()
		if err != nil {
			continue
		}
		for _, sink := range s2.Sinks {
			if sink.IntegrationName == integrationName {
				result = append(result, *s2)
				break
			}
		}
	}
	return result, nil
}
