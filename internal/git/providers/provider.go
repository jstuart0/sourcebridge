// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package providers defines Git hosting provider interfaces.
// OSS mode uses local filesystem access only.
// Commercial mode adds GitHub, GitLab, and Bitbucket OAuth connectors.
package providers

import "context"

// Repository represents a remote repository.
type Repository struct {
	ID       string
	Name     string
	FullName string
	CloneURL string
	Private  bool
}

// Provider is the interface for Git hosting providers.
type Provider interface {
	// Name returns the provider name (e.g., "github", "gitlab").
	Name() string

	// ListRepositories returns accessible repositories.
	ListRepositories(ctx context.Context) ([]Repository, error)

	// CloneRepository clones a repository to a local path.
	CloneRepository(ctx context.Context, repo Repository, destPath string) error
}

// GitHubProvider is a stub for the commercial GitHub App integration.
type GitHubProvider struct{}

func (g *GitHubProvider) Name() string { return "github" }
func (g *GitHubProvider) ListRepositories(_ context.Context) ([]Repository, error) {
	return nil, ErrCommercialOnly
}
func (g *GitHubProvider) CloneRepository(_ context.Context, _ Repository, _ string) error {
	return ErrCommercialOnly
}

// GitLabProvider is a stub for the commercial GitLab integration.
type GitLabProvider struct{}

func (g *GitLabProvider) Name() string { return "gitlab" }
func (g *GitLabProvider) ListRepositories(_ context.Context) ([]Repository, error) {
	return nil, ErrCommercialOnly
}
func (g *GitLabProvider) CloneRepository(_ context.Context, _ Repository, _ string) error {
	return ErrCommercialOnly
}

// BitbucketProvider is a stub for the commercial Bitbucket integration.
type BitbucketProvider struct{}

func (b *BitbucketProvider) Name() string { return "bitbucket" }
func (b *BitbucketProvider) ListRepositories(_ context.Context) ([]Repository, error) {
	return nil, ErrCommercialOnly
}
func (b *BitbucketProvider) CloneRepository(_ context.Context, _ Repository, _ string) error {
	return ErrCommercialOnly
}
