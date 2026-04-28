// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// confluence_http.go implements the [ConfluenceClient] port against the
// Confluence Cloud REST API v2 (no SDK; plain net/http).
//
// # Authentication
//
// Confluence Cloud uses HTTP Basic authentication with the format
// "email:api_token". The client encodes this pair as a Base64 Basic header on
// every request.
//
// # External ID mapping
//
// Confluence has no native "external_id" concept for pages. SourceBridge stores
// its page ID as a Confluence page property keyed "sourcebridge_page_id" via the
// Content Properties API:
//
//	GET /wiki/api/v2/pages/{id}/properties/sourcebridge_page_id
//	PUT /wiki/api/v2/pages/{id}/properties/sourcebridge_page_id
//
// To look up a page by external ID the client searches by title (GET /wiki/api/v2/pages?title=…),
// then reads the property to verify the match. The property is always written on
// upsert so subsequent look-ups are fast (title → ID → verify).
//
// # GetBlockByExternalID
//
// Confluence does not index pages by macro parameter. This implementation
// fetches the full page XHTML and scans for the sourcebridge-block macro with
// the matching id parameter. This is an O(page-size) linear scan; acceptable
// because it is called only during reconciliation and the page is already in
// memory from the GetPage call. A comment documents the trade-off.
//
// # Rate limiting and retry
//
// 429 and 5xx responses are retried up to three times with exponential back-off
// (1 s, 2 s, 4 s). 4xx errors (excluding 429) are returned immediately as a
// typed [ConfluenceAPIError].
package markdown

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/credentials"
)

const (
	maxConfluenceRetries = 3

	// confluencePropertyKey is the page property key used to store the
	// SourceBridge page ID on each Confluence page.
	confluencePropertyKey = "sourcebridge_page_id"
)

// ConfluenceAPIError is the typed error returned by [HTTPConfluenceClient] on
// non-2xx responses.
type ConfluenceAPIError struct {
	StatusCode int
	Message    string
}

func (e *ConfluenceAPIError) Error() string {
	return fmt.Sprintf("confluence API error %d: %s", e.StatusCode, e.Message)
}

// IsConfluenceNotFound reports whether err is a Confluence 404.
// Unwraps the error chain to find a [ConfluenceAPIError].
func IsConfluenceNotFound(err error) bool {
	var ce *ConfluenceAPIError
	return err != nil && errors.As(err, &ce) && ce.StatusCode == http.StatusNotFound
}

// IsConfluenceRateLimited reports whether err is a Confluence 429.
// Unwraps the error chain to find a [ConfluenceAPIError].
func IsConfluenceRateLimited(err error) bool {
	var ce *ConfluenceAPIError
	return err != nil && errors.As(err, &ce) && ce.StatusCode == http.StatusTooManyRequests
}

// ConfluenceHTTPConfig holds construction parameters for [HTTPConfluenceClient].
// Credential fields (email, API token) are intentionally absent: they are
// injected per-call via a [credentials.Snapshot] so that credential rotation
// propagates to the next orchestrator job without a process restart.
type ConfluenceHTTPConfig struct {
	// Site is the Atlassian Cloud site name (e.g. "mycompany" → mycompany.atlassian.net).
	Site string
	// SpaceKey is the Confluence space key (e.g. "ENG").
	// Used when creating new pages.
	SpaceKey string
	// ParentPageID is the Confluence page ID under which new pages are created.
	// Optional; if empty pages are created at the space root.
	ParentPageID string
	// HTTPTimeout is the per-request timeout (defaults to 30 s).
	HTTPTimeout time.Duration
}

func (c ConfluenceHTTPConfig) baseURL() string {
	return fmt.Sprintf("https://%s.atlassian.net/wiki/api/v2", c.Site)
}

func basicAuthHeader(email, token string) string {
	creds := email + ":" + token
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(creds))
}

func (c ConfluenceHTTPConfig) httpTimeout() time.Duration {
	if c.HTTPTimeout > 0 {
		return c.HTTPTimeout
	}
	return 30 * time.Second
}

// HTTPConfluenceClient makes authenticated calls to the Confluence Cloud REST
// API v2 using credentials supplied per-call via a [credentials.Snapshot].
// Construct via [NewHTTPConfluenceClient].
//
// The client is stateless with respect to credentials: each public method
// receives a Snapshot, so mid-job credential rotation does not affect an
// in-flight job (the at-most-one-rotation-per-job invariant).
type HTTPConfluenceClient struct {
	cfg            ConfluenceHTTPConfig
	http           *http.Client
	retryBaseDelay time.Duration // base delay for back-off; 0 means 1s

	// spaceID caches the resolved numeric space ID for cfg.SpaceKey.
	// Confluence v2 endpoints require spaceId (numeric) rather than the
	// human-readable space key, so the first call resolves it via
	// GET /spaces?keys=<key> and subsequent calls reuse the value.
	spaceIDMu sync.Mutex
	spaceID   string
}

func (c *HTTPConfluenceClient) retryDelay(attempt int) time.Duration {
	base := c.retryBaseDelay
	if base <= 0 {
		base = time.Second
	}
	return time.Duration(math.Pow(2, float64(attempt-1))) * base
}

// NewHTTPConfluenceClient constructs an [HTTPConfluenceClient].
// No credentials are accepted here; pass a [credentials.Snapshot] to each
// method call so that token rotation takes effect on the next job.
func NewHTTPConfluenceClient(cfg ConfluenceHTTPConfig) *HTTPConfluenceClient {
	return &HTTPConfluenceClient{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.httpTimeout()},
	}
}

// GetPage fetches the page identified by externalID using credentials from snap.
// Returns (nil, nil, nil) when no page with that external ID exists yet.
func (c *HTTPConfluenceClient) GetPage(ctx context.Context, snap credentials.Snapshot, externalID string) ([]byte, ConfluenceProperties, error) {
	auth := basicAuthHeader(snap.ConfluenceEmail, snap.ConfluenceToken)
	pageID, err := c.findPageIDByExternalID(ctx, auth, externalID)
	if err != nil {
		return nil, nil, err
	}
	if pageID == "" {
		return nil, nil, nil
	}

	// Fetch the page body (storage representation).
	path := fmt.Sprintf("/pages/%s?body-format=storage", url.PathEscape(pageID))
	var resp struct {
		Body struct {
			Storage struct {
				Value string `json:"value"`
			} `json:"storage"`
		} `json:"body"`
		Version struct {
			Number int `json:"number"`
		} `json:"version"`
		Title string `json:"title"`
	}
	if err := c.do(ctx, auth, http.MethodGet, path, nil, &resp); err != nil {
		return nil, nil, fmt.Errorf("confluence_http: GetPage body: %w", err)
	}

	// Fetch stored properties.
	props, err := c.getPageProperties(ctx, auth, pageID)
	if err != nil {
		// Non-fatal — return empty properties.
		props = ConfluenceProperties{}
	}

	return []byte(resp.Body.Storage.Value), props, nil
}

// UpsertPage creates or updates the page identified by externalID.
func (c *HTTPConfluenceClient) UpsertPage(ctx context.Context, snap credentials.Snapshot, externalID string, xhtml []byte, metadata ConfluenceProperties) error {
	auth := basicAuthHeader(snap.ConfluenceEmail, snap.ConfluenceToken)
	pageID, err := c.findPageIDByExternalID(ctx, auth, externalID)
	if err != nil {
		return err
	}

	if pageID == "" {
		return c.createPage(ctx, auth, externalID, xhtml, metadata)
	}
	return c.updatePage(ctx, auth, pageID, externalID, xhtml, metadata)
}

// GetBlockByExternalID fetches the XHTML of a specific block by scanning the
// page's stored XHTML for the sourcebridge-block macro with the matching ID.
//
// Performance note: this performs a full page fetch and O(n) scan of the XHTML.
// For typical wiki pages (tens of blocks) this is negligible. If pages ever
// grow to thousands of blocks, a cached parse could be added.
func (c *HTTPConfluenceClient) GetBlockByExternalID(ctx context.Context, snap credentials.Snapshot, pageExternalID string, blockExternalID ast.BlockID) ([]byte, bool, error) {
	xhtml, _, err := c.GetPage(ctx, snap, pageExternalID)
	if err != nil {
		return nil, false, err
	}
	if xhtml == nil {
		return nil, false, nil
	}

	blocks := parseConfluenceBlocks(xhtml)
	for _, b := range blocks {
		if b.id == blockExternalID {
			return []byte(b.rawXHTML), true, nil
		}
	}
	return nil, false, nil
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

// resolveSpaceID looks up the numeric Confluence space ID for cfg.SpaceKey.
// The result is cached on the client; subsequent calls within the same client
// lifetime are cost-free. Confluence v2 endpoints require spaceId (numeric);
// only the v1 endpoints accepted space-key.
func (c *HTTPConfluenceClient) resolveSpaceID(ctx context.Context, auth string) (string, error) {
	c.spaceIDMu.Lock()
	defer c.spaceIDMu.Unlock()
	if c.spaceID != "" {
		return c.spaceID, nil
	}
	if c.cfg.SpaceKey == "" {
		return "", fmt.Errorf("confluence_http: SpaceKey is empty; cannot resolve space ID")
	}
	params := url.Values{}
	params.Set("keys", c.cfg.SpaceKey)
	path := "/spaces?" + params.Encode()
	var resp struct {
		Results []struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"results"`
	}
	if err := c.do(ctx, auth, http.MethodGet, path, nil, &resp); err != nil {
		return "", fmt.Errorf("confluence_http: resolve space ID for key %q: %w", c.cfg.SpaceKey, err)
	}
	for _, r := range resp.Results {
		if r.Key == c.cfg.SpaceKey && r.ID != "" {
			c.spaceID = r.ID
			return c.spaceID, nil
		}
	}
	return "", fmt.Errorf("confluence_http: no Confluence space found with key %q (verify the key exists and the API token has access)", c.cfg.SpaceKey)
}

// findPageIDByExternalID searches for a Confluence page whose
// sourcebridge_page_id property matches externalID. Returns "" when not found.
func (c *HTTPConfluenceClient) findPageIDByExternalID(ctx context.Context, auth, externalID string) (string, error) {
	// Search by title. Pages are created with the deterministic humanized
	// title from HumanizePageID(externalID); using the same derivation here
	// guarantees create / find round-trip without a separate index. Property
	// verification narrows down any title collisions.
	spaceID, err := c.resolveSpaceID(ctx, auth)
	if err != nil {
		return "", err
	}
	params := url.Values{}
	params.Set("title", HumanizePageID(externalID))
	params.Set("space-id", spaceID)

	path := "/pages?" + params.Encode()
	var searchResp struct {
		Results []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"results"`
	}
	if err := c.do(ctx, auth, http.MethodGet, path, nil, &searchResp); err != nil {
		return "", fmt.Errorf("confluence_http: search pages: %w", err)
	}

	for _, result := range searchResp.Results {
		// Verify the property to avoid title collisions.
		propVal, propErr := c.getPageProperty(ctx, auth, result.ID, confluencePropertyKey)
		if propErr != nil {
			continue
		}
		if propVal == externalID {
			return result.ID, nil
		}
	}
	return "", nil
}

// createPage creates a new Confluence page with the given XHTML body and
// writes the sourcebridge_page_id property. Uses the v2 schema:
// `spaceId` (not `space.key`), `status: "current"`, flat body with
// `representation`+`value`.
func (c *HTTPConfluenceClient) createPage(ctx context.Context, auth, externalID string, xhtml []byte, metadata ConfluenceProperties) error {
	spaceID, err := c.resolveSpaceID(ctx, auth)
	if err != nil {
		return err
	}

	type bodyValue struct {
		Representation string `json:"representation"`
		Value          string `json:"value"`
	}
	type createPayload struct {
		SpaceID  string    `json:"spaceId"`
		Status   string    `json:"status"`
		Title    string    `json:"title"`
		ParentID string    `json:"parentId,omitempty"`
		Body     bodyValue `json:"body"`
	}

	// Human-readable title overrides the externalID fallback when the writer
	// has supplied one via metadata. Search uses the same humanized form, so
	// findPageIDByExternalID will still resolve the page on the next run.
	title := metadata[propConfluenceTitle]
	if title == "" {
		title = HumanizePageID(externalID)
	}

	payload := createPayload{
		SpaceID:  spaceID,
		Status:   "current",
		Title:    title,
		ParentID: c.cfg.ParentPageID,
		Body: bodyValue{
			Representation: "storage",
			Value:          string(xhtml),
		},
	}

	var resp struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, auth, http.MethodPost, "/pages", payload, &resp); err != nil {
		return fmt.Errorf("confluence_http: create page: %w", err)
	}

	// Write the sourcebridge_page_id property.
	if err := c.setPageProperty(ctx, auth, resp.ID, confluencePropertyKey, externalID); err != nil {
		return fmt.Errorf("confluence_http: set page property: %w", err)
	}

	// Write any additional metadata properties. The pseudo-property
	// `propConfluenceTitle` is consumed for the create payload's title and
	// must not be persisted as a page content-property.
	for k, v := range metadata {
		if k == confluencePropertyKey || k == propConfluenceTitle {
			continue
		}
		_ = c.setPageProperty(ctx, auth, resp.ID, k, v) // best-effort
	}
	return nil
}

// updatePage replaces the page body and bumps the version. Uses the v2 schema:
// `id`, `status: "current"`, flat body with `representation`+`value`, and a
// version object.
func (c *HTTPConfluenceClient) updatePage(ctx context.Context, auth, pageID, externalID string, xhtml []byte, metadata ConfluenceProperties) error {
	// Fetch current version number.
	path := fmt.Sprintf("/pages/%s", url.PathEscape(pageID))
	var current struct {
		Version struct {
			Number int `json:"number"`
		} `json:"version"`
	}
	if err := c.do(ctx, auth, http.MethodGet, path, nil, &current); err != nil {
		return fmt.Errorf("confluence_http: get page version: %w", err)
	}

	type versionInfo struct {
		Number int `json:"number"`
	}
	type bodyValue struct {
		Representation string `json:"representation"`
		Value          string `json:"value"`
	}
	type updatePayload struct {
		ID      string      `json:"id"`
		Status  string      `json:"status"`
		Title   string      `json:"title"`
		Body    bodyValue   `json:"body"`
		Version versionInfo `json:"version"`
	}

	title := metadata[propConfluenceTitle]
	if title == "" {
		title = HumanizePageID(externalID)
	}

	payload := updatePayload{
		ID:     pageID,
		Status: "current",
		Title:  title,
		Body: bodyValue{
			Representation: "storage",
			Value:          string(xhtml),
		},
		Version: versionInfo{Number: current.Version.Number + 1},
	}

	if err := c.do(ctx, auth, http.MethodPut, path, payload, nil); err != nil {
		return fmt.Errorf("confluence_http: update page: %w", err)
	}

	// Update metadata properties (best-effort). The pseudo-property
	// `propConfluenceTitle` is consumed for the update payload's title and
	// must not be persisted as a page content-property.
	for k, v := range metadata {
		if k == propConfluenceTitle {
			continue
		}
		_ = c.setPageProperty(ctx, auth, pageID, k, v)
	}
	return nil
}

// pagePropertyRecord is the v2 representation of a content property: a numeric
// id, the human-readable key, the string value, and a version (required when
// updating).
type pagePropertyRecord struct {
	ID      string `json:"id"`
	Key     string `json:"key"`
	Value   string `json:"value"`
	Version struct {
		Number int `json:"number"`
	} `json:"version"`
}

// listPageProperties fetches every content property attached to pageID. v2
// content-property endpoints are keyed by an opaque property id (not the
// human-readable key), so all set/get-by-key flows route through this list.
func (c *HTTPConfluenceClient) listPageProperties(ctx context.Context, auth, pageID string) ([]pagePropertyRecord, error) {
	path := fmt.Sprintf("/pages/%s/properties", url.PathEscape(pageID))
	var resp struct {
		Results []pagePropertyRecord `json:"results"`
	}
	if err := c.do(ctx, auth, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// getPageProperties reads all known SourceBridge properties from a page.
func (c *HTTPConfluenceClient) getPageProperties(ctx context.Context, auth, pageID string) (ConfluenceProperties, error) {
	records, err := c.listPageProperties(ctx, auth, pageID)
	if err != nil {
		return nil, err
	}
	props := make(ConfluenceProperties, len(records))
	for _, r := range records {
		props[r.Key] = r.Value
	}
	return props, nil
}

// getPageProperty reads one property from a page. Returns "" and no error when
// the property does not exist.
func (c *HTTPConfluenceClient) getPageProperty(ctx context.Context, auth, pageID, key string) (string, error) {
	records, err := c.listPageProperties(ctx, auth, pageID)
	if err != nil {
		if IsConfluenceNotFound(err) {
			return "", nil
		}
		return "", err
	}
	for _, r := range records {
		if r.Key == key {
			return r.Value, nil
		}
	}
	return "", nil
}

// setPageProperty writes a single string property on a Confluence page. v2
// requires creating new properties via POST and updating existing properties
// via PUT against the property's numeric id (not its key), so the call first
// lists the page's properties to decide which path to take.
func (c *HTTPConfluenceClient) setPageProperty(ctx context.Context, auth, pageID, key, value string) error {
	records, err := c.listPageProperties(ctx, auth, pageID)
	if err != nil {
		return fmt.Errorf("confluence_http: list properties for %s: %w", pageID, err)
	}
	for _, r := range records {
		if r.Key != key {
			continue
		}
		path := fmt.Sprintf("/pages/%s/properties/%s", url.PathEscape(pageID), url.PathEscape(r.ID))
		payload := map[string]interface{}{
			"key":     key,
			"value":   value,
			"version": map[string]int{"number": r.Version.Number + 1},
		}
		if err := c.do(ctx, auth, http.MethodPut, path, payload, nil); err != nil {
			return fmt.Errorf("confluence_http: update property %q on %s: %w", key, pageID, err)
		}
		return nil
	}

	// Property does not exist yet — create it.
	path := fmt.Sprintf("/pages/%s/properties", url.PathEscape(pageID))
	payload := map[string]interface{}{
		"key":   key,
		"value": value,
	}
	if err := c.do(ctx, auth, http.MethodPost, path, payload, nil); err != nil {
		return fmt.Errorf("confluence_http: create property %q on %s: %w", key, pageID, err)
	}
	return nil
}

// ─── HTTP core ────────────────────────────────────────────────────────────────

func (c *HTTPConfluenceClient) do(ctx context.Context, auth, method, path string, reqBody, respBody interface{}) error {
	for attempt := 0; attempt <= maxConfluenceRetries; attempt++ {
		if attempt > 0 {
			sleep := c.retryDelay(attempt)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(sleep):
			}
		}

		err := c.doOnce(ctx, auth, method, path, reqBody, respBody)
		if err == nil {
			return nil
		}

		var ce *ConfluenceAPIError
		if errors.As(err, &ce) {
			if ce.StatusCode == http.StatusTooManyRequests || ce.StatusCode >= 500 {
				if attempt < maxConfluenceRetries {
					continue
				}
			}
		}
		return err
	}
	return fmt.Errorf("confluence_http: %s %s: exceeded retry limit", method, path)
}

func (c *HTTPConfluenceClient) doOnce(ctx context.Context, auth, method, path string, reqBody, respBody interface{}) error {
	var bodyReader io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	fullURL := c.cfg.baseURL() + path
	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	// Rate-limit sleep if Retry-After is present.
	if resp.StatusCode == http.StatusTooManyRequests {
		if after := resp.Header.Get("Retry-After"); after != "" {
			secs, parseErr := strconv.Atoi(strings.TrimSpace(after))
			if parseErr == nil && secs > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(secs) * time.Second):
				}
			}
		}
	}

	rawBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return fmt.Errorf("read response: %w", readErr)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody struct {
			Message string `json:"message"`
			Status  string `json:"status"`
			Title   string `json:"title"`
		}
		_ = json.Unmarshal(rawBody, &errBody)
		msg := errBody.Message
		if msg == "" {
			msg = errBody.Title
		}
		if msg == "" {
			msg = string(rawBody)
		}
		return &ConfluenceAPIError{StatusCode: resp.StatusCode, Message: msg}
	}

	if respBody != nil && len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, respBody); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
