// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sourcebridge/sourcebridge/internal/changewatch"
)

// ChangeEventDispatcher is the boundary the HTTP ingress and the MCP
// record_change tool funnel through. The single method matches
// changewatch.Router.Submit and lets tests substitute a stub without
// pulling the whole router into the test setup.
//
// Production wiring passes a *changewatch.Router; nil = ingress is
// disabled (responds 503).
type ChangeEventDispatcher interface {
	Submit(ctx context.Context, ev *changewatch.ChangeEvent) (changewatch.SubmitOutcome, error)
}

// connectorEventResponse is the JSON body returned to the connector on
// every accepted POST. The shape is stable across all connector kinds
// and matches the plan v5 §HTTP ingress endpoint contract:
//
//	{ "event_id": "...", "routed_to": "indexing|deduped|rate_limited|..." }
//
// On error paths the same `routed_to` field carries the rejection
// outcome so connectors don't need to inspect HTTP status alone to
// decide what to do next.
type connectorEventResponse struct {
	EventID  string `json:"event_id"`
	RoutedTo string `json:"routed_to"`
	Error    string `json:"error,omitempty"`
}

// handleConnectorEvent is the public HTTP ingress at
// POST /v1/connectors/{id}/events. Behind the
// SOURCEBRIDGE_CONNECTOR_API_ENABLED feature flag (default false).
//
// Phase 1.D: accepts authenticated bearer/JWT requests, validates the
// posted ChangeEvent against the schema, stamps trust + actor based on
// the auth context, dispatches to the change-watch router. HMAC-SHA256
// validation specific to GitHub webhooks lands in Phase 2 with the
// GitHub connector.
//
// Multi-tenant boundary: the auth middleware has already attached the
// org/user identity to the request context. The connector_id from the
// URL is recorded in source.connector_id but does NOT itself authorize
// access — the existing repo-access middleware on the route group
// gates the underlying repo by tenant.
func (s *Server) handleConnectorEvent(w http.ResponseWriter, r *http.Request) {
	connectorID := chi.URLParam(r, "id")
	if connectorID == "" {
		writeConnectorError(w, http.StatusBadRequest, "", "missing_connector_id", "connector id is required in the URL path")
		return
	}

	// Bound the body so a misbehaving connector can't DoS us with a
	// huge payload. The plan-v5 ChangeEvent shape is small (~few KB
	// max even with 1000 file entries); a 1 MiB ceiling is generous
	// without being a vulnerability.
	const maxBody = 1 << 20
	body := http.MaxBytesReader(w, r.Body, maxBody)
	defer r.Body.Close()

	var ev changewatch.ChangeEvent
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ev); err != nil {
		writeConnectorError(w, http.StatusBadRequest, "", "invalid_body", err.Error())
		return
	}

	// Stamp ingress-time fields. The router validates the schema; we
	// fill in fields the connector should NOT have authority over:
	// - source.kind is locked to HTTP-ingress-family kinds — a remote
	//   caller cannot claim to be the in-process record_change tool or
	//   the in-process fsnotify watcher. Phase 2's GitHub connector
	//   will specialize via translator code that stamps
	//   "github_webhook" / "github_app" before reaching this handler;
	//   in 1.D the only HTTP-ingress kind is the generic one.
	// - trust.received_via is always "http_ingress" — connectors cannot
	//   claim "in_process".
	// - trust.verified is true only when the connector's auth context
	//   produced a verified identity. In Phase 1 the bearer/JWT
	//   middleware enforces this; an unauthenticated request never
	//   reaches this handler.
	if ev.Source.ConnectorID == "" {
		ev.Source.ConnectorID = connectorID
	}
	// Lock source.kind regardless of body claim. A caller that posts
	// source.kind="mcp_record_change" or "fsnotify_local" must NOT be
	// able to spoof an in-process connector — those kinds carry
	// different downstream trust assumptions. In 1.D the HTTP ingress
	// only ever stamps SourceKindHTTPIngress; future phases that ship
	// translator-pre-stamped kinds (github_webhook, github_app) will
	// register their own routes, not relay through this generic
	// handler.
	if !isAcceptableHTTPIngressKind(ev.Source.Kind) {
		ev.Source.Kind = changewatch.SourceKindHTTPIngress
	}
	ev.Trust = changewatch.Trust{
		Verified:           true,
		VerificationMethod: "bearer",
		ReceivedVia:        "http_ingress",
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now().UTC()
	}

	// Dispatcher-not-wired path: the router lives behind
	// change_watch.enabled at boot time. When the operator has
	// connector_api.enabled=true but change_watch.enabled=false,
	// connectors get 503 with a clear message rather than silent
	// success or 404 fingerprinting.
	if s.changeDispatcher == nil {
		writeConnectorError(w, http.StatusServiceUnavailable, ev.EventID, "change_watch_disabled", "change-watch router is not enabled on this server")
		return
	}

	outcome, err := s.changeDispatcher.Submit(r.Context(), &ev)
	if err != nil {
		// Most rejection outcomes are 4xx (bad caller); a couple are
		// 5xx (router-side failure). Map per the plan §HTTP ingress
		// endpoint contract.
		status := connectorOutcomeStatus(outcome)
		slog.Info("connector ingress submit returned error",
			"connector_id", connectorID,
			"event_id", ev.EventID,
			"outcome", string(outcome),
			"err", err,
		)
		writeConnectorError(w, status, ev.EventID, string(outcome), err.Error())
		return
	}

	// Happy path: 202 Accepted with the routed_to outcome.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(connectorEventResponse{
		EventID:  ev.EventID,
		RoutedTo: string(outcome),
	})
}

// writeConnectorError emits the error envelope and HTTP status. Always
// writes the response body so connectors get actionable error details.
func writeConnectorError(w http.ResponseWriter, status int, eventID, outcome, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(connectorEventResponse{
		EventID:  eventID,
		RoutedTo: outcome,
		Error:    msg,
	})
}

// connectorOutcomeStatus maps a router SubmitOutcome to an HTTP status.
// The status mapping is part of the public Connector API contract (plan
// §HTTP ingress endpoint > Status codes); changing it is a major-bump
// signal to any external connector that depends on the status alone.
func connectorOutcomeStatus(outcome changewatch.SubmitOutcome) int {
	switch outcome {
	case changewatch.OutcomeIndexing, changewatch.OutcomeDeduped:
		// "Accepted, but observable side-effect already happened or is
		// in progress" — 202 either way. Deduped is not an error from
		// the connector's perspective; the same content was already
		// routed.
		return http.StatusAccepted
	case changewatch.OutcomeRateLimited:
		return http.StatusTooManyRequests
	case changewatch.OutcomeBreakerTripped:
		// Service unavailable on this repo until the breaker recovers;
		// connector should back off.
		return http.StatusServiceUnavailable
	case changewatch.OutcomeRejectedSchema, changewatch.OutcomeRejectedNoDelta, changewatch.OutcomeRejectedInvalidPaths:
		return http.StatusBadRequest
	case changewatch.OutcomeRejectedBranchMismatch, changewatch.OutcomeRejectedUnknownRepo:
		// Conflict shape: caller's claim about repo/branch doesn't
		// match server state.
		return http.StatusConflict
	default:
		// Any new outcome the router introduces lands here as 500 so
		// a missing case lights up loudly in the test suite.
		return http.StatusInternalServerError
	}
}

// ErrConnectorAPIDisabled is the sentinel returned when the operator
// disables the public ingress. Routes that would otherwise serve
// /v1/connectors/* surface this so a caller probing the path gets a
// distinct shape from "endpoint doesn't exist."
var ErrConnectorAPIDisabled = errors.New("connector_api: disabled by operator")

// isAcceptableHTTPIngressKind returns true when the source.kind in a
// posted ChangeEvent is allowed to flow through the HTTP ingress
// without being overwritten. The set is a small whitelist:
//
//   - SourceKindHTTPIngress — the generic 1.D kind
//
// Future kinds added by Phase 2 connectors (github_webhook, github_app)
// will not relay through this handler — they will register their own
// routes that pre-stamp the kind via translator code. A remote caller
// claiming to be `mcp_record_change` or `fsnotify_local` is silently
// rewritten to SourceKindHTTPIngress; we don't reject because the rest
// of the event may still be valid.
func isAcceptableHTTPIngressKind(k changewatch.SourceKind) bool {
	switch k {
	case changewatch.SourceKindHTTPIngress:
		return true
	}
	return false
}

// connectorIDFromHost is a tiny helper for tests: pulls the connector
// id from a path like "/v1/connectors/github-webhook:repo-1234/events".
// Production code uses chi.URLParam directly; this exists so the
// tests can synthesize the right URL shape without a chi context.
func connectorIDFromPath(p string) string {
	const prefix = "/v1/connectors/"
	if !strings.HasPrefix(p, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(p, prefix)
	if i := strings.Index(rest, "/"); i >= 0 {
		return rest[:i]
	}
	return rest
}
