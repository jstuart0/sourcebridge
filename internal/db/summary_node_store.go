// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	surrealdb "github.com/surrealdb/surrealdb.go"
	"github.com/surrealdb/surrealdb.go/pkg/models"

	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
)

// Verify at compile time.
var _ comprehension.SummaryNodeStore = (*SurrealStore)(nil)

type surrealSummaryNode struct {
	ID            *models.RecordID `json:"id,omitempty"`
	CorpusID      string           `json:"corpus_id"`
	UnitID        string           `json:"unit_id"`
	Level         int              `json:"level"`
	ParentID      string           `json:"parent_id"`
	ChildIDs      string           `json:"child_ids"`
	SummaryText   string           `json:"summary_text"`
	Headline      string           `json:"headline"`
	SummaryTokens int              `json:"summary_tokens"`
	SourceTokens  int              `json:"source_tokens"`
	ContentHash   string           `json:"content_hash"`
	ModelUsed     string           `json:"model_used"`
	Strategy      string           `json:"strategy"`
	RevisionFP    string           `json:"revision_fp"`
	Metadata      string           `json:"metadata"`
	GeneratedAt   surrealTime      `json:"generated_at"`
	CreatedAt     surrealTime      `json:"created_at"`
}

func (r *surrealSummaryNode) toSummaryNode() comprehension.SummaryNode {
	n := comprehension.SummaryNode{
		ID:            recordIDString(r.ID),
		CorpusID:      r.CorpusID,
		UnitID:        r.UnitID,
		Level:         r.Level,
		ParentID:      r.ParentID,
		SummaryText:   r.SummaryText,
		Headline:      r.Headline,
		SummaryTokens: r.SummaryTokens,
		SourceTokens:  r.SourceTokens,
		ContentHash:   r.ContentHash,
		ModelUsed:     r.ModelUsed,
		Strategy:      r.Strategy,
		RevisionFP:    r.RevisionFP,
		Metadata:      r.Metadata,
		GeneratedAt:   r.GeneratedAt.Time,
	}
	if r.ChildIDs != "" && r.ChildIDs != "[]" {
		_ = json.Unmarshal([]byte(r.ChildIDs), &n.ChildIDs)
	}
	return n
}

func (s *SurrealStore) GetSummaryNodes(corpusID string) ([]comprehension.SummaryNode, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	sql := `SELECT * FROM ca_summary_node WHERE corpus_id = $corpus_id ORDER BY level, unit_id`
	vars := map[string]any{"corpus_id": corpusID}
	result, err := queryOne[[]surrealSummaryNode](ctx(), db, sql, vars)
	if err != nil {
		return nil, err
	}
	out := make([]comprehension.SummaryNode, len(result))
	for i, r := range result {
		out[i] = r.toSummaryNode()
	}
	return out, nil
}

func (s *SurrealStore) GetSummaryNode(corpusID, unitID string) (*comprehension.SummaryNode, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	sql := `SELECT * FROM ca_summary_node WHERE corpus_id = $corpus_id AND unit_id = $unit_id LIMIT 1`
	vars := map[string]any{"corpus_id": corpusID, "unit_id": unitID}
	result, err := queryOne[[]surrealSummaryNode](ctx(), db, sql, vars)
	if err != nil {
		return nil, nil
	}
	if len(result) == 0 {
		return nil, nil
	}
	n := result[0].toSummaryNode()
	return &n, nil
}

func (s *SurrealStore) StoreSummaryNodes(nodes []comprehension.SummaryNode) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	for _, n := range nodes {
		childJSON, _ := json.Marshal(n.ChildIDs)
		id := n.ID
		if id == "" {
			id = uuid.New().String()
		}
		sql := `
			LET $existing = (SELECT id FROM ca_summary_node WHERE corpus_id = $corpus_id AND unit_id = $unit_id);
			IF array::len($existing) > 0 THEN
				(UPDATE ca_summary_node SET
					level = $level,
					parent_id = $parent_id,
					child_ids = $child_ids,
					summary_text = $summary_text,
					headline = $headline,
					summary_tokens = $summary_tokens,
					source_tokens = $source_tokens,
					content_hash = $content_hash,
					model_used = $model_used,
					strategy = $strategy,
					revision_fp = $revision_fp,
					metadata = $metadata,
					generated_at = time::now()
				WHERE corpus_id = $corpus_id AND unit_id = $unit_id)
			ELSE
				(CREATE ca_summary_node SET
					id = type::thing('ca_summary_node', $id),
					corpus_id = $corpus_id,
					unit_id = $unit_id,
					level = $level,
					parent_id = $parent_id,
					child_ids = $child_ids,
					summary_text = $summary_text,
					headline = $headline,
					summary_tokens = $summary_tokens,
					source_tokens = $source_tokens,
					content_hash = $content_hash,
					model_used = $model_used,
					strategy = $strategy,
					revision_fp = $revision_fp,
					metadata = $metadata,
					generated_at = time::now())
			END;
		`
		vars := map[string]any{
			"id":             id,
			"corpus_id":      n.CorpusID,
			"unit_id":        n.UnitID,
			"level":          n.Level,
			"parent_id":      n.ParentID,
			"child_ids":      string(childJSON),
			"summary_text":   n.SummaryText,
			"headline":       n.Headline,
			"summary_tokens": n.SummaryTokens,
			"source_tokens":  n.SourceTokens,
			"content_hash":   n.ContentHash,
			"model_used":     n.ModelUsed,
			"strategy":       n.Strategy,
			"revision_fp":    n.RevisionFP,
			"metadata":       n.Metadata,
		}
		if _, err := surrealdb.Query[interface{}](ctx(), db, sql, vars); err != nil {
			return err
		}
	}
	return nil
}

func (s *SurrealStore) InvalidateSummaryNodes(corpusID string) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	sql := `DELETE FROM ca_summary_node WHERE corpus_id = $corpus_id`
	vars := map[string]any{"corpus_id": corpusID}
	_, err := surrealdb.Query[interface{}](ctx(), db, sql, vars)
	return err
}
