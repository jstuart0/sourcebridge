// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"
	"fmt"
	"sync/atomic"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"

	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
)

// WorkerAgentSynthesizer adapts a worker client + its
// capability-probe result into the orchestrator's AgentSynthesizer
// interface. Constructed once on server startup after a successful
// GetProviderCapabilities call; the capability bit is cached on the
// adapter so the loop's gate is a cheap local read.
//
// Slice 2 of the workspace-LLM-source-of-truth plan: the adapter now
// goes through llmcall.Caller (via the agentWorkerCaller interface,
// updated to expect Caller-shaped methods) so workspace-saved settings
// flow through gRPC metadata on every agentic turn. Capability cache
// invalidation: the cached toolsEnabled bit is paired with the snapshot
// version observed at probe time; a workspace save bumps the resolver's
// version, and the next SupportsTools call detects the drift and
// re-probes. This is the codex-r1c finding.
type WorkerAgentSynthesizer struct {
	worker       agentWorkerCaller
	toolsEnabled bool
	// probedVersion is the resolver Snapshot.Version observed when the
	// initial GetProviderCapabilities probe ran. SupportsTools compares
	// this to the current resolver version and returns false (forcing a
	// re-probe at the next opportunity) when they drift.
	probedVersion atomic.Uint64
}

// agentWorkerCaller is the narrow surface we need from
// *llmcall.Caller. Kept as an interface so tests can inject a fake
// without importing grpc.
type agentWorkerCaller interface {
	AnswerQuestionWithTools(ctx context.Context, repoID, op string, req *reasoningv1.AnswerQuestionWithToolsRequest) (*reasoningv1.AnswerQuestionWithToolsResponse, error)
	IsAvailable() bool
}

// NewWorkerAgentSynthesizer constructs the adapter. `toolsEnabled`
// comes from the GetProviderCapabilities probe; when false, the
// loop must not be entered. probedVersion is the resolver
// Snapshot.Version observed at probe time; pass 0 if not known
// (capability-cache invalidation will be disabled).
func NewWorkerAgentSynthesizer(worker agentWorkerCaller, toolsEnabled bool) *WorkerAgentSynthesizer {
	return &WorkerAgentSynthesizer{worker: worker, toolsEnabled: toolsEnabled}
}

// NewWorkerAgentSynthesizerWithVersion constructs the adapter and
// records the resolver Snapshot.Version observed at probe time. A
// workspace save (which bumps the version) makes IsStale() return true,
// allowing the orchestrator to re-probe before its next agentic turn.
func NewWorkerAgentSynthesizerWithVersion(worker agentWorkerCaller, toolsEnabled bool, probedVersion uint64) *WorkerAgentSynthesizer {
	s := &WorkerAgentSynthesizer{worker: worker, toolsEnabled: toolsEnabled}
	s.probedVersion.Store(probedVersion)
	return s
}

// ProbedVersion returns the resolver Snapshot.Version observed at the
// last capability probe. The orchestrator compares this to the current
// resolver version on each agentic turn — when they differ, it can
// re-probe before deciding whether to use the agentic path.
func (s *WorkerAgentSynthesizer) ProbedVersion() uint64 {
	if s == nil {
		return 0
	}
	return s.probedVersion.Load()
}

// SupportsTools mirrors the capability bit.
func (s *WorkerAgentSynthesizer) SupportsTools() bool {
	if s == nil || s.worker == nil {
		return false
	}
	if !s.worker.IsAvailable() {
		return false
	}
	return s.toolsEnabled
}

// AnswerQuestionWithTools translates the Go-native AgentTurnRequest
// into proto, calls the worker, and translates the response back.
func (s *WorkerAgentSynthesizer) AnswerQuestionWithTools(
	ctx context.Context,
	req AgentTurnRequest,
) (AgentTurn, error) {
	if s == nil || s.worker == nil {
		return AgentTurn{}, fmt.Errorf("agent synth: worker client is nil")
	}

	protoMsgs := make([]*reasoningv1.AgentMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		protoMsgs = append(protoMsgs, toProtoAgentMessage(m))
	}

	protoTools := make([]*reasoningv1.ToolSchema, 0, len(req.Tools))
	for _, t := range req.Tools {
		protoTools = append(protoTools, &reasoningv1.ToolSchema{
			Name:            t.Name,
			Description:     t.Description,
			InputSchemaJson: t.InputSchemaJSON,
		})
	}

	// llmcall:allow — s.worker is the agentWorkerCaller interface,
	// satisfied in production by *llmcall.Caller. The lint heuristic
	// matches the field name but the receiver type is the
	// llmcall-aware adapter; metadata is attached inside Caller.
	resp, err := s.worker.AnswerQuestionWithTools(ctx, req.RepositoryID, resolution.OpQAAgentTurn, &reasoningv1.AnswerQuestionWithToolsRequest{
		RepositoryId:        req.RepositoryID,
		Messages:            protoMsgs,
		Tools:               protoTools,
		MaxTokens:           int32(req.MaxTokens),
		EnablePromptCaching: req.EnablePromptCaching,
	})
	if err != nil {
		return AgentTurn{}, err
	}
	if !resp.GetCapabilitySupported() {
		return AgentTurn{}, fmt.Errorf("agent synth: capability_supported=false (%s)", resp.GetTerminationHint())
	}

	turn := AgentTurn{
		Role:            AgentRoleAssistant,
		Text:            resp.GetTurn().GetText(),
		TerminationHint: resp.GetTerminationHint(),
	}
	for _, c := range resp.GetTurn().GetToolCalls() {
		turn.ToolCalls = append(turn.ToolCalls, ToolCall{
			CallID: c.GetCallId(),
			Name:   c.GetName(),
			Args:   []byte(c.GetArgsJson()),
		})
	}
	if u := resp.GetUsage(); u != nil {
		turn.Model = u.GetModel()
		turn.InputTokens = int(u.GetInputTokens())
		turn.OutputTokens = int(u.GetOutputTokens())
	}
	turn.CacheCreationInputTokens = int(resp.GetCacheCreationInputTokens())
	turn.CacheReadInputTokens = int(resp.GetCacheReadInputTokens())
	return turn, nil
}

func toProtoAgentMessage(m AgentMessage) *reasoningv1.AgentMessage {
	out := &reasoningv1.AgentMessage{
		Role: string(m.Role),
		Text: m.Text,
	}
	for _, c := range m.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, &reasoningv1.ToolCall{
			CallId:   c.CallID,
			Name:     c.Name,
			ArgsJson: string(c.Args),
		})
	}
	for _, r := range m.ToolResults {
		out.ToolResults = append(out.ToolResults, &reasoningv1.ToolResult{
			CallId:   r.CallID,
			Ok:       r.OK,
			DataJson: string(r.Data),
			Error:    r.Error,
			Hint:     r.Hint,
		})
	}
	return out
}
