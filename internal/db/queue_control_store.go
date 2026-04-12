// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/surrealdb/surrealdb.go"
)

type SurrealQueueControlStore struct {
	client *SurrealDB
}

func NewSurrealQueueControlStore(client *SurrealDB) *SurrealQueueControlStore {
	return &SurrealQueueControlStore{client: client}
}

type QueueControlRecord struct {
	IntakePaused bool `json:"intake_paused"`
}

func (s *SurrealQueueControlStore) LoadQueueControl() (*QueueControlRecord, error) {
	db := s.client.DB()
	if db == nil {
		return nil, nil
	}
	ctx := context.Background()
	raw, err := surrealdb.Query[[]map[string]interface{}](ctx, db,
		"SELECT * FROM type::thing('ca_config', $id)",
		map[string]any{"id": "llm_queue_control"})
	if err != nil {
		slog.Warn("surreal queue control load query failed", "error", err)
		return nil, nil
	}
	if raw == nil || len(*raw) == 0 {
		return nil, nil
	}
	qr := (*raw)[0]
	if qr.Error != nil {
		slog.Warn("queue control load: query error", "error", fmt.Sprintf("%v", qr.Error))
		return nil, nil
	}
	if len(qr.Result) == 0 {
		return nil, nil
	}
	value, _ := qr.Result[0]["value"].(string)
	if value == "" {
		return nil, nil
	}
	var rec QueueControlRecord
	if err := json.Unmarshal([]byte(value), &rec); err != nil {
		slog.Warn("queue control load: invalid json", "error", err)
		return nil, nil
	}
	return &rec, nil
}

func (s *SurrealQueueControlStore) SaveQueueControl(rec *QueueControlRecord) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	ctx := context.Background()
	_, err := surrealdb.Query[interface{}](ctx, db, `
		DEFINE TABLE IF NOT EXISTS ca_config SCHEMAFULL;
		DEFINE FIELD IF NOT EXISTS key ON ca_config TYPE string;
		DEFINE FIELD IF NOT EXISTS value ON ca_config TYPE option<string>;
		DEFINE FIELD IF NOT EXISTS updated_at ON ca_config TYPE datetime DEFAULT time::now();
		DEFINE INDEX IF NOT EXISTS idx_config_key ON ca_config FIELDS key UNIQUE;
	`, map[string]any{})
	if err != nil {
		slog.Warn("failed to ensure ca_config table", "error", err)
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = surrealdb.Query[interface{}](ctx, db,
		"UPSERT type::thing('ca_config', $id) SET key = $key, value = $value, updated_at = time::now()",
		map[string]any{
			"id":    "llm_queue_control",
			"key":   "llm_queue_control",
			"value": string(payload),
		},
	)
	return err
}
