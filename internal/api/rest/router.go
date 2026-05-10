// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/pprof" //nolint:gosec // dev-only profiling, gated behind SOURCEBRIDGE_PPROF_ENABLED
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httprate"
	surrealdb "github.com/surrealdb/surrealdb.go"

	"github.com/sourcebridge/sourcebridge/internal/api/graphql"
	"github.com/sourcebridge/sourcebridge/internal/api/middleware"
	"github.com/sourcebridge/sourcebridge/internal/appdeps"
	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/capabilities"
	"github.com/sourcebridge/sourcebridge/internal/changewatch"
	"github.com/sourcebridge/sourcebridge/internal/clustering"
	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/db"
	"github.com/sourcebridge/sourcebridge/internal/events"
	"github.com/sourcebridge/sourcebridge/internal/featureflags"
	gitres "github.com/sourcebridge/sourcebridge/internal/git/resolution"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexing"
	"github.com/sourcebridge/sourcebridge/internal/installassets"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/webhook"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
	"github.com/sourcebridge/sourcebridge/internal/qa"
	"github.com/sourcebridge/sourcebridge/internal/search"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
	"github.com/sourcebridge/sourcebridge/internal/trash"
	"github.com/sourcebridge/sourcebridge/internal/worker"
	"github.com/sourcebridge/sourcebridge/internal/worker/llmcall"
)

// ServerOption configures optional Server parameters.
type ServerOption func(*Server)

// EnterpriseDB is the interface the rest package requires for the enterprise
// database handle. The single method gives enterprise_routes.go a typed
// accessor to the underlying *surrealdb.DB without a runtime type-assertion.
//
// The concrete implementation is *db.SurrealDB (which already has DB()).
// Any mismatch between the concrete type and this interface is caught at
// compile time when the enterprise build calls WithEnterpriseDB.
type EnterpriseDB interface {
	// DB returns the raw *surrealdb.DB handle. May return nil when the
	// store is running in embedded/disconnected mode.
	DB() *surrealdb.DB
}

// WithEnterpriseDB passes the enterprise database handle for enterprise store
// persistence. The concrete value must implement EnterpriseDB (i.e. expose a
// DB() *surrealdb.DB accessor). Passing *db.SurrealDB directly satisfies this.
func WithEnterpriseDB(db EnterpriseDB) ServerOption {
	return func(s *Server) { s.enterpriseDB = db }
}

// WithTokenStore overrides API token/session persistence.
func WithTokenStore(store auth.APITokenStore) ServerOption {
	return func(s *Server) { s.tokenStore = store }
}

// WithEventBus overrides the default in-process event bus. Primarily useful
// for testing handleSSE without standing up the full server — callers inject
// a pre-built *events.Bus and publish events directly from the test body.
func WithEventBus(bus *events.Bus) ServerOption {
	return func(s *Server) { s.eventBus = bus }
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
//
// R3 slice 2: prefer WithGitResolver in production. The store option
// remains for the migration command + the InvalidateLocal nudge from
// the PUT handler. Both store and resolver should be wired together;
// the store is now interface-narrowed to ctx-aware methods.
func WithGitConfigStore(store GitConfigStore) ServerOption {
	return func(s *Server) { s.gitConfigStore = store }
}

// WithGitResolver wires the runtime git-credential resolver. The resolver
// is the single source of truth for the default PAT and SSH key path on
// any git op. handleGetGitConfig / handleUpdateGitConfig read the
// resolved snapshot from this resolver and call InvalidateLocal after
// a successful save (instead of mutating s.cfg.Git, which is env-only
// after R3).
func WithGitResolver(r gitres.Resolver) ServerOption {
	return func(s *Server) { s.gitResolver = r }
}

// WithLLMConfigStore enables persistent storage of LLM configuration.
func WithLLMConfigStore(store LLMConfigStore) ServerOption {
	return func(s *Server) { s.llmConfigStore = store }
}

// WithLLMResolver wires the runtime LLM-config resolver. The resolver is
// the single source of truth for which provider/api-key/model the server
// uses on any LLM call. handleGetLLMConfig/handleListLLMModels read the
// current resolved snapshot from this resolver instead of s.cfg.LLM
// (which is env-bootstrap only after slice 1).
func WithLLMResolver(r resolution.Resolver) ServerOption {
	return func(s *Server) { s.llmResolver = r }
}

// WithLLMCaller wires the LLM-aware adapter around *worker.Client. Every
// gRPC LLM RPC must flow through this Caller so that workspace-saved
// settings (provider/api-key/model) are attached to the outgoing context
// in metadata. Pass nil when the worker is unavailable; downstream
// callers gate AI features on Caller.IsAvailable().
func WithLLMCaller(c *llmcall.Caller) ServerOption {
	return func(s *Server) { s.llmCaller = c }
}

// WithEncryptionKeySet records whether the API booted with a resolved
// encryption key. Surfaced on GET /api/v1/admin/llm-profiles as
// encryption_key_set so the web UI shows the correct onboarding state
// (r1 Phase 2d / plan §2d).
func WithEncryptionKeySet(set bool) ServerOption {
	return func(s *Server) { s.encryptionKeySet = set }
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

// WithLivingWikiPagePublishStore wires the per-page-per-sink dispatch state
// store introduced in Phase 1 of the incremental-publish redesign. Backs
// content-aware smart-resume (3-way bucket split: regenerate / skipFully /
// skipNeedsFixup) and the async dispatcher's preserve-on-failure semantics
// (SetReady / SetNonReady split). When nil, smart-resume falls back to
// sink-presence-only behavior — pages skip without fingerprint validation.
func WithLivingWikiPagePublishStore(s2 livingwiki.PagePublishStatusStore) ServerOption {
	return func(s *Server) { s.livingWikiPagePublishStore = s2 }
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
	// AppDeps is the shared dependency registry constructed once in NewServer.
	// It is populated after options are applied via syncServerDepsFromAppDeps.
	// The GraphQL resolver receives AppDeps via direct field assignment
	// (resolver.Deps = s.AppDeps) at construction — no sync function exists
	// on the resolver side. The existing lowercase fields below are the primary
	// store; AppDeps holds the same values for consumers that need the registry.
	AppDeps *appdeps.AppDeps

	cfg                        *config.Config
	router                     chi.Router
	localAuth                  *auth.LocalAuth
	jwtMgr                     *auth.JWTManager
	oidc                       *auth.OIDCProvider
	store                      graphstore.GraphStore
	knowledgeStore             knowledge.KnowledgeStore
	jobStore                   llm.JobStore               // persistent store for llm.Job records; defaults to MemStore
	orchestrator               *orchestrator.Orchestrator // shared LLM job orchestrator (created in NewServer)
	worker                     *worker.Client
	eventBus                   *events.Bus
	flags                      featureflags.Flags
	tokenStore                 auth.APITokenStore
	desktopAuth                DesktopAuthSessionStore
	gitConfigStore             GitConfigStore                    // persists git tokens/SSH config across restarts
	llmConfigStore             LLMConfigStore                    // persists LLM provider/model config across restarts
	llmProfileStore            LLMProfileStoreAdapter            // LLM provider profiles slice 1: profile CRUD + active pointer
	queueControlStore          QueueControlStore                 // persists queue intake controls across restarts
	enterpriseDB               EnterpriseDB                      // enterprise database handle; nil when enterprise build is not active
	repoChecker                middleware.RepoAccessChecker      // set by enterprise build to enable tenant repo filtering
	mcp                        *mcpHandler                       // MCP protocol handler (nil when disabled)
	mcpPermChecker             MCPPermissionChecker              // deferred to mcp handler at setup
	mcpAuditLogger             MCPAuditLogger                    // deferred to mcp handler at setup
	mcpToolExtender            MCPToolExtender                   // deferred to mcp handler at setup
	comprehensionStore         comprehension.Store               // comprehension settings + model capabilities
	summaryNodeStore           comprehension.SummaryNodeStore    // cached summary tree nodes
	cache                      db.Cache                          // shared KV cache (memory or Redis); nil = MCP session store falls back to in-memory
	trashStore                 trash.Store                       // soft-delete recycle bin; nil = feature disabled
	qaOrchestrator             *qa.Orchestrator                  // server-side deep-QA orchestrator; nil = server-side QA disabled
	workerLanes                *worker.Lanes                     // shared lane registry used by search + qa
	searchSvc                  *search.Service                   // hybrid retrieval backbone; always set in NewServer
	reqBooster                 *search.RequirementBooster        // repo-scoped requirement link cache; feeds searchSvc boosters
	searchMetrics              *search.Metrics                   // in-process ring buffer of per-stage latency / success
	backfiller                 *search.Backfiller                // post-index embedding backfill; nil when worker is unavailable
	livingWikiStore            livingwiki.Store                  // living-wiki UI settings store; nil = feature unavailable
	livingWikiResolver         *livingwiki.Resolver              // merged living-wiki config (UI + env); nil = only env applies
	livingWikiRepoStore        livingwiki.RepoSettingsStore      // per-repo living-wiki opt-in; nil = feature unavailable
	livingWikiDispatcher       *webhook.Dispatcher               // nil = feature not started or kill-switch active
	livingWikiJobResultStore   livingwiki.JobResultStore         // nil = job result history unavailable
	livingWikiPagePublishStore livingwiki.PagePublishStatusStore // per-page dispatch state (Phase 1 of incremental-publish redesign); nil = smart-resume falls back to sink-presence-only
	livingWikiLiveOrchestrator *lworch.Orchestrator              // living-wiki page-generation orchestrator; nil = feature unavailable
	knowledgeSettingsStore     KnowledgeSettingsStore            // CA-122: operator-tunable knowledge-RPC safety-net timeout; nil = embedded mode (boot env-default only)
	clusterRunner              *clustering.Runner                // subsystem clustering job dispatcher; nil = feature disabled
	healthChecker              *HealthChecker                    // shared DB+worker probe; nil = embedded/test mode, handlers fall back to local checks
	workerVersionLookup        *versionLookup                    // best-effort cache for worker GetVersion (CA-136); nil = workerVersion always "" in /api/v1/version
	gateSnapshotCache          gateSnapshotCache                 // 1-second TTL cache for worker gate snapshot (Phase 7)

	// encryptionKeySet is true when the API booted with a resolved encryption
	// key (from SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE or the literal env
	// var). Surfaced on GET /api/v1/admin/llm-profiles as encryption_key_set
	// so the web UI can show the correct onboarding state (r1 Phase 2d).
	encryptionKeySet bool

	// LLM source-of-truth (single resolver shared with the GraphQL resolver
	// and llmcall.Caller). The Server owns the resolver so handleGetLLMConfig
	// and handleListLLMModels can read the resolved snapshot rather than
	// s.cfg.LLM (which is env-only after slice 1).
	llmResolver resolution.Resolver // nil only in test/embedded mode without a workspace store
	llmCaller   *llmcall.Caller     // nil when worker is unavailable

	// Git source-of-truth (R3 slice 2). Single resolver shared with the
	// GraphQL resolver. handleGetGitConfig + handleUpdateGitConfig read
	// and InvalidateLocal on this resolver instead of mutating s.cfg.Git
	// (which is env-bootstrap only after R3 — captured by VALUE inside
	// the resolver and never mutated post-boot).
	gitResolver gitres.Resolver // nil only in test/embedded mode without a workspace store

	// changeDispatcher is the boundary the change-watch HTTP ingress
	// (POST /v1/connectors/{id}/events) and the record_change MCP tool
	// dispatch through. Wired at server-assembly time when
	// change_watch.enabled is true; nil otherwise (the HTTP route
	// returns 503; the MCP tool returns a structured "disabled"
	// response). The interface decouples the rest package from the
	// concrete *changewatch.Router so tests substitute a stub.
	changeDispatcher ChangeEventDispatcher

	// qaLocator is the QA repo locator created in NewServer and consumed by
	// setupRouter when wiring mcpHandler.fileReader (CA-151). Kept on the
	// struct so both methods share the same instance without threading it
	// through function signatures.
	qaLocator *qaRepoLocator

	// CA-142: graceful-drain lifecycle state.
	// serverDraining is set once (CAS) when SIGTERM or the preStop hook
	// triggers BeginDrain. It gates /readyz, new Living Wiki mutations,
	// and on-demand generation admissions.
	serverDraining atomic.Bool
	drainingAt     time.Time
	drainingMu     sync.Mutex
	// OnDemand counts active on-demand Living Wiki page-generation
	// requests. Admitted before settings/LLM resolution begin so
	// AwaitDrain sees the full request lifetime. Wired into the GraphQL
	// Resolver via DrainAdmitter so the resolver has no direct dep on
	// *rest.Server. CA-142.
	OnDemand *OnDemandTracker
}

// WithChangeDispatcher injects the change-watch dispatcher (typically
// *changewatch.Router) at server-assembly time. Pass nil to leave the
// HTTP ingress and the record_change MCP tool reporting disabled.
func WithChangeDispatcher(d ChangeEventDispatcher) ServerOption {
	return func(s *Server) { s.changeDispatcher = d }
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

// authMiddleware returns the JWT+API-token middleware configured for this
// server.  When Security.APITokenLegacyAdminDefault is true the middleware
// treats tokens whose role field is empty as "admin" (operator escape hatch
// during migration 056 rollout).  All call sites use this helper so the flag
// is honoured consistently without duplicating the config read.
func (s *Server) authMiddleware() func(http.Handler) http.Handler {
	legacyAdmin := s.cfg != nil && s.cfg.Security.APITokenLegacyAdminDefault
	return auth.MiddlewareWithTokensAndLegacyAdmin(s.jwtMgr, s.tokenStore, legacyAdmin)
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
		OnDemand:    NewOnDemandTracker(),
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
			_ = s.knowledgeStore.SetArtifactFailed(context.Background(), job.ArtifactID, "DEADLINE_EXCEEDED", "Generation timed out — please retry")
		}
		if s.livingWikiJobResultStore != nil && job.Subsystem == llm.SubsystemLivingWiki {
			persistStaleLivingWikiResult(s.livingWikiJobResultStore, job)
		}
	}
	// OnJobFailed fires from all three failure paths (finalizeFailed,
	// reaper, reconcileZombieJobs) and propagates the failure into the
	// ca_repository_understanding row so the repo screen shows "Failed"
	// instead of "generating" indefinitely.
	// Receiver is s.knowledgeStore (knowledge.KnowledgeStore at router.go:281),
	// not s.store (graphstore.GraphStore) — same receiver OnStaleJob uses above.
	orchCfg.OnJobFailed = func(job *llm.Job) {
		if job == nil || s.knowledgeStore == nil {
			return
		}
		if job.JobType != "build_repository_understanding" {
			return
		}
		if job.ArtifactID == "" {
			return
		}
		code := job.ErrorCode
		if code == "" {
			code = "JOB_FAILED"
		}
		msg := job.ErrorMessage
		if msg == "" {
			msg = "Repository understanding job failed"
		}
		if err := s.knowledgeStore.MarkRepositoryUnderstandingFailed(context.Background(), job.ArtifactID, code, msg); err != nil {
			slog.Warn("mark_repository_understanding_failed_error",
				"job_id", job.ID,
				"understanding_id", job.ArtifactID,
				"error", err)
		}
	}
	s.orchestrator = orchestrator.New(s.jobStore, orchCfg)
	if s.queueControlStore != nil {
		if rec, err := s.queueControlStore.LoadQueueControl(context.Background()); err == nil && rec != nil {
			s.orchestrator.SetIntakePaused(rec.IntakePaused)
		}
	}
	slog.Info("server drain handler armed",
		"event", "server_drain_handler_armed")

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
		// Backfiller uses a separate WorkerEmbedder so backfill calls don't
		// pollute the query-embedding LRU cache (backfill texts are one-shot
		// and unlikely to be reused as search queries).
		backfillEmb := search.NewWorkerEmbedder(s.worker, "")
		s.backfiller = search.NewBackfiller(s.store, backfillEmb, 0, search.BackfillConfig{
			RPS:       2,
			Batch:     8,
			MaxPerRun: 2000,
		})
	}

	// lazyAgent is the production LazyAgentSynth that implements both the
	// agentic synthesizer interface and UpstreamCapacityProvider. Declared
	// here (outside the if cfg != nil block) so AppDeps can capture it as
	// UpstreamCapacityProvider after the QA block completes (Phase 2 / D4).
	// Remains nil when no cfg or no llmCaller is available.
	var lazyAgent *qa.LazyAgentSynth

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
		o := qa.New(s.llmCaller, reader, s.workerLanes, qaOrchCfg)
		if s.store != nil {
			s.qaLocator = newQARepoLocator(s.store, cfg.Storage.RepoCachePath)
			o = o.WithRepoLocator(s.qaLocator)
			o = o.WithGraphExpander(qa.NewGraphExpander(&qaGraphAdapter{store: s.store}, &qaGraphLookup{store: s.store}))
			o = o.WithRequirementLookup(&qaRequirementLookup{store: s.store})
			o = o.WithSymbolLookup(&qaSymbolLookup{store: s.store})
			o = o.WithFileReader(&qaFileReader{locator: s.qaLocator})
		}
		if s.knowledgeStore != nil {
			o = o.WithArtifactLookup(&qaArtifactLookup{store: s.knowledgeStore})
		}
		if s.orchestrator != nil {
			o = o.WithJobRunner(&qaJobRunner{orch: s.orchestrator, llmResolver: s.llmResolver})
		}
		if s.searchSvc != nil {
			o = o.WithSearcher(&qaSearcher{svc: s.searchSvc})
		}
		// Agentic path — wired through a LazyAgentSynth (CA-126,
		// tester report Issue 2 / Wave 3) so the worker's capability
		// probe is deferred to the first agentic-eligible request and
		// re-attempted on failure with a cooldown.
		//
		// Pre-CA-126 design: a 30s synchronous probe-with-retry at
		// boot. If every attempt failed (e.g. API server boots first
		// in `make dev` and the user starts the worker afterwards),
		// the synthesizer was never wired and agentic features stayed
		// disabled for the pod's entire lifetime. The lazy provider
		// fixes that case AND continues to handle the K8s rolling-
		// restart race correctly (first real request triggers the
		// probe; the worker is up by then).
		//
		// We always wire the lazy provider — even when llmCaller is
		// nil — because the orchestrator's shouldUseAgenticPath gate
		// short-circuits on a nil caller without burning a probe.
		//
		if s.llmCaller != nil {
			versionSrc := &resolverVersionSource{r: s.llmResolver}
			lazyAgent = qa.NewLazyAgentSynth(s.llmCaller, versionSrc, qa.LazyAgentSynthOptions{
				// Per-request probe deadline. The first agentic request
				// after a cold start blocks on this; 2s is the sweet
				// spot — long enough that a healthy worker on the
				// loopback wins comfortably, short enough that a wedged
				// worker doesn't stall user-visible latency.
				Timeout: 2 * time.Second,
				// Cooldown between failed-probe re-attempts. 60s matches
				// the pre-CA-126 worst-case experience while bounding
				// retry pressure when the worker is genuinely down.
				Cooldown: 60 * time.Second,
			})
			o = o.WithAgentSynthesizer(lazyAgent).
				WithAgenticEnabled(cfg.QA.AgenticRetrievalEnabled).
				WithAgenticCanaryPct(cfg.QA.AgenticRetrievalCanaryPct)
			o = o.WithQuestionProfiler(qa.NewWorkerQuestionProfiler(s.llmCaller))
			o = o.WithDecomposer(qa.NewWorkerDecomposer(s.llmCaller), s.llmCaller)
			slog.Info("agent synth: lazy provider wired",
				"agentic_enabled", cfg.QA.AgenticRetrievalEnabled,
				"canary_pct", cfg.QA.AgenticRetrievalCanaryPct,
				"smart_classifier", cfg.QA.SmartClassifierEnabled,
				"query_decomposition", cfg.QA.QueryDecompositionEnabled,
				"prompt_caching", cfg.QA.PromptCachingEnabled,
			)

			// Best-effort boot probe — does NOT block boot. Hot-warms
			// the cache for the K8s rolling-restart case (worker is
			// usually up within seconds of the API). Local-dev users
			// who start the worker after `make dev` see the warning
			// goroutine below and are told what to run.
			workerAddr := cfg.Worker.Address
			if workerAddr == "" {
				workerAddr = "(unknown address)"
			}
			go bootProbeAndWarn(lazyAgent, workerAddr)
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

	// Worker version lookup (CA-136). When a worker client is available,
	// wire the gRPC probe so /api/v1/version reports the worker's version.
	// Cached for 30s; per-call timeout governed by the public handler.
	var workerProbe func(ctx context.Context) (string, error)
	if s.worker != nil {
		workerProbe = s.worker.GetWorkerVersion
	}
	s.workerVersionLookup = newVersionLookup(30*time.Second, workerProbe)

	// Build AppDeps — the shared dependency registry (Phase 2 Slice 5,
	// STRUCT-1). Constructed once here after all fields are settled.
	// The GraphQL resolver receives AppDeps via direct field assignment at
	// construction; there is no resolver-side sync function. AppDeps is also
	// written back into the Server's lowercase fields via syncServerDepsFromAppDeps
	// (idempotent — values already match).
	{
		var clusterStore clustering.ClusterStore
		if cs, ok := s.store.(clustering.ClusterStore); ok {
			clusterStore = cs
		}
		var profileLookup appdeps.LLMProfileLookup
		if s.llmProfileStore != nil {
			profileLookup = s.llmProfileStore
		}
		// Capture lazyAgent as the UpstreamCapacityProvider for the coldstart
		// runner (Phase 2 / D4). LazyAgentSynth implements UpstreamCapacity(ctx)
		// and shares the single-flight probe with SupportsTools (M4). nil when
		// no worker caller is configured (tests, deployments without a worker).
		var capacityProvider lworch.UpstreamCapacityProvider
		if lazyAgent != nil {
			capacityProvider = lazyAgent
		}
		s.AppDeps = &appdeps.AppDeps{
			KnowledgeStore:             s.knowledgeStore,
			Worker:                     s.worker,
			LLMCaller:                  s.llmCaller,
			LLMResolver:                s.llmResolver,
			Orchestrator:               s.orchestrator,
			Config:                     s.cfg,
			EventBus:                   s.eventBus,
			Flags:                      s.flags,
			GitResolver:                s.gitResolver,
			ComprehensionStore:         s.comprehensionStore,
			HealthChecker:              s.healthChecker,
			TrashStore:                 s.trashStore,
			SearchSvc:                  s.searchSvc,
			ReqBooster:                 s.reqBooster,
			Backfiller:                 s.backfiller,
			QA:                         s.qaResolverOrchestrator(),
			LLMProfileLookup:           profileLookup,
			LivingWikiStore:            s.livingWikiStore,
			LivingWikiResolver:         s.livingWikiResolver,
			LivingWikiRepoStore:        s.livingWikiRepoStore,
			LivingWikiJobResultStore:   s.livingWikiJobResultStore,
			LivingWikiLiveOrchestrator: s.livingWikiLiveOrchestrator,
			LivingWikiPagePublishStore: s.livingWikiPagePublishStore,
			// LivingWikiAuditLog: enterprise-only; not stored on Server; left nil
			// here (enterprise routes or tests may set it on the resolver directly).
			ClusterStore:             clusterStore,
			WorkerVersion:            buildWorkerVersionFunc(s),
			DrainAdmitter:            s.DrainAdmitterFor(),
			EncryptionKeySet:         s.encryptionKeySet,
			UpstreamCapacityProvider: capacityProvider,
		}
		// syncServerDepsFromAppDeps is an idempotent identity sync: the Server
		// already has these values from the WithXxx options applied above. It
		// exists as cheap insurance so any future code reading s.AppDeps and the
		// lowercase fields sees consistent state.
		syncServerDepsFromAppDeps(s, s.AppDeps)
	}

	s.setupRouter()
	return s
}

// Orchestrator returns the server's LLM job orchestrator. Exposed so
// tests and the graceful-shutdown path can call Shutdown on it.
func (s *Server) Orchestrator() *orchestrator.Orchestrator {
	return s.orchestrator
}

// kickBackfill fires the embedding backfill for a repo via the shared
// Backfiller. Delegates to Backfiller.KickBackground which spawns its own
// goroutine and returns immediately. Safe to use as an IndexCompleteHook.
func (s *Server) kickBackfill(repoID string) {
	s.backfiller.KickBackground(repoID)
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

	// pprof — gated behind SOURCEBRIDGE_PPROF_ENABLED=true.
	//
	// CA-204: previously mounted at the top level (no auth). Goroutine /
	// heap dumps can leak in-flight tokens, environment values, and other
	// secrets — they MUST require admin role. The auth + RequireRole(admin)
	// chain wraps every pprof endpoint here.
	//
	// Mounted before the global rate limiter so a profile dump under load
	// is not throttled.
	if os.Getenv("SOURCEBRIDGE_PPROF_ENABLED") == "true" {
		slog.Warn("pprof endpoints enabled — gated to admin role (SOURCEBRIDGE_PPROF_ENABLED=true)",
			"path", "/debug/pprof/*")
		r.Group(func(r chi.Router) {
			r.Use(s.authMiddleware())
			r.Use(auth.RequireRole(auth.RoleAdmin))
			r.HandleFunc("/debug/pprof/", pprof.Index)
			r.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
			r.HandleFunc("/debug/pprof/profile", pprof.Profile)
			r.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
			r.HandleFunc("/debug/pprof/trace", pprof.Trace)
			r.Handle("/debug/pprof/goroutine", pprof.Handler("goroutine"))
			r.Handle("/debug/pprof/heap", pprof.Handler("heap"))
			r.Handle("/debug/pprof/allocs", pprof.Handler("allocs"))
			r.Handle("/debug/pprof/block", pprof.Handler("block"))
			r.Handle("/debug/pprof/mutex", pprof.Handler("mutex"))
			r.Handle("/debug/pprof/threadcreate", pprof.Handler("threadcreate"))
		})
	}

	// Public routes
	r.Get("/healthz", s.handleHealthz)
	r.Get("/readyz", s.handleReadyz)
	r.Get("/metrics", s.handleMetrics)
	// /api/health: public-ingress-reachable liveness probe. The Ingress routes
	// /api/* to sourcebridge-api, so this handler ensures uptime monitors
	// pointing at https://<host>/api/health receive a valid 200 response.
	// Mirrors the Next.js web/src/app/api/health/route.ts for the API path
	// (codex r2 C1 fix).
	r.Get("/api/health", s.handleApiHealth)

	// Public version endpoint (CA-136). Returns build metadata for the
	// running API server plus a best-effort worker version. Intentionally
	// unauthenticated — version strings are not sensitive (commit sha is
	// already exposed via /api/v1/admin/status; build date and runtime
	// metadata are innocuous). Used by the web sidebar footer + admin
	// Build Info card to fetch runtime fields not baked into the bundle.
	r.Get("/api/v1/version", s.handleVersion)

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

	// CA-NEW-LOGOUT: /auth/logout stays public (no auth middleware) so that
	// users can log out even after a session expires in another tab. When
	// CSRFFullCoverageEnabled=true, the double-submit cookie check still verifies
	// the request originated from the user's own browser, which closes the
	// autosubmit attack vector (xander CSRF-1).
	if s.cfg.Security.CSRFFullCoverageEnabled {
		r.Group(func(r chi.Router) {
			r.Use(csrfProtectionWithName(
				s.jwtMgr.CSRFCookieName(),
				s.jwtMgr.SessionCookieName(),
				true,
			))
			r.Post("/auth/logout", s.handleLogout)
		})
	} else {
		r.Post("/auth/logout", s.handleLogout)
	}

	// OIDC routes
	r.Get("/auth/oidc/login", s.handleOIDCLogin)
	r.Get("/auth/oidc/callback", s.handleOIDCCallback)

	// Change password requires authentication (CA-NEW-CHGPW: CSRF-gated when flag on).
	r.Group(func(r chi.Router) {
		r.Use(httprate.LimitByIP(10, 1*time.Minute))
		r.Use(auth.Middleware(s.jwtMgr))
		if s.cfg.Security.CSRFFullCoverageEnabled {
			r.Use(csrfProtectionWithName(
				s.jwtMgr.CSRFCookieName(),
				s.jwtMgr.SessionCookieName(),
				true,
			))
		}
		r.Post("/auth/change-password", s.handleChangePassword)
	})

	// GraphQL server — all subsystem dependencies are read via r.Deps.<Field>.
	// ClusteringHook is a closure constructed at wiring time and stays on
	// Resolver directly (not in AppDeps; see appdeps package doc for rationale).
	gqlResolver := &graphql.Resolver{
		Deps:           s.AppDeps,
		Store:          s.store,
		Plan:           graphql.BootCurrentPlan(),
		ClusteringHook: s.clusteringHookFunc(),
	}

	gqlSrv := handler.NewDefaultServer(graphql.NewExecutableSchema(graphql.Config{
		Resolvers: gqlResolver,
	}))

	// Protected API routes (accepts both JWT and API tokens)
	r.Group(func(r chi.Router) {
		r.Use(s.authMiddleware())
		// Tenant repo filtering — repoChecker is set by registerEnterpriseRoutes
		// (after this group is defined), so we read it lazily at request time.
		r.Use(s.lazyRepoAccessMiddleware)
		if s.cfg.Security.CSRFEnabled {
			// fullCoverage=false when the flag is off → Bearer-only bypass (today's
			// behaviour preserved exactly). fullCoverage=true tightens to Bearer+no-session
			// (CA-201) once CSRFFullCoverageEnabled is flipped by the operator post-Phase-1.
			r.Use(csrfProtectionWithName(
				s.jwtMgr.CSRFCookieName(),
				s.jwtMgr.SessionCookieName(),
				s.cfg.Security.CSRFFullCoverageEnabled,
			))
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

		// Per-repository LLM job monitor — authenticated users with read access
		// to the repo. These are the non-admin counterparts to the global
		// /api/v1/admin/llm/* endpoints. Each handler verifies job.RepoID matches
		// the path {id} so jobs from other repos are never leaked.
		// Regression: audit-refactor Phase 0 Slice 4 moved the admin/llm routes
		// behind RequireRole(admin), breaking the repo detail page for non-admins.
		r.Get("/api/v1/repositories/{id}/llm-activity", s.handleRepoLLMActivity)
		r.Get("/api/v1/repositories/{id}/llm-jobs/{job_id}", s.handleRepoLLMJobDetail)
		r.Get("/api/v1/repositories/{id}/llm-jobs/{job_id}/logs", s.handleRepoLLMJobLogs)
		r.Post("/api/v1/repositories/{id}/llm-jobs/{job_id}/cancel", s.handleRepoLLMJobCancel)
	})

	// Authenticated routes — outer group populates claims and applies tenant
	// repo filtering; inner subgroups split on required role.
	//
	// Middleware order: tokens → repo-access → [csrf] → role.
	// Tokens populates claims; repo-access tightens repo IDs; role checks claims.
	// CSRF middleware is added only when CSRFFullCoverageEnabled=true (CA-198).
	r.Group(func(r chi.Router) {
		r.Use(s.authMiddleware())
		r.Use(s.lazyRepoAccessMiddleware)
		// CA-198: gate the second authenticated group on the full-coverage flag.
		// literal true because this branch only executes when the flag is on.
		if s.cfg.Security.CSRFFullCoverageEnabled {
			r.Use(csrfProtectionWithName(
				s.jwtMgr.CSRFCookieName(),
				s.jwtMgr.SessionCookieName(),
				true,
			))
		}

		// Subgroup A: user-scoped token self-service (auth required, no role gate).
		// Non-admin users manage ONLY their own tokens here. Handler-side
		// enforcement (ownership checks, UserID filtering) is the second line of
		// defence so a future route-group change cannot silently widen access.
		r.Group(func(r chi.Router) {
			// API token management — user self-service
			r.Post("/api/v1/tokens", s.handleCreateToken)
			r.Get("/api/v1/tokens", s.handleListTokens)
			r.Get("/api/v1/tokens/current", s.handleCurrentToken)
			r.Delete("/api/v1/tokens/{id}", s.handleRevokeToken)
			r.Post("/api/v1/tokens/current/revoke", s.handleRevokeCurrentToken)
		})

		// Subgroup B: admin-only routes (SEC-4 gate: requires "admin" role).
		// All /api/v1/admin/* endpoints and cross-user token operations live here.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(auth.RoleAdmin))

			// Cross-user token administration
			r.Post("/api/v1/tokens/revoke-user", s.handleRevokeUserTokens)

			// Admin status & config
			r.Get("/api/v1/admin/status", s.handleAdminStatus)
			r.Get("/api/v1/admin/config", s.handleAdminConfig)
			r.Put("/api/v1/admin/config", s.handleAdminUpdateConfig)
			r.Post("/api/v1/admin/test-worker", s.handleAdminTestWorker)
			r.Post("/api/v1/admin/test-llm", s.handleAdminTestLLM)
			r.Get("/api/v1/admin/knowledge", s.handleAdminKnowledgeStatus)

			// CA-122: operator-tunable knowledge-RPC safety-net timeout. The
			// outer cap on per-call repository-scoped knowledge generation
			// (cliff notes, learning path, architecture diagram, workflow
			// story, system explanation, code tour, enterprise report).
			// Distinct from the per-phase reaper which fires on "no
			// progress in 10 min." Default 4h, range 30 min – 24 h.
			// Validation: PUT rejects out-of-range with HTTP 400.
			r.Get("/api/v1/admin/knowledge/timeout", s.handleGetKnowledgeTimeout)
			r.Put("/api/v1/admin/knowledge/timeout", s.handlePutKnowledgeTimeout)

			// LLM job monitor (Phase 2c)
			r.Get("/api/v1/admin/llm/activity", s.handleLLMActivity)
			r.Get("/api/v1/admin/llm/stream", s.handleLLMStream)
			r.Get("/api/v1/admin/llm/control", s.handleGetQueueControl)
			r.Put("/api/v1/admin/llm/control", s.handleUpdateQueueControl)
			r.Post("/api/v1/admin/llm/drain", s.handleDrainQueue)
			// CA-142: admin endpoint to initiate graceful server drain via the
			// public API. Idempotent — returns current drain state whether or
			// not this call was the first to flip the flag.
			r.Post("/api/v1/admin/llm/server-drain", s.handleAdminServerDrain)
			// CA-142: debug slow-job endpoint for drain validation (Phase 4).
			// Only registered when SOURCEBRIDGE_DEBUG_ENDPOINTS=true.
			if s.flags.DebugEndpointsEnabled {
				r.Post("/api/v1/admin/debug/slow-job", s.handleDebugSlowJob)
			}
			r.Get("/api/v1/admin/llm/jobs/{id}", s.handleLLMJobDetail)
			r.Get("/api/v1/admin/llm/jobs/{id}/logs", s.handleLLMJobLogs)
			r.Get("/api/v1/admin/llm/jobs/{id}/logs/stream", s.handleLLMJobLogStream)
			r.Post("/api/v1/admin/llm/jobs/{id}/cancel", s.handleLLMJobCancel)
			r.Post("/api/v1/admin/llm/jobs/{id}/retry", s.handleLLMJobRetry)
			// CA-144: per-page in-flight visibility for living-wiki cold-starts.
			r.Get("/api/v1/admin/llm/jobs/{id}/livingwiki/in-flight", s.handleLivingWikiInFlight)

			// LLM configuration
			r.Get("/api/v1/admin/llm-config", s.handleGetLLMConfig)
			r.Put("/api/v1/admin/llm-config", s.handleUpdateLLMConfig)
			r.Get("/api/v1/admin/llm-models", s.handleListLLMModels)

			// LLM provider profiles (slice 1). Additive surface; legacy
			// /admin/llm-config remains as the active-profile back-compat
			// path. Writes go through the BEGIN/COMMIT helpers in
			// internal/db so workspace.version is bumped, the legacy
			// mirror row stays in sync, and the active-profile watermark
			// advances on every write (codex-H2 / r1d).
			r.Get("/api/v1/admin/llm-profiles", s.handleListLLMProfiles)
			r.Post("/api/v1/admin/llm-profiles", s.handleCreateLLMProfile)
			// Slice 4 polish: count of currently-active LLM-backed jobs.
			// Used by the SwitchProfileDialog so an admin sees how many
			// in-flight jobs are running on the current profile when they
			// confirm a switch. Read-only; route ordered before {id}
			// to avoid being shadowed by canonicalProfileID parsing.
			r.Get("/api/v1/admin/llm-profiles/active-job-count", s.handleActiveLLMJobCount)
			r.Get("/api/v1/admin/llm-profiles/{id}", s.handleGetLLMProfile)
			r.Put("/api/v1/admin/llm-profiles/{id}", s.handleUpdateLLMProfile)
			r.Delete("/api/v1/admin/llm-profiles/{id}", s.handleDeleteLLMProfile)
			r.Post("/api/v1/admin/llm-profiles/{id}/activate", s.handleActivateLLMProfile)

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

			// Telemetry
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
	})

	// Change-watch connector ingress (Phase 1.D). Behind the
	// connector_api.enabled feature flag — default off through Phase 1.
	// When the flag is off the route is never registered, so external
	// probes see a 404 with no fingerprint of the SourceBridge install.
	// Authentication: bearer token / JWT through the same middleware
	// as the other authenticated routes; HMAC validation specific to
	// GitHub webhooks lands in Phase 2.
	if s.cfg != nil && s.cfg.ConnectorAPI.Enabled {
		r.Group(func(r chi.Router) {
			r.Use(s.authMiddleware())
			r.Post("/v1/connectors/{id}/events", s.handleConnectorEvent)
		})
	}

	// MCP (Model Context Protocol) routes
	if s.cfg.MCP.Enabled {
		sessionTTL := time.Duration(s.cfg.MCP.SessionTTL) * time.Second
		keepalive := time.Duration(s.cfg.MCP.Keepalive) * time.Second
		// Wrap the LLM-aware Caller so MCP RPCs flow through the
		// resolver. When llmCaller is nil (worker unavailable), pass nil
		// and the MCP tools degrade gracefully via IsAvailable() checks.
		var mcpWorker mcpWorkerCaller
		if s.llmCaller != nil {
			mcpWorker = &mcpLLMCallerAdapter{
				caller:   s.llmCaller,
				unaryOp:  resolution.OpMCPExplain,
				streamOp: resolution.OpMCPDiscussStream,
				reviewOp: resolution.OpMCPReview,
			}
		}
		s.mcp = newMCPHandlerWithEdition(s.store, s.knowledgeStore, mcpWorker, s.cfg.MCP.Repos, sessionTTL, keepalive, s.cfg.MCP.MaxSessions, s.cache, capabilities.NormalizeEdition(s.cfg.Edition))
		s.mcp.qaOrchestrator = s.qaOrchestrator
		s.mcp.qaEnabled = s.cfg.QA.ServerSideEnabled
		s.mcp.searchSvc = s.searchSvc
		// Shared indexing service — enables end-to-end index_repository
		// + refresh_repository MCP flows (Follow-on #3).
		//
		// Codex r2 high fix: route the git-credentials func through the
		// runtime resolver so MCP indexing fails closed on encrypted-DB
		// integrity errors instead of silently falling back to env. The
		// non-GraphQL surfaces must obey the same source-of-truth
		// contract as the GraphQL clone/import paths.
		var mcpCreds indexing.GitCredentialsFunc
		if s.gitResolver != nil {
			resolver := s.gitResolver
			mcpCreds = func(ctx context.Context) (string, string, error) {
				snap, err := resolver.Resolve(ctx)
				if err != nil {
					return "", "", err
				}
				if snap.IntegrityError != nil {
					return "", "", snap.IntegrityError
				}
				gitres.LogResolved(slog.Default(), "mcp.indexing", snap)
				return snap.Token, snap.SSHKeyPath, nil
			}
		}
		mcpIndexSvc := indexing.NewService(s.cfg, s.store, mcpCreds, nil)
		if hook := s.clusteringHookFunc(); hook != nil {
			mcpIndexSvc.WithClusteringHook(hook)
		}
		mcpIndexSvc.WithIndexCompleteHook(s.kickBackfill)
		s.mcp.indexingSvc = mcpIndexSvc
		s.mcp.allowPrivateGitHosts = s.cfg != nil && s.cfg.Indexing.AllowPrivateGitHosts
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
		// Wire the change-watch dispatcher for the record_change MCP
		// tool (Phase 1.D). When nil the tool is hidden from
		// tools/list. The same dispatcher already feeds the HTTP
		// ingress via s.handleConnectorEvent, so both connectors share
		// one router instance.
		if s.changeDispatcher != nil {
			s.mcp.changeDispatcher = s.changeDispatcher
			// Wire the freshness provider when the dispatcher is a
			// *changewatch.Router (the production case). The type
			// assertion is the only place the rest package reaches
			// into changewatch's concrete type — every other code
			// path goes through interfaces.
			if router, ok := s.changeDispatcher.(*changewatch.Router); ok {
				s.mcp.freshness = NewRouterFreshnessProvider(router)
			}
		}
		// Wire file reader (CA-151): nil-safe — when s.qaLocator is nil (no store),
		// fileReader stays nil and the new symbol-source tools return a
		// "file reader not configured" error rather than panicking.
		if s.qaLocator != nil {
			s.mcp.fileReader = &qaFileReader{locator: s.qaLocator}
		}
		s.mcp.capabilityChecker = capabilities.IsAvailable
		// SSE + message endpoints: behind auth (JWT or API token).
		// Session ownership re-verified against authenticated identity per Slice 7 / SEC-1.
		r.Group(func(r chi.Router) {
			r.Use(s.authMiddleware())
			r.Get("/api/v1/mcp/sse", s.mcp.handleSSE)
			r.Post("/api/v1/mcp/message", s.mcp.handleMessage)
		})
		// Streamable HTTP transport: auth on every request (for Codex, etc.)
		r.Group(func(r chi.Router) {
			r.Use(s.authMiddleware())
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
		// CA-206: handler-time resolver lets admin-UI changes take effect
		// without restart. The eager ConfluenceWebhookSecret stays as a
		// boot-time fallback (used if the resolver is unavailable per
		// request).
		var confluenceResolver func() (string, error)
		if s.livingWikiResolver != nil {
			resolverRef := s.livingWikiResolver
			confluenceResolver = func() (string, error) {
				resolved, err := resolverRef.Get()
				if err != nil {
					return "", err
				}
				if resolved == nil {
					return "", nil
				}
				return resolved.ConfluenceWebhookSecret, nil
			}
		}
		// NEW-H1: Notion-poll requires admin bearer auth (the trigger is
		// an operator-controlled CronJob, not a SaaS push). The auth chain
		// is composed once here so the route layer in
		// RegisterLivingWikiRoutes can wrap.
		notionAuth := func(next http.Handler) http.Handler {
			return s.authMiddleware()(auth.RequireRole(auth.RoleAdmin)(next))
		}
		deps := LivingWikiWebhookDeps{
			Dispatcher:               s.livingWikiDispatcher,
			ConfluenceWebhookSecret:  confluenceSecret,
			ConfluenceSecretResolver: confluenceResolver,
			NotionPollAuthMiddleware: notionAuth,
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

// InternalHandler returns an http.Handler for the internal loopback
// listener (127.0.0.1:8081). Only lifecycle-management routes are
// registered here; no auth middleware is required because the loopback
// bind enforces locality. CA-142.
func (s *Server) InternalHandler() http.Handler {
	r := chi.NewRouter()
	// POST /internal/begin-drain — invoked by the Kubernetes preStop hook.
	r.Post("/internal/begin-drain", s.handleBeginDrainInternal)
	return r
}

// BeginDrain atomically flips the server into draining state and pauses
// the LLM orchestrator's job intake. Returns true if this call was the
// first to flip the flag, false if drain was already in progress.
// Safe to call from multiple goroutines (preStop hook and SIGTERM handler).
func (s *Server) BeginDrain(source string) (first bool) {
	if s == nil {
		return false
	}
	if !s.serverDraining.CompareAndSwap(false, true) {
		// Already draining — log the redundant source for observability.
		slog.Info("begin_drain: already draining, ignoring redundant trigger",
			"source", source,
			"event", "begindrain_duplicate")
		return false
	}
	s.drainingMu.Lock()
	s.drainingAt = time.Now()
	s.drainingMu.Unlock()
	slog.Info("begin_drain",
		"source", source,
		"event", "begindrain",
	)
	if s.orchestrator != nil {
		s.orchestrator.SetIntakePaused(true)
		s.orchestrator.MarkDraining(true)
	}
	// Mark the on-demand tracker as draining under the same lock that
	// TryAdmit uses, so no new on-demand admission can slip through
	// after BeginDrain returns. CA-142.
	if s.OnDemand != nil {
		s.OnDemand.MarkDraining()
	}
	return true
}

// IsDraining reports whether the server is in drain state.
func (s *Server) IsDraining() bool {
	if s == nil {
		return false
	}
	return s.serverDraining.Load()
}

// AwaitDrain waits until both the orchestrator in-flight count and the
// on-demand tracker reach zero, or ctx is cancelled. Returns nil when
// both queues are empty, ctx.Err() on timeout.
func (s *Server) AwaitDrain(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.orchestrator != nil {
		inFlight := s.orchestrator.InFlightCount()
		onDemand := int64(0)
		if s.OnDemand != nil {
			onDemand = s.OnDemand.Count()
		}
		slog.Info("drain await begin",
			"in_flight", inFlight,
			"on_demand", onDemand,
			"event", "drain_await_begin")

		eventCh, unsub := s.orchestrator.Subscribe()
		defer unsub()

		// progressTicker fires every 30s for operator visibility.
		progressTicker := time.NewTicker(30 * time.Second)
		defer progressTicker.Stop()
		// recheckTicker fires frequently so we don't rely solely on job events
		// to detect drain completion. The inflight counter is decremented by a
		// deferred call in runJob AFTER the final job event is published, so
		// checking the count only on event receipt can miss the decrement.
		// A 50ms poll is imperceptible in practice and resolves within a
		// sub-second window even for the last in-flight job. CA-142.
		recheckTicker := time.NewTicker(50 * time.Millisecond)
		defer recheckTicker.Stop()
		start := time.Now()

		for s.orchestrator.InFlightCount() > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-progressTicker.C:
				slog.Info("drain progress",
					"in_flight", s.orchestrator.InFlightCount(),
					"on_demand", func() int64 {
						if s.OnDemand != nil {
							return s.OnDemand.Count()
						}
						return 0
					}(),
					"elapsed_s", time.Since(start).Round(time.Second).String(),
					"event", "drain_progress")
			case <-recheckTicker.C:
				// Poll so we don't get stuck if an inflight.release fires
				// after the last job event has already been consumed.
			case <-eventCh:
				// re-check on every job event
			}
		}
	}
	if s.OnDemand != nil {
		if err := s.OnDemand.WaitZero(ctx); err != nil {
			return err
		}
	}
	return nil
}

// FinishShutdown shuts down stateful components that must outlive HTTP
// connections: the event bus and the living-wiki dispatcher. Call this
// AFTER httpServer.Shutdown and internalServer.Shutdown have returned.
// Uses its own context so component cleanup is not bounded by the
// already-spent HTTP-shutdown budget. CA-142 Medium #4.
func (s *Server) FinishShutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.eventBus != nil {
		if err := s.eventBus.Shutdown(ctx); err != nil {
			slog.Warn("event bus shutdown error", "err", err)
		}
	}
	// Living-wiki dispatcher drain — budget provided by the caller.
	if s.livingWikiDispatcher != nil {
		if err := s.livingWikiDispatcher.Stop(ctx); err != nil {
			slog.Warn("living-wiki dispatcher did not drain cleanly within timeout",
				"err", err,
			)
		}
	}
	return nil
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
		// SEC-12: 'unsafe-eval' removed after static audit (docs/security/csp-audit-2026-05-04.md).
		// The API server returns JSON/GraphQL — it never serves HTML or inline scripts,
		// so script-src directives only matter for the API's own error pages (plain text).
		// Static grep of web/src/: zero eval() / new Function() calls.
		// Mermaid 11.x (web/node_modules/mermaid/dist/): zero eval occurrences (199 chunks verified).
		// Next.js production build does not use eval; dev-mode webpack hot-reload does, but
		// that runs in the web container (separate process), not under this CSP header.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; form-action 'self' https:")
		next.ServeHTTP(w, r)
	})
}
