// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"
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

func (f *fakeReader) GetRepositoryUnderstanding(_ context.Context, repoID string, scope knowledge.ArtifactScope) *knowledge.RepositoryUnderstanding {
	return f.understanding
}
func (f *fakeReader) GetSummaryNodes(_ context.Context, corpusID string) ([]comprehension.SummaryNode, error) {
	return f.summaryNodes, f.summaryErr
}

func TestGetRepositoryStatus_NoRow(t *testing.T) {
	r := &fakeReader{}
	st := GetRepositoryStatus(t.Context(), r, "repo-1", "MyRepo")
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
	st := GetRepositoryStatus(t.Context(), r, "repo-1", "MyRepo")
	if !st.Ready {
		t.Fatal("expected Ready=true")
	}
	if st.CorpusID != "corpus-1" || st.UnderstandingRevision != "rev-1" {
		t.Errorf("fields not propagated: %+v", st)
	}
}

// TestGetRepositoryStatus_ReadyAndPartialWhenReadyStagePartialTree verifies
// that stage=ready with treeStatus=partial is Ready=true, Partial=true.
// This is the post-CA-319 behavior: the predicate was relaxed deliberately
// to enable QA on partial corpora. The old test name was
// TestGetRepositoryStatus_NotReadyWhenPartial; it was renamed and flipped
// when the predicate changed.
func TestGetRepositoryStatus_ReadyAndPartialWhenReadyStagePartialTree(t *testing.T) {
	r := &fakeReader{
		understanding: &knowledge.RepositoryUnderstanding{
			Stage:      knowledge.UnderstandingReady,
			TreeStatus: knowledge.UnderstandingTreePartial,
		},
	}
	st := GetRepositoryStatus(t.Context(), r, "r", "n")
	if !st.Ready {
		t.Error("stage=ready + treeStatus=partial should be Ready=true (CA-319 predicate relaxation)")
	}
	if !st.Partial {
		t.Error("stage=ready + treeStatus=partial should be Partial=true")
	}
}

// TestGetRepositoryStatus_ReadinessMatrix pins the full 6×3 stage/treeStatus
// grid against the CA-319 readiness predicate. If a new
// RepositoryUnderstandingStage constant is added to internal/knowledge/models.go
// without updating this test, the enum-growth tripwire at the top of the test
// will fail loudly — classify the new stage as Ready/Not-ready/Partial and add
// it to the table before proceeding.
func TestGetRepositoryStatus_ReadinessMatrix(t *testing.T) {
	// Enum-growth tripwire: update this slice whenever a new
	// RepositoryUnderstandingStage is added to internal/knowledge/models.go.
	// The test will fail until the new stage is classified in the table below.
	knownStages := []knowledge.RepositoryUnderstandingStage{
		knowledge.UnderstandingBuildingTree,
		knowledge.UnderstandingFirstPassReady,
		knowledge.UnderstandingNeedsRefresh,
		knowledge.UnderstandingDeepening,
		knowledge.UnderstandingReady,
		knowledge.UnderstandingFailed,
	}
	if len(knownStages) != 6 {
		t.Fatalf("enum-growth tripwire: expected 6 known stages, got %d — update knownStages and the table below", len(knownStages))
	}

	type cell struct {
		stage       knowledge.RepositoryUnderstandingStage
		treeStatus  knowledge.RepositoryUnderstandingTreeStatus
		wantReady   bool
		wantPartial bool
		note        string
	}

	cases := []cell{
		// building_tree: no summary nodes exist yet — always blocked.
		{knowledge.UnderstandingBuildingTree, knowledge.UnderstandingTreeMissing, false, false, "building_tree+missing: no corpus"},
		{knowledge.UnderstandingBuildingTree, knowledge.UnderstandingTreePartial, false, false, "building_tree+partial: structurally reachable but excluded"},
		// (building_tree, complete) is structurally unreachable: tree-complete
		// write and stage advancement happen together in the build path. The
		// predicate excludes it defensively.
		{knowledge.UnderstandingBuildingTree, knowledge.UnderstandingTreeComplete, false, false, "building_tree+complete: structurally unreachable, excluded defensively"},

		// first_pass_ready: summary nodes exist; deepening not yet started.
		// Primary new Ready+Partial cells.
		{knowledge.UnderstandingFirstPassReady, knowledge.UnderstandingTreeMissing, false, false, "first_pass_ready+missing: tree must be partial or complete"},
		{knowledge.UnderstandingFirstPassReady, knowledge.UnderstandingTreePartial, true, true, "first_pass_ready+partial: usable, partial corpus"},
		{knowledge.UnderstandingFirstPassReady, knowledge.UnderstandingTreeComplete, true, true, "first_pass_ready+complete: usable, not yet deepened"},

		// needs_refresh: stale but awaiting reindex — excluded (not progressing).
		{knowledge.UnderstandingNeedsRefresh, knowledge.UnderstandingTreeMissing, false, false, "needs_refresh+missing: stale"},
		{knowledge.UnderstandingNeedsRefresh, knowledge.UnderstandingTreePartial, false, false, "needs_refresh+partial: stale"},
		{knowledge.UnderstandingNeedsRefresh, knowledge.UnderstandingTreeComplete, false, false, "needs_refresh+complete: stale"},

		// deepening: active deepening pass in progress. Primary new Ready+Partial cells.
		{knowledge.UnderstandingDeepening, knowledge.UnderstandingTreeMissing, false, false, "deepening+missing: tree must be partial or complete"},
		{knowledge.UnderstandingDeepening, knowledge.UnderstandingTreePartial, true, true, "deepening+partial: usable, partial corpus"},
		{knowledge.UnderstandingDeepening, knowledge.UnderstandingTreeComplete, true, true, "deepening+complete: usable, deepening in progress"},

		// ready: fully deepened.
		{knowledge.UnderstandingReady, knowledge.UnderstandingTreeMissing, false, false, "ready+missing: tree must be partial or complete"},
		{knowledge.UnderstandingReady, knowledge.UnderstandingTreePartial, true, true, "ready+partial: usable but tree incomplete"},
		{knowledge.UnderstandingReady, knowledge.UnderstandingTreeComplete, true, false, "ready+complete: fully ready, not partial"},

		// failed: reachable but intentionally excluded. Recovery path is Phase 2
		// NeedsRefresh transition; the CTA path handles the immediate request.
		// (failed, complete) is reachable in theory (deepening completed but the
		// subsequent stage transition failed), but is intentionally excluded: the
		// failed error_code/message signals something is wrong.
		{knowledge.UnderstandingFailed, knowledge.UnderstandingTreeMissing, false, false, "failed+missing: excluded, use CTA path"},
		{knowledge.UnderstandingFailed, knowledge.UnderstandingTreePartial, false, false, "failed+partial: excluded, use CTA path"},
		{knowledge.UnderstandingFailed, knowledge.UnderstandingTreeComplete, false, false, "failed+complete: reachable but intentionally excluded; recovery via Phase 2 NeedsRefresh"},
	}

	for _, c := range cases {
		t.Run(string(c.stage)+"+"+string(c.treeStatus), func(t *testing.T) {
			r := &fakeReader{
				understanding: &knowledge.RepositoryUnderstanding{
					Stage:      c.stage,
					TreeStatus: c.treeStatus,
					CorpusID:   "corpus-x",
				},
			}
			st := GetRepositoryStatus(t.Context(), r, "repo", "Repo")
			if st.Ready != c.wantReady {
				t.Errorf("Ready: got %v, want %v — %s", st.Ready, c.wantReady, c.note)
			}
			if st.Partial != c.wantPartial {
				t.Errorf("Partial: got %v, want %v — %s", st.Partial, c.wantPartial, c.note)
			}
		})
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
	ev, err := GetSummaryEvidence(t.Context(), r, "c", "How does the auth service handle sessions?", "execution_flow")
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
	ev, err := GetSummaryEvidence(t.Context(), r, "", "anything", "behavior")
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
	ev, err := GetSummaryEvidence(t.Context(), r, "c", "What is the architecture?", "architecture")
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
