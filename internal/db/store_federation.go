// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"fmt"
	"log/slog"
	"time"

	surrealdb "github.com/surrealdb/surrealdb.go"
	"github.com/surrealdb/surrealdb.go/pkg/models"

	"github.com/google/uuid"
	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// --- SurrealDB row types ---

type surrealRepoLink struct {
	ID           *models.RecordID `json:"id,omitempty"`
	SourceRepoID string           `json:"source_repo_id"`
	TargetRepoID string           `json:"target_repo_id"`
	LinkType     string           `json:"link_type"`
	CreatedBy    string           `json:"created_by"`
	CreatedAt    surrealTime      `json:"created_at"`
}

func (r *surrealRepoLink) toRepoLink() *graph.RepoLink {
	return &graph.RepoLink{
		ID:           recordIDString(r.ID),
		SourceRepoID: r.SourceRepoID,
		TargetRepoID: r.TargetRepoID,
		LinkType:     r.LinkType,
		CreatedBy:    r.CreatedBy,
		CreatedAt:    r.CreatedAt.Time,
	}
}

type surrealCrossRepoRef struct {
	ID             *models.RecordID `json:"id,omitempty"`
	SourceSymbolID string           `json:"source_symbol_id"`
	TargetSymbolID string           `json:"target_symbol_id"`
	SourceRepoID   string           `json:"source_repo_id"`
	TargetRepoID   string           `json:"target_repo_id"`
	RefType        string           `json:"ref_type"`
	Confidence     float64          `json:"confidence"`
	ContractFile   string           `json:"contract_file"`
	ConsumerFile   string           `json:"consumer_file"`
	Evidence       string           `json:"evidence"`
	CreatedAt      surrealTime      `json:"created_at"`
	UpdatedAt      surrealTime      `json:"updated_at"`
}

func (r *surrealCrossRepoRef) toCrossRepoRef() *graph.CrossRepoRef {
	return &graph.CrossRepoRef{
		ID:             recordIDString(r.ID),
		SourceSymbolID: r.SourceSymbolID,
		TargetSymbolID: r.TargetSymbolID,
		SourceRepoID:   r.SourceRepoID,
		TargetRepoID:   r.TargetRepoID,
		RefType:        r.RefType,
		Confidence:     r.Confidence,
		ContractFile:   r.ContractFile,
		ConsumerFile:   r.ConsumerFile,
		Evidence:       r.Evidence,
		CreatedAt:      r.CreatedAt.Time,
		UpdatedAt:      r.UpdatedAt.Time,
	}
}

type surrealAPIContract struct {
	ID           *models.RecordID `json:"id,omitempty"`
	RepoID       string           `json:"repo_id"`
	FilePath     string           `json:"file_path"`
	ContractType string           `json:"contract_type"`
	Endpoints    string           `json:"endpoints"`
	Version      string           `json:"version"`
	ContentHash  string           `json:"content_hash"`
	DetectedAt   surrealTime      `json:"detected_at"`
}

func (r *surrealAPIContract) toAPIContract() *graph.APIContract {
	return &graph.APIContract{
		ID:           recordIDString(r.ID),
		RepoID:       r.RepoID,
		FilePath:     r.FilePath,
		ContractType: r.ContractType,
		Endpoints:    r.Endpoints,
		Version:      r.Version,
		ContentHash:  r.ContentHash,
		DetectedAt:   r.DetectedAt.Time,
	}
}

// --- Repo Links ---

func (s *SurrealStore) LinkRepos(sourceRepoID, targetRepoID string) (*graph.RepoLink, error) {
	if sourceRepoID == targetRepoID {
		return nil, fmt.Errorf("cannot link a repository to itself")
	}
	// Canonical ordering to prevent duplicate pairs
	if sourceRepoID > targetRepoID {
		sourceRepoID, targetRepoID = targetRepoID, sourceRepoID
	}

	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}

	linkID := uuid.New().String()
	_, err := surrealdb.Query[interface{}](ctx(), db,
		`CREATE ca_repo_link SET
			id = type::thing('ca_repo_link', $lid),
			source_repo_id = $src,
			target_repo_id = $tgt,
			link_type = 'manual',
			created_at = time::now()`,
		map[string]any{"lid": linkID, "src": sourceRepoID, "tgt": targetRepoID})
	if err != nil {
		return nil, fmt.Errorf("failed to link repos: %w", err)
	}

	return &graph.RepoLink{
		ID:           linkID,
		SourceRepoID: sourceRepoID,
		TargetRepoID: targetRepoID,
		LinkType:     "manual",
		CreatedAt:    time.Now().UTC(),
	}, nil
}

func (s *SurrealStore) UnlinkRepos(linkID string) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	_, err := surrealdb.Query[interface{}](ctx(), db,
		`DELETE type::thing('ca_repo_link', $id)`,
		map[string]any{"id": linkID})
	return err
}

func (s *SurrealStore) GetRepoLinks(repoID string) ([]*graph.RepoLink, error) {
	db := s.client.DB()
	if db == nil {
		return nil, nil
	}
	rows, err := queryOne[[]surrealRepoLink](ctx(), db,
		`SELECT * FROM ca_repo_link WHERE source_repo_id = $id OR target_repo_id = $id`,
		map[string]any{"id": repoID})
	if err != nil {
		return nil, err
	}
	links := make([]*graph.RepoLink, 0, len(rows))
	for i := range rows {
		links = append(links, rows[i].toRepoLink())
	}
	return links, nil
}

// --- Cross-Repo References ---

func (s *SurrealStore) StoreCrossRepoRef(ref *graph.CrossRepoRef) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	refID := uuid.New().String()
	_, err := surrealdb.Query[interface{}](ctx(), db,
		`CREATE ca_cross_repo_ref SET
			id = type::thing('ca_cross_repo_ref', $rid),
			source_symbol_id = $src_sym,
			target_symbol_id = $tgt_sym,
			source_repo_id = $src_repo,
			target_repo_id = $tgt_repo,
			ref_type = $ref_type,
			confidence = $confidence,
			contract_file = $contract_file,
			consumer_file = $consumer_file,
			evidence = $evidence,
			created_at = time::now(),
			updated_at = time::now()`,
		map[string]any{
			"rid":           refID,
			"src_sym":       ref.SourceSymbolID,
			"tgt_sym":       ref.TargetSymbolID,
			"src_repo":      ref.SourceRepoID,
			"tgt_repo":      ref.TargetRepoID,
			"ref_type":      ref.RefType,
			"confidence":    ref.Confidence,
			"contract_file": ref.ContractFile,
			"consumer_file": ref.ConsumerFile,
			"evidence":      ref.Evidence,
		})
	if err != nil {
		return fmt.Errorf("store cross-repo ref: %w", err)
	}
	ref.ID = refID
	return nil
}

func (s *SurrealStore) StoreCrossRepoRefs(refs []*graph.CrossRepoRef) int {
	stored := 0
	for _, ref := range refs {
		if err := s.StoreCrossRepoRef(ref); err != nil {
			slog.Warn("failed to store cross-repo ref", "error", err)
			continue
		}
		stored++
	}
	return stored
}

func (s *SurrealStore) GetCrossRepoRefs(repoID string, refType *string, limit int) ([]*graph.CrossRepoRef, error) {
	db := s.client.DB()
	if db == nil {
		return nil, nil
	}

	sql := `SELECT * FROM ca_cross_repo_ref WHERE source_repo_id = $id OR target_repo_id = $id`
	vars := map[string]any{"id": repoID}
	if refType != nil {
		sql += ` AND ref_type = $ref_type`
		vars["ref_type"] = *refType
	}
	sql += ` ORDER BY confidence DESC`
	if limit > 0 {
		sql += fmt.Sprintf(` LIMIT %d`, limit)
	}

	rows, err := queryOne[[]surrealCrossRepoRef](ctx(), db, sql, vars)
	if err != nil {
		return nil, err
	}
	refs := make([]*graph.CrossRepoRef, 0, len(rows))
	for i := range rows {
		refs = append(refs, rows[i].toCrossRepoRef())
	}
	return refs, nil
}

func (s *SurrealStore) GetSymbolCrossRepoRefs(symbolID string) ([]*graph.CrossRepoRef, error) {
	db := s.client.DB()
	if db == nil {
		return nil, nil
	}
	rows, err := queryOne[[]surrealCrossRepoRef](ctx(), db,
		`SELECT * FROM ca_cross_repo_ref WHERE source_symbol_id = $id OR target_symbol_id = $id`,
		map[string]any{"id": symbolID})
	if err != nil {
		return nil, err
	}
	refs := make([]*graph.CrossRepoRef, 0, len(rows))
	for i := range rows {
		refs = append(refs, rows[i].toCrossRepoRef())
	}
	return refs, nil
}

func (s *SurrealStore) DeleteCrossRepoRefsForRepo(repoID string) error {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	_, err := surrealdb.Query[interface{}](ctx(), db,
		`DELETE FROM ca_cross_repo_ref WHERE source_repo_id = $id OR target_repo_id = $id`,
		map[string]any{"id": repoID})
	return err
}

func (s *SurrealStore) DeleteCrossRepoRefsBetweenRepos(repoA, repoB string) error {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	_, err := surrealdb.Query[interface{}](ctx(), db,
		`DELETE FROM ca_cross_repo_ref WHERE
			(source_repo_id = $a AND target_repo_id = $b) OR
			(source_repo_id = $b AND target_repo_id = $a)`,
		map[string]any{"a": repoA, "b": repoB})
	return err
}

// --- API Contracts ---

func (s *SurrealStore) StoreAPIContract(contract *graph.APIContract) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	contractID := uuid.New().String()
	_, err := surrealdb.Query[interface{}](ctx(), db,
		`CREATE ca_api_contract SET
			id = type::thing('ca_api_contract', $cid),
			repo_id = $repo_id,
			file_path = $file_path,
			contract_type = $contract_type,
			endpoints = $endpoints,
			version = $version,
			content_hash = $content_hash,
			detected_at = time::now()`,
		map[string]any{
			"cid":           contractID,
			"repo_id":       contract.RepoID,
			"file_path":     contract.FilePath,
			"contract_type": contract.ContractType,
			"endpoints":     contract.Endpoints,
			"version":       contract.Version,
			"content_hash":  contract.ContentHash,
		})
	if err != nil {
		return fmt.Errorf("store api contract: %w", err)
	}
	contract.ID = contractID
	return nil
}

func (s *SurrealStore) GetAPIContracts(repoID string) ([]*graph.APIContract, error) {
	db := s.client.DB()
	if db == nil {
		return nil, nil
	}
	rows, err := queryOne[[]surrealAPIContract](ctx(), db,
		`SELECT * FROM ca_api_contract WHERE repo_id = $repo_id ORDER BY file_path ASC`,
		map[string]any{"repo_id": repoID})
	if err != nil {
		return nil, err
	}
	contracts := make([]*graph.APIContract, 0, len(rows))
	for i := range rows {
		contracts = append(contracts, rows[i].toAPIContract())
	}
	return contracts, nil
}

func (s *SurrealStore) DeleteAPIContractsForRepo(repoID string) error {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	_, err := surrealdb.Query[interface{}](ctx(), db,
		`DELETE FROM ca_api_contract WHERE repo_id = $repo_id`,
		map[string]any{"repo_id": repoID})
	return err
}
