// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadRequirementLines(t *testing.T) {
	dir := t.TempDir()
	readme := `# Project

Setup stuff.

## Requirements

- REQ-123: Users must authenticate
  - REQ-456 sub-item
- A non-req bullet
  REQ-AB-9 is inline here too
`
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(readme), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadRequirementLines(dir)
	want := []string{
		"- REQ-123: Users must authenticate",
		"- REQ-456 sub-item",
		"REQ-AB-9 is inline here too",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestLoadRequirementLines_NoReadme(t *testing.T) {
	dir := t.TempDir()
	if got := LoadRequirementLines(dir); len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestSelectRelevantRequirements_ExplicitInQuestion(t *testing.T) {
	lines := []string{
		"- REQ-123: do X",
		"- REQ-456: do Y",
	}
	got := SelectRelevantRequirements(lines, nil, "Tell me about REQ-456")
	if !reflect.DeepEqual(got, []string{"- REQ-456: do Y"}) {
		t.Errorf("got %v", got)
	}
}

func TestSelectRelevantRequirements_InEvidence(t *testing.T) {
	lines := []string{
		"- REQ-1: alpha",
		"- REQ-2: beta",
	}
	got := SelectRelevantRequirements(lines, []string{"snippet referencing REQ-2 inline"}, "explain this code")
	if !reflect.DeepEqual(got, []string{"- REQ-2: beta"}) {
		t.Errorf("got %v", got)
	}
}

func TestSelectRelevantRequirements_RequirementWordFallback(t *testing.T) {
	lines := []string{
		"- REQ-1: login flow works end to end",
		"- REQ-2: billing and payments",
	}
	got := SelectRelevantRequirements(lines, nil, "what requirement covers login?")
	if len(got) == 0 {
		t.Fatal("expected fallback token-match results")
	}
	if got[0] != "- REQ-1: login flow works end to end" {
		t.Errorf("expected login row first, got %q", got[0])
	}
}

func TestSelectRelevantRequirements_NothingSelected(t *testing.T) {
	lines := []string{"- REQ-1: something"}
	got := SelectRelevantRequirements(lines, nil, "plain question no req word")
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestSelectRelevantRequirements_EightLimit(t *testing.T) {
	lines := []string{}
	for i := 0; i < 20; i++ {
		lines = append(lines, "- REQ-X: requirement line with auth token")
	}
	got := SelectRelevantRequirements(lines, nil, "what requirement covers auth token?")
	if len(got) != 8 {
		t.Errorf("expected cap=8, got %d", len(got))
	}
}
