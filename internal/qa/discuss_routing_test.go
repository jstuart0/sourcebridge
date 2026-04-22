// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"
	"strings"
	"testing"

	commonv1 "github.com/sourcebridge/sourcebridge/gen/go/common/v1"
	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
)

// Ensure the discussCode-equivalent inputs produce a well-formed
// AskResult with references + related requirements populated.
// This is the Ledger F10/F11 preservation test at the orchestrator
// level (the GraphQL adapter is tested separately).

type fakeArtifactLookup struct{ block string }

func (f *fakeArtifactLookup) ArtifactContext(id string) string { return f.block }

type fakeRequirementLookup struct {
	byID    map[string]string
	bySymID map[string][]string
}

func (f *fakeRequirementLookup) RequirementContext(id string) string { return f.byID[id] }
func (f *fakeRequirementLookup) RequirementLabelsForSymbols(ids []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, id := range ids {
		for _, label := range f.bySymID[id] {
			if seen[label] {
				continue
			}
			seen[label] = true
			out = append(out, label)
		}
	}
	return out
}

type fakeSymbolLookup struct {
	byID        map[string]string
	filePathsBy map[string]string
	inFile      map[string][]SymbolContextRef
}

func (f *fakeSymbolLookup) SymbolContext(id string) string  { return f.byID[id] }
func (f *fakeSymbolLookup) SymbolFilePath(id string) string { return f.filePathsBy[id] }
func (f *fakeSymbolLookup) SymbolsInFile(repoID, filePath string) []SymbolContextRef {
	return f.inFile[filePath]
}

type fakeFileReader struct{ files map[string]string }

func (f *fakeFileReader) ReadRepoFile(repoID, filePath string) (string, error) {
	if s, ok := f.files[filePath]; ok {
		return s, nil
	}
	return "", nil
}

func TestOrchestrator_DiscussShapePreserved(t *testing.T) {
	reader := &fakeDeepReader{
		understanding: &knowledge.RepositoryUnderstanding{
			Stage: knowledge.UnderstandingReady, TreeStatus: knowledge.UnderstandingTreeComplete,
			CorpusID: "corpus",
		},
		nodes: []comprehension.SummaryNode{},
	}
	synth := &fakeSynth{
		available: true,
		resp: &reasoningv1.AnswerQuestionResponse{
			Answer: "Authentication uses magic links.",
			Usage:  &commonv1.LLMUsage{Model: "claude-sonnet", InputTokens: 100, OutputTokens: 20},
		},
	}
	o := New(synth, reader, nil, DefaultConfig()).
		WithArtifactLookup(&fakeArtifactLookup{block: "ARTIFACT CONTEXT HERE"}).
		WithRequirementLookup(&fakeRequirementLookup{
			byID:    map[string]string{"REQ-42": "REQ-42 description"},
			bySymID: map[string][]string{"sym-auth": {"REQ-42"}},
		}).
		WithSymbolLookup(&fakeSymbolLookup{
			byID:        map[string]string{"sym-auth": "Indexed symbol: auth.Handle"},
			filePathsBy: map[string]string{"sym-auth": "auth/handler.go"},
			inFile: map[string][]SymbolContextRef{
				"auth/handler.go": {{ID: "sym-auth", Name: "Handle", QualifiedName: "auth.Handle"}},
			},
		}).
		WithFileReader(&fakeFileReader{files: map[string]string{"auth/handler.go": "package auth\nfunc Handle() {}"}})

	in := AskInput{
		RepositoryID:  "repo-1",
		Question:      "how does auth work?",
		Mode:          ModeDeep,
		ArtifactID:    "art-1",
		RequirementID: "REQ-42",
		SymbolID:      "sym-auth",
		PriorMessages: []string{"earlier turn about sessions"},
	}
	res, err := o.Ask(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer == "" {
		t.Fatal("expected answer")
	}
	// Context includes artifact, requirement, symbol, and file blocks.
	// Fakes return simplified payloads; assertions check the strings
	// they actually emit (real adapters add more envelope text).
	prompt := synth.lastReq.GetContextCode()
	for _, want := range []string{
		"ARTIFACT CONTEXT HERE",
		"REQ-42 description",
		"Indexed symbol: auth.Handle",
		"package auth",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("context missing %q\ncontext:\n%s", want, prompt)
		}
	}
	// Conversation history made it into the prompt envelope.
	if !strings.Contains(synth.lastReq.GetQuestion(), "earlier turn about sessions") {
		t.Errorf("conversation history not threaded into question")
	}
	// Related requirements: populated from linked symbols.
	if len(res.RelatedRequirements) == 0 || res.RelatedRequirements[0] != "REQ-42" {
		t.Errorf("related requirements not populated: %+v", res.RelatedRequirements)
	}
	// Usage flattened onto the result.
	if res.Usage.Model != "claude-sonnet" || res.Usage.InputTokens != 100 {
		t.Errorf("usage not propagated: %+v", res.Usage)
	}
	// Context symbols sent to the worker.
	if len(synth.lastReq.GetContextSymbols()) == 0 {
		t.Errorf("expected context_symbols on synthesis request")
	}
}

func TestOrchestrator_DiscussFlattensToLegacyShape(t *testing.T) {
	// This tests the helper used by the GraphQL adapter — verifies
	// that the 5 reference variants flatten to strings that mirror
	// what the legacy DiscussCode resolver produced.
	refs := []AskReference{
		{Kind: RefKindSymbol, Symbol: &SymbolRef{QualifiedName: "pkg.Foo"}},
		{Kind: RefKindFileRange, FileRange: &FileRangeRef{FilePath: "a.go"}},
		{Kind: RefKindRequirement, Requirement: &RequirementRef{ExternalID: "REQ-1"}},
		{Kind: RefKindUnderstandingSection, UnderstandingSection: &UnderstandingSectionRef{Headline: "Auth"}},
	}
	got := FlattenReferencesToStrings(refs)
	want := []string{"pkg.Foo", "a.go", "REQ-1", "Auth"}
	if len(got) != len(want) {
		t.Fatalf("count mismatch: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q want %q", i, got[i], want[i])
		}
	}
}
