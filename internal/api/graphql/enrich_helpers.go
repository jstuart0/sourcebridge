// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"sort"

	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
)

// mergeUniqueStrings returns the sorted union of a and b with no duplicates.
// Order is deterministic (sorted) so callers can assert equality in tests.
func mergeUniqueStrings(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	for _, s := range a {
		seen[s] = struct{}{}
	}
	for _, s := range b {
		seen[s] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// explainEvidenceToReferences converts KnowledgeEvidence items from an
// ExplainSystemResponse into the AskReference wire shape that the web UI
// already knows how to render. Symbol evidence items are enriched with the
// qualified name by looking up the symbol in the store.
func explainEvidenceToReferences(
	store graphstore.GraphStore,
	ctx context.Context,
	evidence []*knowledgev1.KnowledgeEvidence,
) []*AskReference {
	out := make([]*AskReference, 0, len(evidence))
	for _, ev := range evidence {
		if ev == nil {
			continue
		}
		switch ev.SourceType {
		case "symbol":
			ref := &AskReference{Kind: "symbol"}
			sym := &AskSymbolRef{
				SymbolID:      ev.SourceId,
				QualifiedName: ev.SourceId, // safe fallback; overwritten below
				FilePath:      optString(ev.FilePath),
				StartLine:     optInt(int(ev.LineStart)),
				EndLine:       optInt(int(ev.LineEnd)),
			}
			// Enrich with qualified name + language from the store when available.
			if store != nil && ev.SourceId != "" {
				if stored := store.GetSymbol(ctx, ev.SourceId); stored != nil {
					sym.QualifiedName = stored.QualifiedName
					sym.Language = optString(stored.Language)
					if sym.FilePath == nil || *sym.FilePath == "" {
						sym.FilePath = optString(stored.FilePath)
					}
					if sym.StartLine == nil {
						sym.StartLine = optInt(stored.StartLine)
					}
					if sym.EndLine == nil {
						sym.EndLine = optInt(stored.EndLine)
					}
				}
			}
			ref.Symbol = sym
			out = append(out, ref)

		case "file", "doc":
			if ev.FilePath == "" {
				continue
			}
			ref := &AskReference{Kind: "file_range"}
			ref.FileRange = &AskFileRangeRef{
				FilePath:  ev.FilePath,
				StartLine: optInt(int(ev.LineStart)),
				EndLine:   optInt(int(ev.LineEnd)),
			}
			out = append(out, ref)

		case "requirement":
			if ev.SourceId == "" {
				continue
			}
			ref := &AskReference{Kind: "requirement"}
			ref.Requirement = &AskRequirementRef{
				ExternalID: ev.SourceId,
				FilePath:   optString(ev.FilePath),
			}
			out = append(out, ref)
		}
	}
	return out
}

// explainEvidenceToRequirementIDs extracts the source IDs of all
// requirement-type evidence items from an ExplainSystemResponse.
func explainEvidenceToRequirementIDs(evidence []*knowledgev1.KnowledgeEvidence) []string {
	out := make([]string, 0)
	seen := make(map[string]struct{})
	for _, ev := range evidence {
		if ev == nil || ev.SourceType != "requirement" || ev.SourceId == "" {
			continue
		}
		if _, exists := seen[ev.SourceId]; !exists {
			seen[ev.SourceId] = struct{}{}
			out = append(out, ev.SourceId)
		}
	}
	return out
}
