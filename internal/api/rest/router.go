// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httprate"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	"github.com/sourcebridge/sourcebridge/internal/api/graphql"
	"github.com/sourcebridge/sourcebridge/internal/api/middleware"
	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/capabilities"
	"github.com/sourcebridge/sourcebridge/internal/clustering"
	"github.com/sourcebridge/sourcebridge/internal/indexing"
	"github.com/sourcebridge/sourcebridge/internal/installassets"
	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/db"
	"github.com/sourcebridge/sourcebridge/internal/events"
	"github.com/sourcebridge/sourcebridge/internal/featureflags"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/webhook"
	"github.com/sourcebridge/sourcebridge/internal/qa"
	"github.com/sourcebridge/sourcebridge/internal/search"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
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

// WithLivingWikiStore wires the living-wiki settings persistence store. When
// nil (embedded mode or external SurrealDB unavailable), the GraphQL resolvers
// return empty settings and the UI shows only env-var-sourced values.
func WithLivingWikiStore(store livingwiki.Store) ServerOption {
	return func(s *Server) { s.livingWikiStore = store }
}

// WithLivingWikiResolver wires the living-wiki settings resolver (UI + env
// fallback). When nil, the GraphQL TestLivingWikiConnection mutation is
// unavailable. Pass the resolver created alongside WithLivingWikiStore.
func WithLivingWikiResolver(r *livingwiki.Resolver) ServerOption {
	return func(s *Server) { s.livingWikiResolver = r }
}

// WithLivingWikiRepoStore wires the per-repo living-wiki opt-in store. When
// nil, the repository living-wiki mutations and query return unavailable errors.
func WithLivingWikiRepoStore(rs livingwiki.RepoSettingsStore) ServerOption {
	return func(s *Server) { s.livingWikiRepoStore = rs }
}

// WithLivingWikiDispatcher wires the living-wiki event dispatcher into the
// server. When non-nil, setupRouter registers /webhooks/confluence and
// /webhooks/notion-poll and routes them to the dispatcher. When nil (embedded
// mode, kill-switch active, or assembly failure), both routes are registered
// as stubs that return 503 so webhook senders receive a clear signal rather
// than a 404.
func WithLivingWikiDispatcher(d *webhook.Dispatcher) ServerOption {
	return func(s *Server) { s.livingWikiDispatcher = d }
}

// WithLivingWikiJobResultStore wires the per-run living-wiki job result store.
// When nil, the lastJobResult GraphQL field resolves to null.
func WithLivingWikiJobResultStore(rs livingwiki.JobResultStore) ServerOption {
	return func(s *Server) { s.livingWikiJobResultStore = rs }
}

// WithLivingWikiLiveOrchestrator wires the living-wiki page-generation
// orchestrator into the GraphQL resolver so the cold-start job goroutine
// (R5) can call Generate directly. When nil, cold-start jobs return a
// "orchestrator unavailable" notice without failing hard.
func WithLivingWikiLiveOrchestrator(o *lworch.Orchestrator) ServerOption {
	return func(s *Server) { s.livingWikiLiveOrchestrator = o }
}

// WithHealthChecker injects a shared HealthChecker used by both /readyz and
// the serviceHealth GraphQL query. Pass nil to skip the checker (embedded
// mode, tests), in which case both handlers fall back to lightweight checks.
func WithHealthChecker(hc *HealthChecker) ServerOption {
	return func(s *Server) { s.healthChecker = hc }
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
	qaOrchestrator     *qa.Orchestrator               // server-side deep-QA orchestrator; nil = server-side QA disabled
	workerLanes        *worker.Lanes                  // shared lane registry used by search + qa
	searchSvc          *search.Service                // hybrid retrieval backbone; always set in NewServer
	reqBooster         *search.RequirementBooster     // repo-scoped requirement link cache; feeds searchSvc boosters
	searchMetrics      *search.Metrics                // in-process ring buffer of per-stage latency / success
	livingWikiStore              livingwiki.Store              // living-wiki UI settings store; nil = feature unavailable
	livingWikiResolver           *livingwiki.Resolver          // merged living-wiki config (UI + env); nil = only env applies
	livingWikiRepoStore          livingwiki.RepoSettingsStore  // per-repo living-wiki opt-in; nil = feature unavailable
	livingWikiDispatcher         *webhook.Dispatcher           // nil = feature not started or kill-switch active
	livingWikiJobResultStore     livingwiki.JobResultStore     // nil = job result history unavailable
	livingWikiLiveOrchestrator   *lworch.Orchestrator          // living-wiki page-generation orchestrator; nil = feature unavailable
	clusterRunner                *clustering.Runner            // subsystem clustering job dispatcher; nil = feature disabled
	healthChecker                *HealthChecker                // shared DB+worker probe; nil = embedded/test mode, handlers fall back to local checks
}

// qaResolverOrchestrator exposes the server's QA orchestrator to the
// GraphQL resolver only when QA is enabled in config. Returning nil
// when the flag is off causes the ask mutation resolver to emit a
// structured "disabled" response, matching the REST handler's 503.
func (s *Server) qaResolverOrchestrator() *qa.Orchestrator {
	if s.cfg == nil || !s.cfg.QA.ServerSideEnabled {
		return nil
	}
	return s.qaOrchestrator
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
	// When the reaper marks a stale job as failed, also mark related
	// state as failed so the UI doesn't show "generating" forever on a
	// job that will never complete:
	//   - Knowledge artifacts get SetArtifactFailed.
	//   - Living-wiki cold-start / retry-excluded jobs get a failed
	//     LivingWikiJobResult written so the per-repo settings panel
	//     surfaces the timeout instead of returning lastJobResult: null.
	orchCfg.OnStaleJob = func(job *llm.Job) {
		if s.knowledgeStore != nil && job.ArtifactID != "" {
			_ = s.knowledgeStore.SetArtifactFailed(job.ArtifactID, "DEADLINE_EXCEEDED", "Generation timed out — please retry")
		}
		if s.livingWikiJobResultStore != nil && job.Subsystem == "living_wiki" {
			persistStaleLivingWikiResult(s.livingWikiJobResultStore, job)
		}
	}
	s.orchestrator = orchestrator.New(s.jobStore, orchCfg)
	if s.queueControlStore != nil {
		if rec, err := s.queueControlStore.LoadQueueControl(); err == nil && rec != nil {
			s.orchestrator.SetIntakePaused(rec.IntakePaused)
		}
	}

	// Worker lanes — shared by search.embed and qa.synthesize so they
	// don't starve each other under load.
	s.workerLanes = worker.NewLanes()
	if cfg != nil && cfg.QA.SynthesisLane > 0 {
		s.workerLanes.Register(worker.NewLane(worker.LaneQASynthesize, cfg.QA.SynthesisLane))
	}

	// Hybrid retrieval service. One instance per process, shared by
	// every transport adapter (MCP, GraphQL, REST, CLI) and by the
	// agentic search_evidence tool. Must be constructed BEFORE the QA
	// orchestrator so WithSearcher wires correctly; the worker
	// embedder is attached below once the worker client is in scope.
	s.searchSvc = search.NewService(s.store)
	s.searchMetrics = search.NewMetrics(0)
	s.searchSvc.Metrics = s.searchMetrics
	if s.worker != nil {
		emb := search.NewWorkerEmbedder(s.worker, "")
		cached := search.NewCachedEmbedder(emb, 2048, 5*time.Minute, 5, 30*time.Second)
		s.searchSvc.WithEmbedder(cached)
	}

	// Server-side QA orchestrator. Default off until Phase 5 flips
	// QAConfig.ServerSideEnabled. The handler also double-checks the
	// flag so operators can disable cleanly without a restart.
	if cfg != nil {
		askModel := cfg.LLM.AskModel
		qaOrchCfg := qa.Config{
			QuestionMaxBytes:          cfg.QA.QuestionMaxBytes,
			AskModel:                  askModel,
			PromptCachingEnabled:      cfg.QA.PromptCachingEnabled,
			SmartClassifierEnabled:    cfg.QA.SmartClassifierEnabled,
			QueryDecompositionEnabled: cfg.QA.QueryDecompositionEnabled,
		}
		var reader qa.UnderstandingReader
		if s.knowledgeStore != nil && s.summaryNodeStore != nil {
			reader = qaUnderstandingReader{knowledge: s.knowledgeStore, summaries: s.summaryNodeStore}
		}
		o := qa.New(s.worker, reader, s.workerLanes, qaOrchCfg)
		if s.store != nil {
			locator := newQARepoLocator(s.store, cfg.Storage.RepoCachePath)
			o = o.WithRepoLocator(locator)
			o = o.WithGraphExpander(qa.NewGraphExpander(&qaGraphAdapter{store: s.store}, &qaGraphLookup{store: s.store}))
			o = o.WithRequirementLookup(&qaRequirementLookup{store: s.store})
			o = o.WithSymbolLookup(&qaSymbolLookup{store: s.store})
			o = o.WithFileReader(&qaFileReader{locator: locator})
		}
		if s.knowledgeStore != nil {
			o = o.WithArtifactLookup(&qaArtifactLookup{store: s.knowledgeStore})
		}
		if s.orchestrator != nil {
			o = o.WithJobRunner(&qaJobRunner{orch: s.orchestrator})
		}
		if s.searchSvc != nil {
			o = o.WithSearcher(&qaSearcher{svc: s.searchSvc})
		}
		// Agentic path — only wires when the provider supports tool
		// use (probed at startup). When the probe fails or returns
		// unsupported, the orchestrator stays on single-shot
		// regardless of AgenticRetrievalEnabled.
		//
		// Probe with retry: the API and the worker come up in parallel
		// under a rolling deploy. If the API pod wins the race, the
		// first probe fails with "connection refused" and the pod
		// stays single-shot for its entire lifetime while the sibling
		// pod (probed seconds later) is wired. We retry for up to 30s
		// so both pods converge to the same capability state. A
		// single run against two pods with split agentic-enablement
		// corrupts benchmarks and produces flaky prod traffic.
		if s.worker != nil {
			var caps *reasoningv1.GetProviderCapabilitiesResponse
			var err error
			for attempt := 1; attempt <= 6; attempt++ {
				caps, err = s.worker.GetProviderCapabilities(context.Background())
				if err == nil {
					break
				}
				slog.Warn("agent synth: capability probe attempt failed; retrying",
					"attempt", attempt, "error", err)
				time.Sleep(5 * time.Second)
			}
			if err != nil {
				slog.Warn("agent synth: provider capability probe failed after retries; agentic disabled",
					"error", err)
			} else if caps.GetToolUseSupported() {
				agent := qa.NewWorkerAgentSynthesizer(s.worker, true)
				o = o.WithAgentSynthesizer(agent).
					WithAgenticEnabled(cfg.QA.AgenticRetrievalEnabled).
					WithAgenticCanaryPct(cfg.QA.AgenticRetrievalCanaryPct)
				// Smart classifier is wired alongside agentic since
				// both rely on the Anthropic provider surface. It
				// still honors its own SmartClassifierEnabled flag
				// at runtime, so wiring here is harmless when off.
				o = o.WithQuestionProfiler(qa.NewWorkerQuestionProfiler(s.worker))
				// Quality-push Phase 4: decomposer + synthesizer wire
				// through to the same worker. Orchestrator honours
				// QueryDecompositionEnabled at runtime so wiring here
				// is harmless when the flag is off.
				o = o.WithDecomposer(qa.NewWorkerDecomposer(s.worker), s.worker)
				slog.Info("agent synth: wired",
					"provider", caps.GetProvider(),
					"model", caps.GetModel(),
					"enabled", cfg.QA.AgenticRetrievalEnabled,
					"canary_pct", cfg.QA.AgenticRetrievalCanaryPct,
					"smart_classifier", cfg.QA.SmartClassifierEnabled,
					"query_decomposition", cfg.QA.QueryDecompositionEnabled,
					"prompt_caching", cfg.QA.PromptCachingEnabled)
			} else {
				slog.Info("agent synth: provider does not support tool use; agentic disabled",
					"provider", caps.GetProvider(), "model", caps.GetModel())
			}
		}
		s.qaOrchestrator = o
		// Publish the server-side QA state to the telemetry counters
		// so the public dashboard can track adoption without collecting
		// any request content. Counts: process-local ring buffer of
		// ask invocations over 14 UTC days (qa.CountAsk is called from
		// Orchestrator.Ask).
		qa.SetServerSideEnabled(cfg.QA.ServerSideEnabled)
	}

	slog.Info("backend feature flags", "enabled", s.flags.EnabledNames())

	// Requirement booster is attached late — it depends on the store
	// being constructed above and doesn't change the QA wiring path.
	s.reqBooster = &search.RequirementBooster{Store: s.store}
	s.searchSvc.WithRequirementBooster(s.reqBooster)

	// Subsystem clustering runner. The store is cast to ClusterStore only
	// when it satisfies the interface (SurrealStore does; the in-memory
	// graph.Store does not yet — it will satisfy it after Sprint 1 tests
	// wire a lightweight adapter, or it can be left nil for tests).
	if cs, ok := s.store.(clustering.ClusterStore); ok {
		s.clusterRunner = clustering.NewRunner(cs, clustering.NewOrchestratorDispatcher(s.orchestrator))
	}

	s.setupRouter()
	return s
}

// Orchestrator returns the server's LLM job orchestrator. Exposed so
// tests and the graceful-shutdown path can call Shutdown on it.
func (s *Server) Orchestrator() *orchestrator.Orchestrator {
	return s.orchestrator
}

// clusteringHookFunc returns a post-index hook that enqueues a clustering job,
// or nil when clustering is not configured (no ClusterStore-compatible store).
func (s *Server) clusteringHookFunc() func(repoID, commitSHA string) {
	if s.clusterRunner == nil {
		return nil
	}
	return s.clusterRunner.EnqueueForRepo
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

	// Install script — serves the embedded SourceBridge installer at the
	// origin so users can run:
	//   curl -fsSL https://<this-server>/install.sh | sh -s -- --server <this-server>
	// The script downloads binaries from the upstream GitHub releases, not
	// from this server. See internal/installassets/install.sh for the source
	// of truth (also reachable as scripts/install.sh via a symlink).
	r.Get("/install.sh", installassets.Handler())

	// Auth routes (rate limited more strictly — 10 req/min per IP covers all
	// credential-submission and desktop-auth endpoints. The /auth/desktop/info
	// probe is included here for budget clarity; it's read-only but is used as a
	// TOFU probe against /auth/desktop/oidc/poll so keeping all four desktop
	// endpoints on the same counter keeps the math obvious. See the security
	// hardening plan (2026-04-28-claude-code-security-hardening.md, decision e)
	// for the shared-budget DoS trade-off analysis.
	r.Group(func(r chi.Router) {
		r.Use(httprate.LimitByIP(10, 1*time.Minute))
		r.Post("/auth/setup", s.handleSetup)
		r.Post("/auth/login", s.handleLogin)
		r.Get("/auth/desktop/info", s.handleDesktopAuthInfo)
		r.Post("/auth/desktop/local-login", s.handleDesktopLocalLogin)
		r.Post("/auth/desktop/oidc/start", s.handleDesktopOIDCStart)
		r.Get("/auth/desktop/oidc/poll", s.handleDesktopOIDCPoll)
	})

	// Auth info endpoint (tells frontend which auth methods are available)
	r.Get("/auth/info", s.handleAuthInfo)
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

	// Extract cluster store for Living Wiki taxonomy resolution when the
	// underlying store satisfies the interface (SurrealStore does).
	var gqlClusterStore clustering.ClusterStore
	if cs, ok := s.store.(clustering.ClusterStore); ok {
		gqlClusterStore = cs
	}

	// GraphQL server
	gqlSrv := handler.NewDefaultServer(graphql.NewExecutableSchema(graphql.Config{
		Resolvers: &graphql.Resolver{
			Store:              s.store,
			KnowledgeStore:     s.knowledgeStore,
			Worker:             s.worker,
			Orchestrator:       s.orchestrator,
			Config:             s.cfg,
			EventBus:           s.eventBus,
			Flags:              s.flags,
			GitConfig:          s.gitConfigStore,
			ComprehensionStore: s.comprehensionStore,
			HealthChecker:      s.healthChecker,
			TrashStore:         s.trashStore,
			SearchSvc:          s.searchSvc,
			ReqBooster:         s.reqBooster,
			QA:                 s.qaResolverOrchestrator(),
			LivingWikiStore:            s.livingWikiStore,
			LivingWikiResolver:         s.livingWikiResolver,
			LivingWikiRepoStore:        s.livingWikiRepoStore,
			LivingWikiJobResultStore:   s.livingWikiJobResultStore,
			LivingWikiLiveOrchestrator: s.livingWikiLiveOrchestrator,
			ClusteringHook:             s.clusteringHookFunc(),
			ClusterStore:               gqlClusterStore,
		},
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

		// Server-Sent Events stream of a discuss_code answer. The web
		// UI uses this for the "Ask" panel so users see tokens as the
		// model generates them. GraphQL's `discussCode` mutation is
		// still the unary fallback for clients that can't consume SSE.
		r.With(aiConcurrencyMiddleware).Post("/api/v1/discuss/stream", s.handleDiscussStream)

		// Server-side deep-QA orchestrator. Default-gated on
		// QAConfig.ServerSideEnabled — handler returns 503 when off.
		r.With(aiConcurrencyMiddleware).Post("/api/v1/ask", s.handleAsk)

		// Hybrid retrieval REST endpoint. Same backend as the GraphQL
		// search(...) field and the MCP search_symbols tool. Useful for
		// CLI and deep-mode QA clients that don't speak GraphQL.
		r.Post("/api/v1/search", s.handleSearch)

		// Subsystem clustering — list clusters and batch LLM relabel job.
		r.Get("/api/v1/repositories/{repo_id}/clusters", s.handleListClusters)
		r.Post("/api/v1/repositories/{repo_id}/clusters/relabel", s.handleRelabelClusters)
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

		// Subsystem clustering stats
		r.Get("/api/v1/admin/clustering/stats", s.handleClusteringStats)

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
		s.mcp = newMCPHandlerWithEdition(s.store, s.knowledgeStore, s.worker, s.cfg.MCP.Repos, sessionTTL, keepalive, s.cfg.MCP.MaxSessions, s.cache, capabilities.NormalizeEdition(s.cfg.Edition))
		s.mcp.qaOrchestrator = s.qaOrchestrator
		s.mcp.qaEnabled = s.cfg.QA.ServerSideEnabled
		s.mcp.searchSvc = s.searchSvc
		// Shared indexing service — enables end-to-end index_repository
		// + refresh_repository MCP flows (Follow-on #3).
		mcpIndexSvc := indexing.NewService(s.cfg, s.store, nil, nil)
		if hook := s.clusteringHookFunc(); hook != nil {
			mcpIndexSvc.WithClusteringHook(hook)
		}
		s.mcp.indexingSvc = mcpIndexSvc
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
		// HEAD probe used by the web client (use-server-capabilities.ts) to
		// detect whether MCP is enabled. Without an explicit handler chi
		// returns 405, which still tells the frontend "route exists" (its
		// fallback path) but logs a noisy "Failed to load resource" line in
		// every browser DevTools console. Answer 204 instead so the probe
		// is invisible.
		//
		// SECURITY: this probe is intentionally unauthenticated. It returns
		// 204 only when MCP is enabled, which lets an unauthenticated
		// observer fingerprint a SourceBridge install with MCP turned on.
		// We accept this trade-off because the probe is the mechanism the
		// web UI uses to render pre-auth UX (the "MCP isn't enabled on this
		// server" banner). Auth-gating the probe would push that detection
		// behind login and degrade the first-touch experience for users on
		// misconfigured servers. The disclosure is bounded — version, tenant
		// data, and auth-method enumeration are not exposed by this endpoint
		// (auth methods are advertised at /auth/desktop/info, which is
		// unauthenticated for the same reason). See the security model
		// section in docs/user/security-model.md for the full pre-auth
		// surface. See also xander finding H4 in the adversarial security
		// audit (2026-04-28-cloud-install-security-review-xander.md).
		r.Method("HEAD", "/api/v1/mcp/http", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		slog.Info("mcp server enabled", "max_sessions", s.cfg.MCP.MaxSessions, "session_ttl", sessionTTL, "keepalive", keepalive)
	}

	// Enterprise routes (no-op in OSS builds, registered when built with -tags enterprise)
	s.registerEnterpriseRoutes(r)

	// Living-wiki webhook routes.
	// Always registered so webhook senders receive a deterministic response:
	//   - Dispatcher present → routes are live, events dispatched.
	//   - Dispatcher nil     → routes return 503 with a clear body so the
	//     sender knows living-wiki is disabled, not that the path is wrong.
	if s.livingWikiDispatcher != nil {
		var confluenceSecret string
		if s.livingWikiResolver != nil {
			if resolved, err := s.livingWikiResolver.Get(); err == nil && resolved != nil {
				confluenceSecret = resolved.ConfluenceWebhookSecret
			}
		}
		deps := LivingWikiWebhookDeps{
			Dispatcher:              s.livingWikiDispatcher,
			ConfluenceWebhookSecret: confluenceSecret,
		}
		RegisterLivingWikiRoutes(r, deps)
		slog.Info("living-wiki webhook routes registered")
	} else {
		RegisterLivingWikiDisabledRoutes(r)
	}

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
	// Living-wiki dispatcher drain — 30-second budget after HTTP connections
	// are closed. If the budget is exceeded we log a warning and continue
	// rather than blocking the process indefinitely.
	if s.livingWikiDispatcher != nil {
		const dispatcherDrainTimeout = 30 * time.Second
		drainCtx, cancel := context.WithTimeout(context.Background(), dispatcherDrainTimeout)
		defer cancel()
		if err := s.livingWikiDispatcher.Stop(drainCtx); err != nil {
			slog.Warn("living-wiki dispatcher did not drain cleanly within timeout",
				"timeout", dispatcherDrainTimeout,
				"err", err,
			)
		}
	}
	return nil
}

// persistStaleLivingWikiResult records a failed LivingWikiJobResult when the
// reaper kills a stuck living-wiki cold-start (or retry-excluded) job. Without
// this, the per-repo settings panel reads `lastJobResult: null` and gives no
// signal that anything went wrong.
//
// repoID is parsed from the job's TargetKey (format `lw:<tenant>:<repoID>`).
// pagesPlanned/pagesGenerated are best-effort, parsed from the progress
// message ("N/M pages complete") so the user can see how far the job got.
func persistStaleLivingWikiResult(store livingwiki.JobResultStore, job *llm.Job) {
	tenantID, repoID := parseLWTargetKey(job.TargetKey)
	if repoID == "" {
		slog.Warn("livingwiki: could not parse repo_id from stale job target_key",
			"job_id", job.ID, "target_key", job.TargetKey)
		return
	}
	planned, generated := parseLWProgressMessage(job.ProgressMessage)
	now := time.Now()
	startedAt := job.CreatedAt
	if job.StartedAt != nil {
		startedAt = *job.StartedAt
	}
	result := &livingwiki.LivingWikiJobResult{
		RepoID:          repoID,
		JobID:           job.ID,
		StartedAt:       startedAt,
		CompletedAt:     &now,
		PagesPlanned:    planned,
		PagesGenerated:  generated,
		Status:          "failed",
		FailureCategory: "transient",
		ErrorMessage:    "Job timed out — no progress for the stale-job threshold. Retry to resume.",
	}
	if err := store.Save(context.Background(), tenantID, result); err != nil {
		slog.Warn("livingwiki: failed to persist stale-reap result",
			"job_id", job.ID, "repo_id", repoID, "error", err)
	}
}

// parseLWTargetKey extracts (tenant, repo) from a living-wiki target key in
// the form `lw:<tenant>:<repoID>`. Returns ("", "") when the format doesn't
// match — the caller skips persistence in that case.
func parseLWTargetKey(targetKey string) (tenant, repoID string) {
	parts := strings.SplitN(targetKey, ":", 3)
	if len(parts) != 3 || parts[0] != "lw" {
		return "", ""
	}
	return parts[1], parts[2]
}

// parseLWProgressMessage extracts (planned, generated) from a progress message
// like "35/169 pages complete". Returns (0, 0) when the message is unparseable,
// which is fine — the persisted result simply shows zero progress.
func parseLWProgressMessage(msg string) (planned, generated int) {
	slash := strings.Index(msg, "/")
	if slash <= 0 {
		return 0, 0
	}
	rest := msg[slash+1:]
	space := strings.Index(rest, " ")
	if space <= 0 {
		return 0, 0
	}
	g, gErr := strconv.Atoi(msg[:slash])
	p, pErr := strconv.Atoi(rest[:space])
	if gErr != nil || pErr != nil {
		return 0, 0
	}
	return p, g
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
