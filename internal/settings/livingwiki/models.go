// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package livingwiki

import (
	"context"
	"errors"
	"time"
)

// ErrEncryptionKeyRequired is returned by RepoSettingsStore.SetRepoSettings
// when the call would persist a non-empty LLMOverride.APIKey but the
// server has no encryption key configured (cfg.Security.EncryptionKey
// is empty AND the OSS escape hatch is off). The GraphQL layer maps
// this to an extension code "ENCRYPTION_KEY_REQUIRED" so the UI can
// render a clear message.
//
// Implementations wrap the lower-layer db.ErrEncryptionKeyRequired
// sentinel — callers should match via errors.Is.
var ErrEncryptionKeyRequired = errors.New("livingwiki: api key cannot be saved without an encryption key")

// Settings holds the living-wiki configuration as stored in the DB (via the
// admin UI). Zero/empty values mean "not configured by UI; use env-var or
// built-in default".
//
// Secret fields (tokens, webhook secrets) are stored encrypted at rest. The
// resolver returns the sentinel "********" for any field that has been set,
// so clients can detect "a value exists" without reading the value back.
type Settings struct {
	// --- Orchestration ---

	// Enabled is the master on/off switch. nil means "not set by UI".
	Enabled *bool `json:"enabled,omitempty"`

	// WorkerCount controls Dispatcher goroutine count. 0 = not set by UI.
	WorkerCount int `json:"worker_count,omitempty"`

	// EventTimeout is the per-event handler deadline. Empty = not set by UI.
	// Stored as a Go duration string (e.g. "5m").
	EventTimeout string `json:"event_timeout,omitempty"`

	// --- Source integrations (encrypted at rest) ---

	// GitHubToken is a Personal Access Token or GitHub App installation token.
	GitHubToken string `json:"github_token,omitempty"`

	// GitLabToken is a GitLab PRIVATE-TOKEN.
	GitLabToken string `json:"gitlab_token,omitempty"`

	// ConfluenceSite is the Atlassian Cloud site subdomain (e.g. "mycompany"
	// for mycompany.atlassian.net). Not a secret; stored in plaintext.
	ConfluenceSite string `json:"confluence_site,omitempty"`

	// ConfluenceEmail is the Atlassian account email for Basic auth.
	ConfluenceEmail string `json:"confluence_email,omitempty"`

	// ConfluenceToken is the Atlassian API token.
	ConfluenceToken string `json:"confluence_token,omitempty"`

	// NotionToken is the Notion integration secret.
	NotionToken string `json:"notion_token,omitempty"`

	// --- Webhook validation secrets (encrypted at rest) ---

	// ConfluenceWebhookSecret is the HMAC-SHA256 secret for Confluence webhooks.
	ConfluenceWebhookSecret string `json:"confluence_webhook_secret,omitempty"`

	// NotionWebhookSecret is reserved for future Notion webhook validation.
	NotionWebhookSecret string `json:"notion_webhook_secret,omitempty"`

	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by,omitempty"`
}

// SecretSentinel is returned in place of a plaintext secret value when one
// has been stored. The UI displays this to communicate "a value exists".
const SecretSentinel = "********"

// secretFields lists field names that carry credentials and must be encrypted
// at rest. The Store implementation uses this list; callers use MaskSecrets.
var secretFields = []string{
	"github_token",
	"gitlab_token",
	"confluence_email",
	"confluence_token",
	"notion_token",
	"confluence_webhook_secret",
	"notion_webhook_secret",
}

// MaskSecrets returns a copy of s where every secret field that has a
// non-empty value is replaced by [SecretSentinel]. This is the struct that
// GraphQL resolvers return to the UI.
func MaskSecrets(s Settings) Settings {
	if s.GitHubToken != "" {
		s.GitHubToken = SecretSentinel
	}
	if s.GitLabToken != "" {
		s.GitLabToken = SecretSentinel
	}
	if s.ConfluenceEmail != "" {
		s.ConfluenceEmail = SecretSentinel
	}
	if s.ConfluenceToken != "" {
		s.ConfluenceToken = SecretSentinel
	}
	if s.NotionToken != "" {
		s.NotionToken = SecretSentinel
	}
	if s.ConfluenceWebhookSecret != "" {
		s.ConfluenceWebhookSecret = SecretSentinel
	}
	if s.NotionWebhookSecret != "" {
		s.NotionWebhookSecret = SecretSentinel
	}
	return s
}

// Store is the persistence interface for living-wiki settings.
// Implementations: [MemStore] (tests) and the SurrealDB store in internal/db.
type Store interface {
	// Get returns the current settings, or a zero-value Settings if none have
	// been saved yet. Secrets are returned decrypted.
	Get() (*Settings, error)

	// Set persists s. Secret fields are encrypted before writing.
	Set(s *Settings) error
}

// RepoSettingsStore is the persistence interface for per-repo living-wiki
// opt-in records. Every method is tenant-scoped (Q5 resolved).
//
// Implementations: [RepoSettingsMemStore] (tests) and the SurrealDB store
// in internal/db.
type RepoSettingsStore interface {
	// GetRepoSettings returns the settings for the given repo, or nil if no
	// row exists yet (default-disabled). A nil return is NOT an error.
	GetRepoSettings(ctx context.Context, tenantID, repoID string) (*RepositoryLivingWikiSettings, error)

	// SetRepoSettings persists s. Creates or replaces the row identified by
	// (TenantID, RepoID).
	SetRepoSettings(ctx context.Context, s RepositoryLivingWikiSettings) error

	// ListEnabledRepos returns all repos with enabled=true for the given
	// tenant. Used by R6's scheduler tick.
	ListEnabledRepos(ctx context.Context, tenantID string) ([]RepositoryLivingWikiSettings, error)

	// DeleteRepoSettings hard-deletes the row for the given repo. Intended
	// for admin cleanup only; normal disable uses SetRepoSettings with
	// Enabled=false.
	DeleteRepoSettings(ctx context.Context, tenantID, repoID string) error

	// RepositoriesUsingSink returns all repos that have a sink with the
	// given integrationName. Used by the admin query.
	RepositoriesUsingSink(ctx context.Context, tenantID, integrationName string) ([]RepositoryLivingWikiSettings, error)
}

// ─────────────────────────────────────────────────────────────────────────────
// Per-page publish status
// ─────────────────────────────────────────────────────────────────────────────

// Fixup status values for lw_page_publish_status.fixup_status (CR4).
const (
	// FixupStatusNone means the page never contained stub links.
	FixupStatusNone = "none"
	// FixupStatusPending means the page was published with stubs; fix-up not yet run.
	FixupStatusPending = "pending"
	// FixupStatusDone means the fix-up pass successfully re-rendered the page.
	FixupStatusDone = "done"
	// FixupStatusFailed means a fix-up attempt failed; will retry on next run.
	FixupStatusFailed = "failed"
)

// PagePublishStatusRow is the Go representation of a single lw_page_publish_status row.
type PagePublishStatusRow struct {
	RepoID             string
	PageID             string
	SinkKind           string
	IntegrationName    string
	Status             string    // "pending", "generating", "ready", "failed", "failed_fixup"
	ErrorMsg           string
	ContentFingerprint string    // empty string (not nil) when no fingerprint yet (LD-7, C2)
	HasStubs           bool
	StubTargetPageIDs  []string
	FixupStatus        string    // one of the FixupStatus* constants
	UpdatedAt          time.Time
}

// SetReadyArgs carries the arguments for PagePublishStatusStore.SetReady.
// This is the ONLY path that writes content_fingerprint (CR9).
type SetReadyArgs struct {
	RepoID          string
	PageID          string
	SinkKind        string
	IntegrationName string
	Fingerprint     string
	HasStubs        bool
	StubTargetIDs   []string
	FixupStatus     string // when empty, inferred from HasStubs
}

// SetNonReadyArgs carries the arguments for PagePublishStatusStore.SetNonReady.
// Does NOT include fingerprint/stub fields (CR9: preserve-on-failure).
type SetNonReadyArgs struct {
	RepoID          string
	PageID          string
	SinkKind        string
	IntegrationName string
	Status          string // "generating", "failed", "failed_fixup"
	ErrorMsg        string
}

// UpdateFixupStatusArgs carries the arguments for updating the fixup state
// on an already-ready row (Phase 3 fix-up pass).
type UpdateFixupStatusArgs struct {
	RepoID          string
	PageID          string
	SinkKind        string
	IntegrationName string
	FixupStatus     string // FixupStatusDone or FixupStatusFailed
	HasStubs        bool   // false when stubs have been cleared
}

// PagePublishStatusStore persists per-page, per-sink publish state for the
// Living Wiki stream-dispatch feature. The table (lw_page_publish_status,
// migration 050) is keyed on (repo_id, page_id, sink_kind, integration_name).
//
// API contract (CR9):
//   - SetReady is the ONLY write path for content_fingerprint and stub fields.
//   - SetNonReady preserves those fields unchanged.
//
// Implementations: LivingWikiPagePublishStatusStore (internal/db) + in-memory
// test stub.
type PagePublishStatusStore interface {
	// SetReady records a successful sink dispatch. Writes status='ready' plus
	// content_fingerprint, has_stubs, stub_target_page_ids, fixup_status.
	SetReady(ctx context.Context, args SetReadyArgs) error

	// SetNonReady records a non-ready transition. Does NOT touch
	// content_fingerprint, has_stubs, stub_target_page_ids, or fixup_status
	// (CR9: preserve-on-failure contract).
	SetNonReady(ctx context.Context, args SetNonReadyArgs) error

	// LoadFingerprints loads all rows for repoID and returns a nested map:
	//   pageID → sinkKey("<sink_kind>/<integration_name>") → PagePublishStatusRow.
	// Used by smart-resume's 3-way bucket split (CR4, LD-7).
	LoadFingerprints(ctx context.Context, repoID string) (map[string]map[string]PagePublishStatusRow, error)

	// ListByRepo returns all rows for repoID, ordered by updated_at DESC.
	// Used by the livingWikiPublishStatus GraphQL query.
	ListByRepo(ctx context.Context, repoID string) ([]PagePublishStatusRow, error)

	// UpdateFixupStatus updates fixup_status (and has_stubs) without touching
	// other fields. Used by Phase 3's fix-up pass.
	UpdateFixupStatus(ctx context.Context, args UpdateFixupStatusArgs) error
}

// ─────────────────────────────────────────────────────────────────────────────

// JobResultStore persists and retrieves per-run living-wiki job outcomes.
// Save is idempotent by JobID — a second Save with the same JobID replaces
// the existing row in place rather than appending a new one. This matches
// the cold-start runner's contract that one logical run produces at most
// one durable result row, even if the runner closure is re-entered (which
// today is prevented by MaxAttempts=1 on the LLM-orchestrator EnqueueRequest;
// the store-level guarantee is independent of that and survives any future
// caller path).
//
// Implementations: [db.LivingWikiJobResultStore] (SurrealDB-backed) and
// [MemJobResultStore] (tests).
type JobResultStore interface {
	// Save persists result under tenantID. Returns an error only on DB failure.
	//
	// Upsert by JobID: a second Save with the same JobID overwrites the
	// existing row's fields. No duplicate rows are produced. The SurrealDB
	// implementation uses UPSERT keyed on a deterministic record id derived
	// from JobID and is backed by a UNIQUE index on the job_id column.
	Save(ctx context.Context, tenantID string, result *LivingWikiJobResult) error

	// GetByJobID returns the result for the given jobID, or nil if not found.
	GetByJobID(ctx context.Context, jobID string) (*LivingWikiJobResult, error)

	// LastResultForRepo returns the most recently started result for the given
	// tenant and repo, or nil when no results have been recorded.
	LastResultForRepo(ctx context.Context, tenantID, repoID string) (*LivingWikiJobResult, error)
}
