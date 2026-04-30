// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package resolution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/config"
)

// Snapshot is the fully-resolved LLM configuration for a single Resolve
// call. Sources is populated for every field with the layer that supplied
// the final value (see Source constants).
//
// IMPORTANT: never log Snapshot.APIKey. The structured log helper in this
// package (LogResolved) emits api_key_set:bool only.
type Snapshot struct {
	Provider    string
	BaseURL     string
	APIKey      string
	Model       string
	DraftModel  string
	TimeoutSecs int

	// OperationGroup is the resolver-internal op classification used when
	// picking a per-op model from the workspace record (analysis / review /
	// discussion / knowledge / report). Derived from the op string passed
	// to Resolve.
	OperationGroup string

	// Sources maps field name → the layer that supplied that field's value.
	Sources map[string]Source

	// Stale is true when the resolver could not reach the DB on this
	// Resolve and returned a cached Snapshot instead. The api-key transport
	// is unchanged; the caller should still pass the snapshot's values to
	// the worker, but operators may want to surface the staleness to
	// admins. Distinct from Sources so log/test plumbing stays clean.
	Stale bool

	// StaleFields, when Stale=true, marks per-field which values came from
	// the now-unreachable workspace layer. Empty when Stale=false.
	StaleFields map[string]bool

	// Version is the workspace DB record's version stamp at resolve time.
	// Zero when the snapshot's provider/api-key did not come from the DB
	// (e.g. env-only or builtin path). Used by capability caches to
	// invalidate themselves on workspace save.
	Version uint64

	// ActiveProfileID is the workspace's active profile id at resolve
	// time, threaded through to LogResolved for operator grep. Empty
	// when the workspace layer is empty (env-only / builtin) OR when
	// the dual-read legacy fallback is serving (truly pre-migration).
	ActiveProfileID string
}

// LLMConfigStore is the narrow interface the resolver needs from the
// workspace-settings persistence layer. The full persistence struct is
// db.SurrealLLMConfigStore; we keep the resolver's view minimal so tests
// can fake it without dragging in SurrealDB.
type LLMConfigStore interface {
	LoadLLMConfig() (*WorkspaceRecord, error)
	LoadLLMConfigVersion() (uint64, error)
}

// ProfileLookupStore is the narrow interface the per-repo override
// path uses to fetch a specific profile by id (slice 3). Slice 1 wires
// the adapter that implements this in addition to LLMConfigStore so
// the type surface is in place from day one.
//
// LoadProfileForResolution returns a *WorkspaceRecord (with api_key
// decrypted) for an arbitrary profile id, not necessarily the active
// one. Returns ErrProfileNotFound for unknown id.
//
// LoadAllProfileIDs is a diagnostics affordance; not on the hot
// resolver path.
type ProfileLookupStore interface {
	LoadProfileForResolution(ctx context.Context, profileID string) (*WorkspaceRecord, error)
	LoadAllProfileIDs(ctx context.Context) ([]string, error)
}

// ErrProfileNotFound is the resolver-package mirror of
// db.ErrProfileNotFound. The adapter translates the db sentinel to
// this one so the resolution package doesn't import internal/db.
var ErrProfileNotFound = errors.New("llm profile not found")

// ErrActiveProfileMissing is the sentinel returned by the adapter
// when active_profile_id points at a deleted/missing profile row
// (codex-H3). The resolver translates this to an "empty workspace
// overlay" + a banner-driving accessor on the adapter; it is NOT
// silently fallen-back to legacy fields (that would resurrect stale
// credentials and mask DB damage).
var ErrActiveProfileMissing = errors.New("active_profile_id points at a missing profile row; admin repair required")

// WorkspaceRecord is the resolver-internal view of the saved workspace
// LLM config. It mirrors db.LLMConfigRecord but lives in this package so
// the resolver does not import internal/db (which would create a cycle).
type WorkspaceRecord struct {
	Provider                 string
	BaseURL                  string
	APIKey                   string // already-decrypted value
	SummaryModel             string
	ReviewModel              string
	AskModel                 string
	KnowledgeModel           string
	ArchitectureDiagramModel string
	ReportModel              string
	DraftModel               string
	TimeoutSecs              int
	AdvancedMode             bool
	Version                  uint64

	// ProfileID is the active profile's record id at the time the
	// record was loaded. Empty when the dual-read fallback returned a
	// legacy-overlay record (truly pre-migration). Used purely for the
	// LogResolved structured log line; not part of any decision logic.
	ProfileID string

	// UpdatedAt is the profile's `updated_at` (or for legacy-fallback
	// overlays, ca_llm_config:default.updated_at). Observability only.
	UpdatedAt time.Time

	// LastLegacyVersionConsumed is the profile's reconciliation
	// watermark (codex-H2 / r1b). Compared against `workspace.version`
	// in the resolver's adapter to detect old-pod legacy writes.
	// Empty/zero in legacy-fallback overlays.
	LastLegacyVersionConsumed uint64
}

// Operation group strings used internally by the resolver. These map ops
// (the public Op* constants) to coarser groups so a single per-area model
// field can apply to multiple ops (e.g., OpDiscussion + OpQASynth both
// resolve to the workspace AskModel). Centralized here so callers and
// tests reference the same string set.
const (
	GroupAnalysis             = "analysis"
	GroupReview               = "review"
	GroupDiscussion           = "discussion"
	GroupKnowledge            = "knowledge"
	GroupArchitectureDiagram  = "architecture_diagram"
	GroupReport               = "report"
)

// RepoOverrideStore is the narrow interface the resolver needs to fetch
// per-repo overrides. Returns nil when no override is set. The store is
// responsible for decryption; the returned APIKey is plaintext (or empty).
//
// History: this method was named LoadLivingWikiLLMOverride in the parent
// delivery (slice 5), back when the override was scoped to living-wiki
// ops only. R2 widens the override to apply to every repo-scoped op, so
// the method is renamed to LoadLLMOverride to match. The on-disk storage
// column (lw_repo_settings.living_wiki_llm_override) keeps its legacy
// name for backward compatibility.
type RepoOverrideStore interface {
	LoadLLMOverride(ctx context.Context, repoID string) (*RepoOverride, error)
}

// RepoOverride is the resolver-internal view of a per-repo LLM override.
// Mirrors the workspace advanced-mode area list exactly so the per-repo
// surface and workspace surface stay aligned. Empty fields fall through
// to the workspace layer.
//
// Slice 3 of the LLM provider profiles plan adds ProfileID. When set,
// the resolver fetches that profile via ProfileLookupStore and overlays
// its fields with source label SourceRepoOverrideProfile. Inline fields
// below are ignored when ProfileID is non-empty; the GraphQL mutation
// enforces mutual exclusion at write time.
type RepoOverride struct {
	// ProfileID points at a saved profile. Non-empty means the resolver
	// uses that profile's values as the per-repo overlay (slice 3 of
	// the LLM provider profiles plan). Empty means "inline override"
	// — the rest of the fields apply.
	ProfileID string

	Provider string
	BaseURL  string
	APIKey   string // already-decrypted

	// AdvancedMode mirrors the workspace flag. When false, the resolver
	// uses SummaryModel for every group. When true, per-area fields
	// override per group with SummaryModel as a fallback.
	AdvancedMode bool

	// Per-area model fields. The resolver picks the right one for the
	// op being resolved via overrideModelForOp; missing fields fall
	// through to workspace via the Sources stamp logic in applyRepoOverride.
	SummaryModel             string
	ReviewModel              string
	AskModel                 string
	KnowledgeModel           string
	ArchitectureDiagramModel string
	ReportModel              string

	// DraftModel is overlaid separately (it is not selected by op group;
	// it accompanies the main model for speculative decoding, mirroring
	// how applyEnvBoot and the workspace handle DraftModel).
	DraftModel string
}

// Resolver returns a Snapshot for a given (repoID, op) pair. The default
// implementation is *DefaultResolver; tests can substitute their own.
type Resolver interface {
	Resolve(ctx context.Context, repoID, op string) (Snapshot, error)
	// InvalidateLocal nudges the local cache to drop its workspace
	// snapshot, forcing the next Resolve to refetch even if the version
	// stamp hasn't changed yet (e.g. immediately after a save on this
	// replica). Cross-replica freshness still relies on the version stamp
	// query in Resolve.
	InvalidateLocal()
}

// DefaultResolver implements Resolver against a workspace store, an optional
// per-repo override store, and an env-bootstrap snapshot taken from cfg.LLM.
type DefaultResolver struct {
	store        LLMConfigStore
	repoStore    RepoOverrideStore
	profileStore ProfileLookupStore // slice 3: nil-safe; only used when override.ProfileID != ""
	envBoot      config.LLMConfig
	log          *slog.Logger

	mu       sync.Mutex
	cache    *WorkspaceRecord // last successful fetch
	cacheVer uint64           // version of the cached record
}

// New constructs a DefaultResolver. The envBoot config is the env-var
// bootstrap layer; it is captured by value so subsequent mutations on the
// caller's *config.Config do not affect resolution. Callers should never
// mutate cfg.LLM after constructing the resolver.
//
// repoStore may be nil (slice 1 ships without per-repo overrides; slice 5
// fills this in). store may be nil only when running in embedded/in-memory
// mode where no DB is available; in that case Resolve always falls through
// to the env-bootstrap layer.
//
// Backwards-compat: callers that don't yet wire the slice-3
// ProfileLookupStore should use NewWithProfileLookup with profileStore=nil
// (or just keep calling New, which defaults profileStore to nil). When
// profileStore is nil, per-repo overrides that carry a non-empty
// ProfileID degrade gracefully: the resolver logs a Warn and treats the
// override as a no-op for that resolve so we never silently leak the
// workspace api_key for a repo that intended a different profile.
func New(store LLMConfigStore, repoStore RepoOverrideStore, envBoot config.LLMConfig, log *slog.Logger) *DefaultResolver {
	return NewWithProfileLookup(store, repoStore, nil, envBoot, log)
}

// NewWithProfileLookup is the slice-3 constructor that accepts the
// ProfileLookupStore needed to resolve per-repo "use a saved profile"
// overrides. The profile lookup is invoked only when an override row's
// ProfileID is non-empty; otherwise the inline-override path runs
// exactly as before.
func NewWithProfileLookup(store LLMConfigStore, repoStore RepoOverrideStore, profileStore ProfileLookupStore, envBoot config.LLMConfig, log *slog.Logger) *DefaultResolver {
	if log == nil {
		log = slog.Default()
	}
	return &DefaultResolver{
		store:        store,
		repoStore:    repoStore,
		profileStore: profileStore,
		envBoot:      envBoot,
		log:          log,
	}
}

// Resolve produces the Snapshot for (repoID, op). The op must be a value
// in KnownOps; an unknown op returns an error so typos surface at test
// time rather than silently routing through the wrong defaults.
func (r *DefaultResolver) Resolve(ctx context.Context, repoID, op string) (Snapshot, error) {
	if _, ok := KnownOps[op]; !ok {
		return Snapshot{}, fmt.Errorf("resolution: unknown op %q (add to KnownOps in internal/llm/resolution/ops.go)", op)
	}

	snap := Snapshot{
		OperationGroup: deriveOperationGroup(op),
		Sources:        make(map[string]Source, 6),
	}

	// Layer 4 (lowest priority): builtin defaults. Filled first so any
	// higher layer just overwrites.
	applyBuiltin(&snap)

	// Layer 3: env-bootstrap (cfg.LLM populated from env at boot).
	applyEnvBoot(&snap, r.envBoot)

	// Layer 2: workspace DB via version-keyed cache.
	r.applyWorkspace(ctx, &snap)

	// Layer 1 (highest priority): per-repo override, only for living-wiki ops.
	r.applyRepoOverride(ctx, &snap, repoID, op)

	return snap, nil
}

// InvalidateLocal drops the cached workspace snapshot. Called by the admin
// PUT handler after a save on the same replica so the next Resolve fetches
// fresh values without waiting for the version-stamp drift.
func (r *DefaultResolver) InvalidateLocal() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache = nil
	r.cacheVer = 0
}

// deriveOperationGroup maps the fine-grained op constants to the coarser
// per-op model groups used by config.LLMConfig.ModelForOperation and the
// resolver's own workspaceModelForOp / overrideModelForOp.
func deriveOperationGroup(op string) string {
	switch op {
	case OpReview:
		return GroupReview
	case OpDiscussion, OpDiscussStream, OpMCPDiscussStream, OpQASynth, OpQADeepSynth, OpQAAgentTurn, OpMCPExplain:
		return GroupDiscussion
	case OpKnowledge, OpLivingWikiColdStart, OpLivingWikiRegen, OpLivingWikiAssembly:
		return GroupKnowledge
	case OpArchitectureDiagram:
		return GroupArchitectureDiagram
	case OpReportGenerate:
		return GroupReport
	case OpAnalysis, OpClusteringRelabel, OpQAClassify, OpQADecompose, OpRequirementsEnrich, OpRequirementsExtract, OpProviderCapabilities, OpModelsList:
		return GroupAnalysis
	default:
		return GroupAnalysis
	}
}

func applyBuiltin(snap *Snapshot) {
	// Provider/model defaults match config.Defaults().
	snap.Provider = "anthropic"
	snap.Model = "claude-sonnet-4-20250514"
	snap.TimeoutSecs = 900
	snap.Sources[FieldProvider] = SourceBuiltin
	snap.Sources[FieldBaseURL] = SourceBuiltin
	snap.Sources[FieldAPIKey] = SourceBuiltin
	snap.Sources[FieldModel] = SourceBuiltin
	snap.Sources[FieldDraftModel] = SourceBuiltin
	snap.Sources[FieldTimeoutSecs] = SourceBuiltin
}

func applyEnvBoot(snap *Snapshot, env config.LLMConfig) {
	if env.Provider != "" {
		snap.Provider = env.Provider
		snap.Sources[FieldProvider] = SourceEnvFallback
	}
	if env.BaseURL != "" {
		snap.BaseURL = env.BaseURL
		snap.Sources[FieldBaseURL] = SourceEnvFallback
	}
	if env.APIKey != "" {
		snap.APIKey = env.APIKey
		snap.Sources[FieldAPIKey] = SourceEnvFallback
	}
	if model := env.ModelForOperation(snap.OperationGroup); model != "" {
		snap.Model = model
		snap.Sources[FieldModel] = SourceEnvFallback
	}
	if env.DraftModel != "" {
		snap.DraftModel = env.DraftModel
		snap.Sources[FieldDraftModel] = SourceEnvFallback
	}
	if env.TimeoutSecs > 0 {
		snap.TimeoutSecs = env.TimeoutSecs
		snap.Sources[FieldTimeoutSecs] = SourceEnvFallback
	}
}

// applyWorkspace fetches the workspace record (cache-aware) and overlays
// any non-empty fields onto snap. On DB outage we fall back to the
// previously cached snapshot and stamp Stale=true.
func (r *DefaultResolver) applyWorkspace(ctx context.Context, snap *Snapshot) {
	if r.store == nil {
		return
	}

	// Cheap version check first. If the version cell is unreachable or
	// returns an empty/zero result we treat the workspace layer as
	// unavailable for THIS Resolve, but we still serve cached values.
	currentVer, verErr := r.store.LoadLLMConfigVersion()

	r.mu.Lock()
	cachedRec := r.cache
	cachedVer := r.cacheVer
	r.mu.Unlock()

	if verErr != nil {
		// DB outage path: serve the last known snapshot if we have one.
		if cachedRec != nil {
			r.log.Warn("llm resolver: workspace version probe failed; serving cached snapshot",
				"error", verErr,
				"cached_version", cachedVer)
			r.overlayWorkspaceFromRecord(snap, cachedRec, true /*stale*/)
			snap.Stale = true
		} else {
			r.log.Warn("llm resolver: workspace unreachable and no cached snapshot; falling through to env",
				"error", verErr)
		}
		return
	}

	// No row yet → workspace layer is empty (env+builtin only).
	if currentVer == 0 {
		return
	}

	// Cache hit: reuse the cached record without a re-fetch.
	if cachedRec != nil && cachedVer == currentVer {
		r.overlayWorkspaceFromRecord(snap, cachedRec, false)
		return
	}

	// Cache miss: full fetch.
	rec, loadErr := r.store.LoadLLMConfig()
	if loadErr != nil {
		// DB hiccup mid-fetch — same outage handling as above.
		if cachedRec != nil {
			r.log.Warn("llm resolver: workspace fetch failed; serving cached snapshot",
				"error", loadErr)
			r.overlayWorkspaceFromRecord(snap, cachedRec, true)
			snap.Stale = true
		} else {
			r.log.Warn("llm resolver: workspace fetch failed; falling through to env",
				"error", loadErr)
		}
		return
	}
	if rec == nil {
		return
	}

	r.mu.Lock()
	r.cache = rec
	r.cacheVer = rec.Version
	r.mu.Unlock()

	r.overlayWorkspaceFromRecord(snap, rec, false)
}

// overlayWorkspaceFromRecord applies non-empty workspace fields onto snap,
// stamping each field with SourceWorkspace. When stale=true, also records
// per-field stale markers.
func (r *DefaultResolver) overlayWorkspaceFromRecord(snap *Snapshot, rec *WorkspaceRecord, stale bool) {
	mark := func(field string) {
		snap.Sources[field] = SourceWorkspace
		if stale {
			if snap.StaleFields == nil {
				snap.StaleFields = make(map[string]bool)
			}
			snap.StaleFields[field] = true
		}
	}

	if rec.Provider != "" {
		snap.Provider = rec.Provider
		mark(FieldProvider)
	}
	if rec.BaseURL != "" {
		snap.BaseURL = rec.BaseURL
		mark(FieldBaseURL)
	}
	if rec.APIKey != "" {
		snap.APIKey = rec.APIKey
		mark(FieldAPIKey)
	}
	if model := workspaceModelForOp(rec, snap.OperationGroup); model != "" {
		snap.Model = model
		mark(FieldModel)
	}
	if rec.DraftModel != "" {
		snap.DraftModel = rec.DraftModel
		mark(FieldDraftModel)
	}
	if rec.TimeoutSecs > 0 {
		snap.TimeoutSecs = rec.TimeoutSecs
		mark(FieldTimeoutSecs)
	}
	snap.Version = rec.Version
	// ActiveProfileID is informational (LogResolved field). Empty when
	// the workspace overlay came from the dual-read legacy fallback
	// (truly pre-migration), since there is no active profile in that
	// case.
	snap.ActiveProfileID = rec.ProfileID
}

// workspaceModelForOp picks the per-op model from a WorkspaceRecord.
// Mirrors config.LLMConfig.ModelForOperation but operates on the resolver's
// own struct shape. R2 added the architecture_diagram group; in advanced
// mode it returns ArchitectureDiagramModel, falling back to SummaryModel.
func workspaceModelForOp(rec *WorkspaceRecord, group string) string {
	if rec == nil {
		return ""
	}
	if !rec.AdvancedMode {
		return rec.SummaryModel
	}
	switch group {
	case GroupAnalysis:
		if rec.SummaryModel != "" {
			return rec.SummaryModel
		}
	case GroupReview:
		if rec.ReviewModel != "" {
			return rec.ReviewModel
		}
	case GroupDiscussion:
		if rec.AskModel != "" {
			return rec.AskModel
		}
	case GroupKnowledge:
		if rec.KnowledgeModel != "" {
			return rec.KnowledgeModel
		}
	case GroupArchitectureDiagram:
		if rec.ArchitectureDiagramModel != "" {
			return rec.ArchitectureDiagramModel
		}
	case GroupReport:
		if rec.ReportModel != "" {
			return rec.ReportModel
		}
	}
	return rec.SummaryModel
}

// overrideModelForOp picks the per-area model from a RepoOverride.
// Mirrors workspaceModelForOp exactly. When AdvancedMode is false,
// returns SummaryModel for every group (so a simple-mode override
// applies to every area). When true, returns the per-area field if
// non-empty, falling back to SummaryModel.
func overrideModelForOp(ov *RepoOverride, group string) string {
	if ov == nil {
		return ""
	}
	if !ov.AdvancedMode {
		return ov.SummaryModel
	}
	switch group {
	case GroupAnalysis:
		if ov.SummaryModel != "" {
			return ov.SummaryModel
		}
	case GroupReview:
		if ov.ReviewModel != "" {
			return ov.ReviewModel
		}
	case GroupDiscussion:
		if ov.AskModel != "" {
			return ov.AskModel
		}
	case GroupKnowledge:
		if ov.KnowledgeModel != "" {
			return ov.KnowledgeModel
		}
	case GroupArchitectureDiagram:
		if ov.ArchitectureDiagramModel != "" {
			return ov.ArchitectureDiagramModel
		}
	case GroupReport:
		if ov.ReportModel != "" {
			return ov.ReportModel
		}
	}
	return ov.SummaryModel
}

// applyRepoOverride applies a per-repo override on top of workspace.
//
// R2 widening: previously gated by IsLivingWikiOp. The override now
// applies to every repo-scoped op, mirroring the workspace area list.
// The per-area model is selected via overrideModelForOp (which respects
// the override's AdvancedMode flag in the same way the workspace does).
// DraftModel is overlaid separately because, like the workspace, it is
// not selected by op group — it accompanies the main model for
// speculative decoding.
//
// Slice 3 (LLM provider profiles): if the override carries a non-empty
// ProfileID, the resolver fetches that profile via ProfileLookupStore
// and overlays its values with source label SourceRepoOverrideProfile.
// Failures (profile deleted, store unreachable) degrade to "no override
// applied" with a Warn log — never silently leak workspace credentials
// for a repo that explicitly chose a different profile.
func (r *DefaultResolver) applyRepoOverride(ctx context.Context, snap *Snapshot, repoID, op string) {
	if r.repoStore == nil || repoID == "" {
		return
	}
	ov, err := r.repoStore.LoadLLMOverride(ctx, repoID)
	if err != nil {
		r.log.Warn("llm resolver: per-repo override fetch failed; falling back to workspace",
			"repo_id", repoID, "error", err)
		return
	}
	if ov == nil {
		return
	}
	// Slice 3: three-mode discrimination. The GraphQL mutation
	// enforces mutual exclusion at write time (saving with ProfileID
	// non-empty clears inline fields atomically; saving with
	// clearProfile=true clears ProfileID), so a well-behaved write
	// path produces rows where AT MOST one mode is populated. The
	// resolver is defensive against pathological rows where both are
	// set: per the slice-3 instruction, inline-mode wins on collision
	// because (a) it preserves today's behavior for any pre-slice-3
	// row that somehow grew a stray ProfileID, and (b) inline values
	// are concrete material the user explicitly typed, while a
	// ProfileID could be a stale leftover from a deleted profile.
	// This is a defense-in-depth choice — production rows should
	// never reach this branch.
	hasInline := ov.Provider != "" || ov.BaseURL != "" || ov.APIKey != "" ||
		ov.SummaryModel != "" || ov.ReviewModel != "" || ov.AskModel != "" ||
		ov.KnowledgeModel != "" || ov.ArchitectureDiagramModel != "" ||
		ov.ReportModel != "" || ov.DraftModel != ""
	if ov.ProfileID != "" && !hasInline {
		r.applyRepoOverrideFromProfile(ctx, snap, repoID, ov.ProfileID)
		return
	}
	r.applyRepoOverrideInline(snap, ov)
}

// applyRepoOverrideInline is the legacy (R2) per-field overlay. Lifted
// out so the profile-mode branch above can early-return without
// duplicating the inline path.
func (r *DefaultResolver) applyRepoOverrideInline(snap *Snapshot, ov *RepoOverride) {
	if ov.Provider != "" {
		snap.Provider = ov.Provider
		snap.Sources[FieldProvider] = SourceRepoOverride
	}
	if ov.BaseURL != "" {
		snap.BaseURL = ov.BaseURL
		snap.Sources[FieldBaseURL] = SourceRepoOverride
	}
	if ov.APIKey != "" {
		snap.APIKey = ov.APIKey
		snap.Sources[FieldAPIKey] = SourceRepoOverride
	}
	if model := overrideModelForOp(ov, snap.OperationGroup); model != "" {
		snap.Model = model
		snap.Sources[FieldModel] = SourceRepoOverride
	}
	if ov.DraftModel != "" {
		snap.DraftModel = ov.DraftModel
		snap.Sources[FieldDraftModel] = SourceRepoOverride
	}
}

// applyRepoOverrideFromProfile fetches the referenced profile and
// overlays its fields with source label SourceRepoOverrideProfile. The
// per-op model is picked from the profile's per-area fields the same
// way the workspace overlay picks from WorkspaceRecord (the profile
// schema mirrors the workspace's, by design).
//
// Failure modes (each treated as "no override applied" with a Warn):
//   - profileStore is nil → resolver wasn't wired for slice 3 yet.
//     This is a configuration bug; without the lookup we cannot
//     resolve the override. Falling through to workspace is the
//     safest behavior — never leak workspace credentials for a repo
//     that intended a different profile silently.
//   - ErrProfileNotFound → the referenced profile was deleted (the
//     activate-handler refuses to delete the active profile, but a
//     non-active profile referenced by a per-repo override CAN be
//     deleted; ruby-M2). The GraphQL field resolver surfaces this to
//     the UI as a typed error so the user can repair the override.
//   - other store errors → DB hiccup; same handling as the workspace
//     overlay's outage path (Warn + serve last-known workspace).
func (r *DefaultResolver) applyRepoOverrideFromProfile(ctx context.Context, snap *Snapshot, repoID, profileID string) {
	if r.profileStore == nil {
		r.log.Warn("llm resolver: per-repo override references a profile but ProfileLookupStore is not wired; falling back to workspace",
			"repo_id", repoID,
			"profile_id", profileID)
		return
	}
	rec, err := r.profileStore.LoadProfileForResolution(ctx, profileID)
	if err != nil {
		if errors.Is(err, ErrProfileNotFound) {
			r.log.Warn("llm resolver: per-repo override references a deleted profile; falling back to workspace",
				"repo_id", repoID,
				"profile_id", profileID)
			return
		}
		r.log.Warn("llm resolver: per-repo override profile fetch failed; falling back to workspace",
			"repo_id", repoID,
			"profile_id", profileID,
			"error", err)
		return
	}
	if rec == nil {
		// Defensive: well-behaved ProfileLookupStore returns
		// ErrProfileNotFound for missing rows, but a misbehaving
		// implementation could return (nil, nil). Treat as not-found.
		r.log.Warn("llm resolver: per-repo override profile fetch returned nil; falling back to workspace",
			"repo_id", repoID,
			"profile_id", profileID)
		return
	}

	// Overlay the profile's fields with source label
	// SourceRepoOverrideProfile so operators can grep for it. The
	// per-op model is picked the same way the workspace overlay picks
	// (workspaceModelForOp) — the profile mirrors the workspace
	// shape, so reusing that helper is correct.
	if rec.Provider != "" {
		snap.Provider = rec.Provider
		snap.Sources[FieldProvider] = SourceRepoOverrideProfile
	}
	if rec.BaseURL != "" {
		snap.BaseURL = rec.BaseURL
		snap.Sources[FieldBaseURL] = SourceRepoOverrideProfile
	}
	if rec.APIKey != "" {
		snap.APIKey = rec.APIKey
		snap.Sources[FieldAPIKey] = SourceRepoOverrideProfile
	}
	if model := workspaceModelForOp(rec, snap.OperationGroup); model != "" {
		snap.Model = model
		snap.Sources[FieldModel] = SourceRepoOverrideProfile
	}
	if rec.DraftModel != "" {
		snap.DraftModel = rec.DraftModel
		snap.Sources[FieldDraftModel] = SourceRepoOverrideProfile
	}
	if rec.TimeoutSecs > 0 {
		snap.TimeoutSecs = rec.TimeoutSecs
		snap.Sources[FieldTimeoutSecs] = SourceRepoOverrideProfile
	}
}
