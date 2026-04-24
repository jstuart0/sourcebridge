// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Phase 2.3 — pagination + response-shape control.
//
// A shared cursor encoding + paginateSlice helper used by list-
// returning tools. Cursors are opaque base64-encoded JSON
// ({"offset": N}), so the wire format stays stable across backend
// changes.
//
// The envelope is additive: requests that omit `cursor` behave
// exactly as before; responses include `next_cursor` only when there
// is a next page. Vanilla clients calling a paginated tool without
// cursors get all-page-one behavior (up to the tool's limit).

type pageCursor struct {
	Offset int `json:"o"`
}

func encodeCursor(offset int) string {
	if offset <= 0 {
		return ""
	}
	raw, _ := json.Marshal(pageCursor{Offset: offset})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeCursor parses an opaque cursor into an offset. Empty cursor
// is treated as "start at offset 0." Invalid cursors return an error
// so the tool can surface a clean INVALID_ARGUMENTS instead of
// silently starting over.
func decodeCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, fmt.Errorf("invalid cursor: %v", err)
	}
	var pc pageCursor
	if err := json.Unmarshal(raw, &pc); err != nil {
		return 0, fmt.Errorf("invalid cursor: %v", err)
	}
	if pc.Offset < 0 {
		return 0, fmt.Errorf("invalid cursor offset: %d", pc.Offset)
	}
	return pc.Offset, nil
}

// paginateSlice slices `total` items by offset + limit, returning the
// slice, a next-page cursor (empty when the slice is the final page),
// and the total count. Inputs with limit<=0 or limit>cap are clamped
// to the tool-specific default and cap.
func paginateSlice[T any](items []T, offset, limit, defaultLimit, cap int) ([]T, string, int) {
	total := len(items)
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > cap {
		limit = cap
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return nil, "", total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := items[offset:end]
	var next string
	if end < total {
		next = encodeCursor(end)
	}
	return page, next, total
}

// paginationArgs is the standard pagination input block every list
// tool accepts. Embeddable in tool-specific arg structs:
//
//   type myParams struct {
//       RepositoryID string `json:"repository_id"`
//       paginationArgs
//   }
type paginationArgs struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	// Fields controls response shape. Values:
	//   "minimal"  — just names / ids (smallest payload)
	//   "standard" — default — names + locations + kind
	//   "full"     — everything the tool knows about each result
	Fields string `json:"fields,omitempty"`
}

// paginationToolProps returns the JSON-schema fragment to merge into
// a tool's input schema for the standard cursor + limit + fields
// triplet. Keeps schemas consistent without each tool retyping.
func paginationToolProps(defaultLimit, cap int) map[string]interface{} {
	return map[string]interface{}{
		"cursor": map[string]interface{}{
			"type":        "string",
			"description": "Opaque cursor from a prior next_cursor. Omit for page 1.",
		},
		"limit": map[string]interface{}{
			"type":        "integer",
			"description": fmt.Sprintf("Max results per page (default %d, cap %d).", defaultLimit, cap),
		},
		"fields": map[string]interface{}{
			"type":        "string",
			"enum":        []string{"minimal", "standard", "full"},
			"description": "Response shape control (default: standard).",
		},
	}
}
