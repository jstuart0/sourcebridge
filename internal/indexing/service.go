// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package indexing provides a shared repository-indexing service used
// by both the GraphQL AddRepository mutation and the MCP
// index_repository tool. Before this package existed, the clone +
// index + store flow lived inline in the GraphQL resolver and couldn't
// be called from other surfaces — the MCP lifecycle tools could only
// create Repository records, not trigger full indexing.
//
// The service takes explicit dependencies (store, config, a
// credential-resolver, an optional knowledge-prewarm hook) so it
// doesn't pull in the GraphQL resolver's graph of concerns. Any
// surface that can construct the deps can call Import / Reindex.
package indexing

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/git"
	gitres "github.com/sourcebridge/sourcebridge/internal/git/resolution"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// GitCredentialsFunc returns the default token + ssh key path for the
// current import. R3 slice 2: ctx-aware and error-returning so a
// workspace integrity failure (corrupt envelope, missing encryption
// key) propagates up rather than silently downgrading to env-only
// behavior. Callers MUST surface a non-nil error as a repository-failed
// state — this is the same fail-closed contract the GraphQL clone path
// enforces.
type GitCredentialsFunc func(ctx context.Context) (token, sshKeyPath string, err error)

// PrewarmHook runs after a successful index with the new repository
// ID. Optional — nil = skip.
type PrewarmHook func(repoID string)

// ClusteringHook runs after a successful index with the repository ID and
// commit SHA. It should enqueue an async clustering job without blocking.
// Optional — nil = skip.
type ClusteringHook func(repoID, commitSHA string)

// Service is the shared indexing entry point.
type Service struct {
	cfg        *config.Config
	store      graphstore.GraphStore
	creds      GitCredentialsFunc
	prewarm    PrewarmHook
	clustering ClusteringHook
}

// NewService builds a Service. cfg + store are required; creds may
// be nil (no credentials resolution — remote clones of private repos
// will fail); prewarm may be nil.
func NewService(cfg *config.Config, store graphstore.GraphStore, creds GitCredentialsFunc, prewarm PrewarmHook) *Service {
	return &Service{cfg: cfg, store: store, creds: creds, prewarm: prewarm}
}

// WithClusteringHook wires a post-index clustering hook into the service.
// The hook is called after each successful index run with the repo ID and
// commit SHA so the clustering job can be enqueued asynchronously.
func (s *Service) WithClusteringHook(hook ClusteringHook) {
	s.clustering = hook
}

// ImportSpec describes a single import request.
type ImportSpec struct {
	Name     string  // display name; if empty, derive from the URL/path
	PathOrURL string // local path or remote git URL
	Token    *string // optional PAT for private HTTPS repos (falls back to creds)
}

// Import creates a Repository record (idempotent on path/URL match)
// and, for new entries, kicks off clone + index in a background
// goroutine. Returns the Repository immediately with its initial
// status so the caller can poll.
func (s *Service) Import(ctx context.Context, spec ImportSpec) (*graphstore.Repository, error) {
	if s.store == nil {
		return nil, fmt.Errorf("indexing service has no store configured")
	}
	if spec.PathOrURL == "" {
		return nil, fmt.Errorf("path_or_url is required")
	}
	name := spec.Name
	if name == "" {
		name = deriveRepoName(spec.PathOrURL)
	}

	isRemote := IsGitURL(spec.PathOrURL)

	// Dedup. Mirrors the GraphQL AddRepository dedupe path so both
	// surfaces converge on the same record.
	if isRemote {
		normalized := NormalizeGitURL(spec.PathOrURL)
		for _, existing := range s.store.ListRepositories() {
			if existing.RemoteURL == normalized {
				return existing, nil
			}
		}
	} else {
		abs, err := filepath.Abs(spec.PathOrURL)
		if err == nil {
			if existing := s.store.GetRepositoryByPath(abs); existing != nil {
				return existing, nil
			}
		}
	}

	repo, err := s.store.CreateRepository(name, spec.PathOrURL)
	if err != nil {
		return nil, fmt.Errorf("creating repository: %w", err)
	}

	meta := graphstore.RepositoryMeta{}
	if isRemote {
		meta.RemoteURL = NormalizeGitURL(spec.PathOrURL)
		if spec.Token != nil && *spec.Token != "" {
			meta.AuthToken = *spec.Token
		}
	}
	s.store.UpdateRepositoryMeta(repo.ID, meta)

	// Background clone + index.
	go s.runImport(repo.ID, name, spec.PathOrURL, isRemote, spec.Token)

	return repo, nil
}

// Reindex re-runs the index for an existing repository. Uses the
// repository's stored ClonePath / Path to decide what to index; for
// remote repos it re-clones into the cache dir if missing.
func (s *Service) Reindex(ctx context.Context, repoID string) error {
	repo := s.store.GetRepository(repoID)
	if repo == nil {
		return fmt.Errorf("repository not found: %s", repoID)
	}

	isRemote := IsGitURL(repo.Path) || repo.RemoteURL != ""
	var token *string
	if repo.AuthToken != "" {
		t := repo.AuthToken
		token = &t
	}
	go s.runImport(repoID, repo.Name, repo.Path, isRemote, token)
	return nil
}

// runImport is the background worker. Identical behavior to the
// previous mutationResolver.importRepository, with dependencies
// threaded explicitly.
func (s *Service) runImport(repoID, repoName, repoPath string, isRemote bool, token *string) {
	ctx := context.Background()

	localPath := repoPath
	if isRemote {
		cacheDir := "./repo-cache"
		if s.cfg != nil && s.cfg.Storage.RepoCachePath != "" {
			cacheDir = s.cfg.Storage.RepoCachePath
		}
		cloneDir := filepath.Join(cacheDir, "repos", sanitizeRepoName(repoName))

		pullToken := ""
		if token != nil {
			pullToken = *token
		}
		sshKeyPath := ""
		if s.creds != nil {
			defaultToken, defaultSSH, credsErr := s.creds(ctx)
			if credsErr != nil && pullToken == "" {
				// No request-scoped fallback and the workspace creds
				// are unusable (corrupt envelope, missing encryption
				// key). Fail closed — same contract the GraphQL clone
				// path enforces. Do NOT silently downgrade to an
				// empty token (the legacy bug).
				s.store.SetRepositoryError(repoID, fmt.Errorf("git credentials integrity failure: %w", credsErr))
				return
			}
			if pullToken == "" {
				pullToken = defaultToken
			}
			sshKeyPath = defaultSSH
		}

		if err := os.MkdirAll(filepath.Dir(cloneDir), 0o755); err != nil {
			s.store.SetRepositoryError(repoID, fmt.Errorf("creating clone dir: %w", err))
			return
		}
		if err := GitCloneCmd(ctx, repoPath, cloneDir, pullToken, sshKeyPath).Run(); err != nil {
			s.store.SetRepositoryError(repoID, fmt.Errorf("cloning repository: %w", err))
			return
		}
		s.store.UpdateRepositoryMeta(repoID, graphstore.RepositoryMeta{ClonePath: cloneDir})
		localPath = cloneDir
	}

	idx := indexer.NewIndexer(nil)
	result, err := idx.IndexRepository(ctx, localPath)
	if err != nil {
		s.store.SetRepositoryError(repoID, fmt.Errorf("indexing repository: %w", err))
		return
	}
	result.RepoName = repoName
	if isRemote {
		result.RepoPath = repoPath
	}
	if _, err := s.store.ReplaceIndexResult(repoID, result); err != nil {
		s.store.SetRepositoryError(repoID, fmt.Errorf("storing index result: %w", err))
		return
	}

	// Rebuild package-level dependency edges now that all imports are stored.
	// This is idempotent; existing package_dep records for the repo are replaced.
	s.store.RecomputePackageDependencies(repoID)
	commitSHA := ""
	if gitMeta, err := git.GetGitMetadata(localPath); err == nil && gitMeta != nil {
		s.store.UpdateRepositoryMeta(repoID, graphstore.RepositoryMeta{
			ClonePath: localPath,
			CommitSHA: gitMeta.CommitSHA,
			Branch:    gitMeta.Branch,
		})
		commitSHA = gitMeta.CommitSHA
	}
	if s.prewarm != nil {
		s.prewarm(repoID)
	}
	// Enqueue async clustering job. This must not block the indexing pipeline.
	if s.clustering != nil {
		s.clustering(repoID, commitSHA)
	}
	slog.Info("repository indexed via shared service", "repo_id", repoID, "name", repoName)
}

// ---------------------------------------------------------------------------
// Exported helpers — mirror the GraphQL-resolver-level helpers so other
// callers (MCP today; any future surface) don't have to reach into the
// GraphQL package for them.
// ---------------------------------------------------------------------------

// IsGitURL reports whether the given path is a remote git URL.
func IsGitURL(s string) bool {
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return true
	}
	if strings.HasPrefix(s, "git@") || strings.HasPrefix(s, "ssh://") || strings.HasPrefix(s, "git://") {
		return true
	}
	return false
}

// NormalizeGitURL removes any auth material from an HTTPS URL and
// trims any trailing ".git" so dedup comparisons converge.
func NormalizeGitURL(url string) string {
	u := url
	// Strip any embedded credentials: "https://user:pass@host/..." →
	// "https://host/..."
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(u, prefix) {
			rest := strings.TrimPrefix(u, prefix)
			if at := strings.LastIndex(rest, "@"); at >= 0 {
				u = prefix + rest[at+1:]
			}
			break
		}
	}
	return strings.TrimSuffix(u, ".git")
}

// GitCloneCmd builds the exec.Cmd for a git clone invocation,
// preferring PAT-embedded URLs for HTTPS and GIT_SSH_COMMAND for ssh
// keys. Shape mirrors the GraphQL resolver's helper.
func GitCloneCmd(ctx context.Context, repoURL, targetDir, token, sshKeyPath string) *exec.Cmd {
	args := []string{"clone", "--depth=1"}
	cloneURL := repoURL

	if token != "" && (strings.HasPrefix(repoURL, "https://") || strings.HasPrefix(repoURL, "http://")) {
		// https://host/... → https://<token>@host/...
		for _, prefix := range []string{"https://", "http://"} {
			if strings.HasPrefix(cloneURL, prefix) {
				cloneURL = prefix + token + "@" + strings.TrimPrefix(cloneURL, prefix)
				break
			}
		}
	}

	args = append(args, cloneURL, targetDir)
	cmd := exec.CommandContext(ctx, "git", args...)
	if sshKeyPath != "" && (strings.HasPrefix(repoURL, "git@") || strings.HasPrefix(repoURL, "ssh://")) {
		// Codex r2 high fix: shell-safe quoting via gitres.BuildGitSSHCommand.
		// Indexing path uses StrictHostKeyChecking=no for legacy compat
		// (clones from internal mirrors where the host key is reset
		// frequently); operators who want stricter behavior should
		// pre-populate ~/.ssh/known_hosts on the API pod.
		cmd.Env = append(os.Environ(), "GIT_SSH_COMMAND="+gitres.BuildGitSSHCommand(sshKeyPath, "no"))
	}
	return cmd
}

// sanitizeRepoName produces a filesystem-safe name used for the
// clone directory. Kept package-private because it's only the
// clone-dir convention; if a caller needs it, it can use any safe
// directory name it wants.
func sanitizeRepoName(name string) string {
	if name == "" {
		return "repo"
	}
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
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

// deriveRepoName picks a sensible display name from a path or URL
// when the caller didn't provide one explicitly.
func deriveRepoName(pathOrURL string) string {
	if pathOrURL == "" {
		return "repo"
	}
	// Remote URL: last path segment without .git.
	if IsGitURL(pathOrURL) {
		u := NormalizeGitURL(pathOrURL)
		if idx := strings.LastIndex(u, "/"); idx >= 0 {
			return u[idx+1:]
		}
		return u
	}
	return filepath.Base(pathOrURL)
}
