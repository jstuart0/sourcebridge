// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package changewatch

import (
	"errors"
	"testing"
	"time"
)

// TestChangeEvent_Validate_HappyPath asserts a fully-populated event
// passes validation.
func TestChangeEvent_Validate_HappyPath(t *testing.T) {
	ev := &ChangeEvent{
		SchemaVersion: ChangeEventSchemaVersion,
		EventID:       "01JAW2K8X9-test",
		RepositoryID:  "repo_abc",
		OccurredAt:    time.Now(),
		Branch:        "refs/heads/main",
		Files: []FileChange{
			{Path: "internal/api/rest/mcp.go", Status: FileChangeModified},
		},
		Source: ChangeSource{Kind: SourceKindFsnotifyLocal},
	}
	got, err := ev.Validate()
	if err != nil {
		t.Fatalf("Validate() returned err=%v, want nil", err)
	}
	if got != OutcomeIndexing {
		t.Fatalf("Validate() outcome = %q, want %q", got, OutcomeIndexing)
	}
}

// TestChangeEvent_Validate_EmptyDelta asserts the load-bearing
// guardrail #1 from the plan v5 (delta-only invariant): an event with
// empty Files[] is rejected with OutcomeRejectedNoDelta + ErrEmptyDelta.
//
// This is Phase 1 done-definition test #8's containment-rejection half.
func TestChangeEvent_Validate_EmptyDelta(t *testing.T) {
	ev := &ChangeEvent{
		SchemaVersion: ChangeEventSchemaVersion,
		EventID:       "01JAW2K8X9-empty",
		RepositoryID:  "repo_abc",
		OccurredAt:    time.Now(),
		Branch:        "refs/heads/main",
		Source:        ChangeSource{Kind: SourceKindFsnotifyLocal},
		// Files left empty intentionally
	}
	outcome, err := ev.Validate()
	if outcome != OutcomeRejectedNoDelta {
		t.Fatalf("Validate() outcome = %q, want %q", outcome, OutcomeRejectedNoDelta)
	}
	if !errors.Is(err, ErrEmptyDelta) {
		t.Fatalf("Validate() err = %v, want errors.Is(ErrEmptyDelta)", err)
	}
}

// TestChangeEvent_Validate_InvalidPaths asserts the path-normalization
// contract is enforced in Validate so violations are caught at the
// router boundary regardless of source.
func TestChangeEvent_Validate_InvalidPaths(t *testing.T) {
	bad := []string{
		"",
		"./internal/foo.go",
		"/internal/foo.go",
		"../escape/foo.go",
		"a/../b.go",
		"a//b.go",
		"a/./b.go",
		`a\b\c.go`,
	}
	for _, badPath := range bad {
		ev := &ChangeEvent{
			SchemaVersion: ChangeEventSchemaVersion,
			EventID:       "01JAW2K8X9-test",
			RepositoryID:  "repo_abc",
			OccurredAt:    time.Now(),
			Branch:        "refs/heads/main",
			Files:         []FileChange{{Path: badPath, Status: FileChangeModified}},
			Source:        ChangeSource{Kind: SourceKindFsnotifyLocal},
		}
		outcome, err := ev.Validate()
		if outcome != OutcomeRejectedInvalidPaths {
			t.Errorf("path=%q: Validate() outcome = %q, want %q", badPath, outcome, OutcomeRejectedInvalidPaths)
		}
		if !errors.Is(err, ErrInvalidPath) {
			t.Errorf("path=%q: Validate() err = %v, want errors.Is(ErrInvalidPath)", badPath, err)
		}
	}
}

// TestChangeEvent_Validate_MissingFields asserts every required field
// is checked with a clear OutcomeRejectedSchema disposition.
func TestChangeEvent_Validate_MissingFields(t *testing.T) {
	base := func() ChangeEvent {
		return ChangeEvent{
			SchemaVersion: ChangeEventSchemaVersion,
			EventID:       "01JAW2K8X9-test",
			RepositoryID:  "repo_abc",
			OccurredAt:    time.Now(),
			Branch:        "refs/heads/main",
			Files:         []FileChange{{Path: "src/a.go", Status: FileChangeModified}},
			Source:        ChangeSource{Kind: SourceKindFsnotifyLocal},
		}
	}
	cases := []struct {
		name string
		mut  func(*ChangeEvent)
	}{
		{"no schema_version", func(e *ChangeEvent) { e.SchemaVersion = "" }},
		{"no event_id", func(e *ChangeEvent) { e.EventID = "" }},
		{"no repository_id", func(e *ChangeEvent) { e.RepositoryID = "" }},
		{"no branch", func(e *ChangeEvent) { e.Branch = "" }},
		{"no occurred_at", func(e *ChangeEvent) { e.OccurredAt = time.Time{} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := base()
			c.mut(&ev)
			outcome, err := ev.Validate()
			if outcome != OutcomeRejectedSchema {
				t.Fatalf("outcome = %q, want %q", outcome, OutcomeRejectedSchema)
			}
			if err == nil {
				t.Fatalf("Validate() err = nil, want non-nil")
			}
		})
	}
}

// TestChangeEvent_Validate_UnknownSourceKind asserts unknown SourceKind
// values are rejected. New connector kinds must be enumerated
// deliberately — we don't accept future-shaped values.
func TestChangeEvent_Validate_UnknownSourceKind(t *testing.T) {
	ev := &ChangeEvent{
		SchemaVersion: ChangeEventSchemaVersion,
		EventID:       "01JAW2K8X9-test",
		RepositoryID:  "repo_abc",
		OccurredAt:    time.Now(),
		Branch:        "refs/heads/main",
		Files:         []FileChange{{Path: "src/a.go", Status: FileChangeModified}},
		Source:        ChangeSource{Kind: SourceKind("future_unknown_connector")},
	}
	outcome, err := ev.Validate()
	if outcome != OutcomeRejectedSchema {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomeRejectedSchema)
	}
	if !errors.Is(err, ErrUnknownSourceKind) {
		t.Fatalf("err = %v, want errors.Is(ErrUnknownSourceKind)", err)
	}
}

// TestChangeEvent_Validate_RenameRequiresOldPath asserts the renamed
// status carries an old_path.
func TestChangeEvent_Validate_RenameRequiresOldPath(t *testing.T) {
	ev := &ChangeEvent{
		SchemaVersion: ChangeEventSchemaVersion,
		EventID:       "01JAW2K8X9-test",
		RepositoryID:  "repo_abc",
		OccurredAt:    time.Now(),
		Branch:        "refs/heads/main",
		Files: []FileChange{
			{Path: "src/new.go", Status: FileChangeRenamed /* OldPath missing */},
		},
		Source: ChangeSource{Kind: SourceKindFsnotifyLocal},
	}
	outcome, _ := ev.Validate()
	if outcome != OutcomeRejectedSchema {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomeRejectedSchema)
	}
}

// TestSchemaMajor_ParsesCommonForms exercises the version-parser used
// by Validate.
func TestSchemaMajor_ParsesCommonForms(t *testing.T) {
	cases := []struct {
		in   string
		want int
		err  bool
	}{
		{"0", 0, false},
		{"0.1", 0, false},
		{"1.0", 1, false},
		{"1.5.2", 1, false},
		{"99", 99, false},
		{"", 0, true},
		{"abc", 0, true},
		{"-1.0", 0, true},
	}
	for _, c := range cases {
		got, err := schemaMajor(c.in)
		gotErr := err != nil
		if gotErr != c.err {
			t.Errorf("schemaMajor(%q) err = %v, want err=%v", c.in, err, c.err)
		}
		if !c.err && got != c.want {
			t.Errorf("schemaMajor(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestChangeEvent_Validate_RejectsFutureSchemaMajor — a 2.x schema
// version is rejected until we explicitly add support.
func TestChangeEvent_Validate_RejectsFutureSchemaMajor(t *testing.T) {
	ev := &ChangeEvent{
		SchemaVersion: "2.0",
		EventID:       "01JAW2K8X9-test",
		RepositoryID:  "repo_abc",
		OccurredAt:    time.Now(),
		Branch:        "refs/heads/main",
		Files:         []FileChange{{Path: "src/a.go", Status: FileChangeModified}},
		Source:        ChangeSource{Kind: SourceKindFsnotifyLocal},
	}
	outcome, err := ev.Validate()
	if outcome != OutcomeRejectedSchema {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomeRejectedSchema)
	}
	if !errors.Is(err, ErrUnsupportedSchemaMajor) {
		t.Fatalf("err = %v, want errors.Is(ErrUnsupportedSchemaMajor)", err)
	}
}
