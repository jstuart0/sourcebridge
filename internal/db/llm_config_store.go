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
	Provider     string `json:"provider"`
	BaseURL      string `json:"base_url"`
	APIKey       string `json:"api_key"`
	SummaryModel string `json:"summary_model"`
	ReviewModel  string `json:"review_model"`
	AskModel     string `json:"ask_model"`
	TimeoutSecs  int    `json:"timeout_secs"`
}

func (s *SurrealLLMConfigStore) LoadLLMConfig() (*LLMConfigRecord, error) {
	db := s.client.DB()
	if db == nil {
		return nil, nil
	}

	ctx := context.Background()

	raw, err := surrealdb.Query[[]map[string]interface{}](ctx, db,
		"SELECT provider, base_url, api_key, summary_model, review_model, ask_model, timeout_secs FROM ca_llm_config WHERE id = type::thing('ca_llm_config', 'default') LIMIT 1",
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
		Provider:     strVal(row, "provider"),
		BaseURL:      strVal(row, "base_url"),
		APIKey:       strVal(row, "api_key"),
		SummaryModel: strVal(row, "summary_model"),
		ReviewModel:  strVal(row, "review_model"),
		AskModel:     strVal(row, "ask_model"),
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
	return rec, nil
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
		DEFINE FIELD IF NOT EXISTS timeout_secs ON ca_llm_config TYPE int;
	`, map[string]any{})
	if err != nil {
		slog.Warn("failed to ensure ca_llm_config table", "error", err)
	}

	_, err = surrealdb.Query[interface{}](ctx, db,
		`UPSERT type::thing('ca_llm_config', 'default') SET
			provider = $provider,
			base_url = $base_url,
			api_key = $api_key,
			summary_model = $summary_model,
			review_model = $review_model,
			ask_model = $ask_model,
			timeout_secs = $timeout_secs`,
		map[string]any{
			"provider":      rec.Provider,
			"base_url":      rec.BaseURL,
			"api_key":       rec.APIKey,
			"summary_model": rec.SummaryModel,
			"review_model":  rec.ReviewModel,
			"ask_model":     rec.AskModel,
			"timeout_secs":  rec.TimeoutSecs,
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
