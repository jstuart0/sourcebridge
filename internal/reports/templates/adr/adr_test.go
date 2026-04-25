// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package adr_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates/adr"
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

func makeCommit(sha, author, subject, body string) templates.Commit {
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
		Timestamp:    fixedTime,
		FilesChanged: 5,
		Insertions:   30,
		Deletions:    10,
	}
}

// canned LLM response with all three sections.
const cannedADRProse = `## Context
The authentication library we were using (legacy-jwt v2) had not been updated in 3 years and had 2 known CVEs. The team evaluated 3 alternatives.

## Decision
We replaced legacy-jwt v2 with golang-jwt v5. The golang-jwt library is actively maintained, supports JWK rotation natively, and has no known CVEs as of 2026-04-25.

## Consequences
All token validation paths now use the new library. The token format changed; existing sessions are invalidated on deployment. The old parse path is removed from internal/auth/legacy.go.`

func newInput(commits []templates.Commit, llm templates.LLMCaller) templates.GenerateInput {
	return templates.GenerateInput{
		RepoID:   "testrepo",
		Audience: quality.AudienceEngineers,
		GitLog:   &stubGitLog{commits: commits},
		LLM:      llm,
		Now:      fixedTime,
	}
}

// --- tests ---

func TestADR_ID(t *testing.T) {
	a := adr.New()
	if a.ID() != "adr" {
		t.Fatalf("expected ID=adr, got %q", a.ID())
	}
}

func TestADR_RequiresGitLog(t *testing.T) {
	input := newInput(nil, &stubLLM{})
	input.GitLog = nil
	_, err := adr.GenerateAll(context.Background(), input)
	if err == nil {
		t.Fatal("expected error when GitLog is nil")
	}
}

func TestADR_RequiresLLM(t *testing.T) {
	input := newInput(nil, nil)
	_, err := adr.GenerateAll(context.Background(), input)
	if err == nil {
		t.Fatal("expected error when LLM is nil")
	}
}

func TestADR_NoADRCommits_EmptyResult(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("aaa1111", "alice", "fix: login timeout", ""),
		makeCommit("bbb2222", "bob", "chore: upgrade deps", ""),
	}

	result, err := adr.GenerateAll(context.Background(), newInput(commits, &stubLLM{response: cannedADRProse}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Pages) != 0 {
		t.Errorf("expected 0 ADR pages for non-ADR commits, got %d", len(result.Pages))
	}
}

func TestADR_DetectsDecisionPrefix(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("aaa1111", "alice", "decision: switch JWT library", "We evaluated golang-jwt and chose it."),
		makeCommit("bbb2222", "bob", "fix: login bug", ""),
	}

	result, err := adr.GenerateAll(context.Background(), newInput(commits, &stubLLM{response: cannedADRProse}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Pages) != 1 {
		t.Fatalf("expected 1 ADR page, got %d", len(result.Pages))
	}
}

func TestADR_DetectsADRPrefix(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("ccc3333", "carol", "adr: adopt hexagonal architecture", ""),
	}
	result, err := adr.GenerateAll(context.Background(), newInput(commits, &stubLLM{response: cannedADRProse}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Pages) != 1 {
		t.Errorf("expected 1 ADR page for 'adr:' prefix, got %d", len(result.Pages))
	}
}

func TestADR_DetectsDesignPrefix(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("ddd4444", "dave", "design: event sourcing for orders", ""),
	}
	result, err := adr.GenerateAll(context.Background(), newInput(commits, &stubLLM{response: cannedADRProse}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Pages) != 1 {
		t.Errorf("expected 1 ADR page for 'design:' prefix, got %d", len(result.Pages))
	}
}

func TestADR_DetectsBreakingChangeInBody(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("eee5555", "eve", "feat: new auth token format", "BREAKING CHANGE: old tokens are invalidated."),
		makeCommit("fff6666", "frank", "fix: minor bug", ""),
	}
	result, err := adr.GenerateAll(context.Background(), newInput(commits, &stubLLM{response: cannedADRProse}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Pages) != 1 {
		t.Errorf("expected 1 ADR page (BREAKING CHANGE in body), got %d", len(result.Pages))
	}
}

func TestADR_DetectsWeSwitchingPhrase(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("ggg7777", "grace", "refactor: switching to gRPC for service communication", ""),
	}
	result, err := adr.GenerateAll(context.Background(), newInput(commits, &stubLLM{response: cannedADRProse}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Pages) != 1 {
		t.Errorf("expected 1 ADR page for 'switching to' phrase, got %d", len(result.Pages))
	}
}

func TestADR_DetectsWeDecidedPhrase(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("hhh8888", "henry", "we decided to move auth to a separate service", ""),
	}
	result, err := adr.GenerateAll(context.Background(), newInput(commits, &stubLLM{response: cannedADRProse}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Pages) != 1 {
		t.Errorf("expected 1 ADR page for 'we decided' phrase, got %d", len(result.Pages))
	}
}

func TestADR_PageHasThreeSections(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("aaa1111", "alice", "decision: use postgres over mongodb", "We considered both options."),
	}
	result, err := adr.GenerateAll(context.Background(), newInput(commits, &stubLLM{response: cannedADRProse}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Pages) == 0 {
		t.Fatal("expected at least one ADR page")
	}
	page := result.Pages[0]

	var sectionHeadings []string
	for _, blk := range page.Blocks {
		if blk.Kind == ast.BlockKindHeading && blk.Content.Heading.Level == 2 {
			sectionHeadings = append(sectionHeadings, blk.Content.Heading.Text)
		}
	}

	required := []string{"Context", "Decision", "Consequences"}
	for _, req := range required {
		found := false
		for _, h := range sectionHeadings {
			if h == req {
				found = true
			}
		}
		if !found {
			t.Errorf("required section %q not found in headings %v", req, sectionHeadings)
		}
	}
}

func TestADR_LLMProseInSectionBlocks(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("aaa1111", "alice", "decision: switch to PostgreSQL", ""),
	}
	result, err := adr.GenerateAll(context.Background(), newInput(commits, &stubLLM{response: cannedADRProse}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Pages) == 0 {
		t.Fatal("no pages generated")
	}

	fullText := ""
	for _, blk := range result.Pages[0].Blocks {
		if blk.Kind == ast.BlockKindParagraph {
			fullText += blk.Content.Paragraph.Markdown + "\n"
		}
	}

	// Key phrases from the canned prose should be present.
	checks := []string{
		"legacy-jwt",
		"golang-jwt v5",
		"known CVEs",
		"JWK rotation",
	}
	for _, phrase := range checks {
		if !strings.Contains(fullText, phrase) {
			t.Errorf("expected phrase %q in page paragraphs, not found", phrase)
		}
	}
}

func TestADR_MultipleADRCommitsProduceMultiplePages(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("aaa1111", "alice", "decision: adopt grpc", ""),
		makeCommit("bbb2222", "bob", "adr: event sourcing for payments", ""),
		makeCommit("ccc3333", "carol", "fix: minor bug", ""),
		makeCommit("ddd4444", "dave", "design: service mesh via istio", ""),
	}
	result, err := adr.GenerateAll(context.Background(), newInput(commits, &stubLLM{response: cannedADRProse}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Pages) != 3 {
		t.Errorf("expected 3 ADR pages (3 matching commits), got %d", len(result.Pages))
	}
}

func TestADR_PageIDsAreDistinctPerCommit(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("aaa1111", "alice", "decision: adopt grpc", ""),
		makeCommit("bbb2222", "bob", "adr: event sourcing", ""),
	}
	result, err := adr.GenerateAll(context.Background(), newInput(commits, &stubLLM{response: cannedADRProse}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Pages) != 2 {
		t.Fatalf("expected 2 pages, got %d", len(result.Pages))
	}
	if result.Pages[0].ID == result.Pages[1].ID {
		t.Errorf("two ADR pages have the same page ID: %q", result.Pages[0].ID)
	}
}

func TestADR_TitleStripsPrefixAndCapitalises(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("aaa1111", "alice", "decision: adopt hexagonal architecture", ""),
	}
	result, err := adr.GenerateAll(context.Background(), newInput(commits, &stubLLM{response: cannedADRProse}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Pages) == 0 {
		t.Fatal("no pages generated")
	}

	// The H1 heading should not contain "decision:" and should be capitalised.
	h1Text := ""
	for _, blk := range result.Pages[0].Blocks {
		if blk.Kind == ast.BlockKindHeading && blk.Content.Heading.Level == 1 {
			h1Text = blk.Content.Heading.Text
		}
	}
	if strings.HasPrefix(strings.ToLower(h1Text), "decision:") {
		t.Errorf("H1 title still contains 'decision:' prefix: %q", h1Text)
	}
	if h1Text == "" {
		t.Error("H1 title is empty")
	}
	// First character should be uppercase.
	if h1Text != "" && h1Text != strings.ToUpper(h1Text[:1])+h1Text[1:] {
		t.Errorf("H1 title not capitalised: %q", h1Text)
	}
}

func TestADR_GenerateInterfaceReturnsFirstPage(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("aaa1111", "alice", "decision: adopt grpc", ""),
		makeCommit("bbb2222", "bob", "adr: event sourcing", ""),
	}
	a := adr.New()
	page, err := a.Generate(context.Background(), newInput(commits, &stubLLM{response: cannedADRProse}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return exactly the first page, not zero.
	if page.ID == "" {
		t.Error("Generate returned a zero-value page despite ADR commits being present")
	}
}

func TestADR_GenerateReturnsEmptyPageWhenNoADRCommits(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("aaa1111", "alice", "fix: login bug", ""),
	}
	a := adr.New()
	page, err := a.Generate(context.Background(), newInput(commits, &stubLLM{response: cannedADRProse}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if page.ID != "" || len(page.Blocks) != 0 {
		t.Error("expected empty page when no ADR commits present")
	}
}

func TestADR_PageManifest(t *testing.T) {
	commits := []templates.Commit{
		makeCommit("aaa1111", "alice", "decision: use redis for caching", ""),
	}
	result, err := adr.GenerateAll(context.Background(), newInput(commits, &stubLLM{response: cannedADRProse}))
	if err != nil || len(result.Pages) == 0 {
		t.Fatalf("unexpected error or no pages: %v", err)
	}
	page := result.Pages[0]
	if page.Manifest.Template != "adr" {
		t.Errorf("expected template=adr, got %q", page.Manifest.Template)
	}
	if page.Manifest.Audience != string(quality.AudienceEngineers) {
		t.Errorf("expected audience=%s, got %q", quality.AudienceEngineers, page.Manifest.Audience)
	}
}

func TestADRValidatorProfile(t *testing.T) {
	profile, ok := adr.ValidatorProfile(quality.AudienceEngineers)
	if !ok {
		t.Fatal("expected a validator profile for adr/for-engineers")
	}
	if profile.Template != quality.TemplateADR {
		t.Errorf("expected TemplateADR, got %v", profile.Template)
	}
}
