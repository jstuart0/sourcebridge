// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package requirements

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func fixtureDir() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..", "tests", "fixtures", "multi-lang-repo")
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestParseMarkdownFixture(t *testing.T) {
	content := readFixture(t, "requirements.md")
	result := ParseMarkdown(content)

	if len(result.Requirements) != 14 {
		t.Fatalf("expected 14 requirements, got %d", len(result.Requirements))
	}

	first := result.Requirements[0]
	if first.ExternalID != "REQ-001" {
		t.Errorf("expected REQ-001, got %s", first.ExternalID)
	}
	if first.Title != "System Startup" {
		t.Errorf("expected 'System Startup', got %q", first.Title)
	}
	if first.Priority != "High" {
		t.Errorf("expected priority High, got %q", first.Priority)
	}
	if len(first.AcceptanceCriteria) != 3 {
		t.Errorf("expected 3 acceptance criteria, got %d", len(first.AcceptanceCriteria))
	}
}

func TestParseMarkdownIDs(t *testing.T) {
	content := readFixture(t, "requirements.md")
	result := ParseMarkdown(content)

	expected := []string{
		"REQ-001", "REQ-003", "REQ-004", "REQ-005", "REQ-006",
		"REQ-007", "REQ-008", "REQ-009", "REQ-010", "REQ-011",
		"REQ-012", "REQ-013", "REQ-014", "REQ-015",
	}

	if len(result.Requirements) != len(expected) {
		t.Fatalf("expected %d requirements, got %d", len(expected), len(result.Requirements))
	}

	for i, req := range result.Requirements {
		if req.ExternalID != expected[i] {
			t.Errorf("requirement %d: expected %s, got %s", i, expected[i], req.ExternalID)
		}
	}
}

func TestParseMarkdownPriorities(t *testing.T) {
	content := readFixture(t, "requirements.md")
	result := ParseMarkdown(content)

	pmap := make(map[string]string)
	for _, r := range result.Requirements {
		pmap[r.ExternalID] = r.Priority
	}

	if pmap["REQ-010"] != "Critical" {
		t.Errorf("REQ-010 priority: expected Critical, got %q", pmap["REQ-010"])
	}
	if pmap["REQ-006"] != "Medium" {
		t.Errorf("REQ-006 priority: expected Medium, got %q", pmap["REQ-006"])
	}
}

func TestParseMarkdownEmpty(t *testing.T) {
	result := ParseMarkdown("")
	if len(result.Requirements) != 0 {
		t.Errorf("expected 0 requirements, got %d", len(result.Requirements))
	}
}

func TestParseMarkdownNoPriority(t *testing.T) {
	content := `## REQ-099: No Priority
This requirement has no priority field.
- **Acceptance Criteria:**
  - Something works
`
	result := ParseMarkdown(content)
	if len(result.Requirements) != 1 {
		t.Fatalf("expected 1 requirement, got %d", len(result.Requirements))
	}
	if result.Requirements[0].Priority != "" {
		t.Errorf("expected empty priority, got %q", result.Requirements[0].Priority)
	}
}

func TestParseCSVFixture(t *testing.T) {
	content := readFixture(t, "requirements.csv")
	result := ParseCSV(content, nil)

	if len(result.Requirements) != 4 {
		t.Fatalf("expected 4 requirements, got %d", len(result.Requirements))
	}

	first := result.Requirements[0]
	if first.ExternalID != "REQ-001" {
		t.Errorf("expected REQ-001, got %s", first.ExternalID)
	}
	if first.Priority != "High" {
		t.Errorf("expected High, got %q", first.Priority)
	}
	if len(first.AcceptanceCriteria) != 3 {
		t.Errorf("expected 3 criteria, got %d", len(first.AcceptanceCriteria))
	}
}

func TestParseCSVCustomMapping(t *testing.T) {
	content := `req_num,name,desc,prio,criteria
R-01,Test Req,A test requirement,High,Works correctly;Handles errors
`
	mapping := map[string]string{
		"id":                  "req_num",
		"title":               "name",
		"description":         "desc",
		"priority":            "prio",
		"acceptance_criteria": "criteria",
	}
	result := ParseCSV(content, mapping)
	if len(result.Requirements) != 1 {
		t.Fatalf("expected 1 requirement, got %d", len(result.Requirements))
	}
	if result.Requirements[0].ExternalID != "R-01" {
		t.Errorf("expected R-01, got %s", result.Requirements[0].ExternalID)
	}
	if len(result.Requirements[0].AcceptanceCriteria) != 2 {
		t.Errorf("expected 2 criteria, got %d", len(result.Requirements[0].AcceptanceCriteria))
	}
}

func TestParseCSVEmpty(t *testing.T) {
	result := ParseCSV("", nil)
	if len(result.Requirements) != 0 {
		t.Errorf("expected 0 requirements, got %d", len(result.Requirements))
	}
}

func TestParseCSVSkipsIncomplete(t *testing.T) {
	content := `id,title,description,priority,acceptance_criteria
REQ-001,Valid Row,Description,High,Criterion 1
,Missing ID,Description,High,Criterion 2
REQ-003,,Description,Medium,Criterion 3
`
	result := ParseCSV(content, nil)
	if len(result.Requirements) != 1 {
		t.Fatalf("expected 1 requirement, got %d", len(result.Requirements))
	}
	if result.Requirements[0].ExternalID != "REQ-001" {
		t.Errorf("expected REQ-001, got %s", result.Requirements[0].ExternalID)
	}
}
