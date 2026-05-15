// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"reflect"
	"sort"
	"testing"

	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
)

// ── mergeUniqueStrings ───────────────────────────────────────────────────────

func TestMergeUniqueStrings_Disjoint(t *testing.T) {
	got := mergeUniqueStrings([]string{"a", "b"}, []string{"c", "d"})
	want := []string{"a", "b", "c", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMergeUniqueStrings_Overlap(t *testing.T) {
	got := mergeUniqueStrings([]string{"a", "b", "c"}, []string{"b", "c", "d"})
	want := []string{"a", "b", "c", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMergeUniqueStrings_EmptyA(t *testing.T) {
	got := mergeUniqueStrings(nil, []string{"x", "y"})
	want := []string{"x", "y"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMergeUniqueStrings_EmptyB(t *testing.T) {
	got := mergeUniqueStrings([]string{"x"}, nil)
	want := []string{"x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMergeUniqueStrings_BothEmpty(t *testing.T) {
	got := mergeUniqueStrings(nil, nil)
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestMergeUniqueStrings_IsSorted(t *testing.T) {
	got := mergeUniqueStrings([]string{"z", "m"}, []string{"a", "f"})
	if !sort.StringsAreSorted(got) {
		t.Errorf("result not sorted: %v", got)
	}
}

// ── explainEvidenceToRequirementIDs ─────────────────────────────────────────

func TestExplainEvidenceToRequirementIDs_Dedup(t *testing.T) {
	evidence := []*knowledgev1.KnowledgeEvidence{
		{SourceType: "requirement", SourceId: "REQ-1"},
		{SourceType: "file", SourceId: "main.go"},
		{SourceType: "requirement", SourceId: "REQ-1"}, // duplicate
		{SourceType: "requirement", SourceId: "REQ-2"},
	}
	got := explainEvidenceToRequirementIDs(evidence)
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d: %v", len(got), got)
	}
	if got[0] != "REQ-1" || got[1] != "REQ-2" {
		t.Errorf("unexpected ids: %v", got)
	}
}

func TestExplainEvidenceToRequirementIDs_Empty(t *testing.T) {
	got := explainEvidenceToRequirementIDs(nil)
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestExplainEvidenceToRequirementIDs_NoRequirements(t *testing.T) {
	evidence := []*knowledgev1.KnowledgeEvidence{
		{SourceType: "symbol", SourceId: "sym-1"},
		{SourceType: "file", SourceId: "foo.go"},
	}
	got := explainEvidenceToRequirementIDs(evidence)
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

// ── explainEvidenceToReferences ─────────────────────────────────────────────

func TestExplainEvidenceToReferences_SymbolRef(t *testing.T) {
	evidence := []*knowledgev1.KnowledgeEvidence{
		{SourceType: "symbol", SourceId: "sym-abc", FilePath: "pkg/foo.go", LineStart: 10, LineEnd: 20},
	}
	refs := explainEvidenceToReferences(nil, context.Background(), evidence)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	r := refs[0]
	if r.Kind != "symbol" {
		t.Errorf("kind: got %q", r.Kind)
	}
	if r.Symbol == nil {
		t.Fatal("expected Symbol sub-ref")
	}
	if r.Symbol.SymbolID != "sym-abc" {
		t.Errorf("SymbolID: got %q", r.Symbol.SymbolID)
	}
	if r.Symbol.FilePath == nil || *r.Symbol.FilePath != "pkg/foo.go" {
		t.Errorf("FilePath: got %v", r.Symbol.FilePath)
	}
	if r.Symbol.StartLine == nil || *r.Symbol.StartLine != 10 {
		t.Errorf("StartLine: got %v", r.Symbol.StartLine)
	}
}

func TestExplainEvidenceToReferences_FileRef(t *testing.T) {
	evidence := []*knowledgev1.KnowledgeEvidence{
		{SourceType: "file", SourceId: "", FilePath: "internal/auth/auth.go", LineStart: 1, LineEnd: 50},
	}
	refs := explainEvidenceToReferences(nil, context.Background(), evidence)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	r := refs[0]
	if r.Kind != "file_range" {
		t.Errorf("kind: got %q", r.Kind)
	}
	if r.FileRange == nil {
		t.Fatal("expected FileRange sub-ref")
	}
	if r.FileRange.FilePath != "internal/auth/auth.go" {
		t.Errorf("FilePath: got %q", r.FileRange.FilePath)
	}
}

func TestExplainEvidenceToReferences_DocRef(t *testing.T) {
	evidence := []*knowledgev1.KnowledgeEvidence{
		{SourceType: "doc", SourceId: "", FilePath: "docs/README.md"},
	}
	refs := explainEvidenceToReferences(nil, context.Background(), evidence)
	if len(refs) != 1 || refs[0].Kind != "file_range" {
		t.Fatalf("expected 1 file_range ref, got %v", refs)
	}
}

func TestExplainEvidenceToReferences_RequirementRef(t *testing.T) {
	evidence := []*knowledgev1.KnowledgeEvidence{
		{SourceType: "requirement", SourceId: "AUTH-001"},
	}
	refs := explainEvidenceToReferences(nil, context.Background(), evidence)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	r := refs[0]
	if r.Kind != "requirement" {
		t.Errorf("kind: got %q", r.Kind)
	}
	if r.Requirement == nil || r.Requirement.ExternalID != "AUTH-001" {
		t.Errorf("requirement externalId: got %+v", r.Requirement)
	}
}

func TestExplainEvidenceToReferences_SkipsEmptyFilePath(t *testing.T) {
	evidence := []*knowledgev1.KnowledgeEvidence{
		{SourceType: "file", FilePath: ""}, // no file path → skip
	}
	refs := explainEvidenceToReferences(nil, context.Background(), evidence)
	if len(refs) != 0 {
		t.Errorf("expected 0 refs, got %d", len(refs))
	}
}

func TestExplainEvidenceToReferences_NilEvidence(t *testing.T) {
	refs := explainEvidenceToReferences(nil, context.Background(), nil)
	if len(refs) != 0 {
		t.Errorf("expected 0 refs, got %d", len(refs))
	}
}
