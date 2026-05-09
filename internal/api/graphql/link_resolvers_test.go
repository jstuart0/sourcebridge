// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"sync"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/events"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// recordingBus wraps events.Bus and records every event type published.
type recordingBus struct {
	bus *events.Bus
	mu  sync.Mutex
	log []events.Event
}

func newRecordingBus() *recordingBus {
	rb := &recordingBus{bus: events.NewBus()}
	rb.bus.Subscribe("*", func(e events.Event) {
		rb.mu.Lock()
		defer rb.mu.Unlock()
		rb.log = append(rb.log, e)
	})
	return rb
}

func (rb *recordingBus) calls() []events.Event {
	rb.bus.Shutdown(context.Background()) //nolint:errcheck // best-effort flush
	rb.mu.Lock()
	defer rb.mu.Unlock()
	out := make([]events.Event, len(rb.log))
	copy(out, rb.log)
	return out
}

// newResolverWithRepoAndLink sets up an in-memory store with a seeded repo,
// requirement, symbol, and link. Returns the resolver, the link ID, and the
// repo ID.
func newResolverWithRepoAndLink(t *testing.T, rb *recordingBus) (*Resolver, string, string) {
	t.Helper()
	store := graphstore.NewStore()

	// Seed the repo.
	result := &indexer.IndexResult{
		RepoName: "link-test-repo",
		RepoPath: "/tmp/link-test",
		Files: []indexer.FileResult{
			{
				Path:     "main.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{
						ID:            "sym-link-test",
						Name:          "FuncA",
						QualifiedName: "pkg.FuncA",
						Kind:          indexer.SymbolFunction,
						Language:      "go",
						FilePath:      "main.go",
						StartLine:     1,
						EndLine:       5,
					},
				},
			},
		},
	}
	repo, err := store.StoreIndexResult(result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	// Seed a requirement.
	store.StoreRequirement(repo.ID, &graphstore.StoredRequirement{
		ID:         "req-link-test",
		ExternalID: "LT-001",
		Title:      "Link test req",
	})

	// Find the stored symbol.
	syms := store.GetSymbolsByFile(repo.ID, "main.go")
	if len(syms) == 0 {
		t.Fatal("no symbols found after StoreIndexResult")
	}

	// Seed a link.
	link := store.StoreLink(repo.ID, &graphstore.StoredLink{
		RepoID:        repo.ID,
		RequirementID: "req-link-test",
		SymbolID:      syms[0].ID,
		Confidence:    0.9,
		Source:        "test",
	})
	if link == nil {
		t.Fatal("StoreLink returned nil")
	}

	r := &Resolver{
		Store:    store,
		EventBus: rb.bus,
	}
	return r, link.ID, repo.ID
}

// T44: cross-tenant VerifyLink → no EventLinkVerified leak (CA-205 dual-layer defense).
//
// When getStore returns nil from VerifyLink (store returns nil for cross-tenant
// links), the resolver must NOT publish EventLinkVerified or EventLinkRejected.
func TestVerifyLink_CrossTenant_NoEventPublished(t *testing.T) {
	rb := newRecordingBus()
	store := graphstore.NewStore()
	r := &Resolver{
		Store:    store,
		EventBus: rb.bus,
	}

	// Drive VerifyLink with a non-existent link ID — the in-memory store
	// returns nil for unknown IDs, matching the TenantFilteredStore behavior
	// for cross-tenant links (CA-203).
	mr := &mutationResolver{Resolver: r}
	_, err := mr.VerifyLink(context.Background(), "nonexistent-link-id", true)
	if err == nil {
		t.Fatal("expected error for non-existent link, got nil")
	}

	published := rb.calls()
	for _, e := range published {
		if e.Type == events.EventLinkVerified || e.Type == events.EventLinkRejected {
			t.Errorf("T44: unexpected event %q published when VerifyLink returned nil", e.Type)
		}
	}
}

// T46a: VerifyLink success → EventLinkVerified contains link_id and repo_id.
func TestVerifyLink_Success_EventPayloadContainsLinkIDAndRepoID(t *testing.T) {
	rb := newRecordingBus()
	r, linkID, repoID := newResolverWithRepoAndLink(t, rb)

	mr := &mutationResolver{Resolver: r}
	result, err := mr.VerifyLink(context.Background(), linkID, true)
	if err != nil {
		t.Fatalf("VerifyLink: unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil RequirementLink")
	}

	published := rb.calls()
	var found *events.Event
	for i := range published {
		if published[i].Type == events.EventLinkVerified {
			found = &published[i]
			break
		}
	}
	if found == nil {
		t.Fatal("T46: EventLinkVerified not published after successful VerifyLink")
	}
	gotLinkID, _ := found.Data["link_id"].(string)
	gotRepoID, _ := found.Data["repo_id"].(string)
	if gotLinkID != linkID {
		t.Errorf("T46: link_id: want %q, got %q", linkID, gotLinkID)
	}
	if gotRepoID != repoID {
		t.Errorf("T46: repo_id: want %q, got %q", repoID, gotRepoID)
	}
}

// T46b: RejectLink (verified=false) → EventLinkRejected contains link_id and repo_id.
func TestVerifyLink_Reject_EventPayloadContainsLinkIDAndRepoID(t *testing.T) {
	rb := newRecordingBus()
	r, linkID, repoID := newResolverWithRepoAndLink(t, rb)

	mr := &mutationResolver{Resolver: r}
	result, err := mr.VerifyLink(context.Background(), linkID, false)
	if err != nil {
		t.Fatalf("VerifyLink(reject): unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil RequirementLink")
	}

	published := rb.calls()
	var found *events.Event
	for i := range published {
		if published[i].Type == events.EventLinkRejected {
			found = &published[i]
			break
		}
	}
	if found == nil {
		t.Fatal("T46: EventLinkRejected not published after VerifyLink(verified=false)")
	}
	gotLinkID, _ := found.Data["link_id"].(string)
	gotRepoID, _ := found.Data["repo_id"].(string)
	if gotLinkID != linkID {
		t.Errorf("T46: link_id: want %q, got %q", linkID, gotLinkID)
	}
	if gotRepoID != repoID {
		t.Errorf("T46: repo_id: want %q, got %q", repoID, gotRepoID)
	}
}
