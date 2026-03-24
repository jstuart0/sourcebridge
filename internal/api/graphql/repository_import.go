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
		if pullToken == "" {
			defaultToken, _ := r.resolveGitCredentials()
			pullToken = defaultToken
		}
		_, sshKeyPath := r.resolveGitCredentials()
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
	if gitMeta, err := git.GetGitMetadata(localPath); err == nil && gitMeta != nil {
		store.UpdateRepositoryMeta(repoID, graphstore.RepositoryMeta{
			ClonePath: localPath,
			CommitSHA: gitMeta.CommitSHA,
			Branch:    gitMeta.Branch,
		})
	}
	go r.seedRepositoryFieldGuide(repoID)
}
