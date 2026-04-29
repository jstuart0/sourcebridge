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
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sourcebridge/sourcebridge/internal/api/rest"
	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/db"
	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/health"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/assembly"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/credentials"
	lwmetrics "github.com/sourcebridge/sourcebridge/internal/livingwiki/metrics"
	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/scheduler"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/webhook"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
	"github.com/sourcebridge/sourcebridge/internal/telemetry"
	"github.com/sourcebridge/sourcebridge/internal/qa"
	"github.com/sourcebridge/sourcebridge/internal/trash"
	"github.com/sourcebridge/sourcebridge/internal/version"
	"github.com/sourcebridge/sourcebridge/internal/worker"
	"github.com/sourcebridge/sourcebridge/internal/worker/llmcall"
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
	var jobStore llm.JobStore
	var comprehensionStore comprehension.Store
	var summaryNodeStore comprehension.SummaryNodeStore
	var lwStore livingwiki.Store
	var lwResolver *livingwiki.Resolver
	var lwRepoStore livingwiki.RepoSettingsStore
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
		jobStore = surrealStore
		comprehensionStore = surrealStore
		summaryNodeStore = surrealStore
		slog.Info("using SurrealDB-backed store (external mode)")
	} else {
		memCS := comprehension.NewMemStore()
		store = graph.NewStore()
		knowledgeStore = knowledge.NewMemStore()
		jobStore = llm.NewMemStore()
		comprehensionStore = memCS
		summaryNodeStore = memCS
		slog.Info("using in-memory store (embedded mode)")
	}

	// Initialize shared cache (memory by default, Redis when configured).
	// Consumed by the MCP session store for HA across replicas, and by
	// the trash retention worker for cross-replica leader election.
	cache := db.NewCache(cfg.Storage)

	// Trash (soft-delete recycle bin). Phase 1 requires external
	// SurrealDB — embedded mode falls back to nil (feature disabled).
	var trashStore trash.Store
	if cfg.Trash.Enabled && cfg.Storage.SurrealMode == "external" {
		trashStore = trash.NewSurrealStore(surrealDB)
		slog.Info("trash (recycle bin) enabled",
			"retention_days", cfg.Trash.RetentionDays,
			"sweep_interval_sec", cfg.Trash.SweepIntervalSec)

		// Retention worker runs in the background for the lifetime of
		// the server process. Leader election via Redis ensures only
		// one replica sweeps per tick when the cache is Redis-backed.
		worker := trash.NewWorker(trashStore, cache, trash.WorkerConfig{
			RetentionDays: cfg.Trash.RetentionDays,
			SweepInterval: time.Duration(cfg.Trash.SweepIntervalSec) * time.Second,
			MaxBatchSize:  cfg.Trash.MaxBatchSize,
		})
		go func() {
			if err := worker.Run(context.Background()); err != nil {
				slog.Error("trash retention worker exited", "error", err)
			}
		}()
	} else if cfg.Trash.Enabled {
		slog.Warn("trash is enabled in config but requires external SurrealDB; feature disabled",
			"storage.surreal_mode", cfg.Storage.SurrealMode)
	}

	knowledgeTimeoutProvider := func() time.Duration {
		// TimeoutSecs is for individual LLM completions (default 30s).
		// Knowledge generation is a multi-step pipeline that takes much
		// longer — use the dedicated constant (default 30 minutes).
		return worker.TimeoutKnowledgeRepository
	}

	// Initialize worker client (non-fatal if unavailable)
	var workerClient *worker.Client
	if cfg.Worker.Address != "" {
		wc, err := worker.New(cfg.Worker.Address, worker.WithKnowledgeTimeoutProvider(knowledgeTimeoutProvider))
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
	var queueControlStore rest.QueueControlStore
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

		// Load persisted LLM config store. The legacy boot-time merge
		// (where DB values were applied onto cfg.LLM and env vars
		// silently won) is gone — see plan
		// thoughts/shared/plans/2026-04-29-workspace-llm-source-of-truth.md.
		// cfg.LLM is now the env-bootstrap layer of the resolver, never
		// mutated post-boot. The resolver layer reads workspace settings
		// directly from the DB on every Resolve via a version-keyed
		// cache, so an admin save on replica A is visible to replica B
		// on the very next worker LLM call.
		lcs := db.NewSurrealLLMConfigStore(surrealDB)
		llmConfigStore = &llmConfigAdapter{store: lcs}
		queueControlStore = &queueControlAdapter{store: db.NewSurrealQueueControlStore(surrealDB)}

		// Living-wiki settings store + resolver (UI > env > default precedence).
		if cfg.Security.EncryptionKey == "" {
			slog.Warn("living-wiki: SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY not set; secrets stored in plaintext")
		}
		lwStore = db.NewLivingWikiSettingsStore(surrealDB, cfg.Security.EncryptionKey)
		lwRepoStore = db.NewLivingWikiRepoSettingsStore(surrealDB)
		lwEnv := livingwiki.EnvConfig{
			Enabled:                 cfg.LivingWiki.Enabled,
			WorkerCount:             cfg.LivingWiki.WorkerCount,
			EventTimeout:            cfg.LivingWiki.EventTimeout,
			ConfluenceWebhookSecret: cfg.LivingWiki.ConfluenceWebhookSecret,
			NotionWebhookSecret:     cfg.LivingWiki.NotionWebhookSecret,
		}
		lwResolver = livingwiki.NewResolver(lwStore, lwEnv, 0)
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

	// Living-wiki dispatcher construction.
	//
	// Kill-switch: check env var before any DB state so operators can disable
	// the feature instantly in an incident without a config change or restart.
	var lwDispatcher *webhook.Dispatcher
	killSwitch := strings.EqualFold(os.Getenv("SOURCEBRIDGE_LIVING_WIKI_KILL_SWITCH"), "true")
	if killSwitch {
		slog.Warn("living-wiki kill-switch active (SOURCEBRIDGE_LIVING_WIKI_KILL_SWITCH=true); dispatcher not started")
	} else if cfg.Storage.SurrealMode == "external" && lwResolver != nil {
		resolved, resolveErr := lwResolver.Get()
		if resolveErr != nil {
			slog.Warn("living-wiki resolver error; dispatcher not started", "error", resolveErr)
		} else if resolved != nil && resolved.Enabled {
			broker := credentials.NewResolverBroker(lwResolver)

			// Parse EventTimeout from the config string (e.g. "5m").
			var eventTimeout time.Duration
			if cfg.LivingWiki.EventTimeout != "" {
				if d, err := time.ParseDuration(cfg.LivingWiki.EventTimeout); err == nil {
					eventTimeout = d
				}
			}

			d, assembleErr := assembly.AssembleDispatcher(assembly.AssemblerDeps{
				SurrealDB:    surrealDB,
				GraphStore:   store,
				WorkerClient: workerClient,
				Broker:       broker,
				Logger:       &slogLivingWikiLogger{},
				WorkerCount:  cfg.LivingWiki.WorkerCount,
				EventTimeout: eventTimeout,
			})
			if assembleErr != nil {
				slog.Error("living-wiki assembly failed; dispatcher not started", "error", assembleErr)
			} else {
				if startErr := d.Start(context.Background()); startErr != nil {
					slog.Error("living-wiki dispatcher failed to start", "error", startErr)
				} else {
					lwDispatcher = d
					slog.Info("living-wiki dispatcher started",
						"worker_count", cfg.LivingWiki.WorkerCount,
						"event_timeout", cfg.LivingWiki.EventTimeout,
					)
				}
			}
		} else {
			slog.Info("living-wiki globally disabled; dispatcher not started")
		}
	}

	// Living-wiki job result store (R8).
	// Constructed regardless of kill-switch status so the GraphQL resolver can
	// query past results even when the dispatcher is not running.
	var lwJobResultStore livingwiki.JobResultStore
	if cfg.Storage.SurrealMode == "external" {
		lwJobResultStore = db.NewLivingWikiJobResultStore(surrealDB)
	}

	// Living-wiki metrics collector (R8).
	// Registered once at boot; the /metrics handler calls WritePrometheusText.
	_ = lwmetrics.Default // ensures the package-level collector is initialized

	// Living-wiki scheduler (R6).
	// Started after the dispatcher so the dispatcher is ready to accept events
	// on the first tick. schedCancel is always non-nil; it is a no-op when the
	// scheduler was not started, so the defer below is unconditionally safe.
	schedCtx, schedCancel := context.WithCancel(context.Background())
	defer schedCancel() // ensure goroutine is cancelled on any exit path
	if lwDispatcher != nil && lwRepoStore != nil {
		var schedInterval time.Duration
		if cfg.LivingWiki.SchedulerInterval != "" {
			if d, err := time.ParseDuration(cfg.LivingWiki.SchedulerInterval); err == nil && d > 0 {
				schedInterval = d
			}
		}
		sched := scheduler.New(scheduler.SchedulerDeps{
			RepoStore:   lwRepoStore,
			Dispatcher:  lwDispatcher,
			Cache:       cache,
			Interval:    schedInterval,
			MaxParallel: cfg.LivingWiki.MaxConcurrentJobsPerTenant,
			TenantID:    "default",
		})
		go func() {
			if err := sched.Run(schedCtx); err != nil && err != context.Canceled {
				slog.Error("living-wiki scheduler exited unexpectedly", "error", err)
			}
		}()
		slog.Info("living-wiki scheduler started",
			"interval", cfg.LivingWiki.SchedulerInterval,
			"max_parallel", cfg.LivingWiki.MaxConcurrentJobsPerTenant,
		)
	}

	// Build the shared health checker. In external mode, pass the SurrealDB
	// instance as the pinger so /readyz and serviceHealth do a real round-trip.
	// In embedded/in-memory mode, pass nil so the checker treats DB as healthy
	// without attempting a WebSocket connection.
	var healthChecker *health.Checker
	if cfg.Storage.SurrealMode == "external" {
		healthChecker = health.New(surrealDB, workerClient)
	} else {
		healthChecker = health.New(nil, workerClient)
	}

	// Build the runtime LLM-config resolver. cfg.LLM is the env-bootstrap
	// layer; the workspace store is layer 2. Per-repo override (slice 5)
	// adds layer 1 by passing a non-nil RepoOverrideStore. Resolver runs
	// in every replica — version-keyed cache makes admin saves visible
	// cross-replica without polling or restart.
	var llmStoreAdapter resolution.LLMConfigStore
	if cfg.Storage.SurrealMode == "external" && surrealDB != nil {
		llmStoreAdapter = &llmStoreResolverAdapter{store: db.NewSurrealLLMConfigStore(surrealDB)}
	}
	llmResolver := resolution.New(llmStoreAdapter, nil /* per-repo override store, slice 5 */, cfg.LLM, slog.Default())

	// Build the LLM-aware adapter around the worker client. Every gRPC
	// LLM RPC must flow through this Caller so resolved metadata is
	// attached to the outgoing context. The AST lint test in
	// internal/llm/resolution/lint_test.go enforces that.
	var llmCaller *llmcall.Caller
	if workerClient != nil {
		llmCaller = llmcall.New(workerClient, llmResolver, slog.Default())
	}

	// Create HTTP server
	server := rest.NewServer(cfg, localAuth, jwtMgr, store, workerClient,
		rest.WithEnterpriseDB(surrealDB.DB()),
		rest.WithHealthChecker(healthChecker),
		rest.WithKnowledgeStore(knowledgeStore),
		rest.WithJobStore(jobStore),
		rest.WithGitConfigStore(gitConfigStore),
		rest.WithLLMConfigStore(llmConfigStore),
		rest.WithLLMResolver(llmResolver),
		rest.WithLLMCaller(llmCaller),
		rest.WithQueueControlStore(queueControlStore),
		rest.WithTokenStore(tokenStore),
		rest.WithDesktopAuthStore(desktopAuthStore),
		rest.WithComprehensionStore(comprehensionStore),
		rest.WithSummaryNodeStore(summaryNodeStore),
		rest.WithCache(cache),
		rest.WithTrashStore(trashStore),
		rest.WithLivingWikiStore(lwStore),
		rest.WithLivingWikiResolver(lwResolver),
		rest.WithLivingWikiRepoStore(lwRepoStore),
		rest.WithLivingWikiDispatcher(lwDispatcher),
		rest.WithLivingWikiJobResultStore(lwJobResultStore),
		rest.WithLivingWikiLiveOrchestrator(func() *lworch.Orchestrator {
			if lwDispatcher == nil {
				return nil
			}
			return lwDispatcher.LiveOrchestrator()
		}()),
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

	// Start anonymous telemetry (opt-out via SOURCEBRIDGE_TELEMETRY=off)
	dataDir := cfg.Storage.RepoCachePath
	if dataDir == "" {
		dataDir = "/data"
	}
	tracker := telemetry.New(
		version.Version,
		cfg.Edition,
		dataDir,
		telemetry.WithLLMProviderKind(classifyTelemetryLLMProviderKind(cfg.LLM.Provider)),
		telemetry.WithCountProvider(&telemetryCountProvider{store: store}),
	)
	tracker.Start()
	defer tracker.Stop()

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
	if err := server.Shutdown(ctx); err != nil {
		return fmt.Errorf("server component shutdown error: %w", err)
	}

	// schedCancel() is called by defer above, ensuring the scheduler goroutine
	// exits after the dispatcher has drained.
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

type queueControlAdapter struct {
	store *db.SurrealQueueControlStore
}

func (a *llmConfigAdapter) LoadLLMConfig() (*rest.LLMConfigRecord, error) {
	rec, err := a.store.LoadLLMConfig()
	if err != nil || rec == nil {
		return nil, err
	}
	return &rest.LLMConfigRecord{
		Provider:                 rec.Provider,
		BaseURL:                  rec.BaseURL,
		APIKey:                   rec.APIKey,
		SummaryModel:             rec.SummaryModel,
		ReviewModel:              rec.ReviewModel,
		AskModel:                 rec.AskModel,
		KnowledgeModel:           rec.KnowledgeModel,
		ArchitectureDiagramModel: rec.ArchitectureDiagramModel,
		ReportModel:              rec.ReportModel,
		DraftModel:               rec.DraftModel,
		TimeoutSecs:              rec.TimeoutSecs,
		AdvancedMode:             rec.AdvancedMode,
	}, nil
}

func (a *llmConfigAdapter) SaveLLMConfig(rec *rest.LLMConfigRecord) error {
	return a.store.SaveLLMConfig(&db.LLMConfigRecord{
		Provider:                 rec.Provider,
		BaseURL:                  rec.BaseURL,
		APIKey:                   rec.APIKey,
		SummaryModel:             rec.SummaryModel,
		ReviewModel:              rec.ReviewModel,
		AskModel:                 rec.AskModel,
		KnowledgeModel:           rec.KnowledgeModel,
		ArchitectureDiagramModel: rec.ArchitectureDiagramModel,
		ReportModel:              rec.ReportModel,
		DraftModel:               rec.DraftModel,
		TimeoutSecs:              rec.TimeoutSecs,
		AdvancedMode:             rec.AdvancedMode,
	})
}

// llmStoreResolverAdapter bridges *db.SurrealLLMConfigStore to the narrow
// resolution.LLMConfigStore interface. The resolver does not import
// internal/db (which would create a cycle), so the adapter lives here.
type llmStoreResolverAdapter struct {
	store *db.SurrealLLMConfigStore
}

func (a *llmStoreResolverAdapter) LoadLLMConfig() (*resolution.WorkspaceRecord, error) {
	rec, err := a.store.LoadLLMConfig()
	if err != nil || rec == nil {
		return nil, err
	}
	return &resolution.WorkspaceRecord{
		Provider:                 rec.Provider,
		BaseURL:                  rec.BaseURL,
		APIKey:                   rec.APIKey,
		SummaryModel:             rec.SummaryModel,
		ReviewModel:              rec.ReviewModel,
		AskModel:                 rec.AskModel,
		KnowledgeModel:           rec.KnowledgeModel,
		ArchitectureDiagramModel: rec.ArchitectureDiagramModel,
		ReportModel:              rec.ReportModel,
		DraftModel:               rec.DraftModel,
		TimeoutSecs:              rec.TimeoutSecs,
		AdvancedMode:             rec.AdvancedMode,
		Version:                  rec.Version,
	}, nil
}

func (a *llmStoreResolverAdapter) LoadLLMConfigVersion() (uint64, error) {
	return a.store.LoadLLMConfigVersion()
}

func (a *queueControlAdapter) LoadQueueControl() (*rest.QueueControlRecord, error) {
	rec, err := a.store.LoadQueueControl()
	if err != nil || rec == nil {
		return nil, err
	}
	return &rest.QueueControlRecord{
		IntakePaused: rec.IntakePaused,
	}, nil
}

func (a *queueControlAdapter) SaveQueueControl(rec *rest.QueueControlRecord) error {
	return a.store.SaveQueueControl(&db.QueueControlRecord{
		IntakePaused: rec.IntakePaused,
	})
}

// telemetryCountProvider provides aggregate counts from the graph store.
type telemetryCountProvider struct {
	store graph.GraphStore
}

func (p *telemetryCountProvider) TelemetryCounts() (repos, users int, features []string, counts map[string]int) {
	if p.store == nil {
		return 0, 0, nil, nil
	}
	allRepos := p.store.ListRepositories()
	repos = len(allRepos)

	var totalFiles, totalSymbols int
	for _, r := range allRepos {
		totalFiles += r.FileCount
		totalSymbols += r.FunctionCount + r.ClassCount
	}

	counts = map[string]int{
		"total_files":        totalFiles,
		"total_symbols":      totalSymbols,
		"qa_asks_total_14d":  qa.AsksTotal14d(),
	}

	// Merge in trash (recycle bin) counters. These are process-start
	// cumulative; safe to read even when the feature is disabled
	// (atomics default to zero).
	for k, v := range trash.Counters() {
		counts[k] = v
	}

	if qa.ServerSideEnabled() {
		features = append(features, "qa_server_side")
	}

	return repos, 0, features, counts
}

func classifyTelemetryLLMProviderKind(provider string) string {
	switch provider {
	case "ollama", "vllm", "llama-cpp", "sglang", "lmstudio":
		return "local"
	case "":
		return ""
	default:
		return "cloud"
	}
}

// slogLivingWikiLogger bridges webhook.Logger to the global slog logger.
type slogLivingWikiLogger struct{}

func (l *slogLivingWikiLogger) Info(msg string, kv ...any) {
	slog.Info(msg, kv...)
}

func (l *slogLivingWikiLogger) Error(msg string, err error, kv ...any) {
	args := append([]any{"error", err}, kv...)
	slog.Error(msg, args...)
}
