// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package comprehension

import "time"

// SummaryNode represents a cached node in a hierarchical summary tree.
// Persisted in ca_summary_node and used for incremental reindexing.
type SummaryNode struct {
	ID            string    `json:"id,omitempty"`
	CorpusID      string    `json:"corpusId"`
	UnitID        string    `json:"unitId"`
	Level         int       `json:"level"`
	ParentID      string    `json:"parentId,omitempty"`
	ChildIDs      []string  `json:"childIds,omitempty"`
	SummaryText   string    `json:"summaryText"`
	Headline      string    `json:"headline,omitempty"`
	SummaryTokens int       `json:"summaryTokens"`
	SourceTokens  int       `json:"sourceTokens"`
	ContentHash   string    `json:"contentHash,omitempty"`
	ModelUsed     string    `json:"modelUsed,omitempty"`
	Strategy      string    `json:"strategy,omitempty"`
	RevisionFP    string    `json:"revisionFp,omitempty"`
	Metadata      string    `json:"metadata,omitempty"`
	GeneratedAt   time.Time `json:"generatedAt"`
}

// SummaryNodeStore is the persistence interface for summary nodes.
type SummaryNodeStore interface {
	// GetSummaryNodes returns all cached nodes for a corpus.
	GetSummaryNodes(corpusID string) ([]SummaryNode, error)

	// GetSummaryNode returns a single node by corpus + unit ID.
	GetSummaryNode(corpusID, unitID string) (*SummaryNode, error)

	// StoreSummaryNodes bulk-upserts nodes for a corpus.
	StoreSummaryNodes(nodes []SummaryNode) error

	// InvalidateSummaryNodes deletes all cached nodes for a corpus.
	InvalidateSummaryNodes(corpusID string) error
}
