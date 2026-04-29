// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package resolution

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

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
}

// LLMConfigStore is the narrow interface the resolver needs from the
// workspace-settings persistence layer. The full persistence struct is
// db.SurrealLLMConfigStore; we keep the resolver's view minimal so tests
// can fake it without dragging in SurrealDB.
type LLMConfigStore interface {
	LoadLLMConfig() (*WorkspaceRecord, error)
	LoadLLMConfigVersion() (uint64, error)
}

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
}

// RepoOverrideStore is the narrow interface the resolver needs to fetch
// per-repo living-wiki overrides. Returns nil when no override is set.
// The store is responsible for decryption; the returned APIKey is
// plaintext (or empty).
type RepoOverrideStore interface {
	LoadLivingWikiLLMOverride(ctx context.Context, repoID string) (*RepoOverride, error)
}

// RepoOverride is the resolver-internal view of a per-repo living-wiki
// LLM override. Slice 5 introduces the persisted form; until then no
// caller registers a non-nil RepoOverrideStore and this stays unused.
type RepoOverride struct {
	Provider string
	BaseURL  string
	APIKey   string // already-decrypted
	Model    string
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
	store     LLMConfigStore
	repoStore RepoOverrideStore
	envBoot   config.LLMConfig
	log       *slog.Logger

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
func New(store LLMConfigStore, repoStore RepoOverrideStore, envBoot config.LLMConfig, log *slog.Logger) *DefaultResolver {
	if log == nil {
		log = slog.Default()
	}
	return &DefaultResolver{
		store:     store,
		repoStore: repoStore,
		envBoot:   envBoot,
		log:       log,
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
// per-op model groups used by config.LLMConfig.ModelForOperation.
func deriveOperationGroup(op string) string {
	switch op {
	case OpReview:
		return "review"
	case OpDiscussion, OpDiscussStream, OpMCPDiscussStream, OpQASynth, OpQADeepSynth, OpQAAgentTurn, OpMCPExplain:
		return "discussion"
	case OpKnowledge, OpLivingWikiColdStart, OpLivingWikiRegen, OpLivingWikiAssembly:
		return "knowledge"
	case OpReportGenerate:
		return "report"
	case OpAnalysis, OpClusteringRelabel, OpQAClassify, OpQADecompose, OpRequirementsEnrich, OpRequirementsExtract, OpProviderCapabilities, OpModelsList:
		return "analysis"
	default:
		return "analysis"
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
}

// workspaceModelForOp picks the per-op model from a WorkspaceRecord.
// Mirrors config.LLMConfig.ModelForOperation but operates on the resolver's
// own struct shape.
func workspaceModelForOp(rec *WorkspaceRecord, group string) string {
	if rec == nil {
		return ""
	}
	if !rec.AdvancedMode {
		return rec.SummaryModel
	}
	switch group {
	case "analysis":
		if rec.SummaryModel != "" {
			return rec.SummaryModel
		}
	case "review":
		if rec.ReviewModel != "" {
			return rec.ReviewModel
		}
	case "discussion":
		if rec.AskModel != "" {
			return rec.AskModel
		}
	case "knowledge":
		if rec.KnowledgeModel != "" {
			return rec.KnowledgeModel
		}
	case "report":
		if rec.ReportModel != "" {
			return rec.ReportModel
		}
	}
	return rec.SummaryModel
}

// applyRepoOverride applies a per-repo override on top of workspace, but
// ONLY for living_wiki.* ops. Other ops ignore the override even when set.
func (r *DefaultResolver) applyRepoOverride(ctx context.Context, snap *Snapshot, repoID, op string) {
	if r.repoStore == nil || repoID == "" {
		return
	}
	if !IsLivingWikiOp(op) {
		return
	}
	ov, err := r.repoStore.LoadLivingWikiLLMOverride(ctx, repoID)
	if err != nil {
		r.log.Warn("llm resolver: per-repo override fetch failed; falling back to workspace",
			"repo_id", repoID, "error", err)
		return
	}
	if ov == nil {
		return
	}
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
	if ov.Model != "" {
		snap.Model = ov.Model
		snap.Sources[FieldModel] = SourceRepoOverride
	}
}
