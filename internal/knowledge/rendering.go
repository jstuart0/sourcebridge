// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

import "strings"

func RequiredCliffNotesSections(scopeType ScopeType, depth ...Depth) []string {
	requestedDepth := DepthMedium
	if len(depth) > 0 {
		requestedDepth = depth[0]
	}
	switch scopeType {
	case ScopeModule:
		return []string{
			"Module Purpose",
			"Key Files",
			"Public API",
			"Internal Architecture",
			"Dependencies & Interactions",
			"Key Patterns & Conventions",
		}
	case ScopeFile:
		return []string{
			"File Purpose",
			"Key Symbols",
			"Dependencies",
			"Usage Patterns",
			"Complexity Notes",
		}
	case ScopeSymbol:
		return []string{
			"Purpose",
			"Signature & Parameters",
			"Call Chain",
			"Impact Analysis",
			"Side Effects & State Changes",
			"Usage Examples",
			"Related Symbols",
		}
	case ScopeRequirement:
		return []string{
			"Requirement Intent",
			"Implementation Summary",
			"Key Implementation Files",
			"Key Symbols",
			"Integration Points",
			"Coverage Assessment",
			"Change Impact",
		}
	default:
		if requestedDepth == DepthDeep {
			return []string{
				"System Purpose",
				"Architecture Overview",
				"External Dependencies",
				"Domain Model",
				"Core System Flows",
				"Code Structure",
				"Security Model",
				"Error Handling Patterns",
				"Data Flow & Request Lifecycle",
				"Concurrency & State Management",
				"Configuration & Feature Flags",
				"Testing Strategy",
				"Key Abstractions",
				"Module Deep Dives",
				"Complexity & Risk Areas",
				"Suggested Starting Points",
			}
		}
		return []string{
			"System Purpose",
			"Architecture Overview",
			"External Dependencies",
			"Domain Model",
			"Core System Flows",
			"Code Structure",
			"Complexity & Risk Areas",
			"Suggested Starting Points",
		}
	}
}

func DeepRefinementSectionTitles(scopeType ScopeType) []string {
	switch scopeType {
	case ScopeModule:
		return []string{
			"Internal Architecture",
			"Dependencies & Interactions",
		}
	case ScopeFile:
		return []string{
			"Usage Patterns",
			"Complexity Notes",
		}
	case ScopeSymbol:
		return []string{
			"Call Chain",
			"Impact Analysis",
			"Side Effects & State Changes",
		}
	case ScopeRequirement:
		return []string{
			"Implementation Summary",
			"Integration Points",
			"Change Impact",
		}
	default:
		return []string{
			"Architecture Overview",
			"Domain Model",
			"External Dependencies",
			"Key Abstractions",
		}
	}
}

func MissingSectionTitles(existing []Section, required []string) []string {
	if len(required) == 0 {
		return nil
	}
	present := make(map[string]struct{}, len(existing))
	for _, sec := range existing {
		title := strings.TrimSpace(sec.Title)
		if title == "" {
			continue
		}
		present[title] = struct{}{}
	}
	missing := make([]string, 0)
	for _, title := range required {
		if _, ok := present[title]; !ok {
			missing = append(missing, title)
		}
	}
	return missing
}

func MergeSectionsByTitle(existing, incoming []Section, selectedTitles map[string]struct{}) []Section {
	if len(existing) == 0 {
		return normalizeSections(incoming)
	}
	merged := make([]Section, 0, len(existing)+len(incoming))
	incomingByTitle := make(map[string]Section, len(incoming))
	for _, sec := range incoming {
		title := strings.TrimSpace(sec.Title)
		if title == "" {
			continue
		}
		incomingByTitle[title] = sec
	}
	for _, sec := range existing {
		title := strings.TrimSpace(sec.Title)
		if _, selected := selectedTitles[title]; selected {
			if replacement, ok := incomingByTitle[title]; ok {
				merged = append(merged, replacement)
				delete(incomingByTitle, title)
				continue
			}
		}
		merged = append(merged, sec)
	}
	for _, sec := range incoming {
		title := strings.TrimSpace(sec.Title)
		if _, selected := selectedTitles[title]; !selected {
			continue
		}
		if _, ok := incomingByTitle[title]; ok {
			merged = append(merged, sec)
			delete(incomingByTitle, title)
		}
	}
	return normalizeSections(merged)
}

func normalizeSections(sections []Section) []Section {
	out := make([]Section, len(sections))
	for i, sec := range sections {
		sec.OrderIndex = i
		if strings.TrimSpace(sec.SectionKey) == "" {
			sec.SectionKey = SectionKeyForTitle(sec.Title)
		}
		if strings.TrimSpace(sec.RefinementStatus) == "" {
			sec.RefinementStatus = "light"
		}
		out[i] = sec
	}
	return out
}
