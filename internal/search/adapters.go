// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package search

import (
	"context"
	"strings"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// Adapter is a single retrieval backend contributing candidates to the
// fusion step. Adapters are expected to bound their own work with the
// passed context deadline; if the deadline expires they must return
// what they have (or nothing) rather than blocking.
type Adapter interface {
	Name() string
	Search(ctx context.Context, in RouterOutput, req *Request) AdapterResult
}

// -----------------------------------------------------------------------------
// Symbol-exact adapter
// -----------------------------------------------------------------------------

// ExactAdapter returns symbols whose name matches the query exactly
// (case-insensitive) or whose qualified_name suffix-matches. Always
// cheap, always runs, never skipped (plan §Symbol exact).
type ExactAdapter struct {
	Store graph.GraphStore
}

func (*ExactAdapter) Name() string { return "exact" }

func (a *ExactAdapter) Search(ctx context.Context, in RouterOutput, req *Request) AdapterResult {
	res := AdapterResult{AdapterID: "exact"}
	if a == nil || a.Store == nil || in.Cleaned == "" {
		res.Unavailable = true
		return res
	}
	// Pull a modest page — the substring match plus name equality
	// filtering below trims it to just the exact hits.
	qPtr := in.Cleaned
	kindPtr := kindFilter(in.Filters, req.Filters)
	syms, _ := a.Store.GetSymbols(ctx, req.Repo, &qPtr, kindPtr, 200, 0)

	lowerQ := strings.ToLower(in.Cleaned)
	for i, s := range syms {
		nameLower := strings.ToLower(s.Name)
		qualLower := strings.ToLower(s.QualifiedName)
		match := false
		score := 0.0
		switch {
		case nameLower == lowerQ:
			match, score = true, 1.0
		case strings.HasSuffix(qualLower, "."+lowerQ), strings.HasSuffix(qualLower, ":"+lowerQ):
			match, score = true, 0.8
		case strings.HasPrefix(nameLower, lowerQ):
			match, score = true, 0.6
		}
		if !match {
			continue
		}
		res.Candidates = append(res.Candidates, &Candidate{
			EntityID:   s.ID,
			EntityType: "symbol",
			AdapterID:  "exact",
			Rank:       i,
			RawScore:   score,
			Symbol:     s,
		})
	}
	// Re-rank locally so the strongest exact match is always rank 0.
	sortCandidatesByScore(res.Candidates)
	return res
}

// -----------------------------------------------------------------------------
// FTS (BM25) adapter
// -----------------------------------------------------------------------------

// FTSAdapter runs ranked full-text search. Prefers the FTSSymbolSearch
// capability interface; falls back to a scored substring pass over
// the in-memory store when the capability isn't available (OSS / CLI
// mode with the memory store, or pre-migration deployments).
type FTSAdapter struct {
	Store graph.GraphStore
}

func (*FTSAdapter) Name() string { return "fts" }

func (a *FTSAdapter) Search(ctx context.Context, in RouterOutput, req *Request) AdapterResult {
	res := AdapterResult{AdapterID: "fts"}
	if a == nil || a.Store == nil || in.Cleaned == "" {
		res.Unavailable = true
		return res
	}

	filters := graph.SymbolSearchFilters{
		Kind:     kindFilter(in.Filters, req.Filters),
		Language: langFilter(in.Filters, req.Filters),
		FilePath: pathFilter(in.Filters, req.Filters),
	}

	// Use FTS capability when available.
	if fts, ok := a.Store.(graph.FTSSymbolSearch); ok {
		ranked := fts.SearchSymbolsFTS(ctx, req.Repo, in.Cleaned, filters, effLimit(req.Limit))
		// Nil from the capability means "backend failed" — mark as
		// unavailable so the fuser ignores this adapter (consistent
		// with plan §Graceful degradation).
		if ranked == nil {
			// Fallback substring pass below.
			goto fallback
		}
		res.Candidates = make([]*Candidate, 0, len(ranked))
		for i, rs := range ranked {
			if rs == nil || rs.Symbol == nil {
				continue
			}
			res.Candidates = append(res.Candidates, &Candidate{
				EntityID:   rs.Symbol.ID,
				EntityType: "symbol",
				AdapterID:  "fts",
				Rank:       i,
				RawScore:   rs.Score,
				Symbol:     rs.Symbol,
			})
		}
		if len(res.Candidates) == 0 {
			// FTS returned no matches (not a failure). Fall back to
			// substring for better recall on pre-migration corpora /
			// short queries that BM25 under-serves.
			goto fallback
		}
		return res
	}

fallback:
	// Legacy substring fallback path. When BM25 isn't available we
	// scan the whole-repo symbol set (within a reasonable cap) and
	// score by token-overlap + light boosts for name-prefix / suffix
	// hits. This preserves natural-language recall in OSS mode and
	// during the pre-migration window.
	syms, _ := a.Store.GetSymbols(ctx, req.Repo, nil, filters.Kind, 5000, 0)
	tokens := tokenizeQuery(in.Cleaned)
	res.Candidates = make([]*Candidate, 0, len(syms))
	for _, s := range syms {
		if s == nil {
			continue
		}
		score := tokenOverlapScore(tokens, s)
		if score <= 0 {
			continue
		}
		res.Candidates = append(res.Candidates, &Candidate{
			EntityID:   s.ID,
			EntityType: "symbol",
			AdapterID:  "fts",
			RawScore:   score,
			Symbol:     s,
		})
	}
	sortCandidatesByScore(res.Candidates)
	// Re-number ranks after sort so RRF uses actual position.
	if lim := effLimit(req.Limit); lim > 0 && len(res.Candidates) > lim {
		res.Candidates = res.Candidates[:lim]
	}
	for i, c := range res.Candidates {
		c.Rank = i
	}
	return res
}

// -----------------------------------------------------------------------------
// Vector (ANN) adapter
// -----------------------------------------------------------------------------

// VectorAdapter runs the semantic arm. Requires both a backend that
// implements VectorSymbolSearch and an Embedder. Either absent → the
// adapter reports unavailable and the fuser skips it.
type VectorAdapter struct {
	Store    graph.GraphStore
	Embedder Embedder
}

func (*VectorAdapter) Name() string { return "vector" }

func (a *VectorAdapter) Search(ctx context.Context, in RouterOutput, req *Request) AdapterResult {
	res := AdapterResult{AdapterID: "vector"}
	if a == nil || a.Store == nil || a.Embedder == nil || in.Cleaned == "" {
		res.Unavailable = true
		return res
	}
	vs, ok := a.Store.(graph.VectorSymbolSearch)
	if !ok {
		res.Unavailable = true
		return res
	}
	tEmbed := time.Now()
	qvec, ok := a.Embedder.Embed(ctx, in.Cleaned)
	res.EmbedMs = float64(time.Since(tEmbed).Nanoseconds()) / 1e6
	if !ok || len(qvec) == 0 {
		res.Unavailable = true
		return res
	}
	filters := graph.SymbolSearchFilters{
		Kind:     kindFilter(in.Filters, req.Filters),
		Language: langFilter(in.Filters, req.Filters),
		FilePath: pathFilter(in.Filters, req.Filters),
	}
	matches := vs.SearchSymbolsVector(ctx, req.Repo, qvec, filters, effLimit(req.Limit))
	if matches == nil {
		// Backend query failed — treat as unavailable.
		res.Unavailable = true
		return res
	}
	res.Candidates = make([]*Candidate, 0, len(matches))
	for i, m := range matches {
		if m == nil || m.Symbol == nil {
			continue
		}
		res.Candidates = append(res.Candidates, &Candidate{
			EntityID:   m.Symbol.ID,
			EntityType: "symbol",
			AdapterID:  "vector",
			Rank:       i,
			RawScore:   m.Similarity,
			Symbol:     m.Symbol,
		})
	}
	return res
}

// -----------------------------------------------------------------------------
// Graph / structural adapter
// -----------------------------------------------------------------------------

// GraphAdapter services calls:foo / callers:foo / impl:Interface
// queries by walking the stored call-graph edges. Purely structural;
// lexical signals are handled by the other adapters.
type GraphAdapter struct {
	Store graph.GraphStore
}

func (*GraphAdapter) Name() string { return "graph" }

func (a *GraphAdapter) Search(ctx context.Context, in RouterOutput, req *Request) AdapterResult {
	res := AdapterResult{AdapterID: "graph"}
	if a == nil || a.Store == nil || len(in.Structural) == 0 || in.Seed == "" {
		res.Unavailable = true
		return res
	}

	// Resolve the seed by name or qualified_name.
	seedSym := resolveSeed(ctx, a.Store, req.Repo, in.Seed)
	if seedSym == nil {
		return res
	}

	var ids []string
	for _, op := range in.Structural {
		switch op {
		case OpCalls:
			ids = append(ids, a.Store.GetCallees(ctx, seedSym.ID)...)
		case OpCallers:
			ids = append(ids, a.Store.GetCallers(ctx, seedSym.ID)...)
		case OpImpl:
			// V1: implementation discovery is a separate investment
			// (needs interface-symbol edges). Return empty for now.
		}
	}

	// Dedupe while preserving first-seen order.
	seen := make(map[string]bool, len(ids))
	symMap := a.Store.GetSymbolsByIDs(ctx, ids)
	rank := 0
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		s := symMap[id]
		if s == nil {
			continue
		}
		res.Candidates = append(res.Candidates, &Candidate{
			EntityID:   id,
			EntityType: "symbol",
			AdapterID:  "graph",
			Rank:       rank,
			RawScore:   1.0,
			Symbol:     s,
		})
		rank++
	}
	return res
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func effLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultLimit
	case n > MaxLimit:
		return MaxLimit
	default:
		return n
	}
}

func kindFilter(router Filters, req Filters) *string {
	if router.Kind != "" {
		v := router.Kind
		return &v
	}
	if req.Kind != "" {
		v := req.Kind
		return &v
	}
	return nil
}

func langFilter(router Filters, req Filters) *string {
	if router.Language != "" {
		v := router.Language
		return &v
	}
	if req.Language != "" {
		v := req.Language
		return &v
	}
	return nil
}

func pathFilter(router Filters, req Filters) *string {
	if router.FilePath != "" {
		v := router.FilePath
		return &v
	}
	if req.FilePath != "" {
		v := req.FilePath
		return &v
	}
	return nil
}

// tokenizeQuery lowercases the query and splits on whitespace and
// common punctuation. A minimal stopword pass prevents pronouns like
// "where" / "is" / "the" from dominating overlap scores.
func tokenizeQuery(q string) []string {
	q = strings.ToLower(q)
	rawFields := strings.FieldsFunc(q, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', ',', '.', '?', '!', '(', ')', '[', ']', '{', '}', '"', '\'', ';', ':':
			return true
		}
		return false
	})
	out := rawFields[:0]
	for _, f := range rawFields {
		if len(f) < 2 {
			continue
		}
		if _, stop := stopwords[f]; stop {
			continue
		}
		out = append(out, f)
	}
	return out
}

var stopwords = map[string]struct{}{
	"the": {}, "a": {}, "an": {}, "is": {}, "are": {}, "was": {}, "were": {},
	"and": {}, "or": {}, "of": {}, "in": {}, "on": {}, "at": {}, "to": {},
	"for": {}, "by": {}, "with": {}, "from": {}, "as": {}, "it": {}, "this": {},
	"that": {}, "these": {}, "those": {}, "where": {}, "how": {}, "what": {},
	"when": {}, "which": {}, "who": {}, "why": {}, "do": {}, "does": {}, "did": {},
	"can": {}, "could": {}, "would": {}, "should": {}, "will": {}, "be": {},
	"been": {}, "being": {}, "has": {}, "have": {}, "had": {}, "i": {}, "you": {},
	"he": {}, "she": {}, "we": {}, "they": {}, "me": {}, "us": {}, "them": {},
	"my": {}, "our": {}, "your": {}, "their": {}, "its": {},
}

// tokenOverlapScore is the scoring function used when BM25 is not
// available. It is intentionally simple: for each query token, reward
// (a) matches in the symbol name most, (b) matches in qualified name
// somewhat less, and (c) matches in the doc comment least.
func tokenOverlapScore(tokens []string, s *graph.StoredSymbol) float64 {
	if len(tokens) == 0 || s == nil {
		return 0
	}
	name := strings.ToLower(s.Name)
	qual := strings.ToLower(s.QualifiedName)
	doc := strings.ToLower(s.DocComment)
	sig := strings.ToLower(s.Signature)
	score := 0.0
	for _, t := range tokens {
		switch {
		case name == t:
			score += 2.0
		case strings.HasPrefix(name, t):
			score += 1.5
		case strings.Contains(name, t):
			score += 1.0
		case strings.Contains(qual, t):
			score += 0.6
		}
		if doc != "" && strings.Contains(doc, t) {
			score += 0.4
		}
		if sig != "" && strings.Contains(sig, t) {
			score += 0.2
		}
	}
	// Normalize roughly by token count so longer queries don't
	// automatically out-rank shorter ones.
	return score / (1.0 + float64(len(tokens))*0.5)
}

// (substringScore used to live here as a legacy FTS-fallback scorer;
// the current adapter wires BM25 + embeddings rather than substring
// fallback, so the helper is dead. Removed to satisfy lint.)

func sortCandidatesByScore(cs []*Candidate) {
	// Small-N sort; stable bubble-sort-style pass is plenty and avoids
	// pulling sort import just for this utility.
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0 && cs[j].RawScore > cs[j-1].RawScore; j-- {
			cs[j], cs[j-1] = cs[j-1], cs[j]
		}
	}
}

// resolveSeed looks up a symbol by exact name first, then qualified
// name. Returns nil if no match.
func resolveSeed(ctx context.Context, store graph.GraphStore, repoID, seed string) *graph.StoredSymbol {
	q := seed
	syms, _ := store.GetSymbols(ctx, repoID, &q, nil, 50, 0)
	lq := strings.ToLower(seed)
	for _, s := range syms {
		if strings.EqualFold(s.Name, seed) {
			return s
		}
	}
	for _, s := range syms {
		qual := strings.ToLower(s.QualifiedName)
		if qual == lq || strings.HasSuffix(qual, "."+lq) || strings.HasSuffix(qual, ":"+lq) {
			return s
		}
	}
	if len(syms) > 0 {
		return syms[0]
	}
	return nil
}
