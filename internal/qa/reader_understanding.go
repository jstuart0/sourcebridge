// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
)

// RepositoryStatus captures the minimum repository-understanding state
// the orchestrator needs to route a deep-QA request: whether the
// understanding corpus is ready, which corpus to read summaries from,
// and the revision that pinned it.
//
// Mirrors Python's DeepUnderstandingContext
// (workers/cli_ask.py:_load_deep_understanding) but reads from the
// same SurrealStore Go already owns instead of reopening a connection.
type RepositoryStatus struct {
	RepositoryID          string
	RepositoryName        string
	CorpusID              string
	UnderstandingStage    string
	TreeStatus            string
	UnderstandingRevision string
	ModelUsed             string
	// Ready mirrors Python _understanding_ready: stage == "ready"
	// AND tree_status == "complete".
	Ready bool
}

// UnderstandingReader wraps the subset of the knowledge/summary stores
// the QA orchestrator needs. It exists as an interface so tests can
// inject lightweight fakes without standing up a SurrealDB.
type UnderstandingReader interface {
	GetRepositoryUnderstanding(repoID string, scope knowledge.ArtifactScope) *knowledge.RepositoryUnderstanding
	GetSummaryNodes(corpusID string) ([]comprehension.SummaryNode, error)
}

// SummaryEvidence is a scored summary-node row the deep pipeline
// packs into the synthesis context. The struct layout matches the
// Python SummaryEvidence dataclass — unit_id / level / headline /
// summary_text / metadata / score / reason — so parity tests compare
// like for like.
type SummaryEvidence struct {
	UnitID      string
	Level       int
	Headline    string
	SummaryText string
	Metadata    map[string]any
	Score       int
	Reason      string
	FilePath    string
}

// GetRepositoryStatus returns the orchestrator-relevant state for a
// single repo-level understanding.
//
// Parity note: the Python implementation preferred the scope_key
// "repository:" row with stage=ready AND tree_status=complete, falling
// back to the latest row for the repo if nothing matched. Since the
// Go store's GetRepositoryUnderstanding already returns the repository
// scope (ScopeRepository) row, we use that directly; on miss we surface
// Ready=false with whatever stage we see so the caller can emit the
// "understanding not ready" CTA instead of a hard error.
func GetRepositoryStatus(reader UnderstandingReader, repoID, repoName string) *RepositoryStatus {
	u := reader.GetRepositoryUnderstanding(repoID, knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository})
	if u == nil {
		return &RepositoryStatus{
			RepositoryID:   repoID,
			RepositoryName: repoName,
			Ready:          false,
		}
	}
	return &RepositoryStatus{
		RepositoryID:          repoID,
		RepositoryName:        repoName,
		CorpusID:              u.CorpusID,
		UnderstandingStage:    string(u.Stage),
		TreeStatus:            string(u.TreeStatus),
		UnderstandingRevision: u.RevisionFP,
		ModelUsed:             u.ModelUsed,
		Ready:                 u.Stage == knowledge.UnderstandingReady && u.TreeStatus == knowledge.UnderstandingTreeComplete,
	}
}

// GetSummaryEvidence ports Python _load_summary_evidence.
//
// Scoring (identical to Python, keep in sync with cli_ask.py:548):
//   +8 per question token appearing in unit_id/headline/summary_text/metadata
//   +5 if level == 1 (file-level)
//   +1 if level == 0
//   +min(level,3) if level > 1
//   +4 if question kind is execution_flow/behavior AND haystack contains
//      any of route/handler/service/session/token/auth
//   +5 if question kind is architecture AND level > 0
//   +1 if metadata.file_path is populated
//   +3 if either headline or summary_text is "useful" (non-empty,
//      not 'could not summarize' / 'n/a' / 'unknown')
//
// Rows with non-useful summary text are discarded (Python `continue`).
// Rows with final score <= 0 are discarded.
// Output sorted by (-score, -level, unit_id).
func GetSummaryEvidence(reader UnderstandingReader, corpusID, question, questionKind string) ([]SummaryEvidence, error) {
	if corpusID == "" {
		return nil, nil
	}
	rows, err := reader.GetSummaryNodes(corpusID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	tokens := tokenizeQuestion(question)

	out := make([]SummaryEvidence, 0, len(rows))
	for _, row := range rows {
		metadata := parseMetadata(row.Metadata)
		metaJSON, _ := json.Marshal(metadata)
		// Sort-keys JSON for stable haystack comparability with Python.
		haystack := strings.ToLower(strings.Join([]string{
			row.UnitID,
			row.Headline,
			row.SummaryText,
			string(metaJSON),
		}, "\n"))
		score := 0
		reasons := []string{}
		for _, tok := range tokens {
			if strings.Contains(haystack, tok) {
				score += 8
				reasons = append(reasons, "match:"+tok)
			}
		}
		level := row.Level
		switch {
		case level == 1:
			score += 5
			reasons = append(reasons, "file-level")
		case level == 0:
			score += 1
		default:
			if level > 3 {
				score += 3
			} else {
				score += level
			}
		}
		filePath, _ := metadata["file_path"].(string)
		if (questionKind == "execution_flow" || questionKind == "behavior") &&
			containsAny(haystack, []string{"route", "handler", "service", "session", "token", "auth"}) {
			score += 4
			reasons = append(reasons, "flow-signal")
		}
		if questionKind == "architecture" && level > 0 {
			score += 5
			reasons = append(reasons, "architecture-level")
		}
		if filePath != "" {
			score += 1
		}
		useful := isUsefulSummary(row.SummaryText) || isUsefulSummary(row.Headline)
		if !useful {
			continue
		}
		score += 3
		reasons = append(reasons, "useful-summary")
		if score <= 0 {
			continue
		}
		out = append(out, SummaryEvidence{
			UnitID:      row.UnitID,
			Level:       level,
			Headline:    row.Headline,
			SummaryText: row.SummaryText,
			Metadata:    metadata,
			Score:       score,
			Reason:      strings.Join(reasons, ", "),
			FilePath:    filePath,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Level != b.Level {
			return a.Level > b.Level
		}
		return a.UnitID < b.UnitID
	})
	return out, nil
}

// stopwords match Python STOPWORDS in workers/cli_ask.py so token
// extraction round-trips for parity tests.
var stopwords = map[string]struct{}{
	"a": {}, "an": {}, "are": {}, "can": {}, "generated": {}, "generate": {},
	"how": {}, "in": {}, "is": {}, "it": {}, "of": {}, "on": {}, "or": {},
	"refresh": {}, "refreshed": {}, "the": {}, "to": {}, "via": {},
	"which": {}, "who": {}, "why": {}, "and": {}, "for": {}, "with": {},
	"this": {}, "that": {}, "what": {}, "does": {}, "into": {}, "from": {},
	"repo": {}, "repository": {}, "flow": {}, "code": {}, "doesn": {},
	"doesnt": {}, "your": {}, "about": {}, "when": {},
}

var tokenRe = regexp.MustCompile(`[A-Za-z0-9_-]+`)

// tokenizeQuestion parallels Python _tokenize_question: lowercase
// alphanumeric/underscore/dash tokens of length ≥ 3, dropping stopwords.
func tokenizeQuestion(q string) []string {
	raw := tokenRe.FindAllString(q, -1)
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		lw := strings.ToLower(t)
		if len(lw) < 3 {
			continue
		}
		if _, stop := stopwords[lw]; stop {
			continue
		}
		out = append(out, lw)
	}
	return out
}

// parseMetadata mirrors Python's tolerant `json.loads(metadata or "{}")
// if str else dict(metadata or {})`.
func parseMetadata(raw string) map[string]any {
	if raw == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return map[string]any{}
	}
	if m == nil {
		return map[string]any{}
	}
	return m
}

func isUsefulSummary(text string) bool {
	s := strings.TrimSpace(text)
	if s == "" {
		return false
	}
	l := strings.ToLower(s)
	return !(strings.HasPrefix(l, "could not summarize") || l == "n/a" || l == "unknown")
}

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
