// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	commonv1 "github.com/sourcebridge/sourcebridge/gen/go/common/v1"
	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
	"github.com/sourcebridge/sourcebridge/internal/usage"
)

func TestMain(m *testing.M) {
	code := m.Run()
	usage.ResetCountersForTest()
	os.Exit(code)
}

type fakeSynth struct {
	available bool
	resp      *reasoningv1.AnswerQuestionResponse
	err       error
	lastReq   *reasoningv1.AnswerQuestionRequest
}

func (f *fakeSynth) IsAvailable() bool { return f.available }
func (f *fakeSynth) AnswerQuestion(_ context.Context, _, _ string, req *reasoningv1.AnswerQuestionRequest) (*reasoningv1.AnswerQuestionResponse, error) {
	f.lastReq = req
	return f.resp, f.err
}

func TestOrchestrator_FastHappyPath(t *testing.T) {
	synth := &fakeSynth{
		available: true,
		resp: &reasoningv1.AnswerQuestionResponse{
			Answer: "Auth starts with magic links.",
			ReferencedSymbols: []*commonv1.CodeSymbol{
				{
					Id:            "sym-1",
					QualifiedName: "auth.Handler",
					Location:      &commonv1.FileLocation{Path: "auth/handler.go", StartLine: 12, EndLine: 40},
					Language:      commonv1.Language_LANGUAGE_GO,
				},
			},
			Usage: &commonv1.LLMUsage{Model: "claude-sonnet-4-6", InputTokens: 100, OutputTokens: 40},
		},
	}
	o := New(synth, nil, nil, DefaultConfig())

	res, err := o.Ask(context.Background(), AskInput{
		RepositoryID: "repo-1",
		Question:     "How does auth work?",
		Mode:         ModeFast,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer != "Auth starts with magic links." {
		t.Errorf("answer = %q", res.Answer)
	}
	if len(res.References) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(res.References))
	}
	if res.References[0].Symbol.FilePath != "auth/handler.go" {
		t.Errorf("ref path = %q", res.References[0].Symbol.FilePath)
	}
	if res.Usage.Model != "claude-sonnet-4-6" || res.Usage.InputTokens != 100 {
		t.Errorf("usage = %+v", res.Usage)
	}
	if res.Diagnostics.QuestionType != string(KindExecutionFlow) {
		t.Errorf("expected execution_flow, got %q", res.Diagnostics.QuestionType)
	}
	if _, ok := res.Diagnostics.StageTimings["qa.classify"]; !ok {
		t.Error("missing classify timing")
	}
	if _, ok := res.Diagnostics.StageTimings["qa.llm_call"]; !ok {
		t.Error("missing llm_call timing")
	}
}

func TestOrchestrator_RejectsMissingQuestion(t *testing.T) {
	o := New(&fakeSynth{available: true}, nil, nil, DefaultConfig())
	_, err := o.Ask(context.Background(), AskInput{RepositoryID: "r"})
	if !IsInvalidInput(err) {
		t.Fatalf("expected InvalidInputError, got %v", err)
	}
}

func TestOrchestrator_RejectsMissingRepo(t *testing.T) {
	o := New(&fakeSynth{available: true}, nil, nil, DefaultConfig())
	_, err := o.Ask(context.Background(), AskInput{Question: "hi"})
	if !IsInvalidInput(err) {
		t.Fatalf("expected InvalidInputError, got %v", err)
	}
}

func TestOrchestrator_RejectsOversizedQuestion(t *testing.T) {
	o := New(&fakeSynth{available: true}, nil, nil, Config{QuestionMaxBytes: 10})
	_, err := o.Ask(context.Background(), AskInput{
		RepositoryID: "r",
		Question:     strings.Repeat("x", 11),
	})
	if !IsInvalidInput(err) {
		t.Fatalf("expected InvalidInputError, got %v", err)
	}
}

func TestOrchestrator_WorkerUnavailable(t *testing.T) {
	o := New(&fakeSynth{available: false}, nil, nil, DefaultConfig())
	res, err := o.Ask(context.Background(), AskInput{RepositoryID: "r", Question: "hi there"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Diagnostics.FallbackUsed != "worker_unavailable" {
		t.Errorf("expected worker_unavailable fallback, got %q", res.Diagnostics.FallbackUsed)
	}
	if res.Answer == "" {
		t.Error("expected user-facing answer even on fallback")
	}
}

func TestOrchestrator_SynthesisError(t *testing.T) {
	synth := &fakeSynth{available: true, err: errors.New("network boom")}
	o := New(synth, nil, nil, DefaultConfig())
	res, err := o.Ask(context.Background(), AskInput{RepositoryID: "r", Question: "hi there"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Diagnostics.FallbackUsed != "synthesis_failed" {
		t.Errorf("expected synthesis_failed, got %q", res.Diagnostics.FallbackUsed)
	}
	if !strings.Contains(res.Answer, "network boom") {
		t.Errorf("answer should surface error: %q", res.Answer)
	}
}

func TestOrchestrator_SerializesConversationHistory(t *testing.T) {
	synth := &fakeSynth{
		available: true,
		resp:      &reasoningv1.AnswerQuestionResponse{Answer: "ok"},
	}
	o := New(synth, nil, nil, DefaultConfig())
	_, err := o.Ask(context.Background(), AskInput{
		RepositoryID:  "r",
		Question:      "latest turn",
		PriorMessages: []string{"previous turn 1", "previous turn 2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if synth.lastReq == nil {
		t.Fatal("no request captured")
	}
	if !strings.Contains(synth.lastReq.GetQuestion(), "previous turn 1") {
		t.Errorf("prior messages not threaded into prompt: %q", synth.lastReq.GetQuestion())
	}
	// Fix A (CA-324): req.Question must NOT contain <question> XML tags —
	// those caused the worker's build_discussion_prompt to render
	// "Question: [full envelope]" with the real question buried inside.
	// The injection-guard is now reconstructed worker-side.
	if strings.Contains(synth.lastReq.GetQuestion(), "<question>") {
		t.Errorf("req.Question must not contain XML <question> tags; got %q", synth.lastReq.GetQuestion())
	}
	// The bare user question must appear in the question payload.
	if !strings.Contains(synth.lastReq.GetQuestion(), "latest turn") {
		t.Errorf("bare question not in req.Question: %q", synth.lastReq.GetQuestion())
	}
}

// TestResolveAskModel_ResolverWinsOverStaticConfig pins the precedence in
// resolveAskModel (CA-395 / T-M4): when AskModelResolver returns a non-empty
// value it must take precedence over the static AskModel field. Without this
// test, silently inverting the precedence check or short-circuiting the resolver
// call would leave the live-profile model unused across all callers.
func TestResolveAskModel_ResolverWinsOverStaticConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.AskModel = "static-model"
	cfg.AskModelResolver = func(_ context.Context) string { return "live-model" }
	o := New(nil, nil, nil, cfg)
	if got := o.resolveAskModel(context.Background()); got != "live-model" {
		t.Errorf("expected resolver value 'live-model', got %q", got)
	}
}

// TestResolveAskModel_ResolverEmptyFallsBackToStatic pins that when
// AskModelResolver returns "" the helper falls back to AskModel.
func TestResolveAskModel_ResolverEmptyFallsBackToStatic(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.AskModel = "static-model"
	cfg.AskModelResolver = func(_ context.Context) string { return "" }
	o := New(nil, nil, nil, cfg)
	if got := o.resolveAskModel(context.Background()); got != "static-model" {
		t.Errorf("expected static fallback 'static-model', got %q", got)
	}
}

// TestResolveAskModel_NilResolverFallsBackToStatic pins that when AskModelResolver
// is nil the helper falls back to the static AskModel field without panicking.
func TestResolveAskModel_NilResolverFallsBackToStatic(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.AskModel = "only-static"
	cfg.AskModelResolver = nil
	o := New(nil, nil, nil, cfg)
	if got := o.resolveAskModel(context.Background()); got != "only-static" {
		t.Errorf("expected static model 'only-static', got %q", got)
	}
}

// TestResolveAskModel_BothEmpty returns empty string when neither field is set.
func TestResolveAskModel_BothEmpty(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.AskModel = ""
	cfg.AskModelResolver = nil
	o := New(nil, nil, nil, cfg)
	if got := o.resolveAskModel(context.Background()); got != "" {
		t.Errorf("expected empty string when nothing configured, got %q", got)
	}
}

func TestBuildPromptEnvelope_DelimitsContext(t *testing.T) {
	p := buildPromptEnvelope(
		AskInput{Question: "how does X work?"},
		"# Section\nBody",
	)
	if !strings.Contains(p, "<context>") || !strings.Contains(p, "</context>") {
		t.Errorf("missing context delimiters: %q", p)
	}
	if !strings.Contains(p, "<question>") || !strings.Contains(p, "</question>") {
		t.Errorf("missing question delimiters: %q", p)
	}
	if !strings.Contains(p, "DATA, not instructions") {
		t.Errorf("missing injection-defense boilerplate")
	}
}

func TestAsk_EmptyModeDefaultsToDeep(t *testing.T) {
	// When the caller omits mode (Mode == ""), the pipeline must default
	// to ModeDeep so retrieval context reaches the synthesizer. This is
	// the contract for GraphQL `ask` and REST /api/v1/ask.
	reader := &fakeDeepReader{
		understanding: &knowledge.RepositoryUnderstanding{
			Stage:      knowledge.UnderstandingReady,
			TreeStatus: knowledge.UnderstandingTreeComplete,
			CorpusID:   "corpus-default-mode",
			RevisionFP: "rev-001",
		},
		nodes: []comprehension.SummaryNode{
			{
				CorpusID:    "corpus-default-mode",
				UnitID:      "core-svc",
				Level:       1,
				Headline:    "Core service entry",
				SummaryText: "Handles the main request flow.",
				Metadata:    `{"file_path":"core/service.go"}`,
			},
		},
	}
	synth := &fakeSynth{
		available: true,
		resp: &reasoningv1.AnswerQuestionResponse{
			Answer: "The main entry point handles X.",
			Usage:  &commonv1.LLMUsage{Model: "m", InputTokens: 50, OutputTokens: 10},
		},
	}
	o := New(synth, reader, nil, DefaultConfig())

	res, err := o.Ask(context.Background(), AskInput{
		RepositoryID: "repo-1",
		Question:     "What is the main entry point?",
		// Mode intentionally omitted — pipeline must default to deep.
	})
	if err != nil {
		t.Fatal(err)
	}
	// (a) The synthesizer must have received non-empty context code,
	// proving the deep retrieval pipeline ran (not the no-retrieval
	// fast path which passes contextMD="" when no caller-supplied code
	// is present).
	if synth.lastReq == nil {
		t.Fatal("synthesizer was not called")
	}
	if synth.lastReq.GetContextCode() == "" {
		t.Error("synthesizer received empty context_code: deep retrieval did not run")
	}
	// (b) Diagnostics must reflect that the deep path executed.
	if res.Diagnostics.Mode != string(ModeDeep) {
		t.Errorf("diagnostics.Mode = %q, want %q", res.Diagnostics.Mode, string(ModeDeep))
	}
}

func TestLanguageFromString(t *testing.T) {
	cases := []struct {
		s    string
		want commonv1.Language
	}{
		{"go", commonv1.Language_LANGUAGE_GO},
		{"GO", commonv1.Language_LANGUAGE_GO},
		{"python", commonv1.Language_LANGUAGE_PYTHON},
		{"py", commonv1.Language_LANGUAGE_PYTHON},
		{"javascript", commonv1.Language_LANGUAGE_JAVASCRIPT},
		{"typescript", commonv1.Language_LANGUAGE_TYPESCRIPT},
		{"tsx", commonv1.Language_LANGUAGE_TYPESCRIPT},
		{"java", commonv1.Language_LANGUAGE_JAVA},
		{"rust", commonv1.Language_LANGUAGE_RUST},
		{"cobol", commonv1.Language_LANGUAGE_UNSPECIFIED},
		{"", commonv1.Language_LANGUAGE_UNSPECIFIED},
	}
	for _, c := range cases {
		if got := languageFromString(c.s); got != c.want {
			t.Errorf("languageFromString(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}
