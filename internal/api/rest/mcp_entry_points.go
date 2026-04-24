// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"

	"github.com/sourcebridge/sourcebridge/internal/entrypoints"
)

// Phase 1b — get_entry_points.
//
// Adapts stored graph.Symbols + FileResults into the
// internal/entrypoints classifier and returns the result. Two modes:
// "basic" is language-agnostic (main funcs + HTTP-verb-named
// functions); "framework_aware" adds per-framework detection
// (Grails controllers, FastAPI/Flask decorators, Go http.ResponseWriter
// signatures, Next.js API routes).

func (h *mcpHandler) getEntryPointsToolDef() mcpToolDefinition {
	return mcpToolDefinition{
		Name:        "get_entry_points",
		Description: "Return structured entry points across the indexed repo — main funcs, HTTP routes, Grails controller actions, message handlers. `precision: \"basic\"` uses language-agnostic heuristics; `precision: \"framework_aware\"` adds per-framework detectors.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
				"kind": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"main", "http_route", "cli_command", "message_handler", "scheduled_job", "grails_controller_action", "any"},
					"description": "Filter to a single entry-point kind (default: any)",
				},
				"precision": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"basic", "framework_aware"},
					"description": "Classifier precision (default: framework_aware)",
				},
				"limit": map[string]interface{}{"type": "integer", "description": "Max results (default: 200, cap 1000)"},
			},
			"required": []string{"repository_id"},
		},
	}
}

type entryPointsResult struct {
	RepositoryID string                  `json:"repository_id"`
	Precision    string                  `json:"precision"`
	EntryPoints  []entrypoints.EntryPoint `json:"entry_points"`
	Total        int                     `json:"total"`
	Truncated    bool                    `json:"truncated,omitempty"`
}

func (h *mcpHandler) callGetEntryPoints(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
		Kind         string `json:"kind"`
		Precision    string `json:"precision"`
		Limit        int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	precision := entrypoints.PrecisionFrameworkAware
	if params.Precision == string(entrypoints.PrecisionBasic) {
		precision = entrypoints.PrecisionBasic
	}
	limit := params.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}

	// Pull every symbol once. The classifier is a linear pass; we
	// don't need pagination here — the Phase 2 pagination work
	// adds cursors for all list tools as a uniform treatment.
	storedSymbols, _ := h.store.GetSymbols(params.RepositoryID, nil, nil, 0, 0)
	symbols := make([]entrypoints.Symbol, 0, len(storedSymbols))
	for _, s := range storedSymbols {
		symbols = append(symbols, entrypoints.Symbol{
			ID:        s.ID,
			Name:      s.Name,
			Kind:      s.Kind,
			Language:  s.Language,
			FilePath:  s.FilePath,
			StartLine: s.StartLine,
			EndLine:   s.EndLine,
			Signature: s.Signature,
			IsTest:    s.IsTest,
		})
	}

	// File metadata — Grails role is what the classifier actually
	// reads. The in-memory graph store doesn't expose GrailsRole on
	// graph.File today (it lives on FileResult during indexing and
	// isn't currently persisted per-file). For Phase 1b we bridge
	// via a minimal file list and leave GrailsRole empty when the
	// store doesn't carry it — the framework-aware classifier falls
	// back to per-symbol signals cleanly.
	storedFiles := h.store.GetFiles(params.RepositoryID)
	files := make([]entrypoints.File, 0, len(storedFiles))
	for _, f := range storedFiles {
		files = append(files, entrypoints.File{
			Path:       f.Path,
			Language:   f.Language,
			GrailsRole: grailsRoleFromPath(f.Path),
		})
	}

	all := entrypoints.Classify(symbols, files, precision)

	// Kind filter.
	if params.Kind != "" && params.Kind != "any" {
		filtered := all[:0]
		for _, ep := range all {
			if string(ep.Kind) == params.Kind {
				filtered = append(filtered, ep)
			}
		}
		all = filtered
	}

	total := len(all)
	truncated := false
	if total > limit {
		all = all[:limit]
		truncated = true
	}

	return entryPointsResult{
		RepositoryID: params.RepositoryID,
		Precision:    string(precision),
		EntryPoints:  all,
		Total:        total,
		Truncated:    truncated,
	}, nil
}

// grailsRoleFromPath is a thin local adapter over the indexer's
// GrailsRoleFor(). Keeps the MCP layer from importing the indexer
// package directly at call-sites; also centralizes the case where
// the graph store would eventually expose the role per file.
func grailsRoleFromPath(path string) string {
	// Duplicate the indexer's classification logic at the MCP layer
	// rather than take a cross-package dependency for a single-call
	// convention lookup. See internal/indexer/grails.go for the
	// canonical implementation — this match set is intentionally
	// identical.
	prefixes := []struct {
		prefix string
		role   string
		ext    string
	}{
		{"grails-app/controllers/", "grails_controller", ".groovy"},
		{"grails-app/domain/", "grails_domain", ".groovy"},
		{"grails-app/services/", "grails_service", ".groovy"},
		{"grails-app/taglib/", "grails_taglib", ".groovy"},
		{"grails-app/conf/", "grails_conf", ".groovy"},
		{"grails-app/views/", "grails_view", ".gsp"},
	}
	for _, p := range prefixes {
		if startsWithFold(path, p.prefix) && endsWithFold(path, p.ext) {
			return p.role
		}
	}
	return ""
}

func startsWithFold(s, prefix string) bool {
	if len(prefix) > len(s) {
		return false
	}
	return equalFold(s[:len(prefix)], prefix)
}

func endsWithFold(s, suffix string) bool {
	if len(suffix) > len(s) {
		return false
	}
	return equalFold(s[len(s)-len(suffix):], suffix)
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ac, bc := a[i], b[i]
		if ac >= 'A' && ac <= 'Z' {
			ac += 32
		}
		if bc >= 'A' && bc <= 'Z' {
			bc += 32
		}
		if ac != bc {
			return false
		}
	}
	return true
}
