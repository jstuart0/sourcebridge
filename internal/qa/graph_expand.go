// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import "context"

// graphAdapterStore is the narrow slice of graph.GraphStore the
// orchestrator calls through. Defined locally so internal/qa doesn't
// import internal/graph directly — the REST server layer wires the
// adapter up at startup.
type graphAdapterStore interface {
	GetCallers(ctx context.Context, symbolID string) []string
	GetCallees(ctx context.Context, symbolID string) []string
}

// graphSymbolLookup resolves a symbol ID to its display metadata.
type graphSymbolLookup interface {
	Lookup(ctx context.Context, id string) (qualifiedName, filePath, language string, startLine, endLine int, ok bool)
}

// NewGraphExpander adapts a GraphStore-shaped collaborator + symbol
// lookup into the orchestrator's GraphExpander interface. The split
// keeps the package boundaries clean: callers at the REST layer pass
// a functional SymbolLookup that reaches into graph.StoredSymbol, and
// this package never imports graph.
func NewGraphExpander(store graphAdapterStore, lookup graphSymbolLookup) GraphExpander {
	return &graphExpander{store: store, lookup: lookup}
}

type graphExpander struct {
	store  graphAdapterStore
	lookup graphSymbolLookup
}

func (g *graphExpander) GetCallers(ctx context.Context, symbolID string) []GraphNeighbor {
	if g == nil || g.store == nil {
		return nil
	}
	return g.resolve(ctx, g.store.GetCallers(ctx, symbolID))
}

func (g *graphExpander) GetCallees(ctx context.Context, symbolID string) []GraphNeighbor {
	if g == nil || g.store == nil {
		return nil
	}
	return g.resolve(ctx, g.store.GetCallees(ctx, symbolID))
}

func (g *graphExpander) resolve(ctx context.Context, ids []string) []GraphNeighbor {
	if len(ids) == 0 || g.lookup == nil {
		return nil
	}
	out := make([]GraphNeighbor, 0, len(ids))
	for _, id := range ids {
		qn, fp, lang, start, end, ok := g.lookup.Lookup(ctx, id)
		if !ok {
			continue
		}
		out = append(out, GraphNeighbor{
			SymbolID:      id,
			QualifiedName: qn,
			FilePath:      fp,
			StartLine:     start,
			EndLine:       end,
			Language:      lang,
		})
	}
	return out
}
