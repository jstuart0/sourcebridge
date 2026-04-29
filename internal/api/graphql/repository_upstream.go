// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/git"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
)

// upstreamCheckDefaults control the cache TTL and per-lookup timeout.
// Exposed as env vars so operators can tune without a redeploy.
//
//	SOURCEBRIDGE_UPSTREAM_CACHE_TTL_SECONDS   (default 30)
//	SOURCEBRIDGE_UPSTREAM_LOOKUP_TIMEOUT_SECS (default 10)
const (
	defaultUpstreamCacheTTL    = 30 * time.Second
	defaultUpstreamLookupAbort = 10 * time.Second
)

var (
	upstreamCacheOnce sync.Once
	upstreamCache     *git.UpstreamCache
)

// sharedUpstreamCache lazily constructs a process-wide cache.
func sharedUpstreamCache() *git.UpstreamCache {
	upstreamCacheOnce.Do(func() {
		ttl := envDurationSeconds("SOURCEBRIDGE_UPSTREAM_CACHE_TTL_SECONDS", defaultUpstreamCacheTTL)
		upstreamCache = git.NewUpstreamCache(ttl)
	})
	return upstreamCache
}

func envDurationSeconds(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n := 0
	if _, err := fmtSscanInt(raw, &n); err != nil || n <= 0 {
		return fallback
	}
	return time.Duration(n) * time.Second
}

// fmtSscanInt is a tiny wrapper to avoid pulling in "fmt.Sscanf" gymnastics.
func fmtSscanInt(s string, out *int) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errNotAnInt
		}
		n = n*10 + int(r-'0')
	}
	*out = n
	return 1, nil
}

var errNotAnInt = &sentinelErr{"not an integer"}

type sentinelErr struct{ msg string }

func (e *sentinelErr) Error() string { return e.msg }

// resolveUpstreamStatus computes the upstream-vs-indexed status for a
// repository. Returns nil when the repo doesn't have a remote URL
// (local-path only; nothing to compare).
//
// Design notes:
//
//   - Cached with the shared UpstreamCache; concurrent page viewers share
//     one upstream ping per TTL.
//   - Per-call context timeout so a hung remote can't block the API.
//   - An unreachable remote returns a soft UNREACHABLE status with the
//     error message; the UI treats it as "unknown", never as an error
//     the user must dismiss.
//   - Token resolution reuses the existing GitConfig / PAT path.
func (r *Resolver) resolveUpstreamStatus(ctx context.Context, repo *graphstore.Repository) *RepositoryUpstreamStatus {
	if repo == nil {
		return nil
	}
	remote := canonicalRemoteURL(repo)
	if remote == "" {
		// Local-path repos have nothing upstream to compare with.
		return &RepositoryUpstreamStatus{
			Status:           UpstreamStatusUnsupported,
			IndexedCommitSha: ptrStringOrNil(repo.CommitSHA),
			CheckedAt:        time.Now().UTC(),
		}
	}

	token, _, credsErr := r.resolveGitCredentialsForOp(ctx, "graphql.upstream")
	// Repo-level token overrides workspace-level. When the repo has its
	// own token, an integrity failure on the workspace token is irrelevant
	// — the repo-scoped token is what we'll use.
	if repo.AuthToken != "" {
		token = repo.AuthToken
		credsErr = nil
	}
	if credsErr != nil {
		// Workspace credentials are unusable AND no repo-level override.
		// Surface a soft UNREACHABLE so the UI doesn't pin a dismissable
		// error on the user; the structured log + the resolver's own
		// integrity log line tell the operator what's wrong. We keep the
		// existing soft-fail UX shape (the previous code path silently
		// continued with no token, leading to opaque 401s upstream).
		slog.Warn("upstream-status: git credential integrity failure; surfacing UNREACHABLE",
			"repo_id", repo.ID, "error", credsErr)
		return &RepositoryUpstreamStatus{
			Status:           UpstreamStatusUnreachable,
			IndexedCommitSha: ptrStringOrNil(repo.CommitSHA),
			CheckedAt:        time.Now().UTC(),
			ErrorMessage:     ptrStringOrNil(credsErr.Error()),
		}
	}

	branch := repo.Branch

	// Apply a bounded timeout so the resolver can't stall behind a dead
	// remote. 10s is enough for a cold TLS handshake + git advertisement.
	lookupCtx, cancel := context.WithTimeout(ctx, defaultUpstreamLookupAbort)
	defer cancel()

	head := sharedUpstreamCache().Lookup(lookupCtx, remote, branch, token)
	now := time.Now().UTC()
	if head == nil {
		return &RepositoryUpstreamStatus{
			Status:           UpstreamStatusUnknown,
			IndexedCommitSha: ptrStringOrNil(repo.CommitSHA),
			CheckedAt:        now,
		}
	}
	if head.Err != nil {
		msg := head.Err.Error()
		// Keep the error compact for transport; operator can tail API
		// logs for the full stack.
		slog.Debug("upstream_check_unreachable", "repo_id", repo.ID, "remote", remote, "error", msg)
		return &RepositoryUpstreamStatus{
			Status:           UpstreamStatusUnreachable,
			IndexedCommitSha: ptrStringOrNil(repo.CommitSHA),
			CheckedAt:        firstNonZeroTime(head.CheckedAt, now),
			ErrorMessage:     ptrStringOrNil(truncate(msg, 200)),
		}
	}
	indexed := strings.TrimSpace(repo.CommitSHA)
	upstream := strings.TrimSpace(head.CommitSHA)

	status := UpstreamStatusUpToDate
	if indexed == "" || !strings.EqualFold(indexed, upstream) {
		status = UpstreamStatusBehind
	}

	return &RepositoryUpstreamStatus{
		Status:            status,
		UpstreamCommitSha: ptrStringOrNil(upstream),
		IndexedCommitSha:  ptrStringOrNil(indexed),
		CheckedAt:         firstNonZeroTime(head.CheckedAt, now),
	}
}

// canonicalRemoteURL returns the best upstream URL for a repo. Prefers
// the explicit RemoteURL field; falls back to Path when it's a git URL
// (we accept HTTPS, SSH, and git:// forms).
func canonicalRemoteURL(repo *graphstore.Repository) string {
	if repo.RemoteURL != "" {
		return repo.RemoteURL
	}
	if isGitURL(repo.Path) {
		return repo.Path
	}
	return ""
}

func ptrStringOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func firstNonZeroTime(a, b time.Time) time.Time {
	if !a.IsZero() {
		return a
	}
	return b
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
