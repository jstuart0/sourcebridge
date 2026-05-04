// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// ─────────────────────────────────────────────────────────────────────────────
// normalizeSite
// ─────────────────────────────────────────────────────────────────────────────

func TestNormalizeSite(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"mycompany", "mycompany"},
		{"mycompany.atlassian.net", "mycompany"},
		{"  mycompany  ", "mycompany"},
		{"  mycompany.atlassian.net  ", "mycompany"},
		{"", ""},
	}
	for _, tc := range cases {
		got := normalizeSite(tc.input)
		if got != tc.want {
			t.Errorf("normalizeSite(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// testConfluenceConnection
// ─────────────────────────────────────────────────────────────────────────────

// newConfluenceStub creates a test server that responds to
// /wiki/rest/api/user/current with the given status code.
func newConfluenceStub(t *testing.T, status int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/wiki/rest/api/user/current", func(w http.ResponseWriter, r *http.Request) {
		// Verify basic auth was sent.
		_, _, ok := r.BasicAuth()
		if !ok {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			w.Write([]byte(`{"accountId":"123","displayName":"Test User"}`))
		}
	})
	return httptest.NewServer(mux)
}

// patchConfluenceHost rewrites https://<site>.atlassian.net/... to use the
// stub server. We do this by overriding the URL in a custom RoundTripper so
// testConfluenceConnection doesn't need to be parameterised with a base URL.
// Instead, we temporarily swap http.DefaultTransport.
type rewriteTransport struct {
	target string // e.g. "http://127.0.0.1:PORT"
	inner  http.RoundTripper
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request so we can mutate the URL.
	r2 := req.Clone(req.Context())
	// Replace host + scheme with the stub server's address.
	r2.URL.Scheme = "http"
	r2.URL.Host = strings.TrimPrefix(rt.target, "http://")
	return rt.inner.RoundTrip(r2)
}

func withStubTransport(t *testing.T, target string, fn func()) {
	t.Helper()
	orig := http.DefaultClient.Transport
	if orig == nil {
		orig = http.DefaultTransport
	}
	http.DefaultClient.Transport = &rewriteTransport{target: target, inner: orig}
	defer func() { http.DefaultClient.Transport = orig }()
	fn()
}

func TestTestConfluenceConnection_Success(t *testing.T) {
	srv := newConfluenceStub(t, http.StatusOK)
	defer srv.Close()

	withStubTransport(t, srv.URL, func() {
		res, err := testConfluenceConnection(context.Background(), "mycompany", "user@example.com", "token123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.Ok {
			t.Errorf("expected Ok=true, got false; message=%v", res.Message)
		}
	})
}

func TestTestConfluenceConnection_Unauthorized(t *testing.T) {
	srv := newConfluenceStub(t, http.StatusUnauthorized)
	defer srv.Close()

	withStubTransport(t, srv.URL, func() {
		res, err := testConfluenceConnection(context.Background(), "mycompany", "user@example.com", "bad-token")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Ok {
			t.Error("expected Ok=false for 401")
		}
		if res.Message == nil || !strings.Contains(*res.Message, "401") {
			t.Errorf("expected message mentioning 401, got %v", res.Message)
		}
	})
}

func TestTestConfluenceConnection_MissingSite(t *testing.T) {
	res, err := testConfluenceConnection(context.Background(), "", "user@example.com", "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Ok {
		t.Error("expected Ok=false when site is empty")
	}
	if res.Message == nil || !strings.Contains(strings.ToLower(*res.Message), "site") {
		t.Errorf("expected message about site, got %v", res.Message)
	}
}

func TestTestConfluenceConnection_MissingEmail(t *testing.T) {
	res, err := testConfluenceConnection(context.Background(), "mycompany", "", "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Ok {
		t.Error("expected Ok=false when email is empty")
	}
}

func TestTestConfluenceConnection_MissingToken(t *testing.T) {
	res, err := testConfluenceConnection(context.Background(), "mycompany", "user@example.com", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Ok {
		t.Error("expected Ok=false when token is empty")
	}
}

func TestTestConfluenceConnection_FullHostnameTolerance(t *testing.T) {
	// User typed "mycompany.atlassian.net" instead of "mycompany" — should work.
	srv := newConfluenceStub(t, http.StatusOK)
	defer srv.Close()

	withStubTransport(t, srv.URL, func() {
		res, err := testConfluenceConnection(context.Background(), "mycompany.atlassian.net", "user@example.com", "token")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.Ok {
			t.Errorf("expected Ok=true for full hostname input; message=%v", res.Message)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// mapLivingWikiSettings round-trip for ConfluenceSite
// ─────────────────────────────────────────────────────────────────────────────

func TestMapLivingWikiSettings_ConfluenceSite(t *testing.T) {
	s := livingwiki.Settings{
		ConfluenceSite: "mycompany",
	}
	out := mapLivingWikiSettings(s, false)
	if out.ConfluenceSite == nil {
		t.Fatal("expected ConfluenceSite to be set")
	}
	if *out.ConfluenceSite != "mycompany" {
		t.Errorf("ConfluenceSite = %q, want %q", *out.ConfluenceSite, "mycompany")
	}
}

func TestMapLivingWikiSettings_ConfluenceSite_EmptyIsNil(t *testing.T) {
	s := livingwiki.Settings{
		ConfluenceSite: "",
	}
	out := mapLivingWikiSettings(s, false)
	if out.ConfluenceSite != nil {
		t.Errorf("expected ConfluenceSite to be nil when empty, got %q", *out.ConfluenceSite)
	}
}
