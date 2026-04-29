// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Resolvers for the per-repository LLM override GraphQL surface.
//
// Slice 2 of plan 2026-04-29-workspace-llm-source-of-truth-r2.md:
// exposes the existing data layer (LLMOverride struct, encrypted at
// rest under the sbenc:v1 envelope) via three GraphQL operations:
//
//   - Mutation.setRepositoryLLMOverride — patch-merge save
//   - Mutation.clearRepositoryLLMOverride — drop the override row
//   - RepositoryLivingWikiSettings.llmOverride — masked field resolver
//
// The api_key is never returned in plaintext on any path. The
// field-resolver loads the saved cipher; apiKeySet+apiKeyHint expose
// enough for the UI to render saved-state without leaking the secret.

package graphql

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/sourcebridge/sourcebridge/internal/maskutil"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// SetRepositoryLLMOverride is the resolver for the setRepositoryLLMOverride
// field. Patch semantics: omitted (nil) fields preserve the saved value;
// empty-string non-secret fields clear that field back to workspace
// inheritance; non-empty values overwrite. apiKey has its own clearAPIKey
// flag (see schema docs).
//
// Returns a GraphQL error with extension code "ENCRYPTION_KEY_REQUIRED"
// when the request includes a non-empty apiKey but the server's
// SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY is not configured. The UI uses
// this code to render a clear actionable message.
func (r *mutationResolver) SetRepositoryLLMOverride(ctx context.Context, repositoryID string, input RepositoryLLMOverrideInput) (*RepositoryLLMOverride, error) {
	if r.LivingWikiRepoStore == nil {
		return nil, fmt.Errorf("living-wiki repo store not configured")
	}

	current, err := r.LivingWikiRepoStore.GetRepoSettings(ctx, defaultTenantID, repositoryID)
	if err != nil {
		return nil, fmt.Errorf("load repo settings: %w", err)
	}
	if current == nil {
		current = defaultRepoSettings(repositoryID)
	}

	// Start from existing override (or empty) and apply the patch.
	ov := current.LLMOverride
	if ov == nil {
		ov = &livingwiki.LLMOverride{}
	} else {
		// Copy so we don't mutate the in-memory record before save.
		copyOv := *ov
		ov = &copyOv
	}

	// String fields: nil = leave alone, "" = clear, non-empty = set.
	if input.Provider != nil {
		ov.Provider = *input.Provider
	}
	if input.BaseURL != nil {
		ov.BaseURL = *input.BaseURL
	}
	if input.SummaryModel != nil {
		ov.SummaryModel = *input.SummaryModel
	}
	if input.ReviewModel != nil {
		ov.ReviewModel = *input.ReviewModel
	}
	if input.AskModel != nil {
		ov.AskModel = *input.AskModel
	}
	if input.KnowledgeModel != nil {
		ov.KnowledgeModel = *input.KnowledgeModel
	}
	if input.ArchitectureDiagramModel != nil {
		ov.ArchitectureDiagramModel = *input.ArchitectureDiagramModel
	}
	if input.ReportModel != nil {
		ov.ReportModel = *input.ReportModel
	}
	if input.DraftModel != nil {
		ov.DraftModel = *input.DraftModel
	}

	// Bool field: nil = leave alone, present = set.
	if input.AdvancedMode != nil {
		ov.AdvancedMode = *input.AdvancedMode
	}

	// apiKey patch semantics:
	//   - clearAPIKey true → drop the saved cipher.
	//   - apiKey present and non-empty → replace.
	//   - apiKey nil OR present-but-empty → leave the saved cipher alone.
	//     (Empty-string is treated as "leave alone" to avoid ambiguous UX
	//     on a password field that submits an empty value when the user
	//     never touched it.)
	if input.ClearAPIKey != nil && *input.ClearAPIKey {
		ov.APIKey = ""
	} else if input.APIKey != nil && *input.APIKey != "" {
		ov.APIKey = *input.APIKey
	}

	// Stamp metadata.
	ov.UpdatedAt = time.Now()
	ov.UpdatedBy = userIDFromContext(ctx)

	// If the patch resulted in a fully empty override (all fields blank),
	// behave like clearRepositoryLLMOverride: drop the row entirely. This
	// keeps "set every field empty" → "no override" symmetric with the
	// explicit clear mutation.
	if ov.IsEmpty() {
		current.LLMOverride = nil
	} else {
		current.LLMOverride = ov
	}

	current.UpdatedAt = time.Now()
	current.UpdatedBy = userIDFromContext(ctx)

	if err := r.LivingWikiRepoStore.SetRepoSettings(ctx, *current); err != nil {
		// Map the encryption-key-required sentinel into a GraphQL error
		// with an extension code so the UI can render a precise message.
		if errors.Is(err, livingwiki.ErrEncryptionKeyRequired) {
			return nil, &gqlerror.Error{
				Path:    graphql.GetPath(ctx),
				Message: "Cannot save API key: SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY is not set on the server. Set the encryption key (32+ random bytes, base64-encoded) and restart, or omit the apiKey field to save other settings only.",
				Extensions: map[string]interface{}{
					"code": "ENCRYPTION_KEY_REQUIRED",
				},
			}
		}
		return nil, fmt.Errorf("persist repo settings: %w", err)
	}

	// Re-load to get the saved value (so the cipher round-trip + masking
	// reflects what's actually persisted, not what the request claimed).
	saved, err := r.LivingWikiRepoStore.GetRepoSettings(ctx, defaultTenantID, repositoryID)
	if err != nil {
		return nil, fmt.Errorf("reload repo settings after save: %w", err)
	}
	if saved == nil || saved.LLMOverride == nil {
		// Whole-row save with empty override → return an empty masked view.
		return &RepositoryLLMOverride{}, nil
	}
	return mapLLMOverrideMasked(saved.LLMOverride), nil
}

// ClearRepositoryLLMOverride removes the per-repository LLM override
// entirely. Returns the updated RepositoryLivingWikiSettings record.
func (r *mutationResolver) ClearRepositoryLLMOverride(ctx context.Context, repositoryID string) (*RepositoryLivingWikiSettings, error) {
	if r.LivingWikiRepoStore == nil {
		return nil, fmt.Errorf("living-wiki repo store not configured")
	}

	current, err := r.LivingWikiRepoStore.GetRepoSettings(ctx, defaultTenantID, repositoryID)
	if err != nil {
		return nil, fmt.Errorf("load repo settings: %w", err)
	}
	if current == nil {
		// Nothing to clear; return an empty default record so the UI
		// can render a "no override; inheriting workspace" hint.
		current = defaultRepoSettings(repositoryID)
	}
	current.LLMOverride = nil
	current.UpdatedAt = time.Now()
	current.UpdatedBy = userIDFromContext(ctx)

	if err := r.LivingWikiRepoStore.SetRepoSettings(ctx, *current); err != nil {
		return nil, fmt.Errorf("persist repo settings: %w", err)
	}
	return mapRepoLivingWikiSettings(current), nil
}

// LlmOverride is the field resolver for RepositoryLivingWikiSettings.llmOverride.
// Loads the override from the repo store and returns a masked GraphQL
// view (api_key is never returned in plaintext).
//
// Returns nil when no override exists for the repo. Errors propagate as
// GraphQL errors so the caller surfaces "couldn't load" rather than
// silently rendering a stale value.
func (r *repositoryLivingWikiSettingsResolver) LlmOverride(ctx context.Context, obj *RepositoryLivingWikiSettings) (*RepositoryLLMOverride, error) {
	if r.LivingWikiRepoStore == nil {
		return nil, nil
	}
	if obj == nil || obj.RepoID == "" {
		return nil, nil
	}
	settings, err := r.LivingWikiRepoStore.GetRepoSettings(ctx, defaultTenantID, obj.RepoID)
	if err != nil {
		return nil, fmt.Errorf("load repo settings for llmOverride: %w", err)
	}
	if settings == nil || settings.LLMOverride == nil {
		return nil, nil
	}
	return mapLLMOverrideMasked(settings.LLMOverride), nil
}

// mapLLMOverrideMasked converts a domain livingwiki.LLMOverride into the
// GraphQL RepositoryLLMOverride view. The api_key is replaced by
// apiKeySet (bool) + apiKeyHint (masked preview) — the raw key is never
// emitted on any path.
func mapLLMOverrideMasked(ov *livingwiki.LLMOverride) *RepositoryLLMOverride {
	if ov == nil {
		return nil
	}

	out := &RepositoryLLMOverride{
		APIKeySet:    ov.APIKey != "",
		AdvancedMode: ov.AdvancedMode,
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
