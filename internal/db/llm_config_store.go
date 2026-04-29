// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/surrealdb/surrealdb.go"
)

// SurrealLLMConfigStore persists LLM configuration in SurrealDB using a
// well-known record ID, following the same pattern as SurrealGitConfigStore.
type SurrealLLMConfigStore struct {
	client *SurrealDB
}

// NewSurrealLLMConfigStore creates a new LLM config store backed by SurrealDB.
func NewSurrealLLMConfigStore(client *SurrealDB) *SurrealLLMConfigStore {
	return &SurrealLLMConfigStore{client: client}
}

// LLMConfigRecord is the persisted LLM configuration.
type LLMConfigRecord struct {
	Provider                 string `json:"provider"`
	BaseURL                  string `json:"base_url"`
	APIKey                   string `json:"api_key"`
	SummaryModel             string `json:"summary_model"`
	ReviewModel              string `json:"review_model"`
	AskModel                 string `json:"ask_model"`
	KnowledgeModel           string `json:"knowledge_model"`
	ArchitectureDiagramModel string `json:"architecture_diagram_model"`
	ReportModel              string `json:"report_model"`
	DraftModel               string `json:"draft_model"`
	TimeoutSecs              int    `json:"timeout_secs"`
	AdvancedMode             bool   `json:"advanced_mode"`
	// Version is bumped on every Save so resolvers on other replicas can
	// detect that the workspace settings changed without polling. Zero
	// means the record has never been saved (or pre-dates the version
	// field). The resolver treats version=0 as "no workspace layer".
	Version uint64 `json:"version"`
}

func (s *SurrealLLMConfigStore) LoadLLMConfig() (*LLMConfigRecord, error) {
	db := s.client.DB()
	if db == nil {
		return nil, nil
	}

	ctx := context.Background()

	raw, err := surrealdb.Query[[]map[string]interface{}](ctx, db,
		"SELECT provider, base_url, api_key, summary_model, review_model, ask_model, knowledge_model, architecture_diagram_model, report_model, draft_model, timeout_secs, advanced_mode, version FROM ca_llm_config WHERE id = type::thing('ca_llm_config', 'default') LIMIT 1",
		map[string]any{})
	if err != nil {
		slog.Warn("surreal llm config load query failed", "error", err)
		return nil, nil
	}

	if raw == nil || len(*raw) == 0 {
		return nil, nil
	}

	qr := (*raw)[0]
	if qr.Error != nil {
		slog.Warn("llm config load: query error", "error", fmt.Sprintf("%v", qr.Error))
		return nil, nil
	}

	if len(qr.Result) == 0 {
		return nil, nil
	}

	row := qr.Result[0]
	rec := &LLMConfigRecord{
		Provider:                 strVal(row, "provider"),
		BaseURL:                  strVal(row, "base_url"),
		APIKey:                   strVal(row, "api_key"),
		SummaryModel:             strVal(row, "summary_model"),
		ReviewModel:              strVal(row, "review_model"),
		AskModel:                 strVal(row, "ask_model"),
		KnowledgeModel:           strVal(row, "knowledge_model"),
		ArchitectureDiagramModel: strVal(row, "architecture_diagram_model"),
		ReportModel:              strVal(row, "report_model"),
		DraftModel:               strVal(row, "draft_model"),
	}
	if v, ok := row["timeout_secs"]; ok {
		switch t := v.(type) {
		case float64:
			rec.TimeoutSecs = int(t)
		case uint64:
			rec.TimeoutSecs = int(t)
		case int:
			rec.TimeoutSecs = t
		}
	}
	if v, ok := row["advanced_mode"]; ok {
		if b, ok := v.(bool); ok {
			rec.AdvancedMode = b
		}
	}
	if v, ok := row["version"]; ok {
		switch t := v.(type) {
		case float64:
			rec.Version = uint64(t)
		case uint64:
			rec.Version = t
		case int64:
			if t >= 0 {
				rec.Version = uint64(t)
			}
		case int:
			if t >= 0 {
				rec.Version = uint64(t)
			}
		}
	}
	return rec, nil
}

// LoadLLMConfigVersion fetches just the version cell. The resolver calls
// this on every Resolve to detect cross-replica workspace saves; keeping
// it as a single-field SELECT keeps the per-resolve cost in the sub-
// millisecond range against a healthy SurrealDB.
//
// Returns 0 when the row doesn't exist yet (no workspace settings saved).
// Errors propagate so the resolver can fall back to its cached snapshot
// and stamp Stale=true.
func (s *SurrealLLMConfigStore) LoadLLMConfigVersion() (uint64, error) {
	db := s.client.DB()
	if db == nil {
		return 0, fmt.Errorf("database not connected")
	}
	ctx := context.Background()
	raw, err := surrealdb.Query[[]map[string]interface{}](ctx, db,
		"SELECT version FROM ca_llm_config WHERE id = type::thing('ca_llm_config', 'default') LIMIT 1",
		map[string]any{})
	if err != nil {
		return 0, err
	}
	if raw == nil || len(*raw) == 0 {
		return 0, nil
	}
	qr := (*raw)[0]
	if qr.Error != nil {
		return 0, fmt.Errorf("%v", qr.Error)
	}
	if len(qr.Result) == 0 {
		return 0, nil
	}
	row := qr.Result[0]
	v, ok := row["version"]
	if !ok {
		return 0, nil
	}
	switch t := v.(type) {
	case float64:
		return uint64(t), nil
	case uint64:
		return t, nil
	case int64:
		if t < 0 {
			return 0, nil
		}
		return uint64(t), nil
	case int:
		if t < 0 {
			return 0, nil
		}
		return uint64(t), nil
	}
	return 0, nil
}

func (s *SurrealLLMConfigStore) SaveLLMConfig(rec *LLMConfigRecord) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	ctx := context.Background()

	// Ensure table exists (idempotent)
	_, err := surrealdb.Query[interface{}](ctx, db, `
		DEFINE TABLE IF NOT EXISTS ca_llm_config SCHEMAFULL;
		DEFINE FIELD IF NOT EXISTS provider ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS base_url ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS api_key ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS summary_model ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS review_model ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS ask_model ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS knowledge_model ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS architecture_diagram_model ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS report_model ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS draft_model ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS timeout_secs ON ca_llm_config TYPE int;
		DEFINE FIELD IF NOT EXISTS advanced_mode ON ca_llm_config TYPE bool;
		DEFINE FIELD IF NOT EXISTS version ON ca_llm_config TYPE int DEFAULT 0;
	`, map[string]any{})
	if err != nil {
		slog.Warn("failed to ensure ca_llm_config table", "error", err)
	}

	// version = (version OR 0) + 1: monotonically bumps on every save so
	// resolvers on other replicas can detect changes via the lightweight
	// LoadLLMConfigVersion probe. Atomic at the row level — concurrent
	// saves UPSERT serially in SurrealDB and each one bumps once.
	_, err = surrealdb.Query[interface{}](ctx, db,
		`UPSERT type::thing('ca_llm_config', 'default') SET
			provider = $provider,
			base_url = $base_url,
			api_key = $api_key,
			summary_model = $summary_model,
				review_model = $review_model,
				ask_model = $ask_model,
				knowledge_model = $knowledge_model,
				architecture_diagram_model = $architecture_diagram_model,
				report_model = $report_model,
				draft_model = $draft_model,
			timeout_secs = $timeout_secs,
			advanced_mode = $advanced_mode,
			version = (IF version != NONE THEN version ELSE 0 END) + 1`,
		map[string]any{
			"provider":                   rec.Provider,
			"base_url":                   rec.BaseURL,
			"api_key":                    rec.APIKey,
			"summary_model":              rec.SummaryModel,
			"review_model":               rec.ReviewModel,
			"ask_model":                  rec.AskModel,
			"knowledge_model":            rec.KnowledgeModel,
			"architecture_diagram_model": rec.ArchitectureDiagramModel,
			"report_model":               rec.ReportModel,
			"draft_model":                rec.DraftModel,
			"timeout_secs":               rec.TimeoutSecs,
			"advanced_mode":              rec.AdvancedMode,
		},
	)
	if err != nil {
		return err
	}

	slog.Info("llm config persisted to database", "provider", rec.Provider)
	return nil
}

// strVal safely extracts a string value from a map.
func strVal(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}
