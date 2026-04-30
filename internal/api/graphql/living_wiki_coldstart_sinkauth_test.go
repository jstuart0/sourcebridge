// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/coldstart"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// emptyCredsBroker satisfies credentials.Broker but returns empty strings for
// every credential. credentials.Take succeeds (no error) but the resulting
// Snapshot has empty ConfluenceSite/ConfluenceEmail/ConfluenceToken, so
// BuildSinkWriters returns *ErrMissingCredentials for a configured Confluence
// sink.
type emptyCredsBroker struct{}

func (emptyCredsBroker) GitHub(_ context.Context) (string, error)              { return "", nil }
func (emptyCredsBroker) GitLab(_ context.Context) (string, error)              { return "", nil }
func (emptyCredsBroker) ConfluenceSite(_ context.Context) (string, error)      { return "", nil }
func (emptyCredsBroker) Confluence(_ context.Context) (string, string, error)  { return "", "", nil }
func (emptyCredsBroker) Notion(_ context.Context) (string, error)              { return "", nil }

// TestDispatchGeneratedPagesSinkAuthFailureClassifiesAsAuth verifies that when
// BuildSinkWriters returns *ErrMissingCredentials, dispatchGeneratedPages sets
// status="failed" and failCat=FailureCategoryAuth.
//
// Path exercised:
//
//	credentials.Take (succeeds, empty snapshot)
//	→ BuildSinkWriters (returns *ErrMissingCredentials)
//	→ IsMissingCredentialsError (true)
//	→ *failCat = FailureCategoryAuth
func TestDispatchGeneratedPagesSinkAuthFailureClassifiesAsAuth(t *testing.T) {
	t.Parallel()

	repoStore := livingwiki.NewRepoSettingsMemStore()
	if err := repoStore.SetRepoSettings(context.Background(), livingwiki.RepositoryLivingWikiSettings{
		TenantID: "default",
		RepoID:   "repo-auth-test",
		Enabled:  true,
		Sinks: []livingwiki.RepoWikiSink{
			{
				IntegrationName: "test-confluence",
				Kind:            livingwiki.RepoWikiSinkConfluence,
				Audience:        livingwiki.RepoWikiAudienceEngineer,
			},
		},
	}); err != nil {
		t.Fatalf("SetRepoSettings: %v", err)
	}

	status := "ok"
	failCat := coldstart.FailureCategoryNone
	errMsg := ""

	// A minimal page with a non-empty ID — the test exercises the credential
	// path, not page content.
	pages := []ast.Page{{ID: "p-auth-1"}}

	dispatchGeneratedPages(
		context.Background(),
		"repo-auth-test", "default",
		pages,
		nil,              // no skipped page IDs
		emptyCredsBroker{},
		repoStore,
		"test-repo",
		&status, &failCat, &errMsg,
		GenerationModeLWDetailed,
	)

	if status != "failed" {
		t.Errorf("status: got %q, want %q", status, "failed")
	}
	if failCat != coldstart.FailureCategoryAuth {
		t.Errorf("failCat: got %q, want %q", failCat, coldstart.FailureCategoryAuth)
	}
	if errMsg == "" {
		t.Error("errMsg should be non-empty when credentials are missing")
	}
}
