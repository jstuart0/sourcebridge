// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graph

import (
	"fmt"
	"time"
)

// ImpactFileDiff mirrors git.FileDiff to avoid import cycles.
type ImpactFileDiff struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"`
	Status    string `json:"status"` // added, modified, deleted, renamed
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// ImpactSymbolChange mirrors indexer.SymbolChange to avoid import cycles.
type ImpactSymbolChange struct {
	SymbolID     string `json:"symbol_id,omitempty"`
	Name         string `json:"name"`
	Kind         string `json:"kind,omitempty"`
	FilePath     string `json:"file_path"`
	ChangeType   string `json:"change_type"` // added, modified, removed
	OldSignature string `json:"old_signature,omitempty"`
	NewSignature string `json:"new_signature,omitempty"`
}

// AffectedLink represents a requirement-code link affected by a change.
type AffectedLink struct {
	LinkID        string  `json:"link_id"`
	RequirementID string  `json:"requirement_id"`
	SymbolID      string  `json:"symbol_id"`
	Impact        string  `json:"impact"` // "symbol_modified", "symbol_removed"
	Confidence    float64 `json:"confidence"`
}

// AffectedRequirement summarizes impact on a requirement.
type AffectedRequirement struct {
	RequirementID string `json:"requirement_id"`
	ExternalID    string `json:"external_id"`
	Title         string `json:"title"`
	AffectedLinks int    `json:"affected_links"`
	TotalLinks    int    `json:"total_links"`
}

// ImpactReport holds the results of a change impact analysis.
type ImpactReport struct {
	ID                   string                `json:"id"`
	RepositoryID         string                `json:"repository_id"`
	OldCommitSHA         string                `json:"old_commit_sha,omitempty"`
	NewCommitSHA         string                `json:"new_commit_sha,omitempty"`
	FilesChanged         []ImpactFileDiff      `json:"files_changed"`
	SymbolsAdded         []ImpactSymbolChange  `json:"symbols_added"`
	SymbolsModified      []ImpactSymbolChange  `json:"symbols_modified"`
	SymbolsRemoved       []ImpactSymbolChange  `json:"symbols_removed"`
	AffectedLinks        []AffectedLink        `json:"affected_links"`
	AffectedRequirements []AffectedRequirement `json:"affected_requirements"`
	StaleArtifacts       []string              `json:"stale_artifacts"`
	ComputedAt           time.Time             `json:"computed_at"`
}

// ComputeImpact analyzes which links and requirements are affected by symbol changes.
func ComputeImpact(store GraphStore, repoID string, fileDiffs []ImpactFileDiff, symbolChanges []ImpactSymbolChange) *ImpactReport {
	report := &ImpactReport{
		ID:              fmt.Sprintf("impact-%s-%d", repoID, time.Now().UnixMilli()),
		RepositoryID:    repoID,
		FilesChanged:    fileDiffs,
		SymbolsAdded:    filterChanges(symbolChanges, "added"),
		SymbolsModified: filterChanges(symbolChanges, "modified"),
		SymbolsRemoved:  filterChanges(symbolChanges, "removed"),
		ComputedAt:      time.Now().UTC(),
	}

	// Find affected links for modified and removed symbols
	affectedLinkSet := make(map[string]*AffectedLink)
	affectedReqMap := make(map[string]*AffectedRequirement)

	for _, sc := range symbolChanges {
		if sc.ChangeType != "modified" && sc.ChangeType != "removed" {
			continue
		}
		if sc.SymbolID == "" {
			continue
		}

		impact := "symbol_modified"
		if sc.ChangeType == "removed" {
			impact = "symbol_removed"
		}

		links := store.GetLinksForSymbol(sc.SymbolID, false)
		for _, link := range links {
			if _, seen := affectedLinkSet[link.ID]; seen {
				continue
			}
			affectedLinkSet[link.ID] = &AffectedLink{
				LinkID:        link.ID,
				RequirementID: link.RequirementID,
				SymbolID:      link.SymbolID,
				Impact:        impact,
				Confidence:    link.Confidence,
			}

			// Track affected requirement
			if _, ok := affectedReqMap[link.RequirementID]; !ok {
				req := store.GetRequirement(link.RequirementID)
				if req != nil {
					totalLinks := len(store.GetLinksForRequirement(req.ID, false))
					affectedReqMap[link.RequirementID] = &AffectedRequirement{
						RequirementID: req.ID,
						ExternalID:    req.ExternalID,
						Title:         req.Title,
						AffectedLinks: 0,
						TotalLinks:    totalLinks,
					}
				}
			}
			if ar, ok := affectedReqMap[link.RequirementID]; ok {
				ar.AffectedLinks++
			}
		}
	}

	// Collect into slices
	for _, al := range affectedLinkSet {
		report.AffectedLinks = append(report.AffectedLinks, *al)
	}
	for _, ar := range affectedReqMap {
		report.AffectedRequirements = append(report.AffectedRequirements, *ar)
	}

	if report.FilesChanged == nil {
		report.FilesChanged = []ImpactFileDiff{}
	}
	if report.SymbolsAdded == nil {
		report.SymbolsAdded = []ImpactSymbolChange{}
	}
	if report.SymbolsModified == nil {
		report.SymbolsModified = []ImpactSymbolChange{}
	}
	if report.SymbolsRemoved == nil {
		report.SymbolsRemoved = []ImpactSymbolChange{}
	}
	if report.AffectedLinks == nil {
		report.AffectedLinks = []AffectedLink{}
	}
	if report.AffectedRequirements == nil {
		report.AffectedRequirements = []AffectedRequirement{}
	}
	if report.StaleArtifacts == nil {
		report.StaleArtifacts = []string{}
	}

	return report
}

// symbolKey uniquely identifies a symbol by file path, name, and kind.
type symbolKey struct {
	filePath string
	name     string
	kind     string
}

// DiffSymbols compares old and new symbol sets for changed files
// and returns the list of symbol-level changes.
func DiffSymbols(oldSymbols, newSymbols []*StoredSymbol, changedFiles map[string]bool) []ImpactSymbolChange {
	oldByKey := make(map[symbolKey]*StoredSymbol)
	for _, s := range oldSymbols {
		if changedFiles[s.FilePath] {
			key := symbolKey{filePath: s.FilePath, name: s.Name, kind: s.Kind}
			oldByKey[key] = s
		}
	}

	newByKey := make(map[symbolKey]*StoredSymbol)
	for _, s := range newSymbols {
		if changedFiles[s.FilePath] {
			key := symbolKey{filePath: s.FilePath, name: s.Name, kind: s.Kind}
			newByKey[key] = s
		}
	}

	var changes []ImpactSymbolChange

	for key, newSym := range newByKey {
		oldSym, existed := oldByKey[key]
		if !existed {
			changes = append(changes, ImpactSymbolChange{
				SymbolID:     newSym.ID,
				Name:         newSym.Name,
				Kind:         newSym.Kind,
				FilePath:     newSym.FilePath,
				ChangeType:   "added",
				NewSignature: newSym.Signature,
			})
		} else if oldSym.Signature != newSym.Signature || oldSym.StartLine != newSym.StartLine || oldSym.EndLine != newSym.EndLine {
			changes = append(changes, ImpactSymbolChange{
				SymbolID:     newSym.ID,
				Name:         newSym.Name,
				Kind:         newSym.Kind,
				FilePath:     newSym.FilePath,
				ChangeType:   "modified",
				OldSignature: oldSym.Signature,
				NewSignature: newSym.Signature,
			})
		}
	}

	for key, oldSym := range oldByKey {
		if _, exists := newByKey[key]; !exists {
			changes = append(changes, ImpactSymbolChange{
				SymbolID:     oldSym.ID,
				Name:         oldSym.Name,
				Kind:         oldSym.Kind,
				FilePath:     oldSym.FilePath,
				ChangeType:   "removed",
				OldSignature: oldSym.Signature,
			})
		}
	}

	return changes
}

func filterChanges(changes []ImpactSymbolChange, changeType string) []ImpactSymbolChange {
	var result []ImpactSymbolChange
	for _, c := range changes {
		if c.ChangeType == changeType {
			result = append(result, c)
		}
	}
	if result == nil {
		result = []ImpactSymbolChange{}
	}
	return result
}
