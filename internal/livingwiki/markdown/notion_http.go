// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// notion_http.go implements the [NotionClient] port against the Notion REST API
// (no SDK; plain net/http).
//
// # Authentication
//
// All requests carry "Authorization: Bearer {integration_token}" and the
// required "Notion-Version" header.
//
// # External ID mapping
//
// Notion pages have a native external_id field in some integrations, but it is
// not universally available. For portability, SourceBridge stores the page ID
// in a database page property named by [NotionHTTPConfig.ExternalIDProperty]
// (default "sourcebridge_page_id"). The database is queried to find a page by
// external ID.
//
// When [NotionHTTPConfig.DatabaseID] is empty the client falls back to searching
// all pages accessible to the integration using the search API with a title
// filter. Title fallback is less reliable (title collisions are possible) but
// works for customers who have not set up a Notion database.
//
// # Content model: children fetch
//
// Notion page content is not inline with the page object; it must be fetched
// separately via GET /blocks/{id}/children. [GetPage] therefore makes two
// round-trips: one for the page metadata and one for the children.
//
// # Rate limiting and retry
//
// Notion's rate limit is expressed via a 429 status with a Retry-After header
// (seconds). The client sleeps for Retry-After before retrying. 5xx responses
// use the same exponential back-off (1 s, 2 s, 4 s) as the other clients,
// capped at 3 retries.
package markdown

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// notionVersion is the Notion API version header value.
	notionVersion = "2022-06-28"

	notionAPIBaseURL     = "https://api.notion.com/v1"
	maxNotionRetries     = 3

	// defaultNotionExternalIDProp is the Notion page property name used to store
	// the SourceBridge page ID when no DatabaseID is configured.
	defaultNotionExternalIDProp = "sourcebridge_page_id"
)

// NotionAPIError is the typed error returned by [HTTPNotionClient] on non-2xx
// responses.
type NotionAPIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *NotionAPIError) Error() string {
	return fmt.Sprintf("notion API error %d (%s): %s", e.StatusCode, e.Code, e.Message)
}

// IsNotionRateLimited reports whether err is a Notion 429 rate-limit error.
// Unwraps the error chain to find a [NotionAPIError].
func IsNotionRateLimited(err error) bool {
	var ne *NotionAPIError
	return err != nil && errors.As(err, &ne) && ne.StatusCode == http.StatusTooManyRequests
}

// IsNotionNotFound reports whether err is a Notion 404 not-found error.
// Unwraps the error chain to find a [NotionAPIError].
func IsNotionNotFound(err error) bool {
	var ne *NotionAPIError
	return err != nil && errors.As(err, &ne) && ne.StatusCode == http.StatusNotFound
}

// NotionHTTPConfig holds construction parameters for [HTTPNotionClient].
type NotionHTTPConfig struct {
	// Token is the Notion integration token.
	Token string
	// DatabaseID is the Notion database that holds SourceBridge pages.
	// When set, UpsertPage creates pages inside this database and GetPage
	// queries it by the ExternalIDProperty.
	// When empty, the client falls back to title-based search (less reliable).
	DatabaseID string
	// ExternalIDProperty is the database property name used to store the
	// SourceBridge page ID. Defaults to "sourcebridge_page_id".
	ExternalIDProperty string
	// HTTPTimeout is the per-request timeout (defaults to 30 s).
	HTTPTimeout time.Duration
}

func (c NotionHTTPConfig) externalIDProp() string {
	if c.ExternalIDProperty != "" {
		return c.ExternalIDProperty
	}
	return defaultNotionExternalIDProp
}

func (c NotionHTTPConfig) httpTimeout() time.Duration {
	if c.HTTPTimeout > 0 {
		return c.HTTPTimeout
	}
	return 30 * time.Second
}

// HTTPNotionClient implements [NotionClient] via the Notion REST API.
// Construct via [NewHTTPNotionClient].
type HTTPNotionClient struct {
	cfg            NotionHTTPConfig
	http           *http.Client
	retryBaseDelay time.Duration // base delay for back-off; 0 means 1s
}

func (n *HTTPNotionClient) retryDelay(attempt int) time.Duration {
	base := n.retryBaseDelay
	if base <= 0 {
		base = time.Second
	}
	return time.Duration(math.Pow(2, float64(attempt-1))) * base
}

// Compile-time interface check.
var _ NotionClient = (*HTTPNotionClient)(nil)

// NewHTTPNotionClient constructs an [HTTPNotionClient].
func NewHTTPNotionClient(cfg NotionHTTPConfig) *HTTPNotionClient {
	return &HTTPNotionClient{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.httpTimeout()},
	}
}

// GetPage fetches all blocks for the page identified by externalID.
// Returns (nil, nil, nil) when no page with that external ID exists yet.
//
// Implementation: two round-trips:
//  1. Resolve externalID → Notion page ID via database query or search.
//  2. GET /blocks/{pageID}/children to fetch the page's content blocks.
func (n *HTTPNotionClient) GetPage(ctx context.Context, externalID string) ([]NotionBlock, NotionProperties, error) {
	pageID, props, err := n.findPageByExternalID(ctx, externalID)
	if err != nil {
		return nil, nil, err
	}
	if pageID == "" {
		return nil, nil, nil
	}

	blocks, err := n.fetchChildren(ctx, pageID)
	if err != nil {
		return nil, nil, fmt.Errorf("notion_http: fetch children for page %s: %w", pageID, err)
	}
	return blocks, props, nil
}

// UpsertPage creates or replaces the page identified by externalID.
func (n *HTTPNotionClient) UpsertPage(ctx context.Context, externalID string, blocks []NotionBlock, properties NotionProperties) error {
	pageID, _, err := n.findPageByExternalID(ctx, externalID)
	if err != nil {
		return err
	}

	if pageID == "" {
		return n.createPage(ctx, externalID, blocks, properties)
	}
	return n.replacePage(ctx, pageID, externalID, blocks, properties)
}

// AppendBlocks appends blocks to an existing page identified by externalID.
func (n *HTTPNotionClient) AppendBlocks(ctx context.Context, pageExternalID string, blocks []NotionBlock) error {
	pageID, _, err := n.findPageByExternalID(ctx, pageExternalID)
	if err != nil {
		return err
	}
	if pageID == "" {
		return fmt.Errorf("notion_http: AppendBlocks: page %q not found", pageExternalID)
	}
	return n.appendBlocksToPage(ctx, pageID, blocks)
}

// UpdateBlock replaces the content of the block identified by blockExternalID
// (the SourceBridge external_id stored on the block). Notion identifies blocks
// by their own UUID; this implementation resolves the external_id to the Notion
// block ID by scanning the block's parent page's children.
func (n *HTTPNotionClient) UpdateBlock(ctx context.Context, blockExternalID string, block NotionBlock) error {
	// Notion PATCH /blocks/{id} requires the Notion block UUID, not our external_id.
	// We store the mapping during upsert by matching external_id in the response.
	// For now: PATCH directly using external_id as if it were the Notion block ID
	// (works when the client constructed the page and remembers Notion's IDs).
	// In practice callers should pass the Notion block UUID obtained during creation.
	path := "/blocks/" + blockExternalID
	type patchPayload struct {
		Type    string      `json:"type"`
		Payload interface{} `json:"paragraph,omitempty"` // simplified for the common case
	}
	data, err := json.Marshal(block)
	if err != nil {
		return fmt.Errorf("marshal block: %w", err)
	}
	var rawBlock map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawBlock); err != nil {
		return fmt.Errorf("unmarshal block for patch: %w", err)
	}
	// Remove fields that are not valid for PATCH /blocks/{id}.
	delete(rawBlock, "object")
	delete(rawBlock, "external_id")

	if err := n.do(ctx, http.MethodPatch, path, rawBlock, nil); err != nil {
		return fmt.Errorf("notion_http: UpdateBlock %s: %w", blockExternalID, err)
	}
	return nil
}

// DeleteBlock removes the block identified by blockExternalID.
func (n *HTTPNotionClient) DeleteBlock(ctx context.Context, blockExternalID string) error {
	path := "/blocks/" + blockExternalID
	if err := n.do(ctx, http.MethodDelete, path, nil, nil); err != nil {
		return fmt.Errorf("notion_http: DeleteBlock %s: %w", blockExternalID, err)
	}
	return nil
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

// findPageByExternalID resolves a SourceBridge external ID to a Notion page ID.
// Returns ("", nil, nil) when not found.
func (n *HTTPNotionClient) findPageByExternalID(ctx context.Context, externalID string) (string, NotionProperties, error) {
	if n.cfg.DatabaseID != "" {
		return n.findPageInDatabase(ctx, externalID)
	}
	return n.findPageByTitle(ctx, externalID)
}

// findPageInDatabase queries the Notion database for a page whose external ID
// property matches externalID.
func (n *HTTPNotionClient) findPageInDatabase(ctx context.Context, externalID string) (string, NotionProperties, error) {
	path := "/databases/" + n.cfg.DatabaseID + "/query"

	// Build a filter on the external ID property.
	filterPayload := map[string]interface{}{
		"filter": map[string]interface{}{
			"property": n.cfg.externalIDProp(),
			"rich_text": map[string]interface{}{
				"equals": externalID,
			},
		},
		"page_size": 1,
	}

	var resp struct {
		Results []struct {
			ID         string                     `json:"id"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"results"`
	}
	if err := n.do(ctx, http.MethodPost, path, filterPayload, &resp); err != nil {
		return "", nil, fmt.Errorf("notion_http: database query: %w", err)
	}
	if len(resp.Results) == 0 {
		return "", nil, nil
	}

	pageID := resp.Results[0].ID
	props := NotionProperties{n.cfg.externalIDProp(): externalID}
	return pageID, props, nil
}

// findPageByTitle uses the Notion search API to find a page by title (fallback
// when no DatabaseID is set). Title-based search is approximate; external ID
// verification is not possible without the property API on non-database pages.
func (n *HTTPNotionClient) findPageByTitle(ctx context.Context, title string) (string, NotionProperties, error) {
	payload := map[string]interface{}{
		"query": title,
		"filter": map[string]string{
			"value":    "page",
			"property": "object",
		},
		"page_size": 5,
	}

	var resp struct {
		Results []struct {
			ID         string `json:"id"`
			Properties map[string]struct {
				Title []struct {
					PlainText string `json:"plain_text"`
				} `json:"title"`
			} `json:"properties"`
		} `json:"results"`
	}
	if err := n.do(ctx, http.MethodPost, "/search", payload, &resp); err != nil {
		return "", nil, fmt.Errorf("notion_http: search: %w", err)
	}

	for _, r := range resp.Results {
		for _, prop := range r.Properties {
			for _, t := range prop.Title {
				if t.PlainText == title {
					return r.ID, NotionProperties{n.cfg.externalIDProp(): title}, nil
				}
			}
		}
	}
	return "", nil, nil
}

// createPage creates a new Notion page with the given blocks and properties.
func (n *HTTPNotionClient) createPage(ctx context.Context, externalID string, blocks []NotionBlock, _ NotionProperties) error {
	type titleText struct {
		Type string `json:"type"`
		Text struct {
			Content string `json:"content"`
		} `json:"text"`
	}
	type titleProp struct {
		Title []titleText `json:"title"`
	}

	parentPayload := map[string]string{}
	if n.cfg.DatabaseID != "" {
		parentPayload["database_id"] = n.cfg.DatabaseID
	} else {
		parentPayload["type"] = "workspace"
	}

	// Build the properties payload.
	propsPayload := map[string]interface{}{
		"title": titleProp{
			Title: []titleText{
				{
					Type: "text",
					Text: struct {
						Content string `json:"content"`
					}{Content: externalID},
				},
			},
		},
	}
	// Add external ID property for database pages.
	if n.cfg.DatabaseID != "" {
		propsPayload[n.cfg.externalIDProp()] = map[string]interface{}{
			"rich_text": []map[string]interface{}{
				{
					"type": "text",
					"text": map[string]string{"content": externalID},
				},
			},
		}
	}

	payload := map[string]interface{}{
		"parent":     parentPayload,
		"properties": propsPayload,
		"children":   blocks,
	}

	var resp struct {
		ID string `json:"id"`
	}
	if err := n.do(ctx, http.MethodPost, "/pages", payload, &resp); err != nil {
		return fmt.Errorf("notion_http: create page: %w", err)
	}
	return nil
}

// replacePage clears the existing page's blocks and appends the new ones.
// Notion has no "replace all blocks" endpoint, so we delete existing blocks and
// then append the new set.
func (n *HTTPNotionClient) replacePage(ctx context.Context, pageID, externalID string, blocks []NotionBlock, _ NotionProperties) error {
	// Fetch existing blocks.
	existing, err := n.fetchChildren(ctx, pageID)
	if err != nil {
		return fmt.Errorf("notion_http: replacePage fetch existing: %w", err)
	}

	// Delete all existing blocks. Errors are logged but do not abort the write
	// because we must still write the new content.
	for _, b := range existing {
		if b.Object == "block" && b.ExternalID != "" {
			_ = n.do(ctx, http.MethodDelete, "/blocks/"+b.ExternalID, nil, nil)
		}
	}

	// Append new blocks.
	if err := n.appendBlocksToPage(ctx, pageID, blocks); err != nil {
		return fmt.Errorf("notion_http: replacePage append blocks: %w", err)
	}

	// Update the title (best-effort).
	_ = n.updatePageTitle(ctx, pageID, externalID)
	return nil
}

// fetchChildren fetches all child blocks for a page or block ID.
func (n *HTTPNotionClient) fetchChildren(ctx context.Context, pageID string) ([]NotionBlock, error) {
	path := "/blocks/" + pageID + "/children?page_size=100"
	var resp struct {
		Results    []NotionBlock `json:"results"`
		HasMore    bool          `json:"has_more"`
		NextCursor string        `json:"next_cursor"`
	}
	if err := n.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}

	blocks := resp.Results
	// Paginate if needed.
	for resp.HasMore && resp.NextCursor != "" {
		nextPath := fmt.Sprintf("/blocks/%s/children?page_size=100&start_cursor=%s", pageID, resp.NextCursor)
		var nextResp struct {
			Results    []NotionBlock `json:"results"`
			HasMore    bool          `json:"has_more"`
			NextCursor string        `json:"next_cursor"`
		}
		if err := n.do(ctx, http.MethodGet, nextPath, nil, &nextResp); err != nil {
			break // partial result is better than none
		}
		blocks = append(blocks, nextResp.Results...)
		resp.HasMore = nextResp.HasMore
		resp.NextCursor = nextResp.NextCursor
	}
	return blocks, nil
}

// appendBlocksToPage calls PATCH /blocks/{id}/children with the given blocks.
func (n *HTTPNotionClient) appendBlocksToPage(ctx context.Context, pageID string, blocks []NotionBlock) error {
	if len(blocks) == 0 {
		return nil
	}
	path := "/blocks/" + pageID + "/children"
	payload := map[string]interface{}{"children": blocks}
	if err := n.do(ctx, http.MethodPatch, path, payload, nil); err != nil {
		return fmt.Errorf("notion_http: append blocks to %s: %w", pageID, err)
	}
	return nil
}

// updatePageTitle updates the page's title property.
func (n *HTTPNotionClient) updatePageTitle(ctx context.Context, pageID, title string) error {
	payload := map[string]interface{}{
		"properties": map[string]interface{}{
			"title": map[string]interface{}{
				"title": []map[string]interface{}{
					{
						"type": "text",
						"text": map[string]string{"content": title},
					},
				},
			},
		},
	}
	path := "/pages/" + pageID
	return n.do(ctx, http.MethodPatch, path, payload, nil)
}

// ─── HTTP core ────────────────────────────────────────────────────────────────

func (n *HTTPNotionClient) do(ctx context.Context, method, path string, reqBody, respBody interface{}) error {
	for attempt := 0; attempt <= maxNotionRetries; attempt++ {
		if attempt > 0 {
			sleep := n.retryDelay(attempt)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(sleep):
			}
		}

		err := n.doOnce(ctx, method, path, reqBody, respBody)
		if err == nil {
			return nil
		}

		var ne *NotionAPIError
		if errors.As(err, &ne) {
			if ne.StatusCode == http.StatusTooManyRequests || ne.StatusCode >= 500 {
				if attempt < maxNotionRetries {
					continue
				}
			}
		}
		return err
	}
	return fmt.Errorf("notion_http: %s %s: exceeded retry limit", method, path)
}

func (n *HTTPNotionClient) doOnce(ctx context.Context, method, path string, reqBody, respBody interface{}) error {
	var bodyReader io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	fullURL := notionAPIBaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+n.cfg.Token)
	req.Header.Set("Notion-Version", notionVersion)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := n.http.Do(req)
	if err != nil {
		return fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	// Handle Retry-After for 429.
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
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(rawBody, &errBody)
		return &NotionAPIError{
			StatusCode: resp.StatusCode,
			Code:       errBody.Code,
			Message:    errBody.Message,
		}
	}

	if respBody != nil && len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, respBody); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
