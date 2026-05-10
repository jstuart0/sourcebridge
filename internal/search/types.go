// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package search implements SourceBridge's hybrid retrieval backbone.
//
// One service — combining lexical (BM25), semantic (vector), symbol
// exact, and graph-structural signals — powers every downstream
// surface: MCP, REST, GraphQL, CLI, web UI, and deep-mode QA.
//
// The public entry point is Service.Search; each transport (MCP
// handler, GraphQL resolver, CLI) adapts the Result envelope to its
// own shape. The core service requires a single authorized repo scope
// per call; cross-repo behavior is an adapter-level fanout over
// already-authorized repos with second-stage RRF merge.
//
// See thoughts/shared/plans/2026-04-22-hybrid-retrieval-search.md for
// architecture, phases, evaluation protocol, and rollout rules.
package search

import (
	"time"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// QueryClass is the output of the rule-based router. It drives which
// adapters are invoked for a given query and, later, how their results
// are weighted.
type QueryClass string

const (
	ClassIdentifier QueryClass = "identifier"
	ClassPhrase     QueryClass = "phrase"
	ClassNaturalLng QueryClass = "natural_language"
	ClassStructural QueryClass = "structural"
	ClassMixed      QueryClass = "mixed"
)

// StructuralOp names the structural operator parsed out of a query
// like `calls:foo` / `callers:foo` / `impl:Interface`.
type StructuralOp string

const (
	OpCalls   StructuralOp = "calls"
	OpCallers StructuralOp = "callers"
	OpImpl    StructuralOp = "impl"
)

// Filters scopes a ranked search by coarse-grained attributes. Empty
// strings mean "no filter on this attribute". Corresponds to the
// operator modifiers `lang:go`, `kind:function`, `path:internal/**`
// in the plan.
type Filters struct {
	Kind     string
	Language string
	FilePath string
}

// Request is a single search invocation against a single repo scope.
// The repo scope is required — the authorization boundary is the
// caller/adapter, not the service. See plan §Data Consistency Rules.
type Request struct {
	// Repo is the already-authorized repo ID. Callers must validate
	// access before constructing the request.
	Repo string
	// Query is the raw user query. The router parses operators,
	// filters, and classes out of this string.
	Query string
	// Limit caps the final result list. 0 falls back to DefaultLimit.
	Limit int
	// Filters are additional attribute predicates applied by every
	// adapter. Filters parsed from the query string (lang:, kind:,
	// path:) are merged here by the router.
	Filters Filters
	// Mode is an optional hint consumed by some boosters / adapters.
	// "deep" is used by deep-QA and may expand graph neighbors more
	// aggressively in a later phase.
	Mode string
	// IncludeDebug populates Response.Debug. Off by default because
	// debug payloads are large and produced lazily by adapters.
	IncludeDebug bool
	// Now is injected for deterministic testing. Zero → time.Now().
	Now time.Time
}

// DefaultLimit is the default when Request.Limit == 0.
const DefaultLimit = 20

// MaxLimit is the cap regardless of what the caller asks for. Deep-QA
// tops out at 50 which is well under this ceiling (plan §Security).
const MaxLimit = 200

// MaxQueryLen is the per-request cap in UTF-8 bytes.
const MaxQueryLen = 512

// Signals carries the per-adapter score breakdown for a single result.
// Zero fields mean "this adapter did not contribute to this result".
// The structure is intentionally flat so it maps to a GraphQL object
// type without surprises.
type Signals struct {
	Exact       float64 `json:"exact"`
	Lexical     float64 `json:"lexical"`
	Semantic    float64 `json:"semantic"`
	Graph       float64 `json:"graph"`
	Requirement float64 `json:"requirement"`
}

// Fired returns the list of adapters that contributed a non-zero
// signal for a result. Used by the UI to render signal chips.
func (s Signals) Fired() []string {
	out := make([]string, 0, 5)
	if s.Exact > 0 {
		out = append(out, "exact")
	}
	if s.Lexical > 0 {
		out = append(out, "lexical")
	}
	if s.Semantic > 0 {
		out = append(out, "semantic")
	}
	if s.Graph > 0 {
		out = append(out, "graph")
	}
	if s.Requirement > 0 {
		out = append(out, "requirement")
	}
	return out
}

// StageTimings is per-stage latency instrumentation for one call. All
// values are in milliseconds. Unpopulated stages are zero.
type StageTimings struct {
	RouteMs  float64 `json:"route_ms"`
	ExactMs  float64 `json:"exact_ms"`
	FTSMs    float64 `json:"fts_ms"`
	EmbedMs  float64 `json:"embed_ms"`
	VectorMs float64 `json:"vector_ms"`
	GraphMs  float64 `json:"graph_ms"`
	FuseMs   float64 `json:"fuse_ms"`
	BoostMs  float64 `json:"boost_ms"`
	TotalMs  float64 `json:"total_ms"`
}

// DegradeFlags records whether a given backend was unavailable or
// failed during this request. UI renders these only in dev/admin mode
// (plan §Phase 5 — Degraded-state UX rules).
type DegradeFlags struct {
	LexicalUnavailable  bool `json:"lexical_unavailable"`
	SemanticUnavailable bool `json:"semantic_unavailable"`
	EmbedderUnavailable bool `json:"embedder_unavailable"`
	GraphUnavailable    bool `json:"graph_unavailable"`
	CircuitOpen         bool `json:"circuit_open"`
}

// Debug is the optional debug envelope returned when
// Request.IncludeDebug is true.
type Debug struct {
	Class    QueryClass     `json:"class"`
	Ops      []StructuralOp `json:"structural_ops,omitempty"`
	Timings  StageTimings   `json:"timings"`
	Degrade  DegradeFlags   `json:"degrade"`
	Adapters []string       `json:"adapters_invoked"`
	Extra    map[string]any `json:"extra,omitempty"`
}

// Result is one ranked entity. EntityType is currently "symbol" for
// the hybrid path; "requirement" and "file" remain on the lexical
// mixed path handled by the GraphQL adapter.
type Result struct {
	EntityType string  `json:"entity_type"` // "symbol" | "requirement" | "file"
	EntityID   string  `json:"entity_id"`
	Title      string  `json:"title"`
	Subtitle   string  `json:"subtitle"` // e.g. qualified name
	FilePath   string  `json:"file_path,omitempty"`
	Line       int     `json:"line,omitempty"`
	RepoID     string  `json:"repo_id"`
	Score      float64 `json:"score"`
	Signals    Signals `json:"signals"`
	// Symbol is populated for symbol results; nil for other entity
	// types. Adapters that project symbols into the GraphQL envelope
	// read this field instead of re-fetching.
	Symbol *graph.StoredSymbol `json:"-"`
}

// Response is the full envelope returned from Service.Search.
type Response struct {
	Results []*Result `json:"results"`
	Debug   *Debug    `json:"debug,omitempty"`
}

// Candidate is an adapter's contribution before fusion. It is scoped
// to an entity ID and carries the adapter's own rank + score.
type Candidate struct {
	EntityID   string
	EntityType string
	AdapterID  string
	Rank       int
	RawScore   float64
	// Populated opportunistically by the first adapter that hydrates
	// the symbol so we don't re-fetch the same row many times during
	// fusion.
	Symbol *graph.StoredSymbol
}

// AdapterResult is a list of candidates from one adapter.
type AdapterResult struct {
	AdapterID   string
	Candidates  []*Candidate
	DurationMs  float64
	Unavailable bool // true when the backend could not run at all
	Err         error
	// EmbedMs is populated by VectorAdapter only: wall time spent on the
	// query embedding call (gRPC GenerateEmbedding round-trip). Zero for
	// all other adapters. Used by collectTimings to populate StageTimings.EmbedMs.
	EmbedMs float64
}
