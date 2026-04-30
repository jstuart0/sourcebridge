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
//
// Slice 3 of the LLM provider profiles plan adds the three-mode
// discriminator. profileId / clearProfile patch logic:
//
//   - input.ClearProfile == true: drop the saved profile_id (revert to
//     inline mode). Inline fields below are still patched as usual; the
//     two are mutually independent inputs by design (matches existing
//     clearAPIKey precedent per ian-M2).
//
//   - input.ProfileID != nil && *input.ProfileID != "": switch to
//     "saved-profile" mode. The new profile_id is validated against
//     LLMProfileLookup; if the profile is missing the mutation returns
//     a GraphQL error with extensions.code = "PROFILE_NO_LONGER_EXISTS"
//     so the UI can render the resolution panel without persisting a
//     stale reference. ALL inline fields are atomically cleared
//     (provider, baseURL, api_key, models) so the saved row carries
//     only { profile_id: <id> } — one mode per row.
//
//   - input.ProfileID nil OR pointer-to-empty-string: leave the saved
//     profile_id unchanged (matches the password-field UX precedent
//     for apiKey).
//
// Mutual exclusion is enforced server-side: the UI is the surface for
// the three-mode radio, but a malformed mutation that sends both a
// non-empty profileId AND inline fields STILL produces a one-mode row
// (profile mode wins; inline fields silently dropped). This is the
// safer invariant because a stale UI cache cannot accidentally write a
// half-and-half row.
func (r *mutationResolver) SetRepositoryLLMOverride(ctx context.Context, repositoryID string, input RepositoryLLMOverrideInput) (*RepositoryLLMOverride, error) {
	if r.LivingWikiRepoStore == nil {
		return nil, fmt.Errorf("living-wiki repo store not configured")
	}

	// Slice 3: validate the referenced profile BEFORE loading the
	// settings row. If the profile is gone we return an actionable
	// error and never touch the override row, preserving the user's
	// previous state for the resolution-panel flow.
	if input.ProfileID != nil && *input.ProfileID != "" {
		if r.LLMProfileLookup != nil {
			name, exists, lerr := r.LLMProfileLookup.LookupProfileName(ctx, *input.ProfileID)
			if lerr != nil {
				return nil, fmt.Errorf("validate profileId: %w", lerr)
			}
			if !exists {
				return nil, &gqlerror.Error{
					Path:    graphql.GetPath(ctx),
					Message: "The selected profile no longer exists. Pick another profile, switch to inline override, or revert to workspace inheritance.",
					Extensions: map[string]interface{}{
						"code":      "PROFILE_NO_LONGER_EXISTS",
						"profileId": *input.ProfileID,
					},
				}
			}
			// Carry the resolved name on a request-local helper so the
			// returned RepositoryLLMOverride.profileName field reflects
			// the just-saved value without an extra round-trip. Stored
			// in ctx-via-no-op? Simpler: use the var name directly when
			// we build the response below.
			_ = name
		}
		// LLMProfileLookup nil (embedded mode / pre-slice-3 wiring) —
		// skip validation. The resolver still degrades gracefully at
		// resolve time (Warn log + workspace fallback).
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

	// Slice 3: profile-id patch. clearProfile takes precedence over an
	// explicit profileId so a UI race ("clearProfile + profileId in the
	// same patch") never silently leaves both set.
	clearingProfile := input.ClearProfile != nil && *input.ClearProfile
	settingProfile := input.ProfileID != nil && *input.ProfileID != ""
	if clearingProfile {
		ov.ProfileID = ""
	} else if settingProfile {
		ov.ProfileID = *input.ProfileID
		// Mutual exclusion: setting ProfileID atomically clears every
		// inline field. The mutation can be sent with inline fields
		// also populated (a stale UI, a malformed client) and the
		// server still produces a one-mode row.
		ov.Provider = ""
		ov.BaseURL = ""
		ov.APIKey = ""
		ov.AdvancedMode = false
		ov.SummaryModel = ""
		ov.ReviewModel = ""
		ov.AskModel = ""
		ov.KnowledgeModel = ""
		ov.ArchitectureDiagramModel = ""
		ov.ReportModel = ""
		ov.DraftModel = ""
	}

	// Inline-field patch. When the patch is in profile mode, the
	// atomic-clear above already wiped these to empty; the input
	// pointers below would re-stamp them, but the inline field
	// branch is gated on `!settingProfile` so they are skipped — a
	// profile-mode save NEVER persists inline fields.
	if !settingProfile {
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
	}

	// Stamp metadata.
	ov.UpdatedAt = time.Now()
	ov.UpdatedBy = userIDFromContext(ctx)

	// If the patch resulted in a fully empty override (all fields blank,
	// AND no profile_id), behave like clearRepositoryLLMOverride: drop
	// the row entirely. Slice 3: a saved profile_id counts as "set"
	// (LLMOverride.IsEmpty checks ProfileID), so a profile-mode save
	// is never silently dropped.
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
	out := mapLLMOverrideMasked(saved.LLMOverride)
	// Slice 3: populate profileName on the response so the UI doesn't
	// need a follow-up GET. Direct lookup matches ian-M3 (no cache in
	// v1; profile-name fetches are cheap by design).
	if out.ProfileID != nil && *out.ProfileID != "" && r.LLMProfileLookup != nil {
		name, exists, lerr := r.LLMProfileLookup.LookupProfileName(ctx, *out.ProfileID)
		if lerr == nil && exists {
			n := name
			out.ProfileName = &n
		}
		// On lookup error or "exists=false", leave profileName nil. We
		// already validated existence at the top of the mutation, so
		// an exists=false here is only possible under a vanishingly
		// rare race (profile deleted between mutation save and reload).
		// The next read pass will surface PROFILE_NO_LONGER_EXISTS.
	}
	return out, nil
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
//
// Slice 3 of the LLM provider profiles plan: when the override is in
// "saved-profile" mode (ProfileID non-empty), the field resolver
// populates profileName via LLMProfileLookup. If the referenced profile
// has been deleted, the resolver returns the override (with profileId
// set, profileName nil) AND a non-fatal GraphQL error carrying
// extensions.code = "PROFILE_NO_LONGER_EXISTS" via the gqlgen
// AddError plumbing — but pragmatic gqlgen wiring is to return the data
// AND the error from the resolver (gqlgen sends both as a partial
// response). The UI listens for this extension code and renders the
// resolution panel; it does NOT navigate away or hide the override.
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
	out := mapLLMOverrideMasked(settings.LLMOverride)
	// Slice 3: enrich profileName from the profile store. Resolution
	// failure handling:
	//   - exists=true → populate profileName.
	//   - exists=false → leave profileName nil AND return a structured
	//     error (data + error) so the UI's resolution-panel listener
	//     fires.
	//   - lookup fails entirely → log + leave profileName nil; do NOT
	//     surface PROFILE_NO_LONGER_EXISTS without proof. A DB outage
	//     should not be misrendered as "your profile is deleted".
	if out.ProfileID != nil && *out.ProfileID != "" && r.LLMProfileLookup != nil {
		name, exists, lerr := r.LLMProfileLookup.LookupProfileName(ctx, *out.ProfileID)
		if lerr != nil {
			// Generic lookup failure — surface but do NOT misclassify.
			return out, fmt.Errorf("lookup profileName: %w", lerr)
		}
		if !exists {
			return out, &gqlerror.Error{
				Path:    graphql.GetPath(ctx),
				Message: "The profile referenced by this repository's override no longer exists. Pick another profile, switch to inline override, or revert to workspace inheritance.",
				Extensions: map[string]interface{}{
					"code":      "PROFILE_NO_LONGER_EXISTS",
					"profileId": *out.ProfileID,
				},
			}
		}
		n := name
		out.ProfileName = &n
	}
	return out, nil
}

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
