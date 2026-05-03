// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/capabilities"
	"github.com/sourcebridge/sourcebridge/internal/clustering"
	"github.com/sourcebridge/sourcebridge/internal/db"
	"github.com/sourcebridge/sourcebridge/internal/indexing"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/qa"
	"github.com/sourcebridge/sourcebridge/internal/search"
	"github.com/sourcebridge/sourcebridge/internal/version"
	"github.com/sourcebridge/sourcebridge/internal/worker"
	"github.com/sourcebridge/sourcebridge/internal/worker/llmcall"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
)

// ---------------------------------------------------------------------------
// MCP protocol constants
// ---------------------------------------------------------------------------

const (
	mcpProtocolVersion = "2025-11-25"
	mcpServerName      = "sourcebridge"
	mcpMaxBodySize     = 1 << 20 // 1MB
)

// mcpServerVersion returns the build-time version of the SourceBridge MCP
// server. Reads from internal/version (the same symbol /api/v1/version,
// admin/status, GraphQL Query.version, and the telemetry sender all use)
// so every visible version surface on a given binary reports the same
// string. Was a hardcoded "1.0.0" constant before CA-137.
//
// Kept as a function (not a var) so it cannot be ldflag-mutated and so
// callers can't accidentally cache a stale value at package init.
func mcpServerVersion() string { return version.Version }

// ---------------------------------------------------------------------------
// JSON-RPC types
// ---------------------------------------------------------------------------

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // may be absent for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *jsonRPCError `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// ---------------------------------------------------------------------------
// MCP content types
// ---------------------------------------------------------------------------

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type mcpToolResult struct {
	Content []mcpContent           `json:"content"`
	IsError bool                   `json:"isError,omitempty"`
	Meta    map[string]interface{} `json:"_meta,omitempty"`
}

// ---------------------------------------------------------------------------
// MCP tool definition
// ---------------------------------------------------------------------------

type mcpToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

// ---------------------------------------------------------------------------
// MCP resource definition
// ---------------------------------------------------------------------------

type mcpResourceDefinition struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ---------------------------------------------------------------------------
// Enterprise extension point interfaces
// ---------------------------------------------------------------------------

// MCPPermissionChecker validates whether a user can access a given repo via MCP.
type MCPPermissionChecker interface {
	CanAccessRepo(tenantID, userID, repoID string) bool
}

// MCPAuditLogger records MCP tool calls and resource reads for compliance.
type MCPAuditLogger interface {
	LogToolCall(tenantID, userID, toolName string, repoID string, durationMs int64, err error)
	LogResourceRead(tenantID, userID, resourceURI string, durationMs int64, err error)
}

// MCPToolExtender lets enterprise builds add extra MCP tools.
type MCPToolExtender interface {
	ExtraTools() []mcpToolDefinition
	CallTool(ctx context.Context, session *mcpSession, toolName string, args json.RawMessage) (interface{}, error)
}

// ---------------------------------------------------------------------------
// Worker interface (for testability)
// ---------------------------------------------------------------------------

// mcpWorkerCaller abstracts the worker methods used by MCP tools. The
// real implementation is mcpLLMCallerAdapter, which wraps an
// *llmcall.Caller and binds the per-call (repoID, op) at the wrapper-
// construction site. Tests inject mocks that satisfy this interface
// directly.
type mcpWorkerCaller interface {
	IsAvailable() bool
	AnswerQuestion(ctx context.Context, req *reasoningv1.AnswerQuestionRequest) (*reasoningv1.AnswerQuestionResponse, error)
}

// workerStreamingCaller is the optional streaming extension. The MCP
// explain_code tool uses it when the caller opted into progress
// notifications; otherwise it falls back to the unary call. Kept
// separate from mcpWorkerCaller so existing test mocks that only
// implement the unary path don't have to change.
type workerStreamingCaller interface {
	mcpWorkerCaller
	AnswerQuestionStream(
		ctx context.Context,
		req *reasoningv1.AnswerQuestionStreamRequest,
	) (reasoningv1.ReasoningService_AnswerQuestionStreamClient, context.CancelFunc, error)
}

// workerReviewCaller is the optional AI-review extension. The MCP
// get_review_for_diff tool uses it when include_ai_review is true;
// otherwise it falls back to structural-only output with degraded: true.
// Kept separate from mcpWorkerCaller (and workerStreamingCaller) so
// existing test mocks that don't implement ReviewFile don't have to
// change until Phase 3 when the tool lands.
type workerReviewCaller interface {
	mcpWorkerCaller
	ReviewFile(ctx context.Context, req *reasoningv1.ReviewFileRequest) (*reasoningv1.ReviewFileResponse, error)
}

// mcpLLMCallerAdapter adapts an *llmcall.Caller to the mcp* worker
// interfaces. It binds the resolver op to the MCP-specific constants so
// every RPC the MCP server makes is stamped with workspace-resolved
// metadata.
//
// repoID is supplied per-call by the MCP tool from the request payload;
// the adapter forwards it into the resolver via the Caller wrapper. We
// bind the *op* at adapter-construction time because every MCP unary
// answer goes through the explain_code tool (OpMCPExplain), every MCP
// stream goes through the discuss_stream tool (OpMCPDiscussStream), and
// every MCP file review goes through the review tool (OpMCPReview).
type mcpLLMCallerAdapter struct {
	caller   *llmcall.Caller
	unaryOp  string
	streamOp string
	reviewOp string
}

func (a *mcpLLMCallerAdapter) IsAvailable() bool {
	return a != nil && a.caller != nil && a.caller.IsAvailable()
}

func (a *mcpLLMCallerAdapter) AnswerQuestion(ctx context.Context, req *reasoningv1.AnswerQuestionRequest) (*reasoningv1.AnswerQuestionResponse, error) {
	return a.caller.AnswerQuestion(ctx, req.GetRepositoryId(), a.unaryOp, req)
}

func (a *mcpLLMCallerAdapter) AnswerQuestionStream(ctx context.Context, req *reasoningv1.AnswerQuestionStreamRequest) (reasoningv1.ReasoningService_AnswerQuestionStreamClient, context.CancelFunc, error) {
	return a.caller.AnswerQuestionStream(ctx, req.GetRepositoryId(), a.streamOp, req)
}

func (a *mcpLLMCallerAdapter) ReviewFile(ctx context.Context, req *reasoningv1.ReviewFileRequest) (*reasoningv1.ReviewFileResponse, error) {
	return a.caller.ReviewFile(ctx, req.GetRepositoryId(), a.reviewOp, req)
}

// Compile-time checks.
var _ mcpWorkerCaller = (*mcpLLMCallerAdapter)(nil)
var _ workerStreamingCaller = (*mcpLLMCallerAdapter)(nil)
var _ workerReviewCaller = (*mcpLLMCallerAdapter)(nil)

// streamDiscussion runs the server-streaming AnswerQuestionStream RPC
// and forwards each AnswerQuestionStreamResponse's content fragment to
// the given ContentEmitter. Returns a synthetic AnswerQuestionResponse
// whose `answer` field is the concatenation of all emitted deltas, so
// the caller's final MCP tool result has the same shape the unary path
// returns. The function blocks until the server sends a terminal
// frame (finished=true), io.EOF, or an error.
func streamDiscussion(
	ctx context.Context,
	caller workerStreamingCaller,
	req *reasoningv1.AnswerQuestionStreamRequest,
	emitter *ContentEmitter,
) (*reasoningv1.AnswerQuestionResponse, error) {
	stream, cancel, err := caller.AnswerQuestionStream(ctx, req)
	if err != nil {
		return nil, err
	}
	defer cancel()

	var (
		buf   strings.Builder
		final *reasoningv1.AnswerQuestionStreamResponse
	)
	for {
		delta, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return nil, recvErr
		}
		if delta.GetFinished() {
			final = delta
			break
		}
		if chunk := delta.GetContentDelta(); chunk != "" {
			buf.WriteString(chunk)
			emitter.Emit(chunk)
		}
	}

	resp := &reasoningv1.AnswerQuestionResponse{
		Answer: buf.String(),
	}
	if final != nil {
		resp.ReferencedSymbols = final.GetReferencedSymbols()
		resp.Usage = final.GetUsage()
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------------

// mcpSession is the per-request view of an MCP session. Fields mirror
// mcpSessionState (which is what the shared store persists) plus an optional
// pod-local chans pointer for SSE delivery. Dispatch handlers read and mutate
// these fields freely; mcpHandler persists changes back via sessionStore.Save
// after dispatch returns.
type mcpSession struct {
	id          string
	claims      *auth.Claims
	initialized bool
	clientInfo  struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	createdAt time.Time
	lastUsed  time.Time

	// chans is non-nil iff this session has an SSE connection anchored on
	// the current replica. Streamable-HTTP sessions always see chans == nil.
	chans *mcpLocalChans
}

// toState serializes the session for the shared store. Channels are pod-local
// and intentionally not persisted.
func (s *mcpSession) toState() *mcpSessionState {
	st := &mcpSessionState{
		ID:            s.id,
		Initialized:   s.initialized,
		ClientName:    s.clientInfo.Name,
		ClientVersion: s.clientInfo.Version,
		CreatedAt:     s.createdAt,
		LastUsed:      s.lastUsed,
	}
	if s.claims != nil {
		st.UserID = s.claims.UserID
		st.OrgID = s.claims.OrgID
		st.Email = s.claims.Email
		st.Role = s.claims.Role
	}
	return st
}

// sessionFromState reconstructs a working session from persisted state,
// attaching pod-local channels if present.
func sessionFromState(st *mcpSessionState, chans *mcpLocalChans) *mcpSession {
	sess := &mcpSession{
		id:          st.ID,
		claims:      &auth.Claims{UserID: st.UserID, OrgID: st.OrgID, Email: st.Email, Role: st.Role},
		initialized: st.Initialized,
		createdAt:   st.CreatedAt,
		lastUsed:    st.LastUsed,
		chans:       chans,
	}
	sess.clientInfo.Name = st.ClientName
	sess.clientInfo.Version = st.ClientVersion
	return sess
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

type mcpHandler struct {
	store          graphstore.GraphStore
	knowledgeStore knowledge.KnowledgeStore
	worker         mcpWorkerCaller
	indexingSvc    *indexing.Service    // Phase-3 follow-on — drives end-to-end index_repository / refresh_repository
	clusterStore   clustering.ClusterStore // subsystem clustering; nil when the store doesn't support it
	edition        capabilities.Edition // drives tools/list filtering + initialize response
	allowedRepos   map[string]bool      // nil = all repos allowed
	sessionTTL     time.Duration
	keepalive      time.Duration
	maxSessions    int

	// sessionStore persists session state (claims, initialized flag, client
	// info, timestamps). With Redis-backed storage, any replica can serve any
	// streamable-HTTP request against any session. With memory-backed storage
	// it behaves like the original single-pod map.
	sessionStore mcpSessionStore

	// localChans holds pod-local event and shutdown channels for sessions
	// that have an SSE connection anchored on this replica. SSE delivery
	// can't cross pods (it owns a TCP connection), so these channels are
	// intentionally pod-scoped. Streamable-HTTP sessions never populate
	// this map — they look state up from sessionStore on every request.
	localChans sync.Map // map[string]*mcpLocalChans

	// Enterprise extension points (nil in OSS)
	permChecker  MCPPermissionChecker
	auditLogger  MCPAuditLogger
	toolExtender MCPToolExtender

	// qaOrchestrator powers the ask_question tool when server-side QA
	// is enabled. Nil = the tool is advertised with a diagnostic
	// "server-side QA disabled" response so clients get a clear hint
	// instead of "Unknown tool".
	qaOrchestrator *qa.Orchestrator
	// qaEnabled mirrors QAConfig.ServerSideEnabled. Checked at
	// tool-call time so an operator flag-flip takes effect without a
	// restart.
	qaEnabled bool

	// searchSvc is the hybrid retrieval backbone. When non-nil,
	// callSearchSymbols routes through it for ranked results; when
	// nil, the legacy substring path is used for rollback safety.
	searchSvc *search.Service

	// freshness powers the additive _meta.freshness envelope on every
	// MCP tool response (Phase 1.C of the MCP-edits plan). When nil
	// the envelope still ships, reporting state="fresh" with no
	// timestamp — the "no change-watch wired, you're reading the
	// operator-indexed state" semantics. Wired at server-assembly time
	// against the change-watch router.
	freshness FreshnessProvider

	// changeDispatcher backs the in-process record_change MCP tool
	// (Phase 1.D of the MCP-edits plan). When nil, the tool is hidden
	// from tools/list entirely so agents don't discover a no-op tool;
	// a hand-crafted tools/call with name="record_change" against a
	// nil dispatcher returns MCPErrCapabilityDisabled (defense in
	// depth). Wired at server-assembly time when change_watch.enabled
	// is true.
	//
	// The interface decouples the rest package from concrete
	// *changewatch.Router so the same tool can be exercised in tests
	// with a stub dispatcher.
	changeDispatcher ChangeEventDispatcher

	// fileReader provides path-traversal-safe file reads keyed by repo ID.
	// Production binding is *qaFileReader (wired in router.go); tests inject
	// a stub via WithFileReader. When nil, get_symbol_source and
	// get_symbol_context return a "file reader not configured" error.
	fileReader fileReader

	// capabilityChecker is the indirection that lets tests flip a capability
	// off without mutating package globals. Defaults to capabilities.IsAvailable
	// in production wiring; tests inject a stub via WithCapabilityChecker.
	capabilityChecker capabilityCheckFunc

	// toolDispatch maps tool name → handler function. Populated at
	// construction by per-phase register*Tools functions. The dispatch
	// path in handleToolsCallCtx consults this map first; enterprise
	// extender tools fall through to toolExtender.CallTool. Drift between
	// this map and baseTools() is caught in both directions:
	//   - TestRegistry_AllMCPToolsExistInBaseTools (capability→baseTools)
	//   - TestDispatchMapCoversBaseTools (baseTools→dispatch)
	// Tools are registered into h.toolDispatch at handler construction by
	// per-phase register*Tools functions. To add a new tool: define its
	// handler, register it in the appropriate register*Tools fn, add it to
	// baseTools() (or a *ToolDefs() slice), declare its capability in
	// internal/capabilities/registry_data.go. Drift between the dispatch
	// map and baseTools() is caught both directions by
	// TestRegistry_AllMCPToolsExistInBaseTools (capability→base) and
	// TestDispatchMapCoversBaseTools (base→dispatch).
	toolDispatch map[string]mcpToolHandlerFunc
}

// fileReader is the minimal interface for reading source files from a repo
// clone. Production binding is *qaFileReader; tests can substitute a mock
// without standing up a real clone on disk.
type fileReader interface {
	ReadRepoFile(repoID, filePath string) (string, error)
}

// capabilityCheckFunc is the function type for checking whether a named
// capability is available for a given edition. Mirrors capabilities.IsAvailable.
type capabilityCheckFunc func(name string, edition capabilities.Edition) bool

// ---------------------------------------------------------------------------
// Dispatcher types and helpers
// ---------------------------------------------------------------------------

// mcpToolHandlerFunc is the uniform handler signature for all MCP tools in
// the dispatch map. ctx is the live request context (used by tools that
// need it for timeout/cancellation; ignored by the others via noCtxHandler).
type mcpToolHandlerFunc func(h *mcpHandler, ctx context.Context, session *mcpSession, args json.RawMessage) (interface{}, error)

// noCtxHandler adapts a (session, args) handler — the shape used by 24 of
// the 26 existing tools — into the dispatcher's (ctx, session, args)
// signature. The two ctx-taking tools (explain_code and ask_question)
// register their handlers directly without this adapter.
func noCtxHandler(fn func(*mcpHandler, *mcpSession, json.RawMessage) (interface{}, error)) mcpToolHandlerFunc {
	return func(h *mcpHandler, _ context.Context, s *mcpSession, a json.RawMessage) (interface{}, error) {
		return fn(h, s, a)
	}
}

// registerTool installs a handler in the dispatch map. Panics on duplicate
// registration so collisions surface at construction (which always runs in
// tests) rather than as silent overwrites where the second registration
// wins. All register*Tools functions MUST use this helper rather than direct
// map assignment.
func (h *mcpHandler) registerTool(name string, fn mcpToolHandlerFunc) {
	if _, exists := h.toolDispatch[name]; exists {
		panic(fmt.Sprintf("duplicate tool registration: %s", name))
	}
	h.toolDispatch[name] = fn
}

// Compile-time guard: ensures *qaFileReader satisfies the fileReader interface.
// A signature drift on qaFileReader is caught at build time, not runtime.
var _ fileReader = (*qaFileReader)(nil)

// mcpLocalChans holds the per-pod delivery channels for an SSE session.
type mcpLocalChans struct {
	eventCh  chan []byte
	done     chan struct{}
	doneOnce sync.Once
}

func (c *mcpLocalChans) closeDone() {
	c.doneOnce.Do(func() { close(c.done) })
}

func newMCPHandler(store graphstore.GraphStore, ks knowledge.KnowledgeStore, w mcpWorkerCaller, repos string, sessionTTL, keepalive time.Duration, maxSessions int, cache db.Cache) *mcpHandler {
	return newMCPHandlerWithEdition(store, ks, w, repos, sessionTTL, keepalive, maxSessions, cache, capabilities.EditionOSS)
}

// newMCPHandlerWithEdition is the variant used by the real server
// wiring (router.go) to thread the configured edition in. Tests use
// the edition-less constructor, which defaults to OSS.
func newMCPHandlerWithEdition(store graphstore.GraphStore, ks knowledge.KnowledgeStore, w mcpWorkerCaller, repos string, sessionTTL, keepalive time.Duration, maxSessions int, cache db.Cache, edition capabilities.Edition) *mcpHandler {
	// Choose the session store based on what the caller provided. A non-nil
	// Redis-capable cache gives us HA out of the box; anything else falls
	// back to an in-process map.
	var ss mcpSessionStore
	if _, isRedis := cache.(*db.RedisCache); isRedis {
		ss = newRedisSessionStore(cache)
		slog.Info("mcp using Redis-backed session store (HA-safe)")
	} else {
		ss = newMemorySessionStore()
		slog.Info("mcp using in-memory session store (single-pod only — set storage.redis_mode=external for HA)")
	}
	h := &mcpHandler{
		store:          store,
		knowledgeStore: ks,
		worker:         w,
		edition:        edition,
		sessionTTL:     sessionTTL,
		keepalive:      keepalive,
		maxSessions:    maxSessions,
		sessionStore:   ss,
		// indexingSvc and clusterStore are wired by router.go after
		// construction so tests without the full Server context can skip
		// them and exercise fallback paths.
	}
	// Wire clusterStore when the backing store satisfies the interface.
	if cs, ok := store.(clustering.ClusterStore); ok {
		h.clusterStore = cs
	}
	if repos != "" {
		h.allowedRepos = make(map[string]bool)
		for _, r := range strings.Split(repos, ",") {
			r = strings.TrimSpace(r)
			if r != "" {
				h.allowedRepos[r] = true
			}
		}
		// Warn about repo IDs that don't exist in the store
		for repoID := range h.allowedRepos {
			if store.GetRepository(repoID) == nil {
				slog.Warn("mcp configured repo not found in store", "repo_id", repoID)
			}
		}
	}
	// Registration must run after all field assignments above.
	// Each register*Tools function may close over handler fields (e.g.
	// h.worker, h.knowledgeStore); all must be set before this block.
	h.toolDispatch = make(map[string]mcpToolHandlerFunc)
	registerCoreTools(h)
	registerRequirementLinkingTools(h)
	registerGapAuditTools(h)
	registerFieldGuideTools(h)
	registerChangeImpactTools(h)
	registerReviewTools(h)

	// Start pod-local chans reaper — TTL cleanup of session state itself is
	// handled by sessionStore (Redis TTL, or the memory store's own reaper).
	// This loop just closes channels for SSE sessions whose persistent state
	// has expired, so the handleSSE goroutine returns.
	go h.reapLocalChans()
	return h
}

// registerCoreTools populates h.toolDispatch with all 26 existing MCP
// tools. It is called once, at the end of newMCPHandlerWithEdition, after
// every handler field is set.
//
// Convention:
//   - Tools whose handler does NOT need the live request context (24/26)
//     are wrapped via noCtxHandler, which discards the context parameter.
//   - Tools that need context for timeout/cancellation propagation
//     (explain_code, ask_question) register their handlers directly using
//     the full mcpToolHandlerFunc signature.
func registerCoreTools(h *mcpHandler) {
	// Non-ctx tools (24).
	h.registerTool("search_symbols", noCtxHandler((*mcpHandler).callSearchSymbols))
	h.registerTool("get_requirements", noCtxHandler((*mcpHandler).callGetRequirements))
	h.registerTool("get_impact_report", noCtxHandler((*mcpHandler).callGetImpactReport))
	h.registerTool("get_cliff_notes", noCtxHandler((*mcpHandler).callGetCliffNotes))
	h.registerTool("get_callers", noCtxHandler((*mcpHandler).callGetCallers))
	h.registerTool("get_callees", noCtxHandler((*mcpHandler).callGetCallees))
	h.registerTool("get_file_imports", noCtxHandler((*mcpHandler).callGetFileImports))
	h.registerTool("get_architecture_diagram", noCtxHandler((*mcpHandler).callGetArchitectureDiagram))
	h.registerTool("get_recent_changes", noCtxHandler((*mcpHandler).callGetRecentChanges))
	h.registerTool("get_tests_for_symbol", noCtxHandler((*mcpHandler).callGetTestsForSymbol))
	h.registerTool("get_entry_points", noCtxHandler((*mcpHandler).callGetEntryPoints))
	h.registerTool("get_symbol_source", noCtxHandler((*mcpHandler).callGetSymbolSource))
	h.registerTool("get_symbol_context", noCtxHandler((*mcpHandler).callGetSymbolContext))
	h.registerTool("index_repository", noCtxHandler((*mcpHandler).callIndexRepository))
	h.registerTool("get_index_status", noCtxHandler((*mcpHandler).callGetIndexStatus))
	h.registerTool("refresh_repository", noCtxHandler((*mcpHandler).callRefreshRepository))
	h.registerTool("review_diff_against_requirements", noCtxHandler((*mcpHandler).callReviewDiffAgainstRequirements))
	h.registerTool("impact_summary", noCtxHandler((*mcpHandler).callImpactSummary))
	h.registerTool("onboard_new_contributor", noCtxHandler((*mcpHandler).callOnboardNewContributor))
	h.registerTool("get_cross_repo_impact", noCtxHandler((*mcpHandler).callGetCrossRepoImpact))
	h.registerTool("get_subsystems", noCtxHandler((*mcpHandler).callGetSubsystems))
	h.registerTool("get_subsystem_by_id", noCtxHandler((*mcpHandler).callGetSubsystemByID))
	h.registerTool("get_subsystem", noCtxHandler((*mcpHandler).callGetSubsystem))
	// record_change is always registered in the dispatch map for defense-in-depth.
	// The handler itself checks h.changeDispatcher and returns MCPErrCapabilityDisabled
	// when nil. The tool is hidden from tools/list (baseTools) via
	// recordChangeToolDefIfAvailable returning nil, so agents don't discover it
	// when the dispatcher is unwired — but a hand-crafted tools/call is handled
	// gracefully rather than returning "Unknown tool".
	h.registerTool("record_change", noCtxHandler((*mcpHandler).callRecordChange))

	// Ctx-bearing tools (2): registered directly without noCtxHandler because
	// their handlers need the live request context for timeout/cancellation.
	h.registerTool("explain_code", func(h *mcpHandler, ctx context.Context, s *mcpSession, a json.RawMessage) (interface{}, error) {
		return h.callExplainCodeCtx(ctx, s, a)
	})
	h.registerTool("ask_question", func(h *mcpHandler, ctx context.Context, s *mcpSession, a json.RawMessage) (interface{}, error) {
		return h.callAskQuestion(ctx, s, a)
	})
}

// WithFileReader sets the file reader for get_symbol_source and
// get_symbol_context. Intended for test injection; production wiring
// happens directly in router.go.
func (h *mcpHandler) WithFileReader(fr fileReader) *mcpHandler {
	h.fileReader = fr
	return h
}

// WithCapabilityChecker sets the capability-check function, enabling tests
// to flip a capability on/off without mutating package globals.
// Production wiring assigns capabilities.IsAvailable directly in router.go.
func (h *mcpHandler) WithCapabilityChecker(c capabilityCheckFunc) *mcpHandler {
	h.capabilityChecker = c
	return h
}

// sessionCount returns the number of active sessions known to the store.
// Redis-backed stores may return 0 (counting is best-effort); memory stores
// return an exact count. Callers use this for maxSessions enforcement only.
func (h *mcpHandler) sessionCount() int {
	n, err := h.sessionStore.Count(context.Background())
	if err != nil {
		slog.Warn("mcp session count failed", "error", err)
		return 0
	}
	return n
}

// reapLocalChans closes the delivery channels for any SSE session whose
// persistent state has expired in the store. Without this, an SSE handler
// would block on a stale eventCh forever while the session was already gone
// from the shared store.
func (h *mcpHandler) reapLocalChans() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	ctx := context.Background()
	for range ticker.C {
		h.localChans.Range(func(key, value interface{}) bool {
			id := key.(string)
			state, err := h.sessionStore.Get(ctx, id)
			if err != nil || state == nil {
				chans := value.(*mcpLocalChans)
				chans.closeDone()
				h.localChans.Delete(id)
				slog.Info("mcp session expired", "session_id", id)
			}
			return true
		})
	}
}

// ---------------------------------------------------------------------------
// SSE endpoint: GET /api/v1/mcp/sse
// ---------------------------------------------------------------------------

func (h *mcpHandler) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Enforce max sessions
	if h.maxSessions > 0 && h.sessionCount() >= h.maxSessions {
		slog.Warn("mcp max sessions reached", "current_sessions", h.sessionCount(), "max_sessions", h.maxSessions)
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many MCP sessions"})
		return
	}

	claims := auth.GetClaims(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	chans := &mcpLocalChans{
		eventCh: make(chan []byte, 64),
		done:    make(chan struct{}),
	}
	sess := &mcpSession{
		id:        uuid.New().String(),
		claims:    claims,
		createdAt: time.Now(),
		lastUsed:  time.Now(),
		chans:     chans,
	}
	// Persist state and register pod-local delivery channels. SSE /message
	// POSTs must land on this pod to hit these channels; multi-replica
	// deployments need sticky routing for the SSE transport.
	if err := h.sessionStore.Save(r.Context(), sess.toState(), h.sessionTTL); err != nil {
		slog.Error("mcp session save failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session store unavailable"})
		return
	}
	h.localChans.Store(sess.id, chans)

	slog.Info("mcp session created", "session_id", sess.id, "user_id", claims.UserID)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Send the endpoint event so the client knows where to POST messages
	messageURL := fmt.Sprintf("/api/v1/mcp/message?sessionId=%s", sess.id)
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", messageURL)
	flusher.Flush()

	// Keepalive ticker
	keepaliveTicker := time.NewTicker(h.keepalive)
	defer keepaliveTicker.Stop()

	ctx := r.Context()
	defer func() {
		h.localChans.Delete(sess.id)
		if err := h.sessionStore.Delete(context.Background(), sess.id); err != nil {
			slog.Warn("mcp session delete failed", "session_id", sess.id, "error", err)
		}
		slog.Info("mcp session closed", "session_id", sess.id, "duration_seconds", int(time.Since(sess.createdAt).Seconds()))
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-chans.done:
			return
		case data := <-chans.eventCh:
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
			flusher.Flush()
		case <-keepaliveTicker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
			slog.Debug("mcp keepalive sent", "session_id", sess.id)
		}
	}
}

// ---------------------------------------------------------------------------
// Message endpoint: POST /api/v1/mcp/message?sessionId=...
// ---------------------------------------------------------------------------

func (h *mcpHandler) handleMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid session"})
		return
	}

	state, err := h.sessionStore.Get(r.Context(), sessionID)
	if err != nil {
		slog.Warn("mcp session load failed", "session_id", sessionID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session store unavailable"})
		return
	}
	if state == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid session"})
		return
	}
	// Pod-local channels must exist for SSE delivery. If this pod doesn't
	// hold the SSE connection (sticky routing misconfigured for multi-replica
	// deployments), we can't push the response back to the client.
	val, ok := h.localChans.Load(sessionID)
	if !ok {
		writeJSON(w, http.StatusMisdirectedRequest, map[string]string{
			"error": "session is anchored to a different replica — ensure sticky routing on the SSE endpoint",
		})
		return
	}
	chans := val.(*mcpLocalChans)
	sess := sessionFromState(state, chans)
	sess.lastUsed = time.Now()

	body, err := io.ReadAll(io.LimitReader(r.Body, mcpMaxBodySize+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}
	if len(body) > mcpMaxBodySize {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body too large"})
		return
	}

	var msg jsonRPCRequest
	if err := json.Unmarshal(body, &msg); err != nil {
		h.sendResponse(sess, errorResponse(nil, -32700, "Parse error: "+err.Error()))
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if msg.JSONRPC != "2.0" {
		h.sendResponse(sess, errorResponse(msg.ID, -32600, "Invalid request: jsonrpc must be '2.0'"))
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Notifications (no ID) don't get responses
	if msg.ID == nil || string(msg.ID) == "" || string(msg.ID) == "null" {
		// Persist lastUsed anyway so the session doesn't time out.
		_ = h.sessionStore.Save(r.Context(), sess.toState(), h.sessionTTL)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := h.safeDispatch(sess, msg)
	// Persist any state changes (initialized flag, lastUsed) back to the store.
	if err := h.sessionStore.Save(r.Context(), sess.toState(), h.sessionTTL); err != nil {
		slog.Warn("mcp session save failed", "session_id", sess.id, "error", err)
	}
	h.sendResponse(sess, resp)
	w.WriteHeader(http.StatusAccepted)
}

// sendResponseTimeout bounds how long we will block waiting for a slow SSE
// client before giving up on a single response. The SSE reader is a tight
// for/select that only waits on the network write; a slow client here means
// the TCP send buffer is saturated. Dropping after this window keeps a stuck
// client from pinning server memory indefinitely, but is long enough to
// absorb normal TCP backpressure (initial window growth, transient RTT
// spikes). See sendResponse below.
const sendResponseTimeout = 5 * time.Second

// sendResponse is a no-op for streamable-HTTP sessions (sess.chans == nil) —
// those responses flow directly in the HTTP response body from the dispatch
// caller. For SSE sessions it pushes the serialized JSON-RPC response onto
// the pod-local eventCh, blocking briefly under backpressure and terminating
// the session if the client is truly stuck.
func (h *mcpHandler) sendResponse(sess *mcpSession, resp jsonRPCResponse) {
	if sess.chans == nil {
		return
	}
	data, err := json.Marshal(resp)
	if err != nil {
		slog.Error("mcp failed to marshal response", "error", err)
		return
	}
	// Try non-blocking first: the common case is a responsive client and an
	// empty-or-nearly-empty buffer, so we avoid the timer allocation.
	select {
	case sess.chans.eventCh <- data:
		return
	case <-sess.chans.done:
		return
	default:
	}
	// Buffer was full. Block with a bounded timeout so a stuck client can't
	// silently swallow a tool response. If the timer fires, the session is
	// already in a bad state — terminate it so the client reconnects instead
	// of hanging forever waiting for a reply it will never receive.
	timer := time.NewTimer(sendResponseTimeout)
	defer timer.Stop()
	select {
	case sess.chans.eventCh <- data:
	case <-sess.chans.done:
	case <-timer.C:
		slog.Error("mcp session stalled, terminating", "session_id", sess.id, "timeout", sendResponseTimeout)
		h.terminateSession(sess)
	}
}

// terminateSession removes a session from both the shared store and the
// pod-local chans map. Safe to call from any goroutine.
func (h *mcpHandler) terminateSession(sess *mcpSession) {
	if val, loaded := h.localChans.LoadAndDelete(sess.id); loaded {
		val.(*mcpLocalChans).closeDone()
	}
	if err := h.sessionStore.Delete(context.Background(), sess.id); err != nil {
		slog.Warn("mcp session delete failed", "session_id", sess.id, "error", err)
	}
}

// ---------------------------------------------------------------------------
// Panic-safe dispatch
// ---------------------------------------------------------------------------

func (h *mcpHandler) safeDispatch(session *mcpSession, msg jsonRPCRequest) (resp jsonRPCResponse) {
	return h.safeDispatchCtx(context.Background(), session, msg)
}

// safeDispatchCtx is the context-carrying flavor of safeDispatch used
// by the streaming path. The context threads the ContentEmitter (see
// mcp_progress.go) down to tools that want to push token-level
// progress to the HTTP layer.
func (h *mcpHandler) safeDispatchCtx(ctx context.Context, session *mcpSession, msg jsonRPCRequest) (resp jsonRPCResponse) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("mcp handler panic", "method", msg.Method, "error", r)
			resp = errorResponse(msg.ID, -32603, "Internal error")
		}
	}()
	return h.dispatchCtx(ctx, session, msg)
}

// ---------------------------------------------------------------------------
// Method dispatch
// ---------------------------------------------------------------------------

// dispatchCtx is the context-carrying entry point invoked by both the
// streaming and non-streaming HTTP handlers. Only tool calls currently
// consume the context; other methods ignore it for backwards
// compatibility. (A non-context wrapper used to live here; it was
// removed when every caller was updated to pass an explicit context —
// the streaming SSE flow always has one to forward to ContentEmitter.)
func (h *mcpHandler) dispatchCtx(ctx context.Context, session *mcpSession, msg jsonRPCRequest) jsonRPCResponse {
	// initialize is always allowed (it's how you start)
	if msg.Method == "initialize" {
		return h.handleInitialize(session, msg)
	}

	// ping is always allowed
	if msg.Method == "ping" {
		return successResponse(msg.ID, struct{}{})
	}

	// All other methods require initialization
	if !session.initialized {
		slog.Warn("mcp pre-init method rejected", "session_id", session.id, "method", msg.Method)
		return errorResponse(msg.ID, -32600, "Session not initialized. Send 'initialize' first.")
	}

	switch msg.Method {
	case "tools/list":
		return h.handleToolsList(session, msg)
	case "tools/call":
		return h.handleToolsCallCtx(ctx, session, msg)
	case "resources/list":
		return h.handleResourcesList(session, msg)
	case "resources/read":
		return h.handleResourcesRead(session, msg)
	case "prompts/list":
		return h.handlePromptsList(session, msg)
	case "prompts/get":
		return h.handlePromptsGet(session, msg)
	default:
		slog.Warn("mcp method not found", "session_id", session.id, "method", msg.Method)
		return errorResponse(msg.ID, -32601, fmt.Sprintf("Method not found: %s", msg.Method))
	}
}

// ---------------------------------------------------------------------------
// initialize
// ---------------------------------------------------------------------------

func (h *mcpHandler) handleInitialize(session *mcpSession, msg jsonRPCRequest) jsonRPCResponse {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct{} `json:"capabilities"`
		ClientInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	if msg.Params != nil {
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return errorResponse(msg.ID, -32602, "Invalid params: "+err.Error())
		}
	}

	// MCP spec: server responds with the version it supports; the client
	// decides whether it can work with it. We log a mismatch but don't reject,
	// because different clients (Claude Code, Codex, Cursor, etc.) ship
	// different protocol versions and the wire format is compatible.
	if params.ProtocolVersion != "" && params.ProtocolVersion != mcpProtocolVersion {
		slog.Info("mcp protocol version negotiation",
			"session_id", session.id,
			"client_version", params.ProtocolVersion,
			"server_version", mcpProtocolVersion,
		)
	}

	session.initialized = true
	session.clientInfo.Name = params.ClientInfo.Name
	session.clientInfo.Version = params.ClientInfo.Version

	slog.Info("mcp session initialized", "session_id", session.id, "client_name", params.ClientInfo.Name, "client_version", params.ClientInfo.Version)

	return successResponse(msg.ID, map[string]interface{}{
		"protocolVersion": mcpProtocolVersion,
		"capabilities": map[string]interface{}{
			"tools":     map[string]interface{}{},
			"resources": map[string]interface{}{},
			"prompts":   map[string]interface{}{},
			// experimental.sourcebridge carries SourceBridge-specific
			// capability declarations. Vanilla MCP clients ignore the
			// extension namespace; capability-aware clients read it to
			// skip tools that aren't available, pick latency-appropriate
			// timeouts, and surface which LLM provider is configured.
			"experimental": map[string]interface{}{
				"sourcebridge": h.experimentalCapabilities(),
			},
		},
		"serverInfo": map[string]interface{}{
			"name":    mcpServerName,
			"version": mcpServerVersion(),
		},
	})
}

// experimentalCapabilities builds the SourceBridge extension block for
// the initialize response. Every field is derived from the capability
// registry — this function is the sole emit point for the initialize
// path, so surfaces don't drift.
func (h *mcpHandler) experimentalCapabilities() map[string]interface{} {
	features := capabilities.AvailableNames(h.edition)
	return map[string]interface{}{
		"edition":  string(h.edition),
		"version":  mcpServerVersion(),
		"features": features,
		"latency_classes": map[string]interface{}{
			"fast_read":    "<= 100ms",
			"search":       "100-500ms",
			"llm":          "5-30s",
			"indexing_op":  "seconds-to-minutes",
		},
	}
}

// ---------------------------------------------------------------------------
// tools/list
// ---------------------------------------------------------------------------

func (h *mcpHandler) handleToolsList(_ *mcpSession, msg jsonRPCRequest) jsonRPCResponse {
	tools := h.baseTools()
	if h.toolExtender != nil {
		tools = append(tools, h.toolExtender.ExtraTools()...)
	}
	// Capability gating — hide any tool whose gating capability isn't
	// available for this edition. Tools with no gating entry in the
	// registry (e.g. anything not yet declared there) pass through
	// untouched so the registry is additive rather than an allowlist.
	filtered := make([]mcpToolDefinition, 0, len(tools))
	for _, t := range tools {
		if cap := capabilities.MCPToolGatedBy(t.Name); cap != nil {
			if !capabilities.IsAvailable(cap.Name, h.edition) {
				continue
			}
		}
		filtered = append(filtered, t)
	}
	return successResponse(msg.ID, map[string]interface{}{"tools": filtered})
}

func (h *mcpHandler) baseTools() []mcpToolDefinition {
	tools := []mcpToolDefinition{
		{
			Name:        "search_symbols",
			Description: "Search for code symbols (functions, classes, types, variables) in a repository.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID to search"},
					"query":         map[string]interface{}{"type": "string", "description": "Search query (name or pattern)"},
					"kind":          map[string]interface{}{"type": "string", "description": "Filter by symbol kind (function, class, type, variable, etc.)"},
					"file_path":     map[string]interface{}{"type": "string", "description": "Filter to symbols in a specific file"},
					"limit":         map[string]interface{}{"type": "integer", "description": "Max results to return (default 50, max 500)"},
					"offset":        map[string]interface{}{"type": "integer", "description": "Offset for pagination"},
				},
				"required": []string{"repository_id", "query"},
			},
		},
		{
			Name:        "explain_code",
			Description: "Get an AI-generated explanation of code. Provide either inline code or a file path.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
					"code":          map[string]interface{}{"type": "string", "description": "Inline code to explain"},
					"file_path":     map[string]interface{}{"type": "string", "description": "File path within the repository to explain"},
					"question":      map[string]interface{}{"type": "string", "description": "Specific question about the code (default: 'Explain this code')"},
				},
				"required": []string{"repository_id"},
			},
		},
		{
			Name:        "get_requirements",
			Description: "List requirements tracked for a repository, optionally with their linked code symbols.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
					"include_links": map[string]interface{}{"type": "boolean", "description": "Include linked symbols for each requirement (default false)"},
					"limit":         map[string]interface{}{"type": "integer", "description": "Max results (default 50, max 500)"},
					"offset":        map[string]interface{}{"type": "integer", "description": "Offset for pagination"},
				},
				"required": []string{"repository_id"},
			},
		},
		{
			Name:        "get_impact_report",
			Description: "Get the latest change impact report for a repository, showing which files, symbols, and requirements are affected by recent changes.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
				},
				"required": []string{"repository_id"},
			},
		},
		{
			Name:        "ask_question",
			Description: "Ask a question about a repository using the server-side deep-QA orchestrator. Returns an answer, structured references, related requirements, and diagnostic telemetry. Prefer this over explain_code when the question is about the whole codebase rather than a single symbol.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id":   map[string]interface{}{"type": "string", "description": "Repository ID"},
					"question":        map[string]interface{}{"type": "string", "description": "Free-form question"},
					"mode":            map[string]interface{}{"type": "string", "enum": []string{"fast", "deep"}, "description": "Retrieval mode (default: deep for whole-repo questions)"},
					"file_path":       map[string]interface{}{"type": "string", "description": "Optional file-path pin"},
					"code":            map[string]interface{}{"type": "string", "description": "Optional inline code context"},
					"language":        map[string]interface{}{"type": "string", "description": "Language hint (go, python, typescript, ...)"},
					"conversation_id": map[string]interface{}{"type": "string", "description": "Stable id across turns"},
					"prior_messages":  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Prior conversation turns"},
				},
				"required": []string{"repository_id", "question"},
			},
		},
		{
			Name:        "get_cliff_notes",
			Description: "Get the cliff notes (AI-generated summary) for a repository, module, file, symbol, or requirement.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
					"scope_type":    map[string]interface{}{"type": "string", "enum": []string{"repository", "module", "file", "symbol", "requirement"}, "description": "Scope type (default: repository)"},
					"scope_path":    map[string]interface{}{"type": "string", "description": "Scope path (file path, module path, or symbol path like 'file.go#FuncName')"},
					"audience":      map[string]interface{}{"type": "string", "enum": []string{"beginner", "developer"}, "description": "Target audience (default: developer)"},
					"depth":         map[string]interface{}{"type": "string", "enum": []string{"summary", "medium", "deep"}, "description": "Level of detail (default: medium)"},
				},
				"required": []string{"repository_id"},
			},
		},
	}
	tools = append(tools, h.phase1aToolDefs()...)
	tools = append(tools, h.symbolSourceToolDefs()...)
	tools = append(tools, h.getTestsForSymbolToolDef())
	tools = append(tools, h.getEntryPointsToolDef())
	tools = append(tools, h.lifecycleToolDefs()...)
	tools = append(tools, h.compoundToolDefs()...)
	tools = append(tools, h.requirementToolDefs()...)
	tools = append(tools, h.gapAuditToolDefs()...)
	tools = append(tools, h.fieldGuideToolDefs()...)
	tools = append(tools, h.changeImpactToolDefs()...)
	tools = append(tools, h.reviewToolDefs()...)
	tools = append(tools, h.crossRepoToolDef())
	tools = append(tools, h.clusteringToolDefs()...)
	// Phase 1.D — record_change. Only surfaced when the change-watch
	// dispatcher is wired at server-assembly time (i.e.,
	// change_watch.enabled=true). Hidden otherwise so agents don't
	// discover a no-op tool.
	if def := h.recordChangeToolDefIfAvailable(); def != nil {
		tools = append(tools, *def)
	}
	return tools
}

// ---------------------------------------------------------------------------
// tools/call
// ---------------------------------------------------------------------------

// handleToolsCallCtx is the context-aware variant. The context
// carries the ContentEmitter from the streaming HTTP handler so
// slow tools (explain_code today, get_cliff_notes next) can push
// token-level deltas back up to the SSE response.
func (h *mcpHandler) handleToolsCallCtx(ctx context.Context, session *mcpSession, msg jsonRPCRequest) jsonRPCResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return errorResponse(msg.ID, -32602, "Invalid params: "+err.Error())
	}

	slog.Info("mcp tool call started", "session_id", session.id, "tool_name", params.Name)
	start := time.Now()

	var result interface{}
	var toolErr error

	if fn, ok := h.toolDispatch[params.Name]; ok {
		result, toolErr = fn(h, ctx, session, params.Arguments)
	} else if h.toolExtender != nil {
		// MCPToolExtender method is CallTool (verified at mcp.go:131-134) — NOT HandleToolCall.
		result, toolErr = h.toolExtender.CallTool(ctx, session, params.Name, params.Arguments)
	} else {
		return errorResponse(msg.ID, -32601, fmt.Sprintf("Unknown tool: %s", params.Name))
	}

	elapsed := time.Since(start)
	slog.Info("mcp tool call completed", "session_id", session.id, "tool_name", params.Name, "duration_ms", elapsed.Milliseconds())

	if h.auditLogger != nil {
		repoID := extractRepoID(params.Arguments)
		h.auditLogger.LogToolCall(session.claims.OrgID, session.claims.UserID, params.Name, repoID, elapsed.Milliseconds(), toolErr)
	}

	// Build the additive _meta.freshness envelope. The repoID is best-
	// effort extracted from the tool arguments; tools that don't carry
	// repository_id (cross-repo or system-level tools) get the default-
	// fresh envelope. The envelope ships on every response — error and
	// success alike — so MCP consumers can rely on the contract being
	// uniform.
	repoIDForFreshness := extractRepoID(params.Arguments)
	freshness := freshnessEnvelope(h.freshness, repoIDForFreshness)

	if toolErr != nil {
		errMeta := toolErrorMeta(toolErr)
		if errMeta == nil {
			errMeta = make(map[string]interface{})
		}
		errMeta["freshness"] = freshness
		return successResponse(msg.ID, mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: toolErr.Error()}},
			IsError: true,
			Meta:    errMeta,
		})
	}

	// Marshal the result to JSON text for the MCP content
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return successResponse(msg.ID, mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: "Failed to serialize result"}},
			IsError: true,
			Meta: map[string]interface{}{
				"freshness": freshness,
			},
		})
	}

	return successResponse(msg.ID, mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: string(resultJSON)}},
		Meta: map[string]interface{}{
			"freshness": freshness,
		},
	})
}

// extractRepoID is a best-effort extraction of repository_id from tool arguments for audit logging.
func extractRepoID(args json.RawMessage) string {
	var parsed struct {
		RepositoryID string `json:"repository_id"`
	}
	_ = json.Unmarshal(args, &parsed)
	return parsed.RepositoryID
}

// ---------------------------------------------------------------------------
// Tool: search_symbols
// ---------------------------------------------------------------------------

func (h *mcpHandler) callSearchSymbols(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
		Query        string `json:"query"`
		Kind         string `json:"kind"`
		FilePath     string `json:"file_path"`
		Limit        int    `json:"limit"`
		Offset       int    `json:"offset"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	if params.Limit <= 0 {
		params.Limit = 50
	}
	if params.Limit > 500 {
		params.Limit = 500
	}

	// Route through the hybrid retrieval service when wired. The MCP
	// transport keeps the symbol-only envelope shape (plan §Phase 3 —
	// "preserve the symbol-only outward MCP result shape").
	if h.searchSvc != nil && params.FilePath == "" && params.Query != "" {
		return h.searchSymbolsHybrid(session, params)
	}

	var kindPtr *string
	if params.Kind != "" {
		kindPtr = &params.Kind
	}
	var queryPtr *string
	if params.Query != "" {
		queryPtr = &params.Query
	}

	var symbols []*graphstore.StoredSymbol
	var total int

	if params.FilePath != "" {
		// Filter by file
		fileSymbols := h.store.GetSymbolsByFile(params.RepositoryID, params.FilePath)
		// Apply query and kind filtering manually
		for _, s := range fileSymbols {
			if queryPtr != nil && !strings.Contains(strings.ToLower(s.Name), strings.ToLower(*queryPtr)) {
				continue
			}
			if kindPtr != nil && s.Kind != *kindPtr {
				continue
			}
			symbols = append(symbols, s)
		}
		total = len(symbols)
		// Apply pagination
		if params.Offset > 0 && params.Offset < len(symbols) {
			symbols = symbols[params.Offset:]
		} else if params.Offset >= len(symbols) {
			symbols = nil
		}
		if len(symbols) > params.Limit {
			symbols = symbols[:params.Limit]
		}
	} else {
		symbols, total = h.store.GetSymbols(params.RepositoryID, queryPtr, kindPtr, params.Limit, params.Offset)
	}

	return packSymbolResults(symbols, total), nil
}

// symbolSearchParams narrows the callSearchSymbols args for the
// hybrid branch without pulling the full type declaration to the
// package scope.
type symbolSearchParams = struct {
	RepositoryID string `json:"repository_id"`
	Query        string `json:"query"`
	Kind         string `json:"kind"`
	FilePath     string `json:"file_path"`
	Limit        int    `json:"limit"`
	Offset       int    `json:"offset"`
}

// searchSymbolsHybrid routes the MCP search_symbols call through the
// hybrid retrieval service, projecting each result back into the
// symbol-only envelope that existing MCP clients (including Claude
// Code) expect.
func (h *mcpHandler) searchSymbolsHybrid(session *mcpSession, params symbolSearchParams) (interface{}, error) {
	ctx := context.Background()
	req := &search.Request{
		Repo:  params.RepositoryID,
		Query: params.Query,
		Limit: params.Limit,
		Filters: search.Filters{
			Kind:     params.Kind,
			FilePath: params.FilePath,
		},
	}
	resp, err := h.searchSvc.Search(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("search: %v", err)
	}
	// Offset is applied client-side since the hybrid service uses
	// cursor-style pagination via its stable tie-break ordering. MCP
	// v1 keeps offset semantics for contract compatibility.
	offset := params.Offset
	results := resp.Results
	if offset > 0 {
		if offset >= len(results) {
			results = nil
		} else {
			results = results[offset:]
		}
	}
	if params.Limit > 0 && len(results) > params.Limit {
		results = results[:params.Limit]
	}
	syms := make([]*graphstore.StoredSymbol, 0, len(results))
	for _, r := range results {
		if r.Symbol != nil {
			syms = append(syms, r.Symbol)
			continue
		}
		if s := h.store.GetSymbol(r.EntityID); s != nil {
			syms = append(syms, s)
		}
	}
	return packSymbolResults(syms, len(resp.Results)), nil
}

// packSymbolResults builds the stable JSON envelope returned to MCP
// clients. Kept in one place so the hybrid and legacy paths emit
// identical shapes.
func packSymbolResults(symbols []*graphstore.StoredSymbol, total int) map[string]interface{} {
	type symbolResult struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		FilePath string `json:"file_path"`
		Line     int    `json:"line"`
		EndLine  int    `json:"end_line,omitempty"`
	}
	results := make([]symbolResult, 0, len(symbols))
	for _, s := range symbols {
		if s == nil {
			continue
		}
		results = append(results, symbolResult{
			ID:       s.ID,
			Name:     s.Name,
			Kind:     s.Kind,
			FilePath: s.FilePath,
			Line:     s.StartLine,
			EndLine:  s.EndLine,
		})
	}
	return map[string]interface{}{
		"symbols":     results,
		"total_count": total,
	}
}

// ---------------------------------------------------------------------------
// Tool: explain_code
// ---------------------------------------------------------------------------

// (Note: a non-context callExplainCode wrapper used to live here.
// All callers now invoke callExplainCodeCtx directly through
// handleToolsCallCtx; the wrapper was removed when nothing in the
// dispatch chain still needed the context-less signature.)

// ---------------------------------------------------------------------------
// Tool: ask_question
// ---------------------------------------------------------------------------

// callAskQuestion dispatches to the server-side QA orchestrator. Unlike
// explain_code (which is symbol-scoped), ask_question is whole-repo
// orchestrated retrieval + synthesis, with structured references and
// diagnostics. Returns a graceful 503-style payload when the flag is
// off so MCP clients get a hint, not an "Unknown tool" error.
func (h *mcpHandler) callAskQuestion(ctx context.Context, session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID   string   `json:"repository_id"`
		Question       string   `json:"question"`
		Mode           string   `json:"mode"`
		FilePath       string   `json:"file_path"`
		Code           string   `json:"code"`
		Language       string   `json:"language"`
		ConversationID string   `json:"conversation_id"`
		PriorMessages  []string `json:"prior_messages"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}
	if !h.qaEnabled || h.qaOrchestrator == nil {
		return map[string]interface{}{
			"answer":        "Server-side QA is disabled on this deployment. Ask the operator to set SOURCEBRIDGE_QA_SERVER_SIDE_ENABLED=true.",
			"error_kind":    "qa_disabled",
			"references":    []interface{}{},
			"diagnostics":   map[string]interface{}{"fallbackUsed": "server_side_disabled"},
		}, nil
	}
	mode := qa.Mode(strings.ToLower(params.Mode))
	if mode == "" {
		// Deep is the whole-repo default for MCP clients; fast is only
		// meaningful when the caller has supplied a file/code pin.
		if params.FilePath != "" || params.Code != "" {
			mode = qa.ModeFast
		} else {
			mode = qa.ModeDeep
		}
	}
	in := qa.AskInput{
		RepositoryID:   params.RepositoryID,
		Question:       params.Question,
		Mode:           mode,
		FilePath:       params.FilePath,
		Code:           params.Code,
		Language:       params.Language,
		ConversationID: params.ConversationID,
		PriorMessages:  params.PriorMessages,
	}

	// Phase 2.5 progress emission. When the caller sent a
	// _meta.progressToken (streamable-HTTP path), a ContentEmitter is
	// bound to the context. Emit a bracketing set of phase markers
	// around the orchestrator call so the client can show
	// "searching…" / "synthesizing…" instead of a 15–30s blank wait.
	// These are now REAL events from the agentic loop. When a
	// ContentEmitter is bound to the context (streamable-HTTP path),
	// the adapter below attaches a qa.ProgressEmitter so the loop
	// pushes structured phase events (planning / tool_call /
	// tool_result / synthesizing / done) to the streaming client.
	emitter := ContentEmitterFromContext(ctx)
	if emitter != nil {
		ctx = qa.WithProgressEmitter(ctx, &contentEmitterProgressAdapter{emitter: emitter})
		// Mode hint up front so the client knows which pipeline is
		// running before any loop events arrive.
		if mode == qa.ModeFast {
			emitter.Emit("[ask_question] mode=fast — pinned to provided context\n")
		} else {
			emitter.Emit("[ask_question] mode=deep — agentic retrieval + synthesis\n")
		}
	}

	res, err := h.qaOrchestrator.Ask(ctx, in)
	if err != nil {
		if emitter != nil {
			emitter.Emit("[ask_question] failed\n")
		}
		return nil, err
	}
	return res, nil
}

// contentEmitterProgressAdapter bridges qa.ProgressEmitter → MCP
// ContentEmitter. Each structured ProgressEvent renders to a single
// line via qa.ProgressEventString and is pushed to the streaming
// client as a content delta.
type contentEmitterProgressAdapter struct {
	emitter *ContentEmitter
}

func (a *contentEmitterProgressAdapter) Emit(event qa.ProgressEvent) {
	if a == nil || a.emitter == nil {
		return
	}
	a.emitter.Emit(qa.ProgressEventString(event))
}

// ContentEmitter is present on the context (i.e. the request came in
// through handleStreamingToolCall with a progressToken), we open the
// worker's AnswerQuestionStream RPC and forward each delta up to the
// HTTP layer. When no emitter is present, we fall back to the unary
// AnswerQuestion call so non-streaming callers still get exactly the
// same final payload.
func (h *mcpHandler) callExplainCodeCtx(ctx context.Context, session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
		Code         string `json:"code"`
		FilePath     string `json:"file_path"`
		Question     string `json:"question"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	if h.worker == nil || !h.worker.IsAvailable() {
		return nil, fmt.Errorf("AI worker is not connected. The explain_code tool requires a running worker.")
	}

	question := params.Question
	if question == "" {
		question = "Explain this code"
	}

	code := params.Code
	if code == "" && params.FilePath != "" {
		// Read source from the repository's indexed files
		repo := h.store.GetRepository(params.RepositoryID)
		if repo == nil {
			return nil, fmt.Errorf("Repository not found or not accessible")
		}
		// Get symbols from the file to provide context
		fileSymbols := h.store.GetSymbolsByFile(params.RepositoryID, params.FilePath)
		if len(fileSymbols) > 0 {
			// Build context from symbol signatures and doc comments
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("// File: %s\n", params.FilePath))
			for _, s := range fileSymbols {
				if s.DocComment != "" {
					sb.WriteString(s.DocComment)
					sb.WriteString("\n")
				}
				if s.Signature != "" {
					sb.WriteString(s.Signature)
					sb.WriteString("\n\n")
				}
			}
			code = sb.String()
		}
		if code == "" {
			return nil, fmt.Errorf("Could not read source file: %s. Repository may need reindexing.", params.FilePath)
		}
	}

	if code == "" {
		return nil, fmt.Errorf("Either 'code' or 'file_path' must be provided")
	}

	// Build the question with the code context
	fullQuestion := fmt.Sprintf("%s\n\n```\n%s\n```", question, code)

	emitter := ContentEmitterFromContext(ctx)

	// Prefer the streaming path when the caller is listening for
	// deltas (progress token present in the MCP request). Otherwise
	// fall back to the unary RPC so non-streaming callers get the
	// exact same payload they would have before.
	streamingClient, ok := h.worker.(workerStreamingCaller)
	if emitter == nil || !ok {
		unaryReq := &reasoningv1.AnswerQuestionRequest{
			Question:     fullQuestion,
			RepositoryId: params.RepositoryID,
		}
		unaryCtx, cancel := context.WithTimeout(context.Background(), worker.TimeoutDiscussion)
		defer cancel()
		// llmcall:allow — h.worker is the mcpWorkerCaller interface,
		// satisfied in production by *mcpLLMCallerAdapter which delegates
		// to *llmcall.Caller. Lint heuristic can't see through the
		// interface, so we allowlist this single line. Tests inject
		// their own mocks satisfying mcpWorkerCaller; both paths go
		// through llmcall.Caller for real RPCs.
		resp, err := h.worker.AnswerQuestion(unaryCtx, unaryReq)
		if err != nil {
			return nil, fmt.Errorf("AI worker timed out or failed: %v", err)
		}
		return map[string]interface{}{
			"explanation": resp.GetAnswer(),
		}, nil
	}

	streamReq := &reasoningv1.AnswerQuestionStreamRequest{
		Question:     fullQuestion,
		RepositoryId: params.RepositoryID,
	}
	resp, err := streamDiscussion(ctx, streamingClient, streamReq, emitter)
	if err != nil {
		return nil, fmt.Errorf("AI worker timed out or failed: %v", err)
	}

	return map[string]interface{}{
		"explanation": resp.GetAnswer(),
	}, nil
}

// ---------------------------------------------------------------------------
// Tool: get_requirements
// ---------------------------------------------------------------------------

func (h *mcpHandler) callGetRequirements(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
		IncludeLinks bool   `json:"include_links"`
		Limit        int    `json:"limit"`
		Offset       int    `json:"offset"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	if params.Limit <= 0 {
		params.Limit = 50
	}
	if params.Limit > 500 {
		params.Limit = 500
	}

	reqs, total := h.store.GetRequirements(params.RepositoryID, params.Limit, params.Offset)

	type linkInfo struct {
		SymbolID   string  `json:"symbol_id"`
		SymbolName string  `json:"symbol_name"`
		FilePath   string  `json:"file_path"`
		Confidence float64 `json:"confidence"`
	}
	type reqResult struct {
		ID          string     `json:"id"`
		ExternalID  string     `json:"external_id,omitempty"`
		Title       string     `json:"title"`
		Description string     `json:"description,omitempty"`
		Priority    string     `json:"priority,omitempty"`
		Tags        []string   `json:"tags,omitempty"`
		Links       []linkInfo `json:"links,omitempty"`
	}

	results := make([]reqResult, 0, len(reqs))
	for _, req := range reqs {
		r := reqResult{
			ID:          req.ID,
			ExternalID:  req.ExternalID,
			Title:       req.Title,
			Description: req.Description,
			Priority:    req.Priority,
			Tags:        req.Tags,
		}
		if params.IncludeLinks {
			links := h.store.GetLinksForRequirement(req.ID, false)
			// Batch lookup symbol names
			symIDs := make([]string, 0, len(links))
			for _, l := range links {
				symIDs = append(symIDs, l.SymbolID)
			}
			symMap := h.store.GetSymbolsByIDs(symIDs)

			for _, l := range links {
				li := linkInfo{
					SymbolID:   l.SymbolID,
					Confidence: l.Confidence,
				}
				if s, ok := symMap[l.SymbolID]; ok {
					li.SymbolName = s.Name
					li.FilePath = s.FilePath
				}
				r.Links = append(r.Links, li)
			}
		}
		results = append(results, r)
	}

	return map[string]interface{}{
		"requirements": results,
		"total_count":  total,
	}, nil
}

// ---------------------------------------------------------------------------
// Tool: get_impact_report
// ---------------------------------------------------------------------------

func (h *mcpHandler) callGetImpactReport(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	report := h.store.GetLatestImpactReport(params.RepositoryID)
	if report == nil {
		return map[string]interface{}{"report": nil}, nil
	}

	return map[string]interface{}{
		"report": map[string]interface{}{
			"id":                    report.ID,
			"old_commit_sha":        report.OldCommitSHA,
			"new_commit_sha":        report.NewCommitSHA,
			"files_changed":         len(report.FilesChanged),
			"symbols_added":         len(report.SymbolsAdded),
			"symbols_modified":      len(report.SymbolsModified),
			"symbols_removed":       len(report.SymbolsRemoved),
			"affected_links":        len(report.AffectedLinks),
			"affected_requirements": len(report.AffectedRequirements),
			"stale_artifacts":       len(report.StaleArtifacts),
			"computed_at":           report.ComputedAt.Format(time.RFC3339),
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Tool: get_cliff_notes
// ---------------------------------------------------------------------------

func (h *mcpHandler) callGetCliffNotes(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
		ScopeType    string `json:"scope_type"`
		ScopePath    string `json:"scope_path"`
		Audience     string `json:"audience"`
		Depth        string `json:"depth"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	if h.knowledgeStore == nil {
		return nil, fmt.Errorf("Knowledge store is not configured. Cliff notes require knowledge persistence.")
	}

	// Apply defaults
	scopeType := knowledge.ScopeType(params.ScopeType)
	if scopeType == "" {
		scopeType = knowledge.ScopeRepository
	}
	audience := knowledge.Audience(params.Audience)
	if audience == "" {
		audience = knowledge.AudienceDeveloper
	}
	depth := knowledge.Depth(params.Depth)
	if depth == "" {
		depth = knowledge.DepthMedium
	}

	key := knowledge.ArtifactKey{
		RepositoryID: params.RepositoryID,
		Type:         knowledge.ArtifactCliffNotes,
		Audience:     audience,
		Depth:        depth,
		Scope: knowledge.ArtifactScope{
			ScopeType: scopeType,
			ScopePath: params.ScopePath,
		},
	}

	artifact := h.knowledgeStore.GetArtifactByKey(key)
	if artifact == nil {
		return map[string]interface{}{
			"artifact": nil,
			"message":  "No cliff notes have been generated for this scope yet.",
		}, nil
	}

	if artifact.Status == knowledge.StatusGenerating {
		return map[string]interface{}{
			"artifact": nil,
			"message":  "Cliff notes are currently being generated. Please try again in a moment.",
		}, nil
	}

	if artifact.Status != knowledge.StatusReady {
		return map[string]interface{}{
			"artifact": nil,
			"message":  fmt.Sprintf("Cliff notes are in '%s' state.", artifact.Status),
		}, nil
	}

	type sectionResult struct {
		Title      string `json:"title"`
		Content    string `json:"content"`
		Summary    string `json:"summary,omitempty"`
		Confidence string `json:"confidence"`
	}

	sections := make([]sectionResult, 0, len(artifact.Sections))
	for _, s := range artifact.Sections {
		sections = append(sections, sectionResult{
			Title:      s.Title,
			Content:    s.Content,
			Summary:    s.Summary,
			Confidence: string(s.Confidence),
		})
	}

	return map[string]interface{}{
		"artifact": map[string]interface{}{
			"id":           artifact.ID,
			"scope_type":   string(artifact.Scope.ScopeType),
			"scope_path":   artifact.Scope.ScopePath,
			"audience":     string(artifact.Audience),
			"depth":        string(artifact.Depth),
			"stale":        artifact.Stale,
			"generated_at": artifact.GeneratedAt.Format(time.RFC3339),
			"sections":     sections,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// resources/list
// ---------------------------------------------------------------------------

func (h *mcpHandler) handleResourcesList(session *mcpSession, msg jsonRPCRequest) jsonRPCResponse {
	repos := h.store.ListRepositories()
	resources := make([]mcpResourceDefinition, 0)

	for _, repo := range repos {
		if !h.isRepoAllowed(session, repo.ID) {
			continue
		}
		resources = append(resources, mcpResourceDefinition{
			URI:         fmt.Sprintf("repository://%s/files", repo.ID),
			Name:        fmt.Sprintf("%s — Files", repo.Name),
			Description: "List of indexed files in the repository",
			MimeType:    "application/json",
		})
		resources = append(resources, mcpResourceDefinition{
			URI:         fmt.Sprintf("repository://%s/symbols", repo.ID),
			Name:        fmt.Sprintf("%s — Symbols", repo.Name),
			Description: "List of indexed code symbols in the repository",
			MimeType:    "application/json",
		})
	}

	return successResponse(msg.ID, map[string]interface{}{"resources": resources})
}

// ---------------------------------------------------------------------------
// resources/read
// ---------------------------------------------------------------------------

func (h *mcpHandler) handleResourcesRead(session *mcpSession, msg jsonRPCRequest) jsonRPCResponse {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return errorResponse(msg.ID, -32602, "Invalid params: "+err.Error())
	}

	start := time.Now()

	// Parse URI: repository://{repoID}/{type}
	if !strings.HasPrefix(params.URI, "repository://") {
		return errorResponse(msg.ID, -32602, "Invalid resource URI: must start with repository://")
	}
	rest := strings.TrimPrefix(params.URI, "repository://")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		return errorResponse(msg.ID, -32602, "Invalid resource URI format: expected repository://{id}/{type}")
	}
	repoID := parts[0]
	resourceType := parts[1]

	if err := h.checkRepoAccess(session, repoID); err != nil {
		return errorResponse(msg.ID, -32602, err.Error())
	}

	var content interface{}
	var readErr error

	switch resourceType {
	case "files":
		content, readErr = h.readFilesResource(repoID)
	case "symbols":
		content, readErr = h.readSymbolsResource(repoID)
	default:
		return errorResponse(msg.ID, -32602, fmt.Sprintf("Unknown resource type: %s", resourceType))
	}

	elapsed := time.Since(start)
	slog.Info("mcp resource read", "session_id", session.id, "resource_uri", params.URI, "duration_ms", elapsed.Milliseconds())

	if h.auditLogger != nil {
		h.auditLogger.LogResourceRead(session.claims.OrgID, session.claims.UserID, params.URI, elapsed.Milliseconds(), readErr)
	}

	if readErr != nil {
		return errorResponse(msg.ID, -32603, readErr.Error())
	}

	contentJSON, _ := json.Marshal(content)
	return successResponse(msg.ID, map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"uri":      params.URI,
				"mimeType": "application/json",
				"text":     string(contentJSON),
			},
		},
	})
}

func (h *mcpHandler) readFilesResource(repoID string) (interface{}, error) {
	files := h.store.GetFiles(repoID)
	if files == nil {
		return nil, fmt.Errorf("repository not found or not indexed")
	}

	type fileEntry struct {
		Path     string `json:"path"`
		Language string `json:"language,omitempty"`
	}

	result := make([]fileEntry, 0, len(files))
	for _, f := range files {
		result = append(result, fileEntry{
			Path:     f.Path,
			Language: f.Language,
		})
	}
	return result, nil
}

func (h *mcpHandler) readSymbolsResource(repoID string) (interface{}, error) {
	symbols, _ := h.store.GetSymbols(repoID, nil, nil, 1000, 0)
	if symbols == nil {
		return nil, fmt.Errorf("repository not found or not indexed")
	}

	type symbolEntry struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		FilePath string `json:"file_path"`
		Line     int    `json:"line"`
	}

	result := make([]symbolEntry, 0, len(symbols))
	for _, s := range symbols {
		result = append(result, symbolEntry{
			ID:       s.ID,
			Name:     s.Name,
			Kind:     s.Kind,
			FilePath: s.FilePath,
			Line:     s.StartLine,
		})
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Repo access helpers
// ---------------------------------------------------------------------------

func (h *mcpHandler) checkRepoAccess(session *mcpSession, repoID string) error {
	if repoID == "" {
		return fmt.Errorf("repository_id is required")
	}
	repo := h.store.GetRepository(repoID)
	if repo == nil {
		return fmt.Errorf("Repository not found or not accessible")
	}
	if !h.isRepoAllowed(session, repoID) {
		return fmt.Errorf("Repository not found or not accessible")
	}
	// Enterprise permission check
	if h.permChecker != nil {
		if !h.permChecker.CanAccessRepo(session.claims.OrgID, session.claims.UserID, repoID) {
			return fmt.Errorf("Repository not found or not accessible")
		}
	}
	return nil
}

func (h *mcpHandler) isRepoAllowed(_ *mcpSession, repoID string) bool {
	if h.allowedRepos == nil {
		return true // all repos allowed
	}
	return h.allowedRepos[repoID]
}

// ---------------------------------------------------------------------------
// Streamable HTTP endpoint: POST /api/v1/mcp/http
// ---------------------------------------------------------------------------
// Implements the MCP Streamable HTTP transport. Unlike the SSE transport,
// clients POST JSON-RPC messages to a single endpoint and receive JSON
// responses directly. Session tracking uses the Mcp-Session-Id header.

func (h *mcpHandler) handleStreamableHTTP(w http.ResponseWriter, r *http.Request) {
	claims := auth.GetClaims(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, mcpMaxBodySize+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}
	if len(body) > mcpMaxBodySize {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body too large"})
		return
	}

	var msg jsonRPCRequest
	if err := json.Unmarshal(body, &msg); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(errorResponse(nil, -32700, "Parse error: "+err.Error()))
		return
	}

	if msg.JSONRPC != "2.0" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(errorResponse(msg.ID, -32600, "Invalid request: jsonrpc must be '2.0'"))
		return
	}

	// For initialize: create a new session
	if msg.Method == "initialize" {
		if h.maxSessions > 0 && h.sessionCount() >= h.maxSessions {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many MCP sessions"})
			return
		}
		sess := &mcpSession{
			id:        uuid.New().String(),
			claims:    claims,
			createdAt: time.Now(),
			lastUsed:  time.Now(),
			// chans intentionally nil — streamable HTTP is synchronous
			// request/response with no pod-local delivery channel.
		}
		resp := h.safeDispatch(sess, msg)
		// Persist initialized state + client info that dispatch just set.
		if err := h.sessionStore.Save(r.Context(), sess.toState(), h.sessionTTL); err != nil {
			slog.Error("mcp streamable session save failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session store unavailable"})
			return
		}
		slog.Info("mcp streamable session created", "session_id", sess.id, "user_id", claims.UserID)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", sess.id)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Notifications (no ID) — acknowledge and done
	if msg.ID == nil || string(msg.ID) == "" || string(msg.ID) == "null" {
		// Look up session to update lastUsed if present
		if sid := r.Header.Get("Mcp-Session-Id"); sid != "" {
			if state, err := h.sessionStore.Get(r.Context(), sid); err == nil && state != nil {
				state.LastUsed = time.Now()
				_ = h.sessionStore.Save(r.Context(), state, h.sessionTTL)
			}
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// All other methods: require session
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(errorResponse(msg.ID, -32600, "Missing Mcp-Session-Id header. Send 'initialize' first."))
		return
	}
	state, err := h.sessionStore.Get(r.Context(), sessionID)
	if err != nil {
		slog.Warn("mcp session load failed", "session_id", sessionID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session store unavailable"})
		return
	}
	if state == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(errorResponse(msg.ID, -32600, "Invalid or expired session. Re-initialize."))
		return
	}
	sess := sessionFromState(state, nil)
	sess.lastUsed = time.Now()

	// Slow tool calls on clients that accept SSE take the streaming path so
	// we can emit progress notifications while the worker runs. Everything
	// else gets a synchronous JSON response.
	if toolCallShouldStream(r, msg) {
		h.handleStreamingToolCall(w, r, sess, msg, sess.id)
		// Persist lastUsed even when streaming; initialized can't change
		// on a tools/call so we don't need the full save dance.
		state.LastUsed = time.Now()
		_ = h.sessionStore.Save(context.Background(), state, h.sessionTTL)
		return
	}

	resp := h.safeDispatch(sess, msg)
	if err := h.sessionStore.Save(r.Context(), sess.toState(), h.sessionTTL); err != nil {
		slog.Warn("mcp session save failed", "session_id", sess.id, "error", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Mcp-Session-Id", sess.id)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// handleStreamableHTTPDelete handles DELETE requests to terminate sessions.
func (h *mcpHandler) handleStreamableHTTPDelete(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if val, ok := h.localChans.LoadAndDelete(sessionID); ok {
		val.(*mcpLocalChans).closeDone()
	}
	if err := h.sessionStore.Delete(r.Context(), sessionID); err != nil {
		slog.Warn("mcp session delete failed", "session_id", sessionID, "error", err)
	}
	slog.Info("mcp streamable session terminated", "session_id", sessionID)
	w.WriteHeader(http.StatusOK)
}

// ---------------------------------------------------------------------------
// JSON-RPC helpers
// ---------------------------------------------------------------------------

func successResponse(id json.RawMessage, result interface{}) jsonRPCResponse {
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
}

func errorResponse(id json.RawMessage, code int, message string) jsonRPCResponse {
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonRPCError{Code: code, Message: message},
	}
}
