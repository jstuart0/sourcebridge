// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package search

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// MetricsSink is the observability hook. Keeping it to a single method
// avoids pulling the orchestrator metrics package into this one; the
// caller at wiring time can adapt the signatures.
type MetricsSink interface {
	Record(stage string, durationMs float64, ok bool)
}

// Service is the single entry point for hybrid retrieval. It owns the
// adapters and boosters; callers construct one per process (or one
// per store binding) and reuse it across requests.
type Service struct {
	Store graph.GraphStore

	// Required adapters: constructed by NewService. Exported so tests
	// can swap individual ones if needed.
	Exact  *ExactAdapter
	FTS    *FTSAdapter
	Vector *VectorAdapter
	Graph  *GraphAdapter

	// Optional — left nil when the deployment has no embedder wired.
	Embedder *CachedEmbedder

	// Boosters applied after fusion in registration order.
	Boosters []Booster

	// Tunables with safe zero-value defaults.
	ExactTimeout  time.Duration // default 30ms
	FTSTimeout    time.Duration // default 60ms
	VectorTimeout time.Duration // default 200ms
	GraphTimeout  time.Duration // default 40ms

	// Metrics sink. Optional.
	Metrics MetricsSink
}

// NewService wires a Service against a store. Embedder / boosters are
// optional and can be set by the caller before first Search.
func NewService(store graph.GraphStore) *Service {
	svc := &Service{Store: store}
	svc.Exact = &ExactAdapter{Store: store}
	svc.FTS = &FTSAdapter{Store: store}
	svc.Vector = &VectorAdapter{Store: store}
	svc.Graph = &GraphAdapter{Store: store}
	svc.Boosters = []Booster{
		&GraphBooster{Store: store},
	}
	return svc
}

// WithEmbedder plugs a CachedEmbedder into the vector adapter. The
// service auto-disables the vector arm when the embedder is nil.
func (s *Service) WithEmbedder(e *CachedEmbedder) *Service {
	s.Embedder = e
	s.Vector.Embedder = e
	return s
}

// WithRequirementBooster appends a requirement booster at the end of
// the booster chain. Convenience for wiring.
func (s *Service) WithRequirementBooster(b *RequirementBooster) *Service {
	s.Boosters = append(s.Boosters, b)
	return s
}

// Search runs the full hybrid pipeline and returns a Response.
//
// Invariants:
//   - req.Repo MUST be an already-authorized repo ID. The service does
//     not check access; that is the adapter's responsibility.
//   - The returned Response.Results are ordered fused_score DESC with
//     deterministic (repo, entity_type, entity_id) tie-break for
//     stable pagination.
func (s *Service) Search(ctx context.Context, req *Request) (*Response, error) {
	if req == nil {
		return nil, fmt.Errorf("nil request")
	}
	if req.Repo == "" {
		return nil, fmt.Errorf("repo scope required")
	}
	// Cap query length (plan §Security — resource-exhaustion guards).
	if len(req.Query) > MaxQueryLen {
		req.Query = req.Query[:MaxQueryLen]
	}

	start := time.Now()

	// (req.Now used to be canonicalised here for downstream date-filter
	// resolution; the current pipeline doesn't apply time-based filters
	// at this stage, so the conditional was dead. Leaving the field on
	// the request for callers that may want it later.)

	// --- Route ---
	tRouteStart := time.Now()
	routed := Classify(req.Query)
	routeMs := msSince(tRouteStart)

	// Merge caller-provided filters on top of router-parsed ones.
	if routed.Filters.Kind == "" && req.Filters.Kind != "" {
		routed.Filters.Kind = req.Filters.Kind
	}
	if routed.Filters.Language == "" && req.Filters.Language != "" {
		routed.Filters.Language = req.Filters.Language
	}
	if routed.Filters.FilePath == "" && req.Filters.FilePath != "" {
		routed.Filters.FilePath = req.Filters.FilePath
	}

	// --- Fan out ---
	adapterCalls := s.plan(routed)
	results := s.fanOut(ctx, adapterCalls, routed, req)

	// --- Fuse ---
	tFuseStart := time.Now()
	symIndex := make(map[string]*graph.StoredSymbol)
	for _, ar := range results {
		for _, c := range ar.Candidates {
			if c.Symbol != nil {
				symIndex[c.EntityID] = c.Symbol
			}
		}
	}
	hydrate := func(c *Candidate) *Result {
		sym := c.Symbol
		if sym == nil {
			sym = symIndex[c.EntityID]
		}
		if sym == nil {
			// Last resort — pull from the store.
			sym = s.Store.GetSymbol(ctx, c.EntityID)
		}
		if sym == nil {
			return nil
		}
		return &Result{
			EntityType: "symbol",
			EntityID:   sym.ID,
			Title:      sym.Name,
			Subtitle:   sym.QualifiedName,
			FilePath:   sym.FilePath,
			Line:       sym.StartLine,
			RepoID:     sym.RepoID,
			Symbol:     sym,
		}
	}
	fused := fuse(results, hydrate)
	fuseMs := msSince(tFuseStart)

	// --- Boost ---
	tBoostStart := time.Now()
	for _, b := range s.Boosters {
		b.Apply(ctx, fused, req, routed)
	}
	// Re-sort after boosters adjust scores. The sort key matches fuse
	// order so pagination stability is preserved.
	sortResultsScoreStable(fused)
	boostMs := msSince(tBoostStart)

	// --- Truncate ---
	limit := effLimit(req.Limit)
	if len(fused) > limit {
		fused = fused[:limit]
	}

	// --- Assemble debug ---
	var debug *Debug
	if req.IncludeDebug {
		debug = &Debug{
			Class:   routed.Class,
			Ops:     routed.Structural,
			Timings: s.collectTimings(routeMs, fuseMs, boostMs, start, results),
			Degrade: s.collectDegrade(results),
		}
		for _, ar := range results {
			debug.Adapters = append(debug.Adapters, ar.AdapterID)
		}
	}

	// --- Metrics ---
	if s.Metrics != nil {
		s.Metrics.Record("total", msSince(start), true)
	}

	return &Response{Results: fused, Debug: debug}, nil
}

// -----------------------------------------------------------------------------
// Internals
// -----------------------------------------------------------------------------

type plannedCall struct {
	adapter Adapter
	stage   string
	timeout time.Duration
}

func (s *Service) plan(routed RouterOutput) []plannedCall {
	var plan []plannedCall
	addIf := func(cond bool, a Adapter, stage string, tt time.Duration) {
		if cond && a != nil {
			plan = append(plan, plannedCall{adapter: a, stage: stage, timeout: tt})
		}
	}

	addIf(routed.WantExact, s.Exact, "exact", dur(s.ExactTimeout, 30*time.Millisecond))
	addIf(routed.WantLexical, s.FTS, "fts", dur(s.FTSTimeout, 60*time.Millisecond))
	addIf(routed.WantVector && s.Vector != nil && s.Vector.Embedder != nil, s.Vector, "vector", dur(s.VectorTimeout, 200*time.Millisecond))
	addIf(routed.WantGraph, s.Graph, "graph", dur(s.GraphTimeout, 40*time.Millisecond))

	// If router produced nothing (e.g. empty query), at least run
	// exact so the caller gets a deterministic empty response.
	if len(plan) == 0 {
		plan = append(plan, plannedCall{adapter: s.Exact, stage: "exact", timeout: dur(s.ExactTimeout, 30*time.Millisecond)})
	}
	return plan
}

func (s *Service) fanOut(ctx context.Context, plan []plannedCall, routed RouterOutput, req *Request) []AdapterResult {
	out := make([]AdapterResult, len(plan))
	var wg sync.WaitGroup
	for i, pc := range plan {
		wg.Add(1)
		go func(i int, pc plannedCall) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, pc.timeout)
			defer cancel()
			t := time.Now()
			res := pc.adapter.Search(cctx, routed, req)
			res.DurationMs = msSince(t)
			out[i] = res
			if s.Metrics != nil {
				s.Metrics.Record(pc.stage, res.DurationMs, !res.Unavailable && res.Err == nil)
			}
		}(i, pc)
	}
	wg.Wait()
	return out
}

func (s *Service) collectTimings(routeMs, fuseMs, boostMs float64, start time.Time, results []AdapterResult) StageTimings {
	t := StageTimings{
		RouteMs: routeMs,
		FuseMs:  fuseMs,
		BoostMs: boostMs,
		TotalMs: msSince(start),
	}
	for _, r := range results {
		switch r.AdapterID {
		case "exact":
			t.ExactMs = r.DurationMs
		case "fts":
			t.FTSMs = r.DurationMs
		case "vector":
			t.VectorMs = r.DurationMs
			t.EmbedMs = r.EmbedMs
		case "graph":
			t.GraphMs = r.DurationMs
		}
	}
	return t
}

func (s *Service) collectDegrade(results []AdapterResult) DegradeFlags {
	var d DegradeFlags
	for _, r := range results {
		if !r.Unavailable {
			continue
		}
		switch r.AdapterID {
		case "fts":
			d.LexicalUnavailable = true
		case "vector":
			d.SemanticUnavailable = true
		case "graph":
			d.GraphUnavailable = true
		}
	}
	if s.Embedder != nil && s.Embedder.CircuitOpen() {
		d.CircuitOpen = true
		d.EmbedderUnavailable = true
	}
	return d
}

func dur(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}

func msSince(t time.Time) float64 {
	return float64(time.Since(t).Nanoseconds()) / 1e6
}

func sortResultsScoreStable(rs []*Result) {
	// stable insertion sort on the already-mostly-sorted slice.
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && lessResult(rs[j], rs[j-1]); j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
}

func lessResult(a, b *Result) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	if a.RepoID != b.RepoID {
		return a.RepoID < b.RepoID
	}
	if a.EntityType != b.EntityType {
		return a.EntityType < b.EntityType
	}
	return a.EntityID < b.EntityID
}
