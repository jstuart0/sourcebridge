// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package activitylog_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates/activitylog"
)

// --- stubs ---

type stubGitLog struct {
	commits []templates.Commit
}

func (s *stubGitLog) Commits(_ string) ([]templates.Commit, error) {
	return s.commits, nil
}

type stubLLM struct {
	response string
}

func (s *stubLLM) Complete(_ context.Context, _, _ string) (string, error) {
	return s.response, nil
}

// --- helpers ---

var fixedTime = time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC)

func monday(weeksAgo int) time.Time {
	return fixedTime.AddDate(0, 0, -weeksAgo*7)
}

func makeCommit(sha, author, subject, body string, ts time.Time, files int, paths []string) templates.Commit {
	short := sha
	if len(sha) > 7 {
		short = sha[:7]
	}
	return templates.Commit{
		SHA:          sha,
		ShortSHA:     short,
		Author:       author,
		AuthorEmail:  author + "@example.com",
		Subject:      subject,
		Body:         body,
		Message:      subject + "\n\n" + body,
		Timestamp:    ts,
		FilesChanged: files,
		TouchedPaths: paths,
	}
}

func newInput(commits []templates.Commit, llm templates.LLMCaller, enableDigest bool) templates.GenerateInput {
	return templates.GenerateInput{
		RepoID:   "testrepo",
		Audience: quality.AudienceEngineers,
		GitLog:   &stubGitLog{commits: commits},
		LLM:      llm,
		Now:      fixedTime,
		Config:   templates.TemplateConfig{EnableLLMDigest: enableDigest},
	}
}

// --- tests ---

func TestActivityLog_ID(t *testing.T) {
	al := activitylog.New()
	if al.ID() != "activity_log" {
		t.Fatalf("expected ID=activity_log, got %q", al.ID())
	}
}

func TestActivityLog_RequiresGitLog(t *testing.T) {
	al := activitylog.New()
	input := newInput(nil, nil, false)
	input.GitLog = nil
	_, err := al.Generate(context.Background(), input)
	if err == nil {
		t.Fatal("expected error when GitLog is nil")
	}
}

func TestActivityLog_RequiresLLMWhenDigestEnabled(t *testing.T) {
	al := activitylog.New()
	input := newInput(nil, nil, true) // EnableLLMDigest=true, LLM=nil
	_, err := al.Generate(context.Background(), input)
	if err == nil {
		t.Fatal("expected error when EnableLLMDigest is true and LLM is nil")
	}
}

func TestActivityLog_EmptyCommitLog(t *testing.T) {
	al := activitylog.New()
	page, err := al.Generate(context.Background(), newInput(nil, nil, false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page.Blocks) != 0 {
		t.Errorf("expected 0 blocks for empty commit log, got %d", len(page.Blocks))
	}
}

func TestActivityLog_BucketsCommitsByWeek(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("aaa1111", "alice", "fix: auth bug", "", monday(0).Add(time.Hour), 3, nil),
		makeCommit("bbb2222", "bob", "feat: new endpoint", "", monday(0).Add(2*time.Hour), 5, nil),
		makeCommit("ccc3333", "alice", "chore: deps", "", monday(1).Add(time.Hour), 1, nil),
	}

	al := activitylog.New()
	page, err := al.Generate(context.Background(), newInput(commits, nil, false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// There should be 2 H2 headings (2 distinct weeks).
	headings := 0
	for _, blk := range page.Blocks {
		if blk.Kind == ast.BlockKindHeading && blk.Content.Heading.Level == 2 {
			headings++
		}
	}
	if headings != 2 {
		t.Errorf("expected 2 week headings, got %d", headings)
	}
}

func TestActivityLog_TableIncludesAuthorStats(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("aaa1111", "alice", "fix: bug", "", monday(0).Add(time.Hour), 3,
			[]string{"internal/auth/jwt.go", "internal/auth/middleware.go"}),
		makeCommit("bbb2222", "alice", "feat: endpoint", "", monday(0).Add(2*time.Hour), 5,
			[]string{"internal/api/rest.go"}),
	}

	al := activitylog.New()
	page, err := al.Generate(context.Background(), newInput(commits, nil, false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, blk := range page.Blocks {
		if blk.Kind != ast.BlockKindTable {
			continue
		}
		tbl := blk.Content.Table
		for _, row := range tbl.Rows {
			if len(row) > 0 && row[0] == "alice" {
				found = true
				if row[1] != "2" {
					t.Errorf("expected alice commit count=2, got %q", row[1])
				}
			}
		}
	}
	if !found {
		t.Error("no table row for author 'alice'")
	}
}

func TestActivityLog_BreakingChangeCallout(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("abc1234", "dev", "feat: switch auth library", "BREAKING CHANGE: the old JWT format is no longer accepted.", monday(0).Add(time.Hour), 10, nil),
	}

	al := activitylog.New()
	page, err := al.Generate(context.Background(), newInput(commits, nil, false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, blk := range page.Blocks {
		if blk.Kind == ast.BlockKindCallout && blk.Content.Callout != nil {
			if strings.Contains(blk.Content.Callout.Body, "BREAKING CHANGE") {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected a BREAKING CHANGE callout block, not found")
	}
}

func TestActivityLog_NoBreakingChangeCalloutWhenNone(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("abc1234", "dev", "feat: add new endpoint", "", monday(0).Add(time.Hour), 2, nil),
	}

	al := activitylog.New()
	page, err := al.Generate(context.Background(), newInput(commits, nil, false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, blk := range page.Blocks {
		if blk.Kind == ast.BlockKindCallout {
			t.Error("unexpected callout block when no BREAKING CHANGE commits")
		}
	}
}

func TestActivityLog_LLMDigestAddedWhenEnabled(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("abc1234", "dev", "feat: add auth", "", monday(0).Add(time.Hour), 3, nil),
	}

	const digestText = "This week the team shipped the new authentication subsystem. No breaking changes."

	al := activitylog.New()
	page, err := al.Generate(context.Background(), newInput(commits, &stubLLM{response: digestText}, true))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, blk := range page.Blocks {
		if blk.Kind == ast.BlockKindParagraph && blk.Content.Paragraph != nil {
			if strings.Contains(blk.Content.Paragraph.Markdown, digestText) {
				found = true
			}
		}
	}
	if !found {
		t.Error("LLM digest paragraph not found in page blocks")
	}
}

func TestActivityLog_LLMDigestAbsentWhenDisabled(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("abc1234", "dev", "feat: add auth", "", monday(0).Add(time.Hour), 3, nil),
	}

	al := activitylog.New()
	// nil LLM is fine when EnableLLMDigest is false.
	page, err := al.Generate(context.Background(), newInput(commits, nil, false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, blk := range page.Blocks {
		if blk.Kind == ast.BlockKindParagraph {
			// There should be no LLM paragraph when digest is disabled.
			t.Errorf("unexpected paragraph block when EnableLLMDigest=false: %q", blk.Content.Paragraph.Markdown)
		}
	}
}

func TestActivityLog_MultipleWeeksMultipleAuthors(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("aaa0001", "alice", "fix: bug1", "", monday(0).Add(1*time.Hour), 2, []string{"internal/auth/auth.go"}),
		makeCommit("aaa0002", "bob", "feat: feature1", "", monday(0).Add(2*time.Hour), 4, []string{"internal/api/rest.go"}),
		makeCommit("bbb0001", "carol", "chore: cleanup", "", monday(1).Add(1*time.Hour), 1, []string{"tools.go"}),
		makeCommit("bbb0002", "alice", "docs: update readme", "", monday(1).Add(2*time.Hour), 1, []string{"README.md"}),
	}

	al := activitylog.New()
	page, err := al.Generate(context.Background(), newInput(commits, nil, false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 2 weeks → 2 headings, 2 tables.
	headings := 0
	tables := 0
	for _, blk := range page.Blocks {
		switch blk.Kind {
		case ast.BlockKindHeading:
			headings++
		case ast.BlockKindTable:
			tables++
		}
	}
	if headings != 2 {
		t.Errorf("expected 2 headings, got %d", headings)
	}
	if tables != 2 {
		t.Errorf("expected 2 tables, got %d", tables)
	}
}

func TestActivityLog_TopPackagesList(t *testing.T) {
	// Verify that authors touching many packages see abbreviated list.
	paths := make([]string, 6)
	for i := range paths {
		paths[i] = fmt.Sprintf("internal/pkg%d/file.go", i)
	}
	commits := []templates.Commit{
		makeCommit("abc1234", "dev", "refactor: big cleanup", "", monday(0).Add(time.Hour), len(paths), paths),
	}

	al := activitylog.New()
	page, err := al.Generate(context.Background(), newInput(commits, nil, false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, blk := range page.Blocks {
		if blk.Kind != ast.BlockKindTable {
			continue
		}
		for _, row := range blk.Content.Table.Rows {
			if len(row) >= 4 && row[0] == "dev" {
				// Should have "N more" suffix when > 3 packages.
				if !strings.Contains(row[3], "more") {
					t.Errorf("expected 'more' in packages column for 6 packages, got %q", row[3])
				}
				return
			}
		}
	}
}

func TestActivityLog_PageManifest(t *testing.T) {
	al := activitylog.New()
	page, err := al.Generate(context.Background(), newInput(nil, nil, false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if page.Manifest.Template != "activity_log" {
		t.Errorf("expected template=activity_log, got %q", page.Manifest.Template)
	}
}

func TestActivityLogValidatorProfile(t *testing.T) {
	profile, ok := activitylog.ValidatorProfile(quality.AudienceEngineers)
	if !ok {
		t.Fatal("expected a validator profile for activity_log/for-engineers")
	}
	if profile.Template != quality.TemplateActivityLog {
		t.Errorf("expected TemplateActivityLog, got %v", profile.Template)
	}
}
