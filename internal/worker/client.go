// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package worker

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	contractsv1 "github.com/sourcebridge/sourcebridge/gen/go/contracts/v1"
	enterprisev1 "github.com/sourcebridge/sourcebridge/gen/go/enterprise/v1"
	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	linkingv1 "github.com/sourcebridge/sourcebridge/gen/go/linking/v1"
	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	requirementsv1 "github.com/sourcebridge/sourcebridge/gen/go/requirements/v1"
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

// Client wraps gRPC connections to the Python worker and exposes typed service
// clients for reasoning, linking, and requirements.
type Client struct {
	conn                     *grpc.ClientConn
	address                  string
	knowledgeTimeoutProvider func() time.Duration
	Reasoning                reasoningv1.ReasoningServiceClient
	Linking                  linkingv1.LinkingServiceClient
	Requirements             requirementsv1.RequirementsServiceClient
	Knowledge                knowledgev1.KnowledgeServiceClient
	EnterpriseReport         enterprisev1.EnterpriseReportServiceClient
	Contracts                contractsv1.ContractsServiceClient
	Health                   healthpb.HealthClient
}

type Option func(*Client)

// WithKnowledgeTimeoutProvider injects a live timeout provider for
// repository-scale knowledge/report generation. The returned duration is used
// as the repository-level ceiling and falls back to built-in defaults when
// zero or negative.
func WithKnowledgeTimeoutProvider(fn func() time.Duration) Option {
	return func(c *Client) {
		c.knowledgeTimeoutProvider = fn
	}
}

// New creates a new worker Client. It attempts to connect to the worker at the
// given address. If the worker is unreachable, the connection is established
// lazily and the API can still start in degraded mode.
func New(address string, opts ...Option) (*Client, error) {
	conn, err := grpc.NewClient(
		address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(50*1024*1024),
			grpc.MaxCallSendMsgSize(50*1024*1024),
		),
	)
	if err != nil {
		return nil, err
	}

	c := &Client{
		conn:                     conn,
		address:                  address,
		knowledgeTimeoutProvider: func() time.Duration { return TimeoutKnowledgeRepository },
		Reasoning:                reasoningv1.NewReasoningServiceClient(conn),
		Linking:                  linkingv1.NewLinkingServiceClient(conn),
		Requirements:             requirementsv1.NewRequirementsServiceClient(conn),
		Knowledge:                knowledgev1.NewKnowledgeServiceClient(conn),
		EnterpriseReport:         enterprisev1.NewEnterpriseReportServiceClient(conn),
		Contracts:                contractsv1.NewContractsServiceClient(conn),
		Health:                   healthpb.NewHealthClient(conn),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c, nil
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
	if c == nil || c.conn == nil {
		return false
	}
	return c.conn.GetState() == connectivity.Ready
}

// CheckHealth performs a gRPC health check against the worker.
func (c *Client) CheckHealth(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutHealth)
	defer cancel()

	resp, err := c.Health.Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		return false, err
	}
	return resp.GetStatus() == healthpb.HealthCheckResponse_SERVING, nil
}

// Close shuts down the gRPC connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Address returns the configured worker address.
func (c *Client) Address() string {
	return c.address
}

// AnalyzeSymbol calls the reasoning worker with the given request and timeout.
func (c *Client) AnalyzeSymbol(ctx context.Context, req *reasoningv1.AnalyzeSymbolRequest) (*reasoningv1.AnalyzeSymbolResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutAnalysis)
	defer cancel()
	return c.Reasoning.AnalyzeSymbol(ctx, req)
}

// AnswerQuestion calls the reasoning worker discussion RPC.
func (c *Client) AnswerQuestion(ctx context.Context, req *reasoningv1.AnswerQuestionRequest) (*reasoningv1.AnswerQuestionResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutDiscussion)
	defer cancel()
	return c.Reasoning.AnswerQuestion(ctx, req)
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
func (c *Client) AnswerQuestionStream(
	ctx context.Context,
	req *reasoningv1.AnswerQuestionRequest,
) (reasoningv1.ReasoningService_AnswerQuestionStreamClient, context.CancelFunc, error) {
	streamCtx, cancel := context.WithTimeout(ctx, TimeoutDiscussion)
	stream, err := c.Reasoning.AnswerQuestionStream(streamCtx, req)
	if err != nil {
		cancel()
		return nil, func() {}, err
	}
	return stream, cancel, nil
}

// ReviewFile calls the reasoning worker review RPC.
func (c *Client) ReviewFile(ctx context.Context, req *reasoningv1.ReviewFileRequest) (*reasoningv1.ReviewFileResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutReview)
	defer cancel()
	return c.Reasoning.ReviewFile(ctx, req)
}

// GenerateEmbedding calls the reasoning worker embedding RPC.
func (c *Client) GenerateEmbedding(ctx context.Context, req *reasoningv1.GenerateEmbeddingRequest) (*reasoningv1.GenerateEmbeddingResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutEmbedding)
	defer cancel()
	return c.Reasoning.GenerateEmbedding(ctx, req)
}

// LinkRequirement calls the linking worker for a single requirement.
func (c *Client) LinkRequirement(ctx context.Context, req *linkingv1.LinkRequirementRequest) (*linkingv1.LinkRequirementResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutLinkItem)
	defer cancel()
	return c.Linking.LinkRequirement(ctx, req)
}

// BatchLink calls the linking worker to link all requirements at once with shared embeddings.
func (c *Client) BatchLink(ctx context.Context, req *linkingv1.BatchLinkRequest) (*linkingv1.BatchLinkResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutLinkTotal)
	defer cancel()
	return c.Linking.BatchLink(ctx, req)
}

// ValidateLink calls the linking worker to validate an existing link.
func (c *Client) ValidateLink(ctx context.Context, req *linkingv1.ValidateLinkRequest) (*linkingv1.ValidateLinkResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutLinkItem)
	defer cancel()
	return c.Linking.ValidateLink(ctx, req)
}

// ParseDocument calls the requirements worker to parse a document.
func (c *Client) ParseDocument(ctx context.Context, req *requirementsv1.ParseDocumentRequest) (*requirementsv1.ParseDocumentResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutParse)
	defer cancel()
	return c.Requirements.ParseDocument(ctx, req)
}

// ParseCSV calls the requirements worker to parse a CSV file.
func (c *Client) ParseCSV(ctx context.Context, req *requirementsv1.ParseCSVRequest) (*requirementsv1.ParseCSVResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutParse)
	defer cancel()
	return c.Requirements.ParseCSV(ctx, req)
}

// EnrichRequirement calls the requirements worker to enrich a requirement.
func (c *Client) EnrichRequirement(ctx context.Context, req *requirementsv1.EnrichRequirementRequest) (*requirementsv1.EnrichRequirementResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutEnrich)
	defer cancel()
	return c.Requirements.EnrichRequirement(ctx, req)
}

// ExtractSpecs calls the requirements worker to extract specs from source files.
func (c *Client) ExtractSpecs(ctx context.Context, req *requirementsv1.ExtractSpecsRequest) (*requirementsv1.ExtractSpecsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutExtraction)
	defer cancel()
	return c.Requirements.ExtractSpecs(ctx, req)
}

// SimulateChange calls the reasoning worker to resolve symbols for a hypothetical change.
func (c *Client) SimulateChange(ctx context.Context, req *reasoningv1.SimulateChangeRequest) (*reasoningv1.SimulateChangeResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutSimulation)
	defer cancel()
	return c.Reasoning.SimulateChange(ctx, req)
}

// GenerateCliffNotes calls the knowledge worker to generate cliff notes.
// Timeout is scoped to the request: repository-level calls get 600s,
// module-level 300s, and file/symbol-level 120s.
func (c *Client) GenerateCliffNotes(ctx context.Context, req *knowledgev1.GenerateCliffNotesRequest) (*knowledgev1.GenerateCliffNotesResponse, error) {
	timeout := timeoutForKnowledgeScope(req.GetScopeType())
	if strings.EqualFold(strings.TrimSpace(req.GetScopeType()), "repository") || strings.TrimSpace(req.GetScopeType()) == "" {
		timeout = c.repositoryKnowledgeTimeout()
	} else {
		timeout = minDuration(c.repositoryKnowledgeTimeout(), timeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.Knowledge.GenerateCliffNotes(ctx, req)
}

// GenerateLearningPath calls the knowledge worker to generate a learning path.
// Learning paths are always repository-scoped today.
func (c *Client) GenerateLearningPath(ctx context.Context, req *knowledgev1.GenerateLearningPathRequest) (*knowledgev1.GenerateLearningPathResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.repositoryKnowledgeTimeout())
	defer cancel()
	return c.Knowledge.GenerateLearningPath(ctx, req)
}

// GenerateArchitectureDiagram calls the knowledge worker to generate an AI architecture diagram.
// Architecture diagrams are repository-scoped today.
func (c *Client) GenerateArchitectureDiagram(ctx context.Context, req *knowledgev1.GenerateArchitectureDiagramRequest) (*knowledgev1.GenerateArchitectureDiagramResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.repositoryKnowledgeTimeout())
	defer cancel()
	return c.Knowledge.GenerateArchitectureDiagram(ctx, req)
}

// GenerateWorkflowStory calls the knowledge worker to generate a workflow story.
func (c *Client) GenerateWorkflowStory(ctx context.Context, req *knowledgev1.GenerateWorkflowStoryRequest) (*knowledgev1.GenerateWorkflowStoryResponse, error) {
	timeout := timeoutForKnowledgeScope(req.GetScopeType())
	if strings.EqualFold(strings.TrimSpace(req.GetScopeType()), "repository") || strings.TrimSpace(req.GetScopeType()) == "" {
		timeout = c.repositoryKnowledgeTimeout()
	} else {
		timeout = minDuration(c.repositoryKnowledgeTimeout(), timeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.Knowledge.GenerateWorkflowStory(ctx, req)
}

// ExplainSystem calls the knowledge worker for a whole-system explanation.
func (c *Client) ExplainSystem(ctx context.Context, req *knowledgev1.ExplainSystemRequest) (*knowledgev1.ExplainSystemResponse, error) {
	timeout := timeoutForKnowledgeScope(req.GetScopeType())
	if strings.EqualFold(strings.TrimSpace(req.GetScopeType()), "repository") || strings.TrimSpace(req.GetScopeType()) == "" {
		timeout = c.repositoryKnowledgeTimeout()
	} else {
		timeout = minDuration(c.repositoryKnowledgeTimeout(), timeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.Knowledge.ExplainSystem(ctx, req)
}

// GenerateCodeTour calls the knowledge worker to generate a code tour.
// Code tours are always repository-scoped today.
func (c *Client) GenerateCodeTour(ctx context.Context, req *knowledgev1.GenerateCodeTourRequest) (*knowledgev1.GenerateCodeTourResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.repositoryKnowledgeTimeout())
	defer cancel()
	return c.Knowledge.GenerateCodeTour(ctx, req)
}

// GenerateReport calls the enterprise report worker to generate a professional report.
// Reports can take a long time (30+ sections × LLM calls) so the timeout is generous.
func (c *Client) GenerateReport(ctx context.Context, req *enterprisev1.GenerateReportRequest) (*enterprisev1.GenerateReportResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.repositoryKnowledgeTimeout())
	defer cancel()
	return c.EnterpriseReport.GenerateReport(ctx, req)
}

// DetectContracts calls the contracts worker to detect API contracts in files.
func (c *Client) DetectContracts(ctx context.Context, req *contractsv1.DetectContractsRequest) (*contractsv1.DetectContractsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutContracts)
	defer cancel()
	return c.Contracts.DetectContracts(ctx, req)
}

// MatchConsumers calls the contracts worker to match consumers to contracts.
func (c *Client) MatchConsumers(ctx context.Context, req *contractsv1.MatchConsumersRequest) (*contractsv1.MatchConsumersResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutContracts)
	defer cancel()
	return c.Contracts.MatchConsumers(ctx, req)
}

// LogStatus logs the current worker connection state.
func (c *Client) LogStatus() {
	if c == nil {
		slog.Info("worker client not configured")
		return
	}
	state := c.conn.GetState()
	slog.Info("worker connection status",
		"address", c.address,
		"state", state.String(),
	)
}
