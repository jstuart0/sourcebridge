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

// Verify at compile time that *SurrealStore satisfies comprehension.Store.
var _ comprehension.Store = (*SurrealStore)(nil)

// ---------------------------------------------------------------------------
// Strategy settings DTOs
// ---------------------------------------------------------------------------

type surrealStrategySettings struct {
	ID                             *models.RecordID `json:"id,omitempty"`
	ScopeType                      string           `json:"scope_type"`
	ScopeKey                       string           `json:"scope_key"`
	StrategyPreferenceChain        string           `json:"strategy_preference_chain"`
	KnowledgeGenerationModeDefault string           `json:"knowledge_generation_mode_default"`
	ModelID                        string           `json:"model_id"`
	MaxConcurrency                 int              `json:"max_concurrency"`
	MaxPromptTokens                int              `json:"max_prompt_tokens"`
	LeafBudgetTokens               int              `json:"leaf_budget_tokens"`
	RefinePassEnabled              bool             `json:"refine_pass_enabled"`
	LongContextMaxTokens           int              `json:"long_context_max_tokens"`
	GraphRAGEntityTypes            string           `json:"graphrag_entity_types"`
	CacheEnabled                   bool             `json:"cache_enabled"`
	AllowUnsafeCombinations        bool             `json:"allow_unsafe_combinations"`
	UpdatedAt                      surrealTime      `json:"updated_at"`
	UpdatedBy                      string           `json:"updated_by"`
	CreatedAt                      surrealTime      `json:"created_at"`
}

func (r *surrealStrategySettings) toSettings() *comprehension.Settings {
	s := &comprehension.Settings{
		ID:                             recordIDString(r.ID),
		ScopeType:                      comprehension.ScopeType(r.ScopeType),
		ScopeKey:                       r.ScopeKey,
		KnowledgeGenerationModeDefault: r.KnowledgeGenerationModeDefault,
		ModelID:                        r.ModelID,
		MaxConcurrency:                 r.MaxConcurrency,
		MaxPromptTokens:                r.MaxPromptTokens,
		LeafBudgetTokens:               r.LeafBudgetTokens,
		LongContextMaxTokens:           r.LongContextMaxTokens,
		UpdatedAt:                      r.UpdatedAt.Time,
		UpdatedBy:                      r.UpdatedBy,
	}

	// Unmarshal JSON arrays stored as strings
	if r.StrategyPreferenceChain != "" && r.StrategyPreferenceChain != "[]" {
		_ = json.Unmarshal([]byte(r.StrategyPreferenceChain), &s.StrategyPreferenceChain)
	}
	if r.GraphRAGEntityTypes != "" && r.GraphRAGEntityTypes != "[]" {
		_ = json.Unmarshal([]byte(r.GraphRAGEntityTypes), &s.GraphRAGEntityTypes)
	}

	// Booleans: we store them as plain bools in SurrealDB, but the
	// domain model uses *bool to distinguish "not set" from "set to false".
	// When reading from DB, we always have an explicit value.
	s.RefinePassEnabled = &r.RefinePassEnabled
	s.CacheEnabled = &r.CacheEnabled
	s.AllowUnsafeCombinations = &r.AllowUnsafeCombinations

	return s
}

// ---------------------------------------------------------------------------
// Model capabilities DTOs
// ---------------------------------------------------------------------------

type surrealModelCapabilities struct {
	ID                     *models.RecordID `json:"id,omitempty"`
	ModelID                string           `json:"model_id"`
	Provider               string           `json:"provider"`
	DeclaredContextTokens  int              `json:"declared_context_tokens"`
	EffectiveContextTokens int              `json:"effective_context_tokens"`
	LongContextQuality     string           `json:"long_context_quality"`
	InstructionFollowing   string           `json:"instruction_following"`
	JSONMode               string           `json:"json_mode"`
	ToolUse                string           `json:"tool_use"`
	ExtractionGrade        string           `json:"extraction_grade"`
	CreativeGrade          string           `json:"creative_grade"`
	EmbeddingModel         bool             `json:"embedding_model"`
	CostPer1kInput         *float64         `json:"cost_per_1k_input,omitempty"`
	CostPer1kOutput        *float64         `json:"cost_per_1k_output,omitempty"`
	LastProbedAt           surrealTime      `json:"last_probed_at"`
	Source                 string           `json:"source"`
	Notes                  string           `json:"notes"`
	CreatedAt              surrealTime      `json:"created_at"`
	UpdatedAt              surrealTime      `json:"updated_at"`
}

func (r *surrealModelCapabilities) toModelCapabilities() *comprehension.ModelCapabilities {
	mc := &comprehension.ModelCapabilities{
		ID:                     recordIDString(r.ID),
		ModelID:                r.ModelID,
		Provider:               r.Provider,
		DeclaredContextTokens:  r.DeclaredContextTokens,
		EffectiveContextTokens: r.EffectiveContextTokens,
		LongContextQuality:     comprehension.UnmarshalLongContextQuality(r.LongContextQuality),
		InstructionFollowing:   r.InstructionFollowing,
		JSONMode:               r.JSONMode,
		ToolUse:                r.ToolUse,
		ExtractionGrade:        r.ExtractionGrade,
		CreativeGrade:          r.CreativeGrade,
		EmbeddingModel:         r.EmbeddingModel,
		CostPer1kInput:         r.CostPer1kInput,
		CostPer1kOutput:        r.CostPer1kOutput,
		Source:                 r.Source,
		Notes:                  r.Notes,
		UpdatedAt:              r.UpdatedAt.Time,
	}
	if !r.LastProbedAt.Time.IsZero() {
		t := r.LastProbedAt.Time
		mc.LastProbedAt = &t
	}
	return mc
}

// ---------------------------------------------------------------------------
// Strategy settings CRUD
// ---------------------------------------------------------------------------

func (s *SurrealStore) GetSettings(scope comprehension.Scope) (*comprehension.Settings, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	sql := `SELECT * FROM ca_strategy_settings WHERE scope_type = $scope_type AND scope_key = $scope_key LIMIT 1`
	vars := map[string]any{
		"scope_type": string(scope.Type),
		"scope_key":  scope.Key,
	}
	result, err := queryOne[[]surrealStrategySettings](ctx(), db, sql, vars)
	if err != nil {
		return nil, nil // not found
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result[0].toSettings(), nil
}

func (s *SurrealStore) SetSettings(settings *comprehension.Settings) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	chainJSON, _ := json.Marshal(settings.StrategyPreferenceChain)
	entityTypesJSON, _ := json.Marshal(settings.GraphRAGEntityTypes)

	refine := false
	if settings.RefinePassEnabled != nil {
		refine = *settings.RefinePassEnabled
	}
	cache := false
	if settings.CacheEnabled != nil {
		cache = *settings.CacheEnabled
	}
	unsafe := false
	if settings.AllowUnsafeCombinations != nil {
		unsafe = *settings.AllowUnsafeCombinations
	}

	// Upsert by scope_type+scope_key
	sql := `
		LET $existing = (SELECT id FROM ca_strategy_settings WHERE scope_type = $scope_type AND scope_key = $scope_key);
		IF array::len($existing) > 0 THEN
			(UPDATE ca_strategy_settings SET
				strategy_preference_chain = $chain,
				knowledge_generation_mode_default = $generation_mode_default,
				model_id = $model_id,
				max_concurrency = $max_concurrency,
				max_prompt_tokens = $max_prompt_tokens,
				leaf_budget_tokens = $leaf_budget_tokens,
				refine_pass_enabled = $refine,
				long_context_max_tokens = $long_context_max_tokens,
				graphrag_entity_types = $entity_types,
				cache_enabled = $cache,
				allow_unsafe_combinations = $unsafe,
				updated_by = $updated_by,
				updated_at = time::now()
			WHERE scope_type = $scope_type AND scope_key = $scope_key)
		ELSE
			(CREATE ca_strategy_settings SET
				id = type::thing('ca_strategy_settings', $id),
				scope_type = $scope_type,
				scope_key = $scope_key,
				strategy_preference_chain = $chain,
				knowledge_generation_mode_default = $generation_mode_default,
				model_id = $model_id,
				max_concurrency = $max_concurrency,
				max_prompt_tokens = $max_prompt_tokens,
				leaf_budget_tokens = $leaf_budget_tokens,
				refine_pass_enabled = $refine,
				long_context_max_tokens = $long_context_max_tokens,
				graphrag_entity_types = $entity_types,
				cache_enabled = $cache,
				allow_unsafe_combinations = $unsafe,
				updated_by = $updated_by,
				updated_at = time::now())
		END;
	`
	id := settings.ID
	if id == "" {
		id = uuid.New().String()
	}
	vars := map[string]any{
		"id":                      id,
		"scope_type":              string(settings.ScopeType),
		"scope_key":               settings.ScopeKey,
		"chain":                   string(chainJSON),
		"generation_mode_default": settings.KnowledgeGenerationModeDefault,
		"model_id":                settings.ModelID,
		"max_concurrency":         settings.MaxConcurrency,
		"max_prompt_tokens":       settings.MaxPromptTokens,
		"leaf_budget_tokens":      settings.LeafBudgetTokens,
		"refine":                  refine,
		"long_context_max_tokens": settings.LongContextMaxTokens,
		"entity_types":            string(entityTypesJSON),
		"cache":                   cache,
		"unsafe":                  unsafe,
		"updated_by":              settings.UpdatedBy,
	}

	_, err := surrealdb.Query[interface{}](ctx(), db, sql, vars)
	return err
}

func (s *SurrealStore) DeleteSettings(scope comprehension.Scope) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	sql := `DELETE FROM ca_strategy_settings WHERE scope_type = $scope_type AND scope_key = $scope_key`
	vars := map[string]any{
		"scope_type": string(scope.Type),
		"scope_key":  scope.Key,
	}
	_, err := surrealdb.Query[interface{}](ctx(), db, sql, vars)
	return err
}

func (s *SurrealStore) ListSettings() ([]comprehension.Settings, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	sql := `SELECT * FROM ca_strategy_settings ORDER BY scope_type, scope_key`
	result, err := queryOne[[]surrealStrategySettings](ctx(), db, sql, nil)
	if err != nil {
		return nil, err
	}
	out := make([]comprehension.Settings, len(result))
	for i, r := range result {
		out[i] = *r.toSettings()
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Model capabilities CRUD
// ---------------------------------------------------------------------------

func (s *SurrealStore) GetModelCapabilities(modelID string) (*comprehension.ModelCapabilities, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	sql := `SELECT * FROM ca_model_capabilities WHERE model_id = $model_id LIMIT 1`
	vars := map[string]any{"model_id": modelID}
	result, err := queryOne[[]surrealModelCapabilities](ctx(), db, sql, vars)
	if err != nil {
		return nil, nil
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result[0].toModelCapabilities(), nil
}

func (s *SurrealStore) SetModelCapabilities(mc *comprehension.ModelCapabilities) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	lcqJSON := comprehension.MarshalLongContextQuality(mc.LongContextQuality)

	sql := `
		LET $existing = (SELECT id FROM ca_model_capabilities WHERE model_id = $model_id);
		IF array::len($existing) > 0 THEN
			(UPDATE ca_model_capabilities SET
				provider = $provider,
				declared_context_tokens = $declared_context,
				effective_context_tokens = $effective_context,
				long_context_quality = $lcq,
				instruction_following = $instruction_following,
				json_mode = $json_mode,
				tool_use = $tool_use,
				extraction_grade = $extraction_grade,
				creative_grade = $creative_grade,
				embedding_model = $embedding_model,
				cost_per_1k_input = $cost_input,
				cost_per_1k_output = $cost_output,
				source = $source,
				notes = $notes,
				updated_at = time::now()
			WHERE model_id = $model_id)
		ELSE
			(CREATE ca_model_capabilities SET
				id = type::thing('ca_model_capabilities', $id),
				model_id = $model_id,
				provider = $provider,
				declared_context_tokens = $declared_context,
				effective_context_tokens = $effective_context,
				long_context_quality = $lcq,
				instruction_following = $instruction_following,
				json_mode = $json_mode,
				tool_use = $tool_use,
				extraction_grade = $extraction_grade,
				creative_grade = $creative_grade,
				embedding_model = $embedding_model,
				cost_per_1k_input = $cost_input,
				cost_per_1k_output = $cost_output,
				source = $source,
				notes = $notes,
				updated_at = time::now())
		END;
	`
	id := mc.ID
	if id == "" {
		id = uuid.New().String()
	}

	vars := map[string]any{
		"id":                    id,
		"model_id":              mc.ModelID,
		"provider":              mc.Provider,
		"declared_context":      mc.DeclaredContextTokens,
		"effective_context":     mc.EffectiveContextTokens,
		"lcq":                   lcqJSON,
		"instruction_following": mc.InstructionFollowing,
		"json_mode":             mc.JSONMode,
		"tool_use":              mc.ToolUse,
		"extraction_grade":      mc.ExtractionGrade,
		"creative_grade":        mc.CreativeGrade,
		"embedding_model":       mc.EmbeddingModel,
		"cost_input":            mc.CostPer1kInput,
		"cost_output":           mc.CostPer1kOutput,
		"source":                mc.Source,
		"notes":                 mc.Notes,
	}

	_, err := surrealdb.Query[interface{}](ctx(), db, sql, vars)
	return err
}

func (s *SurrealStore) DeleteModelCapabilities(modelID string) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	sql := `DELETE FROM ca_model_capabilities WHERE model_id = $model_id`
	vars := map[string]any{"model_id": modelID}
	_, err := surrealdb.Query[interface{}](ctx(), db, sql, vars)
	return err
}

func (s *SurrealStore) ListModelCapabilities() ([]comprehension.ModelCapabilities, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	sql := `SELECT * FROM ca_model_capabilities ORDER BY model_id`
	result, err := queryOne[[]surrealModelCapabilities](ctx(), db, sql, nil)
	if err != nil {
		return nil, err
	}
	out := make([]comprehension.ModelCapabilities, len(result))
	for i, r := range result {
		out[i] = *r.toModelCapabilities()
	}
	return out, nil
}
