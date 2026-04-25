// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package markdown

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
)

// confluenceTestServer builds a minimal Confluence Cloud mock that understands:
//   - GET /wiki/api/v2/pages?title=… → search results
//   - GET /wiki/api/v2/pages/{id}/properties/{key} → property read
//   - PUT /wiki/api/v2/pages/{id}/properties/{key} → property write
//   - GET /wiki/api/v2/pages/{id}?body-format=storage → page body
//   - POST /wiki/api/v2/pages → create
//   - PUT /wiki/api/v2/pages/{id} → update
//
// It stores pages in a simple map keyed by external ID.
type confluenceTestServer struct {
	t        *testing.T
	pages    map[string]string // externalID → confluence page ID
	bodies   map[string]string // confluence page ID → XHTML body
	versions map[string]int    // confluence page ID → version
}

func newConfluenceTestServer(t *testing.T) (*confluenceTestServer, *httptest.Server) {
	t.Helper()
	cts := &confluenceTestServer{
		t:        t,
		pages:    make(map[string]string),
		bodies:   make(map[string]string),
		versions: make(map[string]int),
	}
	srv := httptest.NewServer(http.HandlerFunc(cts.handle))
	return cts, srv
}

func (cts *confluenceTestServer) handle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	switch {
	// Search pages by title.
	case r.Method == http.MethodGet && strings.Contains(path, "/pages") && r.URL.Query().Get("title") != "":
		title := r.URL.Query().Get("title")
		type result struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}
		var results []result
		if id, ok := cts.pages[title]; ok {
			results = append(results, result{ID: id, Title: title})
		}
		writeConfluenceJSON(w, http.StatusOK, map[string]interface{}{"results": results})

	// Get page property.
	case r.Method == http.MethodGet && strings.Contains(path, "/properties/"):
		parts := strings.Split(path, "/properties/")
		if len(parts) < 2 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		pageID := extractConfluencePageID(parts[0])
		key := parts[1]
		// The property value IS the external ID.
		var externalID string
		for eid, id := range cts.pages {
			if id == pageID && key == confluencePropertyKey {
				externalID = eid
				break
			}
		}
		if externalID == "" {
			writeConfluenceJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
			return
		}
		writeConfluenceJSON(w, http.StatusOK, map[string]string{"key": key, "value": externalID})

	// Get page properties list.
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/properties"):
		pageID := extractConfluencePageID(strings.TrimSuffix(path, "/properties"))
		var props []map[string]string
		for eid, id := range cts.pages {
			if id == pageID {
				props = append(props, map[string]string{"key": confluencePropertyKey, "value": eid})
				break
			}
		}
		writeConfluenceJSON(w, http.StatusOK, map[string]interface{}{"results": props})

	// Set page property.
	case r.Method == http.MethodPut && strings.Contains(path, "/properties/"):
		w.WriteHeader(http.StatusOK)

	// Get page body.
	case r.Method == http.MethodGet && strings.Contains(path, "/pages/"):
		pageID := extractConfluencePageID(path)
		body, ok := cts.bodies[pageID]
		if !ok {
			writeConfluenceJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
			return
		}
		ver := cts.versions[pageID]
		writeConfluenceJSON(w, http.StatusOK, map[string]interface{}{
			"id": pageID,
			"body": map[string]interface{}{
				"storage": map[string]string{"value": body},
			},
			"version": map[string]int{"number": ver},
			"title":   pageID,
		})

	// Create page.
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/pages"):
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		title := payload["title"].(string)
		pageID := "pid-" + title
		body := ""
		if b, ok := payload["body"].(map[string]interface{}); ok {
			if s, ok := b["storage"].(map[string]interface{}); ok {
				body, _ = s["value"].(string)
			}
		}
		cts.pages[title] = pageID
		cts.bodies[pageID] = body
		cts.versions[pageID] = 1
		writeConfluenceJSON(w, http.StatusCreated, map[string]string{"id": pageID})

	// Update page.
	case r.Method == http.MethodPut && strings.Contains(path, "/pages/"):
		pageID := extractConfluencePageID(path)
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if b, ok := payload["body"].(map[string]interface{}); ok {
			if s, ok := b["storage"].(map[string]interface{}); ok {
				cts.bodies[pageID], _ = s["value"].(string)
			}
		}
		cts.versions[pageID]++
		w.WriteHeader(http.StatusOK)

	default:
		cts.t.Logf("confluence mock: unexpected %s %s", r.Method, path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func writeConfluenceJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// extractConfluencePageID extracts the last path component as the page ID.
func extractConfluencePageID(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/wiki/api/v2/pages/"), "/")
	// Remove query string if present.
	if idx := strings.Index(parts[0], "?"); idx >= 0 {
		parts[0] = parts[0][:idx]
	}
	return parts[0]
}

func newConfluenceHTTPClient(srv *httptest.Server) *HTTPConfluenceClient {
	return NewHTTPConfluenceClient(ConfluenceHTTPConfig{
		// Override the base URL to point at the test server.
		// We abuse the Site field since baseURL() concatenates it; instead,
		// we override the method via a custom cfg that bakes in the test URL.
		Email:        "test@example.com",
		APIToken:     "test-token",
		SpaceKey:     "ENG",
		HTTPTimeout:  5 * time.Second,
	})
}

// newConfluenceHTTPClientWithURL creates a client that hits the test server.
func newConfluenceHTTPClientWithURL(srv *httptest.Server) *HTTPConfluenceClient {
	c := &HTTPConfluenceClient{
		cfg: ConfluenceHTTPConfig{
			Site:        "testsite", // not used — we override the full URL below
			Email:       "test@example.com",
			APIToken:    "test-token",
			SpaceKey:    "ENG",
			HTTPTimeout: 5 * time.Second,
		},
		http: &http.Client{Timeout: 5 * time.Second},
	}
	// Patch the base URL to the test server. We do this by embedding a custom
	// URL-building approach: override the do method is not possible without
	// changing the struct. Instead, set a custom Site with the server URL
	// baked in by replacing the baseURL helper via a test-specific wrapper.
	// Since Site-based URL construction is internal, we use a small adapter.
	_ = c
	_ = srv
	return newTestConfluenceClient(srv.URL)
}

// newTestConfluenceClient creates an HTTPConfluenceClient that uses srv.URL as
// the API base, bypassing the Site-based URL construction.
func newTestConfluenceClient(baseURL string) *HTTPConfluenceClient {
	c := &HTTPConfluenceClient{
		cfg: ConfluenceHTTPConfig{
			Site:        "", // unused — we embed the full base URL via a wrapper
			Email:       "test@example.com",
			APIToken:    "test-token",
			SpaceKey:    "ENG",
			HTTPTimeout: 5 * time.Second,
		},
		http: &http.Client{Timeout: 5 * time.Second},
	}
	// Override the base URL by wrapping: since the struct's baseURL() relies on
	// Site, we inject a testBaseURL field via the Site field with a sentinel and
	// have the real impl handle it. Simpler: use a custom embedded URL.
	// Actually the cleanest is to set cfg.Site to a value where the resulting
	// URL points to our test server. We construct: baseURL = srv.URL + "/wiki/api/v2"
	// which is what the real impl would do for "https://site.atlassian.net/wiki/api/v2".
	// We achieve this by storing the test server URL in a field the baseURL()
	// method can return — the only way is to have a testBaseURL field, which we
	// don't want to add to prod code. Instead we use a trick:
	// Set Site such that "https://" + Site + ".atlassian.net/wiki/api/v2" != srv.URL,
	// and monkeypatch the http.Client's transport to redirect all requests to srv.URL.
	_ = baseURL
	_ = c
	// Cleanest approach: override Site to embed a fake domain and use a custom
	// http.RoundTripper that redirects calls to the test server.
	return newConfluenceClientWithTransport(baseURL)
}

// confluenceRedirectTransport redirects all requests to the test server base URL.
type confluenceRedirectTransport struct {
	base string
}

func (t *confluenceRedirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Replace the scheme+host with the test server base.
	newURL := t.base + req.URL.Path
	if req.URL.RawQuery != "" {
		newURL += "?" + req.URL.RawQuery
	}
	req2, err := http.NewRequestWithContext(req.Context(), req.Method, newURL, req.Body)
	if err != nil {
		return nil, err
	}
	req2.Header = req.Header
	return http.DefaultTransport.RoundTrip(req2)
}

func newConfluenceClientWithTransport(testBaseURL string) *HTTPConfluenceClient {
	return &HTTPConfluenceClient{
		cfg: ConfluenceHTTPConfig{
			Site:        "testsite",
			Email:       "test@example.com",
			APIToken:    "test-token",
			SpaceKey:    "ENG",
			HTTPTimeout: 5 * time.Second,
		},
		http: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &confluenceRedirectTransport{base: testBaseURL},
		},
		retryBaseDelay: time.Millisecond,
	}
}

// ─── TestHTTPConfluenceClient_GetPage_NotFound ────────────────────────────────

func TestHTTPConfluenceClient_GetPage_NotFound(t *testing.T) {
	_, srv := newConfluenceTestServer(t)
	defer srv.Close()

	client := newConfluenceClientWithTransport(srv.URL)
	xhtml, props, err := client.GetPage(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if xhtml != nil {
		t.Errorf("expected nil xhtml, got %q", xhtml)
	}
	if props != nil {
		t.Errorf("expected nil props, got %v", props)
	}
}

// ─── TestHTTPConfluenceClient_UpsertPage_Create ───────────────────────────────

func TestHTTPConfluenceClient_UpsertPage_Create(t *testing.T) {
	cts, srv := newConfluenceTestServer(t)
	defer srv.Close()

	client := newConfluenceClientWithTransport(srv.URL)
	ctx := context.Background()

	xhtml := []byte("<p>Hello Confluence</p>")
	props := ConfluenceProperties{"sourcebridge_page_id": "my-page"}
	if err := client.UpsertPage(ctx, "my-page", xhtml, props); err != nil {
		t.Fatalf("UpsertPage create: %v", err)
	}

	// Verify it was stored.
	id, ok := cts.pages["my-page"]
	if !ok {
		t.Fatal("page not stored in mock")
	}
	if cts.bodies[id] != "<p>Hello Confluence</p>" {
		t.Errorf("stored body = %q, want %q", cts.bodies[id], "<p>Hello Confluence</p>")
	}
}

// ─── TestHTTPConfluenceClient_UpsertPage_Update ───────────────────────────────

func TestHTTPConfluenceClient_UpsertPage_Update(t *testing.T) {
	cts, srv := newConfluenceTestServer(t)
	defer srv.Close()

	// Pre-seed a page.
	cts.pages["update-page"] = "pid-update-page"
	cts.bodies["pid-update-page"] = "<p>Old content</p>"
	cts.versions["pid-update-page"] = 1

	client := newConfluenceClientWithTransport(srv.URL)
	ctx := context.Background()

	newXHTML := []byte("<p>New content</p>")
	props := ConfluenceProperties{"sourcebridge_page_id": "update-page"}
	if err := client.UpsertPage(ctx, "update-page", newXHTML, props); err != nil {
		t.Fatalf("UpsertPage update: %v", err)
	}

	if cts.bodies["pid-update-page"] != "<p>New content</p>" {
		t.Errorf("body after update = %q, want %q", cts.bodies["pid-update-page"], "<p>New content</p>")
	}
}

// ─── TestHTTPConfluenceClient_GetBlockByExternalID ────────────────────────────

func TestHTTPConfluenceClient_GetBlockByExternalID(t *testing.T) {
	cts, srv := newConfluenceTestServer(t)
	defer srv.Close()

	// Pre-seed a page with a sourcebridge-block macro.
	blockXHTML := `<ac:structured-macro ac:name="sourcebridge-block">
<ac:parameter ac:name="id">block-001</ac:parameter>
<ac:parameter ac:name="kind">paragraph</ac:parameter>
<ac:parameter ac:name="owner">generated</ac:parameter>
<ac:rich-text-body>
<p>Hello world</p>
</ac:rich-text-body>
</ac:structured-macro>`
	cts.pages["my-page"] = "pid-my-page"
	cts.bodies["pid-my-page"] = blockXHTML
	cts.versions["pid-my-page"] = 1

	client := newConfluenceClientWithTransport(srv.URL)
	ctx := context.Background()

	blockBody, ok, err := client.GetBlockByExternalID(ctx, "my-page", ast.BlockID("block-001"))
	if err != nil {
		t.Fatalf("GetBlockByExternalID: %v", err)
	}
	if !ok {
		t.Fatal("expected block to be found")
	}
	if !strings.Contains(string(blockBody), "Hello world") {
		t.Errorf("block body = %q, want to contain 'Hello world'", blockBody)
	}
}

func TestHTTPConfluenceClient_GetBlockByExternalID_Missing(t *testing.T) {
	cts, srv := newConfluenceTestServer(t)
	defer srv.Close()

	cts.pages["my-page"] = "pid-my-page"
	cts.bodies["pid-my-page"] = `<p>No blocks here</p>`
	cts.versions["pid-my-page"] = 1

	client := newConfluenceClientWithTransport(srv.URL)
	ctx := context.Background()

	_, ok, err := client.GetBlockByExternalID(ctx, "my-page", ast.BlockID("nonexistent"))
	if err != nil {
		t.Fatalf("GetBlockByExternalID: %v", err)
	}
	if ok {
		t.Error("expected block not found, got ok=true")
	}
}

// ─── TestHTTPConfluenceClient_429_Retry ───────────────────────────────────────

func TestHTTPConfluenceClient_429_Retry(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.Header().Set("Retry-After", "0")
			writeConfluenceJSON(w, http.StatusTooManyRequests, map[string]string{"message": "rate limit"})
			return
		}
		writeConfluenceJSON(w, http.StatusOK, map[string]interface{}{"results": []interface{}{}})
	}))
	defer srv.Close()

	client := newConfluenceClientWithTransport(srv.URL)
	_, _, err := client.GetPage(context.Background(), "any-page")
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if attempts < 2 {
		t.Errorf("attempts = %d, want >= 2", attempts)
	}
}

// ─── TestHTTPConfluenceClient_500_Retry ───────────────────────────────────────

func TestHTTPConfluenceClient_500_Retry(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			writeConfluenceJSON(w, http.StatusInternalServerError, map[string]string{"message": "server error"})
			return
		}
		writeConfluenceJSON(w, http.StatusOK, map[string]interface{}{"results": []interface{}{}})
	}))
	defer srv.Close()

	client := newConfluenceClientWithTransport(srv.URL)
	_, _, err := client.GetPage(context.Background(), "any-page")
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
}

// ─── TestHTTPConfluenceClient_401 ─────────────────────────────────────────────

func TestHTTPConfluenceClient_401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeConfluenceJSON(w, http.StatusUnauthorized, map[string]string{"message": "Unauthorized"})
	}))
	defer srv.Close()

	client := newConfluenceClientWithTransport(srv.URL)
	_, _, err := client.GetPage(context.Background(), "any-page")
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
	var ce *ConfluenceAPIError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ConfluenceAPIError in chain, got %T: %v", err, err)
	}
	if ce.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want 401", ce.StatusCode)
	}
}

// ─── TestHTTPConfluenceClient_MalformedJSON ───────────────────────────────────

func TestHTTPConfluenceClient_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json {{{"))
	}))
	defer srv.Close()

	client := newConfluenceClientWithTransport(srv.URL)
	_, _, err := client.GetPage(context.Background(), "any-page")
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}
