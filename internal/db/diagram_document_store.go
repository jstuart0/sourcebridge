// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"encoding/json"
	"fmt"
	"strings"

	surrealdb "github.com/surrealdb/surrealdb.go"
	"github.com/surrealdb/surrealdb.go/pkg/models"

	"github.com/sourcebridge/sourcebridge/internal/architecture"
)

type surrealDiagramDocument struct {
	ID         *models.RecordID `json:"id,omitempty"`
	RepoID     string           `json:"repo_id"`
	SourceKind string           `json:"source_kind"`
	Document   string           `json:"document"`
	CreatedAt  surrealTime      `json:"created_at"`
	UpdatedAt  surrealTime      `json:"updated_at"`
}

func (r *surrealDiagramDocument) toDocument() (*architecture.DiagramDocument, error) {
	if strings.TrimSpace(r.Document) == "" {
		return nil, nil
	}
	var doc architecture.DiagramDocument
	if err := json.Unmarshal([]byte(r.Document), &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

func diagramDocumentRecordID(repoID string, sourceKind architecture.SourceKind) string {
	return fmt.Sprintf("%s_%s", repoID, sourceKind)
}

func (s *SurrealStore) StoreDiagramDocument(doc *architecture.DiagramDocument) error {
	db := s.client.DB()
	payload, err := json.Marshal(doc)
	if err != nil {
		return err
	}

	_, err = surrealdb.Query[interface{}](ctx(), db, `
		UPSERT type::thing('ca_diagram_document', $id) SET
			id = type::thing('ca_diagram_document', $id),
			repo_id = $repo_id,
			source_kind = $source_kind,
			document = $document,
			created_at = IF created_at = NONE THEN $created_at ELSE created_at END,
			updated_at = $updated_at
	`, map[string]any{
		"id":          diagramDocumentRecordID(doc.RepositoryID, doc.SourceKind),
		"repo_id":     doc.RepositoryID,
		"source_kind": string(doc.SourceKind),
		"document":    string(payload),
		"created_at":  doc.CreatedAt,
		"updated_at":  doc.UpdatedAt,
	})
	return err
}

func (s *SurrealStore) GetDiagramDocument(repoID string, sourceKinds ...architecture.SourceKind) *architecture.DiagramDocument {
	db := s.client.DB()
	for _, sourceKind := range sourceKinds {
		row, err := queryOne[[]surrealDiagramDocument](ctx(), db,
			"SELECT * FROM type::thing('ca_diagram_document', $id)",
			map[string]any{"id": diagramDocumentRecordID(repoID, sourceKind)},
		)
		if err != nil || len(row) == 0 {
			continue
		}
		doc, err := row[0].toDocument()
		if err != nil || doc == nil {
			continue
		}
		return doc
	}
	return nil
}

func (s *SurrealStore) DeleteDiagramDocument(repoID string, sourceKind architecture.SourceKind) error {
	db := s.client.DB()
	_, err := surrealdb.Query[interface{}](ctx(), db,
		"DELETE type::thing('ca_diagram_document', $id)",
		map[string]any{"id": diagramDocumentRecordID(repoID, sourceKind)},
	)
	return err
}
