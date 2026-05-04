// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package pathutil provides shared path and repository-name helpers used by
// the indexing, GraphQL, and REST API layers.
package pathutil

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SafeJoinRepoPath joins a repo root and a relative path, ensuring the result
// stays inside the repo root. Returns an error for absolute paths or path
// traversal attempts.
//
// This consolidates safeJoinPath (internal/api/graphql/helpers.go) and
// safeJoinRepoPath (internal/api/rest/qa_deps.go), which were structurally
// identical.
func SafeJoinRepoPath(repoRoot, relPath string) (string, error) {
	relPath = strings.TrimPrefix(relPath, "./")
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("absolute path not allowed: %s", relPath)
	}
	joined := filepath.Join(repoRoot, filepath.FromSlash(relPath))
	absJoined, err := filepath.Abs(joined)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolving repo root: %w", err)
	}
	if absJoined != absRoot && !strings.HasPrefix(absJoined, absRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal rejected: %s", relPath)
	}
	return absJoined, nil
}

// SanitizePolicy controls how SanitizeRepoName treats characters.
type SanitizePolicy int

const (
	// StrictPolicy keeps alphanumeric characters plus '-', '_', and '.'.
	// Spaces, forward-slashes, and backslashes become '-'; everything else
	// is dropped. An empty result falls back to "repo".
	// Matches the pre-existing behavior in internal/indexing/service.go.
	StrictPolicy SanitizePolicy = iota

	// GraphQLLegacyPolicy replaces '/', '\', ' ', and ':' with '-' and
	// preserves all other characters including non-ASCII.
	// Matches the pre-existing behavior in internal/api/graphql/helpers.go.
	GraphQLLegacyPolicy

	// QALegacyPolicy replaces only '/' and ':' with '-' and preserves
	// everything else including spaces, backslashes, and non-ASCII.
	// Matches the pre-existing behavior of sanitizeRepoNameForQA in
	// internal/api/rest/qa_deps.go before Slice 7 (b50c087), which used:
	//   strings.NewReplacer("/", "-", ":", "-").Replace(name)
	// Used to compute fallback QA cache-directory paths so existing on-disk
	// directories remain resolvable after the refactor.
	QALegacyPolicy
)

// SanitizeRepoName returns a filesystem-safe form of a repo name according to
// the given policy.
func SanitizeRepoName(name string, policy SanitizePolicy) string {
	switch policy {
	case GraphQLLegacyPolicy:
		r := strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-")
		return r.Replace(name)
	case QALegacyPolicy:
		r := strings.NewReplacer("/", "-", ":", "-")
		return r.Replace(name)
	default: // StrictPolicy
		if name == "" {
			return "repo"
		}
		out := make([]rune, 0, len(name))
		for _, r := range name {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
				r == '-', r == '_', r == '.':
				out = append(out, r)
			case r == ' ', r == '/', r == '\\':
				out = append(out, '-')
			}
		}
		if len(out) == 0 {
			return "repo"
		}
		return string(out)
	}
}

// IsGitURL returns true if s looks like a git URL: http(s)://, git://, ssh://,
// git@ prefix, or .git suffix.
//
// This uses the more complete check from internal/api/graphql/helpers.go which
// includes the .git suffix, superseding the simpler check in
// internal/indexing/service.go.
func IsGitURL(s string) bool {
	return strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "git://") ||
		strings.HasPrefix(s, "git@") ||
		strings.HasPrefix(s, "ssh://") ||
		strings.HasSuffix(s, ".git")
}
