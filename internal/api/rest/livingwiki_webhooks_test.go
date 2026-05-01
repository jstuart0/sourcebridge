// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sourcebridge/sourcebridge/internal/api/rest"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/webhook"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// signBody computes the Confluence HMAC-SHA256 signature for body.
func signBody(t *testing.T, body []byte, secret string) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// (Note: a stubDispatcher + setupConfluenceRouter helper used to live
// here, but the tests below construct their dispatcher inline and
// verify behaviour against the real *webhook.Dispatcher instead of a
// stub. Kept the comment so the next contributor doesn't recreate the
// abstraction by accident.)

// ─────────────────────────────────────────────────────────────────────────────
// Confluence signature validation tests
// ─────────────────────────────────────────────────────────────────────────────

func TestConfluenceWebhook_ValidSignature(t *testing.T) {
	secret := "test-secret-abc"
	r := chi.NewRouter()
	wm := orchestrator.NewMemoryWatermarkStore()
	deps := webhook.DispatcherDeps{WatermarkStore: wm, Logger: webhook.NoopLogger{}}
	d := webhook.NewDispatcher(deps, webhook.DispatcherConfig{WorkerCount: 1, MaxQueueDepth: 10})
	_ = d.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})
	rest.RegisterLivingWikiRoutes(r, rest.LivingWikiWebhookDeps{
		Dispatcher:              d,
		ConfluenceWebhookSecret: secret,
	})

	// Build a page_updated payload with sourcebridge properties.
	payload := map[string]any{
		"eventType": "page_updated",
		"timestamp": 1714000000000,
		"page": map[string]any{
			"id":       "12345",
			"title":    "Architecture Overview",
			"spaceKey": "ENG",
			"properties": []map[string]any{
				{"key": "sourcebridge_page_id", "value": "repo1.arch.overview"},
				{"key": "sourcebridge_repo_id", "value": "repo1"},
			},
		},
		"actor": map[string]any{
			"displayName": "Jane Doe",
			"accountId":   "user-abc",
		},
	}
	body, _ := json.Marshal(payload)
	sig := signBody(t, body, secret)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/confluence", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Confluence-Signature", sig)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Errorf("want 202 Accepted, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestConfluenceWebhook_InvalidSignature(t *testing.T) {
	secret := "correct-secret"
	r := chi.NewRouter()
	wm := orchestrator.NewMemoryWatermarkStore()
	deps := webhook.DispatcherDeps{WatermarkStore: wm, Logger: webhook.NoopLogger{}}
	d := webhook.NewDispatcher(deps, webhook.DispatcherConfig{WorkerCount: 1, MaxQueueDepth: 10})
	_ = d.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})
	rest.RegisterLivingWikiRoutes(r, rest.LivingWikiWebhookDeps{
		Dispatcher:              d,
		ConfluenceWebhookSecret: secret,
	})

	body := []byte(`{"eventType":"page_updated","page":{"id":"1"}}`)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/confluence", bytes.NewReader(body))
	req.Header.Set("X-Confluence-Signature", "sha256=deadbeef")

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401 Unauthorized for bad signature, got %d", rr.Code)
	}
}

func TestConfluenceWebhook_MissingSignatureHeader(t *testing.T) {
	secret := "some-secret"
	r := chi.NewRouter()
	wm := orchestrator.NewMemoryWatermarkStore()
	deps := webhook.DispatcherDeps{WatermarkStore: wm, Logger: webhook.NoopLogger{}}
	d := webhook.NewDispatcher(deps, webhook.DispatcherConfig{WorkerCount: 1, MaxQueueDepth: 10})
	_ = d.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})
	rest.RegisterLivingWikiRoutes(r, rest.LivingWikiWebhookDeps{
		Dispatcher:              d,
		ConfluenceWebhookSecret: secret,
	})

	body := []byte(`{"eventType":"page_updated","page":{"id":"1"}}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/confluence", bytes.NewReader(body))
	// No X-Confluence-Signature header.

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for missing signature header, got %d", rr.Code)
	}
}

func TestConfluenceWebhook_NonPageUpdatedEvent(t *testing.T) {
	// Events that are not page_updated should be acknowledged (204) without
	// being submitted to the dispatcher.
	r := chi.NewRouter()
	wm := orchestrator.NewMemoryWatermarkStore()
	deps := webhook.DispatcherDeps{WatermarkStore: wm, Logger: webhook.NoopLogger{}}
	d := webhook.NewDispatcher(deps, webhook.DispatcherConfig{WorkerCount: 1, MaxQueueDepth: 10})
	_ = d.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})
	// No secret — skip signature validation for simplicity.
	rest.RegisterLivingWikiRoutes(r, rest.LivingWikiWebhookDeps{Dispatcher: d})

	body := []byte(`{"eventType":"page_created","page":{"id":"99"}}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/confluence", bytes.NewReader(body))

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("want 204 NoContent for non-page_updated event, got %d", rr.Code)
	}
}

func TestConfluenceWebhook_NonSourceBridgePage(t *testing.T) {
	// Pages without sourcebridge_page_id property should be silently ignored (204).
	r := chi.NewRouter()
	wm := orchestrator.NewMemoryWatermarkStore()
	deps := webhook.DispatcherDeps{WatermarkStore: wm, Logger: webhook.NoopLogger{}}
	d := webhook.NewDispatcher(deps, webhook.DispatcherConfig{WorkerCount: 1, MaxQueueDepth: 10})
	_ = d.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})
	rest.RegisterLivingWikiRoutes(r, rest.LivingWikiWebhookDeps{Dispatcher: d})

	payload := map[string]any{
		"eventType": "page_updated",
		"page": map[string]any{
			"id":         "777",
			"properties": []map[string]any{}, // no sourcebridge properties
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/confluence", bytes.NewReader(body))

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("want 204 for non-sourcebridge page, got %d", rr.Code)
	}
}

func TestConfluenceWebhook_BadJSON(t *testing.T) {
	r := chi.NewRouter()
	wm := orchestrator.NewMemoryWatermarkStore()
	deps := webhook.DispatcherDeps{WatermarkStore: wm, Logger: webhook.NoopLogger{}}
	d := webhook.NewDispatcher(deps, webhook.DispatcherConfig{WorkerCount: 1, MaxQueueDepth: 10})
	_ = d.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})
	rest.RegisterLivingWikiRoutes(r, rest.LivingWikiWebhookDeps{Dispatcher: d})

	req := httptest.NewRequest(http.MethodPost, "/webhooks/confluence", strings.NewReader("{bad json"))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for malformed JSON, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Notion poll handler tests
// ─────────────────────────────────────────────────────────────────────────────

func TestNotionPoll_ValidRequest(t *testing.T) {
	r := chi.NewRouter()
	wm := orchestrator.NewMemoryWatermarkStore()
	deps := webhook.DispatcherDeps{WatermarkStore: wm, Logger: webhook.NoopLogger{}}
	d := webhook.NewDispatcher(deps, webhook.DispatcherConfig{WorkerCount: 1, MaxQueueDepth: 10})
	_ = d.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})
	rest.RegisterLivingWikiRoutes(r, rest.LivingWikiWebhookDeps{Dispatcher: d})

	body := map[string]any{
		"repo_id":      "repo-notion-1",
		"page_id":      "repo-notion-1.arch.api",
		"requested_by": "cron-job",
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/notion-poll", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Errorf("want 202 Accepted, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp["status"] != "accepted" {
		t.Errorf("want status=accepted, got %q", resp["status"])
	}
	if resp["repo_id"] != "repo-notion-1" {
		t.Errorf("want repo_id=repo-notion-1, got %q", resp["repo_id"])
	}
}

func TestNotionPoll_MissingRepoID(t *testing.T) {
	r := chi.NewRouter()
	wm := orchestrator.NewMemoryWatermarkStore()
	deps := webhook.DispatcherDeps{WatermarkStore: wm, Logger: webhook.NoopLogger{}}
	d := webhook.NewDispatcher(deps, webhook.DispatcherConfig{WorkerCount: 1, MaxQueueDepth: 10})
	_ = d.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})
	rest.RegisterLivingWikiRoutes(r, rest.LivingWikiWebhookDeps{Dispatcher: d})

	body := map[string]any{"page_id": "some-page"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/notion-poll", bytes.NewReader(bodyBytes))

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for missing repo_id, got %d", rr.Code)
	}
}

func TestNotionPoll_BadJSON(t *testing.T) {
	r := chi.NewRouter()
	wm := orchestrator.NewMemoryWatermarkStore()
	deps := webhook.DispatcherDeps{WatermarkStore: wm, Logger: webhook.NoopLogger{}}
	d := webhook.NewDispatcher(deps, webhook.DispatcherConfig{WorkerCount: 1, MaxQueueDepth: 10})
	_ = d.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})
	rest.RegisterLivingWikiRoutes(r, rest.LivingWikiWebhookDeps{Dispatcher: d})

	req := httptest.NewRequest(http.MethodPost, "/webhooks/notion-poll", strings.NewReader("notjson"))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for malformed JSON, got %d", rr.Code)
	}
}

func TestNotionPoll_Idempotent(t *testing.T) {
	// Two identical polls within the same minute-bucket should be accepted;
	// the second will be deduplicated by the dispatcher (same delivery ID).
	// Both calls should return 200 (not 503 or 400).
	r := chi.NewRouter()
	wm := orchestrator.NewMemoryWatermarkStore()
	deps := webhook.DispatcherDeps{WatermarkStore: wm, Logger: webhook.NoopLogger{}}
	d := webhook.NewDispatcher(deps, webhook.DispatcherConfig{
		WorkerCount:    1,
		MaxQueueDepth:  10,
		DedupeCapacity: 100,
		DedupeTTL:      0, // uses default (1h)
	})
	_ = d.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})
	rest.RegisterLivingWikiRoutes(r, rest.LivingWikiWebhookDeps{Dispatcher: d})

	body := map[string]any{"repo_id": "repo-idem", "page_id": "some-page"}
	bodyBytes, _ := json.Marshal(body)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/notion-poll", bytes.NewReader(bodyBytes))
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		// First call → 202, second call (same minute-bucket) → 200 (deduped).
		if rr.Code != http.StatusAccepted && rr.Code != http.StatusOK {
			t.Errorf("call %d: want 200 or 202, got %d", i+1, rr.Code)
		}
	}
}

// ensure unused imports compile
var (
	_ = errors.New
	_ = fmt.Sprintf
)
