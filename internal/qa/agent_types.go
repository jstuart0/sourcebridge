// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import "encoding/json"

// Provider-neutral types for the agentic loop. See
// thoughts/shared/plans/2026-04-23-agentic-retrieval-for-deep-qa.md
// §Provider-Neutral Protocol.

// AgentRole is the role of one message in the agentic conversation
// history. Matches the proto's string values exactly.
type AgentRole string

const (
	AgentRoleSystem     AgentRole = "system"
	AgentRoleUser       AgentRole = "user"
	AgentRoleAssistant  AgentRole = "assistant"
	AgentRoleToolResult AgentRole = "tool_result"
)

// ToolCall is a model-issued request to run a tool.
type ToolCall struct {
	CallID string          `json:"call_id"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args"`
}

// ToolResult is the dispatcher's response to a ToolCall. Shape is
// the standardized {ok, data, error, hint} envelope (§Tool Catalog).
type ToolResult struct {
	CallID string          `json:"call_id"`
	OK     bool            `json:"ok"`
	Data   json.RawMessage `json:"data,omitempty"`
	Error  string          `json:"error,omitempty"`
	Hint   string          `json:"hint,omitempty"`
}

// AgentMessage is one turn in the conversation history. Exactly one
// of Text / ToolCalls / ToolResults is populated, keyed by Role:
//
//   - system    → Text is the system prompt
//   - user      → Text is the user's message (including the initial
//     question and any loop-injected synthetic user prompts)
//   - assistant → either Text (final answer) OR ToolCalls (tool-use
//     turn), never both
//   - tool_result → ToolResults pairs with a prior assistant
//     ToolCalls message by CallID
type AgentMessage struct {
	Role        AgentRole    `json:"role"`
	Text        string       `json:"text,omitempty"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
	ToolResults []ToolResult `json:"tool_results,omitempty"`
}

// AgentTurn is the provider-neutral response from one round trip
// to the worker. Exactly one of Text / ToolCalls is populated.
type AgentTurn struct {
	Role            AgentRole  `json:"role"` // always assistant
	Text            string     `json:"text,omitempty"`
	ToolCalls       []ToolCall `json:"tool_calls,omitempty"`
	InputTokens     int        `json:"input_tokens"`
	OutputTokens    int        `json:"output_tokens"`
	Model           string     `json:"model"`
	TerminationHint string     `json:"termination_hint,omitempty"`
}

// ProviderCapabilities is the cached capability snapshot the
// orchestrator probes on startup. Drives the agentic vs single-shot
// gate.
type ProviderCapabilities struct {
	Provider               string `json:"provider"`
	Model                  string `json:"model"`
	ToolUseSupported       bool   `json:"tool_use_supported"`
	PromptCachingSupported bool   `json:"prompt_caching_supported"`
}
