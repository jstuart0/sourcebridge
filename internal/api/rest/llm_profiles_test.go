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
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/sourcebridge/sourcebridge/internal/config"
)

// minimalConfig returns the bare-minimum *config.Config needed by the
// rest handlers under test (effectiveLLMConfig touches s.cfg.LLM and
// s.cfg.Edition). config.Defaults() pulls in everything else.
func minimalConfig() *config.Config {
	return config.Defaults()
}

// fakeProfileStoreAdapter is an in-memory implementation of
// LLMProfileStoreAdapter. Sufficient to exercise every handler path
// (status codes, error mapping, JSON shapes) without standing up a
// SurrealDB.
type fakeProfileStoreAdapter struct {
	mu                   sync.Mutex
	profiles             map[string]ProfileResponse
	activeID             string
	activeProfileMissing bool
	listErr              error
	getErr               error
	createErr            error
	updateErr            error
	deleteErr            error
	activateErr          error
	createdLast          ProfileCreateRequest
	updatedLast          ProfileUpdateRequest
	deletedLast          string
	activatedLast        string
	activatedBy          string
}

func newFakeStoreAdapter() *fakeProfileStoreAdapter {
	return &fakeProfileStoreAdapter{
		profiles: map[string]ProfileResponse{},
	}
}

func (f *fakeProfileStoreAdapter) ListProfiles(_ context.Context) ([]ProfileResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]ProfileResponse, 0, len(f.profiles))
	for _, p := range f.profiles {
		p.IsActive = p.ID == f.activeID
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeProfileStoreAdapter) GetProfile(_ context.Context, id string) (*ProfileResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	p, ok := f.profiles[id]
	if !ok {
		return nil, ErrProfileNotFound
	}
	p.IsActive = p.ID == f.activeID
	return &p, nil
}

func (f *fakeProfileStoreAdapter) CreateProfile(_ context.Context, req ProfileCreateRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return "", f.createErr
	}
	id := "ca_llm_profile:" + req.Name
	f.profiles[id] = ProfileResponse{
		ID:         id,
		Name:       req.Name,
		Provider:   req.Provider,
		APIKeySet:  req.APIKey != "",
		APIKeyHint: MaskAPIKeyHint(req.APIKey),
	}
	f.createdLast = req
	return id, nil
}

func (f *fakeProfileStoreAdapter) UpdateProfile(_ context.Context, id string, req ProfileUpdateRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return f.updateErr
	}
	if _, ok := f.profiles[id]; !ok {
		return ErrProfileNotFound
	}
	f.updatedLast = req
	return nil
}

func (f *fakeProfileStoreAdapter) DeleteProfile(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.profiles[id]; !ok {
		return ErrProfileNotFound
	}
	if id == f.activeID {
		return ErrProfileActiveDeleteForbidden
	}
	delete(f.profiles, id)
	f.deletedLast = id
	return nil
}

func (f *fakeProfileStoreAdapter) ActivateProfile(_ context.Context, id, by string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.activateErr != nil {
		return f.activateErr
	}
	if _, ok := f.profiles[id]; !ok {
		return ErrProfileNotFound
	}
	f.activeID = id
	f.activatedLast = id
	f.activatedBy = by
	return nil
}

func (f *fakeProfileStoreAdapter) ActiveProfileMissing() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activeProfileMissing
}

func (f *fakeProfileStoreAdapter) ActiveProfileID(_ context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activeID, nil
}

// newServerWithProfileStore constructs a minimal Server with just
// the profile-store wired and the new routes mounted. Used by every
// handler test below.
//
// We build the chi router manually rather than going through NewServer
// because the full constructor pulls in graph store, orchestrator,
// auth, etc. — none of which the profile handlers touch.
func newServerWithProfileStore(t *testing.T, fake *fakeProfileStoreAdapter) *Server {
	t.Helper()
	cfg := minimalConfig()
	s := &Server{
		cfg:             cfg,
		llmProfileStore: fake,
		llmConfigStore:  &nullConfigStore{}, // legacy GET handler reads through this
	}
	r := chi.NewRouter()
	r.Get("/api/v1/admin/llm-profiles", s.handleListLLMProfiles)
	r.Post("/api/v1/admin/llm-profiles", s.handleCreateLLMProfile)
	r.Get("/api/v1/admin/llm-profiles/{id}", s.handleGetLLMProfile)
	r.Put("/api/v1/admin/llm-profiles/{id}", s.handleUpdateLLMProfile)
	r.Delete("/api/v1/admin/llm-profiles/{id}", s.handleDeleteLLMProfile)
	r.Post("/api/v1/admin/llm-profiles/{id}/activate", s.handleActivateLLMProfile)
	r.Get("/api/v1/admin/llm-config", s.handleGetLLMConfig)
	s.router = r
	return s
}

// nullConfigStore is a no-op LLMConfigStore used by the legacy
// /admin/llm-config handler in tests that don't care about the legacy
// path's payload — only the new active_profile_* fields the handler
// surfaces alongside.
type nullConfigStore struct{}

func (nullConfigStore) LoadLLMConfig() (*LLMConfigRecord, error)   { return nil, nil }
func (nullConfigStore) SaveLLMConfig(_ *LLMConfigRecord) error     { return nil }

// ─────────────────────────────────────────────────────────────────────────
// List
// ─────────────────────────────────────────────────────────────────────────

func TestHandler_ListProfiles_OK(t *testing.T) {
	fake := newFakeStoreAdapter()
	fake.profiles["ca_llm_profile:a"] = ProfileResponse{ID: "ca_llm_profile:a", Name: "A"}
	fake.profiles["ca_llm_profile:b"] = ProfileResponse{ID: "ca_llm_profile:b", Name: "B"}
	fake.activeID = "ca_llm_profile:a"
	s := newServerWithProfileStore(t, fake)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm-profiles", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	profiles, ok := body["profiles"].([]interface{})
	if !ok {
		t.Fatalf("missing 'profiles' array; body=%v", body)
	}
	if len(profiles) != 2 {
		t.Errorf("profiles count: got %d, want 2", len(profiles))
	}
	if missing, _ := body["active_profile_missing"].(bool); missing {
		t.Errorf("active_profile_missing: got true, want false")
	}
}

func TestHandler_ListProfiles_BannerOnMissingActive(t *testing.T) {
	// codex-H3: when the resolver latches active-profile-missing, the
	// list response surfaces it for the admin UI to render the repair
	// banner.
	fake := newFakeStoreAdapter()
	fake.activeProfileMissing = true
	s := newServerWithProfileStore(t, fake)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm-profiles", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d", w.Code)
	}
	var body map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	missing, _ := body["active_profile_missing"].(bool)
	if !missing {
		t.Errorf("active_profile_missing: got false, want true")
	}
}

func TestHandler_ListProfiles_503WithoutStore(t *testing.T) {
	// nil profile-store = 503 SERVICE_UNAVAILABLE.
	s := &Server{}
	r := chi.NewRouter()
	r.Get("/api/v1/admin/llm-profiles", s.handleListLLMProfiles)
	s.router = r
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm-profiles", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Get
// ─────────────────────────────────────────────────────────────────────────

func TestHandler_GetProfile_OK(t *testing.T) {
	fake := newFakeStoreAdapter()
	fake.profiles["ca_llm_profile:abc"] = ProfileResponse{ID: "ca_llm_profile:abc", Name: "Hello"}
	s := newServerWithProfileStore(t, fake)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm-profiles/abc", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, body=%s", w.Code, w.Body.String())
	}
	var p ProfileResponse
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if p.Name != "Hello" {
		t.Errorf("name: got %q, want Hello", p.Name)
	}
}

func TestHandler_GetProfile_404(t *testing.T) {
	fake := newFakeStoreAdapter()
	s := newServerWithProfileStore(t, fake)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm-profiles/missing", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Create
// ─────────────────────────────────────────────────────────────────────────

func TestHandler_CreateProfile_201(t *testing.T) {
	fake := newFakeStoreAdapter()
	s := newServerWithProfileStore(t, fake)
	body := `{"name":"Hello","provider":"anthropic","api_key":"sk-test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/llm-profiles", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Errorf("status: got %d, want 201, body=%s", w.Code, w.Body.String())
	}
	if fake.createdLast.Name != "Hello" || fake.createdLast.APIKey != "sk-test" {
		t.Errorf("created request not propagated: %+v", fake.createdLast)
	}
}

func TestHandler_CreateProfile_409DuplicateName(t *testing.T) {
	fake := newFakeStoreAdapter()
	fake.createErr = ErrDuplicateProfileName
	s := newServerWithProfileStore(t, fake)
	body := `{"name":"dup","provider":"anthropic"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/llm-profiles", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("status: got %d, want 409", w.Code)
	}
}

func TestHandler_CreateProfile_422EncryptionRequired(t *testing.T) {
	fake := newFakeStoreAdapter()
	fake.createErr = ErrLLMEncryptionKeyRequired
	s := newServerWithProfileStore(t, fake)
	body := `{"name":"x","provider":"anthropic","api_key":"k"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/llm-profiles", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status: got %d, want 422", w.Code)
	}
}

func TestHandler_CreateProfile_400InvalidProvider(t *testing.T) {
	fake := newFakeStoreAdapter()
	s := newServerWithProfileStore(t, fake)
	body := `{"name":"x","provider":"unknown-provider"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/llm-profiles", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Update
// ─────────────────────────────────────────────────────────────────────────

func TestHandler_UpdateProfile_OK(t *testing.T) {
	fake := newFakeStoreAdapter()
	fake.profiles["ca_llm_profile:abc"] = ProfileResponse{ID: "ca_llm_profile:abc", Name: "X"}
	s := newServerWithProfileStore(t, fake)
	body := `{"provider":"openai","api_key":"sk-new"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/llm-profiles/abc", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, body=%s", w.Code, w.Body.String())
	}
	if fake.updatedLast.Provider == nil || *fake.updatedLast.Provider != "openai" {
		t.Errorf("provider not propagated: %+v", fake.updatedLast)
	}
	if fake.updatedLast.APIKey == nil || *fake.updatedLast.APIKey != "sk-new" {
		t.Errorf("api_key not propagated: %+v", fake.updatedLast)
	}
}

func TestHandler_UpdateProfile_404(t *testing.T) {
	fake := newFakeStoreAdapter()
	s := newServerWithProfileStore(t, fake)
	body := `{"provider":"openai"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/llm-profiles/missing", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

func TestHandler_UpdateProfile_409TargetNoLongerActive(t *testing.T) {
	fake := newFakeStoreAdapter()
	fake.profiles["ca_llm_profile:abc"] = ProfileResponse{ID: "ca_llm_profile:abc", Name: "X"}
	fake.updateErr = ErrProfileTargetNoLongerActive
	s := newServerWithProfileStore(t, fake)
	body := `{"provider":"openai"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/llm-profiles/abc", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("status: got %d, want 409", w.Code)
	}
	var body2 map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body2)
	if body2["error"] != "target_no_longer_active" {
		t.Errorf("error code: got %q, want target_no_longer_active", body2["error"])
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Delete
// ─────────────────────────────────────────────────────────────────────────

func TestHandler_DeleteProfile_204(t *testing.T) {
	fake := newFakeStoreAdapter()
	fake.profiles["ca_llm_profile:abc"] = ProfileResponse{ID: "ca_llm_profile:abc", Name: "X"}
	s := newServerWithProfileStore(t, fake)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/llm-profiles/abc", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("status: got %d, want 204, body=%s", w.Code, w.Body.String())
	}
	if fake.deletedLast != "ca_llm_profile:abc" {
		t.Errorf("delete: target not propagated, got %q", fake.deletedLast)
	}
}

func TestHandler_DeleteProfile_404(t *testing.T) {
	fake := newFakeStoreAdapter()
	s := newServerWithProfileStore(t, fake)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/llm-profiles/missing", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

func TestHandler_DeleteProfile_409Active(t *testing.T) {
	// D5: cannot delete active.
	fake := newFakeStoreAdapter()
	fake.profiles["ca_llm_profile:abc"] = ProfileResponse{ID: "ca_llm_profile:abc", Name: "X"}
	fake.activeID = "ca_llm_profile:abc"
	s := newServerWithProfileStore(t, fake)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/llm-profiles/abc", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("status: got %d, want 409", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Activate
// ─────────────────────────────────────────────────────────────────────────

func TestHandler_ActivateProfile_204(t *testing.T) {
	fake := newFakeStoreAdapter()
	fake.profiles["ca_llm_profile:abc"] = ProfileResponse{ID: "ca_llm_profile:abc", Name: "X"}
	s := newServerWithProfileStore(t, fake)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/llm-profiles/abc/activate", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("status: got %d, want 204, body=%s", w.Code, w.Body.String())
	}
	if fake.activatedLast != "ca_llm_profile:abc" {
		t.Errorf("activate: id not propagated, got %q", fake.activatedLast)
	}
	if fake.activatedBy == "" {
		t.Errorf("activate: by-actor empty")
	}
}

func TestHandler_ActivateProfile_404(t *testing.T) {
	fake := newFakeStoreAdapter()
	s := newServerWithProfileStore(t, fake)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/llm-profiles/missing/activate", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// canonicalProfileID
// ─────────────────────────────────────────────────────────────────────────

func TestCanonicalProfileID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"abc", "ca_llm_profile:abc"},
		{"ca_llm_profile:abc", "ca_llm_profile:abc"},
		{"", ""},
		{"default-migrated", "ca_llm_profile:default-migrated"},
	}
	for _, c := range cases {
		got := canonicalProfileID(c.in)
		if got != c.want {
			t.Errorf("canonicalProfileID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMapProfileError(t *testing.T) {
	cases := []struct {
		err        error
		wantStatus int
	}{
		{ErrProfileNotFound, http.StatusNotFound},
		{ErrDuplicateProfileName, http.StatusConflict},
		{ErrProfileNameRequired, http.StatusUnprocessableEntity},
		{ErrProfileNameTooLong, http.StatusUnprocessableEntity},
		{ErrProfileActiveDeleteForbidden, http.StatusConflict},
		{ErrProfileTargetNoLongerActive, http.StatusConflict},
		{ErrProfileVersionConflict, http.StatusConflict},
		{ErrLLMEncryptionKeyRequired, http.StatusUnprocessableEntity},
		{errors.New("random"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		mapProfileError(w, c.err, "test")
		if w.Code != c.wantStatus {
			t.Errorf("err %v: got status %d, want %d", c.err, w.Code, c.wantStatus)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Legacy GET /admin/llm-config carries active_profile_id (ian-M1)
// ─────────────────────────────────────────────────────────────────────────

func TestHandler_LegacyGetLLMConfigSurfacesActiveProfile(t *testing.T) {
	// ian-M1: legacy GET response is back-compat-extended with
	// active_profile_id, active_profile_name, active_profile_missing.
	fake := newFakeStoreAdapter()
	fake.profiles["ca_llm_profile:abc"] = ProfileResponse{
		ID:       "ca_llm_profile:abc",
		Name:     "Production",
		Provider: "openai",
	}
	fake.activeID = "ca_llm_profile:abc"
	s := newServerWithProfileStore(t, fake)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm-config", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", w.Code, w.Body.String())
	}
	var body llmConfigResponse
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.ActiveProfileID != "ca_llm_profile:abc" {
		t.Errorf("active_profile_id: got %q, want ca_llm_profile:abc", body.ActiveProfileID)
	}
	if body.ActiveProfileName != "Production" {
		t.Errorf("active_profile_name: got %q, want Production", body.ActiveProfileName)
	}
	if body.ActiveProfileMissing {
		t.Errorf("active_profile_missing: got true, want false")
	}
}

func TestHandler_LegacyGetLLMConfigBannerOnMissingActive(t *testing.T) {
	fake := newFakeStoreAdapter()
	fake.activeProfileMissing = true
	s := newServerWithProfileStore(t, fake)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm-config", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	var body llmConfigResponse
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if !body.ActiveProfileMissing {
		t.Errorf("active_profile_missing: got false, want true")
	}
}
