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

func (f *fakeArtifactLookup) ArtifactContext(_ context.Context, id string) string { return f.block }

type fakeRequirementLookup struct {
	byID    map[string]string
	bySymID map[string][]string
}

func (f *fakeRequirementLookup) RequirementContext(_ context.Context, id string) string { return f.byID[id] }
func (f *fakeRequirementLookup) RequirementLabelsForSymbols(_ context.Context, ids []string) []string {
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
	details     map[string]SymbolDetail
}

func (f *fakeSymbolLookup) SymbolContext(ctx context.Context, id string) string {
	return f.byID[id]
}
func (f *fakeSymbolLookup) SymbolFilePath(ctx context.Context, id string) string {
	return f.filePathsBy[id]
}
func (f *fakeSymbolLookup) SymbolsInFile(ctx context.Context, repoID, filePath string) []SymbolContextRef {
	return f.inFile[filePath]
}
func (f *fakeSymbolLookup) SymbolDetails(ctx context.Context, id string) (SymbolDetail, bool) {
	if f.details == nil {
		return SymbolDetail{}, false
	}
	d, found := f.details[id]
	if !found {
		return SymbolDetail{}, false
	}
	// Mirror the real adapter's validity gate: ok=true only when
	// FilePath, StartLine, and EndLine are all usable for slicing.
	if d.FilePath == "" || d.StartLine <= 0 || d.EndLine < d.StartLine {
		return d, false
	}
	return d, true
}

type fakeFileReader struct{ files map[string]string }

func (f *fakeFileReader) ReadRepoFile(ctx context.Context, repoID, filePath string) (string, error) {
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
				"auth/handler.go": {
					{
						ID:            "sym-auth",
						Name:          "Handle",
						QualifiedName: "auth.Handle",
						FilePath:      "auth/handler.go",
						StartLine:     2,
						EndLine:       2,
						Signature:     "func Handle()",
					},
					{
						ID:            "sym-helper",
						Name:          "helper",
						QualifiedName: "auth.helper",
						FilePath:      "auth/handler.go",
						StartLine:     4,
						EndLine:       4,
						Signature:     "func helper() bool",
					},
				},
			},
			details: map[string]SymbolDetail{
				"sym-auth": {
					ID:            "sym-auth",
					Name:          "Handle",
					QualifiedName: "auth.Handle",
					FilePath:      "auth/handler.go",
					StartLine:     2,
					EndLine:       2,
					Signature:     "func Handle()",
				},
				"sym-helper": {
					ID:            "sym-helper",
					Name:          "helper",
					QualifiedName: "auth.helper",
					FilePath:      "auth/handler.go",
					StartLine:     4,
					EndLine:       4,
					Signature:     "func helper() bool",
				},
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
	// Context includes artifact, requirement, and symbol blocks.
	// Fakes return simplified payloads; assertions check the strings
	// they actually emit (real adapters add more envelope text).
	prompt := synth.lastReq.GetContextCode()
	for _, want := range []string{
		"ARTIFACT CONTEXT HERE",
		"REQ-42 description",
		"Indexed symbol: auth.Handle",
		// Decision 1: range-pin succeeded so the labeled block is present.
		"## auth/handler.go:2-2",
		"func Handle() {}",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("context missing %q\ncontext:\n%s", want, prompt)
		}
	}
	// Decision 1: whole-file dump is suppressed when range-pin succeeds.
	// "package auth" only appears in line 1 of the fixture file; it must
	// not appear in the prompt when the range-pin slices only line 2.
	if strings.Contains(prompt, "package auth") {
		t.Errorf("whole-file dump not suppressed; found \"package auth\" in context:\n%s", prompt)
	}
	if strings.Count(prompt, "package auth\nfunc Handle() {}") != 0 {
		t.Errorf("whole-file string appeared in context (should be suppressed)")
	}
	// The sliced source line appears exactly once (not duplicated by a
	// whole-file dump or a second pin).
	if strings.Count(prompt, "func Handle() {}") != 1 {
		t.Errorf("expected \"func Handle() {}\" exactly once, got %d",
			strings.Count(prompt, "func Handle() {}"))
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
	// Context symbols: pinned sym-auth + non-pinned peer sym-helper (dedup
	// removes the duplicate SymbolsInFile entry for sym-auth).
	syms := synth.lastReq.GetContextSymbols()
	if len(syms) != 2 {
		t.Fatalf("expected 2 context_symbols (pinned + peer), got %d: %+v", len(syms), syms)
	}
	// Pinned ref is index 0 (Stage 3d appends before SymbolsInFile loop).
	pinned := syms[0]
	if pinned.GetId() != "sym-auth" {
		t.Errorf("syms[0].id = %q, want \"sym-auth\"", pinned.GetId())
	}
	if pinned.GetName() != "Handle" {
		t.Errorf("syms[0].name = %q, want \"Handle\"", pinned.GetName())
	}
	if pinned.GetQualifiedName() != "auth.Handle" {
		t.Errorf("syms[0].qualified_name = %q, want \"auth.Handle\"", pinned.GetQualifiedName())
	}
	// Phase 3: Signature and Location wired into proto.
	if pinned.GetSignature() != "func Handle()" {
		t.Errorf("syms[0].signature = %q, want \"func Handle()\"", pinned.GetSignature())
	}
	if pinned.GetLocation() == nil {
		t.Fatal("syms[0].location is nil; expected populated Location")
	}
	if pinned.GetLocation().GetPath() != "auth/handler.go" {
		t.Errorf("syms[0].location.path = %q, want \"auth/handler.go\"", pinned.GetLocation().GetPath())
	}
	if pinned.GetLocation().GetStartLine() != 2 {
		t.Errorf("syms[0].location.start_line = %d, want 2", pinned.GetLocation().GetStartLine())
	}
	if pinned.GetLocation().GetEndLine() != 2 {
		t.Errorf("syms[0].location.end_line = %d, want 2", pinned.GetLocation().GetEndLine())
	}
	// Non-pinned peer: sym-helper carries Signature from Phase 2 SymbolsInFile enrichment.
	var helperSym *commonv1.CodeSymbol
	for _, s := range syms {
		if s.GetId() == "sym-helper" {
			helperSym = s
			break
		}
	}
	if helperSym == nil {
		t.Fatal("sym-helper not found in context_symbols")
	}
	if helperSym.GetSignature() != "func helper() bool" {
		t.Errorf("sym-helper.signature = %q, want \"func helper() bool\"", helperSym.GetSignature())
	}
}

// TestOrchestrator_DiscussSymbolFallbackWhenNoLineRange exercises the known-but-
// unsliceable path: SymbolDetails returns (SymbolDetail{Name, QualifiedName}, false)
// because line-range fields are zero. Stage 3d must fall back to the legacy whole-file
// path and still populate context_symbols identity fields.
func TestOrchestrator_DiscussSymbolFallbackWhenNoLineRange(t *testing.T) {
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
	// details map has an entry with identity but zero line-range — the fake
	// returns (detail, false), exercising the known-but-unsliceable contract.
	o := New(synth, reader, nil, DefaultConfig()).
		WithSymbolLookup(&fakeSymbolLookup{
			byID:        map[string]string{"sym-auth": "Indexed symbol: auth.Handle"},
			filePathsBy: map[string]string{"sym-auth": "auth/handler.go"},
			inFile:      map[string][]SymbolContextRef{},
			details: map[string]SymbolDetail{
				// Identity populated, line-range zero — ok=false from fake.
				"sym-auth": {
					Name:          "Handle",
					QualifiedName: "auth.Handle",
					// FilePath, StartLine, EndLine intentionally zero so
					// fakeSymbolLookup.SymbolDetails returns (detail, false).
					// The fake returns ok=true only when the key exists AND
					// the struct has a non-empty FilePath + positive line range.
					// Since FilePath is "", ok=false is forced.
				},
			},
		}).
		WithFileReader(&fakeFileReader{files: map[string]string{"auth/handler.go": "package auth\nfunc Handle() {}"}})

	in := AskInput{
		RepositoryID: "repo-1",
		Question:     "how does auth work?",
		Mode:         ModeDeep,
		SymbolID:     "sym-auth",
	}
	res, err := o.Ask(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer == "" {
		t.Fatal("expected answer")
	}
	prompt := synth.lastReq.GetContextCode()

	// Fallback fires: whole-file content appears in the prompt.
	if !strings.Contains(prompt, "package auth\nfunc Handle() {}") {
		t.Errorf("expected whole-file fallback content in context:\n%s", prompt)
	}
	// No labeled block emitted (range-pin did not succeed).
	if strings.Contains(prompt, "## auth/handler.go:") {
		t.Errorf("labeled block should not appear when range-pin fails:\n%s", prompt)
	}

	// Context symbol identity is still populated even on ok=false.
	syms := synth.lastReq.GetContextSymbols()
	if len(syms) == 0 {
		t.Fatal("expected at least one context_symbol")
	}
	pinned := syms[0]
	if pinned.GetName() != "Handle" {
		t.Errorf("syms[0].name = %q, want \"Handle\"", pinned.GetName())
	}
	if pinned.GetQualifiedName() != "auth.Handle" {
		t.Errorf("syms[0].qualified_name = %q, want \"auth.Handle\"", pinned.GetQualifiedName())
	}
	// Signature absent because range-pin failed (ok=false path skips it).
	if pinned.GetSignature() != "" {
		t.Errorf("syms[0].signature = %q, want \"\" (no signature on fallback path)", pinned.GetSignature())
	}
	// Location nil when range-pin failed.
	if pinned.GetLocation() != nil {
		t.Errorf("syms[0].location should be nil when range-pin fails, got %+v", pinned.GetLocation())
	}
}

// TestOrchestrator_DiscussAuthoritativeFilePath verifies that when a caller
// passes both SymbolID and a stale/wrong FilePath, the pipeline overwrites
// in.FilePath with detail.FilePath once the range-pin succeeds. The worker
// request, the labeled source block, and the SymbolsInFile peer lookup must
// all use the symbol-store's authoritative path, not the caller-supplied one.
func TestOrchestrator_DiscussAuthoritativeFilePath(t *testing.T) {
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
			Answer: "Auth answer.",
			Usage:  &commonv1.LLMUsage{Model: "claude-sonnet", InputTokens: 10, OutputTokens: 5},
		},
	}
	o := New(synth, reader, nil, DefaultConfig()).
		WithSymbolLookup(&fakeSymbolLookup{
			byID:        map[string]string{"sym-auth": "Indexed symbol: auth.Handle"},
			filePathsBy: map[string]string{"sym-auth": "auth/handler.go"},
			inFile: map[string][]SymbolContextRef{
				"auth/handler.go": {
					{
						ID:            "sym-auth",
						Name:          "Handle",
						QualifiedName: "auth.Handle",
						FilePath:      "auth/handler.go",
						StartLine:     1,
						EndLine:       1,
						Signature:     "func Handle()",
					},
					{
						ID:            "sym-peer",
						Name:          "peer",
						QualifiedName: "auth.peer",
						FilePath:      "auth/handler.go",
						StartLine:     2,
						EndLine:       2,
						Signature:     "func peer()",
					},
				},
				// No entry for "wrong.go" — peer lookup on wrong path yields nothing.
			},
			details: map[string]SymbolDetail{
				"sym-auth": {
					ID:            "sym-auth",
					Name:          "Handle",
					QualifiedName: "auth.Handle",
					FilePath:      "auth/handler.go",
					StartLine:     1,
					EndLine:       1,
					Signature:     "func Handle()",
				},
			},
		}).
		WithFileReader(&fakeFileReader{files: map[string]string{
			"auth/handler.go": "func Handle() {}",
		}})

	// Caller passes SymbolID with a stale/wrong FilePath.
	in := AskInput{
		RepositoryID: "repo-1",
		Question:     "how does auth work?",
		Mode:         ModeDeep,
		SymbolID:     "sym-auth",
		FilePath:     "wrong.go",
	}
	res, err := o.Ask(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer == "" {
		t.Fatal("expected answer")
	}
	prompt := synth.lastReq.GetContextCode()

	// Labeled block must use the authoritative path, not "wrong.go".
	if !strings.Contains(prompt, "## auth/handler.go:") {
		t.Errorf("expected labeled block with auth/handler.go, not found in context:\n%s", prompt)
	}
	if strings.Contains(prompt, "## wrong.go:") {
		t.Errorf("stale path wrong.go must not appear in labeled block:\n%s", prompt)
	}

	// Worker request FilePath must be the authoritative path.
	if got := synth.lastReq.GetFilePath(); got != "auth/handler.go" {
		t.Errorf("GetFilePath() = %q, want %q", got, "auth/handler.go")
	}

	// SymbolsInFile peer dedup used auth/handler.go (sym-peer appears) not wrong.go.
	syms := synth.lastReq.GetContextSymbols()
	foundPeer := false
	for _, s := range syms {
		if s.GetId() == "sym-peer" {
			foundPeer = true
		}
	}
	if !foundPeer {
		t.Errorf("sym-peer should appear via SymbolsInFile(auth/handler.go); got symbols: %+v", syms)
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
