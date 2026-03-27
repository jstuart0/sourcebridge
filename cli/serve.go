// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sourcebridge/sourcebridge/internal/api/rest"
	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/db"
	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/worker"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the SourceBridge.ai API server and web UI",
	RunE:  runServe,
}

var servePort int

func init() {
	serveCmd.Flags().IntVar(&servePort, "port", 0, "HTTP port (overrides config)")
}

func runServe(cmd *cobra.Command, args []string) error {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if servePort > 0 {
		cfg.Server.HTTPPort = servePort
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Initialize logger
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Connect to database
	surrealDB := db.NewSurrealDB(cfg.Storage)
	if err := surrealDB.Connect(context.Background()); err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	surrealDB.StartKeepalive()
	defer surrealDB.Close()

	// Choose the store implementation based on surreal mode.
	var store graph.GraphStore
	var knowledgeStore knowledge.KnowledgeStore
	if cfg.Storage.SurrealMode == "external" {
		// Run migrations against the external SurrealDB instance.
		migrationsDir := migrationsPath()
		slog.Info("running database migrations", "dir", migrationsDir)
		if err := surrealDB.Migrate(context.Background(), migrationsDir); err != nil {
			return fmt.Errorf("failed to run migrations: %w", err)
		}

		surrealStore := db.NewSurrealStore(surrealDB)
		store = surrealStore
		knowledgeStore = surrealStore
		slog.Info("using SurrealDB-backed store (external mode)")
	} else {
		store = graph.NewStore()
		knowledgeStore = knowledge.NewMemStore()
		slog.Info("using in-memory store (embedded mode)")
	}

	// Initialize cache
	_ = db.NewCache(cfg.Storage)

	// Initialize worker client (non-fatal if unavailable)
	var workerClient *worker.Client
	if cfg.Worker.Address != "" {
		wc, err := worker.New(cfg.Worker.Address)
		if err != nil {
			slog.Warn("failed to create worker client, AI features disabled", "error", err)
		} else {
			workerClient = wc
			defer workerClient.Close()
			slog.Info("worker client initialized", "address", cfg.Worker.Address)
		}
	} else {
		slog.Info("worker address not configured, AI features disabled")
	}

	// Initialize auth
	jwtMgr := auth.NewJWTManager(cfg.Security.JWTSecret, cfg.Security.JWTTTLMinutes, cfg.Edition)
	var authPersister auth.AuthPersister
	var gitConfigStore rest.GitConfigStore
	var llmConfigStore rest.LLMConfigStore
	var tokenStore auth.APITokenStore
	var oidcStateStore auth.OIDCStateStore
	var desktopAuthStore rest.DesktopAuthSessionStore
	if cfg.Storage.SurrealMode == "external" {
		authPersister = auth.NewSurrealPersister(surrealDB)
		tokenStore = auth.NewSurrealAPITokenStore(surrealDB)
		oidcStateStore = auth.NewSurrealOIDCStateStore(surrealDB)
		desktopAuthStore = rest.NewSurrealDesktopAuthStore(surrealDB)
		slog.Info("auth persistence enabled via SurrealDB")

		// Load persisted git config (DB values fill in where env vars are empty)
		gcs := db.NewSurrealGitConfigStore(surrealDB)
		gitConfigStore = gcs
		if token, sshKey, err := gcs.LoadGitConfig(); err == nil {
			if cfg.Git.DefaultToken == "" && token != "" {
				cfg.Git.DefaultToken = token
				slog.Info("loaded git token from database")
			}
			if cfg.Git.SSHKeyPath == "" && sshKey != "" {
				cfg.Git.SSHKeyPath = sshKey
				slog.Info("loaded git SSH key path from database")
			}
		}

		// Load persisted LLM config (DB values fill in where env vars are empty)
		lcs := db.NewSurrealLLMConfigStore(surrealDB)
		llmConfigStore = &llmConfigAdapter{store: lcs}
		if rec, err := lcs.LoadLLMConfig(); err == nil && rec != nil {
			if cfg.LLM.Provider == "anthropic" && rec.Provider != "" {
				// Only override defaults — if env var was explicitly set, it takes priority
				// The default provider is "anthropic", so if it's still the default, DB wins
				cfg.LLM.Provider = rec.Provider
			}
			if cfg.LLM.BaseURL == "" && rec.BaseURL != "" {
				cfg.LLM.BaseURL = rec.BaseURL
			}
			if cfg.LLM.APIKey == "" && rec.APIKey != "" {
				cfg.LLM.APIKey = rec.APIKey
			}
			if rec.SummaryModel != "" {
				cfg.LLM.SummaryModel = rec.SummaryModel
			}
			if rec.ReviewModel != "" {
				cfg.LLM.ReviewModel = rec.ReviewModel
			}
			if rec.AskModel != "" {
				cfg.LLM.AskModel = rec.AskModel
			}
			if rec.KnowledgeModel != "" {
				cfg.LLM.KnowledgeModel = rec.KnowledgeModel
			}
			if rec.DraftModel != "" {
				cfg.LLM.DraftModel = rec.DraftModel
			}
			if rec.TimeoutSecs > 0 {
				cfg.LLM.TimeoutSecs = rec.TimeoutSecs
			}
			cfg.LLM.AdvancedMode = rec.AdvancedMode
			slog.Info("loaded LLM config from database", "provider", cfg.LLM.Provider, "advanced_mode", cfg.LLM.AdvancedMode)
		}
	}
	if tokenStore == nil {
		tokenStore = auth.NewAPITokenStore()
	}
	if oidcStateStore == nil {
		oidcStateStore = auth.NewMemoryOIDCStateStore()
	}
	if desktopAuthStore == nil {
		desktopAuthStore = rest.NewMemoryDesktopAuthStore()
	}
	localAuth := auth.NewLocalAuth(jwtMgr, authPersister)

	// Create HTTP server
	server := rest.NewServer(cfg, localAuth, jwtMgr, store, workerClient,
		rest.WithEnterpriseDB(surrealDB.DB()),
		rest.WithKnowledgeStore(knowledgeStore),
		rest.WithGitConfigStore(gitConfigStore),
		rest.WithLLMConfigStore(llmConfigStore),
		rest.WithTokenStore(tokenStore),
		rest.WithDesktopAuthStore(desktopAuthStore),
	)

	// Initialize OIDC if configured
	if cfg.Security.OIDC.ClientID != "" && cfg.Security.OIDC.IssuerURL != "" {
		oidcProvider, err := auth.NewOIDCProvider(context.Background(), cfg.Security.OIDC, jwtMgr, oidcStateStore)
		if err != nil {
			slog.Warn("OIDC initialization failed, SSO disabled", "error", err)
		} else {
			server.SetOIDCProvider(oidcProvider)
			slog.Info("OIDC SSO enabled", "issuer", cfg.Security.OIDC.IssuerURL)
		}
	}

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.HTTPPort),
		Handler:      server.Handler(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 360 * time.Second, // Safety backstop for long AI operations; real timeouts are per-operation
		IdleTimeout:  120 * time.Second,
	}

	// Start server
	errCh := make(chan error, 1)
	go func() {
		slog.Info("starting server", "port", cfg.Server.HTTPPort, "url", cfg.Server.PublicBaseURL)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	if cleaner, ok := tokenStore.(auth.CleanupCapable); ok {
		go startAuthCleanupLoop("api_tokens", cleaner)
	}
	if cleaner, ok := oidcStateStore.(auth.CleanupCapable); ok {
		go startAuthCleanupLoop("oidc_states", cleaner)
	}
	if cleaner, ok := desktopAuthStore.(interface {
		Cleanup(context.Context) (int, error)
	}); ok {
		go startDesktopAuthCleanupLoop("desktop_auth_sessions", cleaner)
	}

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		slog.Info("received shutdown signal", "signal", sig)
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	}

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slog.Info("shutting down server")
	if err := httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown error: %w", err)
	}

	slog.Info("server stopped")
	return nil
}

func startAuthCleanupLoop(name string, cleaner auth.CleanupCapable) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		count, err := cleaner.Cleanup(ctx)
		cancel()
		if err != nil {
			slog.Warn("auth cleanup failed", "target", name, "error", err)
			continue
		}
		if count > 0 {
			slog.Info("auth cleanup completed", "target", name, "deleted", count)
		}
	}
}

func startDesktopAuthCleanupLoop(name string, cleaner interface {
	Cleanup(context.Context) (int, error)
}) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		count, err := cleaner.Cleanup(ctx)
		cancel()
		if err != nil {
			slog.Warn("auth cleanup failed", "target", name, "error", err)
			continue
		}
		if count > 0 {
			slog.Info("auth cleanup completed", "target", name, "deleted", count)
		}
	}
}

// migrationsPath returns the path to the database migrations directory.
// It first checks for a SOURCEBRIDGE_MIGRATIONS_DIR env var, then falls back
// to locating the directory relative to the binary.
func migrationsPath() string {
	if dir := os.Getenv("SOURCEBRIDGE_MIGRATIONS_DIR"); dir != "" {
		return dir
	}

	// Try /migrations (Docker container layout)
	if info, err := os.Stat("/migrations"); err == nil && info.IsDir() {
		return "/migrations"
	}

	// Try relative to the executable
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Join(filepath.Dir(exe), "migrations")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}

	// Try relative to the source (works during development)
	_, filename, _, ok := runtime.Caller(0)
	if ok {
		dir := filepath.Join(filepath.Dir(filename), "..", "internal", "db", "migrations")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}

	// Final fallback
	return "internal/db/migrations"
}

// llmConfigAdapter bridges db.SurrealLLMConfigStore and rest.LLMConfigStore
// to avoid a circular import between the db and rest packages.
type llmConfigAdapter struct {
	store *db.SurrealLLMConfigStore
}

func (a *llmConfigAdapter) LoadLLMConfig() (*rest.LLMConfigRecord, error) {
	rec, err := a.store.LoadLLMConfig()
	if err != nil || rec == nil {
		return nil, err
	}
	return &rest.LLMConfigRecord{
		Provider:       rec.Provider,
		BaseURL:        rec.BaseURL,
		APIKey:         rec.APIKey,
		SummaryModel:   rec.SummaryModel,
		ReviewModel:    rec.ReviewModel,
		AskModel:       rec.AskModel,
		KnowledgeModel: rec.KnowledgeModel,
		DraftModel:     rec.DraftModel,
		TimeoutSecs:    rec.TimeoutSecs,
		AdvancedMode:   rec.AdvancedMode,
	}, nil
}

func (a *llmConfigAdapter) SaveLLMConfig(rec *rest.LLMConfigRecord) error {
	return a.store.SaveLLMConfig(&db.LLMConfigRecord{
		Provider:       rec.Provider,
		BaseURL:        rec.BaseURL,
		APIKey:         rec.APIKey,
		SummaryModel:   rec.SummaryModel,
		ReviewModel:    rec.ReviewModel,
		AskModel:       rec.AskModel,
		KnowledgeModel: rec.KnowledgeModel,
		DraftModel:     rec.DraftModel,
		TimeoutSecs:    rec.TimeoutSecs,
		AdvancedMode:   rec.AdvancedMode,
	})
}
