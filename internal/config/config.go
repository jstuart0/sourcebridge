// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package config

import (
	"fmt"
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
type GitConfig struct {
	DefaultToken string `mapstructure:"default_token"` // PAT used when no per-repo token is provided
	SSHKeyPath   string `mapstructure:"ssh_key_path"`  // path to SSH private key for SSH URLs
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
	JWTTTLMinutes       int        `mapstructure:"jwt_ttl_minutes"`
	EncryptionKey       string     `mapstructure:"encryption_key"`
	CSRFEnabled         bool       `mapstructure:"csrf_enabled"`
	GRPCAuthSecret      string     `mapstructure:"grpc_auth_secret"`
	Mode                string     `mapstructure:"mode"` // oss, commercial
	OIDC                OIDCConfig `mapstructure:"oidc"`
	GitHubWebhookSecret string     `mapstructure:"github_webhook_secret"`
	GitLabWebhookSecret string     `mapstructure:"gitlab_webhook_secret"`
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
			Provider:                 "anthropic",
			SummaryModel:             "claude-sonnet-4-20250514",
			ReviewModel:              "claude-sonnet-4-20250514",
			AskModel:                 "claude-sonnet-4-20250514",
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
	v.SetDefault("security.jwt_ttl_minutes", cfg.Security.JWTTTLMinutes)
	v.SetDefault("security.mode", cfg.Security.Mode)
	v.SetDefault("security.oidc.issuer_url", "")
	v.SetDefault("security.oidc.client_id", "")
	v.SetDefault("security.oidc.client_secret", "")
	v.SetDefault("security.oidc.redirect_url", "")
	v.SetDefault("worker.address", cfg.Worker.Address)
	v.SetDefault("git.default_token", "")
	v.SetDefault("git.ssh_key_path", "")
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

	// Try reading config file (not required)
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("error reading config: %w", err)
		}
	}

	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("error parsing config: %w", err)
	}

	// Generate JWT secret if not set
	if cfg.Security.JWTSecret == "" {
		cfg.Security.JWTSecret = os.Getenv("SOURCEBRIDGE_SECURITY_JWT_SECRET")
		if cfg.Security.JWTSecret == "" {
			cfg.Security.JWTSecret = "dev-secret-change-in-production"
		}
	}

	return cfg, nil
}

// Validate checks the configuration for errors.
func (c *Config) Validate() error {
	if c.Server.HTTPPort <= 0 || c.Server.HTTPPort > 65535 {
		return fmt.Errorf("invalid HTTP port: %d", c.Server.HTTPPort)
	}
	if c.Storage.SurrealMode != "embedded" && c.Storage.SurrealMode != "external" {
		return fmt.Errorf("invalid SurrealDB mode: %s (must be 'embedded' or 'external')", c.Storage.SurrealMode)
	}
	if c.Storage.RedisMode != "memory" && c.Storage.RedisMode != "external" {
		return fmt.Errorf("invalid Redis mode: %s (must be 'memory' or 'external')", c.Storage.RedisMode)
	}
	validProviders := map[string]bool{"anthropic": true, "openai": true, "ollama": true, "vllm": true, "llama-cpp": true, "sglang": true, "lmstudio": true, "gemini": true, "openrouter": true}
	if !validProviders[c.LLM.Provider] {
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
	return nil
}
