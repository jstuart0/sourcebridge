// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package config

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// Config holds the complete application configuration.
type Config struct {
	Env           string              `mapstructure:"env"`     // development, production
	Edition       string              `mapstructure:"edition"` // oss, enterprise
	Server        ServerConfig        `mapstructure:"server"`
	Storage       StorageConfig       `mapstructure:"storage"`
	Indexing      IndexingConfig      `mapstructure:"indexing"`
	LLM           LLMConfig           `mapstructure:"llm"`
	Linking       LinkingConfig       `mapstructure:"linking"`
	UI            UIConfig            `mapstructure:"ui"`
	Security      SecurityConfig      `mapstructure:"security"`
	Worker        WorkerConfig        `mapstructure:"worker"`
	Git           GitConfig           `mapstructure:"git"`
	MCP           MCPConfig           `mapstructure:"mcp"`
	Comprehension ComprehensionConfig `mapstructure:"comprehension"`
	Trash         TrashConfig         `mapstructure:"trash"`
	QA            QAConfig            `mapstructure:"qa"`
	LivingWiki    LivingWikiConfig    `mapstructure:"living_wiki"`
	ChangeWatch   ChangeWatchConfig   `mapstructure:"change_watch"`
	ConnectorAPI  ConnectorAPIConfig  `mapstructure:"connector_api"`
	Shutdown      ShutdownConfig      `mapstructure:"shutdown"`
}

// ComprehensionConfig holds tunables for the LLM job orchestrator and
// comprehension strategies. All fields are optional; zero values fall
// through to the orchestrator package defaults.
type ComprehensionConfig struct {
	// MaxConcurrency bounds how many LLM jobs run in parallel across
	// the whole server. Defaults to 3 (safe for a single Ollama).
	MaxConcurrency int `mapstructure:"max_concurrency"`
	// MaxPromptTokens (future) — the budget passed into check_prompt_budget
	// in workers. Not yet read by the Go side but reserved to avoid
	// breaking config files when the setting is introduced.
	MaxPromptTokens int `mapstructure:"max_prompt_tokens"`
}

// GitConfig holds git credentials for cloning private repositories.
//
// R3 slice 2: this struct is the env-bootstrap layer of the git
// credential resolver. cli/serve.go captures it BY VALUE into the
// resolver and never mutates it post-boot. Production deployments
// set the encrypted DB-backed value via the admin UI; the env var is
// only consulted when no DB row exists (fresh install) or as a stale
// fallback during a transient DB outage.
type GitConfig struct {
	DefaultToken string `mapstructure:"default_token"` // PAT used when no per-repo token is provided
	SSHKeyPath   string `mapstructure:"ssh_key_path"`  // path to SSH private key for SSH URLs
	// SSHKeyPathRoot is the allow-root for admin-supplied SSH key paths
	// at save time. Empty → /etc/sourcebridge/git-keys (the homelab + OSS
	// default mount root). Operators with a different layout point this
	// at their own root via SOURCEBRIDGE_GIT_SSH_KEY_PATH_ROOT.
	SSHKeyPathRoot string `mapstructure:"ssh_key_path_root"`
}

// IsDevelopment returns true when running in development mode.
func (c *Config) IsDevelopment() bool {
	return c.Env == "development" || c.Env == "dev"
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	HTTPPort       int      `mapstructure:"http_port"`
	GRPCPort       int      `mapstructure:"grpc_port"`
	PublicBaseURL  string   `mapstructure:"public_base_url"`
	TrustedProxies []string `mapstructure:"trusted_proxies"`
	CORSOrigins    []string `mapstructure:"cors_origins"`
	MaxBodySize    int64    `mapstructure:"max_body_size"`
}

// StorageConfig holds database and cache settings.
type StorageConfig struct {
	SurrealMode      string `mapstructure:"surreal_mode"` // embedded, external
	SurrealURL       string `mapstructure:"surreal_url"`
	SurrealNamespace string `mapstructure:"surreal_namespace"`
	SurrealDatabase  string `mapstructure:"surreal_database"`
	SurrealUser      string `mapstructure:"surreal_user"`
	SurrealPass      string `mapstructure:"surreal_pass"`
	SurrealDataPath  string `mapstructure:"surreal_data_path"`
	RedisURL         string `mapstructure:"redis_url"`
	RedisMode        string `mapstructure:"redis_mode"` // external, memory
	RepoCachePath    string `mapstructure:"repo_cache_path"`
}

// IndexingConfig holds code indexing settings.
type IndexingConfig struct {
	MaxFileSize    int64    `mapstructure:"max_file_size_bytes"`
	IgnoreGlobs    []string `mapstructure:"ignore_globs"`
	MaxConcurrency int      `mapstructure:"max_concurrency"`
	SCIPEnabled    bool     `mapstructure:"scip_enabled"`
}

// LLMConfig holds AI/LLM provider settings.
type LLMConfig struct {
	Provider                 string `mapstructure:"provider"`
	BaseURL                  string `mapstructure:"base_url"`
	APIKey                   string `mapstructure:"api_key"`
	SummaryModel             string `mapstructure:"summary_model"`              // default model (used for analysis in advanced mode)
	ReviewModel              string `mapstructure:"review_model"`               // review operations
	AskModel                 string `mapstructure:"ask_model"`                  // discussion/Q&A operations
	KnowledgeModel           string `mapstructure:"knowledge_model"`            // knowledge generation (cliffNotes, codeTour, etc.)
	ArchitectureDiagramModel string `mapstructure:"architecture_diagram_model"` // AI architecture diagrams
	ReportModel              string `mapstructure:"report_model"`               // report generation
	DraftModel               string `mapstructure:"draft_model"`                // LM Studio only: sent as draft_model per request
	TimeoutSecs              int    `mapstructure:"timeout_seconds"`
	AdvancedMode             bool   `mapstructure:"advanced_mode"` // when true, per-operation models are active
}

// OperationGroup identifies the coarse LLM operation family used for per-operation
// model selection in advanced mode. It is defined here (rather than in
// internal/llm/resolution) so that config.LLMConfig.ModelForOp can accept the
// typed value without introducing a circular import between the config and
// resolution packages. resolution.OperationGroup is a type alias over this type
// so callers can use either name.
type OperationGroup string

const (
	// OpGroupAnalysis covers analysis-style LLM calls (default / fallback group).
	OpGroupAnalysis OperationGroup = "analysis"
	// OpGroupReview covers code-review LLM calls.
	OpGroupReview OperationGroup = "review"
	// OpGroupDiscussion covers discussion / Q&A LLM calls.
	OpGroupDiscussion OperationGroup = "discussion"
	// OpGroupKnowledge covers knowledge-generation LLM calls (cliff notes, etc.).
	OpGroupKnowledge OperationGroup = "knowledge"
	// OpGroupArchitectureDiagram covers architecture-diagram LLM calls.
	OpGroupArchitectureDiagram OperationGroup = "architecture_diagram"
	// OpGroupReport covers long-form report-generation LLM calls.
	OpGroupReport OperationGroup = "report"
)

// ModelForOp is the type-safe variant of ModelForOperation. It accepts a typed
// OperationGroup constant instead of a raw string, catching invalid group names
// at compile time for internal callers.
//
// External callers and tests that already use ModelForOperation(string) are
// unaffected — both methods co-exist.
func (l *LLMConfig) ModelForOp(group OperationGroup) string {
	return l.ModelForOperation(string(group))
}

// ModelForOperation returns the model to use for a given operation group.
// In advanced mode, returns the per-operation model if configured.
// In simple mode (or if the per-operation model is empty), returns SummaryModel.
func (l *LLMConfig) ModelForOperation(group string) string {
	if !l.AdvancedMode {
		return l.SummaryModel
	}
	switch group {
	case "analysis":
		if l.SummaryModel != "" {
			return l.SummaryModel
		}
	case "review":
		if l.ReviewModel != "" {
			return l.ReviewModel
		}
	case "discussion":
		if l.AskModel != "" {
			return l.AskModel
		}
	case "knowledge":
		if l.KnowledgeModel != "" {
			return l.KnowledgeModel
		}
	case "architecture_diagram":
		if l.ArchitectureDiagramModel != "" {
			return l.ArchitectureDiagramModel
		}
	case "report":
		if l.ReportModel != "" {
			return l.ReportModel
		}
	}
	return l.SummaryModel
}

// LinkingConfig holds requirement linking settings.
type LinkingConfig struct {
	MinConfidenceUI        float64 `mapstructure:"min_confidence_ui"`
	MinConfidenceCodeLens  float64 `mapstructure:"min_confidence_codelens"`
	MinConfidencePRComment float64 `mapstructure:"min_confidence_pr_comment"`

	// InvalidateGraceHours is the grace window after a code change before
	// dependent links are flagged as invalidated by the change-watch
	// pipeline. Default 24 hours (the plan v5 nice-to-have L1). The
	// window covers cases where an agent makes a multi-commit edit
	// sequence (refactor + tests + doc updates) — invalidating links on
	// the first commit and not the last would create churn the operator
	// needs to ignore. Zero or negative disables the grace window
	// (links invalidate immediately on the next router event).
	// SOURCEBRIDGE_LINKING_INVALIDATE_GRACE_HOURS.
	InvalidateGraceHours int `mapstructure:"invalidate_grace_hours"`
}

// UIConfig holds user interface settings.
type UIConfig struct {
	Theme          string `mapstructure:"theme"`
	AccentHue      int    `mapstructure:"accent_hue"`
	OverlayDefault bool   `mapstructure:"overlay_default"`
}

// SecurityConfig holds security-related settings.
type SecurityConfig struct {
	JWTSecret           string     `mapstructure:"jwt_secret"`
	// JWTSecretFile resolves to the path of a file containing the JWT
	// signing secret. CA-311: mirrors the EncryptionKey/EncryptionKeyFile
	// pattern so the file (higher-trust than env vars, which leak via
	// /proc, docker inspect, and shell history) takes precedence over
	// SOURCEBRIDGE_SECURITY_JWT_SECRET.  When neither is set, an in-memory
	// secret is auto-generated at boot — sessions do NOT survive restart in
	// that mode; production multi-replica deployments must set the file or
	// the literal env (Helm chart's `jwt-secret` Secret takes the literal
	// path).
	JWTSecretFile       string     `mapstructure:"jwt_secret_file"` // env: SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE
	JWTTTLMinutes       int        `mapstructure:"jwt_ttl_minutes"`
	EncryptionKey       string     `mapstructure:"encryption_key"`
	CSRFEnabled         bool       `mapstructure:"csrf_enabled"`
	GRPCAuthSecret      string     `mapstructure:"grpc_auth_secret"`
	Mode                string     `mapstructure:"mode"` // oss, commercial
	OIDC                OIDCConfig `mapstructure:"oidc"`
	GitHubWebhookSecret string     `mapstructure:"github_webhook_secret"`
	GitLabWebhookSecret string     `mapstructure:"gitlab_webhook_secret"`
	// APITokenLegacyAdminDefault controls the role assigned to API tokens whose
	// role field is empty (i.e. tokens that somehow pre-date migration 056 and
	// were not updated by the backfill).  Default false (least privilege: empty
	// role → "user").  Set true temporarily during a rolling migration if
	// pre-existing tokens must continue acting as admin until the backfill
	// confirms it has run.  A startup warning is emitted when true.
	//
	// Config key: security.api_token_legacy_admin_default
	// Env var:    SOURCEBRIDGE_SECURITY_API_TOKEN_LEGACY_ADMIN_DEFAULT
	APITokenLegacyAdminDefault bool `mapstructure:"api_token_legacy_admin_default" toml:"api_token_legacy_admin_default" json:"api_token_legacy_admin_default"`
}

// ResolveEncryptionKey resolves the encryption key following the priority order:
//
//  1. SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE (file path) — if set and the
//     file exists, is readable, and is non-empty after trimming whitespace.
//  2. s.EncryptionKey (literal value from env / config.toml) — if non-empty.
//  3. Empty string — key is unset; caller logs accordingly.
//
// source is one of: "file", "literal-env", "file-missing-fallback-env",
// "unset".
//
// When both sources are set, the file wins and a WARN is logged (matches Vault
// / Postgres *_FILE precedence — files are higher-trust because env vars leak
// via /proc, docker inspect, and shell history).
//
// If _FILE is set but the file is missing / unreadable / empty, an ERROR is
// logged and the method falls through to literal-env.
//
// r1 H4 — minimum-entropy guardrail: if the resolved key is non-empty and
// shorter than 32 bytes, an ERROR is logged but the key is still returned
// (fail-soft: consistent with the existing WARN-only encryption posture).
// Document the expected format: 32+ bytes of cryptographic random,
// hex- or base64-encoded (generate with `openssl rand -hex 32`).
func (s SecurityConfig) ResolveEncryptionKey() (key string, source string, err error) {
	filePath := strings.TrimSpace(os.Getenv("SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE"))
	hasLiteralEnv := s.EncryptionKey != ""

	if filePath != "" {
		data, readErr := os.ReadFile(filePath)
		if readErr != nil {
			slog.Error("encryption key: SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE is set but file is unreadable; falling back to literal env",
				"path", filePath, "err", readErr)
			if hasLiteralEnv {
				return s.checkEntropyAndReturn(s.EncryptionKey, "file-missing-fallback-env")
			}
			return "", "unset", nil
		}
		trimmed := strings.TrimSpace(string(data))
		if trimmed == "" {
			slog.Error("encryption key: SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE points to an empty file; falling back to literal env",
				"path", filePath)
			if hasLiteralEnv {
				return s.checkEntropyAndReturn(s.EncryptionKey, "file-missing-fallback-env")
			}
			return "", "unset", nil
		}
		if hasLiteralEnv {
			slog.Warn("encryption key resolved from file; SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY env var is set but ignored — file precedence is by design (matches Vault / Postgres convention)",
				"path", filePath)
		}
		return s.checkEntropyAndReturn(trimmed, "file")
	}

	if hasLiteralEnv {
		return s.checkEntropyAndReturn(s.EncryptionKey, "literal-env")
	}

	return "", "unset", nil
}

// ResolveJWTSecret resolves the JWT signing secret following the priority
// order:
//
//  1. SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE (file path) — if set and the
//     file exists, is readable, and is non-empty after trimming whitespace.
//  2. s.JWTSecret (literal value from env / config.toml) — if non-empty.
//  3. Empty string + source="unset" — caller is expected to auto-generate
//     an in-memory secret and warn that sessions will not survive restart.
//
// source is one of: "file", "literal-env", "file-missing-fallback-env",
// "unset".  Mirrors ResolveEncryptionKey's behavior — see that method's
// docstring for the rationale on file precedence.
//
// Unlike ResolveEncryptionKey, the entropy gate (≥32 bytes) is NOT applied
// here; Validate() enforces it after the caller decides between the
// resolved value and the auto-generated fallback. That way the gate fires
// on operator-configured but-too-short secrets without rejecting the
// auto-generated 64-hex placeholder.
func (s SecurityConfig) ResolveJWTSecret() (key string, source string, err error) {
	filePath := strings.TrimSpace(os.Getenv("SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE"))
	if filePath == "" {
		filePath = strings.TrimSpace(s.JWTSecretFile)
	}
	hasLiteralEnv := s.JWTSecret != ""

	if filePath != "" {
		data, readErr := os.ReadFile(filePath)
		if readErr != nil {
			slog.Error("JWT secret: SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE is set but file is unreadable; falling back to literal env",
				"path", filePath, "err", readErr)
			if hasLiteralEnv {
				return s.JWTSecret, "file-missing-fallback-env", nil
			}
			return "", "unset", nil
		}
		trimmed := strings.TrimSpace(string(data))
		if trimmed == "" {
			slog.Error("JWT secret: SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE points to an empty file; falling back to literal env",
				"path", filePath)
			if hasLiteralEnv {
				return s.JWTSecret, "file-missing-fallback-env", nil
			}
			return "", "unset", nil
		}
		if hasLiteralEnv {
			slog.Warn("JWT secret resolved from file; SOURCEBRIDGE_SECURITY_JWT_SECRET env var is set but ignored — file precedence is by design (matches Vault / Postgres convention)",
				"path", filePath)
		}
		return trimmed, "file", nil
	}

	if hasLiteralEnv {
		return s.JWTSecret, "literal-env", nil
	}

	return "", "unset", nil
}

// checkEntropyAndReturn emits an ERROR log when the key is shorter than 32
// bytes (r1 H4 minimum-entropy guardrail) but still returns the key. The
// caller must not treat the error return value from ResolveEncryptionKey as
// an unrecoverable failure — this is a loud warning only.
func (s SecurityConfig) checkEntropyAndReturn(key, source string) (string, string, error) {
	if len(key) < 32 {
		slog.Error("encryption key is shorter than 32 bytes; current implementation hashes passphrase via single-pass SHA-256 (NOT a KDF), so short keys are brute-forceable. "+
			"See docs/admin/llm-config.md#encryption-key for the expected format (32+ random bytes, hex/base64). "+
			"Filed as follow-up: KDF strengthening (PBKDF2/scrypt/Argon2).",
			"key_length_bytes", len(key), "source", source)
	}
	return key, source, nil
}

// OIDCConfig holds OpenID Connect settings for SSO integration.
type OIDCConfig struct {
	IssuerURL    string   `mapstructure:"issuer_url"`
	ClientID     string   `mapstructure:"client_id"`
	ClientSecret string   `mapstructure:"client_secret"`
	RedirectURL  string   `mapstructure:"redirect_url"`
	Scopes       []string `mapstructure:"scopes"`
}

// WorkerConfig holds gRPC worker connection settings.
type WorkerConfig struct {
	Address string `mapstructure:"address"`
	// TLS controls mTLS for the API↔worker gRPC channel. Slice 4 of
	// plan 2026-04-29-workspace-llm-source-of-truth-r2.md. When TLSEnabled
	// is true and all three paths are valid, the worker client dials
	// the worker over TLS with mutual auth. When false (default), the
	// legacy insecure path is used (OSS dev compatibility).
	TLS WorkerTLSConfig `mapstructure:"tls"`
}

// WorkerTLSConfig holds the cert/key/CA paths for mTLS API↔worker
// gRPC. All three paths are required when Enabled is true; the boot
// path validates and fails closed.
type WorkerTLSConfig struct {
	// Enabled toggles mTLS for the worker gRPC channel.
	// SOURCEBRIDGE_WORKER_TLS_ENABLED.
	Enabled bool `mapstructure:"enabled"`
	// CertPath is the API-side client certificate (mounted from a
	// cert-manager Certificate secret).
	// SOURCEBRIDGE_WORKER_TLS_CERT_PATH.
	CertPath string `mapstructure:"cert_path"`
	// KeyPath is the API-side client private key.
	// SOURCEBRIDGE_WORKER_TLS_KEY_PATH.
	KeyPath string `mapstructure:"key_path"`
	// CAPath is the bundle that verifies the worker's server cert.
	// SOURCEBRIDGE_WORKER_TLS_CA_PATH.
	CAPath string `mapstructure:"ca_path"`
	// ServerName is the SNI/SAN to verify against the worker's cert.
	// Defaults to "worker.sourcebridge.svc.cluster.local". Set this
	// when running the worker behind a different DNS name.
	// SOURCEBRIDGE_WORKER_TLS_SERVER_NAME.
	ServerName string `mapstructure:"server_name"`
}

// MCPConfig holds Model Context Protocol (MCP) server settings.
type MCPConfig struct {
	Enabled     bool   `mapstructure:"enabled"`
	Repos       string `mapstructure:"repos"`        // comma-separated repo IDs (empty = all)
	SessionTTL  int    `mapstructure:"session_ttl"`  // seconds before idle session is reaped
	Keepalive   int    `mapstructure:"keepalive"`    // seconds between SSE keepalive pings
	MaxSessions int    `mapstructure:"max_sessions"` // max concurrent MCP sessions (0 = unlimited)
}

// QAConfig controls the server-side deep-QA orchestrator.
//
// The orchestrator (internal/qa) owns both fast and deep question
// answering for hosted deployments. For local-desktop installs the
// subprocess fast path is kept by default so working-tree answers
// still work against uncommitted edits.
//
// Limits are token-budget based. A request/min per-IP cap is only a
// DoS guard; the meaningful budgets are per-session, per-repo, and
// per-deployment token spend.
type QAConfig struct {
	// ServerSideEnabled turns on the new ask endpoint (GraphQL ask,
	// REST POST /api/v1/ask, MCP ask_question). When false the server
	// returns 503 on these surfaces and the CLI falls back to the
	// subprocess path.
	ServerSideEnabled bool `mapstructure:"server_side_enabled"` // SOURCEBRIDGE_QA_SERVER_SIDE_ENABLED
	// LocalFastModeSubprocess keeps the CLI's subprocess fast path
	// active on local-desktop installs so developers retain
	// working-tree visibility (Ledger F13). Hosted / multi-tenant
	// installs set this false once they've validated the server path.
	LocalFastModeSubprocess bool `mapstructure:"local_fast_mode_subprocess"`
	// QuestionMaxBytes caps question length. Default 4096 (4 KB).
	QuestionMaxBytes int `mapstructure:"question_max_bytes"`
	// SessionTokensPerHour caps total prompt+completion tokens per
	// user session per hour. Default 100_000. Zero disables.
	SessionTokensPerHour int `mapstructure:"session_tokens_per_hour"`
	// RepoTokensPerDay caps total tokens per repo per day.
	// Default 1_000_000. Zero disables.
	RepoTokensPerDay int `mapstructure:"repo_tokens_per_day"`
	// DeploymentTokensPerDay is the operator-level circuit breaker.
	// Default 10_000_000. Zero disables.
	DeploymentTokensPerDay int `mapstructure:"deployment_tokens_per_day"`
	// SynthesisLane bounds concurrent synthesis calls (qa.llm_call)
	// against the reasoning worker. Default 4 — tuned so embed and
	// synthesis don't starve each other.
	SynthesisLane int `mapstructure:"synthesis_lane"`
	// AgenticRetrievalEnabled turns on the tool-using deep-QA loop
	// (plan 2026-04-23-agentic-retrieval-for-deep-qa.md). Default
	// false through Phase 3; flipped after the paired benchmark
	// report. Orchestrator additionally requires the active
	// provider/model to support structured tool use — when the
	// capability check fails, agentic is off regardless of this flag.
	AgenticRetrievalEnabled bool `mapstructure:"agentic_retrieval_enabled"`
	// AgenticRetrievalCanaryPct is the percentage of requests the
	// agentic path handles when `AgenticRetrievalEnabled` is false
	// and a staged rollout is in progress. 0 = off; 10 / 50 are
	// Stage A / Stage B; 100 = equivalent to enabling the flag.
	// Per-request coin flip; only evaluated when the provider
	// supports tool use.
	AgenticRetrievalCanaryPct int `mapstructure:"agentic_retrieval_canary_pct"`
	// PromptCachingEnabled applies Anthropic prompt-cache markers
	// (cache_control: ephemeral) to the agentic system prompt and
	// tool schemas. Cuts input-token cost 60–80% on multi-turn loops
	// by serving repeated prefixes from the cache. Default true —
	// safe on all current Anthropic models and ignored by providers
	// that don't support it. Set false to roll back if cache behavior
	// breaks in a future SDK/model combination.
	PromptCachingEnabled bool `mapstructure:"prompt_caching_enabled"`
	// SmartClassifierEnabled turns on the LLM-backed question
	// profiler (quality-push Phase 2). Runs a cheap Haiku call that
	// returns evidence-kind hints and advisory symbol/file/topic
	// candidates; the agentic loop pre-populates seed context with
	// these so the first turn starts with the right hypothesis.
	// Default off until the Phase-5 benchmark confirms quality win.
	SmartClassifierEnabled bool `mapstructure:"smart_classifier_enabled"`
	// QueryDecompositionEnabled turns on the pre-pass that splits
	// multi-hop architecture / cross_cutting / execution_flow
	// questions into 3–4 sub-questions, runs the agentic loop per
	// sub-question in parallel, and synthesizes the final answer.
	// Targets the multi-hop punt failure mode (quality-push Phase 4).
	// Default off until the Phase-5 benchmark confirms quality and
	// cost tradeoffs.
	QueryDecompositionEnabled bool `mapstructure:"query_decomposition_enabled"`
}

// TrashConfig controls the soft-delete recycle bin feature.
//
// When Enabled is false, moveToTrash mutations and the retention worker
// are both no-ops; existing hard-delete paths remain active. Turning
// this on upgrades hard-deletes into soft-deletes and starts the
// retention sweep.
type TrashConfig struct {
	Enabled          bool `mapstructure:"enabled"`            // SOURCEBRIDGE_TRASH_ENABLED
	RetentionDays    int  `mapstructure:"retention_days"`     // SOURCEBRIDGE_TRASH_RETENTION_DAYS (default 30, min 1, max 365)
	SweepIntervalSec int  `mapstructure:"sweep_interval_sec"` // SOURCEBRIDGE_TRASH_SWEEP_INTERVAL (default 21600 = 6h)
	MaxBatchSize     int  `mapstructure:"max_batch_size"`     // SOURCEBRIDGE_TRASH_SWEEP_MAX_BATCH (default 500)
}

// LivingWikiConfig controls the living-wiki trigger layer (A1.P3).
//
// Environment variable prefix: SOURCEBRIDGE_LIVING_WIKI_*
//
// Example (config.toml):
//
//	[living_wiki]
//	enabled                    = true
//	worker_count               = 4
//	event_timeout              = "5m"
//	confluence_webhook_secret  = "your-confluence-hmac-secret"
//	notion_webhook_secret      = "your-notion-secret"
//	scheduler_interval         = "15m"
//	max_concurrent_jobs_per_tenant = 5
type LivingWikiConfig struct {
	// Enabled activates the living-wiki feature. When false, the dispatcher
	// is not started and webhook endpoints return 501.
	Enabled bool `mapstructure:"enabled"`

	// WorkerCount is the number of goroutines draining the global overflow
	// queue inside the Dispatcher. Default 4.
	WorkerCount int `mapstructure:"worker_count"`

	// EventTimeout is the maximum duration allowed for a single event handler.
	// Default 5 minutes. Expressed as a Go duration string (e.g. "5m").
	EventTimeout string `mapstructure:"event_timeout"`

	// ConfluenceWebhookSecret is the HMAC-SHA256 shared secret used to validate
	// the X-Confluence-Signature header on incoming Confluence webhooks.
	// When empty, signature validation is skipped (development only).
	// Set via SOURCEBRIDGE_LIVING_WIKI_CONFLUENCE_WEBHOOK_SECRET.
	ConfluenceWebhookSecret string `mapstructure:"confluence_webhook_secret"`

	// NotionWebhookSecret is reserved for Notion webhook validation when Notion
	// ships a richer webhook model. Unused as of early 2026.
	// Set via SOURCEBRIDGE_LIVING_WIKI_NOTION_WEBHOOK_SECRET.
	NotionWebhookSecret string `mapstructure:"notion_webhook_secret"`

	// SchedulerInterval is the default regen frequency per repo.
	// Default "15m". Expressed as a Go duration string.
	// Set via SOURCEBRIDGE_LIVING_WIKI_SCHEDULER_INTERVAL.
	SchedulerInterval string `mapstructure:"scheduler_interval"`

	// MaxConcurrentJobsPerTenant caps concurrent regen jobs across the tenant.
	// Default 5. Prevents a single high-volume tenant from monopolising workers.
	// Set via SOURCEBRIDGE_LIVING_WIKI_MAX_CONCURRENT_JOBS_PER_TENANT.
	MaxConcurrentJobsPerTenant int `mapstructure:"max_concurrent_jobs_per_tenant"`
}

// ChangeWatchConfig controls the in-process change-watch feedback loop
// (Phase 1.C of the MCP-edits plan,
// thoughts/shared/plans/2026-04-29-mcp-edits-feedback-loop.md).
//
// Environment variable prefix: SOURCEBRIDGE_CHANGE_WATCH_*
//
// The feedback loop is the closed pipeline that detects changes (passive
// fsnotify or in-process record_change), routes them through a per-repo
// rate-limited router with delta-only guardrails, and surgically
// re-derives the symbol tier so subsequent MCP reads return fresh
// answers with honest freshness metadata. Default off through Phase 1
// burn-in; flipped at the end of Phase 1.E.
//
// Example (config.toml):
//
//	[change_watch]
//	enabled                = false
//	debounce_ms            = 2000
//	rate_limit_per_min     = 30
//	repo_breaker_per_min   = 60
//	t0_budget_ms           = 100
type ChangeWatchConfig struct {
	// Enabled is the umbrella feature flag that gates the watcher and
	// the router. When false (the Phase 1.C default), the watcher does
	// not start and the router accepts no events. The freshness
	// envelope on MCP responses is independent of this flag — it ships
	// enabled (additive metadata) so MCP consumers can rely on the
	// contract from day one.
	// SOURCEBRIDGE_CHANGE_WATCH_ENABLED.
	Enabled bool `mapstructure:"enabled"`

	// DebounceMs is the per-repo debounce window for fsnotify events.
	// Default 2000ms (Balanced mode). Phase 4 makes this per-repo
	// configurable via the repository-mode column (Fast=500ms,
	// Balanced=2000ms, Strict=30000ms); for 1.C only the Balanced
	// default is wired. SOURCEBRIDGE_CHANGE_WATCH_DEBOUNCE_MS.
	DebounceMs int `mapstructure:"debounce_ms"`

	// RateLimitPerMin caps router events per (repository, source.kind)
	// per minute. Default 30. Per-kind throttle applied alongside the
	// per-repo aggregate breaker below.
	// SOURCEBRIDGE_CHANGE_WATCH_RATE_LIMIT_PER_MIN.
	RateLimitPerMin int `mapstructure:"rate_limit_per_min"`

	// RepoBreakerPerMin trips the per-repo aggregate circuit breaker
	// (across all source.kind values combined) when sustained traffic
	// stays above this rate for 5 consecutive minutes. Default 60.
	// SOURCEBRIDGE_CHANGE_WATCH_REPO_BREAKER_PER_MIN.
	RepoBreakerPerMin int `mapstructure:"repo_breaker_per_min"`

	// T0BudgetMs is the hard ceiling for synchronous T0 refresh on read
	// (the IndexFiles call invoked from the read path when the requested
	// file is dirty). Beyond this budget the router serves current data
	// with freshness.partial_refresh=true. Default 100ms.
	// SOURCEBRIDGE_CHANGE_WATCH_T0_BUDGET_MS.
	T0BudgetMs int `mapstructure:"t0_budget_ms"`
}

// ConnectorAPIConfig controls the public HTTP ingress for change-watch
// connectors (Phase 1.D of the MCP-edits plan). The endpoint at
// POST /v1/connectors/{id}/events accepts ChangeEvent payloads from
// external connectors (GitHub, GitLab, custom) and dispatches them to
// the same in-process router as the fsnotify watcher and the
// record_change MCP tool.
//
// Environment variable prefix: SOURCEBRIDGE_CONNECTOR_API_*
//
// The endpoint is **internal/unstable** through Phase 1; the schema
// promotion to 1.0 (and operator-facing public docs) ship in Phase 2
// after the schema-stability checkpoint passes. Default off through
// Phase 1; flipped at the end of Phase 2 burn-in.
//
// Example (config.toml):
//
//	[connector_api]
//	enabled = false
type ConnectorAPIConfig struct {
	// Enabled gates the public HTTP ingress. When false (the Phase 1
	// default) the route returns 404 just like any other unregistered
	// endpoint — connectors that probe the path see no fingerprint of
	// the SourceBridge install.
	// SOURCEBRIDGE_CONNECTOR_API_ENABLED.
	Enabled bool `mapstructure:"enabled"`
}

// ShutdownConfig controls the graceful-drain behaviour on SIGTERM.
// CA-142: mirrors the worker's drain pattern so in-flight Living Wiki
// cold-start jobs (and any long-running LLM orchestrator job) can
// complete before the pod is SIGKILLed.
//
// Environment variable: SOURCEBRIDGE_SHUTDOWN_GRACE_SECONDS
// (Viper maps [shutdown].grace_seconds → SOURCEBRIDGE_SHUTDOWN_GRACE_SECONDS
// via the dot→underscore rule and the SOURCEBRIDGE_ prefix.)
//
// The Kubernetes terminationGracePeriodSeconds on the API Deployment
// MUST exceed this value by at least preStop-sleep + 60 s of slack.
// With grace_seconds=3600 the manifest sets 3900 s (65 min).
//
// Example (config.toml):
//
//	[shutdown]
//	grace_seconds = 3600
type ShutdownConfig struct {
	// GraceSeconds is the upper bound the process waits for in-flight
	// LLM jobs to complete after SIGTERM before force-exiting.
	// Default 3600 (60 min) — matches the Living Wiki cold-start
	// time budget. Zero means "wait forever" (not recommended for
	// production; kubelet's SIGKILL is the outer bound).
	// SOURCEBRIDGE_SHUTDOWN_GRACE_SECONDS.
	GraceSeconds int `mapstructure:"grace_seconds"`
}

// Defaults returns a Config with all default values.
func Defaults() *Config {
	return &Config{
		Env: "production",
		Server: ServerConfig{
			HTTPPort:      8080,
			GRPCPort:      50051,
			PublicBaseURL: "http://localhost:8080",
			CORSOrigins:   []string{"http://localhost:3000"},
			MaxBodySize:   10 * 1024 * 1024, // 10MB
		},
		Storage: StorageConfig{
			SurrealMode:      "embedded",
			SurrealURL:       "ws://localhost:8000/rpc",
			SurrealNamespace: "sourcebridge",
			SurrealDatabase:  "sourcebridge",
			SurrealUser:      "root",
			SurrealPass:      "root",
			SurrealDataPath:  "./surrealdb-data",
			RedisMode:        "memory",
			RepoCachePath:    "./repo-cache",
		},
		Indexing: IndexingConfig{
			MaxFileSize:    1024 * 1024, // 1MB
			IgnoreGlobs:    []string{"node_modules/**", "dist/**", ".git/**", "vendor/**", "__pycache__/**"},
			MaxConcurrency: 8,
			SCIPEnabled:    true,
		},
		LLM: LLMConfig{
			// Provider and model fields are intentionally empty so fresh
			// installs seed a blank Default profile rather than an Anthropic
			// profile the user hasn't configured (RC-1 fix — see
			// thoughts/shared/investigations/2026-05-05-deliver-fresh-install-llm-onboarding.md).
			// The profile editor falls back to "ollama" when provider is empty,
			// giving users a sensible starting point without implying they have
			// Anthropic credentials.
			// To restore the prior Anthropic default, set
			// SOURCEBRIDGE_LLM_PROVIDER=anthropic in your config.
			Provider:                 "",
			SummaryModel:             "",
			ReviewModel:              "",
			AskModel:                 "",
			ArchitectureDiagramModel: "",
			// 900s (15 min) covers any single LLM call from the slowest local
			// models we've measured. The prior 30s default was ignored
			// downstream anyway; operators can tune via the admin UI.
			TimeoutSecs: 900,
		},
		Linking: LinkingConfig{
			MinConfidenceUI:        0.5,
			MinConfidenceCodeLens:  0.7,
			MinConfidencePRComment: 0.8,
			InvalidateGraceHours:   24, // 24h grace before dependent links transition to invalidated
		},
		UI: UIConfig{
			Theme:          "dark",
			AccentHue:      250,
			OverlayDefault: true,
		},
		Security: SecurityConfig{
			JWTTTLMinutes: 1440, // 24 hours
			CSRFEnabled:   true,
			Mode:          "oss",
		},
		Worker: WorkerConfig{
			Address: "localhost:50051",
		},
		MCP: MCPConfig{
			Enabled:     false,
			SessionTTL:  3600, // 1 hour
			Keepalive:   30,   // 30 seconds
			MaxSessions: 100,
		},
		Trash: TrashConfig{
			Enabled:          true,
			RetentionDays:    30,
			SweepIntervalSec: 6 * 3600,
			MaxBatchSize:     500,
		},
		QA: QAConfig{
			ServerSideEnabled:         false, // default-off through Phase 4
			LocalFastModeSubprocess:   true,
			QuestionMaxBytes:          4096,
			SessionTokensPerHour:      100_000,
			RepoTokensPerDay:          1_000_000,
			DeploymentTokensPerDay:    10_000_000,
			SynthesisLane:             4,
			AgenticRetrievalEnabled:   false, // default-off through Phase 3
			AgenticRetrievalCanaryPct: 0,
			PromptCachingEnabled:      true,  // Anthropic-safe default
			SmartClassifierEnabled:    false, // default-off through quality-push Phase 5
			QueryDecompositionEnabled: false, // default-off through quality-push Phase 5
		},
		LivingWiki: LivingWikiConfig{
			Enabled:                    false, // opt-in; teams enable when ready to ship the wiki
			WorkerCount:                4,
			EventTimeout:               "5m",
			SchedulerInterval:          "15m",
			MaxConcurrentJobsPerTenant: 5,
		},
		ChangeWatch: ChangeWatchConfig{
			Enabled:           false, // umbrella flag default-off through Phase 1 burn-in
			DebounceMs:        2000,  // Balanced default; Phase 4 wires the per-repo mode column
			RateLimitPerMin:   30,    // per-(repo, source.kind) throttle
			RepoBreakerPerMin: 60,    // per-repo aggregate breaker (5min sustained)
			T0BudgetMs:        100,   // T0 sync-refresh-on-read hard ceiling
		},
		ConnectorAPI: ConnectorAPIConfig{
			Enabled: false, // public HTTP ingress default-off through Phase 1; flipped end of Phase 2
		},
		Shutdown: ShutdownConfig{
			// 3600s = 60 minutes — matches the Living Wiki cold-start time budget
			// (coldStartTimeBudget in internal/api/graphql/living_wiki_coldstart.go).
			// terminationGracePeriodSeconds on the API Deployment is set to 3900s
			// (grace_seconds + preStop-sleep + 60s slack). CA-142.
			GraceSeconds: 3600,
		},
	}
}

// Load reads configuration from file, env vars, and defaults.
func Load() (*Config, error) {
	cfg := Defaults()

	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("toml")
	v.AddConfigPath(".")
	v.AddConfigPath("$HOME/.config/sourcebridge")
	v.AddConfigPath("/etc/sourcebridge")

	// Environment variable mapping
	v.SetEnvPrefix("SOURCEBRIDGE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Set defaults so Viper knows about nested keys for env binding
	v.SetDefault("env", cfg.Env)
	v.SetDefault("edition", cfg.Edition)
	v.SetDefault("server.http_port", cfg.Server.HTTPPort)
	v.SetDefault("server.grpc_port", cfg.Server.GRPCPort)
	v.SetDefault("server.public_base_url", cfg.Server.PublicBaseURL)
	v.SetDefault("server.max_body_size", cfg.Server.MaxBodySize)
	v.SetDefault("storage.surreal_mode", cfg.Storage.SurrealMode)
	v.SetDefault("storage.surreal_url", cfg.Storage.SurrealURL)
	v.SetDefault("storage.surreal_namespace", cfg.Storage.SurrealNamespace)
	v.SetDefault("storage.surreal_database", cfg.Storage.SurrealDatabase)
	v.SetDefault("storage.surreal_user", cfg.Storage.SurrealUser)
	v.SetDefault("storage.surreal_pass", cfg.Storage.SurrealPass)
	v.SetDefault("storage.surreal_data_path", cfg.Storage.SurrealDataPath)
	v.SetDefault("storage.redis_mode", cfg.Storage.RedisMode)
	v.SetDefault("storage.redis_url", cfg.Storage.RedisURL)
	v.SetDefault("storage.repo_cache_path", cfg.Storage.RepoCachePath)
	v.SetDefault("llm.provider", cfg.LLM.Provider)
	v.SetDefault("llm.base_url", cfg.LLM.BaseURL)
	v.SetDefault("llm.api_key", "")
	v.SetDefault("llm.summary_model", cfg.LLM.SummaryModel)
	v.SetDefault("llm.architecture_diagram_model", cfg.LLM.ArchitectureDiagramModel)
	v.SetDefault("llm.report_model", cfg.LLM.ReportModel)
	v.SetDefault("llm.timeout_seconds", cfg.LLM.TimeoutSecs)
	v.SetDefault("security.jwt_secret", "")
	v.SetDefault("security.jwt_secret_file", "")
	v.SetDefault("security.grpc_auth_secret", "")
	v.SetDefault("security.jwt_ttl_minutes", cfg.Security.JWTTTLMinutes)
	v.SetDefault("security.encryption_key", "")
	v.SetDefault("security.csrf_enabled", cfg.Security.CSRFEnabled)
	v.SetDefault("security.mode", cfg.Security.Mode)
	v.SetDefault("security.api_token_legacy_admin_default", false)
	v.SetDefault("security.github_webhook_secret", "")
	v.SetDefault("security.gitlab_webhook_secret", "")
	v.SetDefault("security.oidc.issuer_url", "")
	v.SetDefault("security.oidc.client_id", "")
	v.SetDefault("security.oidc.client_secret", "")
	v.SetDefault("security.oidc.redirect_url", "")
	v.SetDefault("worker.address", cfg.Worker.Address)
	v.SetDefault("worker.tls.enabled", false)
	v.SetDefault("worker.tls.cert_path", "")
	v.SetDefault("worker.tls.key_path", "")
	v.SetDefault("worker.tls.ca_path", "")
	v.SetDefault("worker.tls.server_name", "worker.sourcebridge.svc.cluster.local")
	v.SetDefault("git.default_token", "")
	v.SetDefault("git.ssh_key_path", "")
	v.SetDefault("git.ssh_key_path_root", "") // empty → resolution.DefaultSSHKeyPathRoot
	v.SetDefault("mcp.enabled", cfg.MCP.Enabled)
	v.SetDefault("mcp.repos", cfg.MCP.Repos)
	v.SetDefault("mcp.session_ttl", cfg.MCP.SessionTTL)
	v.SetDefault("mcp.keepalive", cfg.MCP.Keepalive)
	v.SetDefault("mcp.max_sessions", cfg.MCP.MaxSessions)
	v.SetDefault("trash.enabled", cfg.Trash.Enabled)
	v.SetDefault("trash.retention_days", cfg.Trash.RetentionDays)
	v.SetDefault("trash.sweep_interval_sec", cfg.Trash.SweepIntervalSec)
	v.SetDefault("trash.max_batch_size", cfg.Trash.MaxBatchSize)
	v.SetDefault("qa.server_side_enabled", cfg.QA.ServerSideEnabled)
	v.SetDefault("qa.local_fast_mode_subprocess", cfg.QA.LocalFastModeSubprocess)
	v.SetDefault("qa.question_max_bytes", cfg.QA.QuestionMaxBytes)
	v.SetDefault("qa.session_tokens_per_hour", cfg.QA.SessionTokensPerHour)
	v.SetDefault("qa.repo_tokens_per_day", cfg.QA.RepoTokensPerDay)
	v.SetDefault("qa.deployment_tokens_per_day", cfg.QA.DeploymentTokensPerDay)
	v.SetDefault("qa.synthesis_lane", cfg.QA.SynthesisLane)
	v.SetDefault("qa.agentic_retrieval_enabled", cfg.QA.AgenticRetrievalEnabled)
	v.SetDefault("qa.agentic_retrieval_canary_pct", cfg.QA.AgenticRetrievalCanaryPct)
	v.SetDefault("qa.prompt_caching_enabled", cfg.QA.PromptCachingEnabled)
	v.SetDefault("qa.smart_classifier_enabled", cfg.QA.SmartClassifierEnabled)
	v.SetDefault("qa.query_decomposition_enabled", cfg.QA.QueryDecompositionEnabled)
	v.SetDefault("living_wiki.enabled", cfg.LivingWiki.Enabled)
	v.SetDefault("living_wiki.worker_count", cfg.LivingWiki.WorkerCount)
	v.SetDefault("living_wiki.event_timeout", cfg.LivingWiki.EventTimeout)
	v.SetDefault("living_wiki.confluence_webhook_secret", "")
	v.SetDefault("living_wiki.notion_webhook_secret", "")
	v.SetDefault("living_wiki.scheduler_interval", cfg.LivingWiki.SchedulerInterval)
	v.SetDefault("living_wiki.max_concurrent_jobs_per_tenant", cfg.LivingWiki.MaxConcurrentJobsPerTenant)
	v.SetDefault("change_watch.enabled", cfg.ChangeWatch.Enabled)
	v.SetDefault("change_watch.debounce_ms", cfg.ChangeWatch.DebounceMs)
	v.SetDefault("change_watch.rate_limit_per_min", cfg.ChangeWatch.RateLimitPerMin)
	v.SetDefault("change_watch.repo_breaker_per_min", cfg.ChangeWatch.RepoBreakerPerMin)
	v.SetDefault("change_watch.t0_budget_ms", cfg.ChangeWatch.T0BudgetMs)
	v.SetDefault("connector_api.enabled", cfg.ConnectorAPI.Enabled)
	v.SetDefault("linking.invalidate_grace_hours", cfg.Linking.InvalidateGraceHours)
	v.SetDefault("shutdown.grace_seconds", cfg.Shutdown.GraceSeconds)

	// Try reading config file (not required)
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("error reading config: %w", err)
		}
	}

	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("error parsing config: %w", err)
	}

	// CA-311: resolve JWT secret with file priority, falling through to
	// literal env, then auto-generating an in-memory secret if neither is
	// set. The previous "dev-secret-change-in-production" literal fallback
	// is removed — Validate() now refuses to start with a short or weak
	// secret. See ResolveJWTSecret for the full priority chain.
	resolvedJWT, jwtSource, err := cfg.Security.ResolveJWTSecret()
	if err != nil {
		return nil, fmt.Errorf("resolving JWT secret: %w", err)
	}
	if resolvedJWT == "" {
		generated, genErr := generateRandomJWTSecret()
		if genErr != nil {
			return nil, fmt.Errorf("auto-generating JWT secret: %w", genErr)
		}
		resolvedJWT = generated
		jwtSource = "auto-generated"
		slog.Info("JWT secret: auto-generated (in-memory only — does NOT persist; sessions invalidated on restart). For production / multi-replica, configure SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE or SOURCEBRIDGE_SECURITY_JWT_SECRET.",
			"length_bytes", len(resolvedJWT))
	} else {
		slog.Debug("JWT secret resolved", "source", jwtSource, "length_bytes", len(resolvedJWT))
	}
	cfg.Security.JWTSecret = resolvedJWT

	return cfg, nil
}

// generateRandomJWTSecret returns a 32-byte cryptographic-random secret
// hex-encoded as a 64-character ASCII string. The hex form is chosen so the
// returned string passes the ≥32-byte length gate trivially (64 ASCII bytes
// = 32 raw random bytes after decode) while remaining safe to use as the
// HMAC key directly (jwt.SigningMethodHS256 takes []byte and the entropy is
// preserved either way).
func generateRandomJWTSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := cryptorand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Validate checks the configuration for errors.
func (c *Config) Validate() error {
	if c.Server.HTTPPort <= 0 || c.Server.HTTPPort > 65535 {
		return fmt.Errorf("invalid HTTP port: %d", c.Server.HTTPPort)
	}
	// CA-311: refuse to start with a JWT secret shorter than 32 bytes. The
	// auto-generated path produces a 64-hex-char string (32 raw bytes), so
	// this only fires when an operator explicitly configures a too-short
	// secret via SOURCEBRIDGE_SECURITY_JWT_SECRET / _FILE / config.toml.
	if len(c.Security.JWTSecret) < 32 {
		return fmt.Errorf("JWT secret is shorter than 32 bytes (got %d) — set SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE to a path containing a 32+ byte random secret (generate with: openssl rand -hex 32)", len(c.Security.JWTSecret))
	}
	if c.Storage.SurrealMode != "embedded" && c.Storage.SurrealMode != "external" {
		return fmt.Errorf("invalid SurrealDB mode: %s (must be 'embedded' or 'external')", c.Storage.SurrealMode)
	}
	if c.Storage.RedisMode != "memory" && c.Storage.RedisMode != "external" {
		return fmt.Errorf("invalid Redis mode: %s (must be 'memory' or 'external')", c.Storage.RedisMode)
	}
	// Empty provider is allowed — it means "not yet configured" (fresh install).
	// The migration seeds a blank Default profile; the admin UI pre-fills
	// "ollama" so the user picks from there. Any non-empty value must be known.
	validProviders := map[string]bool{"anthropic": true, "openai": true, "ollama": true, "vllm": true, "llama-cpp": true, "sglang": true, "lmstudio": true, "gemini": true, "openrouter": true}
	if c.LLM.Provider != "" && !validProviders[c.LLM.Provider] {
		return fmt.Errorf("invalid LLM provider: %s", c.LLM.Provider)
	}
	if (c.LLM.Provider == "ollama" || c.LLM.Provider == "vllm" || c.LLM.Provider == "llama-cpp" || c.LLM.Provider == "sglang" || c.LLM.Provider == "lmstudio") && c.LLM.BaseURL == "" {
		return fmt.Errorf("llm.base_url is required when provider is %s", c.LLM.Provider)
	}
	if c.Trash.Enabled {
		if c.Trash.RetentionDays < 1 || c.Trash.RetentionDays > 365 {
			return fmt.Errorf("invalid trash.retention_days: %d (must be 1..365)", c.Trash.RetentionDays)
		}
		if c.Trash.SweepIntervalSec < 60 {
			return fmt.Errorf("invalid trash.sweep_interval_sec: %d (must be >= 60)", c.Trash.SweepIntervalSec)
		}
		if c.Trash.MaxBatchSize < 1 || c.Trash.MaxBatchSize > 10000 {
			return fmt.Errorf("invalid trash.max_batch_size: %d (must be 1..10000)", c.Trash.MaxBatchSize)
		}
	}
	if c.QA.QuestionMaxBytes < 0 {
		return fmt.Errorf("invalid qa.question_max_bytes: %d (must be >= 0)", c.QA.QuestionMaxBytes)
	}
	if c.QA.SessionTokensPerHour < 0 || c.QA.RepoTokensPerDay < 0 || c.QA.DeploymentTokensPerDay < 0 {
		return fmt.Errorf("qa token budgets must be non-negative (0 disables)")
	}
	if c.QA.SynthesisLane < 0 {
		return fmt.Errorf("invalid qa.synthesis_lane: %d (must be >= 0)", c.QA.SynthesisLane)
	}
	if c.QA.AgenticRetrievalCanaryPct < 0 || c.QA.AgenticRetrievalCanaryPct > 100 {
		return fmt.Errorf("invalid qa.agentic_retrieval_canary_pct: %d (must be 0..100)", c.QA.AgenticRetrievalCanaryPct)
	}
	if c.Worker.TLS.Enabled {
		if c.Worker.TLS.CertPath == "" {
			return fmt.Errorf("worker.tls.cert_path is required when worker.tls.enabled is true")
		}
		if c.Worker.TLS.KeyPath == "" {
			return fmt.Errorf("worker.tls.key_path is required when worker.tls.enabled is true")
		}
		if c.Worker.TLS.CAPath == "" {
			return fmt.Errorf("worker.tls.ca_path is required when worker.tls.enabled is true")
		}
	}
	if c.ChangeWatch.DebounceMs < 0 {
		return fmt.Errorf("invalid change_watch.debounce_ms: %d (must be >= 0)", c.ChangeWatch.DebounceMs)
	}
	if c.ChangeWatch.RateLimitPerMin < 0 {
		return fmt.Errorf("invalid change_watch.rate_limit_per_min: %d (must be >= 0)", c.ChangeWatch.RateLimitPerMin)
	}
	if c.ChangeWatch.RepoBreakerPerMin < 0 {
		return fmt.Errorf("invalid change_watch.repo_breaker_per_min: %d (must be >= 0)", c.ChangeWatch.RepoBreakerPerMin)
	}
	if c.ChangeWatch.T0BudgetMs < 0 {
		return fmt.Errorf("invalid change_watch.t0_budget_ms: %d (must be >= 0)", c.ChangeWatch.T0BudgetMs)
	}
	if c.Linking.InvalidateGraceHours < 0 {
		return fmt.Errorf("invalid linking.invalidate_grace_hours: %d (must be >= 0; 0 disables the grace window)", c.Linking.InvalidateGraceHours)
	}
	return nil
}
