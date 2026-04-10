// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package comprehension

import (
	"encoding/json"
	"time"
)

// ScopeType determines the level of configuration inheritance.
// Order from broadest to most specific: workspace > corpus_type > artifact_type > user.
type ScopeType string

const (
	ScopeWorkspace    ScopeType = "workspace"
	ScopeCorpusType   ScopeType = "corpus_type"
	ScopeArtifactType ScopeType = "artifact_type"
	ScopeUser         ScopeType = "user"
)

// Scope identifies a specific settings scope.
type Scope struct {
	Type ScopeType `json:"type"`
	Key  string    `json:"key"`
}

// WorkspaceScope is the default workspace scope.
var WorkspaceScope = Scope{Type: ScopeWorkspace, Key: "default"}

// Settings holds the comprehension configuration for a given scope.
// Zero-value fields mean "inherit from parent scope."
type Settings struct {
	ID                      string    `json:"id,omitempty"`
	ScopeType               ScopeType `json:"scopeType"`
	ScopeKey                string    `json:"scopeKey"`
	StrategyPreferenceChain []string  `json:"strategyPreferenceChain,omitempty"`
	ModelID                 string    `json:"modelId,omitempty"`
	MaxConcurrency          int       `json:"maxConcurrency,omitempty"`
	MaxPromptTokens         int       `json:"maxPromptTokens,omitempty"`
	LeafBudgetTokens        int       `json:"leafBudgetTokens,omitempty"`
	RefinePassEnabled       *bool     `json:"refinePassEnabled,omitempty"`
	LongContextMaxTokens    int       `json:"longContextMaxTokens,omitempty"`
	GraphRAGEntityTypes     []string  `json:"graphragEntityTypes,omitempty"`
	CacheEnabled            *bool     `json:"cacheEnabled,omitempty"`
	AllowUnsafeCombinations *bool     `json:"allowUnsafeCombinations,omitempty"`
	UpdatedAt               time.Time `json:"updatedAt"`
	UpdatedBy               string    `json:"updatedBy,omitempty"`
}

// EffectiveSettings is a fully-resolved settings object with no zero-value
// gaps. Each field records which scope provided the value.
type EffectiveSettings struct {
	Settings
	// InheritedFrom maps field names to the scope that provided the value.
	InheritedFrom map[string]Scope `json:"inheritedFrom,omitempty"`
}

// DefaultSettings returns the built-in defaults used when no workspace
// settings exist.
func DefaultSettings() Settings {
	refine := false
	cache := false
	unsafe := false
	return Settings{
		ScopeType:               ScopeWorkspace,
		ScopeKey:                "default",
		StrategyPreferenceChain: []string{"hierarchical", "single_shot"},
		ModelID:                 "",
		MaxConcurrency:          3,
		MaxPromptTokens:         100000,
		LeafBudgetTokens:        3000,
		RefinePassEnabled:       &refine,
		LongContextMaxTokens:    0,
		GraphRAGEntityTypes:     []string{},
		CacheEnabled:            &cache,
		AllowUnsafeCombinations: &unsafe,
	}
}

// ModelCapabilities describes what a model can do. Mirrors the Python
// ModelCapabilities dataclass in workers/comprehension/capabilities.py.
type ModelCapabilities struct {
	ID                     string            `json:"id,omitempty"`
	ModelID                string            `json:"modelId"`
	Provider               string            `json:"provider"`
	DeclaredContextTokens  int               `json:"declaredContextTokens"`
	EffectiveContextTokens int               `json:"effectiveContextTokens"`
	LongContextQuality     map[int]string    `json:"longContextQuality,omitempty"`
	InstructionFollowing   string            `json:"instructionFollowing"`
	JSONMode               string            `json:"jsonMode"`
	ToolUse                string            `json:"toolUse"`
	ExtractionGrade        string            `json:"extractionGrade"`
	CreativeGrade          string            `json:"creativeGrade"`
	EmbeddingModel         bool              `json:"embeddingModel"`
	CostPer1kInput         *float64          `json:"costPer1kInput,omitempty"`
	CostPer1kOutput        *float64          `json:"costPer1kOutput,omitempty"`
	LastProbedAt           *time.Time        `json:"lastProbedAt,omitempty"`
	Source                 string            `json:"source"`
	Notes                  string            `json:"notes,omitempty"`
	UpdatedAt              time.Time         `json:"updatedAt"`
}

// MarshalLongContextQuality serializes the LongContextQuality map to JSON
// for SurrealDB storage (stored as a JSON string in a string column).
func MarshalLongContextQuality(m map[int]string) string {
	if len(m) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// UnmarshalLongContextQuality deserializes the JSON string from SurrealDB.
func UnmarshalLongContextQuality(s string) map[int]string {
	if s == "" || s == "{}" {
		return nil
	}
	var m map[int]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}
