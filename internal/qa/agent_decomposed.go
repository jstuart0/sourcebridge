// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	"github.com/sourcebridge/sourcebridge/internal/citations"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
)

// Decomposition budgets (quality-push Phase 4). These bracket the
// single-loop budgets defined in agent_loop.go. Parent wall clock
// covers the whole orchestration: decompose call + N parallel
// sub-loops + synthesis call. Each sub-loop gets a tighter budget
// so the overall p95 lands within the Decision Rule's p95 ceiling.
const (
	// decompParentWallClock is the total budget for the whole
	// decomposition flow. 180s covers: ~3s decompose + up to 60s
	// of parallel sub-loops + ~20s synthesis + slack. Raised from
	// the initial 120s after first smoke showed sub-loops cut off
	// at 30s were hitting deadline on the final synthesis turn.
	decompParentWallClock = 180 * time.Second

	// decompSubLoopWallClock caps each sub-loop. 60s per sub-loop
	// gives Sonnet 4.5 room for 3–5 turns including a dense
	// final-answer synthesis turn. Since sub-loops run in parallel
	// (bounded at decompMaxConcurrent), the parent budget only
	// needs to absorb the slowest sub-loop, not the sum.
	decompSubLoopWallClock = 60 * time.Second

	// decompMaxConcurrent bounds parallel sub-loop execution. Four
	// parallel paths keep the worker lane utilisation reasonable
	// without over-committing tokens on the repo.
	decompMaxConcurrent = 4
)

// subAnswer carries the output of one sub-loop into the final
// synthesis turn.
type subAnswer struct {
	question          string
	answer            string
	referenceHandles  []string
	toolCallsCount    int
	terminationReason string
	cacheCreationToks int
	cacheReadToks     int
}

// runDecomposed executes the full Phase-4 decomposition pipeline:
// decompose → parallel sub-loops → final synthesis. Returns
// (nil, nil) when the caller should fall through to the single-
// loop path (atomic question, decomposer disabled, no synthesizer).
// Returns (result, nil) on success; (nil, err) on a wiring failure.
//
// The caller (runAgentic) is responsible for recording
// StageTimings["qa.ask"] after this returns.
func (o *Orchestrator) runDecomposed(
	ctx context.Context,
	in AskInput,
	kind QuestionKind,
	profile QuestionProfile,
	summaries []SummaryEvidence,
	result *AskResult,
) (*AskResult, error) {
	if o == nil || o.decomposer == nil || o.decompSynthesizer == nil || o.agent == nil {
		return nil, nil
	}

	// 1. Decompose.
	decomposeCtx, decomposeCancel := context.WithTimeout(ctx, 3*time.Second)
	t1 := time.Now()
	subQuestions, err := o.decomposer.Decompose(decomposeCtx, in, kind)
	decomposeCancel()
	result.Diagnostics.StageTimings["qa.decompose"] = FromDuration(time.Since(t1))
	if err != nil {
		// Fall through to single-loop on decomposer error.
		return nil, nil
	}
	// <= 1 sub-questions means the decomposer thinks the question
	// is atomic. Don't spawn parallel infrastructure for a no-op.
	if len(subQuestions) <= 1 {
		return nil, nil
	}

	result.Diagnostics.DecompositionUsed = true
	result.Diagnostics.SubQuestionCount = len(subQuestions)

	// 2. Bound the whole orchestration so a stuck sub-loop can't
	// run away with the parent wall clock.
	parentCtx, parentCancel := context.WithTimeout(ctx, decompParentWallClock)
	defer parentCancel()

	// 3. Fan out sub-loops. A semaphore channel caps concurrency.
	sem := make(chan struct{}, decompMaxConcurrent)
	answers := make([]subAnswer, len(subQuestions))
	var wg sync.WaitGroup
	t2 := time.Now()
	for i, sq := range subQuestions {
		wg.Add(1)
		go func(idx int, subQ string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			answers[idx] = o.runSubLoop(parentCtx, in, kind, profile, summaries, subQ)
		}(i, sq)
	}
	wg.Wait()
	result.Diagnostics.StageTimings["qa.decompose.subloops"] = FromDuration(time.Since(t2))

	// 4. Synthesis. Feeds all sub-answers into a single turn that
	// returns the final answer.
	t3 := time.Now()
	synthResp, err := o.decompSynthesizer.SynthesizeDecomposedAnswer(parentCtx,
		in.RepositoryID, resolution.OpQADeepSynth,
		buildSynthesisRequest(in, answers, o.config.PromptCachingEnabled),
	)
	result.Diagnostics.StageTimings["qa.decompose.synthesize"] = FromDuration(time.Since(t3))
	if err != nil {
		return nil, fmt.Errorf("synthesis failed: %w", err)
	}

	// 5. Populate AskResult.
	result.Answer = stripCitationTags(synthResp.GetAnswer())
	result.References = unionSubAnswerReferences(answers, synthResp.GetAnswer())
	if result.References == nil {
		result.References = []AskReference{}
	}

	// Aggregate diagnostics across sub-loops.
	result.Diagnostics.AgenticUsed = true
	result.Diagnostics.ToolCallsCount = 0
	toolNamesSet := map[string]struct{}{}
	totalCacheRead := 0
	totalCacheCreation := 0
	for _, sa := range answers {
		result.Diagnostics.ToolCallsCount += sa.toolCallsCount
		totalCacheRead += sa.cacheReadToks
		totalCacheCreation += sa.cacheCreationToks
	}
	// Tool names union — best-effort; we don't track per-sub
	// tool-name lists, so this stays empty in decomposition diag
	// unless a future refactor plumbs them through subAnswer.
	_ = toolNamesSet

	result.Diagnostics.CacheCreationInputTokens = totalCacheCreation + int(synthResp.GetCacheCreationInputTokens())
	result.Diagnostics.CacheReadInputTokens = totalCacheRead + int(synthResp.GetCacheReadInputTokens())
	result.Diagnostics.TerminationReason = "decomposed"

	if u := synthResp.GetUsage(); u != nil {
		result.Usage.Model = u.GetModel()
		result.Usage.InputTokens = int(u.GetInputTokens())
		result.Usage.OutputTokens = int(u.GetOutputTokens())
		result.Diagnostics.ModelUsed = u.GetModel()
	}

	if in.IncludeDebug {
		result.Debug = &AskDebug{
			Prompt: fmt.Sprintf(
				"decomposed: %d sub-questions, %d total tool calls",
				len(subQuestions), result.Diagnostics.ToolCallsCount,
			),
			ContextMarkdown: synthResp.GetAnswer(),
		}
	}

	return result, nil
}

// runSubLoop runs one sub-question through its own agentic loop
// with a tighter budget. Used by runDecomposed's parallel fan-out.
// Returns a subAnswer even on failure — the parent synthesizer
// handles empty answers gracefully.
func (o *Orchestrator) runSubLoop(
	ctx context.Context,
	parentIn AskInput,
	kind QuestionKind,
	profile QuestionProfile,
	summaries []SummaryEvidence,
	subQuestion string,
) subAnswer {
	subIn := parentIn
	subIn.Question = subQuestion

	seed := buildAgentSeedMessages(subIn, kind, summaries, profile.EvidenceHints)
	dispatcher := NewAgentToolDispatcher(o, subIn.RepositoryID)

	loop, err := o.RunAgentLoopWithBudget(ctx, subIn, kind, seed, o.agent, dispatcher, decompSubLoopWallClock)
	sa := subAnswer{question: subQuestion}
	if err != nil || loop == nil {
		sa.terminationReason = "worker_error"
		return sa
	}
	sa.answer = loop.RawAnswer // preserve citation tags for synthesis
	sa.toolCallsCount = loop.ToolCallsCount
	sa.terminationReason = loop.TerminationReason
	sa.cacheCreationToks = loop.CacheCreationInputTokens
	sa.cacheReadToks = loop.CacheReadInputTokens
	// Extract handles from the loop's final-turn references so the
	// synthesizer prompt sees them under each sub-answer.
	handles := make([]string, 0, len(loop.References))
	for _, ref := range loop.References {
		h := handleFromReference(ref)
		if h != "" {
			handles = append(handles, h)
		}
	}
	sort.Strings(handles)
	sa.referenceHandles = handles
	return sa
}

// buildSynthesisRequest formats the final-synthesis RPC payload.
func buildSynthesisRequest(in AskInput, answers []subAnswer, cacheEnabled bool) *reasoningv1.SynthesizeDecomposedAnswerRequest {
	protoAnswers := make([]*reasoningv1.DecomposedSubAnswer, 0, len(answers))
	for _, a := range answers {
		protoAnswers = append(protoAnswers, &reasoningv1.DecomposedSubAnswer{
			SubQuestion:       a.question,
			SubAnswer:         a.answer,
			ReferenceHandles:  a.referenceHandles,
			TerminationReason: a.terminationReason,
			ToolCallsCount:    int32(a.toolCallsCount),
		})
	}
	return &reasoningv1.SynthesizeDecomposedAnswerRequest{
		RepositoryId:         in.RepositoryID,
		OriginalQuestion:     in.Question,
		SubAnswers:           protoAnswers,
		EnablePromptCaching:  cacheEnabled,
	}
}

// unionSubAnswerReferences collects references from every sub-loop,
// deduped by handle. The final-synthesis answer may cite handles
// using `[cite:...]`; the resolver parses those, but we still
// need a reference set when citations are missing.
func unionSubAnswerReferences(answers []subAnswer, synthAnswer string) []AskReference {
	// Build a pseudo tool_result message containing every referenced
	// handle so the existing resolveReferencesFromAnswer logic can
	// flatten them. This avoids re-implementing handle→AskReference
	// mapping here.
	seen := map[string]struct{}{}
	refs := []AskReference{}
	for _, a := range answers {
		for _, h := range a.referenceHandles {
			if _, dup := seen[h]; dup {
				continue
			}
			seen[h] = struct{}{}
			refs = append(refs, AskReference{
				Kind:  RefKindFileRange, // best-effort; refFromHandle would need the full row to do better
				Title: h,
				FileRange: &FileRangeRef{
					FilePath: handlePath(h),
				},
			})
		}
	}
	return refs
}

// handleFromReference extracts the stable handle string from an
// AskReference. Inverse of the handle-construction in agent_tools.go.
// Delegates to the shared citations package so the format is canonical.
func handleFromReference(ref AskReference) string {
	switch ref.Kind {
	case RefKindSymbol:
		if ref.Symbol != nil && ref.Symbol.SymbolID != "" {
			return citations.FormatSymbol(ref.Symbol.SymbolID)
		}
	case RefKindFileRange:
		if ref.FileRange != nil && ref.FileRange.FilePath != "" {
			return citations.FormatFileRange(ref.FileRange.FilePath, ref.FileRange.StartLine, ref.FileRange.EndLine)
		}
	case RefKindRequirement:
		if ref.Requirement != nil {
			return ref.Requirement.ExternalID
		}
	case RefKindUnderstandingSection:
		if ref.UnderstandingSection != nil && ref.UnderstandingSection.SectionID != "" {
			return ref.UnderstandingSection.SectionID
		}
	}
	return ref.Title
}

// handlePath pulls the file-path prefix from a `path:start-end`
// handle. Returns the handle unchanged when it doesn't match the
// pattern.
func handlePath(handle string) string {
	for i := len(handle) - 1; i >= 0; i-- {
		if handle[i] == ':' {
			return handle[:i]
		}
	}
	return handle
}
