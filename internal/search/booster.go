// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package search

import (
	"context"
	"sync"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// Booster is a post-fusion score adjuster. Each booster is
// self-disabling when its signal is absent — a repo with no call-graph
// still runs GraphBooster, but it contributes zero.
type Booster interface {
	Name() string
	Apply(ctx context.Context, results []*Result, req *Request, routed RouterOutput)
}

// -----------------------------------------------------------------------------
// Graph proximity booster
// -----------------------------------------------------------------------------

// GraphBooster lifts results whose symbol is a caller/callee of the
// resolved seed symbol. Only runs when the query has a strong exact
// or structural seed. Signal is additive:
//
//	boost = GraphWeight / (1 + hop_count)
//
// hop_count is 1 for direct neighbors; we don't walk further in V1.
type GraphBooster struct {
	Store       graph.GraphStore
	GraphWeight float64 // default 0.10 if zero
}

func (*GraphBooster) Name() string { return "graph" }

func (b *GraphBooster) Apply(ctx context.Context, results []*Result, req *Request, routed RouterOutput) {
	if b == nil || b.Store == nil || len(results) == 0 {
		return
	}
	weight := b.GraphWeight
	if weight <= 0 {
		weight = 0.10
	}
	// Choose a seed: explicit structural seed wins; otherwise use the
	// top exact-hit result, provided one exists and has the exact
	// signal set.
	seedID := ""
	if routed.Seed != "" {
		if sym := resolveSeed(ctx, b.Store, req.Repo, routed.Seed); sym != nil {
			seedID = sym.ID
		}
	}
	if seedID == "" {
		for _, r := range results {
			if r.Signals.Exact >= 0.9 {
				seedID = r.EntityID
				break
			}
		}
	}
	if seedID == "" {
		return
	}
	neighbors := make(map[string]bool)
	for _, id := range b.Store.GetCallers(ctx, seedID) {
		neighbors[id] = true
	}
	for _, id := range b.Store.GetCallees(ctx, seedID) {
		neighbors[id] = true
	}
	if len(neighbors) == 0 {
		return
	}
	for _, r := range results {
		if r.EntityID == seedID || !neighbors[r.EntityID] {
			continue
		}
		lift := weight / 2.0 // hop = 1
		r.Score += lift
		if lift > r.Signals.Graph {
			r.Signals.Graph = lift
		}
	}
}

// -----------------------------------------------------------------------------
// Requirement booster — Phase 4
// -----------------------------------------------------------------------------

// RequirementBooster lifts symbols that have one or more requirement
// links. The cache maps repo_id → symbol_id → max(link.confidence) and
// is O(1) to consult on the hot path.
//
// If the repo has zero links the booster is a total no-op (plan §4).
type RequirementBooster struct {
	Store  graph.GraphStore
	Weight float64 // default 0.15 if zero

	mu    sync.RWMutex
	cache map[string]map[string]float64 // repo_id -> symbol_id -> confidence
	warm  map[string]bool               // repos we've prewarmed
}

func (*RequirementBooster) Name() string { return "requirement" }

// Invalidate drops the cached link map for a repo. Should be called
// after bulk link writes to force the next Apply() to rehydrate.
func (b *RequirementBooster) Invalidate(repoID string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.cache, repoID)
	delete(b.warm, repoID)
}

func (b *RequirementBooster) Apply(ctx context.Context, results []*Result, req *Request, _ RouterOutput) {
	if b == nil || b.Store == nil || len(results) == 0 {
		return
	}
	weight := b.Weight
	if weight <= 0 {
		weight = 0.15
	}

	linkMap := b.linksFor(ctx, req.Repo)
	if len(linkMap) == 0 {
		return // no-op on zero-link repos
	}
	for _, r := range results {
		conf, ok := linkMap[r.EntityID]
		if !ok {
			continue
		}
		lift := conf * weight
		r.Score += lift
		if lift > r.Signals.Requirement {
			r.Signals.Requirement = lift
		}
	}
}

// linksFor returns (repo_id → symbol_id → max confidence). Cached so
// the hot path is a single map read.
func (b *RequirementBooster) linksFor(ctx context.Context, repoID string) map[string]float64 {
	b.mu.RLock()
	if b.cache != nil {
		if m, ok := b.cache[repoID]; ok {
			b.mu.RUnlock()
			return m
		}
	}
	b.mu.RUnlock()

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cache == nil {
		b.cache = make(map[string]map[string]float64)
		b.warm = make(map[string]bool)
	}
	if m, ok := b.cache[repoID]; ok {
		return m
	}
	links := b.Store.GetLinksForRepo(ctx, repoID)
	m := make(map[string]float64, len(links))
	for _, l := range links {
		if l == nil || l.Rejected {
			continue
		}
		if l.Confidence > m[l.SymbolID] {
			m[l.SymbolID] = l.Confidence
		}
	}
	b.cache[repoID] = m
	b.warm[repoID] = true
	return m
}
