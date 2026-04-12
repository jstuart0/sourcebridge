// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"fmt"

	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
)

// --- Query resolvers ---

func (r *queryResolver) ComprehensionSettings(ctx context.Context, scopeType *string, scopeKey *string) (*EffectiveComprehensionSettings, error) {
	if r.ComprehensionStore == nil {
		return nil, fmt.Errorf("comprehension settings not configured")
	}
	st := "workspace"
	sk := "default"
	if scopeType != nil {
		st = *scopeType
	}
	if scopeKey != nil {
		sk = *scopeKey
	}
	scope := comprehension.Scope{Type: comprehension.ScopeType(st), Key: sk}
	eff, err := comprehension.Resolve(r.ComprehensionStore, scope)
	if err != nil {
		return nil, err
	}
	return mapEffectiveSettings(eff), nil
}

func (r *queryResolver) ComprehensionSettingsList(ctx context.Context) ([]*ComprehensionSettings, error) {
	if r.ComprehensionStore == nil {
		return nil, fmt.Errorf("comprehension settings not configured")
	}
	list, err := r.ComprehensionStore.ListSettings()
	if err != nil {
		return nil, err
	}
	result := make([]*ComprehensionSettings, len(list))
	for i, s := range list {
		result[i] = mapSettings(&s)
	}
	return result, nil
}

func (r *queryResolver) ModelCapabilities(ctx context.Context) ([]*ModelCapabilityProfile, error) {
	if r.ComprehensionStore == nil {
		return nil, fmt.Errorf("comprehension settings not configured")
	}
	list, err := r.ComprehensionStore.ListModelCapabilities()
	if err != nil {
		return nil, err
	}
	result := make([]*ModelCapabilityProfile, len(list))
	for i, mc := range list {
		result[i] = mapModelCapability(&mc)
	}
	return result, nil
}

func (r *queryResolver) ModelCapability(ctx context.Context, modelID string) (*ModelCapabilityProfile, error) {
	if r.ComprehensionStore == nil {
		return nil, fmt.Errorf("comprehension settings not configured")
	}
	mc, err := r.ComprehensionStore.GetModelCapabilities(modelID)
	if err != nil {
		return nil, err
	}
	if mc == nil {
		return nil, nil
	}
	return mapModelCapability(mc), nil
}

// --- Mutation resolvers ---

func (r *mutationResolver) UpdateComprehensionSettings(ctx context.Context, input UpdateComprehensionSettingsInput) (*EffectiveComprehensionSettings, error) {
	if r.ComprehensionStore == nil {
		return nil, fmt.Errorf("comprehension settings not configured")
	}
	scopeKey := ""
	if input.ScopeKey != nil {
		scopeKey = *input.ScopeKey
	}

	settings := &comprehension.Settings{
		ScopeType:               comprehension.ScopeType(input.ScopeType),
		ScopeKey:                scopeKey,
		StrategyPreferenceChain: input.StrategyPreferenceChain,
		RefinePassEnabled:       input.RefinePassEnabled,
		CacheEnabled:            input.CacheEnabled,
		AllowUnsafeCombinations: input.AllowUnsafeCombinations,
	}
	if input.ModelID != nil {
		settings.ModelID = *input.ModelID
	}
	if input.MaxConcurrency != nil {
		settings.MaxConcurrency = *input.MaxConcurrency
	}
	if input.MaxPromptTokens != nil {
		settings.MaxPromptTokens = *input.MaxPromptTokens
	}
	if input.LeafBudgetTokens != nil {
		settings.LeafBudgetTokens = *input.LeafBudgetTokens
	}
	if input.LongContextMaxTokens != nil {
		settings.LongContextMaxTokens = *input.LongContextMaxTokens
	}
	if len(input.GraphragEntityTypes) > 0 {
		settings.GraphRAGEntityTypes = input.GraphragEntityTypes
	}

	if err := r.ComprehensionStore.SetSettings(settings); err != nil {
		return nil, err
	}

	scope := comprehension.Scope{Type: settings.ScopeType, Key: settings.ScopeKey}
	eff, err := comprehension.Resolve(r.ComprehensionStore, scope)
	if err != nil {
		return nil, err
	}
	return mapEffectiveSettings(eff), nil
}

func (r *mutationResolver) ResetComprehensionSettings(ctx context.Context, scopeType string, scopeKey *string) (bool, error) {
	if r.ComprehensionStore == nil {
		return false, fmt.Errorf("comprehension settings not configured")
	}
	sk := ""
	if scopeKey != nil {
		sk = *scopeKey
	}
	scope := comprehension.Scope{Type: comprehension.ScopeType(scopeType), Key: sk}
	if err := r.ComprehensionStore.DeleteSettings(scope); err != nil {
		return false, err
	}
	return true, nil
}

func (r *mutationResolver) UpdateModelCapabilities(ctx context.Context, input UpdateModelCapabilitiesInput) (*ModelCapabilityProfile, error) {
	if r.ComprehensionStore == nil {
		return nil, fmt.Errorf("comprehension settings not configured")
	}

	// Get existing or create new
	mc, err := r.ComprehensionStore.GetModelCapabilities(input.ModelID)
	if err != nil {
		return nil, err
	}
	if mc == nil {
		mc = &comprehension.ModelCapabilities{
			ModelID:              input.ModelID,
			InstructionFollowing: "low",
			JSONMode:             "none",
			ToolUse:              "none",
			ExtractionGrade:      "low",
			CreativeGrade:        "low",
			Source:               "manual",
		}
	}

	// Apply patches
	if input.Provider != nil {
		mc.Provider = *input.Provider
	}
	if input.DeclaredContextTokens != nil {
		mc.DeclaredContextTokens = *input.DeclaredContextTokens
	}
	if input.EffectiveContextTokens != nil {
		mc.EffectiveContextTokens = *input.EffectiveContextTokens
	}
	if input.InstructionFollowing != nil {
		mc.InstructionFollowing = *input.InstructionFollowing
	}
	if input.JSONMode != nil {
		mc.JSONMode = *input.JSONMode
	}
	if input.ToolUse != nil {
		mc.ToolUse = *input.ToolUse
	}
	if input.ExtractionGrade != nil {
		mc.ExtractionGrade = *input.ExtractionGrade
	}
	if input.CreativeGrade != nil {
		mc.CreativeGrade = *input.CreativeGrade
	}
	if input.EmbeddingModel != nil {
		mc.EmbeddingModel = *input.EmbeddingModel
	}
	if input.Source != nil {
		mc.Source = *input.Source
	}
	if input.Notes != nil {
		mc.Notes = *input.Notes
	}

	if err := r.ComprehensionStore.SetModelCapabilities(mc); err != nil {
		return nil, err
	}
	return mapModelCapability(mc), nil
}

func (r *mutationResolver) DeleteModelCapabilities(ctx context.Context, modelID string) (bool, error) {
	if r.ComprehensionStore == nil {
		return false, fmt.Errorf("comprehension settings not configured")
	}
	if err := r.ComprehensionStore.DeleteModelCapabilities(modelID); err != nil {
		return false, err
	}
	return true, nil
}

// --- Mappers ---

func mapSettings(s *comprehension.Settings) *ComprehensionSettings {
	cs := &ComprehensionSettings{
		ScopeType:               string(s.ScopeType),
		ScopeKey:                s.ScopeKey,
		StrategyPreferenceChain: s.StrategyPreferenceChain,
		RefinePassEnabled:       s.RefinePassEnabled,
		CacheEnabled:            s.CacheEnabled,
		AllowUnsafeCombinations: s.AllowUnsafeCombinations,
	}
	if s.ID != "" {
		cs.ID = &s.ID
	}
	if s.ModelID != "" {
		cs.ModelID = &s.ModelID
	}
	if s.MaxConcurrency > 0 {
		v := s.MaxConcurrency
		cs.MaxConcurrency = &v
	}
	if s.MaxPromptTokens > 0 {
		v := s.MaxPromptTokens
		cs.MaxPromptTokens = &v
	}
	if s.LeafBudgetTokens > 0 {
		v := s.LeafBudgetTokens
		cs.LeafBudgetTokens = &v
	}
	if s.LongContextMaxTokens > 0 {
		v := s.LongContextMaxTokens
		cs.LongContextMaxTokens = &v
	}
	if len(s.GraphRAGEntityTypes) > 0 {
		cs.GraphragEntityTypes = s.GraphRAGEntityTypes
	}
	if !s.UpdatedAt.IsZero() {
		cs.UpdatedAt = &s.UpdatedAt
	}
	if s.UpdatedBy != "" {
		cs.UpdatedBy = &s.UpdatedBy
	}
	return cs
}

func mapEffectiveSettings(eff *comprehension.EffectiveSettings) *EffectiveComprehensionSettings {
	refine := false
	if eff.RefinePassEnabled != nil {
		refine = *eff.RefinePassEnabled
	}
	cache := false
	if eff.CacheEnabled != nil {
		cache = *eff.CacheEnabled
	}
	unsafe := false
	if eff.AllowUnsafeCombinations != nil {
		unsafe = *eff.AllowUnsafeCombinations
	}

	result := &EffectiveComprehensionSettings{
		ScopeType:               string(eff.ScopeType),
		ScopeKey:                eff.ScopeKey,
		StrategyPreferenceChain: eff.StrategyPreferenceChain,
		ModelID:                 eff.ModelID,
		MaxConcurrency:          eff.MaxConcurrency,
		MaxPromptTokens:         eff.MaxPromptTokens,
		LeafBudgetTokens:        eff.LeafBudgetTokens,
		RefinePassEnabled:       refine,
		LongContextMaxTokens:    eff.LongContextMaxTokens,
		GraphragEntityTypes:     eff.GraphRAGEntityTypes,
		CacheEnabled:            cache,
		AllowUnsafeCombinations: unsafe,
	}

	// Convert inherited-from map to list of FieldOrigin
	for field, scope := range eff.InheritedFrom {
		result.InheritedFrom = append(result.InheritedFrom, &FieldOrigin{
			Field:     field,
			ScopeType: string(scope.Type),
			ScopeKey:  scope.Key,
		})
	}
	return result
}

func mapModelCapability(mc *comprehension.ModelCapabilities) *ModelCapabilityProfile {
	p := &ModelCapabilityProfile{
		ModelID:                mc.ModelID,
		Provider:               mc.Provider,
		DeclaredContextTokens:  mc.DeclaredContextTokens,
		EffectiveContextTokens: mc.EffectiveContextTokens,
		InstructionFollowing:   mc.InstructionFollowing,
		JSONMode:               mc.JSONMode,
		ToolUse:                mc.ToolUse,
		ExtractionGrade:        mc.ExtractionGrade,
		CreativeGrade:          mc.CreativeGrade,
		EmbeddingModel:         mc.EmbeddingModel,
		CostPer1kInput:         mc.CostPer1kInput,
		CostPer1kOutput:        mc.CostPer1kOutput,
		LastProbedAt:           mc.LastProbedAt,
		Source:                 mc.Source,
	}
	if mc.ID != "" {
		p.ID = &mc.ID
	}
	if mc.Notes != "" {
		p.Notes = &mc.Notes
	}
	if !mc.UpdatedAt.IsZero() {
		p.UpdatedAt = &mc.UpdatedAt
	}
	return p
}
