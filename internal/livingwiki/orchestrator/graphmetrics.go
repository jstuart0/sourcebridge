// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator

import (
	"context"
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// GraphStoreMetrics is a [GraphMetricsProvider] that queries a [graph.GraphStore]
// for real reference and relation counts.
//
// # Page-ID to graph-subject mapping
//
// Architecture page IDs are derived from package paths by the TaxonomyResolver:
//
//	archPageID("test-repo", "internal/auth") → "test-repo.arch.internal.auth"
//
// To reverse this: strip the repoID prefix and the ".arch." infix, then
// replace dots with slashes to recover the package path.
//
// For non-architecture pages (api_reference, system_overview, glossary), the
// page subject is the whole repository — counts aggregate across all packages.
//
// # PageReferenceCount
//
// Returns the number of distinct packages in the graph that import at least
// one symbol from the page's subject package.  Concretely: fetch all symbols
// in the subject package, sum up the unique caller-package set for each.
//
// # GraphRelationCount
//
// Returns the total number of call-graph edges (caller → callee) where the
// callee symbol lives in the subject package.  This is the raw inbound edge
// count, not deduplicated by package.
type GraphStoreMetrics struct {
	store graph.GraphStore
}

// NewGraphStoreMetrics creates a [GraphStoreMetrics] backed by the given store.
// store must be non-nil.
func NewGraphStoreMetrics(store graph.GraphStore) *GraphStoreMetrics {
	return &GraphStoreMetrics{store: store}
}

// Compile-time interface check.
var _ GraphMetricsProvider = (*GraphStoreMetrics)(nil)

// PageReferenceCount returns the number of distinct caller packages that
// import the subject package of pageID.
func (m *GraphStoreMetrics) PageReferenceCount(repoID, pageID string) int {
	pkg := pageSubject(repoID, pageID)
	if pkg == "" {
		// Non-architecture page — aggregate across all packages in the repo.
		return m.repoReferenceCount(repoID)
	}
	return m.packageReferenceCount(repoID, pkg)
}

// GraphRelationCount returns the total number of inbound call-graph edges
// to any symbol in the subject package of pageID.
func (m *GraphStoreMetrics) GraphRelationCount(repoID, pageID string) int {
	pkg := pageSubject(repoID, pageID)
	if pkg == "" {
		return m.repoRelationCount(repoID)
	}
	return m.packageRelationCount(repoID, pkg)
}

// packageReferenceCount counts distinct caller packages importing any symbol
// in pkg within the given repository.
//
// CA-171: replaced inner GetSymbol-per-callerID with one GetSymbolsByIDs batch
// to eliminate the SurrealDB N+1 that stalled Living Wiki page generation.
// The outer GetCallers-per-symbol N+1 remains; that requires a new store
// helper and is tracked separately.
func (m *GraphStoreMetrics) packageReferenceCount(repoID, pkg string) int {
	syms, _ := m.store.GetSymbols(repoID, nil, nil, 0, 0)
	callerIDSet := make(map[string]struct{})
	for _, sym := range syms {
		if sym.FilePath == "" {
			continue
		}
		if !symbolInPackage(sym.FilePath, pkg) {
			continue
		}
		for _, callerID := range m.store.GetCallers(sym.ID) {
			callerIDSet[callerID] = struct{}{}
		}
	}
	if len(callerIDSet) == 0 {
		return 0
	}
	callerIDs := make([]string, 0, len(callerIDSet))
	for id := range callerIDSet {
		callerIDs = append(callerIDs, id)
	}
	callers := m.store.GetSymbolsByIDs(callerIDs)
	callerPkgs := make(map[string]bool)
	for _, caller := range callers {
		if caller == nil {
			continue
		}
		callerPkg := filePackage(caller.FilePath)
		if callerPkg != pkg {
			callerPkgs[callerPkg] = true
		}
	}
	return len(callerPkgs)
}

// packageRelationCount counts total inbound call-graph edges to symbols in pkg.
func (m *GraphStoreMetrics) packageRelationCount(repoID, pkg string) int {
	syms, _ := m.store.GetSymbols(repoID, nil, nil, 0, 0)
	total := 0
	for _, sym := range syms {
		if !symbolInPackage(sym.FilePath, pkg) {
			continue
		}
		total += len(m.store.GetCallers(sym.ID))
	}
	return total
}

// repoReferenceCount aggregates reference counts across all packages in the repo.
//
// CA-171: same N+1 fix as packageReferenceCount — batch-fetch callers once
// instead of one GetSymbol per callerID.
func (m *GraphStoreMetrics) repoReferenceCount(repoID string) int {
	syms, _ := m.store.GetSymbols(repoID, nil, nil, 0, 0)
	callerIDSet := make(map[string]struct{})
	for _, sym := range syms {
		for _, callerID := range m.store.GetCallers(sym.ID) {
			callerIDSet[callerID] = struct{}{}
		}
	}
	if len(callerIDSet) == 0 {
		return 0
	}
	callerIDs := make([]string, 0, len(callerIDSet))
	for id := range callerIDSet {
		callerIDs = append(callerIDs, id)
	}
	callers := m.store.GetSymbolsByIDs(callerIDs)
	callerPkgs := make(map[string]bool)
	for _, caller := range callers {
		if caller == nil {
			continue
		}
		callerPkgs[filePackage(caller.FilePath)] = true
	}
	return len(callerPkgs)
}

// repoRelationCount counts all inbound call edges for the repo.
func (m *GraphStoreMetrics) repoRelationCount(repoID string) int {
	syms, _ := m.store.GetSymbols(repoID, nil, nil, 0, 0)
	total := 0
	for _, sym := range syms {
		total += len(m.store.GetCallers(sym.ID))
	}
	return total
}

// pageSubject extracts the package path from an architecture page ID.
// Returns "" for non-architecture pages (api_reference, glossary, system_overview).
//
// Format: "<repoID>.arch.<pkg-dots>" where pkg-dots uses dots in place of slashes.
// Example: "test-repo.arch.internal.auth" → "internal/auth"
func pageSubject(repoID, pageID string) string {
	// Strip repoID prefix + ".arch."
	archInfix := ".arch."
	var suffix string
	if repoID != "" {
		prefix := repoID + archInfix
		if !strings.HasPrefix(pageID, prefix) {
			return ""
		}
		suffix = pageID[len(prefix):]
	} else {
		prefix := "arch."
		if !strings.HasPrefix(pageID, prefix) {
			return ""
		}
		suffix = pageID[len(prefix):]
	}
	// Replace dots with slashes to recover the package path.
	return strings.ReplaceAll(suffix, ".", "/")
}

// symbolInPackage reports whether a symbol at filePath belongs to pkg.
// It checks whether filePath starts with pkg+"/" or equals pkg.
func symbolInPackage(filePath, pkg string) bool {
	return filePath == pkg ||
		strings.HasPrefix(filePath, pkg+"/") ||
		strings.HasPrefix(filePath, "./"+pkg+"/")
}

// filePackage extracts the package directory from a file path.
// "internal/auth/auth.go" → "internal/auth"
func filePackage(filePath string) string {
	idx := strings.LastIndex(filePath, "/")
	if idx < 0 {
		return filePath
	}
	return filePath[:idx]
}

// GraphStoreMetricsWithContext wraps [GraphStoreMetrics] to satisfy
// a hypothetical context-aware interface. Currently the GraphStore
// does not accept contexts; this is a forward-compatibility wrapper
// that can be removed when the store is updated.
//
// The unused ctx parameter is accepted to document intent.
func (m *GraphStoreMetrics) PageReferenceCountCtx(_ context.Context, repoID, pageID string) int {
	return m.PageReferenceCount(repoID, pageID)
}

func (m *GraphStoreMetrics) GraphRelationCountCtx(_ context.Context, repoID, pageID string) int {
	return m.GraphRelationCount(repoID, pageID)
}
