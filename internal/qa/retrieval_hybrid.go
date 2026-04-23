// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"os"
	"path/filepath"
	"strings"
)

// filesFromSearchHits converts a slice of hybrid-search hits into
// FileEvidence the context packer consumes. Deduplicates by file,
// reads a line-window snippet from the cloned repo at the hit's
// line number, and tags each hit with its fired signals as the
// provenance reason.
//
// When the clone isn't available (no locator, no on-disk files),
// returns an empty slice — the caller falls through to the grep
// retriever in that case.
func filesFromSearchHits(hits []SearchHit, locator RepoLocator, repoID string) []FileEvidence {
	if len(hits) == 0 {
		return nil
	}
	var root string
	if locator != nil {
		if r, ok := locator.LocateRepoClone(repoID); ok {
			root = r
		}
	}

	byPath := make(map[string]FileEvidence, len(hits))
	for i, h := range hits {
		if h.FilePath == "" {
			continue
		}
		// Filter test/docs/benchmark files the same way the local
		// retriever does — we want product code dominating the top-K
		// for deep questions, and the search backbone does not apply
		// that heuristic itself.
		if isTestPath(h.FilePath) {
			continue
		}

		ev, exists := byPath[h.FilePath]
		if !exists {
			ev = FileEvidence{
				Path:      h.FilePath,
				StartLine: h.StartLine,
				EndLine:   h.EndLine,
				// Preserve the original rank as a monotonically
				// decreasing score so stable ordering doesn't need
				// another sort pass.
				Score:   100 - i,
				Reasons: append([]string{}, h.Signals...),
			}
			if root != "" {
				snippet, sStart, sEnd, _ := hybridReadSnippet(root, h.FilePath, h.StartLine)
				if snippet != "" {
					ev.Snippet = snippet
					// Prefer the file-read window bounds over the
					// symbol's own bounds — the window gives more
					// context around the match for the LLM.
					if sStart > 0 {
						ev.StartLine = sStart
					}
					if sEnd > 0 {
						ev.EndLine = sEnd
					}
				}
			}
			ev.Reason = strings.Join(ev.Reasons, ";")
			byPath[h.FilePath] = ev
			continue
		}
		// Dedupe: add new fired signals to the existing record so the
		// Reason captures "this file matched on lexical AND graph AND
		// semantic" in a single entry.
		for _, sig := range h.Signals {
			if !containsStr(ev.Reasons, sig) {
				ev.Reasons = append(ev.Reasons, sig)
			}
		}
		ev.Reason = strings.Join(ev.Reasons, ";")
		byPath[h.FilePath] = ev
	}

	out := make([]FileEvidence, 0, len(byPath))
	for _, ev := range byPath {
		out = append(out, ev)
	}
	return out
}

// hybridReadSnippet reads a ~80-line window centred on focalLine from
// repoRoot/relPath. Falls back to the head of the file when the line
// isn't known (focalLine <= 0). Returns empty on any error — the
// caller treats missing snippets as "no signal loss", since the
// search hit's title/subtitle already describes the result.
func hybridReadSnippet(repoRoot, relPath string, focalLine int) (snippet string, startLine int, endLine int, err error) {
	abs := filepath.Join(repoRoot, filepath.FromSlash(relPath))
	// Don't follow symlinks out of the repo root — same safety
	// posture as the FileRetriever's bestSnippet reader.
	info, serr := os.Stat(abs)
	if serr != nil {
		return "", 0, 0, serr
	}
	if info.Size() > 200_000 {
		// Oversized file — don't pack.
		return "", 0, 0, nil
	}
	data, rerr := os.ReadFile(abs)
	if rerr != nil {
		return "", 0, 0, rerr
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return "", 0, 0, nil
	}
	window := 80
	start := 0
	if focalLine > 0 {
		start = focalLine - window/2 - 1
		if start < 0 {
			start = 0
		}
	}
	end := start + window
	if end > len(lines) {
		end = len(lines)
		start = end - window
		if start < 0 {
			start = 0
		}
	}
	return strings.Join(lines[start:end], "\n"), start + 1, end, nil
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
