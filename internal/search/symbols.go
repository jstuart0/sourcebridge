// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package search

// SymbolSearcher provides symbol/name search for files, functions, classes.
type SymbolSearcher struct{}

// NewSymbolSearcher creates a new symbol searcher.
func NewSymbolSearcher() *SymbolSearcher {
	return &SymbolSearcher{}
}
