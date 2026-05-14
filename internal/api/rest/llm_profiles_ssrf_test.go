// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

// LLM profiles SSRF validation tests (CA-335).
//
// ValidateLLMBaseURL is wired into handleCreateLLMProfile and
// handleUpdateLLMProfile. These tests pin the gate behaviour under
// AllowPrivateBaseURL=true (default, preserves Ollama workflows) and
// AllowPrivateBaseURL=false (cloud/hardened deployments).
//
// Bare IP literals are used for RFC1918 / link-local test cases so
// ValidateLLMBaseURL takes the net.ParseIP fast-path and no real DNS
// lookup is performed. The scheme-allowlist path (ftp://) also requires
// no DNS lookup. The one public-IP case uses 93.184.216.34
// (IANA example.com) to avoid real DNS while staying clearly public.
//
// Test matrix (plan lines 216-224, XAN-H1):
//   (a)  Public URL — accepted under AllowPrivateBaseURL=true.
//   (a2) Public URL — accepted under AllowPrivateBaseURL=false (not in denylist).
//   (b)  Private RFC1918 IP, AllowPrivateBaseURL=false — rejected 400.
//   (c)  Same private IP, AllowPrivateBaseURL=true — accepted.
//   (d)  Cloud-metadata 169.254.169.254, AllowPrivateBaseURL=false — rejected 400.
//   (d2) Cloud-metadata URL, AllowPrivateBaseURL=true — accepted (documented residual).
//   (e)  Disallowed scheme ftp:// — rejected regardless of flag.
//   (-) Empty base_url — accepted (means "use provider default").
//
// Update-handler mirror (subset):
//   (u-nil) nil BaseURL pointer — no-op, accepted.
//   (u-bad) Non-nil private URL, AllowPrivateBaseURL=false — rejected 400.
//   (u-good) Non-nil public URL — accepted.

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/indexing/pathutil"
)

// ssrfServerCfg returns a *config.Config with AllowPrivateBaseURL set
// to the requested value. All other fields come from config.Defaults().
func ssrfServerCfg(allowPrivate bool) *config.Config {
	cfg := config.Defaults()
	cfg.LLM.AllowPrivateBaseURL = allowPrivate
	return cfg
}

// newSSRFServer builds a minimal Server whose profile handlers are
// driven by the given cfg. The fake profile store always succeeds so
// only the validator gate is observable.
func newSSRFServer(t *testing.T, cfg *config.Config) *Server {
	t.Helper()
	fake := newFakeStoreAdapter()
	s := &Server{
		cfg:             cfg,
		llmProfileStore: fake,
		llmConfigStore:  &nullConfigStore{},
	}
	r := chi.NewRouter()
	r.Post("/api/v1/admin/llm-profiles", s.handleCreateLLMProfile)
	r.Put("/api/v1/admin/llm-profiles/{id}", s.handleUpdateLLMProfile)
	s.router = r
	return s
}

// postCreate sends POST /api/v1/admin/llm-profiles with the given base_url.
func postCreate(t *testing.T, s *Server, baseURL string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{
		"name":     "test-profile",
		"provider": "openai",
		"base_url": baseURL,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/llm-profiles",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// putUpdate sends PUT /api/v1/admin/llm-profiles/{id} with base_url set
// when non-nil, omitted when nil.
func putUpdate(t *testing.T, s *Server, bareID string, baseURL *string) *httptest.ResponseRecorder {
	t.Helper()
	payload := map[string]interface{}{}
	if baseURL != nil {
		payload["base_url"] = *baseURL
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/llm-profiles/"+bareID,
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// seedSSRFProfile adds a profile directly to the fake store so update
// tests have a target to hit.
func seedSSRFProfile(s *Server, name string) {
	fake := s.llmProfileStore.(*fakeProfileStoreAdapter)
	id := "ca_llm_profile:" + name
	fake.profiles[id] = ProfileResponse{ID: id, Name: name, Provider: "openai"}
}

// assertNoURLReflected verifies that none of the raw strings appear in
// the response body (XAN-L2 + TES-M2: raw input must not be reflected).
func assertNoURLReflected(t *testing.T, body string, fragments ...string) {
	t.Helper()
	for _, frag := range fragments {
		if strings.Contains(body, frag) {
			t.Errorf("response body reflects raw URL fragment %q; body=%s", frag, body)
		}
	}
}

// assert400WithErrorKey checks status 400 + JSON body with "error" key.
func assert400WithErrorKey(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d; body=%s", w.Code, w.Body.String())
		return
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not valid JSON: %v; raw=%s", err, w.Body.String())
	}
	if body["error"] == "" {
		t.Errorf("want non-empty 'error' key in JSON body; got %v", body)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Create handler: scheme / IP / flag matrix
// ─────────────────────────────────────────────────────────────────────────────

// (a) Public URL accepted with AllowPrivateBaseURL=true (default).
func TestLLMProfileSSRF_Create_PublicURL_Accepted(t *testing.T) {
	s := newSSRFServer(t, ssrfServerCfg(true))
	w := postCreate(t, s, "https://api.openai.com")
	if w.Code != http.StatusCreated {
		t.Errorf("want 201, got %d; body=%s", w.Code, w.Body.String())
	}
}

// (a2) Public bare IP accepted with AllowPrivateBaseURL=false.
// Uses 93.184.216.34 (IANA example.com) — clearly public, no DNS needed.
func TestLLMProfileSSRF_Create_PublicIP_AcceptedWhenFlagOff(t *testing.T) {
	s := newSSRFServer(t, ssrfServerCfg(false))
	w := postCreate(t, s, "https://93.184.216.34")
	if w.Code != http.StatusCreated {
		t.Errorf("want 201, got %d; body=%s", w.Code, w.Body.String())
	}
}

// (b) RFC1918 IP rejected when AllowPrivateBaseURL=false.
func TestLLMProfileSSRF_Create_PrivateIP_RejectedWhenFlagOff(t *testing.T) {
	s := newSSRFServer(t, ssrfServerCfg(false))
	w := postCreate(t, s, "http://192.168.1.100:11434")
	assert400WithErrorKey(t, w)
	assertNoURLReflected(t, w.Body.String(), "192.168.1.100")
}

// (c) Same RFC1918 IP accepted when AllowPrivateBaseURL=true (default Ollama workflow).
func TestLLMProfileSSRF_Create_PrivateIP_AcceptedWhenFlagOn(t *testing.T) {
	s := newSSRFServer(t, ssrfServerCfg(true))
	w := postCreate(t, s, "http://192.168.1.100:11434")
	if w.Code != http.StatusCreated {
		t.Errorf("want 201, got %d; body=%s", w.Code, w.Body.String())
	}
}

// (d) Cloud-metadata 169.254.169.254 rejected when AllowPrivateBaseURL=false.
func TestLLMProfileSSRF_Create_CloudMetadata_RejectedWhenFlagOff(t *testing.T) {
	s := newSSRFServer(t, ssrfServerCfg(false))
	w := postCreate(t, s, "http://169.254.169.254/latest/meta-data/")
	assert400WithErrorKey(t, w)
	assertNoURLReflected(t, w.Body.String(), "169.254.169.254")
}

// (d2) Cloud-metadata URL ACCEPTED when AllowPrivateBaseURL=true (default).
// Pins the documented residual from Decision 1 — default flip to false
// is planned for 1.0. When this test changes from 201 to 400 the default
// has been flipped and Decision 1 has been revisited.
func TestLLMProfileSSRF_Create_CloudMetadata_AcceptedWhenFlagOn(t *testing.T) {
	s := newSSRFServer(t, ssrfServerCfg(true))
	w := postCreate(t, s, "http://169.254.169.254/latest/meta-data/")
	if w.Code != http.StatusCreated {
		t.Errorf("want 201 (documented residual: AllowPrivateBaseURL=true default), got %d; body=%s",
			w.Code, w.Body.String())
	}
}

// (e) Disallowed scheme rejected regardless of AllowPrivateBaseURL.
// Scheme allowlist is unconditional in ValidateLLMBaseURL.
func TestLLMProfileSSRF_Create_DisallowedScheme_Rejected(t *testing.T) {
	for _, allow := range []bool{true, false} {
		s := newSSRFServer(t, ssrfServerCfg(allow))
		w := postCreate(t, s, "ftp://example.com")
		if w.Code != http.StatusBadRequest {
			t.Errorf("AllowPrivate=%v: want 400, got %d; body=%s", allow, w.Code, w.Body.String())
		}
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("AllowPrivate=%v: body not valid JSON: %v; raw=%s", allow, err, w.Body.String())
		}
		if body["error"] == "" {
			t.Errorf("AllowPrivate=%v: want non-empty 'error' key; got %v", allow, body)
		}
		assertNoURLReflected(t, w.Body.String(), "ftp://example.com", "example.com")
	}
}

// Empty base_url is accepted regardless of flag (means "use provider default").
func TestLLMProfileSSRF_Create_EmptyBaseURL_Accepted(t *testing.T) {
	for _, allow := range []bool{true, false} {
		s := newSSRFServer(t, ssrfServerCfg(allow))
		w := postCreate(t, s, "")
		if w.Code != http.StatusCreated {
			t.Errorf("AllowPrivate=%v: want 201, got %d; body=%s", allow, w.Code, w.Body.String())
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Update handler mirror tests
// ─────────────────────────────────────────────────────────────────────────────

// (u-nil) nil BaseURL pointer — validator is not called, update succeeds.
func TestLLMProfileSSRF_Update_NilBaseURL_NoOp(t *testing.T) {
	s := newSSRFServer(t, ssrfServerCfg(false))
	seedSSRFProfile(s, "p1")
	w := putUpdate(t, s, "p1", nil)
	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d; body=%s", w.Code, w.Body.String())
	}
}

// (u-bad) Non-nil private URL rejected when AllowPrivateBaseURL=false.
func TestLLMProfileSSRF_Update_PrivateURL_RejectedWhenFlagOff(t *testing.T) {
	s := newSSRFServer(t, ssrfServerCfg(false))
	seedSSRFProfile(s, "p2")
	bad := "http://10.0.0.1:11434"
	w := putUpdate(t, s, "p2", &bad)
	assert400WithErrorKey(t, w)
	assertNoURLReflected(t, w.Body.String(), "10.0.0.1")
}

// (u-good) Non-nil public URL accepted.
func TestLLMProfileSSRF_Update_PublicURL_Accepted(t *testing.T) {
	s := newSSRFServer(t, ssrfServerCfg(false))
	seedSSRFProfile(s, "p3")
	good := "https://93.184.216.34"
	w := putUpdate(t, s, "p3", &good)
	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d; body=%s", w.Code, w.Body.String())
	}
}

// compile-time check: pathutil.LookupIPFunc is the expected type.
var _ pathutil.LookupIPFunc = func(_ string) ([]net.IP, error) { return nil, nil }
