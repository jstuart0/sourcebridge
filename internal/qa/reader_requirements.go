// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var reqIDRe = regexp.MustCompile(`\bREQ-[A-Z0-9-]+\b`)

// LoadRequirementLines reads README.md at the repo root and returns
// the trimmed lines that mention at least one REQ-* identifier.
// Mirrors Python _extract_requirement_lines so deep-mode requirement
// selection is stable across the migration.
func LoadRequirementLines(repoPath string) []string {
	path := filepath.Join(repoPath, "README.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := []string{}
	for _, line := range strings.Split(string(data), "\n") {
		if reqIDRe.MatchString(line) {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

// SelectRelevantRequirements implements Python's
// _select_relevant_requirements: prefer REQ-* IDs mentioned in the
// question or in evidence snippets; fall back to a token-match scan
// when the word "requirement" appears in the question; otherwise
// return nothing.
//
// evidenceSnippets is the concatenation of all retrieved snippets
// (file evidence, summaries) the deep pipeline ultimately packs, so
// the requirement selector sees the same text Python did.
func SelectRelevantRequirements(requirementLines []string, evidenceSnippets []string, question string) []string {
	if len(requirementLines) == 0 {
		return nil
	}
	explicit := extractReqIDs(strings.ToUpper(question))
	for _, s := range evidenceSnippets {
		for id := range extractReqIDs(strings.ToUpper(s)) {
			explicit[id] = struct{}{}
		}
	}
	lower := strings.ToLower(question)
	hasRequirementWord := strings.Contains(lower, "requirement")
	if len(explicit) == 0 && !hasRequirementWord {
		return nil
	}
	selected := []string{}
	for _, line := range requirementLines {
		for id := range explicit {
			if strings.Contains(line, id) {
				selected = append(selected, line)
				break
			}
		}
	}
	if len(selected) > 0 {
		if len(selected) > 8 {
			selected = selected[:8]
		}
		return selected
	}
	if hasRequirementWord {
		tokens := tokenizeQuestion(question)
		type ranked struct {
			score int
			line  string
		}
		r := make([]ranked, 0, len(requirementLines))
		for _, line := range requirementLines {
			low := strings.ToLower(line)
			score := 0
			for _, t := range tokens {
				if strings.Contains(low, t) {
					score++
				}
			}
			if score > 0 {
				r = append(r, ranked{score, line})
			}
		}
		sort.SliceStable(r, func(i, j int) bool {
			if r[i].score != r[j].score {
				return r[i].score > r[j].score
			}
			return r[i].line < r[j].line
		})
		out := make([]string, 0, len(r))
		for _, e := range r {
			if len(out) >= 8 {
				break
			}
			out = append(out, e.line)
		}
		return out
	}
	return nil
}

func extractReqIDs(s string) map[string]struct{} {
	m := map[string]struct{}{}
	for _, match := range reqIDRe.FindAllString(s, -1) {
		m[match] = struct{}{}
	}
	return m
}
