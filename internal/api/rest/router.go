// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httprate"

	"github.com/sourcebridge/sourcebridge/internal/api/graphql"
	"github.com/sourcebridge/sourcebridge/internal/api/middleware"
	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/db"
	"github.com/sourcebridge/sourcebridge/internal/events"
	"github.com/sourcebridge/sourcebridge/internal/featureflags"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
	"github.com/sourcebridge/sourcebridge/internal/trash"
	"github.com/sourcebridge/sourcebridge/internal/worker"
)

// ServerOption configures optional Server parameters.
type ServerOption func(*Server)

// WithEnterpriseDB passes a raw database handle for enterprise store persistence.
// The value should be a *surrealdb.DB; it is stored as interface{} to avoid
// importing the SurrealDB SDK in OSS builds.
func WithEnterpriseDB(db interface{}) ServerOption {
	return func(s *Server) { s.enterpriseDB = db }
}

// WithTokenStore overrides API token/session persistence.
func WithTokenStore(store auth.APITokenStore) ServerOption {
	return func(s *Server) { s.tokenStore = store }
}

// WithDesktopAuthStore overrides desktop auth session persistence.
func WithDesktopAuthStore(store DesktopAuthSessionStore) ServerOption {
	return func(s *Server) { s.desktopAuth = store }
}

// WithKnowledgeStore sets the knowledge persistence store.
func WithKnowledgeStore(ks knowledge.KnowledgeStore) ServerOption {
	return func(s *Server) { s.knowledgeStore = ks }
}

// WithJobStore sets the persistent llm.JobStore used by the orchestrator.
// When unset, the server falls back to an in-memory store — which is
// fine for tests and the OSS quickstart, but means job history is lost
// on restart. Production deployments should pass the SurrealDB-backed
// store created via db.NewSurrealStore.
func WithJobStore(js llm.JobStore) ServerOption {
	return func(s *Server) { s.jobStore = js }
}

// WithRepoChecker sets the tenant repo access checker for multi-tenant filtering.
func WithRepoChecker(rc middleware.RepoAccessChecker) ServerOption {
	return func(s *Server) { s.repoChecker = rc }
}

// WithGitConfigStore enables persistent storage of git credentials.
func WithGitConfigStore(store GitConfigStore) ServerOption {
	return func(s *Server) { s.gitConfigStore = store }
}

// WithLLMConfigStore enables persistent storage of LLM configuration.
func WithLLMConfigStore(store LLMConfigStore) ServerOption {
	return func(s *Server) { s.llmConfigStore = store }
}

// WithQueueControlStore enables persisted LLM queue intake controls.
func WithQueueControlStore(store QueueControlStore) ServerOption {
	return func(s *Server) { s.queueControlStore = store }
}

// WithMCPPermissionChecker sets the enterprise MCP permission checker.
func WithMCPPermissionChecker(pc MCPPermissionChecker) ServerOption {
	return func(s *Server) { s.mcpPermChecker = pc }
}

// WithMCPAuditLogger sets the enterprise MCP audit logger.
func WithMCPAuditLogger(al MCPAuditLogger) ServerOption {
	return func(s *Server) { s.mcpAuditLogger = al }
}

// WithMCPToolExtender sets the enterprise MCP tool extender.
func WithMCPToolExtender(te MCPToolExtender) ServerOption {
	return func(s *Server) { s.mcpToolExtender = te }
}

// WithComprehensionStore injects the comprehension settings and model
// capabilities store into the server.
func WithComprehensionStore(cs comprehension.Store) ServerOption {
	return func(s *Server) { s.comprehensionStore = cs }
}

// WithSummaryNodeStore injects the summary node persistence store.
func WithSummaryNodeStore(sns comprehension.SummaryNodeStore) ServerOption {
	return func(s *Server) { s.summaryNodeStore = sns }
}

// WithCache injects a shared KV cache (memory or Redis). The MCP session
// store uses this to persist streamable-HTTP session state across replicas
// when a Redis-backed cache is provided.
func WithCache(c db.Cache) ServerOption {
	return func(s *Server) { s.cache = c }
}

// WithTrashStore wires the soft-delete recycle bin. Callers pass nil
// to run without the feature (embedded mode, or when trash is disabled
// in config).
func WithTrashStore(ts trash.Store) ServerOption {
	return func(s *Server) { s.trashStore = ts }
}

// Server is the HTTP API server.
type Server struct {
	cfg                *config.Config
	router             chi.Router
	localAuth          *auth.LocalAuth
	jwtMgr             *auth.JWTManager
	oidc               *auth.OIDCProvider
	store              graphstore.GraphStore
	knowledgeStore     knowledge.KnowledgeStore
	jobStore           llm.JobStore               // persistent store for llm.Job records; defaults to MemStore
	orchestrator       *orchestrator.Orchestrator // shared LLM job orchestrator (created in NewServer)
	worker             *worker.Client
	eventBus           *events.Bus
	flags              featureflags.Flags
	tokenStore         auth.APITokenStore
	desktopAuth        DesktopAuthSessionStore
	gitConfigStore     GitConfigStore                 // persists git tokens/SSH config across restarts
	llmConfigStore     LLMConfigStore                 // persists LLM provider/model config across restarts
	queueControlStore  QueueControlStore              // persists queue intake controls across restarts
	enterpriseDB       interface{}                    // *surrealdb.DB when available, type-asserted in enterprise_routes.go
	repoChecker        middleware.RepoAccessChecker   // set by enterprise build to enable tenant repo filtering
	mcp                *mcpHandler                    // MCP protocol handler (nil when disabled)
	mcpPermChecker     MCPPermissionChecker           // deferred to mcp handler at setup
	mcpAuditLogger     MCPAuditLogger                 // deferred to mcp handler at setup
	mcpToolExtender    MCPToolExtender                // deferred to mcp handler at setup
	comprehensionStore comprehension.Store            // comprehension settings + model capabilities
	summaryNodeStore   comprehension.SummaryNodeStore // cached summary tree nodes
	cache              db.Cache                       // shared KV cache (memory or Redis); nil = MCP session store falls back to in-memory
	trashStore         trash.Store                    // soft-delete recycle bin; nil = feature disabled
}

// getStore returns a tenant-filtered store when RepoAccessMiddleware has
// injected one, otherwise returns the base store.
func (s *Server) getStore(r *http.Request) graphstore.GraphStore {
	if filtered := middleware.StoreFromContext(r.Context()); filtered != nil {
		return filtered
	}
	return s.store
}

// lazyRepoAccessMiddleware applies tenant repo filtering when a repoChecker
// is configured. It reads s.repoChecker at request time (not at router setup
// time) because enterprise initialization happens after the protected route
// group is defined.
func (s *Server) lazyRepoAccessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.repoChecker == nil {
			next.ServeHTTP(w, r)
			return
		}
		middleware.RepoAccessMiddleware(s.store, s.repoChecker)(next).ServeHTTP(w, r)
	})
}

// NewServer creates a new HTTP server with all routes.
func NewServer(cfg *config.Config, localAuth *auth.LocalAuth, jwtMgr *auth.JWTManager, store graphstore.GraphStore, workerClient *worker.Client, opts ...ServerOption) *Server {
	if store == nil {
		store = graphstore.NewStore()
	}
	s := &Server{
		cfg:         cfg,
		localAuth:   localAuth,
		jwtMgr:      jwtMgr,
		store:       store,
		worker:      workerClient,
		eventBus:    events.NewBus(),
		flags:       featureflags.LoadFromEnv(),
		tokenStore:  auth.NewAPITokenStore(),
		desktopAuth: NewMemoryDesktopAuthStore(),
	}
	for _, opt := range opts {
		opt(s)
	}

	// Fall back to an in-memory job store when none was supplied via
	// WithJobStore. This keeps the OSS quickstart and tests working
	// without a SurrealDB dependency; production callers should supply
	// the SurrealStore via WithJobStore.
	if s.jobStore == nil {
		s.jobStore = llm.NewMemStore()
	}
	// Build the orchestrator from config, with sensible defaults if the
	// comprehension section is absent (zero-value Config uses the
	// package defaults — max_concurrency=3, 5s/30s retry, etc.).
	orchCfg := orchestrator.Config{}
	if cfg != nil && cfg.Comprehension.MaxConcurrency > 0 {
		orchCfg.MaxConcurrency = cfg.Comprehension.MaxConcurrency
	}
	// When the reaper marks a stale job as failed, also mark the linked
	// knowledge artifact as failed so the UI doesn't show "generating"
	// forever on a job that will never complete.
	orchCfg.OnStaleJob = func(job *llm.Job) {
		if s.knowledgeStore != nil && job.ArtifactID != "" {
			_ = s.knowledgeStore.SetArtifactFailed(job.ArtifactID, "DEADLINE_EXCEEDED", "Generation timed out — please retry")
		}
	}
	s.orchestrator = orchestrator.New(s.jobStore, orchCfg)
	if s.queueControlStore != nil {
		if rec, err := s.queueControlStore.LoadQueueControl(); err == nil && rec != nil {
			s.orchestrator.SetIntakePaused(rec.IntakePaused)
		}
	}
	slog.Info("backend feature flags", "enabled", s.flags.EnabledNames())

	s.setupRouter()
	return s
}

// Orchestrator returns the server's LLM job orchestrator. Exposed so
// tests and the graceful-shutdown path can call Shutdown on it.
func (s *Server) Orchestrator() *orchestrator.Orchestrator {
	return s.orchestrator
}

// SetOIDCProvider configures the OIDC provider for SSO login.
func (s *Server) SetOIDCProvider(o *auth.OIDCProvider) {
	s.oidc = o
}

func (s *Server) setupRouter() {
	r := chi.NewRouter()

	// Global middleware
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(securityHeaders)
	r.Use(metricsMiddleware)

	// CORS
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   s.cfg.Server.CORSOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// Rate limiting
	r.Use(httprate.LimitByIP(100, 1*time.Minute))

	// Public routes
	r.Get("/healthz", s.handleHealthz)
	r.Get("/readyz", s.handleReadyz)
	r.Get("/metrics", s.handleMetrics)

	// Auth routes (rate limited more strictly)
	r.Group(func(r chi.Router) {
		r.Use(httprate.LimitByIP(10, 1*time.Minute))
		r.Post("/auth/setup", s.handleSetup)
		r.Post("/auth/login", s.handleLogin)
	})

	// Auth info endpoint (tells frontend which auth methods are available)
	r.Get("/auth/info", s.handleAuthInfo)
	r.Get("/auth/desktop/info", s.handleDesktopAuthInfo)
	r.Post("/auth/desktop/local-login", s.handleDesktopLocalLogin)
	r.Post("/auth/desktop/oidc/start", s.handleDesktopOIDCStart)
	r.Get("/auth/desktop/oidc/poll", s.handleDesktopOIDCPoll)
	r.Post("/auth/logout", s.handleLogout)

	// OIDC routes
	r.Get("/auth/oidc/login", s.handleOIDCLogin)
	r.Get("/auth/oidc/callback", s.handleOIDCCallback)

	// Change password requires authentication
	r.Group(func(r chi.Router) {
		r.Use(httprate.LimitByIP(10, 1*time.Minute))
		r.Use(auth.Middleware(s.jwtMgr))
		r.Post("/auth/change-password", s.handleChangePassword)
	})

	// GraphQL server
	gqlSrv := handler.NewDefaultServer(graphql.NewExecutableSchema(graphql.Config{
		Resolvers: &graphql.Resolver{Store: s.store, KnowledgeStore: s.knowledgeStore, Worker: s.worker, Orchestrator: s.orchestrator, Config: s.cfg, EventBus: s.eventBus, Flags: s.flags, GitConfig: s.gitConfigStore, ComprehensionStore: s.comprehensionStore, TrashStore: s.trashStore},
	}))

	// Protected API routes (accepts both JWT and API tokens)
	r.Group(func(r chi.Router) {
		r.Use(auth.MiddlewareWithTokens(s.jwtMgr, s.tokenStore))
		// Tenant repo filtering — repoChecker is set by registerEnterpriseRoutes
		// (after this group is defined), so we read it lazily at request time.
		r.Use(s.lazyRepoAccessMiddleware)
		if s.cfg.Security.CSRFEnabled {
			r.Use(csrfProtectionWithName(s.jwtMgr.CSRFCookieName()))
		}

		// CSRF token endpoint
		r.Get("/api/v1/csrf-token", s.handleCSRFToken)

		// GraphQL endpoint (with AI concurrency control)
		r.With(graphqlCountMiddleware, aiConcurrencyMiddleware).Handle("/api/v1/graphql", gqlSrv)

		// SSE events
		r.Get("/api/v1/events", s.handleSSE)
	})

	// Admin API routes (requires auth, accepts both JWT and API tokens)
	r.Group(func(r chi.Router) {
		r.Use(auth.MiddlewareWithTokens(s.jwtMgr, s.tokenStore))
		r.Use(s.lazyRepoAccessMiddleware)
		r.Get("/api/v1/admin/status", s.handleAdminStatus)
		r.Get("/api/v1/admin/config", s.handleAdminConfig)
		r.Put("/api/v1/admin/config", s.handleAdminUpdateConfig)
		r.Post("/api/v1/admin/test-worker", s.handleAdminTestWorker)
		r.Post("/api/v1/admin/test-llm", s.handleAdminTestLLM)
		r.Get("/api/v1/admin/knowledge", s.handleAdminKnowledgeStatus)

		// LLM job monitor (Phase 2c)
		r.Get("/api/v1/admin/llm/activity", s.handleLLMActivity)
		r.Get("/api/v1/admin/llm/stream", s.handleLLMStream)
		r.Get("/api/v1/admin/llm/control", s.handleGetQueueControl)
		r.Put("/api/v1/admin/llm/control", s.handleUpdateQueueControl)
		r.Post("/api/v1/admin/llm/drain", s.handleDrainQueue)
		r.Get("/api/v1/admin/llm/jobs/{id}", s.handleLLMJobDetail)
		r.Get("/api/v1/admin/llm/jobs/{id}/logs", s.handleLLMJobLogs)
		r.Get("/api/v1/admin/llm/jobs/{id}/logs/stream", s.handleLLMJobLogStream)
		r.Post("/api/v1/admin/llm/jobs/{id}/cancel", s.handleLLMJobCancel)
		r.Post("/api/v1/admin/llm/jobs/{id}/retry", s.handleLLMJobRetry)

		// LLM configuration
		r.Get("/api/v1/admin/llm-config", s.handleGetLLMConfig)
		r.Put("/api/v1/admin/llm-config", s.handleUpdateLLMConfig)
		r.Get("/api/v1/admin/llm-models", s.handleListLLMModels)

		// Git configuration
		r.Get("/api/v1/admin/git-config", s.handleGetGitConfig)
		r.Put("/api/v1/admin/git-config", s.handleUpdateGitConfig)

		// Comprehension settings (Phase 6)
		r.Get("/api/v1/admin/comprehension/settings", s.handleListComprehensionSettings)
		r.Get("/api/v1/admin/comprehension/settings/effective", s.handleGetEffectiveComprehensionSettings)
		r.Put("/api/v1/admin/comprehension/settings", s.handleUpdateComprehensionSettings)
		r.Delete("/api/v1/admin/comprehension/settings", s.handleResetComprehensionSettings)

		// Model capabilities (Phase 6)
		r.Get("/api/v1/admin/comprehension/models", s.handleListModelCapabilities)
		r.Get("/api/v1/admin/comprehension/models/{modelId}", s.handleGetModelCapabilities)
		r.Put("/api/v1/admin/comprehension/models", s.handleUpdateModelCapabilities)
		r.Delete("/api/v1/admin/comprehension/models/{modelId}", s.handleDeleteModelCapabilities)

		// Summary node cache (Phase 7)
		r.Get("/api/v1/admin/llm/corpus/{corpusId}/nodes", s.handleGetSummaryNodes)
		r.Put("/api/v1/admin/llm/corpus/nodes", s.handleStoreSummaryNodes)
		r.Post("/api/v1/admin/llm/corpus/{corpusId}/invalidate", s.handleInvalidateSummaryNodes)

		// Reports — enterprise only (registered via enterprise routes)

		// API token management
		r.Post("/api/v1/tokens", s.handleCreateToken)
		r.Get("/api/v1/tokens", s.handleListTokens)
		r.Get("/api/v1/tokens/current", s.handleCurrentToken)
		r.Post("/api/v1/tokens/revoke-user", s.handleRevokeUserTokens)
		r.Delete("/api/v1/tokens/{id}", s.handleRevokeToken)
		r.Post("/api/v1/tokens/current/revoke", s.handleRevokeCurrentToken)
		r.Post("/api/v1/telemetry", s.handleTelemetryEvent)

		// Data export
		r.Get("/api/v1/export/traceability", s.handleExportTraceability)
		r.Get("/api/v1/export/requirements", s.handleExportRequirements)
		r.Get("/api/v1/export/symbols", s.handleExportSymbols)
		r.Get("/api/v1/export/knowledge/{id}", s.handleExportKnowledgeArtifact)

		// Diagram document API (structured architecture diagrams — read-only in OSS)
		r.Get("/api/v1/diagrams/{repoId}", s.handleGetDiagramDocument)
		r.Get("/api/v1/diagrams/{repoId}/structured", s.handleGetStructuredDiagram)
		r.Put("/api/v1/diagrams/{repoId}", s.handlePutDiagramDocument)
		r.Delete("/api/v1/diagrams/{repoId}", s.handleDeleteDiagramDocument)
		r.Post("/api/v1/diagrams/{repoId}/import", s.handleImportMermaid)
		r.Get("/api/v1/diagrams/{repoId}/export/mermaid", s.handleExportDiagramMermaid)
		r.Get("/api/v1/diagrams/{repoId}/export/json", s.handleExportDiagramJSON)
	})

	// MCP (Model Context Protocol) routes
	if s.cfg.MCP.Enabled {
		sessionTTL := time.Duration(s.cfg.MCP.SessionTTL) * time.Second
		keepalive := time.Duration(s.cfg.MCP.Keepalive) * time.Second
		s.mcp = newMCPHandler(s.store, s.knowledgeStore, s.worker, s.cfg.MCP.Repos, sessionTTL, keepalive, s.cfg.MCP.MaxSessions, s.cache)
		// Wire enterprise extensions if provided via server options
		if s.mcpPermChecker != nil {
			s.mcp.permChecker = s.mcpPermChecker
		}
		if s.mcpAuditLogger != nil {
			s.mcp.auditLogger = s.mcpAuditLogger
		}
		if s.mcpToolExtender != nil {
			s.mcp.toolExtender = s.mcpToolExtender
		}
		// SSE endpoint: behind auth (JWT or API token)
		r.Group(func(r chi.Router) {
			r.Use(auth.MiddlewareWithTokens(s.jwtMgr, s.tokenStore))
			r.Get("/api/v1/mcp/sse", s.mcp.handleSSE)
		})
		// Message endpoint: session-based auth (no JWT middleware — session owns auth)
		r.Post("/api/v1/mcp/message", s.mcp.handleMessage)
		// Streamable HTTP transport: auth on every request (for Codex, etc.)
		r.Group(func(r chi.Router) {
			r.Use(auth.MiddlewareWithTokens(s.jwtMgr, s.tokenStore))
			r.Post("/api/v1/mcp/http", s.mcp.handleStreamableHTTP)
			r.Delete("/api/v1/mcp/http", s.mcp.handleStreamableHTTPDelete)
		})
		slog.Info("mcp server enabled", "max_sessions", s.cfg.MCP.MaxSessions, "session_ttl", sessionTTL, "keepalive", keepalive)
	}

	// Enterprise routes (no-op in OSS builds, registered when built with -tags enterprise)
	s.registerEnterpriseRoutes(r)

	// GraphQL playground (development only, no auth required)
	if s.cfg.IsDevelopment() {
		r.Get("/api/v1/playground", playground.Handler("SourceBridge", "/api/v1/graphql"))
	}

	s.router = r
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.router
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.eventBus != nil {
		if err := s.eventBus.Shutdown(ctx); err != nil {
			return err
		}
	}
	if s.orchestrator != nil {
		graceful := 5 * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			if remaining := time.Until(deadline); remaining > 0 {
				graceful = remaining
			}
		}
		if err := s.orchestrator.Shutdown(graceful); err != nil {
			return err
		}
	}
	return nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; form-action 'self' https:")
		next.ServeHTTP(w, r)
	})
}
