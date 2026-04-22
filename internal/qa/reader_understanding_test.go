// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"reflect"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
)

type fakeReader struct {
	understanding *knowledge.RepositoryUnderstanding
	summaryNodes  []comprehension.SummaryNode
	summaryErr    error
}

func (f *fakeReader) GetRepositoryUnderstanding(repoID string, scope knowledge.ArtifactScope) *knowledge.RepositoryUnderstanding {
	return f.understanding
}
func (f *fakeReader) GetSummaryNodes(corpusID string) ([]comprehension.SummaryNode, error) {
	return f.summaryNodes, f.summaryErr
}

func TestGetRepositoryStatus_NoRow(t *testing.T) {
	r := &fakeReader{}
	st := GetRepositoryStatus(r, "repo-1", "MyRepo")
	if st == nil {
		t.Fatal("expected non-nil status")
	}
	if st.Ready {
		t.Error("expected Ready=false for missing understanding")
	}
	if st.RepositoryID != "repo-1" || st.RepositoryName != "MyRepo" {
		t.Errorf("repo id/name not propagated: %+v", st)
	}
}

func TestGetRepositoryStatus_Ready(t *testing.T) {
	r := &fakeReader{
		understanding: &knowledge.RepositoryUnderstanding{
			ID:           "u-1",
			RepositoryID: "repo-1",
			CorpusID:     "corpus-1",
			RevisionFP:   "rev-1",
			Stage:        knowledge.UnderstandingReady,
			TreeStatus:   knowledge.UnderstandingTreeComplete,
			ModelUsed:    "claude-sonnet-4-6",
		},
	}
	st := GetRepositoryStatus(r, "repo-1", "MyRepo")
	if !st.Ready {
		t.Fatal("expected Ready=true")
	}
	if st.CorpusID != "corpus-1" || st.UnderstandingRevision != "rev-1" {
		t.Errorf("fields not propagated: %+v", st)
	}
}

func TestGetRepositoryStatus_NotReadyWhenPartial(t *testing.T) {
	r := &fakeReader{
		understanding: &knowledge.RepositoryUnderstanding{
			Stage:      knowledge.UnderstandingReady,
			TreeStatus: knowledge.UnderstandingTreePartial,
		},
	}
	st := GetRepositoryStatus(r, "r", "n")
	if st.Ready {
		t.Error("partial tree should not be ready")
	}
}

func TestTokenizeQuestion(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"How does auth work?", []string{"auth", "work"}},
		{"what is the architecture?", []string{"architecture"}},
		{"List REQ-1234 and REQ-AB-9", []string{"list", "req-1234", "req-ab-9"}},
		{"a or an", nil},
	}
	for _, c := range cases {
		got := tokenizeQuestion(c.in)
		if c.want == nil {
			if len(got) != 0 {
				t.Errorf("tokenize(%q) = %v, want empty", c.in, got)
			}
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("tokenize(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestGetSummaryEvidence_SortingAndScoring(t *testing.T) {
	r := &fakeReader{
		summaryNodes: []comprehension.SummaryNode{
			{
				CorpusID: "c", UnitID: "u-auth-service", Level: 1,
				Headline: "Auth service", SummaryText: "Handles login sessions and tokens.",
				Metadata: `{"file_path":"auth/session.go"}`,
			},
			{
				CorpusID: "c", UnitID: "u-readme", Level: 0,
				Headline: "Repo overview", SummaryText: "Project root.",
				Metadata: `{}`,
			},
			{
				CorpusID: "c", UnitID: "u-bad", Level: 1,
				Headline: "", SummaryText: "Could not summarize this unit.",
				Metadata: `{}`,
			},
		},
	}
	ev, err := GetSummaryEvidence(r, "c", "How does the auth service handle sessions?", "execution_flow")
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 2 {
		t.Fatalf("expected 2 useful evidences (bad row filtered), got %d: %+v", len(ev), ev)
	}
	if ev[0].UnitID != "u-auth-service" {
		t.Errorf("expected auth service first (higher score), got %q", ev[0].UnitID)
	}
	if ev[0].FilePath != "auth/session.go" {
		t.Errorf("file_path not propagated: %+v", ev[0])
	}
}

func TestGetSummaryEvidence_EmptyCorpus(t *testing.T) {
	r := &fakeReader{}
	ev, err := GetSummaryEvidence(r, "", "anything", "behavior")
	if err != nil {
		t.Fatal(err)
	}
	if ev != nil {
		t.Errorf("expected nil for empty corpus, got %v", ev)
	}
}

func TestGetSummaryEvidence_ArchitectureBoost(t *testing.T) {
	r := &fakeReader{
		summaryNodes: []comprehension.SummaryNode{
			{CorpusID: "c", UnitID: "module-level", Level: 2,
				Headline: "Module architecture", SummaryText: "Architecture overview of the module.",
				Metadata: `{}`},
		},
	}
	ev, err := GetSummaryEvidence(r, "c", "What is the architecture?", "architecture")
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 1 {
		t.Fatalf("expected 1 evidence, got %d", len(ev))
	}
	// token match "architecture": +8
	// level=2 else branch: +min(2,3)=2
	// architecture + level>0: +5
	// useful-summary: +3
	// Total: 18
	if ev[0].Score != 18 {
		t.Errorf("expected score=18, got %+v", ev[0])
	}
}

func TestIsUsefulSummary(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"   ", false},
		{"N/A", false},
		{"Unknown", false},
		{"Could not summarize this unit.", false},
		{"The auth service coordinates login.", true},
	}
	for _, c := range cases {
		if got := isUsefulSummary(c.in); got != c.want {
			t.Errorf("isUsefulSummary(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
