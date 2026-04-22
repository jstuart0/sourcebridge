// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestAskInputJSONRoundtrip(t *testing.T) {
	in := AskInput{
		RepositoryID:   "repo-123",
		Question:       "How does auth work?",
		Mode:           ModeDeep,
		ConversationID: "conv-1",
		PriorMessages:  []string{"first turn", "second turn"},
		FilePath:       "auth/session.go",
		Code:           "func Foo() {}",
		Language:       "go",
		ArtifactID:     "art-1",
		SymbolID:       "sym-1",
		RequirementID:  "REQ-1",
		IncludeDebug:   true,
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out AskInput
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("roundtrip mismatch:\nin=%#v\nout=%#v", in, out)
	}
}

func TestAskResultJSONRoundtrip(t *testing.T) {
	res := AskResult{
		Answer: "The auth flow starts with...",
		References: []AskReference{
			{
				Kind:   RefKindSymbol,
				Symbol: &SymbolRef{SymbolID: "s1", QualifiedName: "pkg.Foo", FilePath: "a.go", StartLine: 10, EndLine: 20, Language: "go"},
				Title:  "pkg.Foo",
			},
			{
				Kind:      RefKindFileRange,
				FileRange: &FileRangeRef{FilePath: "b.go", StartLine: 1, EndLine: 3, Snippet: "x"},
				Title:     "b.go:1-3",
			},
			{
				Kind:        RefKindRequirement,
				Requirement: &RequirementRef{ExternalID: "REQ-42", Title: "Auth", FilePath: "README.md"},
				Title:       "REQ-42",
			},
			{
				Kind:                 RefKindUnderstandingSection,
				UnderstandingSection: &UnderstandingSectionRef{ArtifactID: "a1", SectionID: "s1", Headline: "Overview", Kind: "section"},
				Title:                "Overview",
			},
			{
				Kind:      RefKindCrossRepoRef,
				CrossRepo: &CrossRepoRef{RepositoryID: "repo-2", Note: "see also"},
				Title:     "repo-2",
			},
		},
		RelatedRequirements: []string{"REQ-42"},
		Diagnostics: AskDiagnostics{
			QuestionType:          "architecture",
			UnderstandingStage:    "ready",
			TreeStatus:            "complete",
			UnderstandingRevision: "rev-1",
			UnderstandingUsed:     true,
			GraphExpansionUsed:    true,
			FilesConsidered:       []string{"a.go", "b.go"},
			FilesUsed:             []string{"a.go"},
			FallbackUsed:          "",
			ModelUsed:             "claude-sonnet-4-6",
			StageTimings: map[string]DurationMs{
				"qa.classify": 12,
				"qa.retrieve": 340,
				"qa.llm_call": 1200,
			},
			Mode: "deep",
		},
		Usage: AskUsage{Model: "claude-sonnet-4-6", InputTokens: 3200, OutputTokens: 480},
		Debug: &AskDebug{
			Prompt:          "<context>...</context>",
			ContextMarkdown: "# Summaries\n...",
			Candidates: []DebugCandidate{
				{Source: "summary", ID: "sec-1", Score: 0.87, Reason: "token match"},
			},
		},
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out AskResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(res, out) {
		t.Fatalf("roundtrip mismatch:\nin=%#v\nout=%#v", res, out)
	}
}

func TestFromDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want DurationMs
	}{
		{0, 0},
		{time.Millisecond, 1},
		{500 * time.Microsecond, 0}, // truncates
		{2500 * time.Millisecond, 2500},
	}
	for _, c := range cases {
		got := FromDuration(c.in)
		if got != c.want {
			t.Errorf("FromDuration(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestFlattenReferencesToStrings(t *testing.T) {
	refs := []AskReference{
		{Kind: RefKindSymbol, Symbol: &SymbolRef{QualifiedName: "pkg.Foo"}, Title: ""},
		{Kind: RefKindFileRange, FileRange: &FileRangeRef{FilePath: "a.go"}, Title: ""},
		{Kind: RefKindRequirement, Requirement: &RequirementRef{ExternalID: "REQ-1"}, Title: ""},
		{Kind: RefKindUnderstandingSection, UnderstandingSection: &UnderstandingSectionRef{Headline: "Auth"}, Title: ""},
		{Kind: RefKindCrossRepoRef, CrossRepo: &CrossRepoRef{RepositoryID: "r1"}, Title: ""},
		{Kind: RefKindSymbol, Title: "explicit-title", Symbol: &SymbolRef{QualifiedName: "ignored"}},
	}
	got := FlattenReferencesToStrings(refs)
	want := []string{"pkg.Foo", "a.go", "REQ-1", "Auth", "r1", "explicit-title"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got=%v want=%v", got, want)
	}
}

// TestDiagnosticKeysCanonicalSet ensures the 12 canonical diagnostic
// keys from the plan's §Verified Assumptions round-trip through JSON
// with stable names. If a rename happens this test fails, forcing a
// deliberate contract change.
func TestDiagnosticKeysCanonicalSet(t *testing.T) {
	d := AskDiagnostics{
		QuestionType:          "x",
		UnderstandingStage:    "x",
		TreeStatus:            "x",
		UnderstandingRevision: "x",
		UnderstandingUsed:     true,
		GraphExpansionUsed:    true,
		FilesConsidered:       []string{"x"},
		FilesUsed:             []string{"x"},
		FallbackUsed:          "x",
		ModelUsed:             "x",
		StageTimings:          map[string]DurationMs{"x": 1},
		Mode:                  "x",
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := []string{
		"questionType",
		"understandingStage",
		"treeStatus",
		"understandingRevision",
		"understandingUsed",
		"graphExpansionUsed",
		"filesConsidered",
		"filesUsed",
		"fallbackUsed",
		"modelUsed",
		"stageTimings",
		"mode",
	}
	if len(m) != len(want) {
		t.Errorf("diagnostic key count = %d, want %d (keys=%v)", len(m), len(want), m)
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Errorf("missing canonical diagnostic key: %q", k)
		}
	}
}
