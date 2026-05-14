// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

import (
	"strings"
	"testing"
)

func TestDiscussionContextFromArtifact_NilArtifact(t *testing.T) {
	if got := DiscussionContextFromArtifact(nil); got != "" {
		t.Fatalf("expected empty string for nil artifact, got %q", got)
	}
}

func TestDiscussionContextFromArtifact_EmptySections(t *testing.T) {
	art := &Artifact{
		Type:     ArtifactCliffNotes,
		Sections: nil,
	}
	if got := DiscussionContextFromArtifact(art); got != "" {
		t.Fatalf("expected empty string for artifact with no sections, got %q", got)
	}
	art.Sections = []Section{}
	if got := DiscussionContextFromArtifact(art); got != "" {
		t.Fatalf("expected empty string for artifact with empty sections slice, got %q", got)
	}
}

func TestDiscussionContextFromArtifact_ThreeSections(t *testing.T) {
	art := &Artifact{
		Type: ArtifactCliffNotes,
		Sections: []Section{
			{Title: "Overview", Summary: "The main entry point."},
			{Title: "Auth", Content: "Handles authentication.", Summary: ""},
			{Title: "Storage", Summary: "Database layer."},
		},
	}
	got := DiscussionContextFromArtifact(art)
	want := "Indexed cliff_notes context for repository.\n\n" +
		"Overview:\nThe main entry point.\n\n" +
		"Auth:\nHandles authentication.\n\n" +
		"Storage:\nDatabase layer."
	if got != want {
		t.Fatalf("three-section output mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestDiscussionContextFromArtifact_TruncatesAtSix(t *testing.T) {
	sections := make([]Section, 10)
	for i := range sections {
		sections[i] = Section{
			Title:   "Section",
			Summary: "body",
		}
	}
	art := &Artifact{
		Type:     ArtifactCliffNotes,
		Sections: sections,
	}
	got := DiscussionContextFromArtifact(art)
	// Header + 6 sections = 7 parts joined by "\n\n".
	parts := strings.Split(got, "\n\n")
	if len(parts) != 7 {
		t.Fatalf("expected 7 parts (1 header + 6 sections), got %d\noutput: %q", len(parts), got)
	}
}

// TestDiscussionContextFromArtifact_PromptShapeSnapshot pins the exact output
// format against a known fixture. If this test fails, the discussion-context
// prompt shape changed. The LLM has been trained against a stable format;
// deliberate format changes need an explicit CHANGELOG entry + worker-side
// prompt alignment. See CA-241 / CA-329.
func TestDiscussionContextFromArtifact_PromptShapeSnapshot(t *testing.T) {
	art := &Artifact{
		Type: ArtifactCliffNotes,
		Scope: &ArtifactScope{
			ScopePath: "github.com/example/repo",
		},
		Sections: []Section{
			{Title: "Entry Points", Summary: "cmd/main.go bootstraps the server."},
			{Title: "Core Logic", Content: "internal/core/ houses the domain."},
		},
	}
	got := DiscussionContextFromArtifact(art)
	const want = "Indexed cliff_notes context for github.com/example/repo.\n\n" +
		"Entry Points:\ncmd/main.go bootstraps the server.\n\n" +
		"Core Logic:\ninternal/core/ houses the domain."
	if got != want {
		t.Fatalf(
			"prompt shape snapshot mismatch — this is a CA-241/CA-329 regression.\n"+
				"If this change is intentional, update the snapshot and add a CHANGELOG entry.\n\n"+
				"got:\n%s\n\nwant:\n%s",
			got, want,
		)
	}
}
