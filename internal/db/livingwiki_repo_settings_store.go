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
// in SurrealDB. Slice 5 of the workspace-LLM-source-of-truth plan added a
// per-repo LLM override (LivingWikiLLMOverride). When set, that override
// includes an api_key field encrypted at rest with the same sbenc:v1
// envelope used by ca_llm_config.
type LivingWikiRepoSettingsStore struct {
	client           *SurrealDB
	encryptionKey    string
	allowUnencrypted bool
}

// LivingWikiRepoSettingsStoreOption configures optional behavior.
type LivingWikiRepoSettingsStoreOption func(*LivingWikiRepoSettingsStore)

// WithLivingWikiRepoEncryptionKey sets the encryption key used for the
// per-repo LLM override's api_key field.
func WithLivingWikiRepoEncryptionKey(key string) LivingWikiRepoSettingsStoreOption {
	return func(s *LivingWikiRepoSettingsStore) {
		s.encryptionKey = key
	}
}

// WithLivingWikiRepoAllowUnencrypted is the OSS escape hatch.
func WithLivingWikiRepoAllowUnencrypted(allow bool) LivingWikiRepoSettingsStoreOption {
	return func(s *LivingWikiRepoSettingsStore) {
		s.allowUnencrypted = allow
	}
}

// NewLivingWikiRepoSettingsStore creates a store backed by the given SurrealDB client.
func NewLivingWikiRepoSettingsStore(client *SurrealDB, opts ...LivingWikiRepoSettingsStoreOption) *LivingWikiRepoSettingsStore {
	s := &LivingWikiRepoSettingsStore{client: client}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
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

	// LLMOverride is the per-repo LLM override for living-wiki ops only.
	// Slice 5: the api_key inside is encrypted at rest under the
	// sbenc:v1 envelope. SurrealDB tolerates missing fields on read so
	// pre-migration rows decode unchanged (LLMOverride stays nil).
	LLMOverride *surrealLivingWikiLLMOverride `json:"living_wiki_llm_override,omitempty"`
}

// surrealLivingWikiLLMOverride is the on-disk shape of LivingWikiLLMOverride.
// APIKeyCipher holds the sbenc:v1-encrypted api key (or empty when no
// key override is set, or the legacy plaintext form for any rows that
// pre-date the encryption rollout).
type surrealLivingWikiLLMOverride struct {
	Provider     string       `json:"provider,omitempty"`
	BaseURL      string       `json:"base_url,omitempty"`
	APIKeyCipher string       `json:"api_key_cipher,omitempty"`
	Model        string       `json:"model,omitempty"`
	UpdatedAt    *surrealTime `json:"updated_at,omitempty"`
	UpdatedBy    string       `json:"updated_by,omitempty"`
}

type surrealRepoWikiSink struct {
	Kind            string `json:"kind"`
	IntegrationName string `json:"integration_name"`
	Audience        string `json:"audience"`
	EditPolicy      string `json:"edit_policy,omitempty"`
}

func (r *surrealLivingWikiRepoSettings) toSettings(decryptAPIKey func(string) (string, error)) (*livingwiki.RepositoryLivingWikiSettings, error) {
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

	// Decrypt the LLM override's api key when present. Empty cipher
	// means "no key override" — fall through to workspace settings via
	// the resolver's overlay logic.
	if r.LLMOverride != nil {
		ov := &livingwiki.LivingWikiLLMOverride{
			Provider: r.LLMOverride.Provider,
			BaseURL:  r.LLMOverride.BaseURL,
			Model:    r.LLMOverride.Model,
			UpdatedBy: r.LLMOverride.UpdatedBy,
		}
		if r.LLMOverride.UpdatedAt != nil {
			ov.UpdatedAt = r.LLMOverride.UpdatedAt.Time
		}
		if r.LLMOverride.APIKeyCipher != "" {
			plaintext, err := decryptAPIKey(r.LLMOverride.APIKeyCipher)
			if err != nil {
				return nil, fmt.Errorf("living-wiki repo override api key decrypt: %w", err)
			}
			ov.APIKey = plaintext
		}
		// Skip empty overrides (all fields blank). Distinct from "no
		// row" but indistinguishable in resolver semantics.
		if !ov.IsEmpty() {
			s.LLMOverride = ov
		}
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
	return result[0].toSettings(s.decryptOverrideAPIKey)
}

// decryptOverrideAPIKey is the closure used by toSettings to decrypt the
// per-repo override's api_key. Reuses the sbenc:v1 envelope helpers from
// llm_config_store.go via a local SurrealLLMConfigStore instance — keeps
// the encryption logic in one place.
func (s *LivingWikiRepoSettingsStore) decryptOverrideAPIKey(stored string) (string, error) {
	helper := &SurrealLLMConfigStore{
		encryptionKey:    s.encryptionKey,
		allowUnencrypted: s.allowUnencrypted,
	}
	return helper.decryptAPIKey(stored)
}

// encryptOverrideAPIKey is the encryption counterpart used by SetRepoSettings.
func (s *LivingWikiRepoSettingsStore) encryptOverrideAPIKey(plaintext string) (string, error) {
	helper := &SurrealLLMConfigStore{
		encryptionKey:    s.encryptionKey,
		allowUnencrypted: s.allowUnencrypted,
	}
	return helper.encryptAPIKey(plaintext)
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

	// LLMOverride: encrypt the api_key under the same envelope as
	// ca_llm_config. Nil or empty override results in NONE on disk so
	// the resolver treats the repo as inheriting workspace settings.
	llmOverrideClause := ""
	if settings.LLMOverride != nil && !settings.LLMOverride.IsEmpty() {
		cipher, err := s.encryptOverrideAPIKey(settings.LLMOverride.APIKey)
		if err != nil {
			return fmt.Errorf("living-wiki repo override api key encrypt: %w", err)
		}
		// Map to native Go map for SurrealDB to materialize as a record.
		// Marshalling through the surreal*Override struct would also work
		// but native maps are cheaper and keep the SET clause readable.
		vars["llm_override_provider"] = settings.LLMOverride.Provider
		vars["llm_override_base_url"] = settings.LLMOverride.BaseURL
		vars["llm_override_api_key_cipher"] = cipher
		vars["llm_override_model"] = settings.LLMOverride.Model
		llmOverrideClause = `living_wiki_llm_override = {
			provider: $llm_override_provider,
			base_url: $llm_override_base_url,
			api_key_cipher: $llm_override_api_key_cipher,
			model: $llm_override_model,
			updated_at: time::now(),
			updated_by: $updated_by,
		},
		`
	} else {
		// Explicit clear: NONE removes the field on update.
		llmOverrideClause = "living_wiki_llm_override = NONE,\n\t\t\t"
	}

	// last_run_at and disabled_at are option<datetime>. Build the SET clause
	// dynamically: include each field only when set. Omitted fields default
	// to NONE (which option<datetime> accepts). Trying to pass null/NONE
	// through a Go variable and compare against SurrealQL NONE failed —
	// the SDK serialized nil interface as JSON null, which SurrealQL did not
	// equate with NONE in the IF check, falling through to type::datetime(null)
	// and erroring "Expected a datetime but cannot convert NULL".
	dateClauses := ""
	if settings.LastRunAt != nil {
		vars["last_run_at"] = settings.LastRunAt.UTC().Format(time.RFC3339Nano)
		dateClauses += "last_run_at = type::datetime($last_run_at),\n\t\t\t"
	}
	if settings.DisabledAt != nil {
		vars["disabled_at"] = settings.DisabledAt.UTC().Format(time.RFC3339Nano)
		dateClauses += "disabled_at = type::datetime($disabled_at),\n\t\t\t"
	}

	// SurrealDB's `UPSERT <table> SET ... WHERE ...` only updates pre-existing
	// rows that match WHERE — it does NOT insert when WHERE matches nothing
	// and the result is silently empty. Address the row by a deterministic
	// composite-key ID via type::thing() so UPSERT actually creates or
	// updates the record.
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
			` + dateClauses + llmOverrideClause + `
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
		s2, err := rows[i].toSettings(s.decryptOverrideAPIKey)
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
		s2, err := rows[i].toSettings(s.decryptOverrideAPIKey)
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
