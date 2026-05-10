// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"
	"errors"
	"strings"
	"testing"

	commonv1 "github.com/sourcebridge/sourcebridge/gen/go/common/v1"
	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
)

// fakeDeepReader implements UnderstandingReader with adjustable responses.
type fakeDeepReader struct {
	understanding *knowledge.RepositoryUnderstanding
	nodes         []comprehension.SummaryNode
	nodesErr      error
}

func (f *fakeDeepReader) GetRepositoryUnderstanding(_ context.Context, repoID string, scope knowledge.ArtifactScope) *knowledge.RepositoryUnderstanding {
	return f.understanding
}
func (f *fakeDeepReader) GetSummaryNodes(_ context.Context, corpusID string) ([]comprehension.SummaryNode, error) {
	return f.nodes, f.nodesErr
}

func TestDeep_UnderstandingNotReadyReturnsCTA(t *testing.T) {
	reader := &fakeDeepReader{
		understanding: &knowledge.RepositoryUnderstanding{
			Stage:      knowledge.UnderstandingBuildingTree,
			TreeStatus: knowledge.UnderstandingTreeMissing,
		},
	}
	o := New(&fakeSynth{available: true}, reader, nil, DefaultConfig())
	res, err := o.Ask(context.Background(), AskInput{
		RepositoryID: "repo-1", Question: "What is the architecture?", Mode: ModeDeep,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Diagnostics.FallbackUsed != "understanding_not_ready" {
		t.Errorf("expected understanding_not_ready, got %q", res.Diagnostics.FallbackUsed)
	}
	if len(res.References) == 0 {
		t.Fatal("expected CTA reference")
	}
	ref := res.References[0]
	if ref.UnderstandingSection == nil || ref.UnderstandingSection.Kind != "action_cta" {
		t.Errorf("expected action_cta reference, got %+v", ref)
	}
	if res.Answer == "" {
		t.Error("expected plain-English answer alongside CTA")
	}
}

func TestDeep_ReadyRoutesThroughSynthesis(t *testing.T) {
	reader := &fakeDeepReader{
		understanding: &knowledge.RepositoryUnderstanding{
			Stage:      knowledge.UnderstandingReady,
			TreeStatus: knowledge.UnderstandingTreeComplete,
			CorpusID:   "corpus-1",
			RevisionFP: "rev-abc",
		},
		nodes: []comprehension.SummaryNode{
			{
				CorpusID: "corpus-1", UnitID: "auth-svc", Level: 1,
				Headline: "Auth service", SummaryText: "Handles login and session tokens.",
				Metadata: `{"file_path":"auth/session.go"}`,
			},
		},
	}
	synth := &fakeSynth{
		available: true,
		resp: &reasoningv1.AnswerQuestionResponse{
			Answer: "Authentication orchestrates magic links and session tokens.",
			Usage:  &commonv1.LLMUsage{Model: "m", InputTokens: 120, OutputTokens: 30},
		},
	}
	o := New(synth, reader, nil, DefaultConfig())

	res, err := o.Ask(context.Background(), AskInput{
		RepositoryID: "repo-1",
		Question:     "how does auth work?",
		Mode:         ModeDeep,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer == "" {
		t.Fatal("expected answer populated")
	}
	if res.Diagnostics.UnderstandingStage != "ready" {
		t.Errorf("expected understandingStage=ready, got %q", res.Diagnostics.UnderstandingStage)
	}
	if !res.Diagnostics.UnderstandingUsed {
		t.Error("expected understandingUsed=true")
	}
	if len(res.References) == 0 {
		t.Fatal("expected understanding_section reference")
	}
	found := false
	for _, r := range res.References {
		if r.Kind == RefKindUnderstandingSection && r.UnderstandingSection.Headline == "Auth service" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing understanding_section for Auth service: %+v", res.References)
	}
	if len(res.Diagnostics.FilesConsidered) == 0 {
		t.Errorf("expected filesConsidered populated from summary metadata")
	}
	if !strings.Contains(synth.lastReq.GetContextCode(), "Auth service") {
		t.Errorf("context markdown missing summary headline: %q", synth.lastReq.GetContextCode())
	}
}

func TestDeep_WorkerUnavailableStillReturnsReferences(t *testing.T) {
	reader := &fakeDeepReader{
		understanding: &knowledge.RepositoryUnderstanding{
			Stage:      knowledge.UnderstandingReady,
			TreeStatus: knowledge.UnderstandingTreeComplete,
			CorpusID:   "corpus-1",
		},
		nodes: []comprehension.SummaryNode{
			{CorpusID: "corpus-1", UnitID: "u", Level: 1,
				Headline: "Service", SummaryText: "Good data here to route around.",
				Metadata: `{"file_path":"svc.go"}`},
		},
	}
	o := New(&fakeSynth{available: false}, reader, nil, DefaultConfig())
	res, err := o.Ask(context.Background(), AskInput{
		RepositoryID: "r", Question: "explain the service", Mode: ModeDeep,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Diagnostics.FallbackUsed != "worker_unavailable" {
		t.Errorf("expected worker_unavailable, got %q", res.Diagnostics.FallbackUsed)
	}
	if len(res.References) == 0 {
		t.Errorf("expected references preserved even on worker_unavailable: %+v", res)
	}
}

func TestDeep_SynthesisErrorPreservesEvidence(t *testing.T) {
	reader := &fakeDeepReader{
		understanding: &knowledge.RepositoryUnderstanding{
			Stage: knowledge.UnderstandingReady, TreeStatus: knowledge.UnderstandingTreeComplete,
			CorpusID: "c",
		},
		nodes: []comprehension.SummaryNode{
			{CorpusID: "c", UnitID: "u", Level: 1, Headline: "X", SummaryText: "stuff", Metadata: `{}`},
		},
	}
	synth := &fakeSynth{available: true, err: errors.New("boom")}
	o := New(synth, reader, nil, DefaultConfig())
	res, err := o.Ask(context.Background(), AskInput{
		RepositoryID: "r", Question: "tell me", Mode: ModeDeep,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Diagnostics.FallbackUsed != "synthesis_failed" {
		t.Errorf("expected synthesis_failed, got %q", res.Diagnostics.FallbackUsed)
	}
	if !strings.Contains(res.Answer, "boom") {
		t.Errorf("expected error surfaced in answer, got %q", res.Answer)
	}
	// Evidence references survived.
	if len(res.References) == 0 {
		t.Error("expected references preserved after synthesis failure")
	}
}

func TestDeep_DegradesWhenNoReader(t *testing.T) {
	synth := &fakeSynth{
		available: true,
		resp:      &reasoningv1.AnswerQuestionResponse{Answer: "generic"},
	}
	o := New(synth, nil, nil, DefaultConfig())
	res, err := o.Ask(context.Background(), AskInput{
		RepositoryID: "r", Question: "something", Mode: ModeDeep,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Diagnostics.FallbackUsed == "understanding_not_ready" {
		t.Error("no reader should not trigger CTA; it should degrade to fast-like synthesis")
	}
	if res.Answer != "generic" {
		t.Errorf("expected synthesis still to run; got %q", res.Answer)
	}
}

func TestBuildContextMarkdown_IncludesSummaries(t *testing.T) {
	sums := []SummaryEvidence{
		{UnitID: "u1", Headline: "First", SummaryText: "Some summary text."},
		{UnitID: "u2", Headline: "", SummaryText: "Another."},
	}
	md := buildContextMarkdown(AskInput{}, sums, nil)
	if !strings.Contains(md, "# Understanding summaries") {
		t.Error("missing summaries section header")
	}
	if !strings.Contains(md, "## First") {
		t.Error("missing headline heading")
	}
	if !strings.Contains(md, "## u2") {
		t.Error("expected unit_id fallback when headline missing")
	}
	if !strings.Contains(md, "ca_summary_node/u1") {
		t.Error("missing provenance pointer")
	}
}
