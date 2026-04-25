// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package templates defines the common [Template] interface and port types
// for the A2 auto-extracted report templates (Glossary, Activity log, ADRs).
//
// Each template is a self-contained sub-package that implements [Template].
// A single registry can dispatch to all of them via the shared interface.
//
// # Port types
//
// [SymbolGraph], [GitLog], and [LLMCaller] are ports — narrow interfaces that
// decouple the templates from the concrete infrastructure that supplies data.
// No concrete implementations live here; callers inject them. Tests use the
// stub fakes in each template's _test.go file.
//
// # AST output
//
// Every template produces an [ast.Page] rather than raw markdown. The page is
// then rendered by the caller's choice of sink adapter (markdown_writer,
// confluence_writer, etc.).
//
// # Quality validation
//
// Each template applies its Q.2 validator profile. The generated [ast.Page] is
// accompanied by a [quality.ValidationResult] that the caller can attach to the
// PR description.
package templates

import (
	"context"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/quality"
)

// Template is the common interface every A2 report template implements.
// Implementations must be safe for concurrent use once constructed.
type Template interface {
	// ID returns the stable string identifier for this template.
	// One of: "glossary", "activity_log", "adr".
	ID() string

	// Generate produces a wiki page from the provided input.
	// The returned [ast.Page] is fully formed; the caller renders it with the
	// appropriate sink adapter.
	//
	// Generate may invoke LLM via input.LLM when the template requires it
	// (A2.3 ADRs; A2.2 activity-log digest when Config.EnableLLMDigest is set).
	// When input.LLM is nil and the template requires it, Generate returns an
	// error rather than silently skipping the LLM-dependent content.
	//
	// The caller is responsible for running quality.Run against the result when
	// it wants validation; Generate itself does not gate on quality checks so
	// the retry loop in A1.P1 can control the number of attempts.
	Generate(ctx context.Context, input GenerateInput) (ast.Page, error)
}

// GenerateInput carries all data a template needs to produce one page.
// Fields that are not needed by a particular template are ignored; callers
// should populate everything they have and let templates pick what they need.
type GenerateInput struct {
	// RepoID is the opaque repository identifier used in citations and page IDs.
	RepoID string

	// Audience controls which voice profile and validator profile to use.
	// Must match a quality.Audience constant.
	Audience quality.Audience

	// SymbolGraph provides exported symbol data for the Glossary template.
	// May be nil for templates that do not use the symbol graph.
	SymbolGraph SymbolGraph

	// GitLog provides commit history for the Activity log and ADR templates.
	// May be nil for templates that do not use git history.
	GitLog GitLog

	// LLM is the caller-injected LLM interface. May be nil for zero-LLM
	// templates (Glossary) and for Activity log when EnableLLMDigest is false.
	// ADR template requires a non-nil LLM.
	LLM LLMCaller

	// Now is the wall-clock time used for page provenance. Callers should set
	// this to time.Now(); tests can set a fixed value for determinism.
	Now time.Time

	// Config carries template-specific configuration flags.
	Config TemplateConfig
}

// TemplateConfig holds optional configuration that some templates honour.
type TemplateConfig struct {
	// EnableLLMDigest enables the optional weekly LLM summarisation pass in
	// the Activity log template. When false the structural log is still
	// generated but the 2-paragraph digest is omitted.
	EnableLLMDigest bool
}

// SymbolGraph is the port through which templates access exported symbol data.
// The concrete implementation is supplied by the caller; the interface is
// narrow by design so the template tests can use simple stubs.
type SymbolGraph interface {
	// ExportedSymbols returns all exported symbols for the given repo.
	// Symbols are returned in an unspecified order; callers sort as needed.
	ExportedSymbols(repoID string) ([]Symbol, error)
}

// Symbol is one exported identifier from the symbol graph.
type Symbol struct {
	// Package is the fully-qualified import path (e.g. "internal/auth").
	Package string

	// Name is the unqualified exported identifier (e.g. "Middleware").
	Name string

	// Signature is the full declaration as it appears in source
	// (e.g. "func Middleware(next http.Handler) http.Handler").
	Signature string

	// DocComment is the Go/Python/etc. doc comment for this symbol,
	// stripped of comment markers but preserving paragraph breaks.
	DocComment string

	// FilePath is the repo-relative file path where the symbol is declared.
	FilePath string

	// StartLine is the 1-based line number of the declaration.
	StartLine int

	// EndLine is the 1-based last line of the declaration.
	EndLine int
}

// GitLog is the port through which templates access indexed commit history.
type GitLog interface {
	// Commits returns commits for the given repo in reverse-chronological order.
	// The returned slice may be limited by the implementation; callers should not
	// assume all-time history is always available.
	Commits(repoID string) ([]Commit, error)
}

// Commit is one indexed git commit.
type Commit struct {
	// SHA is the full 40-character git commit SHA.
	SHA string

	// ShortSHA is the first 7 characters of SHA, for display.
	ShortSHA string

	// Author is the commit author name.
	Author string

	// AuthorEmail is the commit author email.
	AuthorEmail string

	// Message is the full commit message (subject + body).
	Message string

	// Subject is the first line of the commit message.
	Subject string

	// Body is everything after the first blank line in the commit message.
	// Empty when the commit has only a subject line.
	Body string

	// Timestamp is the author date of the commit (UTC).
	Timestamp time.Time

	// FilesChanged is the number of files modified by this commit.
	FilesChanged int

	// Insertions is the number of added lines.
	Insertions int

	// Deletions is the number of deleted lines.
	Deletions int

	// TouchedPaths is the list of repo-relative paths that this commit touched.
	TouchedPaths []string
}

// LLMCaller is the port through which templates invoke LLM inference.
// Implementations may be backed by the Python worker gRPC endpoint, a
// direct API client, or a test stub.
type LLMCaller interface {
	// Complete sends prompt to the LLM and returns the generated text.
	// systemPrompt is inserted as the system message; userPrompt as the user
	// turn. The implementation controls model selection and retry behaviour.
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}
