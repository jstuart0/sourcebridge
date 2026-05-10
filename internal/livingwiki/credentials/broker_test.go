// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package credentials_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/credentials"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// ─── stub broker ─────────────────────────────────────────────────────────────

// stubBroker is a simple in-memory Broker for tests.
type stubBroker struct {
	github     string
	gitlab     string
	cfSite     string
	cfEmail    string
	cfToken    string
	notion     string
	githubErr  error
	gitlabErr  error
	cfSiteErr  error
	cfErr      error
	notionErr  error
	callCounts map[string]int
}

func newStubBroker(gh, gl, cfEmail, cfToken, nt string) *stubBroker {
	return &stubBroker{
		github: gh, gitlab: gl,
		cfSite:  "testsite",
		cfEmail: cfEmail, cfToken: cfToken,
		notion:     nt,
		callCounts: make(map[string]int),
	}
}

func (b *stubBroker) GitHub(_ context.Context) (string, error) {
	b.callCounts["github"]++
	return b.github, b.githubErr
}

func (b *stubBroker) GitLab(_ context.Context) (string, error) {
	b.callCounts["gitlab"]++
	return b.gitlab, b.gitlabErr
}

func (b *stubBroker) ConfluenceSite(_ context.Context) (string, error) {
	b.callCounts["confluencesite"]++
	return b.cfSite, b.cfSiteErr
}

func (b *stubBroker) Confluence(_ context.Context) (string, string, error) {
	b.callCounts["confluence"]++
	return b.cfEmail, b.cfToken, b.cfErr
}

func (b *stubBroker) Notion(_ context.Context) (string, error) {
	b.callCounts["notion"]++
	return b.notion, b.notionErr
}

// ─── Take tests ───────────────────────────────────────────────────────────────

func TestTake_CollectsAllCredentials(t *testing.T) {
	b := newStubBroker("gh-token", "gl-token", "user@example.com", "cf-token", "notion-token")

	snap, err := credentials.Take(context.Background(), b)
	if err != nil {
		t.Fatalf("Take returned unexpected error: %v", err)
	}

	if snap.GitHubToken != "gh-token" {
		t.Errorf("GitHubToken = %q, want %q", snap.GitHubToken, "gh-token")
	}
	if snap.GitLabToken != "gl-token" {
		t.Errorf("GitLabToken = %q, want %q", snap.GitLabToken, "gl-token")
	}
	if snap.ConfluenceSite != "testsite" {
		t.Errorf("ConfluenceSite = %q, want %q", snap.ConfluenceSite, "testsite")
	}
	if snap.ConfluenceEmail != "user@example.com" {
		t.Errorf("ConfluenceEmail = %q, want %q", snap.ConfluenceEmail, "user@example.com")
	}
	if snap.ConfluenceToken != "cf-token" {
		t.Errorf("ConfluenceToken = %q, want %q", snap.ConfluenceToken, "cf-token")
	}
	if snap.NotionToken != "notion-token" {
		t.Errorf("NotionToken = %q, want %q", snap.NotionToken, "notion-token")
	}
}

func TestTake_CallsEachBrokerMethodOnce(t *testing.T) {
	b := newStubBroker("g", "l", "e", "t", "n")
	_, err := credentials.Take(context.Background(), b)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}

	for _, method := range []string{"github", "gitlab", "confluencesite", "confluence", "notion"} {
		if b.callCounts[method] != 1 {
			t.Errorf("broker.%s called %d times, want 1", method, b.callCounts[method])
		}
	}
}

func TestTake_ErrorOnGitHub_ReturnsZeroSnapshot(t *testing.T) {
	b := newStubBroker("", "gl", "e", "t", "n")
	b.githubErr = errors.New("vault unavailable")

	snap, err := credentials.Take(context.Background(), b)
	if err == nil {
		t.Fatal("Take should have returned an error")
	}
	// Zero snapshot — no partial credentials leaked.
	if snap != (credentials.Snapshot{}) {
		t.Errorf("expected zero Snapshot on error, got %+v", snap)
	}
}

func TestTake_ErrorOnConfluence_ReturnsZeroSnapshot(t *testing.T) {
	b := newStubBroker("gh", "gl", "", "", "n")
	b.cfErr = errors.New("token expired")

	snap, err := credentials.Take(context.Background(), b)
	if err == nil {
		t.Fatal("Take should have returned an error")
	}
	if snap != (credentials.Snapshot{}) {
		t.Errorf("expected zero Snapshot on error, got %+v", snap)
	}
}

func TestTake_ErrorOnNotion_ReturnsZeroSnapshot(t *testing.T) {
	b := newStubBroker("gh", "gl", "e", "t", "")
	b.notionErr = errors.New("connection refused")

	snap, err := credentials.Take(context.Background(), b)
	if err == nil {
		t.Fatal("Take should have returned an error")
	}
	if snap != (credentials.Snapshot{}) {
		t.Errorf("expected zero Snapshot on error, got %+v", snap)
	}
}

// ─── Snapshot value semantics ─────────────────────────────────────────────────

// TestSnapshot_ValueSemantics verifies that modifying a copy of a Snapshot does
// not affect the original. This documents the immutability guarantee callers rely on.
func TestSnapshot_ValueSemantics(t *testing.T) {
	original := credentials.Snapshot{
		GitHubToken:     "original-gh",
		GitLabToken:     "original-gl",
		ConfluenceEmail: "orig@example.com",
		ConfluenceToken: "original-cf",
		NotionToken:     "original-nt",
	}

	copy := original
	copy.GitHubToken = "mutated"
	copy.ConfluenceEmail = "mutated@example.com"

	if original.GitHubToken != "original-gh" {
		t.Errorf("original.GitHubToken mutated to %q", original.GitHubToken)
	}
	if original.ConfluenceEmail != "orig@example.com" {
		t.Errorf("original.ConfluenceEmail mutated to %q", original.ConfluenceEmail)
	}
}

// ─── ResolverBroker tests ─────────────────────────────────────────────────────

func newTestResolver(gh, gl, cfEmail, cfToken, nt string) *livingwiki.Resolver {
	store := livingwiki.NewMemStore()
	enabled := true
	_ = store.Set(context.Background(), &livingwiki.Settings{
		Enabled:         &enabled,
		GitHubToken:     gh,
		GitLabToken:     gl,
		ConfluenceEmail: cfEmail,
		ConfluenceToken: cfToken,
		NotionToken:     nt,
	})
	return livingwiki.NewResolver(store, livingwiki.EnvConfig{}, time.Millisecond*10)
}

func TestResolverBroker_ReturnsResolvedCredentials(t *testing.T) {
	r := newTestResolver("gh-tok", "gl-tok", "cf@example.com", "cf-tok", "nt-tok")
	b := credentials.NewResolverBroker(r)

	snap, err := credentials.Take(context.Background(), b)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}

	if snap.GitHubToken != "gh-tok" {
		t.Errorf("GitHubToken = %q, want %q", snap.GitHubToken, "gh-tok")
	}
	if snap.ConfluenceEmail != "cf@example.com" {
		t.Errorf("ConfluenceEmail = %q, want %q", snap.ConfluenceEmail, "cf@example.com")
	}
	if snap.NotionToken != "nt-tok" {
		t.Errorf("NotionToken = %q, want %q", snap.NotionToken, "nt-tok")
	}
}

// TestResolverBroker_CacheRespected verifies that the resolver's TTL cache is
// used: repeated Broker calls within the TTL window should not increment the
// underlying store's Get call count. We test this indirectly by confirming the
// resolver returns a consistent value without errors across rapid successive calls.
func TestResolverBroker_CacheRespected(t *testing.T) {
	store := livingwiki.NewMemStore()
	enabled := true
	_ = store.Set(context.Background(), &livingwiki.Settings{
		Enabled:     &enabled,
		GitHubToken: "cached-token",
	})
	// Short TTL but long enough for the test.
	resolver := livingwiki.NewResolver(store, livingwiki.EnvConfig{}, 5*time.Second)
	b := credentials.NewResolverBroker(resolver)

	for i := 0; i < 10; i++ {
		tok, err := b.GitHub(context.Background())
		if err != nil {
			t.Fatalf("GitHub call %d: %v", i, err)
		}
		if tok != "cached-token" {
			t.Errorf("GitHub call %d: got %q, want %q", i, tok, "cached-token")
		}
	}
}

// TestResolverBroker_RespectsInvalidation verifies that after Resolver.Invalidate
// and a token rotation, a fresh Take reflects the new value.
// This exercises the at-most-one-rotation-per-job guarantee from the other side:
// the rotation is visible immediately after invalidation.
func TestResolverBroker_RespectsInvalidation(t *testing.T) {
	store := livingwiki.NewMemStore()
	enabled := true
	_ = store.Set(context.Background(), &livingwiki.Settings{
		Enabled:         &enabled,
		ConfluenceEmail: "old@example.com",
		ConfluenceToken: "old-token",
	})
	// Use a long TTL so the cache stays warm between calls.
	resolver := livingwiki.NewResolver(store, livingwiki.EnvConfig{}, 30*time.Second)
	b := credentials.NewResolverBroker(resolver)

	// First snapshot — old credentials.
	snap1, err := credentials.Take(context.Background(), b)
	if err != nil {
		t.Fatalf("Take (before rotation): %v", err)
	}
	if snap1.ConfluenceToken != "old-token" {
		t.Errorf("snap1.ConfluenceToken = %q, want %q", snap1.ConfluenceToken, "old-token")
	}

	// Simulate a credential rotation: update store and invalidate cache.
	_ = store.Set(context.Background(), &livingwiki.Settings{
		Enabled:         &enabled,
		ConfluenceEmail: "new@example.com",
		ConfluenceToken: "new-token",
	})
	resolver.Invalidate()

	// Second snapshot — new credentials.
	snap2, err := credentials.Take(context.Background(), b)
	if err != nil {
		t.Fatalf("Take (after rotation): %v", err)
	}
	if snap2.ConfluenceToken != "new-token" {
		t.Errorf("snap2.ConfluenceToken = %q, want %q", snap2.ConfluenceToken, "new-token")
	}

	// Original snapshot is unaffected (value semantics).
	if snap1.ConfluenceToken != "old-token" {
		t.Errorf("snap1 was mutated; ConfluenceToken = %q", snap1.ConfluenceToken)
	}
}
