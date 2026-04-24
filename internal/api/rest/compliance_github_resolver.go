//go:build enterprise

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"net/url"
	"strings"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
)

// complianceGitHubResolver maps internal repo IDs to GitHub
// owner/name + auth token for the compliance platform collector.
// It reads from the graph store's Repository.RemoteURL + AuthToken,
// which covers both public GitHub repos and private repos accessed
// via a personal access token. GitHub App installation tokens are a
// follow-up once the enterprise installation-id plumbing exposes them
// here — for now the resolver returns ok=false when it can't produce
// a (owner, name, token) triple.
type complianceGitHubResolver struct {
	store graphstore.GraphStore
}

func newComplianceGitHubResolver(store graphstore.GraphStore) *complianceGitHubResolver {
	return &complianceGitHubResolver{store: store}
}

// ResolveGitHubRepo implements routes.GitHubRepoResolver. Returns
// ok=false for non-GitHub remotes, missing repos, or repos without
// a stored token — the collector then records the attempt as
// unreachable and no facts are produced for that repo.
func (r *complianceGitHubResolver) ResolveGitHubRepo(ctx context.Context, repoID string) (string, string, string, bool) {
	if r == nil || r.store == nil || repoID == "" {
		return "", "", "", false
	}
	repo := r.store.GetRepository(repoID)
	if repo == nil || repo.RemoteURL == "" {
		return "", "", "", false
	}
	owner, name, ok := parseGitHubRemote(repo.RemoteURL)
	if !ok {
		return "", "", "", false
	}
	// Without a token we still want read-only public-repo gathering
	// to work, so we return ok=true with an empty token. The public
	// endpoints for branch protection require authentication though,
	// so collectors will mostly get 404s on a tokenless resolve —
	// they'll be recorded as unreachable, which is the right thing.
	return owner, name, repo.AuthToken, true
}

// parseGitHubRemote extracts (owner, name) from a git remote URL.
// Supports https://github.com/owner/repo(.git) and git@github.com:owner/repo(.git).
func parseGitHubRemote(remote string) (string, string, bool) {
	remote = strings.TrimSpace(remote)
	// SSH form: git@github.com:owner/repo.git
	if strings.HasPrefix(remote, "git@github.com:") {
		trimmed := strings.TrimPrefix(remote, "git@github.com:")
		trimmed = strings.TrimSuffix(trimmed, ".git")
		parts := strings.SplitN(trimmed, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", false
		}
		return parts[0], parts[1], true
	}
	// HTTPS form.
	u, err := url.Parse(remote)
	if err != nil || u.Host == "" {
		return "", "", false
	}
	if !strings.EqualFold(u.Host, "github.com") && !strings.EqualFold(u.Host, "www.github.com") {
		return "", "", false
	}
	path := strings.TrimPrefix(u.Path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
