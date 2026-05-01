// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sourcebridge/sourcebridge/internal/changewatch"
)

// stubChangeDispatcher records every Submit call and returns a
// configurable outcome/error pair. Mirrors the in-process test
// substitutes used in internal/changewatch tests.
type stubChangeDispatcher struct {
	mu       sync.Mutex
	calls    []*changewatch.ChangeEvent
	outcome  changewatch.SubmitOutcome
	err      error
}

func (s *stubChangeDispatcher) Submit(_ context.Context, ev *changewatch.ChangeEvent) (changewatch.SubmitOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := *ev
	s.calls = append(s.calls, &clone)
	if s.err != nil {
		return s.outcome, s.err
	}
	if s.outcome == "" {
		return changewatch.OutcomeIndexing, nil
	}
	return s.outcome, nil
}

func (s *stubChangeDispatcher) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *stubChangeDispatcher) lastCall() *changewatch.ChangeEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return nil
	}
	return s.calls[len(s.calls)-1]
}

// makeConnectorRequest builds a chi-aware *http.Request with the
// {id} URL param populated so handleConnectorEvent's chi.URLParam
// returns the expected value. Going through chi's RouteContext is the
// only way to populate that param outside a real router.
func makeConnectorRequest(t *testing.T, connectorID string, body interface{}) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/connectors/"+connectorID+"/events", &buf)
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", connectorID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// validEvent builds a baseline ChangeEvent the connector handler
// will accept (subject to the dispatcher's behavior).
func validEvent() changewatch.ChangeEvent {
	return changewatch.ChangeEvent{
		SchemaVersion: changewatch.ChangeEventSchemaVersion,
		EventID:       "ev-test-1",
		RepositoryID:  "repo-1234",
		OccurredAt:    time.Now().UTC(),
		Branch:        "main",
		Files: []changewatch.FileChange{
			{Path: "pkg0/file1.go", Status: changewatch.FileChangeModified},
		},
		Source: changewatch.ChangeSource{
			Kind:        changewatch.SourceKindHTTPIngress,
			ConnectorID: "test-connector",
		},
	}
}

// TestConnectorEvent_DispatcherNotWired — when the operator enables
// connector_api but change_watch is off, the endpoint reports 503 with
// a clear error rather than silently accepting events into a
// nil-dispatcher.
func TestConnectorEvent_DispatcherNotWired(t *testing.T) {
	srv := &Server{} // changeDispatcher=nil
	rec := httptest.NewRecorder()
	srv.handleConnectorEvent(rec, makeConnectorRequest(t, "test-connector", validEvent()))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	var resp connectorEventResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.RoutedTo != "change_watch_disabled" {
		t.Errorf("routed_to = %q, want change_watch_disabled", resp.RoutedTo)
	}
}

// TestConnectorEvent_HappyPath — a well-formed event reaches the
// dispatcher with trust populated and the connector_id stamped.
func TestConnectorEvent_HappyPath(t *testing.T) {
	disp := &stubChangeDispatcher{}
	srv := &Server{changeDispatcher: disp}
	rec := httptest.NewRecorder()
	srv.handleConnectorEvent(rec, makeConnectorRequest(t, "github-webhook:repo-1234", validEvent()))

	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	if disp.callCount() != 1 {
		t.Fatalf("dispatcher call count = %d, want 1", disp.callCount())
	}
	got := disp.lastCall()
	if got.Trust.ReceivedVia != "http_ingress" {
		t.Errorf("trust.received_via = %q, want http_ingress", got.Trust.ReceivedVia)
	}
	if !got.Trust.Verified {
		t.Errorf("trust.verified = false, want true (auth middleware should have gated)")
	}
	if got.Trust.VerificationMethod != "bearer" {
		t.Errorf("trust.verification_method = %q, want bearer", got.Trust.VerificationMethod)
	}
	// The connector_id stamping defaults to the URL parameter when the
	// body didn't set Source.ConnectorID — but our valid event sets it
	// to "test-connector", which should be preserved.
	if got.Source.ConnectorID != "test-connector" {
		t.Errorf("source.connector_id = %q, want test-connector (body should be preserved)", got.Source.ConnectorID)
	}
}

// TestConnectorEvent_ConnectorIDStampingFromURL — when the body omits
// source.connector_id, the URL path's {id} parameter fills it in.
// This pins the contract that operators don't need to repeat the
// connector id in the body.
func TestConnectorEvent_ConnectorIDStampingFromURL(t *testing.T) {
	disp := &stubChangeDispatcher{}
	srv := &Server{changeDispatcher: disp}
	ev := validEvent()
	ev.Source.ConnectorID = ""
	rec := httptest.NewRecorder()
	srv.handleConnectorEvent(rec, makeConnectorRequest(t, "from-url-only", ev))

	got := disp.lastCall()
	if got == nil || got.Source.ConnectorID != "from-url-only" {
		t.Errorf("source.connector_id = %q, want from-url-only", got.Source.ConnectorID)
	}
}

// TestConnectorEvent_DefaultsHTTPIngressKind — when the body omits
// source.kind, the handler stamps SourceKindHTTPIngress so callers
// don't have to know about the enum yet (Phase 2 specializes this for
// GitHub).
func TestConnectorEvent_DefaultsHTTPIngressKind(t *testing.T) {
	disp := &stubChangeDispatcher{}
	srv := &Server{changeDispatcher: disp}
	ev := validEvent()
	ev.Source.Kind = ""
	rec := httptest.NewRecorder()
	srv.handleConnectorEvent(rec, makeConnectorRequest(t, "x", ev))

	got := disp.lastCall()
	if got == nil || got.Source.Kind != changewatch.SourceKindHTTPIngress {
		t.Errorf("source.kind = %q, want http_ingress", got.Source.Kind)
	}
}

// TestConnectorEvent_TrustOverridden — even if a malicious connector
// stamps trust.verified=true with verification_method="none" in the
// body, the handler overwrites trust at ingress so the router cannot
// be tricked.
func TestConnectorEvent_TrustOverridden(t *testing.T) {
	disp := &stubChangeDispatcher{}
	srv := &Server{changeDispatcher: disp}
	ev := validEvent()
	ev.Trust = changewatch.Trust{Verified: true, VerificationMethod: "none", ReceivedVia: "in_process"}
	rec := httptest.NewRecorder()
	srv.handleConnectorEvent(rec, makeConnectorRequest(t, "x", ev))

	got := disp.lastCall()
	if got == nil {
		t.Fatalf("dispatcher not called")
	}
	if got.Trust.ReceivedVia != "http_ingress" {
		t.Errorf("trust.received_via = %q, want http_ingress (must overwrite body claim)", got.Trust.ReceivedVia)
	}
	if got.Trust.VerificationMethod != "bearer" {
		t.Errorf("trust.verification_method = %q, want bearer (must overwrite body claim)", got.Trust.VerificationMethod)
	}
}

// TestConnectorEvent_BadJSON — malformed body gets 400 with an
// actionable error.
func TestConnectorEvent_BadJSON(t *testing.T) {
	disp := &stubChangeDispatcher{}
	srv := &Server{changeDispatcher: disp}
	req := httptest.NewRequest(http.MethodPost, "/v1/connectors/x/events", strings.NewReader("not json"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "x")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleConnectorEvent(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if disp.callCount() != 0 {
		t.Errorf("dispatcher call count = %d, want 0 (rejected before dispatch)", disp.callCount())
	}
}

// TestConnectorEvent_OutcomeStatusMapping — every router outcome maps
// to a documented HTTP status code per the plan §HTTP ingress endpoint
// contract.
func TestConnectorEvent_OutcomeStatusMapping(t *testing.T) {
	cases := []struct {
		outcome    changewatch.SubmitOutcome
		err        error
		wantStatus int
	}{
		{changewatch.OutcomeIndexing, nil, http.StatusAccepted},
		{changewatch.OutcomeDeduped, nil, http.StatusAccepted},
		{changewatch.OutcomeRateLimited, errors.New("rate limited"), http.StatusTooManyRequests},
		{changewatch.OutcomeBreakerTripped, errors.New("breaker open"), http.StatusServiceUnavailable},
		{changewatch.OutcomeRejectedSchema, errors.New("schema"), http.StatusBadRequest},
		{changewatch.OutcomeRejectedNoDelta, errors.New("empty delta"), http.StatusBadRequest},
		{changewatch.OutcomeRejectedInvalidPaths, errors.New("invalid path"), http.StatusBadRequest},
		{changewatch.OutcomeRejectedBranchMismatch, errors.New("branch"), http.StatusConflict},
		{changewatch.OutcomeRejectedUnknownRepo, errors.New("unknown repo"), http.StatusConflict},
	}

	for _, c := range cases {
		t.Run(string(c.outcome), func(t *testing.T) {
			disp := &stubChangeDispatcher{outcome: c.outcome, err: c.err}
			srv := &Server{changeDispatcher: disp}
			rec := httptest.NewRecorder()
			srv.handleConnectorEvent(rec, makeConnectorRequest(t, "x", validEvent()))
			if rec.Code != c.wantStatus {
				t.Errorf("outcome=%q status=%d want=%d body=%s", c.outcome, rec.Code, c.wantStatus, rec.Body.String())
			}
			// Body always has routed_to for both error and success
			// shapes — the contract.
			var resp connectorEventResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.RoutedTo != string(c.outcome) {
				t.Errorf("routed_to = %q, want %q", resp.RoutedTo, c.outcome)
			}
		})
	}
}

// TestConnectorEvent_UnknownOutcomeIs500 — a future router that
// introduces a new outcome enum value should NOT silently leak through
// as 200; the default-case 500 lights up the missing case.
func TestConnectorEvent_UnknownOutcomeIs500(t *testing.T) {
	disp := &stubChangeDispatcher{outcome: "future_outcome_we_havent_mapped", err: errors.New("future")}
	srv := &Server{changeDispatcher: disp}
	rec := httptest.NewRecorder()
	srv.handleConnectorEvent(rec, makeConnectorRequest(t, "x", validEvent()))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (unknown outcomes must alarm loudly)", rec.Code)
	}
}

// TestConnectorEvent_DisallowsUnknownFields — a body with extra
// top-level fields the schema doesn't recognize is rejected. This
// pins the schema-stability checkpoint contract: connectors that
// shipped against 0.x should not silently degrade when 1.0 lands;
// they should fail loudly so the operator notices.
func TestConnectorEvent_DisallowsUnknownFields(t *testing.T) {
	disp := &stubChangeDispatcher{}
	srv := &Server{changeDispatcher: disp}
	body := `{"schema_version":"0.1","event_id":"x","repository_id":"r","branch":"main","occurred_at":"2026-04-30T00:00:00Z","files":[{"path":"a.go","status":"modified"}],"source":{"kind":"http_ingress"},"completely_invented_field":42}`
	req := httptest.NewRequest(http.MethodPost, "/v1/connectors/x/events", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "x")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleConnectorEvent(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (unknown fields must reject)", rec.Code)
	}
	if disp.callCount() != 0 {
		t.Errorf("dispatcher called despite unknown-field rejection")
	}
}

// TestConnectorIDFromPath_Helper — the small URL-parse shim used by
// tests is stable over the documented path shape.
func TestConnectorIDFromPath_Helper(t *testing.T) {
	cases := map[string]string{
		"/v1/connectors/github-webhook:repo-1234/events": "github-webhook:repo-1234",
		"/v1/connectors/x/events":                        "x",
		"/v1/connectors/y":                               "y",
		"":                                               "",
		"/something/else":                                "",
	}
	for path, want := range cases {
		if got := connectorIDFromPath(path); got != want {
			t.Errorf("connectorIDFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}
