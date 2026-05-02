// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Helpers for the per-repository LLM override GraphQL surface.
//
// Slice 2 of plan 2026-04-29-workspace-llm-source-of-truth-r2.md:
// the data layer (LLMOverride struct, encrypted at rest under the
// sbenc:v1 envelope) is exposed via three GraphQL operations:
//
//   - Mutation.setRepositoryLLMOverride — patch-merge save
//   - Mutation.clearRepositoryLLMOverride — drop the override row
//   - RepositoryLivingWikiSettings.llmOverride — masked field resolver
//
// The api_key is never returned in plaintext on any path. The
// field-resolver loads the saved cipher; apiKeySet+apiKeyHint expose
// enough for the UI to render saved-state without leaking the secret.
//
// CA-138: the three resolver methods listed above moved into the
// gqlgen-managed schema.resolvers.go (resolver-ownership inversion —
// gqlgen's follow-schema layout regenerates resolver bodies from the
// package-wide method scan, so dedicated copies couldn't be made
// idempotent). This file retains the helper that masks the persisted
// override before returning it to GraphQL clients.

package graphql

import (
	"github.com/sourcebridge/sourcebridge/internal/maskutil"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// mapLLMOverrideMasked converts a domain livingwiki.LLMOverride into the
// GraphQL RepositoryLLMOverride view. The api_key is replaced by
// apiKeySet (bool) + apiKeyHint (masked preview) — the raw key is never
// emitted on any path.
//
// Slice 3 of the LLM provider profiles plan: ProfileID is propagated
// through verbatim. ProfileName is NOT populated here — the caller
// (field resolver / mutation) enriches it via LLMProfileLookup so the
// non-DB-touching pure mapper stays pure.
func mapLLMOverrideMasked(ov *livingwiki.LLMOverride) *RepositoryLLMOverride {
	if ov == nil {
		return nil
	}

	out := &RepositoryLLMOverride{
		APIKeySet:    ov.APIKey != "",
		AdvancedMode: ov.AdvancedMode,
	}

	// Slice 3: surface profileId so the UI knows which mode the
	// override is in (profile vs. inline). profileName is enriched by
	// the caller (field resolver) via LLMProfileLookup.
	if ov.ProfileID != "" {
		v := ov.ProfileID
		out.ProfileID = &v
	}

	// Optional string fields: emit nil for empty so callers can
	// distinguish "no value saved" from "explicit empty string".
	if ov.Provider != "" {
		v := ov.Provider
		out.Provider = &v
	}
	if ov.BaseURL != "" {
		v := ov.BaseURL
		out.BaseURL = &v
	}
	if ov.APIKey != "" {
		hint := maskutil.Token(ov.APIKey)
		out.APIKeyHint = &hint
	}
	if ov.SummaryModel != "" {
		v := ov.SummaryModel
		out.SummaryModel = &v
	}
	if ov.ReviewModel != "" {
		v := ov.ReviewModel
		out.ReviewModel = &v
	}
	if ov.AskModel != "" {
		v := ov.AskModel
		out.AskModel = &v
	}
	if ov.KnowledgeModel != "" {
		v := ov.KnowledgeModel
		out.KnowledgeModel = &v
	}
	if ov.ArchitectureDiagramModel != "" {
		v := ov.ArchitectureDiagramModel
		out.ArchitectureDiagramModel = &v
	}
	if ov.ReportModel != "" {
		v := ov.ReportModel
		out.ReportModel = &v
	}
	if ov.DraftModel != "" {
		v := ov.DraftModel
		out.DraftModel = &v
	}

	if !ov.UpdatedAt.IsZero() {
		t := ov.UpdatedAt
		out.UpdatedAt = &t
	}
	if ov.UpdatedBy != "" {
		v := ov.UpdatedBy
		out.UpdatedBy = &v
	}

	return out
}
