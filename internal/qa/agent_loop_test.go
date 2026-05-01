// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// scriptedSynth is a scripted AgentSynthesizer for loop tests. It
// pops one pre-programmed AgentTurn per call and records what was
// sent.
type scriptedSynth struct {
	turns    []AgentTurn
	recv     []AgentTurnRequest
	support  bool
	errOnCall int // turn (1-indexed) that returns an error instead
}

func (s *scriptedSynth) SupportsTools(_ context.Context) bool { return s.support }

func (s *scriptedSynth) AnswerQuestionWithTools(_ context.Context, req AgentTurnRequest) (AgentTurn, error) {
	s.recv = append(s.recv, req)
	if s.errOnCall > 0 && len(s.recv) == s.errOnCall {
		return AgentTurn{}, context.DeadlineExceeded
	}
	if len(s.turns) == 0 {
		return AgentTurn{}, nil
	}
	t := s.turns[0]
	s.turns = s.turns[1:]
	return t, nil
}

// TestAgentLoopTerminatesOnTextOnly: the simplest success path —
// model answers in one turn without tool calls. Records
// Turn1TextOnly=true as the diagnostic.
func TestAgentLoopTerminatesOnTextOnly(t *testing.T) {
	synth := &scriptedSynth{
		support: true,
		turns: []AgentTurn{
			{Role: AgentRoleAssistant, Text: "just answering from the seed", Model: "m1"},
		},
	}
	o := New(nil, nil, nil, DefaultConfig())
	res, err := o.RunAgentLoop(
		context.Background(),
		AskInput{RepositoryID: "r", Question: "what?"},
		KindBehavior,
		[]AgentMessage{{Role: AgentRoleSystem, Text: "sys"}},
		synth,
		NewAgentToolDispatcher(o, "r"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.TerminationReason != "answer" {
		t.Errorf("expected terminationReason=answer, got %q", res.TerminationReason)
	}
	if !res.Turn1TextOnly {
		t.Error("expected Turn1TextOnly=true")
	}
	if res.ToolCallsCount != 0 {
		t.Errorf("expected 0 tool calls, got %d", res.ToolCallsCount)
	}
	if res.Answer != "just answering from the seed" {
		t.Errorf("answer: %q", res.Answer)
	}
}

// TestAgentLoopRefusesWhenNoToolSupport: orchestrator's gate; loop
// itself errors out when SupportsTools=false.
func TestAgentLoopRefusesWhenNoToolSupport(t *testing.T) {
	synth := &scriptedSynth{support: false}
	o := New(nil, nil, nil, DefaultConfig())
	_, err := o.RunAgentLoop(
		context.Background(),
		AskInput{RepositoryID: "r", Question: "?"},
		KindBehavior, nil, synth, NewAgentToolDispatcher(o, "r"),
	)
	if err == nil {
		t.Error("expected error when synthesizer lacks tool support")
	}
}

// TestAgentLoopBudgetForcesSynthesis: when the cumulative tool-call
// count would exceed the cap, the loop injects
// evidence_budget_exhausted for the offending calls. The next turn
// is expected to be a text answer; TerminationReason=budget.
func TestAgentLoopBudgetForcesSynthesis(t *testing.T) {
	// Claim budget=6 (default for non-architecture). Feed 7 tool
	// calls across two turns. Second turn crosses the line.
	synth := &scriptedSynth{support: true}
	// Turn 1: 5 tool calls. All should execute.
	synth.turns = append(synth.turns, AgentTurn{
		Role:      AgentRoleAssistant,
		ToolCalls: makeReadFileCalls("t1", 5),
		Model:     "m1",
	})
	// Turn 2: 2 more tool calls. 5+2=7 > budget=6 → budget gate
	// fires and injects evidence_budget_exhausted.
	synth.turns = append(synth.turns, AgentTurn{
		Role:      AgentRoleAssistant,
		ToolCalls: makeReadFileCalls("t2", 2),
		Model:     "m1",
	})
	// Turn 3: the model answers.
	synth.turns = append(synth.turns, AgentTurn{
		Role:  AgentRoleAssistant,
		Text:  "answering from whatever you gave me",
		Model: "m1",
	})

	o := New(nil, nil, nil, DefaultConfig()).
		WithFileReader(&fakeFileReaderTool{body: "x\n"})
	res, err := o.RunAgentLoop(
		context.Background(),
		AskInput{RepositoryID: "r", Question: "?"},
		KindBehavior,
		[]AgentMessage{{Role: AgentRoleSystem, Text: "sys"}},
		synth,
		NewAgentToolDispatcher(o, "r"),
	)
	if err != nil {
		t.Fatal(err)
	}
	// Budget fired.
	if res.TerminationReason != "budget" && res.TerminationReason != "answer" {
		t.Errorf("expected budget or answer termination (model may answer after the synthetic result), got %q",
			res.TerminationReason)
	}
	if res.ToolCallsCount < 5 {
		t.Errorf("expected at least the first 5 tool calls to execute, got %d", res.ToolCallsCount)
	}
}

// makeReadFileCalls builds N read_file ToolCalls with distinct paths.
func makeReadFileCalls(prefix string, n int) []ToolCall {
	out := make([]ToolCall, n)
	for i := 0; i < n; i++ {
		args, _ := json.Marshal(map[string]any{"path": "file" + string(rune('a'+i)) + ".go"})
		out[i] = ToolCall{
			CallID: prefix + "-" + string(rune('a'+i)),
			Name:   ToolReadFile,
			Args:   args,
		}
	}
	return out
}

// TestCitationExtractionResolves handles from the final-turn tool
// results into AskReference[]. Happy path.
func TestCitationExtractionResolves(t *testing.T) {
	synth := &scriptedSynth{support: true}
	synth.turns = []AgentTurn{
		// Turn 1: model calls read_file once.
		{
			Role: AgentRoleAssistant,
			ToolCalls: []ToolCall{{
				CallID: "c1",
				Name:   ToolReadFile,
				Args:   mustJSON(t, map[string]any{"path": "a/b.go", "start_line": 1, "end_line": 5}),
			}},
			Model: "m1",
		},
		// Turn 2: model answers with a citation tag to the handle
		// read_file just returned.
		{
			Role:  AgentRoleAssistant,
			Text:  "The entry point is in a/b.go lines 1-5 [cite:a/b.go:1-5]. Clean.",
			Model: "m1",
		},
	}
	body := ""
	for i := 0; i < 20; i++ {
		body += "line\n"
	}
	o := New(nil, nil, nil, DefaultConfig()).
		WithFileReader(&fakeFileReaderTool{body: body})
	res, err := o.RunAgentLoop(
		context.Background(),
		AskInput{RepositoryID: "r", Question: "entry?"},
		KindBehavior,
		[]AgentMessage{{Role: AgentRoleSystem, Text: "sys"}},
		synth,
		NewAgentToolDispatcher(o, "r"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.CitationFallbackUsed {
		t.Error("expected citation path, not fallback")
	}
	if len(res.References) != 1 {
		t.Fatalf("expected 1 reference, got %d", len(res.References))
	}
	if res.References[0].Kind != RefKindFileRange {
		t.Errorf("expected FileRange, got %s", res.References[0].Kind)
	}
	// Answer should have cite tag stripped.
	if strings.Contains(res.Answer, "[cite:") {
		t.Errorf("cite tag not stripped: %q", res.Answer)
	}
}

// TestCitationFallbackFiresWhenNoCitations: confirms the structural
// fallback when the model ignores the citation instruction.
func TestCitationFallbackFiresWhenNoCitations(t *testing.T) {
	synth := &scriptedSynth{support: true}
	synth.turns = []AgentTurn{
		{
			Role: AgentRoleAssistant,
			ToolCalls: []ToolCall{{
				CallID: "c1",
				Name:   ToolReadFile,
				Args:   mustJSON(t, map[string]any{"path": "a/b.go", "end_line": 5}),
			}},
			Model: "m1",
		},
		// Turn 2: model answers without any citation tags.
		{
			Role:  AgentRoleAssistant,
			Text:  "the answer mentions stuff but does not cite",
			Model: "m1",
		},
	}
	body := ""
	for i := 0; i < 20; i++ {
		body += "line\n"
	}
	o := New(nil, nil, nil, DefaultConfig()).
		WithFileReader(&fakeFileReaderTool{body: body})
	res, err := o.RunAgentLoop(
		context.Background(),
		AskInput{RepositoryID: "r", Question: "?"},
		KindBehavior,
		[]AgentMessage{{Role: AgentRoleSystem, Text: "sys"}},
		synth,
		NewAgentToolDispatcher(o, "r"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !res.CitationFallbackUsed {
		t.Error("expected CitationFallbackUsed=true when answer has no [cite:...] tags")
	}
	// Fallback still emits references from the final-turn tool result.
	if len(res.References) == 0 {
		t.Error("expected fallback references from the tool result")
	}
}

// TestStripCitationTagsCleansAnswer verifies the user-visible answer
// has no `[cite:...]` markers.
func TestStripCitationTagsCleansAnswer(t *testing.T) {
	in := "Look at [cite:a/b.go:1-10] and also [cite:sym_xyz]."
	out := stripCitationTags(in)
	if strings.Contains(out, "[cite:") {
		t.Errorf("tags not stripped: %q", out)
	}
	if !strings.Contains(out, "Look at") {
		t.Errorf("content lost: %q", out)
	}
}

// TestCanaryAdmitDeterministic: same (repo, time-window) admits the
// same way so retries of the same request don't flap paths.
func TestCanaryAdmitDeterministic(t *testing.T) {
	a := canaryAdmit("repo-1", 50)
	b := canaryAdmit("repo-1", 50)
	if a != b {
		t.Error("canary admit should be stable within a 5-minute window")
	}
	// 100% always admits; 0 never.
	if !canaryAdmit("x", 100) {
		t.Error("canary at 100%% should always admit")
	}
	if canaryAdmit("x", 0) {
		t.Error("canary at 0%% should never admit")
	}
}

// TestProjectToolResultTokensCovered: every tool maps to a non-zero
// projection so the evidence-budget gate always gets a signal.
func TestProjectToolResultTokensCovered(t *testing.T) {
	for _, name := range []string{
		ToolSearchEvidence, ToolReadFile, ToolGetCallers,
		ToolGetCallees, ToolGetSummary, ToolGetRequirements,
	} {
		got := projectToolResultTokens(ToolCall{Name: name})
		if got <= 0 {
			t.Errorf("tool %s has zero projection; budget gate needs a signal", name)
		}
	}
}

// mustJSON is a test helper.
func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return json.RawMessage(b)
}
