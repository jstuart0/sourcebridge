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
)

// notionRedirectTransport redirects all requests to the test server.
type notionRedirectTransport struct {
	base string
}

func (t *notionRedirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
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

func newTestNotionClient(srv *httptest.Server, dbID string) *HTTPNotionClient {
	return &HTTPNotionClient{
		cfg: NotionHTTPConfig{
			Token:       "test-token",
			DatabaseID:  dbID,
			HTTPTimeout: 5 * time.Second,
		},
		http: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &notionRedirectTransport{base: srv.URL},
		},
		retryBaseDelay: time.Millisecond,
	}
}

// notionMockServer is a minimal Notion API mock.
type notionMockServer struct {
	t      *testing.T
	pages  map[string]string          // externalID → notion page ID
	blocks map[string][]NotionBlock   // notion page ID → blocks
	titles map[string]string          // notion page ID → title
}

func newNotionMockServer(t *testing.T) (*notionMockServer, *httptest.Server) {
	t.Helper()
	ns := &notionMockServer{
		t:      t,
		pages:  make(map[string]string),
		blocks: make(map[string][]NotionBlock),
		titles: make(map[string]string),
	}
	srv := httptest.NewServer(http.HandlerFunc(ns.handle))
	return ns, srv
}

func writeNotionJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (ns *notionMockServer) handle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	switch {
	// Database query.
	case r.Method == http.MethodPost && strings.Contains(path, "/databases/") && strings.HasSuffix(path, "/query"):
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)

		// Extract the filter value (externalID).
		externalID := ""
		if f, ok := payload["filter"].(map[string]interface{}); ok {
			if rt, ok := f["rich_text"].(map[string]interface{}); ok {
				externalID, _ = rt["equals"].(string)
			}
		}

		var results []map[string]interface{}
		if pageID, ok := ns.pages[externalID]; ok {
			results = append(results, map[string]interface{}{
				"id": pageID,
				"properties": map[string]interface{}{
					"sourcebridge_page_id": externalID,
				},
			})
		}
		writeNotionJSON(w, http.StatusOK, map[string]interface{}{
			"results":  results,
			"has_more": false,
		})

	// Search (title fallback).
	case r.Method == http.MethodPost && path == "/v1/search":
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		query, _ := payload["query"].(string)

		var results []map[string]interface{}
		for eid, pid := range ns.pages {
			if eid == query || ns.titles[pid] == query {
				results = append(results, map[string]interface{}{
					"id": pid,
					"properties": map[string]interface{}{
						"title": map[string]interface{}{
							"title": []map[string]string{{"plain_text": eid}},
						},
					},
				})
			}
		}
		writeNotionJSON(w, http.StatusOK, map[string]interface{}{"results": results, "has_more": false})

	// Fetch block children.
	case r.Method == http.MethodGet && strings.Contains(path, "/blocks/") && strings.HasSuffix(path, "/children"):
		pageID := extractNotionID(path, "/blocks/", "/children")
		blocks, ok := ns.blocks[pageID]
		if !ok {
			blocks = []NotionBlock{}
		}
		writeNotionJSON(w, http.StatusOK, map[string]interface{}{
			"results":  blocks,
			"has_more": false,
		})

	// Append blocks to page.
	case r.Method == http.MethodPatch && strings.Contains(path, "/blocks/") && strings.HasSuffix(path, "/children"):
		pageID := extractNotionID(path, "/blocks/", "/children")
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if children, ok := payload["children"]; ok {
			data, _ := json.Marshal(children)
			var blocks []NotionBlock
			_ = json.Unmarshal(data, &blocks)
			ns.blocks[pageID] = append(ns.blocks[pageID], blocks...)
		}
		writeNotionJSON(w, http.StatusOK, map[string]interface{}{})

	// Create page.
	case r.Method == http.MethodPost && path == "/v1/pages":
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)

		// Extract external ID from properties.
		var externalID string
		if props, ok := payload["properties"].(map[string]interface{}); ok {
			if rt, ok := props["sourcebridge_page_id"].(map[string]interface{}); ok {
				if items, ok := rt["rich_text"].([]interface{}); ok && len(items) > 0 {
					if item, ok := items[0].(map[string]interface{}); ok {
						if text, ok := item["text"].(map[string]interface{}); ok {
							externalID, _ = text["content"].(string)
						}
					}
				}
			}
			// Fallback: extract from title.
			if externalID == "" {
				if title, ok := props["title"].(map[string]interface{}); ok {
					if items, ok := title["title"].([]interface{}); ok && len(items) > 0 {
						if item, ok := items[0].(map[string]interface{}); ok {
							if text, ok := item["text"].(map[string]interface{}); ok {
								externalID, _ = text["content"].(string)
							}
						}
					}
				}
			}
		}

		pageID := "notion-" + externalID
		ns.pages[externalID] = pageID
		ns.titles[pageID] = externalID

		// Store any children blocks.
		if children, ok := payload["children"]; ok {
			data, _ := json.Marshal(children)
			var blocks []NotionBlock
			_ = json.Unmarshal(data, &blocks)
			ns.blocks[pageID] = blocks
		}

		writeNotionJSON(w, http.StatusOK, map[string]interface{}{"id": pageID})

	// Delete block.
	case r.Method == http.MethodDelete && strings.Contains(path, "/blocks/"):
		// Accept delete without error.
		writeNotionJSON(w, http.StatusOK, map[string]interface{}{})

	// Update block.
	case r.Method == http.MethodPatch && strings.Contains(path, "/blocks/") && !strings.HasSuffix(path, "/children"):
		writeNotionJSON(w, http.StatusOK, map[string]interface{}{})

	// Update page (PATCH /v1/pages/{id}).
	case r.Method == http.MethodPatch && strings.Contains(path, "/pages/"):
		writeNotionJSON(w, http.StatusOK, map[string]interface{}{})

	default:
		ns.t.Logf("notion mock: unexpected %s %s", r.Method, path)
		writeNotionJSON(w, http.StatusNotFound, map[string]interface{}{"code": "not_found", "message": "not found"})
	}
}

// extractNotionID extracts the ID segment between prefix and suffix in a path.
func extractNotionID(path, prefix, suffix string) string {
	s := strings.TrimPrefix(path, "/v1"+prefix)
	if suffix != "" {
		s = strings.TrimSuffix(s, suffix)
	}
	return s
}

// ─── TestHTTPNotionClient_GetPage_NotFound ────────────────────────────────────

func TestHTTPNotionClient_GetPage_NotFound(t *testing.T) {
	_, srv := newNotionMockServer(t)
	defer srv.Close()

	client := newTestNotionClient(srv, "db-123")
	blocks, props, err := client.GetPage(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if blocks != nil {
		t.Errorf("expected nil blocks, got %v", blocks)
	}
	if props != nil {
		t.Errorf("expected nil props, got %v", props)
	}
}

// ─── TestHTTPNotionClient_UpsertPage_Create ───────────────────────────────────

func TestHTTPNotionClient_UpsertPage_Create(t *testing.T) {
	ns, srv := newNotionMockServer(t)
	defer srv.Close()

	client := newTestNotionClient(srv, "db-123")
	ctx := context.Background()

	blocks := []NotionBlock{
		{Object: "block", Type: "paragraph", ExternalID: "b001",
			paragraph: &notionParagraph{RichText: notionRichText("Hello Notion")}},
	}
	props := NotionProperties{"sourcebridge_page_id": "create-page"}
	if err := client.UpsertPage(ctx, "create-page", blocks, props); err != nil {
		t.Fatalf("UpsertPage create: %v", err)
	}

	// Verify the page was created.
	pageID, ok := ns.pages["create-page"]
	if !ok {
		t.Fatal("page not created in mock")
	}
	if len(ns.blocks[pageID]) == 0 {
		t.Error("expected blocks to be stored")
	}
}

// ─── TestHTTPNotionClient_UpsertPage_Update ───────────────────────────────────

func TestHTTPNotionClient_UpsertPage_Update(t *testing.T) {
	ns, srv := newNotionMockServer(t)
	defer srv.Close()

	// Pre-seed an existing page.
	ns.pages["update-page"] = "notion-update-page"
	ns.blocks["notion-update-page"] = []NotionBlock{
		{Object: "block", Type: "paragraph", ExternalID: "old-b1",
			paragraph: &notionParagraph{RichText: notionRichText("Old content")}},
	}

	client := newTestNotionClient(srv, "db-123")
	ctx := context.Background()

	newBlocks := []NotionBlock{
		{Object: "block", Type: "paragraph", ExternalID: "new-b1",
			paragraph: &notionParagraph{RichText: notionRichText("New content")}},
	}
	if err := client.UpsertPage(ctx, "update-page", newBlocks, NotionProperties{}); err != nil {
		t.Fatalf("UpsertPage update: %v", err)
	}
	// After replace, new blocks should be appended.
	if len(ns.blocks["notion-update-page"]) == 0 {
		t.Error("expected blocks after update")
	}
}

// ─── TestHTTPNotionClient_AppendBlocks ────────────────────────────────────────

func TestHTTPNotionClient_AppendBlocks(t *testing.T) {
	ns, srv := newNotionMockServer(t)
	defer srv.Close()

	ns.pages["my-page"] = "notion-my-page"
	ns.blocks["notion-my-page"] = []NotionBlock{}

	client := newTestNotionClient(srv, "db-123")
	ctx := context.Background()

	extra := []NotionBlock{
		{Object: "block", Type: "paragraph", ExternalID: "extra-b1",
			paragraph: &notionParagraph{RichText: notionRichText("Extra")}},
	}
	if err := client.AppendBlocks(ctx, "my-page", extra); err != nil {
		t.Fatalf("AppendBlocks: %v", err)
	}
	if len(ns.blocks["notion-my-page"]) != 1 {
		t.Errorf("block count = %d, want 1", len(ns.blocks["notion-my-page"]))
	}
}

// ─── TestHTTPNotionClient_DeleteBlock ─────────────────────────────────────────

func TestHTTPNotionClient_DeleteBlock(t *testing.T) {
	_, srv := newNotionMockServer(t)
	defer srv.Close()

	client := newTestNotionClient(srv, "db-123")
	if err := client.DeleteBlock(context.Background(), "block-xyz"); err != nil {
		t.Fatalf("DeleteBlock: %v", err)
	}
}

// ─── TestHTTPNotionClient_429_Retry ───────────────────────────────────────────

func TestHTTPNotionClient_429_Retry(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.Header().Set("Retry-After", "0")
			writeNotionJSON(w, http.StatusTooManyRequests, map[string]string{
				"code": "rate_limited", "message": "rate limited",
			})
			return
		}
		writeNotionJSON(w, http.StatusOK, map[string]interface{}{
			"results": []interface{}{}, "has_more": false,
		})
	}))
	defer srv.Close()

	client := newTestNotionClient(srv, "db-123")
	_, _, err := client.GetPage(context.Background(), "any")
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if attempts < 2 {
		t.Errorf("attempts = %d, want >= 2", attempts)
	}
}

// ─── TestHTTPNotionClient_500_Retry ───────────────────────────────────────────

func TestHTTPNotionClient_500_Retry(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			writeNotionJSON(w, http.StatusInternalServerError, map[string]string{"message": "internal"})
			return
		}
		writeNotionJSON(w, http.StatusOK, map[string]interface{}{
			"results": []interface{}{}, "has_more": false,
		})
	}))
	defer srv.Close()

	client := newTestNotionClient(srv, "db-123")
	_, _, err := client.GetPage(context.Background(), "any")
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
}

// ─── TestHTTPNotionClient_401 ─────────────────────────────────────────────────

func TestHTTPNotionClient_401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeNotionJSON(w, http.StatusUnauthorized, map[string]string{
			"code": "unauthorized", "message": "Unauthorized",
		})
	}))
	defer srv.Close()

	client := newTestNotionClient(srv, "db-123")
	_, _, err := client.GetPage(context.Background(), "any")
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
	var ne *NotionAPIError
	if !errors.As(err, &ne) {
		t.Fatalf("expected *NotionAPIError in chain, got %T: %v", err, err)
	}
	if ne.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want 401", ne.StatusCode)
	}
}

// ─── TestHTTPNotionClient_MalformedJSON ───────────────────────────────────────

func TestHTTPNotionClient_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{{{not json"))
	}))
	defer srv.Close()

	client := newTestNotionClient(srv, "db-123")
	_, _, err := client.GetPage(context.Background(), "any")
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

// ─── TestHTTPNotionClient_TitleFallback ───────────────────────────────────────

func TestHTTPNotionClient_TitleFallback(t *testing.T) {
	ns, srv := newNotionMockServer(t)
	defer srv.Close()

	ns.pages["fallback-page"] = "notion-fallback"
	ns.blocks["notion-fallback"] = []NotionBlock{
		{Object: "block", Type: "paragraph", ExternalID: "b1",
			paragraph: &notionParagraph{RichText: notionRichText("Content")}},
	}

	// Client with no DatabaseID → uses search.
	client := newTestNotionClient(srv, "")
	blocks, _, err := client.GetPage(context.Background(), "fallback-page")
	if err != nil {
		t.Fatalf("GetPage (title fallback): %v", err)
	}
	if len(blocks) == 0 {
		t.Error("expected blocks, got none")
	}
}

// ─── TestHTTPNotionClient_Version_Header ──────────────────────────────────────

func TestHTTPNotionClient_Version_Header(t *testing.T) {
	var gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVersion = r.Header.Get("Notion-Version")
		writeNotionJSON(w, http.StatusOK, map[string]interface{}{
			"results": []interface{}{}, "has_more": false,
		})
	}))
	defer srv.Close()

	client := newTestNotionClient(srv, "db-123")
	_, _, _ = client.GetPage(context.Background(), "any")

	if gotVersion != notionVersion {
		t.Errorf("Notion-Version = %q, want %q", gotVersion, notionVersion)
	}
}
