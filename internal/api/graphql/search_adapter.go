// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"strings"
	"time"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/search"
)

// searchLegacy preserves the original substring-only Search
// implementation so tests that construct a bare Resolver (no search
// service) keep working. It is also the rollback path for the hybrid
// service.
func (r *Resolver) searchLegacy(ctx context.Context, query string, repositoryID *string, limit *int) ([]*SearchResult, error) {
	maxResults := 50
	if limit != nil && *limit > 0 && *limit < 200 {
		maxResults = *limit
	}

	var results []*SearchResult
	queryLower := strings.ToLower(query)

	repos := r.getStore(ctx).ListRepositories(ctx)
	for _, repo := range repos {
		if repositoryID != nil && *repositoryID != "" && repo.ID != *repositoryID {
			continue
		}
		symbols, _ := r.getStore(ctx).GetSymbols(ctx, repo.ID, nil, nil, 0, 0)
		for _, sym := range symbols {
			if len(results) >= maxResults {
				break
			}
			nameLower := strings.ToLower(sym.Name)
			qualLower := strings.ToLower(sym.QualifiedName)
			if strings.Contains(nameLower, queryLower) || strings.Contains(qualLower, queryLower) {
				fp := sym.FilePath
				sl := sym.StartLine
				results = append(results, &SearchResult{
					Type:           "symbol",
					ID:             sym.ID,
					Title:          sym.Name,
					Description:    &sym.QualifiedName,
					FilePath:       &fp,
					Line:           &sl,
					RepositoryID:   repo.ID,
					RepositoryName: repo.Name,
				})
			}
		}
		results = append(results, legacyRequirementResults(r.getStore(ctx), repo, query, maxResults-len(results))...)
		results = append(results, legacyFileResults(r.getStore(ctx), repo, query, maxResults-len(results))...)
	}

	if len(results) > maxResults {
		results = results[:maxResults]
	}
	return results, nil
}

// Cross-repo fanout bounds. These are intentionally conservative
// defaults that match plan §Data Consistency Rules. If a deployment
// genuinely needs more than maxCrossRepoSearchRepos authorized repos
// in one omit-repositoryId call it should upgrade to a paginated
// search flow, not raise the cap silently.
const (
	maxCrossRepoSearchRepos    = 20
	crossRepoSearchConcurrency = 8
	perRepoSearchTimeout       = 350 * time.Millisecond
)

// mapSearchResultSymbol converts an internal/search.Result whose
// entity_type is "symbol" into the GraphQL SearchResult envelope.
// Score and signals are always populated; legacy clients that don't
// request the new fields pay nothing for their presence.
func mapSearchResultSymbol(r *search.Result, repo *graphstore.Repository) *SearchResult {
	if r == nil || r.Symbol == nil {
		return nil
	}
	sym := r.Symbol
	fp := sym.FilePath
	sl := sym.StartLine
	score := r.Score
	desc := sym.QualifiedName
	out := &SearchResult{
		Type:           "symbol",
		ID:             sym.ID,
		Title:          sym.Name,
		Description:    &desc,
		FilePath:       &fp,
		Line:           &sl,
		RepositoryID:   repo.ID,
		RepositoryName: repo.Name,
		Score:          &score,
		Signals:        mapSearchSignals(r.Signals),
	}
	return out
}

// mapSearchSignals returns nil for the zero-value Signals so the
// GraphQL envelope doesn't show an object full of zeroes on legacy
// results that don't participate in hybrid ranking (requirements,
// files). Non-zero signals get a populated SearchSignals with only
// the fired dimensions set.
func mapSearchSignals(s search.Signals) *SearchSignals {
	if s.Exact == 0 && s.Lexical == 0 && s.Semantic == 0 && s.Graph == 0 && s.Requirement == 0 {
		return nil
	}
	out := &SearchSignals{}
	if s.Exact > 0 {
		v := s.Exact
		out.Exact = &v
	}
	if s.Lexical > 0 {
		v := s.Lexical
		out.Lexical = &v
	}
	if s.Semantic > 0 {
		v := s.Semantic
		out.Semantic = &v
	}
	if s.Graph > 0 {
		v := s.Graph
		out.Graph = &v
	}
	if s.Requirement > 0 {
		v := s.Requirement
		out.Requirement = &v
	}
	return out
}

// mergeSearchResultRanks performs a second-stage RRF across per-repo
// result lists and returns a single deduped ranking. Each list is
// treated as independently ranked, matching plan §Cross-repo merge
// strategy — raw per-repo scores are never compared directly.
func mergeSearchResultRanks(perRepo [][]*SearchResult) []*SearchResult {
	const k = 60.0
	type agg struct {
		score float64
		res   *SearchResult
	}
	aggMap := make(map[string]*agg)
	for _, list := range perRepo {
		for rank, r := range list {
			if r == nil {
				continue
			}
			key := r.RepositoryID + ":" + r.Type + ":" + r.ID
			a, ok := aggMap[key]
			if !ok {
				a = &agg{res: r}
				aggMap[key] = a
			}
			a.score += 1.0 / (k + float64(rank))
		}
	}
	out := make([]*SearchResult, 0, len(aggMap))
	for _, a := range aggMap {
		out = append(out, a.res)
	}
	// Deterministic tie-break: score DESC, then repo id, type, id ASC.
	// Using a stable insertion sort keeps the implementation free of
	// extra deps and is fine at these sizes.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			a := aggMap[out[j].RepositoryID+":"+out[j].Type+":"+out[j].ID].score
			b := aggMap[out[j-1].RepositoryID+":"+out[j-1].Type+":"+out[j-1].ID].score
			if a > b ||
				(a == b && out[j].RepositoryID < out[j-1].RepositoryID) ||
				(a == b && out[j].RepositoryID == out[j-1].RepositoryID && out[j].Type < out[j-1].Type) ||
				(a == b && out[j].RepositoryID == out[j-1].RepositoryID && out[j].Type == out[j-1].Type && out[j].ID < out[j-1].ID) {
				out[j], out[j-1] = out[j-1], out[j]
				continue
			}
			break
		}
	}
	return out
}

// legacyRequirementResults returns lexical requirement matches for a
// repo. Requirements are not in the hybrid index in V1 (plan
// §Requirements and files in mixed search), so we keep the cheap
// substring scan here to preserve the mixed-entity UX.
func legacyRequirementResults(store graphstore.GraphStore, repo *graphstore.Repository, query string, limit int) []*SearchResult {
	if store == nil || repo == nil || query == "" || limit <= 0 {
		return nil
	}
	q := strings.ToLower(query)
	reqs, _ := store.GetRequirements(context.Background(), repo.ID, 0, 0)
	out := make([]*SearchResult, 0)
	for _, r := range reqs {
		if len(out) >= limit {
			break
		}
		if strings.Contains(strings.ToLower(r.Title), q) ||
			strings.Contains(strings.ToLower(r.Description), q) ||
			strings.Contains(strings.ToLower(r.ExternalID), q) {
			desc := r.Description
			out = append(out, &SearchResult{
				Type:           "requirement",
				ID:             r.ID,
				Title:          r.ExternalID + ": " + r.Title,
				Description:    &desc,
				RepositoryID:   repo.ID,
				RepositoryName: repo.Name,
			})
		}
	}
	return out
}

// legacyFileResults returns lexical file-path matches for a repo.
// Same rationale as legacyRequirementResults.
func legacyFileResults(store graphstore.GraphStore, repo *graphstore.Repository, query string, limit int) []*SearchResult {
	if store == nil || repo == nil || query == "" || limit <= 0 {
		return nil
	}
	q := strings.ToLower(query)
	files := store.GetFiles(context.Background(), repo.ID)
	out := make([]*SearchResult, 0)
	for _, f := range files {
		if len(out) >= limit {
			break
		}
		if strings.Contains(strings.ToLower(f.Path), q) {
			fp := f.Path
			out = append(out, &SearchResult{
				Type:           "file",
				ID:             f.ID,
				Title:          f.Path,
				FilePath:       &fp,
				RepositoryID:   repo.ID,
				RepositoryName: repo.Name,
			})
		}
	}
	return out
}
