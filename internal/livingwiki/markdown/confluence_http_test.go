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
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/credentials"
)

// testSnap is a credentials.Snapshot pre-populated with test values.
var testSnap = credentials.Snapshot{
	ConfluenceEmail: "test@example.com",
	ConfluenceToken: "test-token",
}

// confluenceTestServer builds a minimal Confluence Cloud mock that understands:
//   - GET /wiki/api/v2/spaces?keys=… → space-key → space-id resolution
//   - GET /wiki/api/v2/pages?title=… → search results
//   - GET /wiki/api/v2/pages/{id}/properties/{key} → property read
//   - PUT /wiki/api/v2/pages/{id}/properties/{key} → property write
//   - GET /wiki/api/v2/pages/{id}?body-format=storage → page body
//   - POST /wiki/api/v2/pages → create (v2 schema: spaceId + status + flat body)
//   - PUT /wiki/api/v2/pages/{id} → update (v2 schema: id + status + flat body)
//
// It stores pages in a simple map keyed by external ID. The handlers reject
// requests that omit the v2-required spaceId / status fields so a regression to
// the v1 schema fails the test loudly.
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
	// Resolve space key → space id.
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/spaces"):
		key := r.URL.Query().Get("keys")
		type result struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		}
		results := []result{}
		if key != "" {
			results = append(results, result{ID: "space-id-" + key, Key: key})
		}
		writeConfluenceJSON(w, http.StatusOK, map[string]interface{}{"results": results})

	// List all pages in space (full-scan fallback — no title filter).
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/pages") && r.URL.Query().Get("title") == "":
		type result struct {
			ID string `json:"id"`
		}
		var results []result
		// Return all known page IDs so findPageByPropertyScan can probe properties.
		seen := make(map[string]bool)
		for _, id := range cts.pages {
			if !seen[id] {
				seen[id] = true
				results = append(results, result{ID: id})
			}
		}
		writeConfluenceJSON(w, http.StatusOK, map[string]interface{}{"results": results, "_links": map[string]string{}})

	// Search pages by title.
	case r.Method == http.MethodGet && strings.Contains(path, "/pages") && r.URL.Query().Get("title") != "":
		title := r.URL.Query().Get("title")
		// v2 search must include space-id, not space-key. Fail loudly on
		// regressions so the schema mismatch is caught at test time.
		if r.URL.Query().Get("space-id") == "" {
			cts.t.Errorf("search pages: missing space-id query param (got %s)", r.URL.RawQuery)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		type result struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}
		var results []result
		if id, ok := cts.pages[title]; ok {
			results = append(results, result{ID: id, Title: title})
		}
		writeConfluenceJSON(w, http.StatusOK, map[string]interface{}{"results": results})

	// Get page properties list (v2: returns array with id+key+value+version).
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/properties"):
		pageID := extractConfluencePageID(strings.TrimSuffix(path, "/properties"))
		var props []map[string]interface{}
		for eid, id := range cts.pages {
			if id == pageID {
				props = append(props, map[string]interface{}{
					"id":      "prop-" + confluencePropertyKey,
					"key":     confluencePropertyKey,
					"value":   eid,
					"version": map[string]int{"number": 1},
				})
				break
			}
		}
		writeConfluenceJSON(w, http.StatusOK, map[string]interface{}{"results": props})

	// Update page property by property-id (v2 PUT).
	case r.Method == http.MethodPut && strings.Contains(path, "/properties/"):
		w.WriteHeader(http.StatusOK)

	// Create page property (v2 POST).
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/properties"):
		w.WriteHeader(http.StatusCreated)

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
		// v2 schema: require spaceId + status to catch v1-schema regressions.
		if s, _ := payload["spaceId"].(string); s == "" {
			cts.t.Errorf("createPage: missing spaceId in payload: %#v", payload)
			writeConfluenceJSON(w, http.StatusBadRequest, map[string]interface{}{
				"errors": []map[string]string{{"title": "spaceId: must not be null"}},
			})
			return
		}
		if s, _ := payload["status"].(string); s != "current" {
			cts.t.Errorf("createPage: status should be \"current\", got %v", payload["status"])
		}
		title := payload["title"].(string)
		pageID := "pid-" + title
		body := ""
		if b, ok := payload["body"].(map[string]interface{}); ok {
			body, _ = b["value"].(string)
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
		// v2 update schema: require id + status (catches v1 regressions).
		if id, _ := payload["id"].(string); id == "" {
			cts.t.Errorf("updatePage: missing id in payload: %#v", payload)
		}
		if s, _ := payload["status"].(string); s != "current" {
			cts.t.Errorf("updatePage: status should be \"current\", got %v", payload["status"])
		}
		if b, ok := payload["body"].(map[string]interface{}); ok {
			cts.bodies[pageID], _ = b["value"].(string)
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

// newConfluenceHTTPClient is a convenience wrapper around newConfluenceClientWithTransport.
func newConfluenceHTTPClient(srv *httptest.Server) *HTTPConfluenceClient {
	return newConfluenceClientWithTransport(srv.URL)
}

// confluencePageKeys returns the keys of a pages map for diagnostic output.
func confluencePageKeys(pages map[string]string) []string {
	out := make([]string, 0, len(pages))
	for k := range pages {
		out = append(out, k)
	}
	return out
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
	xhtml, props, err := client.GetPage(context.Background(), testSnap, "nonexistent")
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
	if err := client.UpsertPage(ctx, testSnap, "my-page", xhtml, props); err != nil {
		t.Fatalf("UpsertPage create: %v", err)
	}

	// Verify it was stored under the humanized title (HumanizePageID("my-page") = "My page").
	// createPage uses HumanizePageID(externalID) as the Confluence page title when no
	// propConfluenceTitle is supplied in metadata, so the mock keys on "My page" not "my-page".
	humanTitle := HumanizePageID("my-page")
	id, ok := cts.pages[humanTitle]
	if !ok {
		t.Fatalf("page not stored in mock under title %q (have keys: %v)", humanTitle, confluencePageKeys(cts.pages))
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
	if err := client.UpsertPage(ctx, testSnap, "update-page", newXHTML, props); err != nil {
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

	// Pre-seed a page with sb-block HTML-comment markers (the renderer's
	// current format — matches what real Confluence pages will contain).
	blockXHTML := `<!-- sb:block id="block-001" kind="paragraph" owner="generated" -->
<p>Hello world</p>
<!-- /sb:block -->`
	cts.pages["my-page"] = "pid-my-page"
	cts.bodies["pid-my-page"] = blockXHTML
	cts.versions["pid-my-page"] = 1

	client := newConfluenceClientWithTransport(srv.URL)
	ctx := context.Background()

	blockBody, ok, err := client.GetBlockByExternalID(ctx, testSnap, "my-page", ast.BlockID("block-001"))
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

	_, ok, err := client.GetBlockByExternalID(ctx, testSnap, "my-page", ast.BlockID("nonexistent"))
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
	client.spaceID = "preset" // skip space-id resolution; test focuses on retry behaviour
	_, _, err := client.GetPage(context.Background(), testSnap, "any-page")
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
	client.spaceID = "preset" // skip space-id resolution; test focuses on retry behaviour
	_, _, err := client.GetPage(context.Background(), testSnap, "any-page")
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
	_, _, err := client.GetPage(context.Background(), testSnap, "any-page")
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
	_, _, err := client.GetPage(context.Background(), testSnap, "any-page")
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}
