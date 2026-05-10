// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package livingwiki provides persistence and resolution for the living-wiki
// feature configuration.
//
// # Precedence (highest wins)
//
//  1. UI-stored value (persisted via [Store])
//  2. Environment variable / config.toml value (EnvConfig fallback)
//  3. Built-in default
//
// Resolved values are cached for up to 30 seconds so the DB is not queried on
// every webhook delivery. Call [Resolver.Invalidate] after a successful [Store.Set]
// to force an immediate refresh.
package livingwiki

import (
	"context"
	"os"
	"strconv"
	"sync"
	"time"
)

// EnvConfig carries the env-var / config.toml values that act as fallback
// when the UI has not set a value. Populate this from config.LivingWikiConfig
// at startup.
type EnvConfig struct {
	Enabled                 bool
	WorkerCount             int
	EventTimeout            string
	ConfluenceWebhookSecret string
	NotionWebhookSecret     string

	// Source integration credentials read from env at startup.
	// These are the legacy env-var values; the UI overrides them.
	GitHubToken     string
	GitLabToken     string
	ConfluenceSite  string
	ConfluenceEmail string
	ConfluenceToken string
	NotionToken     string
}

// Resolved is the fully merged, ready-to-use configuration the living-wiki
// code actually consumes. Every field has a concrete value (no optionals).
type Resolved struct {
	Enabled      bool
	WorkerCount  int
	EventTimeout time.Duration

	GitHubToken     string
	GitLabToken     string
	ConfluenceSite  string
	ConfluenceEmail string
	ConfluenceToken string
	NotionToken     string

	ConfluenceWebhookSecret string
	NotionWebhookSecret     string
}

// Resolver merges UI-stored settings with env-var fallbacks.
// It caches results for ~30 seconds to avoid hammering the DB on every event.
type Resolver struct {
	store Store
	env   EnvConfig
	ttl   time.Duration

	mu       sync.Mutex
	cached   *Resolved
	cachedAt time.Time
}

// NewResolver creates a Resolver. ttl is the cache duration; pass 0 to use
// the default of 30 seconds.
func NewResolver(store Store, env EnvConfig, ttl time.Duration) *Resolver {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &Resolver{store: store, env: env, ttl: ttl}
}

// Invalidate clears the cached resolved value, forcing the next [Get] call to
// re-read from the store. Call this after a successful [Store.Set].
func (r *Resolver) Invalidate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cached = nil
}

// Get returns the resolved living-wiki configuration, merging UI settings over
// env-var fallbacks over built-in defaults.
func (r *Resolver) Get() (*Resolved, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cached != nil && time.Since(r.cachedAt) < r.ttl {
		cp := *r.cached
		return &cp, nil
	}

	ui, err := r.store.Get(context.Background())
	if err != nil {
		return nil, err
	}

	resolved := r.merge(ui)
	r.cached = resolved
	r.cachedAt = time.Now()

	cp := *resolved
	return &cp, nil
}

// merge applies UI > env > default precedence for every field.
func (r *Resolver) merge(ui *Settings) *Resolved {
	out := &Resolved{}

	// --- Enabled ---
	if ui.Enabled != nil {
		out.Enabled = *ui.Enabled
	} else if v := os.Getenv("SOURCEBRIDGE_LIVING_WIKI_ENABLED"); v != "" {
		out.Enabled, _ = strconv.ParseBool(v)
	} else {
		out.Enabled = r.env.Enabled
	}

	// --- WorkerCount ---
	if ui.WorkerCount > 0 {
		out.WorkerCount = ui.WorkerCount
	} else if v := os.Getenv("SOURCEBRIDGE_LIVING_WIKI_WORKER_COUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			out.WorkerCount = n
		}
	} else if r.env.WorkerCount > 0 {
		out.WorkerCount = r.env.WorkerCount
	} else {
		out.WorkerCount = 4
	}

	// --- EventTimeout ---
	rawTimeout := ""
	if ui.EventTimeout != "" {
		rawTimeout = ui.EventTimeout
	} else if v := os.Getenv("SOURCEBRIDGE_LIVING_WIKI_EVENT_TIMEOUT"); v != "" {
		rawTimeout = v
	} else if r.env.EventTimeout != "" {
		rawTimeout = r.env.EventTimeout
	}
	if d, err := time.ParseDuration(rawTimeout); err == nil && d > 0 {
		out.EventTimeout = d
	} else {
		out.EventTimeout = 5 * time.Minute
	}

	// --- GitHubToken ---
	out.GitHubToken = firstNonEmpty(
		ui.GitHubToken,
		os.Getenv("SOURCEBRIDGE_LIVING_WIKI_GITHUB_TOKEN"),
		r.env.GitHubToken,
	)

	// --- GitLabToken ---
	out.GitLabToken = firstNonEmpty(
		ui.GitLabToken,
		os.Getenv("SOURCEBRIDGE_LIVING_WIKI_GITLAB_TOKEN"),
		r.env.GitLabToken,
	)

	// --- ConfluenceSite ---
	out.ConfluenceSite = firstNonEmpty(
		ui.ConfluenceSite,
		os.Getenv("SOURCEBRIDGE_LIVING_WIKI_CONFLUENCE_SITE"),
		r.env.ConfluenceSite,
	)

	// --- ConfluenceEmail ---
	out.ConfluenceEmail = firstNonEmpty(
		ui.ConfluenceEmail,
		os.Getenv("SOURCEBRIDGE_LIVING_WIKI_CONFLUENCE_EMAIL"),
		r.env.ConfluenceEmail,
	)

	// --- ConfluenceToken ---
	out.ConfluenceToken = firstNonEmpty(
		ui.ConfluenceToken,
		os.Getenv("SOURCEBRIDGE_LIVING_WIKI_CONFLUENCE_TOKEN"),
		r.env.ConfluenceToken,
	)

	// --- NotionToken ---
	out.NotionToken = firstNonEmpty(
		ui.NotionToken,
		os.Getenv("SOURCEBRIDGE_LIVING_WIKI_NOTION_TOKEN"),
		r.env.NotionToken,
	)

	// --- ConfluenceWebhookSecret ---
	out.ConfluenceWebhookSecret = firstNonEmpty(
		ui.ConfluenceWebhookSecret,
		os.Getenv("SOURCEBRIDGE_LIVING_WIKI_CONFLUENCE_WEBHOOK_SECRET"),
		r.env.ConfluenceWebhookSecret,
	)

	// --- NotionWebhookSecret ---
	out.NotionWebhookSecret = firstNonEmpty(
		ui.NotionWebhookSecret,
		os.Getenv("SOURCEBRIDGE_LIVING_WIKI_NOTION_WEBHOOK_SECRET"),
		r.env.NotionWebhookSecret,
	)

	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
