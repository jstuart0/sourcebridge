// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package search

// ContentSearcher provides repository content search.
type ContentSearcher struct{}

// NewContentSearcher creates a new content searcher.
func NewContentSearcher() *ContentSearcher {
	return &ContentSearcher{}
}
