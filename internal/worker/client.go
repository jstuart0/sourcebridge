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
	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	linkingv1 "github.com/sourcebridge/sourcebridge/gen/go/linking/v1"
	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	requirementsv1 "github.com/sourcebridge/sourcebridge/gen/go/requirements/v1"
)

// Timeout presets for different operation classes.
const (
	TimeoutHealth     = 3 * time.Second
	TimeoutEmbedding  = 10 * time.Second
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
	TimeoutKnowledge = 1800 * time.Second
	TimeoutContracts = 120 * time.Second
)

// Per-scope knowledge timeouts. Repository-level generations may legitimately
// run for many minutes on large codebases; file/symbol scopes should never
// need more than a couple of minutes, and a stuck call shouldn't hold the
// worker's attention past that.
const (
	TimeoutKnowledgeRepository = 1800 * time.Second // 30min — thinking models (Qwen 3.x) need ~70s/leaf × 39 leaves at concurrency 4
	TimeoutKnowledgeModule     = 300 * time.Second
	TimeoutKnowledgeFile       = 120 * time.Second
	TimeoutKnowledgeSymbol     = 120 * time.Second
	TimeoutKnowledgeDefault    = 300 * time.Second
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
	conn         *grpc.ClientConn
	address      string
	Reasoning    reasoningv1.ReasoningServiceClient
	Linking      linkingv1.LinkingServiceClient
	Requirements requirementsv1.RequirementsServiceClient
	Knowledge    knowledgev1.KnowledgeServiceClient
	Contracts    contractsv1.ContractsServiceClient
	Health       healthpb.HealthClient
}

// New creates a new worker Client. It attempts to connect to the worker at the
// given address. If the worker is unreachable, the connection is established
// lazily and the API can still start in degraded mode.
func New(address string) (*Client, error) {
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
		conn:         conn,
		address:      address,
		Reasoning:    reasoningv1.NewReasoningServiceClient(conn),
		Linking:      linkingv1.NewLinkingServiceClient(conn),
		Requirements: requirementsv1.NewRequirementsServiceClient(conn),
		Knowledge:    knowledgev1.NewKnowledgeServiceClient(conn),
		Contracts:    contractsv1.NewContractsServiceClient(conn),
		Health:       healthpb.NewHealthClient(conn),
	}
	return c, nil
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
	ctx, cancel := context.WithTimeout(ctx, timeoutForKnowledgeScope(req.GetScopeType()))
	defer cancel()
	return c.Knowledge.GenerateCliffNotes(ctx, req)
}

// GenerateLearningPath calls the knowledge worker to generate a learning path.
// Learning paths are always repository-scoped today.
func (c *Client) GenerateLearningPath(ctx context.Context, req *knowledgev1.GenerateLearningPathRequest) (*knowledgev1.GenerateLearningPathResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutKnowledgeRepository)
	defer cancel()
	return c.Knowledge.GenerateLearningPath(ctx, req)
}

// GenerateWorkflowStory calls the knowledge worker to generate a workflow story.
func (c *Client) GenerateWorkflowStory(ctx context.Context, req *knowledgev1.GenerateWorkflowStoryRequest) (*knowledgev1.GenerateWorkflowStoryResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, timeoutForKnowledgeScope(req.GetScopeType()))
	defer cancel()
	return c.Knowledge.GenerateWorkflowStory(ctx, req)
}

// ExplainSystem calls the knowledge worker for a whole-system explanation.
func (c *Client) ExplainSystem(ctx context.Context, req *knowledgev1.ExplainSystemRequest) (*knowledgev1.ExplainSystemResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, timeoutForKnowledgeScope(req.GetScopeType()))
	defer cancel()
	return c.Knowledge.ExplainSystem(ctx, req)
}

// GenerateCodeTour calls the knowledge worker to generate a code tour.
// Code tours are always repository-scoped today.
func (c *Client) GenerateCodeTour(ctx context.Context, req *knowledgev1.GenerateCodeTourRequest) (*knowledgev1.GenerateCodeTourResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutKnowledgeRepository)
	defer cancel()
	return c.Knowledge.GenerateCodeTour(ctx, req)
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
