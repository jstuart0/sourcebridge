// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// GQL-5 (bob A-H4): buildColdStartRunner and coldStartConfig have moved to
// internal/livingwiki/coldstart/ (exported as BuildRunner and Config).
//
// This file retains package-private aliases so the extensive test suite in
// internal/api/graphql/ continues to compile and pass without modification,
// and so that callers in schema.resolvers.go and living_wiki_plan_preview.go
// don't require mass updates in this slice.
//
// New code MUST use coldstart.BuildRunner(coldstart.Config{...}) directly.

package graphql

import (
	"context"
	"sync"

	"github.com/sourcebridge/sourcebridge/internal/clustering"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/coldstart"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/credentials"
	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/sinks"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
	"github.com/sourcebridge/sourcebridge/internal/worker/llmcall"
)

const (
	// GenerationMode values for Living Wiki cold-start jobs (CR12 Part B).
	// Re-exported here from the coldstart package so existing graphql-package
	// callers do not need updating.
	GenerationModeLWDetailed = coldstart.GenerationModeLWDetailed
	GenerationModeLWOverview = coldstart.GenerationModeLWOverview

	// coldStartTimeBudget is the shim alias for coldstart.ColdStartTimeBudget.
	// Retained for schema.resolvers.go's on-demand generation path.
	coldStartTimeBudget = coldstart.ColdStartTimeBudget
)

// coldStartConfig is a package-private type alias for coldstart.Config.
// Retained so the test suite (package graphql) can continue constructing
// configs with the familiar unexported name. Production call sites use
// coldstart.Config directly.
type coldStartConfig = coldstart.Config

// buildColdStartRunner is a package-private shim that delegates to
// coldstart.BuildRunner. Retained for backward compatibility with the
// extensive graphql test suite. Production call sites use
// coldstart.BuildRunner directly.
func buildColdStartRunner(cfg coldStartConfig) func(ctx context.Context, rt llm.Runtime) error {
	return coldstart.BuildRunner(cfg)
}

// newRegistryTierFunc is a package-private shim that delegates to
// coldstart.NewRegistryTierFunc. Callers in schema.resolvers.go use the
// same registry-backed tier resolution as the cold-start runner.
//
// Two production callers (codex r1c HIGH #3):
//   - GenerateLivingWikiPageOnDemand in schema.resolvers.go
//   - BuildRunner (now in coldstart package)
var newRegistryTierFunc = coldstart.NewRegistryTierFunc

// resolveTaxonomyForMode is a package-private shim delegating to
// coldstart.ResolveTaxonomyForMode. Used by schema.resolvers.go and
// living_wiki_plan_preview.go.
func resolveTaxonomyForMode(ctx context.Context, mode, repoID string, gs graphstore.GraphStore, lc *llmcall.Caller, cs clustering.ClusterStore) ([]lworch.PlannedPage, error) {
	return coldstart.ResolveTaxonomyForMode(ctx, mode, repoID, gs, lc, cs)
}

// graphStoreSymbolGraph shim — schema.resolvers.go constructs this with
// &graphStoreSymbolGraph{store: gs}; we preserve the field name to avoid
// changing that file in this slice. The underlying templates.SymbolGraph is
// lazy-initialized on first use (via coldstart.NewGraphStoreSymbolGraph) so
// the cache lives for the lifetime of the shim object, matching the original.
type graphStoreSymbolGraph struct {
	store graphstore.GraphStore
	once  sync.Once
	inner templates.SymbolGraph
}

func (g *graphStoreSymbolGraph) ExportedSymbols(repoID string) ([]templates.Symbol, error) {
	g.once.Do(func() { g.inner = coldstart.NewGraphStoreSymbolGraph(g.store) })
	return g.inner.ExportedSymbols(repoID)
}

// coldStartLLMCaller shim — schema.resolvers.go constructs this with field
// names {caller, repoID, op}; we preserve those to avoid changing that file.
// Delegates to coldstart.NewLLMCaller.
type coldStartLLMCaller struct {
	caller *llmcall.Caller
	repoID string
	op     string
}

func (c *coldStartLLMCaller) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	return coldstart.NewLLMCaller(c.caller, c.repoID, c.op).Complete(ctx, systemPrompt, userPrompt)
}

// dispatchGeneratedPages is a package-private shim delegating to
// coldstart.DispatchGeneratedPages. Used by schema.resolvers.go on-demand path.
func dispatchGeneratedPages(
	ctx context.Context,
	repoID, tenantID string,
	generatedPages []ast.Page,
	skippedPageIDs []string,
	broker credentials.Broker,
	repoSettingsStore livingwiki.RepoSettingsStore,
	repoName string,
	status *string,
	failCat *coldstart.FailureCategory,
	errMsg *string,
	mode string,
) []livingwiki.SinkWriteResult {
	return coldstart.DispatchGeneratedPages(ctx, repoID, tenantID, generatedPages, skippedPageIDs, broker, repoSettingsStore, repoName, status, failCat, errMsg, mode)
}

// ─────────────────────────────────────────────────────────────────────────────
// Test-visible shims for symbols that moved to the coldstart package.
// These are used by the graphql package test suite (package graphql).
// ─────────────────────────────────────────────────────────────────────────────

// atomicStringSlice is a local concurrency-safe string accumulator.
// Retained in the graphql package so tests that call .append()/.snapshot()
// can access the unexported methods (cross-package unexported methods are
// inaccessible even through type aliases). The coldstart package has its own
// AtomicStringSlice with exported methods for production use.
type atomicStringSlice struct {
	mu  sync.Mutex
	val []string
}

func (a *atomicStringSlice) append(s string) {
	a.mu.Lock()
	a.val = append(a.val, s)
	a.mu.Unlock()
}

func (a *atomicStringSlice) snapshot() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.val) == 0 {
		return nil
	}
	cp := make([]string, len(a.val))
	copy(cp, a.val)
	return cp
}

// Bucket constants used by graphql-package tests.
const (
	bucketSkipFully      = coldstart.BucketSkipFully
	bucketSkipNeedsFixup = coldstart.BucketSkipNeedsFixup
	bucketRegenerate     = coldstart.BucketRegenerate
)

// resolveTaxonomy is a package-private shim for tests.
func resolveTaxonomy(ctx context.Context, repoID string, gs graphstore.GraphStore, lc *llmcall.Caller, cs clustering.ClusterStore) ([]lworch.PlannedPage, error) {
	return coldstart.ResolveTaxonomy(ctx, repoID, gs, lc, cs)
}

// attachKnowledgeArtifacts is a package-private shim for tests.
func attachKnowledgeArtifacts(ctx context.Context, repoID string, pages []lworch.PlannedPage, ks knowledge.KnowledgeStore) []lworch.PlannedPage {
	return coldstart.AttachKnowledgeArtifacts(ctx, repoID, pages, ks)
}

// buildExclusionReasons is a package-private shim for tests.
func buildExclusionReasons(excluded []lworch.ExcludedPage) []string {
	return coldstart.BuildExclusionReasons(excluded)
}

// classifyPage is a package-private shim for tests.
func classifyPage(
	pageID string,
	alreadyPublished map[string]struct{},
	currentFp string,
	persistedFps map[string]map[string]livingwiki.PagePublishStatusRow,
	writers []sinks.NamedSinkWriter,
) string {
	return coldstart.ClassifyPage(pageID, alreadyPublished, currentFp, persistedFps, writers)
}

// applyPageCap and applyPageSelection delegate to coldstart exports.

func applyPageCap(
	pages []lworch.PlannedPage,
	maxPagesPerJob int,
	pageCountOverride *int,
	excludedOnlyRetry bool,
) (out []lworch.PlannedPage, capSource string, capValue int, effectiveCap int, preCap int) {
	return coldstart.ApplyPageCap(pages, maxPagesPerJob, pageCountOverride, excludedOnlyRetry)
}

func applyPageSelection(pages []lworch.PlannedPage, selectedIDs []string) []lworch.PlannedPage {
	return coldstart.ApplyPageSelection(pages, selectedIDs)
}
