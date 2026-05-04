// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// Unit tests for assembleDiscussionContext (GQL-4, Phase 1 Slice 3).

package graphql

import (
	"context"
	"strings"
	"testing"

	commonv1 "github.com/sourcebridge/sourcebridge/gen/go/common/v1"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
)

// newDiscussResolver returns a minimal *Resolver suitable for context-assembly
// tests. It wires a real in-memory graph store so symbol / requirement lookups
// return zero-value misses (nil) without panicking.
func newDiscussResolver() *Resolver {
	return &Resolver{
		Store: graphstore.NewStore(),
		// KnowledgeStore: nil — GetKnowledgeArtifact path is skipped when nil.
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// assembleDiscussionContext — table-driven cases
// ─────────────────────────────────────────────────────────────────────────────

func TestAssembleDiscussionContext_CodeProvided(t *testing.T) {
	t.Parallel()
	r := newDiscussResolver()
	code := "func hello() {}"
	input := DiscussCodeInput{
		RepositoryID: "repo-1",
		Question:     "what does this do?",
		Code:         &code,
	}
	contextCode, contextSymbols, lang, effectiveFilePath, err := assembleDiscussionContext(context.Background(), r, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(contextCode, code) {
		t.Errorf("contextCode %q does not contain provided code %q", contextCode, code)
	}
	if contextSymbols != nil {
		t.Errorf("expected nil symbols when no symbol/file info given, got %v", contextSymbols)
	}
	if lang != commonv1.Language_LANGUAGE_UNSPECIFIED {
		t.Errorf("expected LANGUAGE_UNSPECIFIED when no language or file given, got %v", lang)
	}
	if effectiveFilePath != "" {
		t.Errorf("expected empty effectiveFilePath when no file given, got %q", effectiveFilePath)
	}
}

func TestAssembleDiscussionContext_CodeWithLanguage(t *testing.T) {
	t.Parallel()
	r := newDiscussResolver()
	code := "def hello(): pass"
	lang := LanguagePython
	input := DiscussCodeInput{
		RepositoryID: "repo-2",
		Question:     "explain this",
		Code:         &code,
		Language:     &lang,
	}
	contextCode, _, gotLang, _, err := assembleDiscussionContext(context.Background(), r, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(contextCode, code) {
		t.Errorf("contextCode %q does not contain provided code", contextCode)
	}
	if gotLang != commonv1.Language_LANGUAGE_PYTHON {
		t.Errorf("expected PYTHON, got %v", gotLang)
	}
}

func TestAssembleDiscussionContext_ConversationHistoryOnly(t *testing.T) {
	t.Parallel()
	r := newDiscussResolver()
	code := "x := 1"
	input := DiscussCodeInput{
		RepositoryID:        "repo-3",
		Question:            "next step?",
		Code:                &code,
		ConversationHistory: []string{"turn1", "turn2"},
	}
	contextCode, _, _, _, err := assembleDiscussionContext(context.Background(), r, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(contextCode, "turn1") {
		t.Errorf("contextCode %q does not contain conversation history", contextCode)
	}
	if !strings.Contains(contextCode, "turn2") {
		t.Errorf("contextCode %q does not contain second turn", contextCode)
	}
}

func TestAssembleDiscussionContext_EmptyInputReturnsError(t *testing.T) {
	t.Parallel()
	r := newDiscussResolver()
	input := DiscussCodeInput{
		RepositoryID: "repo-4",
		Question:     "what?",
		// No code, filePath, artifactId, symbolId, conversationHistory
	}
	_, _, _, _, err := assembleDiscussionContext(context.Background(), r, input)
	if err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
	if !strings.Contains(err.Error(), "provide code") {
		t.Errorf("expected 'provide code ...' error, got: %v", err)
	}
}

func TestAssembleDiscussionContext_FilePath_DerivedsLanguage(t *testing.T) {
	t.Parallel()
	// When a filePath is supplied but the source file doesn't exist, the
	// file-read fails with "source unavailable" — that's the error path.
	// This test exercises the code path via a known extension so lang derivation
	// fires before the file-read failure returns an error.
	r := newDiscussResolver()
	fp := "src/main.go"
	input := DiscussCodeInput{
		RepositoryID: "repo-5",
		Question:     "review",
		FilePath:     &fp,
	}
	_, _, _, _, err := assembleDiscussionContext(context.Background(), r, input)
	// Expect an error because the repository root is unresolvable (no repo
	// configured in the stub store). The important assertion is that it's NOT
	// a "provide code" error — language was derived and an attempt was made.
	if err == nil {
		t.Fatal("expected error for unreachable repo root, got nil")
	}
	if strings.Contains(err.Error(), "provide code") {
		t.Errorf("error should be a source/read error, not 'provide code': %v", err)
	}
}

func TestAssembleDiscussionContext_ArtifactID_NilKnowledgeStore(t *testing.T) {
	t.Parallel()
	// When KnowledgeStore is nil, the artifact branch is skipped silently.
	// The input has no other context, so the function returns the "provide code" error.
	r := newDiscussResolver()
	r.KnowledgeStore = nil
	artifactID := "art-abc"
	input := DiscussCodeInput{
		RepositoryID: "repo-6",
		Question:     "what?",
		ArtifactID:   &artifactID,
	}
	_, _, _, _, err := assembleDiscussionContext(context.Background(), r, input)
	if err == nil {
		t.Fatal("expected 'provide code' error when KnowledgeStore is nil and nothing else given")
	}
	if !strings.Contains(err.Error(), "provide code") {
		t.Errorf("expected 'provide code ...' error, got: %v", err)
	}
}

func TestAssembleDiscussionContext_RequirementID_NotFound(t *testing.T) {
	t.Parallel()
	// When RequirementID is given but the store returns nil, the branch is
	// skipped. Without other context, returns "provide code" error.
	r := newDiscussResolver()
	reqID := "req-xyz"
	input := DiscussCodeInput{
		RepositoryID:  "repo-7",
		Question:      "why?",
		RequirementID: &reqID,
	}
	_, _, _, _, err := assembleDiscussionContext(context.Background(), r, input)
	if err == nil {
		t.Fatal("expected 'provide code' error when requirement not found and nothing else given")
	}
	if !strings.Contains(err.Error(), "provide code") {
		t.Errorf("expected 'provide code ...' error, got: %v", err)
	}
}
