// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graph

import "sort"

type ExecutionNode struct {
	SymbolID   string
	SymbolName string
	FilePath   string
	StartLine  int
	EndLine    int
	Observed   bool
	Reason     string
}

// TraceLikelyExecutionPath builds a conservative caller/current/callee chain for a symbol.
func TraceLikelyExecutionPath(store GraphStore, repoID, symbolID string, maxDepth int) []ExecutionNode {
	if maxDepth <= 0 {
		maxDepth = 6
	}

	allSymbols, _ := store.GetSymbols(repoID, nil, nil, 0, 0)
	byID := make(map[string]*StoredSymbol, len(allSymbols))
	fanOut := make(map[string]int)
	for _, sym := range allSymbols {
		byID[sym.ID] = sym
	}
	for _, edge := range store.GetCallEdges(repoID) {
		fanOut[edge.CallerID]++
	}

	current := byID[symbolID]
	if current == nil {
		return nil
	}

	steps := make([]ExecutionNode, 0, maxDepth+2)
	callerChain := traceCallerChain(store, byID, fanOut, current.ID, 2)
	for i := len(callerChain) - 1; i >= 0; i-- {
		steps = append(steps, callerChain[i])
	}
	steps = append(steps, executionNodeFromSymbol(current, true, "Selected code step"))

	visited := map[string]bool{current.ID: true}
	currentID := current.ID
	for depth := 0; depth < maxDepth; depth++ {
		nextID := selectPrimaryNeighbor(store.GetCallees(currentID), byID, fanOut, currentID, visited)
		if nextID == "" {
			break
		}
		next := byID[nextID]
		if next == nil {
			break
		}
		visited[nextID] = true
		steps = append(steps, executionNodeFromSymbol(next, true, "Called by the previous step"))
		currentID = nextID
	}

	return steps
}

func traceCallerChain(store GraphStore, byID map[string]*StoredSymbol, fanOut map[string]int, symbolID string, maxDepth int) []ExecutionNode {
	visited := map[string]bool{symbolID: true}
	var chain []ExecutionNode
	currentID := symbolID
	for depth := 0; depth < maxDepth; depth++ {
		prevID := selectPrimaryNeighbor(store.GetCallers(currentID), byID, fanOut, currentID, visited)
		if prevID == "" {
			break
		}
		prev := byID[prevID]
		if prev == nil {
			break
		}
		visited[prevID] = true
		chain = append(chain, executionNodeFromSymbol(prev, true, "Calls the next step"))
		currentID = prevID
	}
	return chain
}

func selectPrimaryNeighbor(candidateIDs []string, byID map[string]*StoredSymbol, fanOut map[string]int, currentID string, visited map[string]bool) string {
	type scored struct {
		id    string
		score int
		sym   *StoredSymbol
	}
	current := byID[currentID]
	var candidates []scored
	for _, id := range candidateIDs {
		if visited[id] {
			continue
		}
		sym := byID[id]
		if sym == nil || sym.IsTest {
			continue
		}
		score := 0
		if current != nil && sym.FilePath != current.FilePath {
			score += 2
		}
		if fanOut[id] > 0 {
			score += min(fanOut[id], 3)
		}
		if sym.DocComment != "" {
			score++
		}
		candidates = append(candidates, scored{id: id, score: score, sym: sym})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			if candidates[i].sym.FilePath == candidates[j].sym.FilePath {
				return candidates[i].sym.Name < candidates[j].sym.Name
			}
			return candidates[i].sym.FilePath < candidates[j].sym.FilePath
		}
		return candidates[i].score > candidates[j].score
	})
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0].id
}

func executionNodeFromSymbol(sym *StoredSymbol, observed bool, reason string) ExecutionNode {
	return ExecutionNode{
		SymbolID:   sym.ID,
		SymbolName: sym.Name,
		FilePath:   sym.FilePath,
		StartLine:  sym.StartLine,
		EndLine:    sym.EndLine,
		Observed:   observed,
		Reason:     reason,
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
