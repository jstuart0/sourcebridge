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

// IsGitURL returns true if s looks like a remote git URL.
//
// Scheme-prefixed URLs (http://, https://, git://, ssh://, git@) are always
// classified as remote. For .git-suffix strings the function guards against
// local bare-repo paths: absolute paths and explicit relative paths
// (./repo.git, ../parent/repo.git) remain local. A bare name without a
// slash (repo.git) is also treated as local because it is ambiguous without
// network context. Only host-shaped strings with a dot before the first slash
// are classified as remote shorthand (e.g. github.com/user/repo.git).
//
// Pre-Slice-7 the indexing layer only checked URL schemes; the graphql layer
// also accepted the .git suffix but only on host-shaped strings. The consolidated
// function preserves both behaviors while fixing the regression introduced in
// b50c087 where /abs/path/repo.git was misclassified as remote.
func IsGitURL(s string) bool {
	// Scheme-prefixed URLs are unambiguously remote.
	if strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "git://") ||
		strings.HasPrefix(s, "git@") ||
		strings.HasPrefix(s, "ssh://") {
		return true
	}

	// .git suffix: only treat as remote when the string is host-shaped.
	// Absolute paths, explicit relative paths, and bare names are local.
	if strings.HasSuffix(s, ".git") {
		if filepath.IsAbs(s) {
			return false // /abs/path/repo.git — local bare repo
		}
		if strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
			return false // ./repo.git or ../parent/repo.git — local bare repo
		}
		// Require at least one slash and a hostname-shaped prefix (contains a
		// dot before the first slash) to distinguish github.com/user/repo.git
		// from a bare local name like repo.git.
		slashIdx := strings.Index(s, "/")
		if slashIdx > 0 && strings.Contains(s[:slashIdx], ".") {
			return true // github.com/user/repo.git shape
		}
		return false // bare name (repo.git) or no hostname
	}

	return false
}
