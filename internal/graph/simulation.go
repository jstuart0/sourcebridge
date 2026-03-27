// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graph

import (
	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
)

// BuildSimulatedChanges converts resolved symbols from the worker into
// ImpactFileDiff and ImpactSymbolChange slices suitable for ComputeImpact().
// All resolved symbols are treated as "modified".
func BuildSimulatedChanges(resolved []*reasoningv1.SimulatedSymbolMatch) ([]ImpactFileDiff, []ImpactSymbolChange) {
	fileSet := make(map[string]bool)
	var symbolChanges []ImpactSymbolChange

	for _, r := range resolved {
		symbolChanges = append(symbolChanges, ImpactSymbolChange{
			SymbolID:   r.SymbolId,
			Name:       r.Name,
			Kind:       r.Kind,
			FilePath:   r.FilePath,
			ChangeType: "modified",
		})
		fileSet[r.FilePath] = true
	}

	var fileDiffs []ImpactFileDiff
	for path := range fileSet {
		fileDiffs = append(fileDiffs, ImpactFileDiff{
			Path:   path,
			Status: "modified",
		})
	}

	return fileDiffs, symbolChanges
}

// ExpandViaCallGraph adds direct callers of resolved symbols to the change set.
// This simulates the "ripple effect" where modifying a function signature
// forces changes in its callers. maxDepth caps the traversal depth (recommended: 2).
func ExpandViaCallGraph(store GraphStore, resolved []*reasoningv1.SimulatedSymbolMatch, maxDepth int) []ImpactSymbolChange {
	if maxDepth > 4 {
		maxDepth = 4
	}

	seen := make(map[string]bool)
	var expanded []ImpactSymbolChange

	for _, r := range resolved {
		seen[r.SymbolId] = true
		expanded = append(expanded, ImpactSymbolChange{
			SymbolID:   r.SymbolId,
			Name:       r.Name,
			Kind:       r.Kind,
			FilePath:   r.FilePath,
			ChangeType: "modified",
		})
	}

	// Walk callers up to maxDepth
	frontier := make([]string, 0, len(resolved))
	for _, r := range resolved {
		frontier = append(frontier, r.SymbolId)
	}

	for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, symID := range frontier {
			callers := store.GetCallers(symID)
			for _, callerID := range callers {
				if seen[callerID] {
					continue
				}
				seen[callerID] = true
				sym := store.GetSymbol(callerID)
				if sym != nil {
					expanded = append(expanded, ImpactSymbolChange{
						SymbolID:   sym.ID,
						Name:       sym.Name,
						Kind:       sym.Kind,
						FilePath:   sym.FilePath,
						ChangeType: "modified",
					})
					next = append(next, callerID)
				}
			}
		}
		frontier = next
	}

	return expanded
}
