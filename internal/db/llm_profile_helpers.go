// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/surrealdb/surrealdb.go"
)

// ─────────────────────────────────────────────────────────────────────────
// CAS-guarded helpers for the profile-aware write path (codex-H2 / r1c / r1d)
// ─────────────────────────────────────────────────────────────────────────
//
// Every new-code write that touches the active profile or the workspace
// version MUST go through one of these helpers. They wrap a SurrealDB
// `BEGIN; ... COMMIT;` batch with:
//   - a CAS guard on `ca_llm_config:default.version` (so two concurrent
//     new-code writers serialize cleanly via THROW + retry),
//   - a workspace-version bump,
//   - active-profile watermark advance to the post-bump version
//     (so the resolver's reconciliation step can distinguish old-pod
//      legacy writes from new-code edits — codex-r1d M1).
//
// On `THROW ca_llm_config_version_changed` the helper translates the
// error to ErrVersionConflict so callers know to re-read and retry. On
// `THROW profile_not_found` it returns ErrProfileNotFound. On
// `THROW profile_now_active_use_active_helper` (raced activation
// promoted the target into active during the write) it returns
// ErrTargetNoLongerActive — the caller maps this to 409 at the API.
//
// The helpers don't loop internally. Looping (cap=3) lives at the
// caller layer where the original patch / activation intent is known
// — see writeActiveProfileWithRetry / activateProfileWithRetry below.

// ProfilePatch is the value-shape applied by the active-profile and
// non-active-profile helpers. APIKey here is the FINAL SEALED FORM
// (post-cipher.Encrypt). Helpers do NOT encrypt; the caller passes
// a sealed value so the SQL can mirror it byte-for-byte to the
// legacy row when needed. APIKeyMode controls whether the api_key
// column is updated:
//   - apiKeyKeep   = no api_key clause in the UPDATE (preserve)
//   - apiKeyClear  = api_key = '' (zero ciphertext)
//   - apiKeySet    = api_key = $api_key (the sealed bytes)
type ProfilePatch struct {
	Provider                 string
	BaseURL                  string
	APIKey                   string // sealed bytes (sbenc:v1:<...> or empty); only used when APIKeyMode == apiKeySet
	APIKeyMode               apiKeyMode
	SummaryModel             string
	ReviewModel              string
	AskModel                 string
	KnowledgeModel           string
	ArchitectureDiagramModel string
	ReportModel              string
	DraftModel               string
	TimeoutSecs              int
	AdvancedMode             bool
	// FieldsPresent declares which of the above scalar fields the patch
	// actually changes. Fields not present are preserved (the helper
	// skips writing them). This mirrors the pointer-patch semantics of
	// ProfileUpdate for the helper layer, where we can't carry pointers
	// across the SurrealQL boundary.
	FieldsPresent ProfilePatchFields
}

// ProfilePatchFields names which scalar fields are present in the
// ProfilePatch. Booleans for value-typed fields; api_key is conveyed
// via APIKeyMode.
type ProfilePatchFields struct {
	Provider                 bool
	BaseURL                  bool
	SummaryModel             bool
	ReviewModel              bool
	AskModel                 bool
	KnowledgeModel           bool
	ArchitectureDiagramModel bool
	ReportModel              bool
	DraftModel               bool
	TimeoutSecs              bool
	AdvancedMode             bool
}

type apiKeyMode int

const (
	apiKeyKeep apiKeyMode = iota
	apiKeyClear
	apiKeySet
)

// APIKeyModeKeep returns the apiKeyMode that preserves the existing
// api_key column. Exposed for cross-package callers (cli wiring +
// REST handlers) that build ProfilePatch values without importing
// the package-internal constants.
func APIKeyModeKeep() apiKeyMode { return apiKeyKeep }

// APIKeyModeClear returns the apiKeyMode that zeros the api_key
// column (writes empty ciphertext).
func APIKeyModeClear() apiKeyMode { return apiKeyClear }

// APIKeyModeSet returns the apiKeyMode that writes the patch's
// already-sealed APIKey bytes into the column.
func APIKeyModeSet() apiKeyMode { return apiKeySet }

// ─────────────────────────────────────────────────────────────────────────
// Sentinel errors
// ─────────────────────────────────────────────────────────────────────────

// ErrVersionConflict is returned when the workspace version cell has
// been bumped by another writer between the caller's snapshot read and
// this helper's BEGIN/COMMIT. Caller re-reads + retries (cap 3).
var ErrVersionConflict = errors.New("ca_llm_config version changed since read; retry with fresh snapshot")

// ErrWatermarkConflict is returned by the reconciler when the active
// profile's last_legacy_version_consumed has advanced since the
// snapshot read. Resolver treats this the same as ErrVersionConflict.
var ErrWatermarkConflict = errors.New("active profile watermark changed since read; another reconciler raced")

// ErrTargetNoLongerActive is returned by writeActiveProfileWithLegacyMirror
// when the writer's intended-active-id no longer matches
// ca_llm_config:default.active_profile_id (a concurrent activation
// flipped the active profile while this writer was preparing). Caller
// maps to 409 at the API (codex-r1e Low — explicit, no fall-through).
var ErrTargetNoLongerActive = errors.New("intended active profile is no longer active; another writer activated a different profile")

// ErrLegacyChanged is returned by MigrateToProfiles' BEGIN/COMMIT batch
// when an old-pod legacy SaveLLMConfig commits between the migration's
// step-2 read and the batch's CAS guard. Caller retries from step 1
// with a fresh read (codex-r1d-NEW).
var ErrLegacyChanged = errors.New("ca_llm_config:default version changed during migration batch; old-pod legacy write interleaved")

// ─────────────────────────────────────────────────────────────────────────
// SurrealDB error → Go sentinel translation
// ─────────────────────────────────────────────────────────────────────────

// translateThrowErr inspects a SurrealDB error message for the THROW
// strings used by the helpers' BEGIN/COMMIT batches and returns the
// matching Go sentinel. Returns nil if no known THROW string is
// present (caller wraps the raw error).
func translateThrowErr(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "ca_llm_config_version_changed_during_reconcile"),
		strings.Contains(msg, "ca_llm_config_version_changed"):
		return ErrVersionConflict
	case strings.Contains(msg, "active_profile_watermark_changed_during_reconcile"),
		strings.Contains(msg, "active_profile_watermark_changed"):
		return ErrWatermarkConflict
	case strings.Contains(msg, "profile_not_found"):
		return ErrProfileNotFound
	case strings.Contains(msg, "profile_now_active_use_active_helper"):
		return ErrTargetNoLongerActive
	case strings.Contains(msg, "llm_profile_migration_legacy_changed"):
		return ErrLegacyChanged
	}
	return nil
}

// runBatch executes a BEGIN/COMMIT batch and translates any THROW
// errors into typed sentinels. Returns the raw error otherwise.
func runBatch(ctx context.Context, surrealDB *SurrealDB, sql string, vars map[string]any) error {
	db := surrealDB.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	raw, err := surrealdb.Query[interface{}](ctx, db, sql, vars)
	if err != nil {
		if typed := translateThrowErr(err); typed != nil {
			return typed
		}
		return err
	}
	if raw == nil {
		return nil
	}
	// Each statement in the batch produces a query result. If any one
	// has an Error field, surface it.
	for _, qr := range *raw {
		if qr.Error != nil {
			errStr := fmt.Sprintf("%v", qr.Error)
			if typed := translateThrowErr(errors.New(errStr)); typed != nil {
				return typed
			}
			return fmt.Errorf("batch query error: %s", errStr)
		}
	}
	return nil
}

// buildProfileFieldClause assembles the SET-clause fragment for the
// profile UPDATE (only fields that FieldsPresent flags as true). The
// clause is used in writeActiveProfileWithLegacyMirror and
// writeNonActiveProfileWithWatermarkBump.
//
// Returns the SET fragment string (no leading comma, no trailing comma)
// and the params map keyed by `$profile_<col>` so the SQL can bind them.
func buildProfileFieldClauses(p ProfilePatch, prefix string) (string, map[string]any) {
	clauses := []string{}
	vars := map[string]any{}
	add := func(col string, ok bool, val any) {
		if !ok {
			return
		}
		key := prefix + col
		clauses = append(clauses, fmt.Sprintf("%s = $%s", col, key))
		vars[key] = val
	}
	add("provider", p.FieldsPresent.Provider, p.Provider)
	add("base_url", p.FieldsPresent.BaseURL, p.BaseURL)
	add("summary_model", p.FieldsPresent.SummaryModel, p.SummaryModel)
	add("review_model", p.FieldsPresent.ReviewModel, p.ReviewModel)
	add("ask_model", p.FieldsPresent.AskModel, p.AskModel)
	add("knowledge_model", p.FieldsPresent.KnowledgeModel, p.KnowledgeModel)
	add("architecture_diagram_model", p.FieldsPresent.ArchitectureDiagramModel, p.ArchitectureDiagramModel)
	add("report_model", p.FieldsPresent.ReportModel, p.ReportModel)
	add("draft_model", p.FieldsPresent.DraftModel, p.DraftModel)
	add("timeout_secs", p.FieldsPresent.TimeoutSecs, p.TimeoutSecs)
	add("advanced_mode", p.FieldsPresent.AdvancedMode, p.AdvancedMode)

	switch p.APIKeyMode {
	case apiKeyClear:
		clauses = append(clauses, "api_key = ''")
	case apiKeySet:
		clauses = append(clauses, fmt.Sprintf("api_key = $%sapi_key", prefix))
		vars[prefix+"api_key"] = p.APIKey
	}

	return strings.Join(clauses, ", "), vars
}

// ─────────────────────────────────────────────────────────────────────────
// writeActiveProfileWithLegacyMirror (codex-H2 / r1d)
// ─────────────────────────────────────────────────────────────────────────

// writeActiveProfileWithLegacyMirror commits a patch to the currently-
// active profile AND mirrors the patched fields back to the legacy
// `ca_llm_config:default` row, in a single CAS-guarded BEGIN/COMMIT.
//
// Used by:
//   - PUT /admin/llm-profiles/{id} when id == active_profile_id
//   - legacy PUT /admin/llm-config (translates to a patch against the active profile)
//
// CAS contract:
//   - observedVersion is the workspace.version the caller READ before
//     building the patch. The batch THROWs ca_llm_config_version_changed
//     if version has advanced; caller re-reads + retries.
//   - The batch ALSO re-reads active_profile_id inside the transaction.
//     If it differs from intendedActiveID, the batch THROWs
//     ca_llm_config_version_changed (the version must also have moved
//     since activations bump it); the caller's retry then sees the new
//     active id and decides whether to surface 409 target_no_longer_active
//     or to proceed (see writeActiveProfileWithRetry).
func writeActiveProfileWithLegacyMirror(
	ctx context.Context,
	surrealDB *SurrealDB,
	observedVersion uint64,
	intendedActiveID string,
	patch ProfilePatch,
) error {
	tableName, recordID, ok := splitRecordID(intendedActiveID)
	if !ok || tableName != "ca_llm_profile" {
		return ErrProfileNotFound
	}

	profileSet, profileVars := buildProfileFieldClauses(patch, "profile_")
	legacySet, legacyVars := buildProfileFieldClauses(patch, "legacy_")

	// Build the SQL with conditional SET fragments. We ALWAYS update
	// updated_at + last_legacy_version_consumed on the profile, and
	// version + updated_at on the workspace row. The patch fields
	// only land if FieldsPresent flagged them.
	profileSetFull := profileSet
	if profileSetFull != "" {
		profileSetFull += ", "
	}
	profileSetFull += "updated_at = type::datetime($now), last_legacy_version_consumed = $new_version"

	legacySetFull := legacySet
	if legacySetFull != "" {
		legacySetFull += ", "
	}
	legacySetFull += "version = $new_version, updated_at = type::datetime($now)"

	sql := fmt.Sprintf(`
		BEGIN;
		LET $cur = (SELECT version, active_profile_id FROM ca_llm_config:default)[0];
		IF $cur.version != $observed_version {
			THROW "ca_llm_config_version_changed";
		};
		IF $cur.active_profile_id != $intended_active_id {
			THROW "ca_llm_config_version_changed";
		};
		LET $new_version = $cur.version + 1;
		UPDATE type::thing('ca_llm_profile', $profile_rid) SET %s;
		UPDATE ca_llm_config:default SET %s;
		COMMIT;
	`, profileSetFull, legacySetFull)

	vars := map[string]any{
		"observed_version":   observedVersion,
		"intended_active_id": intendedActiveID,
		"profile_rid":        recordID,
		"now":                time.Now().UTC().Format(time.RFC3339Nano),
	}
	for k, v := range profileVars {
		vars[k] = v
	}
	for k, v := range legacyVars {
		vars[k] = v
	}
	return runBatch(ctx, surrealDB, sql, vars)
}

// ─────────────────────────────────────────────────────────────────────────
// writeNonActiveProfileWithWatermarkBump (codex-r1d M1)
// ─────────────────────────────────────────────────────────────────────────

// writeNonActiveProfileWithWatermarkBump commits a patch to a profile
// that is NOT currently active, advances the active profile's watermark
// to the post-bump workspace.version (so the resolver doesn't false-
// positive the next read into a reconciliation), and bumps workspace.version.
//
// THROW conditions:
//   - workspace.version != observedVersion → ErrVersionConflict
//   - target profile is the current active one → ErrTargetNoLongerActive
//     (the caller switches to writeActiveProfileWithLegacyMirror)
func writeNonActiveProfileWithWatermarkBump(
	ctx context.Context,
	surrealDB *SurrealDB,
	observedVersion uint64,
	profileID string,
	patch ProfilePatch,
) error {
	tableName, recordID, ok := splitRecordID(profileID)
	if !ok || tableName != "ca_llm_profile" {
		return ErrProfileNotFound
	}

	profileSet, profileVars := buildProfileFieldClauses(patch, "profile_")
	profileSetFull := profileSet
	if profileSetFull != "" {
		profileSetFull += ", "
	}
	profileSetFull += "updated_at = type::datetime($now)"

	// codex-r1e M1: bind $active_id explicitly via LET so the conditional
	// UPDATE compiles against a known variable. Without the LET, SurrealDB
	// raises an undefined-variable error on parse.
	sql := fmt.Sprintf(`
		BEGIN;
		LET $cur = (SELECT version, active_profile_id FROM ca_llm_config:default)[0];
		IF $cur.version != $observed_version {
			THROW "ca_llm_config_version_changed";
		};
		LET $active_id = $cur.active_profile_id;
		IF $profile_id == $active_id {
			THROW "profile_now_active_use_active_helper";
		};
		LET $new_version = $cur.version + 1;
		UPDATE type::thing('ca_llm_profile', $profile_rid) SET %s;
		IF $active_id != "" {
			UPDATE type::thing('ca_llm_profile', string::split($active_id, ':')[1]) SET
				last_legacy_version_consumed = $new_version,
				updated_at = type::datetime($now);
		};
		UPDATE ca_llm_config:default SET version = $new_version, updated_at = type::datetime($now);
		COMMIT;
	`, profileSetFull)

	vars := map[string]any{
		"observed_version": observedVersion,
		"profile_id":       profileID,
		"profile_rid":      recordID,
		"now":              time.Now().UTC().Format(time.RFC3339Nano),
	}
	for k, v := range profileVars {
		vars[k] = v
	}
	return runBatch(ctx, surrealDB, sql, vars)
}

// ─────────────────────────────────────────────────────────────────────────
// activateProfileWithLegacyMirror (codex-r1d M2)
// ─────────────────────────────────────────────────────────────────────────

// activateProfileWithLegacyMirror flips active_profile_id to a new id,
// mirrors the new active profile's contents back onto the legacy row,
// and advances the new active profile's watermark — all in one
// CAS-guarded batch.
//
// THROW conditions:
//   - workspace.version != observedVersion → ErrVersionConflict
//   - target profile id does not exist     → ErrProfileNotFound
func activateProfileWithLegacyMirror(
	ctx context.Context,
	surrealDB *SurrealDB,
	observedVersion uint64,
	newActiveID string,
) error {
	tableName, recordID, ok := splitRecordID(newActiveID)
	if !ok || tableName != "ca_llm_profile" {
		return ErrProfileNotFound
	}
	sql := `
		BEGIN;
		LET $cur = (SELECT version, active_profile_id FROM ca_llm_config:default)[0];
		IF $cur.version != $observed_version {
			THROW "ca_llm_config_version_changed";
		};
		LET $new_profile = (SELECT * FROM ca_llm_profile WHERE id = type::thing('ca_llm_profile', $new_active_rid) LIMIT 1)[0];
		IF $new_profile == NONE {
			THROW "profile_not_found";
		};
		LET $new_version = $cur.version + 1;
		UPDATE ca_llm_config:default SET
			active_profile_id          = $new_active_id,
			provider                   = $new_profile.provider,
			base_url                   = $new_profile.base_url,
			api_key                    = $new_profile.api_key,
			summary_model              = $new_profile.summary_model,
			review_model               = $new_profile.review_model,
			ask_model                  = $new_profile.ask_model,
			knowledge_model            = $new_profile.knowledge_model,
			architecture_diagram_model = $new_profile.architecture_diagram_model,
			report_model               = $new_profile.report_model,
			draft_model                = $new_profile.draft_model,
			timeout_secs               = $new_profile.timeout_secs,
			advanced_mode              = $new_profile.advanced_mode,
			version                    = $new_version,
			updated_at                 = type::datetime($now);
		UPDATE type::thing('ca_llm_profile', $new_active_rid) SET
			last_legacy_version_consumed = $new_version,
			updated_at                   = type::datetime($now);
		COMMIT;
	`
	vars := map[string]any{
		"observed_version": observedVersion,
		"new_active_id":    newActiveID,
		"new_active_rid":   recordID,
		"now":              time.Now().UTC().Format(time.RFC3339Nano),
	}
	return runBatch(ctx, surrealDB, sql, vars)
}

// ─────────────────────────────────────────────────────────────────────────
// deleteNonActiveProfile (codex-r1d M1; xander-M1 zero-ciphertext)
// ─────────────────────────────────────────────────────────────────────────

// deleteNonActiveProfile zeros the profile's api_key, deletes the row,
// advances the active profile's watermark, and bumps workspace.version
// — all in one CAS-guarded batch.
//
// THROW conditions:
//   - workspace.version != observedVersion → ErrVersionConflict
//   - target profile is currently active   → ErrTargetNoLongerActive
//     (the API caller's pre-check should have rejected this, but
//      defense-in-depth)
//
// Returns nil if the profile has already been deleted (no-op idempotent).
func deleteNonActiveProfile(
	ctx context.Context,
	surrealDB *SurrealDB,
	observedVersion uint64,
	profileID string,
) error {
	tableName, recordID, ok := splitRecordID(profileID)
	if !ok || tableName != "ca_llm_profile" {
		return ErrProfileNotFound
	}
	sql := `
		BEGIN;
		LET $cur = (SELECT version, active_profile_id FROM ca_llm_config:default)[0];
		IF $cur.version != $observed_version {
			THROW "ca_llm_config_version_changed";
		};
		LET $active_id = $cur.active_profile_id;
		IF $profile_id == $active_id {
			THROW "profile_now_active_use_active_helper";
		};
		LET $new_version = $cur.version + 1;
		UPDATE type::thing('ca_llm_profile', $profile_rid) SET api_key = '';
		DELETE type::thing('ca_llm_profile', $profile_rid);
		IF $active_id != "" {
			UPDATE type::thing('ca_llm_profile', string::split($active_id, ':')[1]) SET
				last_legacy_version_consumed = $new_version,
				updated_at = type::datetime($now);
		};
		UPDATE ca_llm_config:default SET version = $new_version, updated_at = type::datetime($now);
		COMMIT;
	`
	vars := map[string]any{
		"observed_version": observedVersion,
		"profile_id":       profileID,
		"profile_rid":      recordID,
		"now":              time.Now().UTC().Format(time.RFC3339Nano),
	}
	return runBatch(ctx, surrealDB, sql, vars)
}

// ─────────────────────────────────────────────────────────────────────────
// reconcileLegacyToActive (codex-H2 / r1c)
// ─────────────────────────────────────────────────────────────────────────

// ReconcileResult is returned by reconcileLegacyToActive so the
// resolver can log "actually wrote" vs "skipped (raced)" cleanly.
type ReconcileResult struct {
	ActuallyWrote bool
	NewWatermark  uint64
}

// ─────────────────────────────────────────────────────────────────────────
// Workspace-version bumps after operations that don't naturally bump it
// ─────────────────────────────────────────────────────────────────────────

// BumpVersionAfterCreate is called after lps.CreateProfile to bump the
// workspace.version cell and advance the active profile's watermark
// (so the resolver doesn't false-positive a reconciliation on the next
// read because we created a non-active profile).
//
// CAS-guarded BEGIN/COMMIT; bounded retry on ErrVersionConflict.
//
// When no active profile exists yet (truly pre-migration window),
// this only bumps the workspace version cell — there's no active
// profile watermark to advance.
func BumpVersionAfterCreate(ctx context.Context, surrealDB *SurrealDB, lcs *SurrealLLMConfigStore) (uint64, error) {
	if surrealDB == nil || lcs == nil {
		return 0, fmt.Errorf("bump version: stores not configured")
	}
	for attempt := 0; attempt < helperRetryCap; attempt++ {
		_, observedVersion, err := lcs.LoadActiveProfileIDAndVersion(ctx)
		if err != nil {
			return 0, err
		}
		err = bumpWorkspaceAndActiveWatermark(ctx, surrealDB, observedVersion)
		if err != nil {
			if errors.Is(err, ErrVersionConflict) {
				continue
			}
			return 0, err
		}
		return observedVersion + 1, nil
	}
	return 0, ErrVersionConflict
}

// bumpWorkspaceAndActiveWatermark runs the CAS-guarded version-bump
// + active-profile watermark advance batch.
func bumpWorkspaceAndActiveWatermark(ctx context.Context, surrealDB *SurrealDB, observedVersion uint64) error {
	sql := `
		BEGIN;
		LET $cur = (SELECT version, active_profile_id FROM ca_llm_config:default)[0];
		IF $cur.version != $observed_version {
			THROW "ca_llm_config_version_changed";
		};
		LET $active_id = $cur.active_profile_id;
		LET $new_version = $cur.version + 1;
		IF $active_id != "" {
			UPDATE type::thing('ca_llm_profile', string::split($active_id, ':')[1]) SET
				last_legacy_version_consumed = $new_version,
				updated_at = type::datetime($now);
		};
		UPDATE ca_llm_config:default SET version = $new_version, updated_at = type::datetime($now);
		COMMIT;
	`
	vars := map[string]any{
		"observed_version": observedVersion,
		"now":              time.Now().UTC().Format(time.RFC3339Nano),
	}
	return runBatch(ctx, surrealDB, sql, vars)
}

// ─────────────────────────────────────────────────────────────────────────
// Retry loops (bounded; cap=3) around the CAS-guarded helpers
// ─────────────────────────────────────────────────────────────────────────

// helperRetryCap caps the number of times the retrying wrappers below
// will re-read snapshot + retry on ErrVersionConflict before bubbling
// it up to the caller (typically mapped to 409 at the API).
const helperRetryCap = 3

// WriteActiveProfilePatchWithRetry resolves the currently-active
// profile via lcs.LoadActiveProfileIDAndVersion, applies the patch via
// writeActiveProfileWithLegacyMirror, and retries on ErrVersionConflict
// up to helperRetryCap. On retry, the active id is re-checked: if it
// differs from the original observation, returns ErrTargetNoLongerActive
// (codex-r1e Low; the API maps this to 409 target_no_longer_active).
//
// patch.APIKey must be the SEALED form (post-cipher.Encrypt). The
// caller is responsible for cipher.Encrypt; we don't pass a cipher
// down because some tests want to inspect the on-disk bytes directly.
//
// On success, returns the post-bump workspace.version.
func WriteActiveProfilePatchWithRetry(
	ctx context.Context,
	surrealDB *SurrealDB,
	lcs *SurrealLLMConfigStore,
	patch ProfilePatch,
) (uint64, error) {
	if surrealDB == nil || lcs == nil {
		return 0, fmt.Errorf("write active profile: stores not configured")
	}
	originalActiveID, _, err := lcs.LoadActiveProfileIDAndVersion(ctx)
	if err != nil {
		return 0, err
	}
	if originalActiveID == "" {
		return 0, fmt.Errorf("write active profile: no active profile yet (boot race or pre-migration)")
	}

	for attempt := 0; attempt < helperRetryCap; attempt++ {
		activeID, observedVersion, err := lcs.LoadActiveProfileIDAndVersion(ctx)
		if err != nil {
			return 0, err
		}
		if activeID != originalActiveID {
			return 0, ErrTargetNoLongerActive
		}
		if err := writeActiveProfileWithLegacyMirror(ctx, surrealDB, observedVersion, activeID, patch); err != nil {
			if errors.Is(err, ErrVersionConflict) {
				continue
			}
			return 0, err
		}
		return observedVersion + 1, nil
	}
	return 0, ErrVersionConflict
}

// WriteNonActivePatchWithRetry applies a patch to a non-active profile
// via writeNonActiveProfileWithWatermarkBump, with bounded retry on
// ErrVersionConflict. If the target profile has been promoted to
// active during the retry window, returns ErrTargetNoLongerActive
// (the caller switches strategies).
func WriteNonActivePatchWithRetry(
	ctx context.Context,
	surrealDB *SurrealDB,
	lcs *SurrealLLMConfigStore,
	profileID string,
	patch ProfilePatch,
) (uint64, error) {
	if surrealDB == nil || lcs == nil {
		return 0, fmt.Errorf("write non-active profile: stores not configured")
	}
	for attempt := 0; attempt < helperRetryCap; attempt++ {
		_, observedVersion, err := lcs.LoadActiveProfileIDAndVersion(ctx)
		if err != nil {
			return 0, err
		}
		if err := writeNonActiveProfileWithWatermarkBump(ctx, surrealDB, observedVersion, profileID, patch); err != nil {
			if errors.Is(err, ErrVersionConflict) {
				continue
			}
			return 0, err
		}
		return observedVersion + 1, nil
	}
	return 0, ErrVersionConflict
}

// ActivateProfileWithRetry flips active_profile_id, retrying on
// ErrVersionConflict.
func ActivateProfileWithRetry(
	ctx context.Context,
	surrealDB *SurrealDB,
	lcs *SurrealLLMConfigStore,
	newActiveID string,
) (uint64, error) {
	if surrealDB == nil || lcs == nil {
		return 0, fmt.Errorf("activate profile: stores not configured")
	}
	for attempt := 0; attempt < helperRetryCap; attempt++ {
		_, observedVersion, err := lcs.LoadActiveProfileIDAndVersion(ctx)
		if err != nil {
			return 0, err
		}
		if err := activateProfileWithLegacyMirror(ctx, surrealDB, observedVersion, newActiveID); err != nil {
			if errors.Is(err, ErrVersionConflict) {
				continue
			}
			return 0, err
		}
		return observedVersion + 1, nil
	}
	return 0, ErrVersionConflict
}

// DeleteNonActiveWithRetry deletes a non-active profile, retrying on
// ErrVersionConflict.
func DeleteNonActiveWithRetry(
	ctx context.Context,
	surrealDB *SurrealDB,
	lcs *SurrealLLMConfigStore,
	profileID string,
) (uint64, error) {
	if surrealDB == nil || lcs == nil {
		return 0, fmt.Errorf("delete non-active profile: stores not configured")
	}
	for attempt := 0; attempt < helperRetryCap; attempt++ {
		_, observedVersion, err := lcs.LoadActiveProfileIDAndVersion(ctx)
		if err != nil {
			return 0, err
		}
		if err := deleteNonActiveProfile(ctx, surrealDB, observedVersion, profileID); err != nil {
			if errors.Is(err, ErrVersionConflict) {
				continue
			}
			return 0, err
		}
		return observedVersion + 1, nil
	}
	return 0, ErrVersionConflict
}

// ReconcileLegacyToActiveExported is the cli-package-facing wrapper for
// reconcileLegacyToActive. The cli/serve.go layer wires this into the
// resolution.ProfileAwareReconciler interface.
//
// The underlying helper is unexported because it is part of the
// migration/helper-private surface; the exported wrapper documents
// that it's the cross-package boundary.
func ReconcileLegacyToActiveExported(
	ctx context.Context,
	surrealDB *SurrealDB,
	observedVersion uint64,
	observedWatermark uint64,
	activeID string,
) (ReconcileResult, error) {
	return reconcileLegacyToActive(ctx, surrealDB, observedVersion, observedWatermark, activeID)
}

// reconcileLegacyToActive copies the legacy row's CURRENT contents
// (which include any old-pod writes since the last new-code update)
// onto the active profile, advances the watermark, and bumps
// workspace.version. CAS guards on BOTH workspace.version AND
// active_profile.last_legacy_version_consumed (codex-r1c).
//
// On any CAS failure: returns ErrVersionConflict or ErrWatermarkConflict.
// The resolver treats both as "snapshot stale, skip this reconcile."
func reconcileLegacyToActive(
	ctx context.Context,
	surrealDB *SurrealDB,
	observedVersion uint64,
	observedWatermark uint64,
	activeID string,
) (ReconcileResult, error) {
	tableName, recordID, ok := splitRecordID(activeID)
	if !ok || tableName != "ca_llm_profile" {
		return ReconcileResult{}, ErrProfileNotFound
	}
	sql := `
		BEGIN;
		LET $cur = (SELECT version, provider, base_url, api_key, summary_model, review_model, ask_model, knowledge_model, architecture_diagram_model, report_model, draft_model, timeout_secs, advanced_mode FROM ca_llm_config:default)[0];
		LET $prof = (SELECT last_legacy_version_consumed FROM ca_llm_profile WHERE id = type::thing('ca_llm_profile', $active_rid) LIMIT 1)[0];
		IF $cur.version != $observed_version {
			THROW "ca_llm_config_version_changed_during_reconcile";
		};
		IF $prof.last_legacy_version_consumed != $observed_watermark {
			THROW "active_profile_watermark_changed_during_reconcile";
		};
		LET $new_version = $cur.version + 1;
		UPDATE type::thing('ca_llm_profile', $active_rid) SET
			provider                     = $cur.provider,
			base_url                     = $cur.base_url,
			api_key                      = $cur.api_key,
			summary_model                = $cur.summary_model,
			review_model                 = $cur.review_model,
			ask_model                    = $cur.ask_model,
			knowledge_model              = $cur.knowledge_model,
			architecture_diagram_model   = $cur.architecture_diagram_model,
			report_model                 = $cur.report_model,
			draft_model                  = $cur.draft_model,
			timeout_secs                 = $cur.timeout_secs,
			advanced_mode                = $cur.advanced_mode,
			updated_at                   = type::datetime($now),
			last_legacy_version_consumed = $new_version;
		UPDATE ca_llm_config:default SET version = $new_version, updated_at = type::datetime($now);
		COMMIT;
	`
	vars := map[string]any{
		"observed_version":   observedVersion,
		"observed_watermark": observedWatermark,
		"active_rid":         recordID,
		"now":                time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := runBatch(ctx, surrealDB, sql, vars); err != nil {
		return ReconcileResult{}, err
	}
	return ReconcileResult{
		ActuallyWrote: true,
		NewWatermark:  observedVersion + 1,
	}, nil
}
