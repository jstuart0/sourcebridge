// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// AgentSynthesizer is the narrow worker surface the agent loop
// consumes. One call == one conversation turn. The worker is
// stateless; the orchestrator accumulates history and replays it
// each call.
type AgentSynthesizer interface {
	AnswerQuestionWithTools(ctx context.Context, req AgentTurnRequest) (AgentTurn, error)
	// SupportsTools reports whether the provider/model behind this
	// synthesizer can do structured tool use. When false, the loop
	// falls back to the single-shot path.
	SupportsTools() bool
}

// AgentTurnRequest is the orchestrator-side request for one turn.
// Typed here so internal/qa doesn't import provider SDK types.
type AgentTurnRequest struct {
	RepositoryID string
	Messages     []AgentMessage
	Tools        []ToolSchema
	MaxTokens    int
	// EnablePromptCaching routes through to the worker's Anthropic
	// cache_control markers. Orchestrator sources the bool from
	// Config.PromptCachingEnabled.
	EnablePromptCaching bool
}

// AgentLoopResult is the final envelope the loop returns to deepAsk.
type AgentLoopResult struct {
	// Answer is the model's final text. Citation tags are stripped
	// before populating this field — callers see clean prose.
	Answer string
	// RawAnswer is the final text WITH `[cite:<handle>]` tags still
	// in place. Retained in AskDebug.
	RawAnswer string
	// References were resolved from citation tags in RawAnswer.
	References []AskReference
	// ToolTrace is every tool call + result the loop executed, in
	// order. Retained in AskDebug; never surfaced as References.
	ToolTrace []AgentTraceEntry
	// Diagnostics the loop recorded as it ran. Merged into
	// AskResult.Diagnostics by deepAsk.
	TurnsCount          int
	ToolCallsCount      int
	ToolNames           []string
	TerminationReason   string
	Turn1TextOnly       bool
	LoopGuardTriggered  bool
	CitationFallbackUsed bool
	EvidenceTokens      int
	EvidenceExhausted   bool
	TotalTokens         int
	Model               string
	// Prompt-cache accounting summed across turns (Anthropic). Zero
	// when caching is disabled or the provider doesn't support it.
	CacheCreationInputTokens int
	CacheReadInputTokens     int
}

// AgentTraceEntry captures one (call, result) pair for diagnostics.
type AgentTraceEntry struct {
	Turn      int        `json:"turn"`
	Call      ToolCall   `json:"call"`
	Result    ToolResult `json:"result"`
	DurationMs int64     `json:"duration_ms"`
}

// ---- Loop budgets per plan §Loop Protocol ---------------------------

// toolCallBudget returns the per-ask cap keyed on question class.
// Architecture questions get a higher cap because they legitimately
// want multi-step graph traversal; all other classes stay at 6.
func toolCallBudget(kind QuestionKind) int {
	if kind == KindArchitecture {
		return 10
	}
	return 6
}

// evidenceTokenBudget is the aggregate cap on tool-result tokens
// delivered to the final synthesis turn. Enforced by rejecting the
// crossing tool call rather than trimming (plan §H9).
func evidenceTokenBudget(kind QuestionKind) int {
	switch kind {
	case KindArchitecture:
		return 20000
	case KindCrossCutting:
		return 16000
	case KindExecutionFlow:
		return 14000
	default:
		return 10000
	}
}

// Wall-clock cap for the whole loop; applies to every class.
//
// Empirically from Phase 3 benchmark: the agentic loop p95 landed at
// 44s with the old 60s cap, and 4 of 6 top-20 regressions were
// questions that hit the wall clock mid-synthesis and returned the
// error as the answer. 90s gives real headroom for architecture
// synthesis; beyond that the loop is thrashing and the timeout is
// the correct answer.
const agentWallClockBudget = 90 * time.Second

// Per-turn deadline cap (min with remaining budget). Raised from 30s
// after Phase 3 showed Sonnet 4.5 occasionally takes 35s on the
// final synthesis turn when context is dense (20+ tool results in
// scope).
const agentPerTurnDeadline = 45 * time.Second

// Per-tool deadline cap (min with remaining budget).
const agentPerToolDeadline = 5 * time.Second

// Number of consecutive exact-match tool calls before the
// loop-guard synthetic-recovery result fires.
const agentDedupThreshold = 3

// QuestionKind entry for cross-cutting needs to exist; the
// classifier emits "cross_cutting" as a string already.
const KindCrossCutting QuestionKind = "cross_cutting"

// ---- Loop entry ------------------------------------------------------

// RunAgentLoop executes the bounded tool-using loop with the
// default wall-clock budget (agentWallClockBudget). See
// RunAgentLoopWithBudget for the budget-taking variant used by the
// decomposition path.
func (o *Orchestrator) RunAgentLoop(
	ctx context.Context,
	in AskInput,
	kind QuestionKind,
	seedMessages []AgentMessage,
	synth AgentSynthesizer,
	dispatcher *AgentToolDispatcher,
) (*AgentLoopResult, error) {
	return o.RunAgentLoopWithBudget(ctx, in, kind, seedMessages, synth, dispatcher, agentWallClockBudget)
}

// RunAgentLoopWithBudget executes the bounded tool-using loop with
// a caller-specified wall-clock budget. The decomposition path
// (quality-push Phase 4) uses this to give each sub-loop a tighter
// budget than the parent orchestration. Returns a well-formed
// AgentLoopResult even on termination paths; Go errors are
// reserved for unrecoverable dispatcher misuse.
func (o *Orchestrator) RunAgentLoopWithBudget(
	ctx context.Context,
	in AskInput,
	kind QuestionKind,
	seedMessages []AgentMessage,
	synth AgentSynthesizer,
	dispatcher *AgentToolDispatcher,
	wallClock time.Duration,
) (*AgentLoopResult, error) {
	if synth == nil || !synth.SupportsTools() {
		return nil, errors.New("agent loop called but synthesizer lacks tool support")
	}
	if dispatcher == nil {
		return nil, errors.New("agent loop called with nil dispatcher")
	}
	if wallClock <= 0 {
		wallClock = agentWallClockBudget
	}

	loopStart := time.Now()
	result := &AgentLoopResult{}
	history := append([]AgentMessage(nil), seedMessages...)
	// Add the user question as the final seed message. Any additional
	// caller-supplied code/artifact/symbol pins are already in the
	// system-prompt portion of seedMessages.
	history = append(history, AgentMessage{
		Role: AgentRoleUser,
		Text: in.Question,
	})

	maxTools := toolCallBudget(kind)
	evidenceCap := evidenceTokenBudget(kind)
	toolNamesSeen := map[string]struct{}{}
	var recentCallSignatures []string

	// Progress: consumers see "planning…" before the first worker
	// round-trip. Safe no-op when no emitter is attached.
	emitProgress(ctx, loopStart, "planning")

	for turn := 1; ; turn++ {
		// Deadline propagation (plan §Deadline Propagation).
		remaining := wallClock - time.Since(loopStart)
		if remaining <= 0 {
			result.TerminationReason = "timeout"
			break
		}
		perTurn := agentPerTurnDeadline
		if remaining < perTurn {
			perTurn = remaining
		}
		turnCtx, turnCancel := context.WithTimeout(ctx, perTurn)

		// One round-trip to the worker.
		turnStart := time.Now()
		resp, err := synth.AnswerQuestionWithTools(turnCtx, AgentTurnRequest{
			RepositoryID:        in.RepositoryID,
			Messages:            history,
			Tools:               dispatcher.AvailableTools(),
			MaxTokens:           o.config.MaxAnswerTokens,
			EnablePromptCaching: o.config.PromptCachingEnabled,
		})
		turnCancel()
		if err != nil {
			// Parent cancellation propagates as "cancelled"; other
			// errors surface as "timeout" when the deadline fired and
			// "worker_error" otherwise.
			if ctx.Err() != nil {
				result.TerminationReason = "cancelled"
				break
			}
			if errors.Is(err, context.DeadlineExceeded) {
				result.TerminationReason = "timeout"
				break
			}
			result.TerminationReason = "worker_error"
			result.RawAnswer = fmt.Sprintf("agent turn failed: %v", err)
			break
		}
		result.TurnsCount = turn
		result.TotalTokens += resp.InputTokens + resp.OutputTokens
		result.CacheCreationInputTokens += resp.CacheCreationInputTokens
		result.CacheReadInputTokens += resp.CacheReadInputTokens
		if resp.Model != "" {
			result.Model = resp.Model
		}
		_ = turnStart // reserved for per-turn span timing if added

		// Text-only response → final answer, loop terminates.
		if len(resp.ToolCalls) == 0 {
			// Progress: the turn produced the final answer directly.
			emitProgress(ctx, loopStart, "synthesizing")
			result.RawAnswer = resp.Text
			if turn == 1 {
				result.Turn1TextOnly = true
			}
			result.TerminationReason = "answer"
			// Record the assistant turn in history so downstream debug
			// captures the exact text we synthesized from.
			history = append(history, AgentMessage{
				Role: AgentRoleAssistant,
				Text: resp.Text,
			})
			break
		}

		// Tool-use turn. Record the assistant message in history.
		history = append(history, AgentMessage{
			Role:      AgentRoleAssistant,
			Text:      resp.Text,
			ToolCalls: resp.ToolCalls,
		})

		// Budget gate: if this turn's calls would exceed the
		// tool-call cap, inject the evidence-exhausted error as the
		// result for EVERY call this turn. The model sees the
		// error, next turn is forced to answer.
		if result.ToolCallsCount+len(resp.ToolCalls) > maxTools {
			budgetResults := make([]ToolResult, 0, len(resp.ToolCalls))
			for _, c := range resp.ToolCalls {
				budgetResults = append(budgetResults, errResult(c.CallID, ErrEvidenceBudgetExhausted,
					"tool-call budget exhausted; answer from the evidence already in context"))
			}
			history = append(history, AgentMessage{
				Role:        AgentRoleToolResult,
				ToolResults: budgetResults,
			})
			result.ToolCallsCount += len(resp.ToolCalls)
			// Don't break — give the model one more turn to answer.
			// If that turn also emits tool_use we'll force-terminate
			// via the same gate.
			result.TerminationReason = "budget"
			continue
		}

		// Dedup guard: if the same (name, argsHash) triple has been
		// called `agentDedupThreshold` times consecutively, inject a
		// synthetic recovery result rather than forced-terminating
		// (plan §H2).
		callResults := make([]ToolResult, 0, len(resp.ToolCalls))
		for _, call := range resp.ToolCalls {
			sig := callSignature(call)
			recentCallSignatures = append(recentCallSignatures, sig)
			if len(recentCallSignatures) > agentDedupThreshold {
				recentCallSignatures = recentCallSignatures[len(recentCallSignatures)-agentDedupThreshold:]
			}
			if len(recentCallSignatures) == agentDedupThreshold && allEqual(recentCallSignatures) {
				// Inject synthetic recovery result. Don't terminate.
				result.LoopGuardTriggered = true
				callResults = append(callResults, ToolResult{
					CallID: call.CallID,
					OK:     false,
					Error:  "repeated_call",
					Hint:   "this tool call has returned the same input three times in a row; try a different tool or different arguments",
				})
				// Reset so we don't keep firing on the same signature.
				recentCallSignatures = nil
				continue
			}

			// Evidence-budget gate: project this call's worst-case
			// size before executing. If projection exceeds the cap,
			// reject the call with evidence_budget_exhausted
			// (reject-and-force-synthesis per §H9).
			projection := projectToolResultTokens(call)
			if result.EvidenceTokens+projection > evidenceCap {
				result.EvidenceExhausted = true
				callResults = append(callResults, errResult(call.CallID, ErrEvidenceBudgetExhausted,
					"evidence budget exhausted; answer from the evidence already in context. Do not call more retrieval tools."))
				continue
			}

			toolCtx, toolCancel := context.WithTimeout(ctx, minDuration(
				agentPerToolDeadline,
				wallClock-time.Since(loopStart),
			))
			// Progress: tool_call. Emit before dispatch so clients
			// can show "agent is calling search_evidence…" while the
			// tool runs.
			emitProgress(ctx, loopStart, "tool_call", withTool(call.Name))
			execStart := time.Now()
			toolResult := dispatcher.Dispatch(toolCtx, call)
			execMs := time.Since(execStart).Milliseconds()
			toolCancel()
			emitProgress(ctx, loopStart, "tool_result",
				withTool(call.Name),
				withDuration(time.Since(execStart)),
			)

			// Record evidence tokens (best-effort approximation).
			result.EvidenceTokens += approxTokens(toolResult)

			// Record trace entry.
			result.ToolTrace = append(result.ToolTrace, AgentTraceEntry{
				Turn:       turn,
				Call:       call,
				Result:     toolResult,
				DurationMs: execMs,
			})
			toolNamesSeen[call.Name] = struct{}{}
			result.ToolCallsCount++

			callResults = append(callResults, toolResult)
		}

		// Append all tool results for this turn in one tool_result
		// message. Anthropic expects them paired as a single user-role
		// content block.
		history = append(history, AgentMessage{
			Role:        AgentRoleToolResult,
			ToolResults: callResults,
		})
	}

	// Stable, sorted tool names for diagnostics.
	for name := range toolNamesSeen {
		result.ToolNames = append(result.ToolNames, name)
	}
	sort.Strings(result.ToolNames)

	// Reference resolution (§Reference Emission Contract, v5 citation-
	// driven with structural fallback).
	refs, fallback := resolveReferencesFromAnswer(result.RawAnswer, history)
	result.References = refs
	result.CitationFallbackUsed = fallback

	// Strip citation tags from user-visible answer.
	result.Answer = stripCitationTags(result.RawAnswer)

	// Progress: loop terminated. TerminationReason carries the
	// structural outcome (answer / timeout / budget / worker_error
	// / cancelled) so clients can distinguish "done because the
	// agent produced an answer" from "done because we timed out."
	emitProgress(ctx, loopStart, "done", withTermination(result.TerminationReason))

	return result, nil
}

// callSignature is a stable identity for dedup detection.
func callSignature(c ToolCall) string {
	h := sha256.Sum256(c.Args)
	return c.Name + ":" + hex.EncodeToString(h[:8])
}

func allEqual(ss []string) bool {
	if len(ss) == 0 {
		return false
	}
	first := ss[0]
	for _, s := range ss[1:] {
		if s != first {
			return false
		}
	}
	return true
}

// projectToolResultTokens gives a conservative upper bound on the
// tokens a tool call might return, used for the evidence-budget
// gate BEFORE executing. Per-tool caps are known so we use them.
func projectToolResultTokens(call ToolCall) int {
	switch call.Name {
	case ToolSearchEvidence:
		return 3000 // 20 results × ~150 tokens each
	case ToolReadFile:
		return 5000 // 500-line ceiling × ~10 tokens/line
	case ToolGetCallers, ToolGetCallees:
		return 1200 // 25 neighbors × ~48 tokens each
	case ToolGetSummary:
		return 800 // one summary, ~600 tokens typical
	case ToolGetRequirements:
		return 2500 // 25 requirements × ~100 tokens each
	case ToolFindTests:
		// 10 tests × ~300 tokens/each (body + assertions), plus
		// per-test metadata overhead.
		return 3500
	}
	return 1500
}

// approxTokens is the rough token count of a ToolResult payload.
// 4 chars ≈ 1 token is the standard rule-of-thumb; good enough for
// budget accounting at this scale.
func approxTokens(r ToolResult) int {
	if !r.OK {
		return len(r.Error)/4 + len(r.Hint)/4 + 1
	}
	return len(r.Data)/4 + 1
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// stripCitationTags removes inline `[cite:<handle>]` markers from
// the user-visible answer. We preserve the raw in AskDebug.
func stripCitationTags(s string) string {
	out := s
	for {
		i := strings.Index(out, "[cite:")
		if i < 0 {
			break
		}
		j := strings.Index(out[i:], "]")
		if j < 0 {
			break
		}
		// Also remove a leading space so we don't leave double spaces.
		start := i
		if start > 0 && out[start-1] == ' ' {
			start--
		}
		out = out[:start] + out[i+j+1:]
	}
	return strings.TrimSpace(out)
}
