// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package resolution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// ProfileAwareConfigStore is the adapter-facing interface implemented
// by the SurrealDB-backed config store (SurrealLLMConfigStore.LoadConfigSnapshot).
// Kept here so the resolution package does not import internal/db
// (which would create a cycle).
//
// The contract: return the workspace pointer + version + legacy fields
// in a single round trip. When the row does not exist (truly pre-
// migration), return a zero-value snapshot with no error.
type ProfileAwareConfigStore interface {
	LoadConfigSnapshot(ctx context.Context) (*ConfigSnapshot, error)
	LoadLLMConfigVersion() (uint64, error)
}

// ProfileAwareProfileStore is the adapter-facing interface implemented
// by the SurrealDB-backed profile store
// (SurrealLLMProfileStore.LoadProfileForResolution / LoadAllProfileIDs).
type ProfileAwareProfileStore interface {
	LoadProfileForResolution(ctx context.Context, profileID string) (*WorkspaceRecord, error)
	LoadAllProfileIDs(ctx context.Context) ([]string, error)
}

// ProfileAwareReconciler is the adapter-facing interface for the
// resolver-driven legacy → active-profile write-through (codex-H2 / r1c).
// The adapter does not invoke this directly when the dual-read
// fallback is disabled.
type ProfileAwareReconciler interface {
	ReconcileLegacyToActive(
		ctx context.Context,
		observedVersion uint64,
		observedWatermark uint64,
		activeID string,
	) (ReconcileResult, error)
}

// ReconcileResult mirrors db.ReconcileResult so the resolution package
// does not import internal/db. The adapter copies fields across.
type ReconcileResult struct {
	ActuallyWrote bool
	NewWatermark  uint64
}

// ConfigSnapshot mirrors db.LLMConfigSnapshot — kept here to avoid
// importing internal/db. The cli/serve.go wiring layer copies fields
// from the db type into this one.
type ConfigSnapshot struct {
	ActiveProfileID string
	Version         uint64
	UpdatedAt       time.Time

	// Legacy fields (kept on ca_llm_config:default for the rolling
	// deploy window per D8). Already-decrypted plaintext for api_key.
	LegacyProvider                 string
	LegacyBaseURL                  string
	LegacyAPIKey                   string
	LegacySummaryModel             string
	LegacyReviewModel              string
	LegacyAskModel                 string
	LegacyKnowledgeModel           string
	LegacyArchitectureDiagramModel string
	LegacyReportModel              string
	LegacyDraftModel               string
	LegacyTimeoutSecs              int
	LegacyAdvancedMode             bool
}

// ProfileAwareLLMResolverAdapter implements both LLMConfigStore (for
// the existing DefaultResolver) and ProfileLookupStore (for the
// per-repo override path in slice 3). It owns the dual-read fallback
// logic (D8), the reconciliation watermark scheme (codex-H2 / r1c),
// and the active-profile-missing banner state (codex-H3).
//
// Concurrency: the latched activeProfileMissing flag uses an atomic
// bool so concurrent Resolves can each update it without contention.
// The reconciliation path uses the underlying ReconcileLegacyToActive
// (which runs a CAS-guarded BEGIN/COMMIT batch); concurrent resolvers
// observing the same gap converge cleanly via the helper's CAS guards.
type ProfileAwareLLMResolverAdapter struct {
	configStore  ProfileAwareConfigStore
	profileStore ProfileAwareProfileStore
	reconciler   ProfileAwareReconciler

	// dualReadFallbackEnabled gates the transitional D8 fallback. While
	// true, the adapter returns the legacy overlay when active_profile_id
	// is empty. Flipped to false in the post-rollout cleanup follow-up.
	// Tests cover both states (dexter-M1).
	dualReadFallbackEnabled bool

	// activeProfileMissing latches when active_profile_id points at a
	// deleted profile (codex-H3). The admin REST handler reads it via
	// ActiveProfileMissing() to surface the repair banner. Reset on
	// the next successful resolve where the active profile is found.
	activeProfileMissing atomic.Bool

	log *slog.Logger

	// reconcileMu serializes legacy → active write-throughs from THIS
	// replica. The CAS-guarded BEGIN/COMMIT in the reconciler handles
	// cross-replica races; this mutex avoids burning multiple in-flight
	// reconciliation queries from a single replica when many concurrent
	// Resolves observe the same gap.
	reconcileMu sync.Mutex
}

// DualReadFallbackEnabled is the slice-1 default for the D8 transitional
// fallback. Flipped to false in the post-rollout cleanup follow-up
// after thor verification confirms no old pods remain (dexter-M1).
//
// Tests cover both true and false paths so the flip is mechanical.
const DualReadFallbackEnabled = true

// NewProfileAwareLLMResolverAdapter constructs the adapter. log may be
// nil (defaults to slog.Default()). reconciler may be nil to disable
// the resolver-driven write-through (e.g., test mode); the dual-read
// fallback path still serves legacy content correctly when reconciler
// is absent — it just doesn't re-anchor the watermark.
func NewProfileAwareLLMResolverAdapter(
	configStore ProfileAwareConfigStore,
	profileStore ProfileAwareProfileStore,
	reconciler ProfileAwareReconciler,
	log *slog.Logger,
) *ProfileAwareLLMResolverAdapter {
	if log == nil {
		log = slog.Default()
	}
	return &ProfileAwareLLMResolverAdapter{
		configStore:             configStore,
		profileStore:            profileStore,
		reconciler:              reconciler,
		dualReadFallbackEnabled: DualReadFallbackEnabled,
		log:                     log,
	}
}

// SetDualReadFallback toggles the D8 fallback. Reserved for tests
// (dexter-M1: tests cover both states).
func (a *ProfileAwareLLMResolverAdapter) SetDualReadFallback(enabled bool) {
	a.dualReadFallbackEnabled = enabled
}

// ActiveProfileMissing reports whether the most recent resolve found
// active_profile_id pointing at a deleted/missing profile. The admin
// REST handler consults this to render the "Active profile is missing"
// banner (UX §4.3 / codex-H3).
func (a *ProfileAwareLLMResolverAdapter) ActiveProfileMissing() bool {
	return a.activeProfileMissing.Load()
}

// LoadLLMConfig is the resolver's primary read path. Returns the
// resolved workspace record, performing the dual-read fallback when
// active_profile_id is empty (D8) and the version-watermark
// reconciliation when an old-pod legacy write is detected (codex-H2 / r1c).
//
// Implements resolution.LLMConfigStore.
func (a *ProfileAwareLLMResolverAdapter) LoadLLMConfig() (*WorkspaceRecord, error) {
	ctx := context.Background()

	snap, err := a.configStore.LoadConfigSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("profile-aware adapter: load snapshot: %w", err)
	}
	if snap == nil {
		// Treat nil as a zero-value snapshot.
		return nil, nil
	}

	// Pre-migration / fresh-install / post-cleanup case.
	if snap.ActiveProfileID == "" {
		a.activeProfileMissing.Store(false)
		if a.dualReadFallbackEnabled && snap.LegacyProvider != "" {
			return legacyOverlayFromSnap(snap), nil
		}
		return nil, nil
	}

	// Active profile is set — fetch it.
	profile, err := a.profileStore.LoadProfileForResolution(ctx, snap.ActiveProfileID)
	if err != nil {
		if errors.Is(err, ErrProfileNotFound) {
			// Pointer-to-deleted-profile: admin-repair-required.
			// Do NOT silently fall back to legacy fields — that would
			// resurrect stale credentials and mask DB damage (codex-H3).
			a.log.Error("active_profile_id points at a missing profile row; admin repair required",
				"active_profile_id", snap.ActiveProfileID)
			a.activeProfileMissing.Store(true)
			return nil, nil
		}
		return nil, fmt.Errorf("profile-aware adapter: load profile %s: %w", snap.ActiveProfileID, err)
	}
	a.activeProfileMissing.Store(false)

	// Rolling-deploy reconciliation (codex-H2 / r1c).
	if a.dualReadFallbackEnabled && snap.Version > profile.LastLegacyVersionConsumed {
		// Old-pod legacy write detected. Serve the legacy overlay for
		// THIS resolve (it's at least as fresh as the active profile)
		// and fire the reconciliation write-through for subsequent
		// resolvers across the cluster.
		if a.reconciler != nil {
			a.kickReconciliation(ctx, snap.Version, profile.LastLegacyVersionConsumed, snap.ActiveProfileID)
		}
		return legacyOverlayFromSnap(snap), nil
	}
	if snap.Version < profile.LastLegacyVersionConsumed {
		// Defensive: should be impossible by construction (watermark only
		// ever moves forward). Log and proceed with the active profile.
		a.log.Error("legacy_watermark_inversion (defensive log)",
			"active_profile_id", snap.ActiveProfileID,
			"workspace_version", snap.Version,
			"profile_watermark", profile.LastLegacyVersionConsumed)
	}

	profile.ProfileID = snap.ActiveProfileID
	if profile.Version == 0 {
		profile.Version = snap.Version
	}
	return profile, nil
}

// LoadLLMConfigVersion forwards to the underlying config store. The
// version cell remains on ca_llm_config:default; the resolver's
// version-keyed cache uses this for the cheap probe.
func (a *ProfileAwareLLMResolverAdapter) LoadLLMConfigVersion() (uint64, error) {
	return a.configStore.LoadLLMConfigVersion()
}

// LoadProfileForResolution implements ProfileLookupStore. Used by the
// per-repo override path in slice 3.
func (a *ProfileAwareLLMResolverAdapter) LoadProfileForResolution(ctx context.Context, profileID string) (*WorkspaceRecord, error) {
	return a.profileStore.LoadProfileForResolution(ctx, profileID)
}

// LoadAllProfileIDs implements ProfileLookupStore.
func (a *ProfileAwareLLMResolverAdapter) LoadAllProfileIDs(ctx context.Context) ([]string, error) {
	return a.profileStore.LoadAllProfileIDs(ctx)
}

// kickReconciliation runs the legacy → active write-through under the
// adapter's reconcileMu so multiple concurrent Resolves on the same
// replica issue at most one in-flight reconcile query. Cross-replica
// concurrency is handled by the CAS-guarded BEGIN/COMMIT inside
// reconcileLegacyToActive.
func (a *ProfileAwareLLMResolverAdapter) kickReconciliation(
	ctx context.Context,
	observedVersion, observedWatermark uint64,
	activeID string,
) {
	a.reconcileMu.Lock()
	defer a.reconcileMu.Unlock()

	result, err := a.reconciler.ReconcileLegacyToActive(ctx, observedVersion, observedWatermark, activeID)
	switch {
	case err == nil && result.ActuallyWrote:
		LogLegacyWriteReconciled(a.log, activeID, observedWatermark, result.NewWatermark)
	case errors.Is(err, ErrVersionConflict), errors.Is(err, ErrWatermarkConflict):
		// Another writer raced us. Don't write the stale snapshot.
		// Next resolve will pick up the new state and reconcile if
		// still needed.
		a.log.Debug("legacy_write_reconcile_skipped_stale_snapshot",
			"active_profile_id", activeID)
	case err != nil:
		// Unexpected error path. Log and let the next resolve retry.
		a.log.Warn("legacy_write_reconcile_failed",
			"err", err,
			"active_profile_id", activeID,
			"from_watermark", observedWatermark,
			"to_workspace_version", observedVersion)
	}
}

// ErrVersionConflict mirrors db.ErrVersionConflict — kept here so the
// adapter doesn't import internal/db. The cli wiring constructs the
// reconciler implementation that translates db sentinels into these.
var ErrVersionConflict = errors.New("ca_llm_config version changed since read; retry with fresh snapshot")

// ErrWatermarkConflict mirrors db.ErrWatermarkConflict.
var ErrWatermarkConflict = errors.New("active profile watermark changed since read; another reconciler raced")

// legacyOverlayFromSnap materializes a WorkspaceRecord from the
// snapshot's legacy fields. Used when the dual-read fallback is in
// effect (truly pre-migration, codex-H3) OR when an old-pod legacy
// write is detected and we serve the legacy overlay for THIS resolve
// (codex-H2).
//
// ProfileID is intentionally empty in this path — the resolved record
// did not come from a profile; LogResolved will show
// active_profile_id="" in this state, which is the correct signal to
// operators.
func legacyOverlayFromSnap(snap *ConfigSnapshot) *WorkspaceRecord {
	return &WorkspaceRecord{
		Provider:                 snap.LegacyProvider,
		BaseURL:                  snap.LegacyBaseURL,
		APIKey:                   snap.LegacyAPIKey,
		SummaryModel:             snap.LegacySummaryModel,
		ReviewModel:              snap.LegacyReviewModel,
		AskModel:                 snap.LegacyAskModel,
		KnowledgeModel:           snap.LegacyKnowledgeModel,
		ArchitectureDiagramModel: snap.LegacyArchitectureDiagramModel,
		ReportModel:              snap.LegacyReportModel,
		DraftModel:               snap.LegacyDraftModel,
		TimeoutSecs:              snap.LegacyTimeoutSecs,
		AdvancedMode:             snap.LegacyAdvancedMode,
		Version:                  snap.Version,
		ProfileID:                "",
		UpdatedAt:                snap.UpdatedAt,
		// LastLegacyVersionConsumed is irrelevant for legacy-overlay
		// records (there's no profile yet); leave zero.
		LastLegacyVersionConsumed: 0,
	}
}

// Compile-time interface checks: the adapter must implement both
// LLMConfigStore and ProfileLookupStore.
var (
	_ LLMConfigStore    = (*ProfileAwareLLMResolverAdapter)(nil)
	_ ProfileLookupStore = (*ProfileAwareLLMResolverAdapter)(nil)
)
