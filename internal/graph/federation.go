// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graph

import "time"

// RepoLink represents a link between two repositories.
type RepoLink struct {
	ID           string    `json:"id"`
	SourceRepoID string    `json:"source_repo_id"`
	TargetRepoID string    `json:"target_repo_id"`
	LinkType     string    `json:"link_type"`
	CreatedBy    string    `json:"created_by,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// CrossRepoRef represents a detected cross-repo symbol reference.
type CrossRepoRef struct {
	ID             string    `json:"id"`
	SourceSymbolID string    `json:"source_symbol_id"`
	TargetSymbolID string    `json:"target_symbol_id"`
	SourceRepoID   string    `json:"source_repo_id"`
	TargetRepoID   string    `json:"target_repo_id"`
	RefType        string    `json:"ref_type"`
	Confidence     float64   `json:"confidence"`
	ContractFile   string    `json:"contract_file,omitempty"`
	ConsumerFile   string    `json:"consumer_file,omitempty"`
	Evidence       string    `json:"evidence,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// APIContract represents a detected API contract in a repository.
type APIContract struct {
	ID           string    `json:"id"`
	RepoID       string    `json:"repo_id"`
	FilePath     string    `json:"file_path"`
	ContractType string    `json:"contract_type"`
	Endpoints    string    `json:"endpoints"`
	Version      string    `json:"version"`
	ContentHash  string    `json:"content_hash"`
	DetectedAt   time.Time `json:"detected_at"`
}
