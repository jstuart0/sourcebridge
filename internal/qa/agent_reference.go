// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// Reference emission per §Reference Emission Contract (v5).
//
// citation-driven: parse `[cite:<handle>]` tags from the final answer,
// resolve each against the final-turn tool results, emit one
// AskReference per resolved handle. Unresolved handles become
// diagnostics. Uncited results go to diagnostics, not references.
//
// Fallback: if the answer contains zero citation tags, emit
// references for every source that appeared in the final-turn tool
// results. Records `CitationFallbackUsed=true` in the loop result so
// Phase 3 bounds the rate.

var citeTagRe = regexp.MustCompile(`\[cite:([^\]]+)\]`)

// resolveReferencesFromAnswer returns the list of AskReferences the
// answer cites, plus a bool indicating whether the structural
// fallback fired (no `[cite:...]` tags found).
//
// Citation path resolves handles against the FINAL-turn tool result
// block (the one the model most recently saw) because that's the
// context it answered from.
//
// Fallback scans ALL tool results across the conversation. A last
// under-specific search_evidence call can return zero hits; limiting
// the fallback to that one slice would drop perfectly good
// references from earlier turns. Dedup by handle keeps the list
// clean.
func resolveReferencesFromAnswer(answer string, history []AgentMessage) ([]AskReference, bool) {
	finalResults := finalTurnResults(history)
	allResults := allToolResults(history)
	if len(allResults) == 0 {
		// No tools were called — no references to emit.
		return []AskReference{}, false
	}

	// Index every known handle so unresolved-cite checks see the full
	// history. Citation-driven resolution uses this same index so the
	// model can cite a handle from any turn, not just the most recent.
	byHandle := indexResultsByHandle(allResults)

	// Parse citation tags.
	matches := citeTagRe.FindAllStringSubmatch(answer, -1)
	if len(matches) == 0 {
		// Fallback: structural emission from every tool result,
		// deduped by handle. Broader than final-turn-only because a
		// terminal zero-hit call must not erase earlier evidence.
		_ = finalResults
		return emitAllFromResults(allResults), true
	}
	refs := make([]AskReference, 0, len(matches))
	seenHandles := map[string]struct{}{}
	for _, m := range matches {
		handle := strings.TrimSpace(m[1])
		if handle == "" {
			continue
		}
		if _, dup := seenHandles[handle]; dup {
			continue
		}
		seenHandles[handle] = struct{}{}
		if ref, ok := refFromHandle(handle, byHandle); ok {
			refs = append(refs, ref)
		}
		// Unresolved handles: silently skipped. The loop's
		// CitationFallbackUsed flag logs the overall failure mode.
	}
	if len(refs) == 0 {
		// All cited handles failed to resolve — fall back structurally
		// across the whole conversation.
		return emitAllFromResults(allResults), true
	}
	return refs, false
}

// allToolResults flattens every tool_result across the conversation
// into one slice. Used by the structural fallback so references
// aren't tied to a single terminal turn that may have returned
// nothing.
func allToolResults(history []AgentMessage) []ToolResult {
	out := []ToolResult{}
	for _, m := range history {
		if m.Role == AgentRoleToolResult {
			out = append(out, m.ToolResults...)
		}
	}
	return out
}

// finalTurnResults returns the ToolResults from the last tool_result
// message in history (the one that directly precedes the final text
// answer). Empty when the conversation had no tool calls.
func finalTurnResults(history []AgentMessage) []ToolResult {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == AgentRoleToolResult {
			return history[i].ToolResults
		}
	}
	return nil
}

// indexResultsByHandle parses each ok=true tool result's JSON data
// and builds a handle → (toolName, data JSON) map. Used by
// citation-driven resolution so `[cite:sym_abc]` finds the right
// source.
type resultIndex struct {
	toolName string
	data     map[string]any
}

func indexResultsByHandle(results []ToolResult) map[string]resultIndex {
	idx := map[string]resultIndex{}
	for _, r := range results {
		if !r.OK || len(r.Data) == 0 {
			continue
		}
		var generic map[string]any
		if err := json.Unmarshal(r.Data, &generic); err != nil {
			continue
		}
		// Every tool we ship either returns a top-level `handle`
		// (read_file, get_summary single-row) or a `results` array
		// where each row has a `handle` field. Handle both shapes.
		if h, ok := generic["handle"].(string); ok && h != "" {
			idx[h] = resultIndex{
				toolName: toolNameFromData(generic),
				data:     generic,
			}
			continue
		}
		if rows, ok := generic["results"].([]any); ok {
			for _, row := range rows {
				rowMap, _ := row.(map[string]any)
				if rowMap == nil {
					continue
				}
				h, _ := rowMap["handle"].(string)
				if h == "" {
					continue
				}
				idx[h] = resultIndex{
					toolName: toolNameFromData(rowMap),
					data:     rowMap,
				}
			}
			continue
		}
		if nbrs, ok := generic["neighbors"].([]any); ok {
			for _, row := range nbrs {
				rowMap, _ := row.(map[string]any)
				if rowMap == nil {
					continue
				}
				h, _ := rowMap["handle"].(string)
				if h == "" {
					continue
				}
				idx[h] = resultIndex{
					toolName: ToolGetCallers, // caller or callee — both produce SymbolRef
					data:     rowMap,
				}
			}
		}
	}
	return idx
}

// toolNameFromData infers the tool name from result-row shape. We
// use `entity_type` when present (search_evidence rows) else fall
// back to shape-matching. Only called on ok=true rows.
func toolNameFromData(row map[string]any) string {
	if t, ok := row["entity_type"].(string); ok {
		switch t {
		case "symbol":
			return "symbol"
		case "file":
			return "file"
		case "requirement":
			return "requirement"
		}
	}
	if _, ok := row["summary_text"]; ok {
		return ToolGetSummary
	}
	if _, ok := row["content"]; ok {
		return ToolReadFile
	}
	return ""
}

// refFromHandle builds an AskReference from the indexed tool-result
// row matching this handle. Returns ok=false when the handle is
// unresolved.
func refFromHandle(handle string, idx map[string]resultIndex) (AskReference, bool) {
	row, present := idx[handle]
	if !present {
		return AskReference{}, false
	}
	switch row.toolName {
	case "symbol", ToolGetCallers, ToolGetCallees:
		r := AskReference{
			Kind:  RefKindSymbol,
			Title: stringField(row.data, "title", "qualified_name"),
			Symbol: &SymbolRef{
				SymbolID:      stringField(row.data, "entity_id", "symbol_id"),
				QualifiedName: stringField(row.data, "qualified_name", "title"),
				FilePath:      stringField(row.data, "file_path"),
				StartLine:     intField(row.data, "start_line"),
				EndLine:       intField(row.data, "end_line"),
				Language:      stringField(row.data, "language"),
			},
		}
		if r.Title == "" {
			r.Title = handle
		}
		return r, true
	case "file", ToolReadFile:
		start, end := parseHandleRange(handle)
		r := AskReference{
			Kind:  RefKindFileRange,
			Title: handle,
			FileRange: &FileRangeRef{
				FilePath:  stringField(row.data, "file_path", "path"),
				StartLine: pickInt(intField(row.data, "start_line"), start),
				EndLine:   pickInt(intField(row.data, "end_line"), end),
				Snippet:   stringField(row.data, "content"),
			},
		}
		return r, true
	case "requirement":
		r := AskReference{
			Kind:  RefKindRequirement,
			Title: stringField(row.data, "title", "external_id"),
			Requirement: &RequirementRef{
				ExternalID: stringField(row.data, "external_id"),
				Title:      stringField(row.data, "title"),
				FilePath:   stringField(row.data, "file_path"),
			},
		}
		if r.Requirement.ExternalID == "" {
			r.Requirement.ExternalID = handle
		}
		return r, true
	case ToolGetSummary:
		r := AskReference{
			Kind:  RefKindUnderstandingSection,
			Title: stringField(row.data, "headline", "unit_id"),
			UnderstandingSection: &UnderstandingSectionRef{
				SectionID: stringField(row.data, "unit_id"),
				Headline:  stringField(row.data, "headline"),
				Kind:      "section",
			},
		}
		return r, true
	}
	// Unknown shape — emit a best-effort cross-repo ref so the
	// citation isn't lost.
	return AskReference{
		Kind:  RefKindCrossRepoRef,
		Title: handle,
		CrossRepo: &CrossRepoRef{
			Note: "resolved from citation handle",
		},
	}, true
}

// emitAllFromResults is the structural fallback (no citations
// present): emit one AskReference per final-turn result source.
func emitAllFromResults(results []ToolResult) []AskReference {
	refs := []AskReference{}
	seen := map[string]struct{}{}
	for _, r := range results {
		if !r.OK || len(r.Data) == 0 {
			continue
		}
		var generic map[string]any
		if err := json.Unmarshal(r.Data, &generic); err != nil {
			continue
		}
		// Single-handle results.
		if h, ok := generic["handle"].(string); ok && h != "" {
			if _, dup := seen[h]; dup {
				continue
			}
			seen[h] = struct{}{}
			idx := map[string]resultIndex{h: {
				toolName: toolNameFromData(generic),
				data:     generic,
			}}
			if ref, ok := refFromHandle(h, idx); ok {
				refs = append(refs, ref)
			}
		}
		// Multi-handle results.
		for _, key := range []string{"results", "neighbors"} {
			arr, ok := generic[key].([]any)
			if !ok {
				continue
			}
			for _, row := range arr {
				rowMap, _ := row.(map[string]any)
				if rowMap == nil {
					continue
				}
				h, _ := rowMap["handle"].(string)
				if h == "" {
					continue
				}
				if _, dup := seen[h]; dup {
					continue
				}
				seen[h] = struct{}{}
				toolName := toolNameFromData(rowMap)
				if toolName == "" && key == "neighbors" {
					toolName = ToolGetCallers
				}
				idx := map[string]resultIndex{h: {
					toolName: toolName,
					data:     rowMap,
				}}
				if ref, ok := refFromHandle(h, idx); ok {
					refs = append(refs, ref)
				}
			}
		}
	}
	return refs
}

// ---- small helpers -------------------------------------------------

func stringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func intField(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

func pickInt(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

// parseHandleRange pulls line numbers from `path:start-end`. Returns
// zeros when the handle has no line range.
func parseHandleRange(handle string) (int, int) {
	idx := strings.LastIndex(handle, ":")
	if idx < 0 {
		return 0, 0
	}
	tail := handle[idx+1:]
	dash := strings.Index(tail, "-")
	if dash < 0 {
		return 0, 0
	}
	s, _ := strconv.Atoi(tail[:dash])
	e, _ := strconv.Atoi(tail[dash+1:])
	return s, e
}
