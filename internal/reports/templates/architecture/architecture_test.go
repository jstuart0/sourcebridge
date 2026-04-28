// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package architecture_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates/architecture"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test doubles
// ─────────────────────────────────────────────────────────────────────────────

// stubSymbolGraph returns a fixed slice of symbols.
type stubSymbolGraph struct {
	syms []templates.Symbol
}

func (s *stubSymbolGraph) ExportedSymbols(_ string) ([]templates.Symbol, error) {
	return s.syms, nil
}

// capturingLLM records the prompts it receives and returns a fixed response.
type capturingLLM struct {
	systemPrompt string
	userPrompt   string
	response     string
}

func (l *capturingLLM) Complete(_ context.Context, sys, user string) (string, error) {
	l.systemPrompt = sys
	l.userPrompt = user
	return l.response, nil
}

// minimalLLMResponse is a minimal six-section response that passes the renderer.
const minimalLLMResponse = `## Overview
Package src/db opens and manages database connections.

## Key types
No exported types.

## Public API
Connect opens a connection (src/db/conn.go:10-25).

## Dependencies
No dependencies.

## Used by
No callers.

## Code example
` + "```go" + `
// src/db/conn.go:10-25
func Connect(dsn string) error {
    return nil
}
` + "```"

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

// TestGeneratePackagePage_BodyInPrompt asserts that when symbols have a Body
// field populated, the user prompt contains the fenced source excerpt and the
// full doc comment (not just a one-line summary).
func TestGeneratePackagePage_BodyInPrompt(t *testing.T) {
	t.Parallel()

	llm := &capturingLLM{response: minimalLLMResponse}
	graph := &stubSymbolGraph{
		syms: []templates.Symbol{
			{
				Package:    "src/db",
				Name:       "Connect",
				Signature:  "func Connect(dsn string) error",
				DocComment: "Connect opens a database connection using the provided DSN.\nIt validates the connection before returning.",
				FilePath:   "src/db/conn.go",
				StartLine:  10,
				EndLine:    25,
				Body:       "func Connect(dsn string) error {\n\treturn db.Open(dsn)\n}",
			},
		},
	}

	tmpl := architecture.New()
	input := templates.GenerateInput{
		RepoID:      "test-repo",
		Audience:    quality.AudienceEngineers,
		SymbolGraph: graph,
		LLM:         llm,
		Now:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	page, err := tmpl.GeneratePackagePage(context.Background(), input, architecture.PackageInfo{
		Package: "src/db",
	})
	if err != nil {
		t.Fatalf("GeneratePackagePage: %v", err)
	}
	if page.ID == "" {
		t.Error("expected non-empty page ID")
	}

	// The user prompt must contain:
	// 1. The fenced source body.
	if !strings.Contains(llm.userPrompt, "```go") {
		t.Error("user prompt should contain a fenced Go code block with the source body")
	}
	if !strings.Contains(llm.userPrompt, "func Connect(dsn string) error") {
		t.Error("user prompt should contain the Connect function body")
	}
	// 2. The full doc comment, not just a one-line summary.
	if !strings.Contains(llm.userPrompt, "validates the connection before returning") {
		t.Error("user prompt should contain the full doc comment, not just the first line")
	}
	// 3. The file/line citation.
	if !strings.Contains(llm.userPrompt, "src/db/conn.go:10-25") {
		t.Error("user prompt should contain the file:line-line citation")
	}
}

// TestGeneratePackagePage_EmptyPackage asserts that a package with no exported
// symbols produces a non-empty page (the placeholder) without calling the LLM.
func TestGeneratePackagePage_EmptyPackage(t *testing.T) {
	t.Parallel()

	llm := &capturingLLM{response: minimalLLMResponse}
	graph := &stubSymbolGraph{syms: nil}

	tmpl := architecture.New()
	input := templates.GenerateInput{
		RepoID:      "test-repo",
		Audience:    quality.AudienceEngineers,
		SymbolGraph: graph,
		LLM:         llm,
		Now:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	page, err := tmpl.GeneratePackagePage(context.Background(), input, architecture.PackageInfo{
		Package: "src/db",
	})
	if err != nil {
		t.Fatalf("GeneratePackagePage (empty package): %v", err)
	}

	// LLM must not have been called.
	if llm.userPrompt != "" {
		t.Error("LLM should not have been called for an empty package")
	}

	// The page must have at least two blocks: a heading and a paragraph.
	if len(page.Blocks) < 2 {
		t.Errorf("expected at least 2 blocks, got %d", len(page.Blocks))
	}

	// The page must contain a heading block.
	hasHeading := false
	for _, b := range page.Blocks {
		if b.Kind == ast.BlockKindHeading {
			hasHeading = true
			break
		}
	}
	if !hasHeading {
		t.Error("expected at least one heading block in the empty-package page")
	}
}

// TestGeneratePackagePage_PackageDoc asserts that when a symbol's DocComment
// starts with "Package ", it is emitted as package-level documentation in the
// prompt.
func TestGeneratePackagePage_PackageDoc(t *testing.T) {
	t.Parallel()

	llm := &capturingLLM{response: minimalLLMResponse}
	graph := &stubSymbolGraph{
		syms: []templates.Symbol{
			{
				Package:    "internal/auth",
				Name:       "Middleware",
				Signature:  "func Middleware(next http.Handler) http.Handler",
				DocComment: "Package auth provides HTTP middleware for JWT authentication.\n\nMiddleware wraps a handler to verify the bearer token.",
				FilePath:   "internal/auth/middleware.go",
				StartLine:  5,
				EndLine:    30,
				Body:       "func Middleware(next http.Handler) http.Handler {\n\treturn http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {\n\t\t// verify token\n\t\tnext.ServeHTTP(w, r)\n\t})\n}",
			},
		},
	}

	tmpl := architecture.New()
	input := templates.GenerateInput{
		RepoID:      "test-repo",
		Audience:    quality.AudienceEngineers,
		SymbolGraph: graph,
		LLM:         llm,
		Now:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	_, err := tmpl.GeneratePackagePage(context.Background(), input, architecture.PackageInfo{
		Package: "internal/auth",
	})
	if err != nil {
		t.Fatalf("GeneratePackagePage: %v", err)
	}

	// The prompt should contain the package-level doc section.
	if !strings.Contains(llm.userPrompt, "Package documentation:") {
		t.Error("user prompt should contain 'Package documentation:' header")
	}
	if !strings.Contains(llm.userPrompt, "provides HTTP middleware for JWT authentication") {
		t.Error("user prompt should contain the package-level doc text")
	}
}
