// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package comprehension

// scopeBroadToSpecific is the inheritance chain from broadest to most specific.
var scopeBroadToSpecific = []ScopeType{ScopeWorkspace, ScopeCorpusType, ScopeArtifactType, ScopeUser}

// Resolve computes the effective settings for a target scope by walking
// the inheritance chain from the target scope up to workspace defaults.
// Each field is sourced from the most specific scope that sets it.
func Resolve(store Store, target Scope) (*EffectiveSettings, error) {
	defaults := DefaultSettings()
	eff := &EffectiveSettings{
		Settings:      defaults,
		InheritedFrom: map[string]Scope{},
	}

	// Walk from broadest (workspace) to most specific (target) so
	// later values override earlier ones.
	chain := buildChain(target)
	for _, scope := range chain {
		s, err := store.GetSettings(scope)
		if err != nil {
			return nil, err
		}
		if s == nil {
			continue
		}
		applyOverrides(eff, s, scope)
	}

	// Overwrite scope identity to reflect the target.
	eff.ScopeType = target.Type
	eff.ScopeKey = target.Key

	return eff, nil
}

// buildChain returns the scope hierarchy from broadest to the target.
// For example, if target is {artifact_type, "cliff_notes"}, the chain is:
//
//	{workspace, default} -> {corpus_type, ""} -> {artifact_type, cliff_notes}
//
// User scope includes all four levels.
func buildChain(target Scope) []Scope {
	var chain []Scope
	for _, st := range scopeBroadToSpecific {
		if st == target.Type {
			chain = append(chain, target)
			break
		}
		// Include broader scopes. Workspace always uses the default key;
		// intermediate scopes use empty key so operators can set
		// type-level defaults without naming every specific value.
		if st == ScopeWorkspace {
			chain = append(chain, WorkspaceScope)
		} else {
			chain = append(chain, Scope{Type: st, Key: ""})
		}
	}
	if len(chain) == 0 {
		chain = []Scope{WorkspaceScope}
	}
	return chain
}

// applyOverrides merges non-zero fields from src into eff, recording
// the scope origin.
func applyOverrides(eff *EffectiveSettings, src *Settings, scope Scope) {
	if len(src.StrategyPreferenceChain) > 0 {
		eff.StrategyPreferenceChain = src.StrategyPreferenceChain
		eff.InheritedFrom["strategyPreferenceChain"] = scope
	}
	if src.KnowledgeGenerationModeDefault != "" {
		eff.KnowledgeGenerationModeDefault = src.KnowledgeGenerationModeDefault
		eff.InheritedFrom["knowledgeGenerationModeDefault"] = scope
	}
	if src.ModelID != "" {
		eff.ModelID = src.ModelID
		eff.InheritedFrom["modelId"] = scope
	}
	if src.MaxConcurrency > 0 {
		eff.MaxConcurrency = src.MaxConcurrency
		eff.InheritedFrom["maxConcurrency"] = scope
	}
	if src.MaxPromptTokens > 0 {
		eff.MaxPromptTokens = src.MaxPromptTokens
		eff.InheritedFrom["maxPromptTokens"] = scope
	}
	if src.LeafBudgetTokens > 0 {
		eff.LeafBudgetTokens = src.LeafBudgetTokens
		eff.InheritedFrom["leafBudgetTokens"] = scope
	}
	if src.RefinePassEnabled != nil {
		eff.RefinePassEnabled = src.RefinePassEnabled
		eff.InheritedFrom["refinePassEnabled"] = scope
	}
	if src.LongContextMaxTokens > 0 {
		eff.LongContextMaxTokens = src.LongContextMaxTokens
		eff.InheritedFrom["longContextMaxTokens"] = scope
	}
	if len(src.GraphRAGEntityTypes) > 0 {
		eff.GraphRAGEntityTypes = src.GraphRAGEntityTypes
		eff.InheritedFrom["graphragEntityTypes"] = scope
	}
	if src.CacheEnabled != nil {
		eff.CacheEnabled = src.CacheEnabled
		eff.InheritedFrom["cacheEnabled"] = scope
	}
	if src.AllowUnsafeCombinations != nil {
		eff.AllowUnsafeCombinations = src.AllowUnsafeCombinations
		eff.InheritedFrom["allowUnsafeCombinations"] = scope
	}
}
