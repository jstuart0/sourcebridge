// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package source holds layering-neutral primitives for working with
// source-file content. Importable by any layer (REST, GraphQL, qa) —
// no dependencies on other internal packages.
package source

import "strings"

// SliceLines returns lines[start-1:end] from a 1-based inclusive
// [start, end] window. Returns "" when content is empty, end < start,
// start <= 0, end <= 0, or start > len(lines). When end > len(lines)
// it clamps to EOF.
func SliceLines(content string, start, end int) string {
	if content == "" || end < start || start <= 0 || end <= 0 {
		return ""
	}
	lines := strings.Split(content, "\n")
	n := len(lines)
	if start > n {
		return ""
	}
	if end > n {
		end = n
	}
	return strings.Join(lines[start-1:end], "\n")
}
