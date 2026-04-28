// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package sinks_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/credentials"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/markdown"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/sinks"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// ─────────────────────────────────────────────────────────────────────────────
// BuildSinkWriters tests
// ─────────────────────────────────────────────────────────────────────────────

// TestBuildSinkWritersConfluenceHappyPath verifies that a fully configured
// Confluence sink produces a ConfluenceSinkWriter.
func TestBuildSinkWritersConfluenceHappyPath(t *testing.T) {
	t.Parallel()

	settings := &livingwiki.RepositoryLivingWikiSettings{
		RepoID: "repo-1",
		Sinks: []livingwiki.RepoWikiSink{
			{
				Kind:            livingwiki.RepoWikiSinkConfluence,
				IntegrationName: "eng-docs",
				Audience:        livingwiki.RepoWikiAudienceEngineer,
			},
		},
	}
	snap := credentials.Snapshot{
		ConfluenceSite:  "mycompany",
		ConfluenceEmail: "bot@mycompany.com",
		ConfluenceToken: "tok-abc",
	}

	writers, err := sinks.BuildSinkWriters(context.Background(), settings, snap, "my-repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(writers) != 1 {
		t.Fatalf("expected 1 writer, got %d", len(writers))
	}
	if writers[0].Name != "eng-docs" {
		t.Errorf("expected name 'eng-docs', got %q", writers[0].Name)
	}
	if writers[0].Writer.Kind() != markdown.SinkKindConfluence {
		t.Errorf("expected kind CONFLUENCE, got %q", writers[0].Writer.Kind())
	}
}

// TestBuildSinkWritersNotionHappyPath verifies that a fully configured Notion
// sink produces a NotionSinkWriter.
func TestBuildSinkWritersNotionHappyPath(t *testing.T) {
	t.Parallel()

	settings := &livingwiki.RepositoryLivingWikiSettings{
		RepoID: "repo-2",
		Sinks: []livingwiki.RepoWikiSink{
			{
				Kind:            livingwiki.RepoWikiSinkNotion,
				IntegrationName: "product-wiki",
				Audience:        livingwiki.RepoWikiAudienceProduct,
			},
		},
	}
	snap := credentials.Snapshot{
		NotionToken: "notion-secret",
	}

	writers, err := sinks.BuildSinkWriters(context.Background(), settings, snap, "repo-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(writers) != 1 {
		t.Fatalf("expected 1 writer, got %d", len(writers))
	}
	if writers[0].Writer.Kind() != markdown.SinkKindNotion {
		t.Errorf("expected kind NOTION, got %q", writers[0].Writer.Kind())
	}
}

// TestBuildSinkWritersMissingConfluenceSite verifies that a missing
// ConfluenceSite returns ErrMissingCredentials.
func TestBuildSinkWritersMissingConfluenceSite(t *testing.T) {
	t.Parallel()

	settings := &livingwiki.RepositoryLivingWikiSettings{
		RepoID: "repo-3",
		Sinks: []livingwiki.RepoWikiSink{
			{Kind: livingwiki.RepoWikiSinkConfluence, IntegrationName: "eng"},
		},
	}
	snap := credentials.Snapshot{
		// ConfluenceSite deliberately empty
		ConfluenceEmail: "bot@example.com",
		ConfluenceToken: "tok",
	}

	_, err := sinks.BuildSinkWriters(context.Background(), settings, snap, "")
	if err == nil {
		t.Fatal("expected error for missing ConfluenceSite")
	}
	var me *sinks.ErrMissingCredentials
	if !errors.As(err, &me) {
		t.Errorf("expected *ErrMissingCredentials, got %T: %v", err, err)
	}
	if !sinks.IsMissingCredentialsError(err) {
		t.Error("IsMissingCredentialsError should return true")
	}
}

// TestBuildSinkWritersMissingNotionToken verifies that a missing NotionToken
// returns ErrMissingCredentials.
func TestBuildSinkWritersMissingNotionToken(t *testing.T) {
	t.Parallel()

	settings := &livingwiki.RepositoryLivingWikiSettings{
		RepoID: "repo-4",
		Sinks: []livingwiki.RepoWikiSink{
			{Kind: livingwiki.RepoWikiSinkNotion, IntegrationName: "prod"},
		},
	}
	snap := credentials.Snapshot{} // NotionToken empty

	_, err := sinks.BuildSinkWriters(context.Background(), settings, snap, "")
	if err == nil {
		t.Fatal("expected error for missing NotionToken")
	}
	if !sinks.IsMissingCredentialsError(err) {
		t.Errorf("expected ErrMissingCredentials, got %T: %v", err, err)
	}
}

// TestBuildSinkWritersGitRepoSkipped verifies that GIT_REPO is silently
// skipped (writers list comes back length 0) rather than aborting the
// whole build. Skip-rather-than-abort lets a repo configured with
// [GIT_REPO, CONFLUENCE] still produce a working Confluence writer.
func TestBuildSinkWritersGitRepoSkipped(t *testing.T) {
	t.Parallel()

	settings := &livingwiki.RepositoryLivingWikiSettings{
		RepoID: "repo-5",
		Sinks: []livingwiki.RepoWikiSink{
			{Kind: livingwiki.RepoWikiSinkGitRepo, IntegrationName: "main-repo"},
		},
	}
	snap := credentials.Snapshot{}

	writers, err := sinks.BuildSinkWriters(context.Background(), settings, snap, "")
	if err != nil {
		t.Fatalf("expected nil error when only an unimplemented sink is configured, got: %v", err)
	}
	if len(writers) != 0 {
		t.Errorf("expected 0 writers (GIT_REPO skipped), got %d", len(writers))
	}
}

// TestBuildSinkWritersNilSettings returns nil without error when settings is nil.
func TestBuildSinkWritersNilSettings(t *testing.T) {
	t.Parallel()

	writers, err := sinks.BuildSinkWriters(context.Background(), nil, credentials.Snapshot{}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(writers) != 0 {
		t.Errorf("expected 0 writers for nil settings, got %d", len(writers))
	}
}

// TestBuildSinkWritersNoSinks returns nil without error when the settings have
// an empty Sinks slice.
func TestBuildSinkWritersNoSinks(t *testing.T) {
	t.Parallel()

	settings := &livingwiki.RepositoryLivingWikiSettings{
		RepoID: "repo-empty",
		Sinks:  []livingwiki.RepoWikiSink{},
	}

	writers, err := sinks.BuildSinkWriters(context.Background(), settings, credentials.Snapshot{}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(writers) != 0 {
		t.Errorf("expected 0 writers, got %d", len(writers))
	}
}
