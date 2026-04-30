// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package livingwiki

import "time"

// LLMOverride is the per-repository LLM override. Mirrors the workspace
// /admin/llm advanced-mode area list (summary, review, ask, knowledge,
// architecture_diagram, report, draft) so the per-repo override surface
// stays aligned with the workspace surface by construction. When a new
// area is added to the workspace, it must be added here in the same
// change.
//
// Scope (R2 widening): unlike the parent delivery (which scoped the
// override to living-wiki ops only), R2 applies the override to every
// repo-scoped LLM op. The Go type was renamed from LivingWikiLLMOverride
// to LLMOverride to reflect that. The on-disk SurrealDB column keeps
// its legacy name (`lw_repo_settings.living_wiki_llm_override`) for
// backward compatibility — see CLAUDE.md legacy-name caveat. The
// nested legacy `model` JSON key is also preserved during a transition
// release; see internal/db/livingwiki_repo_settings_store.go.
//
// The api_key field holds plaintext at this layer; the SurrealDB store
// encrypts/decrypts via the same sbenc:v1 envelope used by
// ca_llm_config.api_key (see internal/db/llm_config_store.go).
type LLMOverride struct {
	// Provider, BaseURL are optional. Empty fields fall through to the
	// workspace layer in the resolver.
	Provider string `json:"provider,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`

	// APIKey is plaintext at the application layer. Empty means "use
	// the workspace api_key" — the resolver overlays this only when
	// the override sets a non-empty value.
	APIKey string `json:"api_key,omitempty"`

	// AdvancedMode mirrors the workspace flag. When false, SummaryModel
	// applies to every area (resolver picks SummaryModel for all groups).
	// When true, per-area model fields are honored with SummaryModel as
	// a fallback for unset areas.
	AdvancedMode bool `json:"advanced_mode,omitempty"`

	// Per-area model fields. Names match the workspace advanced-mode
	// fields exactly (see web/src/app/(app)/admin/llm/page.tsx).
	SummaryModel             string `json:"summary_model,omitempty"`
	ReviewModel              string `json:"review_model,omitempty"`
	AskModel                 string `json:"ask_model,omitempty"`
	KnowledgeModel           string `json:"knowledge_model,omitempty"`
	ArchitectureDiagramModel string `json:"architecture_diagram_model,omitempty"`
	ReportModel              string `json:"report_model,omitempty"`

	// DraftModel is overlaid separately by the resolver (it is not
	// selected by op group; it accompanies the main model for
	// speculative decoding). LM Studio / llama.cpp / SGLang.
	DraftModel string `json:"draft_model,omitempty"`

	UpdatedAt time.Time `json:"updated_at,omitempty"`
	UpdatedBy string    `json:"updated_by,omitempty"`
}

// IsEmpty reports whether the override has no fields set. Used by
// callers to decide between "leave the override untouched" and "this
// override is a no-op, treat as no override".
func (o *LLMOverride) IsEmpty() bool {
	if o == nil {
		return true
	}
	return o.Provider == "" &&
		o.BaseURL == "" &&
		o.APIKey == "" &&
		!o.AdvancedMode &&
		o.SummaryModel == "" &&
		o.ReviewModel == "" &&
		o.AskModel == "" &&
		o.KnowledgeModel == "" &&
		o.ArchitectureDiagramModel == "" &&
		o.ReportModel == "" &&
		o.DraftModel == ""
}

// RepositoryLivingWikiSettings is the per-repo living-wiki opt-in record.
// A repo without a row in this table is treated as disabled (nil return from
// GetRepoSettings means "not yet configured").
type RepositoryLivingWikiSettings struct {
	TenantID string `json:"tenant_id"`

	RepoID string `json:"repo_id"`

	// Enabled is the per-repo on/off. Requires the global Enabled flag also
	// to be true at runtime. Preserved on disable so re-enabling restores config.
	Enabled bool `json:"enabled"`

	// Mode is the publish mode for this repo.
	Mode RepoWikiMode `json:"mode"`

	// Sinks is the ordered list of configured sinks for this repo.
	// Audience and EditPolicy live on each sink (per-sink, not per-repo).
	Sinks []RepoWikiSink `json:"sinks"`

	// ExcludePaths is a list of glob patterns (relative to repo root) to
	// exclude from page generation. Empty means no exclusions.
	ExcludePaths []string `json:"exclude_paths,omitempty"`

	// StaleWhenStrategy controls how stale-detection walks dependencies.
	StaleWhenStrategy StaleStrategy `json:"stale_when_strategy"`

	// MaxPagesPerJob caps page generation per scheduler tick to prevent
	// runaway regen. Default 50.
	MaxPagesPerJob int `json:"max_pages_per_job,omitempty"`

	// LastRunAt is the timestamp of the most recent completed regen pass.
	LastRunAt *time.Time `json:"last_run_at,omitempty"`

	// DisabledAt is set when the user disables living-wiki for this repo.
	// Non-nil triggers the stale-banner insertion pass on next scheduler tick.
	DisabledAt *time.Time `json:"disabled_at,omitempty"`

	// AutoCleanOrphans controls whether the post-dispatch orphan-cleanup pass
	// runs after a successful job. Defaults to true (nil = not yet set →
	// treat as enabled). Set to false via the UI to keep manually-created or
	// previously-generated pages that are no longer in the current taxonomy.
	// Nil means the user has never changed the default.
	AutoCleanOrphans *bool `json:"auto_clean_orphans,omitempty"`

	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by,omitempty"`

	// LLMOverride lets the user override the workspace LLM settings for
	// this repository. Nil means "inherit workspace settings". A non-nil
	// override with empty fields is treated as nil (see IsEmpty); the
	// resolver only overlays non-empty fields.
	//
	// Scope (R2 widening): the resolver applies this override to every
	// repo-scoped LLM op (summary/review/discussion/knowledge/
	// architecture_diagram/report/...). Earlier (parent delivery), the
	// override was gated to living-wiki ops only. The struct field name
	// here is `LLMOverride` to reflect the wider scope. The on-disk
	// SurrealDB column name is `living_wiki_llm_override` and is
	// preserved as legacy for backward compatibility.
	LLMOverride *LLMOverride `json:"living_wiki_llm_override,omitempty"`

	// LivingWikiOverviewEnabled controls Overview-mode (subsystem-level)
	// generation. Default for new repos: true. Existing repos are backfilled
	// to true by migration 051 (J1 — Jay's locked decision).
	//
	// Phase 1 deliverable (CR12 Part A): the schema and Go field land here so
	// Phase 2's index renderer can read the flags without waiting for Phase 4b.
	// The GraphQL surface and UI toggles land in Phase 4b.
	LivingWikiOverviewEnabled bool `json:"living_wiki_overview_enabled"`

	// LivingWikiDetailedEnabled controls Detailed-mode (per-folder) generation.
	// Default for new repos: false. Existing repos are backfilled to true by
	// migration 051 to preserve today's per-folder pages (J1).
	LivingWikiDetailedEnabled bool `json:"living_wiki_detailed_enabled"`
}

// RepoWikiMode is the publish mode for a living-wiki repo.
type RepoWikiMode string

const (
	RepoWikiModePRReview      RepoWikiMode = "PR_REVIEW"
	RepoWikiModeDirectPublish RepoWikiMode = "DIRECT_PUBLISH"
)

// StaleStrategy controls how stale-detection walks the dependency graph.
type StaleStrategy string

const (
	StaleStrategyDirect     StaleStrategy = "DIRECT"
	StaleStrategyTransitive StaleStrategy = "TRANSITIVE"
)

// RepoWikiAudience identifies the target audience for pages generated to a sink.
type RepoWikiAudience string

const (
	RepoWikiAudienceEngineer RepoWikiAudience = "ENGINEER"
	RepoWikiAudienceProduct  RepoWikiAudience = "PRODUCT"
	RepoWikiAudienceOperator RepoWikiAudience = "OPERATOR"
)

// RepoWikiSinkKind classifies the type of a living-wiki sink.
type RepoWikiSinkKind string

const (
	RepoWikiSinkGitRepo           RepoWikiSinkKind = "GIT_REPO"
	RepoWikiSinkConfluence        RepoWikiSinkKind = "CONFLUENCE"
	RepoWikiSinkNotion            RepoWikiSinkKind = "NOTION"
	RepoWikiSinkGitHubWiki        RepoWikiSinkKind = "GITHUB_WIKI"
	RepoWikiSinkGitLabWiki        RepoWikiSinkKind = "GITLAB_WIKI"
	RepoWikiSinkBackstageTechDocs RepoWikiSinkKind = "BACKSTAGE_TECHDOCS"
	RepoWikiSinkMkDocs            RepoWikiSinkKind = "MKDOCS"
	RepoWikiSinkDocusaurus        RepoWikiSinkKind = "DOCUSAURUS"
	RepoWikiSinkVitePress         RepoWikiSinkKind = "VITEPRESS"
)

// RepoWikiEditPolicy controls how generated content is written to a sink.
type RepoWikiEditPolicy string

const (
	RepoWikiEditPolicyProposePR    RepoWikiEditPolicy = "PROPOSE_PR"
	RepoWikiEditPolicyDirectPublish RepoWikiEditPolicy = "DIRECT_PUBLISH"
)

// RepoWikiSink is the per-sink configuration attached to a repo's living-wiki settings.
type RepoWikiSink struct {
	// Kind is the sink type.
	Kind RepoWikiSinkKind `json:"kind"`

	// IntegrationName is a stable human-readable label for this sink instance.
	// Must be unique within the repo's sink list.
	IntegrationName string `json:"integration_name"`

	// Audience is the target audience for pages generated to this sink.
	Audience RepoWikiAudience `json:"audience"`

	// EditPolicy controls how out-of-band sink edits are handled.
	// When empty, DefaultRepoEditPolicy for the sink's Kind applies.
	EditPolicy RepoWikiEditPolicy `json:"edit_policy,omitempty"`
}

// AutoCleanOrphansDisabled reports whether the user has explicitly opted out of
// the orphan-cleanup pass. Returns false (meaning "cleanup is enabled") when
// AutoCleanOrphans is nil (the default) or true. Returns true only when the
// user has explicitly set the field to false.
func (s *RepositoryLivingWikiSettings) AutoCleanOrphansDisabled() bool {
	if s.AutoCleanOrphans == nil {
		return false // nil → default-on
	}
	return !*s.AutoCleanOrphans
}

// DefaultRepoEditPolicy returns the default edit policy for a given sink kind.
// "Proposal-first" sinks (git-native PR flow) default to PROPOSE_PR.
// "Direct-publish" sinks (no native PR concept) default to DIRECT_PUBLISH.
//
// | SinkKind              | Default EditPolicy |
// |-----------------------|--------------------|
// | GIT_REPO              | PROPOSE_PR         |
// | CONFLUENCE            | PROPOSE_PR         |
// | NOTION                | PROPOSE_PR         |
// | GITHUB_WIKI           | PROPOSE_PR         |
// | GITLAB_WIKI           | PROPOSE_PR         |
// | BACKSTAGE_TECHDOCS    | DIRECT_PUBLISH     |
// | MKDOCS                | DIRECT_PUBLISH     |
// | DOCUSAURUS            | DIRECT_PUBLISH     |
// | VITEPRESS             | DIRECT_PUBLISH     |
var DefaultRepoEditPolicy = map[RepoWikiSinkKind]RepoWikiEditPolicy{
	RepoWikiSinkGitRepo:           RepoWikiEditPolicyProposePR,
	RepoWikiSinkConfluence:        RepoWikiEditPolicyProposePR,
	RepoWikiSinkNotion:            RepoWikiEditPolicyProposePR,
	RepoWikiSinkGitHubWiki:        RepoWikiEditPolicyProposePR,
	RepoWikiSinkGitLabWiki:        RepoWikiEditPolicyProposePR,
	RepoWikiSinkBackstageTechDocs: RepoWikiEditPolicyDirectPublish,
	RepoWikiSinkMkDocs:            RepoWikiEditPolicyDirectPublish,
	RepoWikiSinkDocusaurus:        RepoWikiEditPolicyDirectPublish,
	RepoWikiSinkVitePress:         RepoWikiEditPolicyDirectPublish,
}

// EffectiveEditPolicy resolves the edit policy for a sink, applying the
// DefaultRepoEditPolicy table when the sink's EditPolicy field is empty.
func (s RepoWikiSink) EffectiveEditPolicy() RepoWikiEditPolicy {
	if s.EditPolicy != "" {
		return s.EditPolicy
	}
	if p, ok := DefaultRepoEditPolicy[s.Kind]; ok {
		return p
	}
	return RepoWikiEditPolicyProposePR
}

// SinkWriteResult records the per-sink push outcome for one job run.
// Persisted alongside LivingWikiJobResult so the UI can show per-sink counts.
type SinkWriteResult struct {
	// IntegrationName is the human-readable label for the sink instance.
	IntegrationName string `json:"integration_name"`
	// Kind is the sink type (e.g. "CONFLUENCE", "NOTION").
	Kind string `json:"kind"`
	// PagesWritten is the count of pages successfully pushed to this sink.
	PagesWritten int `json:"pages_written"`
	// PagesFailed is the count of pages whose write calls returned an error.
	PagesFailed int `json:"pages_failed"`
	// FailedPageIDs lists the IDs of pages that failed to write.
	FailedPageIDs []string `json:"failed_page_ids,omitempty"`
	// Error is non-empty when a non-recoverable error stopped this sink early
	// (e.g. authentication failure).
	Error string `json:"error,omitempty"`
}

// LivingWikiJobResult records the per-run outcome of one living-wiki job.
// Used by the UI's settings panel summary and the "Retry excluded pages" CTA.
type LivingWikiJobResult struct {
	// RepoID is the repository this result belongs to.
	RepoID              string     `json:"repo_id"`
	JobID               string     `json:"job_id"`
	StartedAt           time.Time  `json:"started_at"`
	CompletedAt         *time.Time `json:"completed_at,omitempty"`
	PagesPlanned        int        `json:"pages_planned"`
	PagesGenerated      int        `json:"pages_generated"`
	PagesExcluded       int        `json:"pages_excluded"`
	ExcludedPageIDs     []string   `json:"excluded_page_ids,omitempty"`
	GeneratedPageTitles []string   `json:"generated_page_titles,omitempty"`
	ExclusionReasons    []string   `json:"exclusion_reasons,omitempty"`
	// ExclusionFailureCategories is parallel to ExcludedPageIDs: for each
	// excluded page (in the same order), the per-page failure category from
	// the orchestrator's classifier. Values are one of:
	//   "deadline_exceeded", "provider_unavailable", "provider_compute",
	//   "llm_empty", "render_error", "template_internal", or "" for
	//   gate-failure exclusions.
	//
	// The UI renders a one-line failure-breakdown summary above the
	// per-page exclusions list using these category counts. Empty entries
	// (gate failures) are grouped under the "gate" bucket in the UI.
	//
	// Invariant: len(ExcludedPageIDs) == len(ExclusionFailureCategories)
	// after persistence. Both slices are derived from the orchestrator's
	// `result.Excluded` to avoid order/length divergence between live
	// progress counters and the persisted record.
	ExclusionFailureCategories []string `json:"exclusion_failure_categories,omitempty"`
	// SinkWriteResults records per-sink push counts for this job.
	// Populated after the dispatch phase completes.
	SinkWriteResults []SinkWriteResult `json:"sink_write_results,omitempty"`
	// Status is one of: "running", "ok", "partial", "failed".
	Status string `json:"status"`
	// FailureCategory classifies terminal failures into one of three buckets
	// that drive distinct CTAs in the UI (R5 taxonomy).
	// Values: "" (success/partial), "transient", "auth", "partial_content".
	// Always empty when Status is "ok" or "running".
	FailureCategory string `json:"failure_category,omitempty"`
	ErrorMessage    string `json:"error_message,omitempty"`
}
