// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// CA-323: pin the response-shape contract for OIDC error paths.
//
// CA-208 introduced the scrub: both error paths return
// `{"error":"authentication_failed","correlation_id":"<uuid>"}` and log
// the full reason server-side. Without these tests, a future "cleanup"
// commit could silently re-introduce the IdP-supplied error string or
// drop the correlation_id, regressing the schema-leak fix.
//
// stubOIDCProvider exists only to make `s.oidc != nil` and to return
// controlled errors from Exchange. The handler's two scrub branches —
// IdP-error and exchange-failure — are exercised independently.

type stubOIDCProvider struct {
	exchangeErr error
}

func (s *stubOIDCProvider) AuthorizationURL(_ context.Context) (string, string, error) {
	return "https://example.com/authorize?state=test", "test-state", nil
}

func (s *stubOIDCProvider) Exchange(_ context.Context, _, _ string) (string, error) {
	if s.exchangeErr != nil {
		return "", s.exchangeErr
	}
	return "stub-token", nil
}

// uuidRegex matches the RFC-4122 v4 UUID shape that uuid.New().String()
// emits. Pinning the format prevents a future commit from accidentally
// switching to a non-correlatable id.
var uuidRegex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestOIDCCallback_IdPSuppliedError_ReturnsScrubbedBody(t *testing.T) {
	s := &Server{oidc: &stubOIDCProvider{}}

	// IdP redirects back with attacker-controllable error params. The handler
	// MUST scrub `errMsg` and `desc` from the response body and emit a fresh
	// correlation_id.
	req := httptest.NewRequest(http.MethodGet,
		"/auth/oidc/callback?error=server_error&error_description=this+leaks+sensitive+state",
		nil)
	w := httptest.NewRecorder()

	s.handleOIDCCallback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}

	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body not JSON: %v; raw=%q", err, w.Body.String())
	}

	if body["error"] != "authentication_failed" {
		t.Errorf("error field: got %q, want %q", body["error"], "authentication_failed")
	}
	if !uuidRegex.MatchString(body["correlation_id"]) {
		t.Errorf("correlation_id not a v4 UUID: got %q", body["correlation_id"])
	}

	// The CA-208 contract is explicit that `description` is gone from the
	// response. Pin it here — re-introducing it would let an attacker leak
	// arbitrary strings to the browser via error_description.
	if _, present := body["description"]; present {
		t.Errorf("response leaked 'description' field; CA-208 intentionally removed it")
	}

	// The IdP-supplied error strings MUST NOT appear anywhere in the response
	// body (not in error, not in correlation_id, not elsewhere).
	for k, v := range body {
		if strings.Contains(v, "server_error") || strings.Contains(v, "this leaks sensitive state") {
			t.Errorf("field %q leaked IdP-supplied content: %q", k, v)
		}
	}
}

func TestOIDCCallback_ExchangeFailure_ReturnsScrubbedBody(t *testing.T) {
	// Force Exchange to fail with a leak-shaped error (would normally leak
	// IdP token endpoint URL, partial credentials, etc.).
	s := &Server{
		oidc: &stubOIDCProvider{
			exchangeErr: errors.New(
				"id_token verification failed: oidc: malformed jwt: invalid character at position 42 token=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJsZWFreS1pZHAtaW50ZXJuYWwifQ",
			),
		},
	}

	req := httptest.NewRequest(http.MethodGet,
		"/auth/oidc/callback?code=valid-looking-code&state=valid-looking-state",
		nil)
	w := httptest.NewRecorder()

	s.handleOIDCCallback(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d (exchange-failure path is 401)",
			w.Code, http.StatusUnauthorized)
	}

	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body not JSON: %v; raw=%q", err, w.Body.String())
	}
	if body["error"] != "authentication_failed" {
		t.Errorf("error field: got %q, want %q", body["error"], "authentication_failed")
	}
	if !uuidRegex.MatchString(body["correlation_id"]) {
		t.Errorf("correlation_id not a v4 UUID: got %q", body["correlation_id"])
	}

	// Critical: the leaky token contents must not appear in the response.
	rawBody := w.Body.String()
	for _, leak := range []string{
		"id_token verification failed",
		"malformed jwt",
		"eyJhbGciOi",
		"leaky-idp-internal",
	} {
		if strings.Contains(rawBody, leak) {
			t.Errorf("response leaked exchange error content: contains %q in %q", leak, rawBody)
		}
	}
}

func TestOIDCCallback_LogsFullErrorAtWarn(t *testing.T) {
	// Capture slog output. The full IdP error MUST be logged server-side at
	// WARN with the same correlation_id that's in the response — that's
	// how operators correlate the user's "auth failed" report back to the
	// underlying IdP issue.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	s := &Server{oidc: &stubOIDCProvider{}}

	req := httptest.NewRequest(http.MethodGet,
		"/auth/oidc/callback?error=invalid_request&error_description=missing+nonce",
		nil)
	w := httptest.NewRecorder()
	s.handleOIDCCallback(w, req)

	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	corrID := body["correlation_id"]
	if corrID == "" {
		t.Fatal("response missing correlation_id")
	}

	logged := buf.String()
	if !strings.Contains(logged, `"level":"WARN"`) {
		t.Errorf("expected WARN level log; got: %s", logged)
	}
	if !strings.Contains(logged, corrID) {
		t.Errorf("server-side log missing correlation_id %q; got: %s", corrID, logged)
	}
	if !strings.Contains(logged, "invalid_request") {
		t.Errorf("server-side log missing IdP error 'invalid_request' (must be retained for operator debugging); got: %s", logged)
	}
	if !strings.Contains(logged, "missing nonce") {
		t.Errorf("server-side log missing error_description (operator-facing context); got: %s", logged)
	}
}

func TestOIDCCallback_NoProvider_Returns404(t *testing.T) {
	// Belt-and-suspenders: when OIDC isn't configured at all, the handler
	// must return 404 with an explicit message — never silently fall
	// through to the scrubbed path (which would obscure the misconfig).
	s := &Server{} // oidc is nil

	req := httptest.NewRequest(http.MethodGet,
		"/auth/oidc/callback?error=server_error", nil)
	w := httptest.NewRecorder()
	s.handleOIDCCallback(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want %d (no provider configured)", w.Code, http.StatusNotFound)
	}
}

func TestOIDCCallback_MissingCodeOrState_NotScrubbed(t *testing.T) {
	// The "missing code or state" path is a client error, not an IdP-supplied
	// leak vector. It must NOT use the scrubbed shape (the user should see
	// the actionable message). Pin this so future cleanups don't over-scrub.
	s := &Server{oidc: &stubOIDCProvider{}}

	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback?code=&state=", nil)
	w := httptest.NewRecorder()
	s.handleOIDCCallback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}

	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] != "missing code or state" {
		t.Errorf("error field: got %q, want %q", body["error"], "missing code or state")
	}
	if body["correlation_id"] != "" {
		t.Error("missing-code path should not emit correlation_id (no scrub applies)")
	}
}
