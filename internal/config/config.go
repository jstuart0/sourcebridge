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
	Env       string          `mapstructure:"env"`     // development, production
	Edition   string          `mapstructure:"edition"` // oss, enterprise
	Server    ServerConfig    `mapstructure:"server"`
	Storage   StorageConfig   `mapstructure:"storage"`
	Indexing  IndexingConfig  `mapstructure:"indexing"`
	LLM       LLMConfig      `mapstructure:"llm"`
	Linking   LinkingConfig   `mapstructure:"linking"`
	UI        UIConfig        `mapstructure:"ui"`
	Security  SecurityConfig  `mapstructure:"security"`
	Worker    WorkerConfig    `mapstructure:"worker"`
	Git       GitConfig       `mapstructure:"git"`
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
	Provider      string `mapstructure:"provider"`
	BaseURL       string `mapstructure:"base_url"`
	APIKey        string `mapstructure:"api_key"`
	SummaryModel  string `mapstructure:"summary_model"`
	ReviewModel   string `mapstructure:"review_model"`
	AskModel      string `mapstructure:"ask_model"`
	TimeoutSecs   int    `mapstructure:"timeout_seconds"`
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
	JWTSecret            string     `mapstructure:"jwt_secret"`
	JWTTTLMinutes        int        `mapstructure:"jwt_ttl_minutes"`
	EncryptionKey        string     `mapstructure:"encryption_key"`
	CSRFEnabled          bool       `mapstructure:"csrf_enabled"`
	GRPCAuthSecret       string     `mapstructure:"grpc_auth_secret"`
	Mode                 string     `mapstructure:"mode"` // oss, commercial
	OIDC                 OIDCConfig `mapstructure:"oidc"`
	GitHubWebhookSecret  string     `mapstructure:"github_webhook_secret"`
	GitLabWebhookSecret  string     `mapstructure:"gitlab_webhook_secret"`
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
			Provider:     "anthropic",
			SummaryModel: "claude-sonnet-4-20250514",
			ReviewModel:  "claude-sonnet-4-20250514",
			AskModel:     "claude-sonnet-4-20250514",
			TimeoutSecs:  30,
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
	validProviders := map[string]bool{"anthropic": true, "openai": true, "ollama": true, "vllm": true}
	if !validProviders[c.LLM.Provider] {
		return fmt.Errorf("invalid LLM provider: %s", c.LLM.Provider)
	}
	if (c.LLM.Provider == "ollama" || c.LLM.Provider == "vllm") && c.LLM.BaseURL == "" {
		return fmt.Errorf("llm.base_url is required when provider is %s", c.LLM.Provider)
	}
	return nil
}
