package graphql

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sourcebridge/sourcebridge/internal/git"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

func (r *mutationResolver) importRepository(repoID, repoName, repoPath string, isRemote bool, token *string) {
	ctx := context.Background()
	store := r.Store
	if store == nil {
		return
	}

	localPath := repoPath
	if isRemote {
		cacheDir := "./repo-cache"
		if r.Config != nil && r.Config.Storage.RepoCachePath != "" {
			cacheDir = r.Config.Storage.RepoCachePath
		}
		cloneDir := filepath.Join(cacheDir, "repos", sanitizeRepoName(repoName))
		pullToken := ""
		if token != nil {
			pullToken = *token
		}
		// Resolve workspace credentials once. If the repo carries its own
		// auth_token (the explicit, request-scoped form) we keep using
		// that and ignore a workspace integrity error — the repo-level
		// token would override anyway.
		defaultToken, sshKeyPath, credsErr := r.resolveGitCredentials(ctx)
		if credsErr != nil && pullToken == "" {
			// No request-scoped fallback and the workspace creds are
			// unusable (corrupt envelope, missing key). Fail closed.
			store.SetRepositoryError(repoID, fmt.Errorf("git credentials integrity failure: %w", credsErr))
			return
		}
		if pullToken == "" {
			pullToken = defaultToken
		}
		if err := os.MkdirAll(filepath.Dir(cloneDir), 0o755); err != nil {
			store.SetRepositoryError(repoID, fmt.Errorf("creating clone dir: %w", err))
			return
		}
		if err := gitCloneCmd(ctx, repoPath, cloneDir, pullToken, sshKeyPath).Run(); err != nil {
			store.SetRepositoryError(repoID, fmt.Errorf("cloning repository: %w", err))
			return
		}
		store.UpdateRepositoryMeta(repoID, graphstore.RepositoryMeta{ClonePath: cloneDir})
		localPath = cloneDir
	}

	idx := indexer.NewIndexer(nil)
	result, err := idx.IndexRepository(ctx, localPath)
	if err != nil {
		store.SetRepositoryError(repoID, fmt.Errorf("indexing repository: %w", err))
		return
	}
	result.RepoName = repoName
	if isRemote {
		result.RepoPath = repoPath
	}
	if _, err := store.ReplaceIndexResult(repoID, result); err != nil {
		store.SetRepositoryError(repoID, fmt.Errorf("storing index result: %w", err))
		return
	}
	commitSHA := ""
	if gitMeta, err := git.GetGitMetadata(localPath); err == nil && gitMeta != nil {
		store.UpdateRepositoryMeta(repoID, graphstore.RepositoryMeta{
			ClonePath: localPath,
			CommitSHA: gitMeta.CommitSHA,
			Branch:    gitMeta.Branch,
		})
		commitSHA = gitMeta.CommitSHA
	}
	if knowledgePrewarmOnIndexEnabled() {
		go r.seedRepositoryFieldGuide(repoID)
	}
	// Enqueue async clustering job. Must not block the indexing pipeline.
	if r.ClusteringHook != nil {
		r.ClusteringHook(repoID, commitSHA)
	}
}
