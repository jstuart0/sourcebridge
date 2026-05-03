// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

// symbolSourceToolDefs returns the tool definitions for get_symbol_source and
// get_symbol_context. They are registered here so the capability registry drift
// test (TestRegistry_AllMCPToolsExistInBaseTools) passes from Phase 1 onward.
// Handler logic is wired in Phase 2 (get_symbol_source) and Phase 3
// (get_symbol_context).
func (h *mcpHandler) symbolSourceToolDefs() []mcpToolDefinition {
	return []mcpToolDefinition{
		{
			Name: "get_symbol_source",
			Description: "Return a symbol's source bytes plus identity (name, qualified_name, kind, " +
				"signature, language) and 1-based inclusive line range. " +
				"Provide either `symbol_id` (fast path) or the `(file_path, symbol_name)` pair, " +
				"optionally disambiguated with `line_start`. " +
				"`context_lines` (0–10, silently clamped) returns surrounding lines. " +
				"Symbols longer than 500 lines include a `source_note` warning string.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
					"file_path":     map[string]interface{}{"type": "string", "description": "Repo-relative file path containing the target symbol (required unless symbol_id is provided)"},
					"symbol_name":   map[string]interface{}{"type": "string", "description": "Name of the target symbol (required unless symbol_id is provided)"},
					"line_start":    map[string]interface{}{"type": "integer", "description": "Optional disambiguator when the same name appears multiple times in the file"},
					"symbol_id":     map[string]interface{}{"type": "string", "description": "Optional fast-path identifier — bypasses (file_path, symbol_name) resolution"},
					"context_lines": map[string]interface{}{"type": "integer", "description": "Lines of surrounding context to prepend/append (default 0; values > 10 are silently clamped to 10)"},
				},
				"required": []string{"repository_id"},
			},
		},
		{
			Name: "get_symbol_context",
			Description: "Return a single-call bundle of a symbol's source plus first-hop callers, callees, " +
				"and file imports. Eliminates the 2-round-trip tax of search_symbols → file read. " +
				"Provide either `symbol_id` or `(file_path, symbol_name)`. " +
				"When `call_graph` or `file_imports` capabilities are disabled, the bundle degrades " +
				"gracefully: the `symbol` and source are always returned; missing sections are signalled " +
				"by `degraded: true` and `degraded_reason`.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
					"file_path":     map[string]interface{}{"type": "string", "description": "Repo-relative file path containing the target symbol (required unless symbol_id is provided)"},
					"symbol_name":   map[string]interface{}{"type": "string", "description": "Name of the target symbol (required unless symbol_id is provided)"},
					"line_start":    map[string]interface{}{"type": "integer", "description": "Optional disambiguator when the same name appears multiple times in the file"},
					"symbol_id":     map[string]interface{}{"type": "string", "description": "Optional fast-path identifier — bypasses (file_path, symbol_name) resolution"},
					"context_lines": map[string]interface{}{"type": "integer", "description": "Lines of surrounding context to prepend/append (default 0; values > 10 are silently clamped to 10)"},
				},
				"required": []string{"repository_id"},
			},
		},
	}
}
