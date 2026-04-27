// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package livingwiki

import "time"

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

	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by,omitempty"`
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
	Status              string     `json:"status"`
	ErrorMessage        string     `json:"error_message,omitempty"`
}
