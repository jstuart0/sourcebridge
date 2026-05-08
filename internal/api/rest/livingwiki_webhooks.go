// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// livingwiki_webhooks.go provides HTTP handlers for the living-wiki trigger
// layer (Workstream A1.P3).
//
// # Confluence webhook
//
// Confluence Cloud calls POST /webhooks/confluence with a JSON payload and an
// X-Confluence-Signature header. The header value is "sha256=<hex-digest>"
// where the digest is HMAC-SHA256 over the raw request body using the shared
// webhook secret (configured as SOURCEBRIDGE_LIVING_WIKI_CONFLUENCE_WEBHOOK_SECRET).
//
// We validate the signature before any parsing to avoid amplification attacks
// from unauthenticated callers. On signature failure the handler returns 401.
//
// Confluence fires page_updated events when any page changes. We filter for
// pages that carry the "sourcebridge_page_id" property (set by the Confluence
// sink adapter on upsert). For matching pages we construct a [SinkBlockEditEvent]
// per changed block and submit to the Dispatcher.
//
// # Notion poll endpoint
//
// Notion's webhook model as of early 2026 is limited to automation-triggered
// callbacks for database changes — it does not fire on arbitrary block edits.
// POST /webhooks/notion-poll is therefore a polling endpoint: an external poller
// (e.g. a Kubernetes CronJob or the SourceBridge background scheduler) hits this
// endpoint to trigger a reconciliation pass for a given repo + page. The endpoint
// calls Dispatcher.Submit(ManualRefreshEvent) which in turn calls PollAndReconcile
// for each configured Notion sink.
//
// # Route registration
//
// Enterprise builds wire these handlers via enterprise_routes.go. OSS builds
// can register them independently via [RegisterLivingWikiRoutes], which adds
// the routes without requiring the enterprise routes.Context.
package rest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/webhook"
)

// ─────────────────────────────────────────────────────────────────────────────
// Deps injected at registration time
// ─────────────────────────────────────────────────────────────────────────────

// LivingWikiWebhookDeps holds the dependencies needed by the living-wiki
// webhook handlers. Separated from the rest of the Server to keep the handler
// constructors testable without a full Server.
type LivingWikiWebhookDeps struct {
	// Dispatcher receives normalized events. Required.
	Dispatcher *webhook.Dispatcher

	// ConfluenceWebhookSecret is the HMAC-SHA256 shared secret for validating
	// X-Confluence-Signature headers, captured at handler construction.
	// Tests typically set this directly. The production path also wires
	// ConfluenceSecretResolver below so post-boot admin-UI changes take
	// effect without restart.
	//
	// CA-206 (2026-05-08): the handler now refuses to dispatch when the
	// effective secret is empty. The previous "skip signature validation
	// when empty" semantics are removed — every Confluence webhook request
	// now requires a configured secret + valid signature.
	ConfluenceWebhookSecret string

	// ConfluenceSecretResolver, if non-nil, is consulted on every request
	// before falling back to ConfluenceWebhookSecret. CA-206: handler-time
	// resolution lets operators configure the secret via the admin UI
	// (which writes through to the resolver's cache) without a server
	// restart. Returns the resolved secret (possibly ""), or an error if
	// resolution itself failed (transient — handler treats error as
	// "use the captured secret").
	ConfluenceSecretResolver func() (string, error)

	// NotionWebhookSecret is reserved for future Notion webhook validation.
	// Unused until Notion supports arbitrary block-level webhooks.
	NotionWebhookSecret string

	// NotionPollAuthMiddleware wraps /webhooks/notion-poll with bearer auth
	// so unauthenticated callers can't trigger reconciliation. Required
	// when Dispatcher is non-nil; the route refuses to register without it.
	// Typically set to s.authMiddleware() composed with
	// auth.RequireRole(auth.RoleAdmin) — see router.go.
	NotionPollAuthMiddleware func(http.Handler) http.Handler
}

// ─────────────────────────────────────────────────────────────────────────────
// Route registration
// ─────────────────────────────────────────────────────────────────────────────

// RegisterLivingWikiRoutes registers the living-wiki webhook endpoints on the
// given router. Both OSS and enterprise builds can call this; enterprise builds
// additionally call it from enterprise_routes.go after wiring the Dispatcher.
//
// Routes added:
//
//	POST /webhooks/confluence      — Confluence Cloud page_updated events
//	POST /webhooks/notion-poll     — External poller trigger for Notion reconciliation
func RegisterLivingWikiRoutes(r chi.Router, deps LivingWikiWebhookDeps) {
	h := newLivingWikiWebhookHandler(deps)
	// CA-206: /webhooks/confluence is unauthenticated at the route layer
	// because Confluence Cloud cannot inject Authorization headers. The
	// handler validates HMAC-SHA256 against the resolved secret per request
	// and refuses unauthenticated dispatch when the secret is unconfigured.
	r.Post("/webhooks/confluence", h.confluenceWebhook)

	// NEW-H1 (2026-05-08): /webhooks/notion-poll requires admin bearer
	// auth. The endpoint is the trigger for an operator-controlled CronJob,
	// not a SaaS push, so bearer is the cleaner fit (no shared-secret
	// sprawl; the trigger ties to a real service-account API token).
	// Refuse to register if no auth middleware was provided — better to
	// 404 than expose an unauthenticated dispatcher trigger.
	if deps.NotionPollAuthMiddleware != nil {
		r.Group(func(r chi.Router) {
			r.Use(deps.NotionPollAuthMiddleware)
			r.Post("/webhooks/notion-poll", h.notionPollWebhook)
		})
	} else {
		slog.Warn("livingwiki: /webhooks/notion-poll NOT registered (no auth middleware provided) — operator must configure auth to enable Notion-poll webhook")
	}
}

// RegisterLivingWikiDisabledRoutes registers 503 stub handlers for the
// living-wiki webhook paths. Called when the dispatcher is nil (embedded mode,
// kill-switch active, or assembly failure) so webhook senders receive a
// deterministic "feature disabled" response rather than a 404.
//
// A 503 is correct here: the route exists and is understood by the server, but
// the feature is currently unavailable. This matches the semantics of service
// unavailability vs. route not found.
func RegisterLivingWikiDisabledRoutes(r chi.Router) {
	const body = `{"error":"living_wiki_disabled","message":"Living wiki feature is not active on this server instance."}`
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, body)
	}
	r.Post("/webhooks/confluence", handler)
	r.Post("/webhooks/notion-poll", handler)
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler struct
// ─────────────────────────────────────────────────────────────────────────────

type livingWikiWebhookHandler struct {
	deps LivingWikiWebhookDeps
}

func newLivingWikiWebhookHandler(deps LivingWikiWebhookDeps) *livingWikiWebhookHandler {
	return &livingWikiWebhookHandler{deps: deps}
}

// ─────────────────────────────────────────────────────────────────────────────
// Confluence handler
// ─────────────────────────────────────────────────────────────────────────────

// confluenceWebhookPayload is a minimal parse of the Confluence Cloud webhook
// envelope. Confluence sends many event types; we only act on "page_updated".
//
// Confluence Cloud webhook envelope (as of REST API v2):
//
//	{
//	  "eventType": "page_updated",
//	  "timestamp": 1714000000000,
//	  "page": {
//	    "id":        "123456789",
//	    "title":     "Architecture Overview",
//	    "spaceKey":  "ENG",
//	    "version":   {"number": 5},
//	    "properties": [
//	      {"key": "sourcebridge_page_id", "value": "repo1.arch.internal_auth"},
//	      {"key": "sourcebridge_repo_id", "value": "repo1"}
//	    ]
//	  },
//	  "actor": {"displayName": "Jane Doe", "accountId": "abc123"},
//	  "webhookEvent": "page_updated"
//	}
//
// Note: The property list is not always present in the webhook body; when
// absent we fall through without dispatching (the polling path will catch
// the change). The "sourcebridge_page_id" property is only present on pages
// that SourceBridge has previously written to.
//
// # Deferred
//
// Confluence does not include the changed block IDs in the webhook payload —
// only the page that changed. The dispatcher therefore submits a whole-page
// ManualRefreshEvent which triggers PollAndReconcile, which fetches the page
// and diffs blocks using the stored canonical AST. This is a safe fallback
// at the cost of one extra API round-trip per page update.
type confluenceWebhookPayload struct {
	EventType    string `json:"eventType"`
	WebhookEvent string `json:"webhookEvent"`
	Timestamp    int64  `json:"timestamp"`
	Page         struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		SpaceKey string `json:"spaceKey"`
		Properties []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"properties"`
	} `json:"page"`
	Actor struct {
		DisplayName string `json:"displayName"`
		AccountID   string `json:"accountId"`
	} `json:"actor"`
}

func (h *livingWikiWebhookHandler) confluenceWebhook(w http.ResponseWriter, r *http.Request) {
	// CA-206: handler-time secret resolution.  An admin who configures the
	// secret via the admin UI gets the new value picked up on the next
	// request without needing a server restart.  The resolver is expected
	// to cache; per-request lookups are cheap.
	secret := h.deps.ConfluenceWebhookSecret
	if h.deps.ConfluenceSecretResolver != nil {
		if resolved, resolveErr := h.deps.ConfluenceSecretResolver(); resolveErr == nil {
			secret = resolved
		} else {
			slog.Warn("livingwiki: confluence webhook: resolver returned err — falling back to captured secret",
				"err", resolveErr)
		}
	}

	// CA-206: refuse to dispatch if the secret is unconfigured.  Previously
	// an empty secret silently skipped signature validation — now the
	// route returns 503 with a scrubbed body (operator-actionable details
	// only in the server log).
	if secret == "" {
		slog.Warn("livingwiki: confluence webhook: secret unconfigured — dispatch refused",
			"remedy", "configure ConfluenceWebhookSecret in admin/livingwiki settings",
			"remote_addr", r.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, `{"error":"route_unavailable"}`)
		return
	}

	// Read body first (needed for both signature and parsing).
	body, err := io.ReadAll(io.LimitReader(r.Body, 2*1024*1024)) // 2 MB safety cap
	if err != nil {
		slog.Error("livingwiki: confluence webhook: reading body", "err", err)
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	// Signature validation.  Now unconditional — the missing-secret branch
	// above already returned 503.
	sig := r.Header.Get("X-Confluence-Signature")
	if !validateConfluenceSignature(body, sig, secret) {
		slog.Warn("livingwiki: confluence webhook: signature validation failed",
			"remote_addr", r.RemoteAddr,
		)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// Parse envelope.
	var payload confluenceWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		slog.Error("livingwiki: confluence webhook: parsing payload", "err", err)
		http.Error(w, "invalid JSON payload", http.StatusBadRequest)
		return
	}

	// We only act on page_updated events.
	eventType := payload.EventType
	if eventType == "" {
		eventType = payload.WebhookEvent
	}
	if eventType != "page_updated" {
		// Acknowledge non-actionable events so Confluence stops retrying them.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Extract SourceBridge IDs from page properties.
	var sbPageID, sbRepoID string
	for _, prop := range payload.Page.Properties {
		switch prop.Key {
		case "sourcebridge_page_id":
			sbPageID = prop.Value
		case "sourcebridge_repo_id":
			sbRepoID = prop.Value
		}
	}

	if sbPageID == "" || sbRepoID == "" {
		// This page is not managed by SourceBridge — ignore.
		slog.Debug("livingwiki: confluence webhook: page not managed by sourcebridge; ignoring",
			"confluence_page_id", payload.Page.ID,
			"space_key", payload.Page.SpaceKey,
		)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Derive a delivery ID from the Confluence page ID + timestamp so
	// duplicate deliveries for the same page update are deduplicated.
	deliveryID := fmt.Sprintf("confluence:%s:%d", payload.Page.ID, payload.Timestamp)

	// We submit a ManualRefreshEvent here rather than a SinkBlockEditEvent
	// because Confluence does not include changed block IDs in the webhook
	// payload. The dispatcher's ManualRefreshEvent handler calls PollAndReconcile
	// which fetches the page, diffs blocks, and submits SinkBlockEdits.
	event := webhook.ManualRefreshEvent{
		Repo:        sbRepoID,
		Delivery:    deliveryID,
		PageID:      sbPageID,
		RequestedBy: payload.Actor.AccountID,
	}

	if err := h.deps.Dispatcher.Submit(r.Context(), event); err != nil {
		if err == webhook.ErrDuplicate {
			// Already processed — idempotent 200.
			w.WriteHeader(http.StatusOK)
			return
		}
		slog.Error("livingwiki: confluence webhook: dispatcher rejected event",
			"err", err,
			"repo_id", sbRepoID,
			"page_id", sbPageID,
		)
		http.Error(w, "event queue full", http.StatusServiceUnavailable)
		return
	}

	slog.Info("livingwiki: confluence webhook: event submitted",
		"repo_id", sbRepoID,
		"page_id", sbPageID,
		"confluence_page_id", payload.Page.ID,
	)
	w.WriteHeader(http.StatusAccepted)
}

// validateConfluenceSignature returns true when the X-Confluence-Signature
// header matches the HMAC-SHA256 of the body using the given secret.
//
// Confluence sends the signature as "sha256=<hex-digest>". An empty or
// malformed header always fails validation.
func validateConfluenceSignature(body []byte, header, secret string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	gotHex := strings.TrimPrefix(header, prefix)
	got, err := hex.DecodeString(gotHex)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)
	return hmac.Equal(got, expected)
}

// ─────────────────────────────────────────────────────────────────────────────
// Notion poll handler
// ─────────────────────────────────────────────────────────────────────────────

// notionPollRequest is the JSON body accepted by POST /webhooks/notion-poll.
//
// An external poller (scheduler, CronJob) POSTs this to trigger reconciliation
// for a given repo and optional page. When PageID is empty, the dispatcher
// triggers a full-repo reconciliation pass.
type notionPollRequest struct {
	// RepoID is the opaque SourceBridge repository identifier. Required.
	RepoID string `json:"repo_id"`

	// PageID is the SourceBridge page ID to reconcile. Optional.
	// When absent, all pages for the repo are reconciled.
	PageID string `json:"page_id,omitempty"`

	// RequestedBy is the user or service identity triggering the poll.
	// Used for audit logging. Optional.
	RequestedBy string `json:"requested_by,omitempty"`
}

func (h *livingWikiWebhookHandler) notionPollWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024)) // 64 KB — request is tiny
	if err != nil {
		slog.Error("livingwiki: notion poll: reading body", "err", err)
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var req notionPollRequest
	if err := json.Unmarshal(body, &req); err != nil {
		slog.Error("livingwiki: notion poll: parsing request", "err", err)
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if req.RepoID == "" {
		http.Error(w, "repo_id is required", http.StatusBadRequest)
		return
	}

	// Use a delivery ID derived from the repo + page + wall-clock minute so
	// two polls within the same minute for the same target are idempotent.
	minuteBucket := time.Now().Truncate(time.Minute).Unix()
	deliveryID := fmt.Sprintf("notion-poll:%s:%s:%d", req.RepoID, req.PageID, minuteBucket)

	event := webhook.ManualRefreshEvent{
		Repo:        req.RepoID,
		Delivery:    deliveryID,
		PageID:      req.PageID,
		RequestedBy: req.RequestedBy,
	}

	if err := h.deps.Dispatcher.Submit(r.Context(), event); err != nil {
		if err == webhook.ErrDuplicate {
			w.WriteHeader(http.StatusOK)
			return
		}
		slog.Error("livingwiki: notion poll: dispatcher rejected event",
			"err", err,
			"repo_id", req.RepoID,
			"page_id", req.PageID,
		)
		http.Error(w, "event queue full", http.StatusServiceUnavailable)
		return
	}

	slog.Info("livingwiki: notion poll: event submitted",
		"repo_id", req.RepoID,
		"page_id", req.PageID,
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "accepted",
		"repo_id": req.RepoID,
		"page_id": req.PageID,
	})
}
