// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package worker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	contractsv1 "github.com/sourcebridge/sourcebridge/gen/go/contracts/v1"
	enterprisev1 "github.com/sourcebridge/sourcebridge/gen/go/enterprise/v1"
	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	linkingv1 "github.com/sourcebridge/sourcebridge/gen/go/linking/v1"
	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	requirementsv1 "github.com/sourcebridge/sourcebridge/gen/go/requirements/v1"
	"github.com/sourcebridge/sourcebridge/internal/worker/tlsreload"
)

// Timeout presets for different operation classes.
const (
	TimeoutHealth     = 3 * time.Second
	TimeoutEmbedding  = 60 * time.Second // cold-start local Ollama needs a few seconds per batch; 10s cut off real users
	TimeoutAnalysis   = 120 * time.Second
	TimeoutDiscussion = 120 * time.Second
	TimeoutReview     = 120 * time.Second
	TimeoutLinkItem   = 30 * time.Second
	TimeoutLinkTotal  = 600 * time.Second
	TimeoutParse      = 60 * time.Second
	TimeoutEnrich     = 120 * time.Second
	TimeoutExtraction = 300 * time.Second
	TimeoutSimulation = 30 * time.Second
	// TimeoutKnowledge is the uniform legacy timeout for knowledge-generation
	// RPCs. Prefer timeoutForKnowledgeScope for callers that know the scope.
	TimeoutKnowledge = 3600 * time.Second
	TimeoutContracts = 120 * time.Second
)

// Per-scope knowledge timeouts. Repository-level generations may legitimately
// run for many minutes on large codebases; file/symbol scopes should never
// need more than a couple of minutes, and a stuck call shouldn't hold the
// worker's attention past that.
const (
	// Repo-level DEEP cliff notes on large codebases with local models can
	// take 45-60+ minutes (measured: qwen3:32b at 48 min, qwen3.5:35b-a3b at
	// 55 min, qwen3.6:35b-a3b at 42 min). The previous 30-minute ceiling was
	// killing real completions on Mac Studio hardware. 60 minutes lets every
	// dense model up through 70B finish while still catching runaway 100B+
	// MoE loads that are operationally too slow anyway.
	TimeoutKnowledgeRepository = 3600 * time.Second
	TimeoutKnowledgeModule     = 600 * time.Second
	TimeoutKnowledgeFile       = 300 * time.Second
	TimeoutKnowledgeSymbol     = 300 * time.Second
	TimeoutKnowledgeDefault    = 600 * time.Second
)

// timeoutForKnowledgeScope returns an appropriate worker timeout for a given
// knowledge generation scope. Unknown scopes fall back to
// TimeoutKnowledgeDefault so a typo in the scope string cannot silently
// extend or shrink the timeout beyond safe bounds.
func timeoutForKnowledgeScope(scopeType string) time.Duration {
	switch strings.ToLower(strings.TrimSpace(scopeType)) {
	case "", "repository", "repo":
		return TimeoutKnowledgeRepository
	case "module", "package":
		return TimeoutKnowledgeModule
	case "file":
		return TimeoutKnowledgeFile
	case "symbol", "requirement":
		return TimeoutKnowledgeSymbol
	default:
		return TimeoutKnowledgeDefault
	}
}

// clientBundle is the immutable connection-bundle the Client serves
// RPCs from. R3 slice 4 introduces this so a cert rotation can swap
// the active bundle atomically without disrupting in-flight RPCs.
//
// Each RPC acquires the current bundle, runs its call, and releases.
// The rotator publishes a new bundle, marks the old one closing, and
// closes the old conn once its in-flight count drains to zero. There
// is no artificial deadline: long knowledge-generation RPCs (30+ min)
// finish naturally on the old conn while new RPCs run on the new conn.
//
// Race-freedom (codex r1c): on every acquire we increment inflight
// before re-checking that (a) the bundle isn't already closing AND
// (b) the bundle pointer hasn't been swapped beneath us. If either
// fails we release and retry against the new current. Between
// `bundle.Store(new)` and `closing.Store(true)` a goroutine that
// loaded the old bundle and incremented inflight is one of:
//   - already-running on old: fine, releases when its RPC finishes
//   - newly-incremented after closing flipped: acquire-loop releases
//   - in the gap: the bundle-pointer double-check catches it
type clientBundle struct {
	conn             *grpc.ClientConn
	reasoning        reasoningv1.ReasoningServiceClient
	linking          linkingv1.LinkingServiceClient
	requirements     requirementsv1.RequirementsServiceClient
	knowledge        knowledgev1.KnowledgeServiceClient
	enterpriseReport enterprisev1.EnterpriseReportServiceClient
	contracts        contractsv1.ContractsServiceClient
	health           healthpb.HealthClient
	inflight         atomic.Int64
	closing          atomic.Bool
}

// Client wraps gRPC connections to the Python worker and exposes typed service
// clients for reasoning, linking, requirements, knowledge, contracts, and
// enterprise-report calls.
//
// R3 slice 4: when wired with a tlsreload.Watcher (via WithTLSReloadWatcher),
// the Client automatically cycles its underlying gRPC connection when a
// validated cert reload fires. New RPCs handshake under the new cert; in-
// flight RPCs against the old conn complete naturally and the old conn
// closes once drained.
type Client struct {
	bundle  atomic.Pointer[clientBundle]
	address string

	knowledgeTimeoutProvider func() time.Duration

	// Hot-reload coordination state.
	tlsWatcher       *tlsreload.Watcher
	dialOpts         []grpc.DialOption // captured at New() so rotation can re-dial
	rotateMu         sync.Mutex        // serializes rotations; readers are lock-free
	healthCheckOnSwap bool

	// Closed is set once Close() has run; further bundle rotations are
	// no-ops and acquire returns nil so callers can short-circuit.
	closed atomic.Bool
}

type Option func(*Client)

// TLSConfig captures the mTLS material the API needs to dial the worker
// over a mutually-authenticated TLS channel. When all four fields are
// set (CertPath, KeyPath, CAPath, ServerName) and Enabled is true, the
// client uses credentials.NewTLS instead of insecure.NewCredentials.
//
// Slice 4 of plan 2026-04-29-workspace-llm-source-of-truth-r2.md.
type TLSConfig struct {
	Enabled    bool
	CertPath   string
	KeyPath    string
	CAPath     string
	ServerName string
}

// WithKnowledgeTimeoutProvider injects a live timeout provider for
// repository-scale knowledge/report generation. The returned duration is used
// as the repository-level ceiling and falls back to built-in defaults when
// zero or negative.
func WithKnowledgeTimeoutProvider(fn func() time.Duration) Option {
	return func(c *Client) {
		c.knowledgeTimeoutProvider = fn
	}
}

// WithTLSReloadWatcher wires a hot-reload watcher to the Client so cert
// rotations on disk automatically cycle the gRPC connection. The
// watcher's GetClientCertificate hook is captured by the dial's
// tls.Config so future TLS handshakes always pick up the latest cert.
// On every successful reload the Client redials and atomic-swaps the
// active bundle. Old conns drain naturally — no in-flight RPC is
// disrupted.
//
// R3 slice 4. When the watcher is nil this is a no-op (e.g. OSS dev
// without mTLS).
func WithTLSReloadWatcher(w *tlsreload.Watcher) Option {
	return func(c *Client) {
		c.tlsWatcher = w
	}
}

// WithHealthCheckOnSwap enables a short health-check probe against
// the new conn before the bundle is swapped. If the probe fails (e.g.
// the new cert doesn't actually establish handshake with the worker
// because of a server-side misconfiguration) the rotation is aborted
// and the old bundle stays current. Defaults to enabled when wired
// via NewWithRotation; the option is exposed for tests that want to
// disable it.
func WithHealthCheckOnSwap(enable bool) Option {
	return func(c *Client) {
		c.healthCheckOnSwap = enable
	}
}

// New creates a new worker Client. It attempts to connect to the worker at the
// given address. If the worker is unreachable, the connection is established
// lazily and the API can still start in degraded mode.
//
// When tlsCfg.Enabled is true, all four paths/fields must be valid; the
// function builds a tls.Config with mutual auth, loads the client cert
// + CA bundle, and dials with credentials.NewTLS. On any TLS load
// failure, returns an error (no silent fallback to insecure). When
// tlsCfg.Enabled is false (zero value or explicitly disabled), the
// legacy insecure path is used (OSS dev compatibility).
//
// R3 slice 4: when an Option supplied via WithTLSReloadWatcher is set,
// the Client subscribes to OnReload and cycles its connection on every
// successful reload. The dial credentials still come from the supplied
// tlsCfg at boot — the watcher tracks the same paths for hot-reload.
func New(address string, tlsCfg TLSConfig, opts ...Option) (*Client, error) {
	creds, err := dialCredentials(tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("worker tls: %w", err)
	}

	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(50*1024*1024),
			grpc.MaxCallSendMsgSize(50*1024*1024),
		),
	}

	conn, err := grpc.NewClient(address, dialOpts...)
	if err != nil {
		return nil, err
	}

	c := &Client{
		address:                  address,
		dialOpts:                 dialOpts,
		knowledgeTimeoutProvider: func() time.Duration { return TimeoutKnowledgeRepository },
		healthCheckOnSwap:        true,
	}
	c.bundle.Store(buildBundle(conn))

	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}

	// Subscribe to reload events so the Client can cycle the conn on
	// validated cert changes. Subscribing AFTER the bundle is published
	// guarantees the first OnReload sees a live bundle.
	if c.tlsWatcher != nil {
		c.tlsWatcher.OnReload(func(success bool, err error) {
			if !success {
				return // validation failure; old bundle remains current
			}
			if rotErr := c.rotateConnection(); rotErr != nil {
				slog.Warn("worker tls rotation failed; keeping old bundle",
					"error", rotErr,
					"address", c.address)
			}
		})
	}
	return c, nil
}

// buildBundle constructs a fresh clientBundle around an existing conn.
// All service stubs are built once per bundle so accessors don't have
// to construct them per RPC.
func buildBundle(conn *grpc.ClientConn) *clientBundle {
	return &clientBundle{
		conn:             conn,
		reasoning:        reasoningv1.NewReasoningServiceClient(conn),
		linking:          linkingv1.NewLinkingServiceClient(conn),
		requirements:     requirementsv1.NewRequirementsServiceClient(conn),
		knowledge:        knowledgev1.NewKnowledgeServiceClient(conn),
		enterpriseReport: enterprisev1.NewEnterpriseReportServiceClient(conn),
		contracts:        contractsv1.NewContractsServiceClient(conn),
		health:           healthpb.NewHealthClient(conn),
	}
}

// acquire returns the current bundle with its inflight count
// incremented, or nil if the Client is closed. Callers MUST call
// release(b) when done. The acquire-loop is race-free against
// concurrent rotations (see clientBundle docstring).
func (c *Client) acquire() *clientBundle {
	if c.closed.Load() {
		return nil
	}
	for {
		b := c.bundle.Load()
		if b == nil {
			return nil
		}
		b.inflight.Add(1)
		if !b.closing.Load() && c.bundle.Load() == b {
			return b
		}
		// Old/closing bundle. Release and retry on the new current.
		b.inflight.Add(-1)
		// The rotator publishes a new bundle BEFORE flipping closing
		// on the old, so a fresh Load() will see the new one. No
		// sleep needed — the loop is tight and rare.
	}
}

func (c *Client) release(b *clientBundle) {
	if b == nil {
		return
	}
	b.inflight.Add(-1)
}

// rotateConnection redials with the captured DialOptions, optionally
// validates with a healthcheck against the new conn, and atomically
// swaps the active bundle. The old conn is closed once its in-flight
// drains to zero (no artificial deadline — long RPCs finish naturally).
//
// Returns an error when the redial or healthcheck fails. In that case
// the active bundle is NOT swapped; the old bundle remains current
// and the validation-failure log line tells operators why.
//
// rotateMu serializes rotations so concurrent OnReload events (rare:
// fsnotify burst + poll-loop tick coinciding) don't race-build two
// new conns.
func (c *Client) rotateConnection() error {
	if c.closed.Load() {
		return nil
	}
	c.rotateMu.Lock()
	defer c.rotateMu.Unlock()

	newConn, err := grpc.NewClient(c.address, c.dialOpts...)
	if err != nil {
		return fmt.Errorf("redial worker: %w", err)
	}

	if c.healthCheckOnSwap {
		// Short health-check probe. Failure aborts the swap so a
		// rotation that produced an unusable cert (wrong server SAN,
		// expired worker cert, etc.) doesn't black-hole every RPC.
		probeCtx, cancel := context.WithTimeout(context.Background(), TimeoutHealth)
		probeBundle := buildBundle(newConn)
		_, probeErr := probeBundle.health.Check(probeCtx, &healthpb.HealthCheckRequest{})
		cancel()
		if probeErr != nil {
			_ = newConn.Close()
			return fmt.Errorf("post-rotation health probe failed: %w", probeErr)
		}
	}

	newBundle := buildBundle(newConn)
	oldBundle := c.bundle.Swap(newBundle)
	if oldBundle != nil {
		oldBundle.closing.Store(true)
		go c.drainAndClose(oldBundle)
	}

	slog.Info("worker tls rotation complete",
		"address", c.address,
		"new_inflight", newBundle.inflight.Load())
	return nil
}

// drainAndClose waits for the old bundle's in-flight RPCs to finish,
// then closes the underlying gRPC conn. Logs a warning every 5 minutes
// if the drain hasn't completed (e.g. a hung RPC).
func (c *Client) drainAndClose(b *clientBundle) {
	if b == nil || b.conn == nil {
		return
	}
	const logEvery = 5 * time.Minute
	const pollEvery = 100 * time.Millisecond
	deadlineWarn := time.Now().Add(logEvery)
	for {
		if b.inflight.Load() == 0 {
			break
		}
		if time.Now().After(deadlineWarn) {
			slog.Warn("worker tls rotation: old conn drain still pending",
				"inflight", b.inflight.Load(),
				"address", c.address)
			deadlineWarn = time.Now().Add(logEvery)
		}
		time.Sleep(pollEvery)
		if c.closed.Load() {
			break
		}
	}
	_ = b.conn.Close()
}

// dialCredentials returns the gRPC TransportCredentials for the worker
// dial. When TLS is disabled, returns insecure.NewCredentials() (legacy
// path, OSS dev / no-cert-manager environments). When enabled, loads
// the client cert + CA bundle from disk and builds a tls.Config with
// mutual auth.
func dialCredentials(cfg TLSConfig) (credentials.TransportCredentials, error) {
	if !cfg.Enabled {
		return insecure.NewCredentials(), nil
	}
	if cfg.CertPath == "" || cfg.KeyPath == "" || cfg.CAPath == "" {
		return nil, fmt.Errorf("tls enabled but cert_path/key_path/ca_path are required")
	}

	clientCert, err := tls.LoadX509KeyPair(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("load client cert: %w", err)
	}

	caPEM, err := os.ReadFile(cfg.CAPath)
	if err != nil {
		return nil, fmt.Errorf("read ca bundle: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse ca bundle: no PEM certs found at %s", cfg.CAPath)
	}

	serverName := cfg.ServerName
	if serverName == "" {
		serverName = "worker.sourcebridge.svc.cluster.local"
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      caPool,
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS12,
	}
	slog.Info("worker tls enabled", "server_name", serverName, "cert", cfg.CertPath, "ca", cfg.CAPath)
	return credentials.NewTLS(tlsCfg), nil
}

func minDuration(a, b time.Duration) time.Duration {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	if a < b {
		return a
	}
	return b
}

func (c *Client) repositoryKnowledgeTimeout() time.Duration {
	if c == nil || c.knowledgeTimeoutProvider == nil {
		return TimeoutKnowledgeRepository
	}
	if d := c.knowledgeTimeoutProvider(); d > 0 {
		return d
	}
	return TimeoutKnowledgeRepository
}

// IsAvailable checks whether the worker gRPC connection is in READY state.
func (c *Client) IsAvailable() bool {
	if c == nil {
		return false
	}
	b := c.bundle.Load()
	if b == nil || b.conn == nil {
		return false
	}
	return b.conn.GetState() == connectivity.Ready
}

// CheckHealth performs a gRPC health check against the worker.
func (c *Client) CheckHealth(ctx context.Context) (bool, error) {
	b := c.acquire()
	if b == nil {
		return false, fmt.Errorf("worker client closed")
	}
	defer c.release(b)

	ctx, cancel := context.WithTimeout(ctx, TimeoutHealth)
	defer cancel()

	resp, err := b.health.Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		return false, err
	}
	return resp.GetStatus() == healthpb.HealthCheckResponse_SERVING, nil
}

// Close shuts down the gRPC connection. Idempotent and safe to call
// from any goroutine. Once Close runs, future acquire() calls return
// nil so RPC methods return cleanly with a "closed" error.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	b := c.bundle.Load()
	if b == nil || b.conn == nil {
		return nil
	}
	return b.conn.Close()
}

// Address returns the configured worker address.
func (c *Client) Address() string {
	return c.address
}

// errClientClosed is returned by RPC methods after Close().
var errClientClosed = fmt.Errorf("worker client closed")

// AnalyzeSymbol calls the reasoning worker with the given request and timeout.
func (c *Client) AnalyzeSymbol(ctx context.Context, req *reasoningv1.AnalyzeSymbolRequest) (*reasoningv1.AnalyzeSymbolResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, TimeoutAnalysis)
	defer cancel()
	return b.reasoning.AnalyzeSymbol(ctx, req)
}

// AnswerQuestion calls the reasoning worker discussion RPC.
func (c *Client) AnswerQuestion(ctx context.Context, req *reasoningv1.AnswerQuestionRequest) (*reasoningv1.AnswerQuestionResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, TimeoutDiscussion)
	defer cancel()
	return b.reasoning.AnswerQuestion(ctx, req)
}

// AnswerQuestionWithTools is the agentic-retrieval variant of
// AnswerQuestion. Each invocation is one turn of the conversation;
// the orchestrator accumulates history and re-calls.
func (c *Client) AnswerQuestionWithTools(ctx context.Context, req *reasoningv1.AnswerQuestionWithToolsRequest) (*reasoningv1.AnswerQuestionWithToolsResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, TimeoutDiscussion)
	defer cancel()
	return b.reasoning.AnswerQuestionWithTools(ctx, req)
}

// GetProviderCapabilities returns the active provider's feature
// flags. Called once on startup so the orchestrator can gate the
// agentic path without per-request capability checks.
func (c *Client) GetProviderCapabilities(ctx context.Context) (*reasoningv1.GetProviderCapabilitiesResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return b.reasoning.GetProviderCapabilities(ctx, &reasoningv1.GetProviderCapabilitiesRequest{})
}

// ClassifyQuestion runs the LLM-backed question classifier. Quick
// timeout (2s) because callers fall back to the keyword classifier
// when this fails.
func (c *Client) ClassifyQuestion(ctx context.Context, req *reasoningv1.ClassifyQuestionRequest) (*reasoningv1.ClassifyQuestionResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return b.reasoning.ClassifyQuestion(ctx, req)
}

// DecomposeQuestion runs the Haiku decomposer. Short timeout since
// the caller falls back to the single-loop path on any failure.
func (c *Client) DecomposeQuestion(ctx context.Context, req *reasoningv1.DecomposeQuestionRequest) (*reasoningv1.DecomposeQuestionResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return b.reasoning.DecomposeQuestion(ctx, req)
}

// SynthesizeDecomposedAnswer runs the final-synthesis turn for a
// decomposed query. Uses the discussion timeout because it's a
// Sonnet call that may take 10–20s on dense sub-answer bodies.
func (c *Client) SynthesizeDecomposedAnswer(ctx context.Context, req *reasoningv1.SynthesizeDecomposedAnswerRequest) (*reasoningv1.SynthesizeDecomposedAnswerResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, TimeoutDiscussion)
	defer cancel()
	return b.reasoning.SynthesizeDecomposedAnswer(ctx, req)
}

// AnswerQuestionStream opens a server-streaming discussion RPC. Callers
// receive AnswerDelta frames as the model generates output, then a
// terminal frame with `finished=true` carrying the final usage and
// referenced_symbols. The deadline matches the unary variant so callers
// get identical timeout semantics whether they chose to stream or not.
//
// The returned stream handle is responsible for cancellation on error;
// the caller should read until io.EOF (or a transport error) and then
// drop the context via the returned cancel func. Returning the cancel
// lets the caller bail out mid-stream (e.g. user hit stop) without
// leaking the background goroutine.
//
// R3 slice 4: streaming RPCs hold the bundle for the full duration of
// the stream. The returned cancel wraps both the context cancel and
// the bundle release so callers retain the same single-cancel API.
func (c *Client) AnswerQuestionStream(
	ctx context.Context,
	req *reasoningv1.AnswerQuestionRequest,
) (reasoningv1.ReasoningService_AnswerQuestionStreamClient, context.CancelFunc, error) {
	b := c.acquire()
	if b == nil {
		return nil, func() {}, errClientClosed
	}
	streamCtx, cancel := context.WithTimeout(ctx, TimeoutDiscussion)
	stream, err := b.reasoning.AnswerQuestionStream(streamCtx, req)
	if err != nil {
		cancel()
		c.release(b)
		return nil, func() {}, err
	}
	released := false
	wrappedCancel := func() {
		cancel()
		if !released {
			released = true
			c.release(b)
		}
	}
	return stream, wrappedCancel, nil
}

// ReviewFile calls the reasoning worker review RPC.
func (c *Client) ReviewFile(ctx context.Context, req *reasoningv1.ReviewFileRequest) (*reasoningv1.ReviewFileResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, TimeoutReview)
	defer cancel()
	return b.reasoning.ReviewFile(ctx, req)
}

// GenerateEmbedding calls the reasoning worker embedding RPC.
func (c *Client) GenerateEmbedding(ctx context.Context, req *reasoningv1.GenerateEmbeddingRequest) (*reasoningv1.GenerateEmbeddingResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, TimeoutEmbedding)
	defer cancel()
	return b.reasoning.GenerateEmbedding(ctx, req)
}

// LinkRequirement calls the linking worker for a single requirement.
func (c *Client) LinkRequirement(ctx context.Context, req *linkingv1.LinkRequirementRequest) (*linkingv1.LinkRequirementResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, TimeoutLinkItem)
	defer cancel()
	return b.linking.LinkRequirement(ctx, req)
}

// BatchLink calls the linking worker to link all requirements at once with shared embeddings.
func (c *Client) BatchLink(ctx context.Context, req *linkingv1.BatchLinkRequest) (*linkingv1.BatchLinkResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, TimeoutLinkTotal)
	defer cancel()
	return b.linking.BatchLink(ctx, req)
}

// ValidateLink calls the linking worker to validate an existing link.
func (c *Client) ValidateLink(ctx context.Context, req *linkingv1.ValidateLinkRequest) (*linkingv1.ValidateLinkResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, TimeoutLinkItem)
	defer cancel()
	return b.linking.ValidateLink(ctx, req)
}

// ParseDocument calls the requirements worker to parse a document.
func (c *Client) ParseDocument(ctx context.Context, req *requirementsv1.ParseDocumentRequest) (*requirementsv1.ParseDocumentResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, TimeoutParse)
	defer cancel()
	return b.requirements.ParseDocument(ctx, req)
}

// ParseCSV calls the requirements worker to parse a CSV file.
func (c *Client) ParseCSV(ctx context.Context, req *requirementsv1.ParseCSVRequest) (*requirementsv1.ParseCSVResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, TimeoutParse)
	defer cancel()
	return b.requirements.ParseCSV(ctx, req)
}

// EnrichRequirement calls the requirements worker to enrich a requirement.
func (c *Client) EnrichRequirement(ctx context.Context, req *requirementsv1.EnrichRequirementRequest) (*requirementsv1.EnrichRequirementResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, TimeoutEnrich)
	defer cancel()
	return b.requirements.EnrichRequirement(ctx, req)
}

// ExtractSpecs calls the requirements worker to extract specs from source files.
func (c *Client) ExtractSpecs(ctx context.Context, req *requirementsv1.ExtractSpecsRequest) (*requirementsv1.ExtractSpecsResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, TimeoutExtraction)
	defer cancel()
	return b.requirements.ExtractSpecs(ctx, req)
}

// SimulateChange calls the reasoning worker to resolve symbols for a hypothetical change.
func (c *Client) SimulateChange(ctx context.Context, req *reasoningv1.SimulateChangeRequest) (*reasoningv1.SimulateChangeResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, TimeoutSimulation)
	defer cancel()
	return b.reasoning.SimulateChange(ctx, req)
}

// GenerateCliffNotes calls the knowledge worker to generate cliff notes.
// Timeout is scoped to the request: repository-level calls get 600s,
// module-level 300s, and file/symbol-level 120s.
func (c *Client) GenerateCliffNotes(ctx context.Context, req *knowledgev1.GenerateCliffNotesRequest) (*knowledgev1.GenerateCliffNotesResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	timeout := timeoutForKnowledgeScope(req.GetScopeType())
	if strings.EqualFold(strings.TrimSpace(req.GetScopeType()), "repository") || strings.TrimSpace(req.GetScopeType()) == "" {
		timeout = c.repositoryKnowledgeTimeout()
	} else {
		timeout = minDuration(c.repositoryKnowledgeTimeout(), timeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return b.knowledge.GenerateCliffNotes(ctx, req)
}

// GenerateLearningPath calls the knowledge worker to generate a learning path.
// Learning paths are always repository-scoped today.
func (c *Client) GenerateLearningPath(ctx context.Context, req *knowledgev1.GenerateLearningPathRequest) (*knowledgev1.GenerateLearningPathResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, c.repositoryKnowledgeTimeout())
	defer cancel()
	return b.knowledge.GenerateLearningPath(ctx, req)
}

// GenerateArchitectureDiagram calls the knowledge worker to generate an AI architecture diagram.
// Architecture diagrams are repository-scoped today.
func (c *Client) GenerateArchitectureDiagram(ctx context.Context, req *knowledgev1.GenerateArchitectureDiagramRequest) (*knowledgev1.GenerateArchitectureDiagramResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, c.repositoryKnowledgeTimeout())
	defer cancel()
	return b.knowledge.GenerateArchitectureDiagram(ctx, req)
}

// GenerateWorkflowStory calls the knowledge worker to generate a workflow story.
func (c *Client) GenerateWorkflowStory(ctx context.Context, req *knowledgev1.GenerateWorkflowStoryRequest) (*knowledgev1.GenerateWorkflowStoryResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	timeout := timeoutForKnowledgeScope(req.GetScopeType())
	if strings.EqualFold(strings.TrimSpace(req.GetScopeType()), "repository") || strings.TrimSpace(req.GetScopeType()) == "" {
		timeout = c.repositoryKnowledgeTimeout()
	} else {
		timeout = minDuration(c.repositoryKnowledgeTimeout(), timeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return b.knowledge.GenerateWorkflowStory(ctx, req)
}

// ExplainSystem calls the knowledge worker for a whole-system explanation.
func (c *Client) ExplainSystem(ctx context.Context, req *knowledgev1.ExplainSystemRequest) (*knowledgev1.ExplainSystemResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	timeout := timeoutForKnowledgeScope(req.GetScopeType())
	if strings.EqualFold(strings.TrimSpace(req.GetScopeType()), "repository") || strings.TrimSpace(req.GetScopeType()) == "" {
		timeout = c.repositoryKnowledgeTimeout()
	} else {
		timeout = minDuration(c.repositoryKnowledgeTimeout(), timeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return b.knowledge.ExplainSystem(ctx, req)
}

// GenerateCodeTour calls the knowledge worker to generate a code tour.
// Code tours are always repository-scoped today.
func (c *Client) GenerateCodeTour(ctx context.Context, req *knowledgev1.GenerateCodeTourRequest) (*knowledgev1.GenerateCodeTourResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, c.repositoryKnowledgeTimeout())
	defer cancel()
	return b.knowledge.GenerateCodeTour(ctx, req)
}

// GenerateReport calls the enterprise report worker to generate a professional report.
// Reports can take a long time (30+ sections × LLM calls) so the timeout is generous.
func (c *Client) GenerateReport(ctx context.Context, req *enterprisev1.GenerateReportRequest) (*enterprisev1.GenerateReportResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, c.repositoryKnowledgeTimeout())
	defer cancel()
	return b.enterpriseReport.GenerateReport(ctx, req)
}

// DetectContracts calls the contracts worker to detect API contracts in files.
func (c *Client) DetectContracts(ctx context.Context, req *contractsv1.DetectContractsRequest) (*contractsv1.DetectContractsResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, TimeoutContracts)
	defer cancel()
	return b.contracts.DetectContracts(ctx, req)
}

// MatchConsumers calls the contracts worker to match consumers to contracts.
func (c *Client) MatchConsumers(ctx context.Context, req *contractsv1.MatchConsumersRequest) (*contractsv1.MatchConsumersResponse, error) {
	b := c.acquire()
	if b == nil {
		return nil, errClientClosed
	}
	defer c.release(b)
	ctx, cancel := context.WithTimeout(ctx, TimeoutContracts)
	defer cancel()
	return b.contracts.MatchConsumers(ctx, req)
}

// LogStatus logs the current worker connection state.
func (c *Client) LogStatus() {
	if c == nil {
		slog.Info("worker client not configured")
		return
	}
	b := c.bundle.Load()
	if b == nil || b.conn == nil {
		slog.Info("worker client has no active bundle", "address", c.address)
		return
	}
	state := b.conn.GetState()
	slog.Info("worker connection status",
		"address", c.address,
		"state", state.String(),
	)
}
