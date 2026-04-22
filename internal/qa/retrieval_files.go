// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileEvidence is a scored file-level retrieval candidate packed into
// the deep context assembly. Mirrors Python FileEvidence so the
// parity benchmark compares like for like.
type FileEvidence struct {
	Path      string   // repo-relative, forward-slash
	Score     int      // higher = more relevant
	Snippet   string   // best line window from the file
	StartLine int      // 1-based start line of the snippet
	EndLine   int      // 1-based end line
	Reason    string   // semicolon-joined provenance (e.g. "path:auth;heuristic:session")
	Reasons   []string // structured reasons for diagnostics
}

// FileRetriever walks a repository clone on local disk, scores files
// against a question, and returns the best N. This is the Go
// equivalent of workers/cli_ask.py:_best_deep_files + its helpers
// (_collect_file_evidence, _score_file, _best_snippet, plan hints).
// Correctness goal: produce evidence equivalent enough that the
// Phase-4 parity benchmark does not silently regress F3 (ledger:
// best-deep-files selection).
type FileRetriever struct {
	// RepoRoot is the absolute path to the cloned repository.
	RepoRoot string
	// SupportedExts is the allow-list of file extensions to consider.
	// Defaults match Python SUPPORTED_EXTENSIONS.
	SupportedExts map[string]struct{}
	// MaxFiles caps results returned.
	MaxFiles int
	// MaxSnippetLines caps the lines per snippet window.
	MaxSnippetLines int
	// MaxFileBytes bounds how much of a file we load for scoring.
	MaxFileBytes int64
	// SkipDirs names directories that are never traversed.
	SkipDirs map[string]struct{}
}

// DefaultFileRetriever returns a retriever with defaults matching the
// Python baseline (workers/cli_ask.py constants).
func DefaultFileRetriever(repoRoot string) *FileRetriever {
	return &FileRetriever{
		RepoRoot: repoRoot,
		SupportedExts: map[string]struct{}{
			".go": {}, ".py": {}, ".ts": {}, ".js": {}, ".java": {}, ".rs": {},
			".tsx": {}, ".jsx": {},
		},
		MaxFiles:        8,
		MaxSnippetLines: 80,
		MaxFileBytes:    32_000,
		SkipDirs: map[string]struct{}{
			"node_modules": {}, ".git": {}, "dist": {}, "__pycache__": {},
			"build": {}, ".next": {}, "target": {}, ".venv": {}, "venv": {},
		},
	}
}

// BestFiles walks the repo, scores every candidate file, applies
// classifier path boosts, and returns the ranked top-N. Pipeline
// mirrors Python _best_deep_files closely enough that the parity
// benchmark can treat arm differences as bug signal, not noise.
//
// Scoring (per file):
//
//	base  = token-match (8 per token) + domain signals (12 per domain match)
//	      + routes/services path bonuses + readme.md bonus
//	delta = PathBoosts(path, question, kind)
//	final = base + delta.Delta
//
// After scoring, test files are pushed to the tail so product code
// dominates the top-K by default (matches Python).
func (r *FileRetriever) BestFiles(question string, kind QuestionKind) []FileEvidence {
	if r.RepoRoot == "" {
		return nil
	}
	tokens := tokenizeQuestion(question)
	qLower := strings.ToLower(question)

	merged := map[string]FileEvidence{}
	_ = filepath.WalkDir(r.RepoRoot, func(abs string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate unreadable entries
		}
		if d.IsDir() {
			if _, skip := r.SkipDirs[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if _, ok := r.SupportedExts[ext]; !ok {
			// README.md gets a small bump elsewhere but isn't a
			// snippet source, so skip non-source files.
			if strings.ToLower(d.Name()) == "readme.md" {
				// fallthrough to score
			} else {
				return nil
			}
		}
		rel, rerr := filepath.Rel(r.RepoRoot, abs)
		if rerr != nil {
			return nil
		}
		relFwd := filepath.ToSlash(rel)

		base, domain := scoreFile(relFwd, tokens, qLower)
		boost := PathBoosts(relFwd, question, kind)
		score := base + boost.Delta
		if score <= 0 && len(boost.Reasons) == 0 {
			// Low-signal file; skip snippet load to keep traversal
			// cheap on big repos.
			return nil
		}
		snippet, sStart, sEnd, serr := bestSnippet(abs, tokens, r.MaxSnippetLines, r.MaxFileBytes)
		if serr != nil || snippet == "" {
			return nil
		}
		reasons := []string{}
		if domain != "" {
			reasons = append(reasons, domain)
		}
		reasons = append(reasons, boost.Reasons...)
		ev := FileEvidence{
			Path:      relFwd,
			Score:     score,
			Snippet:   snippet,
			StartLine: sStart,
			EndLine:   sEnd,
			Reason:    strings.Join(reasons, ";"),
			Reasons:   reasons,
		}
		if prev, ok := merged[relFwd]; !ok || ev.Score > prev.Score {
			merged[relFwd] = ev
		}
		return nil
	})

	ranked := make([]FileEvidence, 0, len(merged))
	for _, ev := range merged {
		ranked = append(ranked, ev)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].Path < ranked[j].Path
	})

	// Push test files to the tail: product code usually outranks
	// tests for deep questions. Mirrors Python's non_tests/tests split.
	nonTests := make([]FileEvidence, 0, len(ranked))
	tests := make([]FileEvidence, 0, len(ranked))
	for _, ev := range ranked {
		if isTestPath(ev.Path) {
			tests = append(tests, ev)
		} else {
			nonTests = append(nonTests, ev)
		}
	}
	out := append(nonTests, tests...)

	limit := r.MaxFiles
	if kind == KindArchitecture && limit > 4 {
		limit = 4 // Python narrows the view for architecture questions
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// scoreFile ports Python _score_file. Tokens are lowercased; domain
// buckets boost auth/billing/team paths. A small readme.md bump keeps
// repo-level docs visible without swamping the list.
func scoreFile(path string, tokens []string, qLower string) (int, string) {
	pathLower := strings.ToLower(path)
	score := 0
	for _, tok := range tokens {
		if strings.Contains(pathLower, tok) {
			score += 8
		}
	}
	domains := map[string][]string{
		"auth":    {"auth", "session", "jwt", "magic-link", "magic_link", "signin", "signup"},
		"billing": {"billing", "stripe", "payment"},
		"team":    {"team", "invitation", "member"},
	}
	domainHit := ""
	// Python intersects tokens ∩ domain names, then checks path match.
	// Here we do the same check but use qLower as a safety net when a
	// token was stemmed away by the >=3-char filter.
	for name, patterns := range domains {
		tokenHit := false
		for _, t := range tokens {
			if t == name {
				tokenHit = true
				break
			}
		}
		if !tokenHit && !strings.Contains(qLower, name) {
			continue
		}
		for _, p := range patterns {
			if strings.Contains(pathLower, p) {
				score += 12
				domainHit = "domain:" + name
				break
			}
		}
	}
	parts := strings.Split(pathLower, "/")
	for _, part := range parts {
		if part == "routes" {
			score += 2
		}
		if part == "services" {
			score += 3
		}
	}
	if strings.HasSuffix(pathLower, "/readme.md") || pathLower == "readme.md" {
		score += 1
	}
	return score, domainHit
}

// bestSnippet returns the most question-relevant window of lines from
// a file, bounded by MaxSnippetLines. Ports Python _best_snippet:
// find the single best-scoring line and take a window around it.
func bestSnippet(absPath string, tokens []string, maxLines int, maxBytes int64) (string, int, int, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return "", 0, 0, err
	}
	if info.Size() > maxBytes {
		// Skip oversized files so a vendored blob doesn't dominate.
		return "", 0, 0, nil
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", 0, 0, err
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return "", 0, 0, nil
	}
	bestLine, bestScore := 0, 0
	for i, line := range lines {
		lw := strings.ToLower(line)
		s := 0
		for _, tok := range tokens {
			if strings.Contains(lw, tok) {
				s++
			}
		}
		if s > bestScore {
			bestScore = s
			bestLine = i
		}
	}
	// Window around bestLine; if nothing matched, take the head.
	start := bestLine - maxLines/2
	if start < 0 {
		start = 0
	}
	end := start + maxLines
	if end > len(lines) {
		end = len(lines)
		start = end - maxLines
		if start < 0 {
			start = 0
		}
	}
	window := lines[start:end]
	return strings.Join(window, "\n"), start + 1, end, nil
}

// isTestPath identifies files that live under tests/ or follow the
// common *_test.*, *.test.* naming.
func isTestPath(path string) bool {
	p := strings.ToLower(path)
	return strings.Contains(p, "/test") ||
		strings.Contains(p, "_test.") ||
		strings.Contains(p, ".test.") ||
		strings.HasPrefix(p, "tests/") ||
		strings.HasPrefix(p, "test/")
}
