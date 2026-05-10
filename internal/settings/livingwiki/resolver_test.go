// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package livingwiki

import (
	"context"
	"testing"
	"time"
)

func TestResolver_Defaults(t *testing.T) {
	store := NewMemStore()
	r := NewResolver(store, EnvConfig{}, 0)

	res, err := r.Get()
	if err != nil {
		t.Fatal(err)
	}
	if res.Enabled {
		t.Error("expected Enabled=false by default")
	}
	if res.WorkerCount != 4 {
		t.Errorf("expected WorkerCount=4, got %d", res.WorkerCount)
	}
	if res.EventTimeout != 5*time.Minute {
		t.Errorf("expected EventTimeout=5m, got %s", res.EventTimeout)
	}
}

func TestResolver_EnvFallback(t *testing.T) {
	store := NewMemStore()
	env := EnvConfig{
		Enabled:                 true,
		WorkerCount:             8,
		EventTimeout:            "10m",
		ConfluenceWebhookSecret: "env-secret",
	}
	r := NewResolver(store, env, 0)

	res, err := r.Get()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Enabled {
		t.Error("expected Enabled=true from env")
	}
	if res.WorkerCount != 8 {
		t.Errorf("expected WorkerCount=8, got %d", res.WorkerCount)
	}
	if res.EventTimeout != 10*time.Minute {
		t.Errorf("expected EventTimeout=10m, got %s", res.EventTimeout)
	}
	if res.ConfluenceWebhookSecret != "env-secret" {
		t.Errorf("expected confluence secret from env, got %q", res.ConfluenceWebhookSecret)
	}
}

func TestResolver_UIOverridesEnv(t *testing.T) {
	store := NewMemStore()
	env := EnvConfig{
		Enabled:     false,
		WorkerCount: 8,
	}
	r := NewResolver(store, env, 0)

	enabled := true
	_ = store.Set(context.Background(), &Settings{
		Enabled:     &enabled,
		WorkerCount: 2,
		GitHubToken: "ui-token",
	})
	r.Invalidate()

	res, err := r.Get()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Enabled {
		t.Error("expected Enabled=true from UI override")
	}
	if res.WorkerCount != 2 {
		t.Errorf("expected WorkerCount=2 from UI, got %d", res.WorkerCount)
	}
	if res.GitHubToken != "ui-token" {
		t.Errorf("expected GitHubToken from UI, got %q", res.GitHubToken)
	}
}

func TestResolver_Cache(t *testing.T) {
	store := NewMemStore()
	r := NewResolver(store, EnvConfig{}, 5*time.Second)

	// First call populates cache
	_, err := r.Get()
	if err != nil {
		t.Fatal(err)
	}

	// Change backing store without invalidating cache
	wc := true
	_ = store.Set(context.Background(), &Settings{Enabled: &wc, WorkerCount: 99})

	// Cache should return old value
	res, _ := r.Get()
	if res.WorkerCount == 99 {
		t.Error("expected cached value, not fresh read")
	}

	// After invalidation, fresh read
	r.Invalidate()
	res, _ = r.Get()
	if res.WorkerCount != 99 {
		t.Errorf("expected WorkerCount=99 after invalidation, got %d", res.WorkerCount)
	}
}

func TestMaskSecrets(t *testing.T) {
	s := Settings{
		GitHubToken:             "ghp_realtoken",
		GitLabToken:             "glpat-realtoken",
		ConfluenceEmail:         "user@example.com",
		ConfluenceToken:         "attoken",
		NotionToken:             "secret_notion",
		ConfluenceWebhookSecret: "hmac-secret",
		NotionWebhookSecret:     "notion-secret",
	}
	masked := MaskSecrets(s)
	for _, v := range []string{
		masked.GitHubToken,
		masked.GitLabToken,
		masked.ConfluenceEmail,
		masked.ConfluenceToken,
		masked.NotionToken,
		masked.ConfluenceWebhookSecret,
		masked.NotionWebhookSecret,
	} {
		if v != SecretSentinel {
			t.Errorf("expected sentinel %q, got %q", SecretSentinel, v)
		}
	}
}

func TestMemStore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()

	// Empty get
	s, err := store.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("expected non-nil empty settings")
	}

	// Set + get
	enabled := true
	in := &Settings{
		Enabled:     &enabled,
		WorkerCount: 6,
		GitHubToken: "token123",
	}
	if err := store.Set(ctx, in); err != nil {
		t.Fatal(err)
	}

	out, err := store.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if out.GitHubToken != "token123" {
		t.Errorf("expected token123, got %q", out.GitHubToken)
	}
	if out.WorkerCount != 6 {
		t.Errorf("expected WorkerCount=6, got %d", out.WorkerCount)
	}
}
