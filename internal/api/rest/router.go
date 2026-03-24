// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
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
	"github.com/sourcebridge/sourcebridge/internal/events"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
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

// Server is the HTTP API server.
type Server struct {
	cfg            *config.Config
	router         chi.Router
	localAuth      *auth.LocalAuth
	jwtMgr         *auth.JWTManager
	oidc           *auth.OIDCProvider
	store          graphstore.GraphStore
	knowledgeStore knowledge.KnowledgeStore
	worker         *worker.Client
	eventBus       *events.Bus
	tokenStore     auth.APITokenStore
	desktopAuth    DesktopAuthSessionStore
	gitConfigStore GitConfigStore               // persists git tokens/SSH config across restarts
	llmConfigStore LLMConfigStore               // persists LLM provider/model config across restarts
	enterpriseDB   interface{}                  // *surrealdb.DB when available, type-asserted in enterprise_routes.go
	repoChecker    middleware.RepoAccessChecker // set by enterprise build to enable tenant repo filtering
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
		tokenStore:  auth.NewAPITokenStore(),
		desktopAuth: NewMemoryDesktopAuthStore(),
	}
	for _, opt := range opts {
		opt(s)
	}
	s.setupRouter()
	return s
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
		Resolvers: &graphql.Resolver{Store: s.store, KnowledgeStore: s.knowledgeStore, Worker: s.worker, Config: s.cfg, EventBus: s.eventBus, GitConfig: s.gitConfigStore},
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

		// LLM configuration
		r.Get("/api/v1/admin/llm-config", s.handleGetLLMConfig)
		r.Put("/api/v1/admin/llm-config", s.handleUpdateLLMConfig)

		// Git configuration
		r.Get("/api/v1/admin/git-config", s.handleGetGitConfig)
		r.Put("/api/v1/admin/git-config", s.handleUpdateGitConfig)

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
	})

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
