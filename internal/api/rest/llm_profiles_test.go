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

	"github.com/go-chi/chi/v5"

	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/llm"
)

// Local aliases used by the slice-4 active-job-count tests so the
// new test cases don't have to rename existing references throughout
// the file. JobStatus and Subsystem are kept alongside Job for
// completeness even though only Job is currently referenced — keeping
// the alias set together makes adding the next test case trivial.
//
//nolint:unused // Convenience aliases; kept as a set for ergonomics.
type (
	llmgo_Job       = llm.Job
	llmgo_JobStatus = llm.JobStatus
	llmgo_Subsystem = llm.Subsystem
)

// nolint:unused
var (
	llmgo_NewMemStore         = llm.NewMemStore
	llmgo_StatusPending       = llm.StatusPending
	llmgo_StatusGenerating    = llm.StatusGenerating
	llmgo_StatusReady         = llm.StatusReady
	llmgo_StatusFailed        = llm.StatusFailed
	llmgo_SubsystemKnowledge  = llm.SubsystemKnowledge
	llmgo_SubsystemClustering = llm.SubsystemClustering
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
	activeIDErr          error
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
	// canonicalizeOnEmit mirrors the post-fix behavior of the real
	// SurrealDB-backed store (internal/db/llm_profile_store.go's
	// extractRecordIDString → canonicalizeRecordIDString): record-id
	// strings are stripped of the SurrealDB-Go U+27E8/U+27E9 angle
	// brackets on read so they match plain-string columns. Tests that
	// exercise the bracket/no-bracket asymmetry set this true; legacy
	// tests leave it false to preserve their existing behavior.
	canonicalizeOnEmit bool
}

// fakeCanonicalizeRecordIDString mirrors the production canonicalizer in
// internal/db/llm_profile_store.go. Kept local to the rest tests so the
// fake doesn't pull in the db package just to model the post-fix contract.
func fakeCanonicalizeRecordIDString(s string) string {
	idx := strings.IndexByte(s, ':')
	if idx < 0 {
		return s
	}
	table, key := s[:idx], s[idx+1:]
	const open, close = "⟨", "⟩"
	if strings.HasPrefix(key, open) && strings.HasSuffix(key, close) {
		key = strings.TrimSuffix(strings.TrimPrefix(key, open), close)
	}
	return table + ":" + key
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
	activeID := f.activeID
	if f.canonicalizeOnEmit {
		activeID = fakeCanonicalizeRecordIDString(activeID)
	}
	for _, p := range f.profiles {
		if f.canonicalizeOnEmit {
			p.ID = fakeCanonicalizeRecordIDString(p.ID)
		}
		p.IsActive = p.ID == activeID
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
	if f.activeIDErr != nil {
		return "", f.activeIDErr
	}
	if f.canonicalizeOnEmit {
		return fakeCanonicalizeRecordIDString(f.activeID), nil
	}
	return f.activeID, nil
}

// LookupProfileName implements the slice-3 LLMProfileLookup-shaped
// method on the rest adapter. Returns the saved profile's name when
// the id exists, or ("", false, nil) for missing ids — matching the
// real adapter's distinction between "deleted" and "DB outage".
func (f *fakeProfileStoreAdapter) LookupProfileName(_ context.Context, profileID string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if profileID == "" {
		return "", false, nil
	}
	for _, p := range f.profiles {
		if p.ID == profileID {
			return p.Name, true, nil
		}
	}
	return "", false, nil
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
	// Slice 4: in-flight LLM-job count, route ordered before {id}
	// to avoid being shadowed by the chi pattern match (mirrors the
	// router.go ordering exactly).
	r.Get("/api/v1/admin/llm-profiles/active-job-count", s.handleActiveLLMJobCount)
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

func (nullConfigStore) LoadLLMConfig(_ context.Context) (*LLMConfigRecord, error) { return nil, nil }
func (nullConfigStore) SaveLLMConfig(_ context.Context, _ *LLMConfigRecord) error { return nil }

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

func TestHandler_ListProfiles_BannerOnLiveActiveIDMismatch(t *testing.T) {
	// codex-r2b Low: when activeID is non-empty but does NOT appear in
	// the profile list (data corruption between resolves), the list
	// handler MUST compute active_profile_missing=true even when the
	// resolver latch is still false (it has not yet observed the gap
	// because no LLM Resolve has fired since the corruption).
	fake := newFakeStoreAdapter()
	fake.profiles["ca_llm_profile:a"] = ProfileResponse{
		ID:       "ca_llm_profile:a",
		Name:     "A",
		IsActive: false,
	}
	fake.activeID = "ca_llm_profile:gone-after-purge"
	fake.activeProfileMissing = false // resolver latch hasn't been hit yet
	s := newServerWithProfileStore(t, fake)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm-profiles", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	missing, _ := body["active_profile_missing"].(bool)
	if !missing {
		t.Errorf("active_profile_missing: got false, want true (live id check should detect the gap even when the resolver latch is stale)")
	}
}

func TestHandler_ListProfiles_NoBannerWhenActiveIDPresent(t *testing.T) {
	// codex-r2b Low: positive control. When activeID matches a profile
	// in the list, active_profile_missing must be false regardless of
	// the resolver latch.
	fake := newFakeStoreAdapter()
	fake.profiles["ca_llm_profile:a"] = ProfileResponse{
		ID:       "ca_llm_profile:a",
		Name:     "A",
		IsActive: true,
	}
	fake.activeID = "ca_llm_profile:a"
	fake.activeProfileMissing = false
	s := newServerWithProfileStore(t, fake)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm-profiles", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	var body map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	missing, _ := body["active_profile_missing"].(bool)
	if missing {
		t.Errorf("active_profile_missing: got true, want false")
	}
}

func TestHandler_ListProfiles_BannerWhenActiveIDLookupFailsButLatched(t *testing.T) {
	// codex-r2b Low: when ActiveProfileID(ctx) errors (DB outage), the
	// handler falls back to the resolver latch. If the latch is true,
	// the banner must be rendered (don't silently say "not missing"
	// when we can't actually check).
	fake := newFakeStoreAdapter()
	fake.profiles["ca_llm_profile:a"] = ProfileResponse{ID: "ca_llm_profile:a", Name: "A"}
	fake.activeID = ""
	fake.activeIDErr = errors.New("simulated db outage")
	fake.activeProfileMissing = true
	s := newServerWithProfileStore(t, fake)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm-profiles", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	missing, _ := body["active_profile_missing"].(bool)
	if !missing {
		t.Errorf("active_profile_missing: got false, want true (latch fallback when live lookup errors)")
	}
}

// TestHandler_ListProfiles_RecordIDBracketAsymmetry is the regression test for the
// SurrealDB-Go-v1.4.0 record-id stringification asymmetry that broke the deployed
// LLM profile picker (see thoughts/shared/investigations/2026-05-01-llm-profile-picker-broken-deployed.md).
//
// The bug: surrealdb-go's models.RecordID.String() wraps record-id keys containing
// non-alphanumeric characters in U+27E8 / U+27E9 brackets — e.g. the migrated id
// "default-migrated" stringifies as "ca_llm_profile:⟨default-migrated⟩". Meanwhile
// the legacy active_profile_id column is a plain string and persists the unbracketed
// form. The list handler did literal `p.ID == activeID`, so the bracketed listed id
// never matched the unbracketed active id → active_profile_missing=true, is_active=false
// for every profile, and the admin UI locked the editor + showed the repair banner.
//
// The fix canonicalizes record-id strings on read in internal/db/llm_profile_store.go
// (extractRecordIDString → canonicalizeRecordIDString). After the fix, both the listed
// p.ID and the active id come through unbracketed. This test models that contract at
// the handler boundary: the fake adapter is given the bracketed STORED form on the
// listed profile and the unbracketed form on the active id (mirroring the production
// asymmetry), and the fake canonicalizes on emit (mirroring what the real store now
// does post-fix). The handler must then report active_profile_missing=false and the
// listed profile's is_active=true.
//
// Pre-fix this could not be exercised because the original fake did
// `p.IsActive = p.ID == f.activeID` directly — the same bug shape as the handler —
// without any seam to express the bracket/no-bracket asymmetry. That's the test gap
// that allowed the bug through.
func TestHandler_ListProfiles_RecordIDBracketAsymmetry(t *testing.T) {
	fake := newFakeStoreAdapter()
	// Plant the raw (pre-canonicalization) shapes the live thor SurrealDB returns:
	// the listed profile id is what models.RecordID.String() emits (bracketed); the
	// active_profile_id column is a plain string (unbracketed).
	const bracketedListedID = "ca_llm_profile:⟨default-migrated⟩"
	const canonicalID = "ca_llm_profile:default-migrated"
	fake.canonicalizeOnEmit = true
	fake.profiles[canonicalID] = ProfileResponse{
		ID:   bracketedListedID, // stored shape
		Name: "Default",
	}
	fake.activeID = canonicalID // already unbracketed (plain-string column)
	fake.activeProfileMissing = false

	s := newServerWithProfileStore(t, fake)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm-profiles", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if missing, _ := body["active_profile_missing"].(bool); missing {
		t.Errorf("active_profile_missing: got true, want false (canonicalization on read should make the listed id and active id agree)")
	}
	profiles, ok := body["profiles"].([]interface{})
	if !ok || len(profiles) != 1 {
		t.Fatalf("expected exactly one profile in response; got %v", body["profiles"])
	}
	prof, _ := profiles[0].(map[string]interface{})
	if id, _ := prof["id"].(string); id != canonicalID {
		t.Errorf("listed profile id: got %q, want %q (unbracketed canonical form)", id, canonicalID)
	}
	if isActive, _ := prof["is_active"].(bool); !isActive {
		t.Errorf("listed profile is_active: got false, want true (canonicalized listed id should match canonicalized active id)")
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

func TestHandler_DeleteProfile_PercentEncodedID_204(t *testing.T) {
	// Frontend uses encodeURIComponent on the full record id, so the colon
	// arrives as %3A. Chi v5 returns the raw path segment, not a decoded
	// one — canonicalProfileID must URL-decode before splitting.
	fake := newFakeStoreAdapter()
	fake.profiles["ca_llm_profile:g3j30dgw5fhpxav9t4me"] = ProfileResponse{
		ID:   "ca_llm_profile:g3j30dgw5fhpxav9t4me",
		Name: "X",
	}
	s := newServerWithProfileStore(t, fake)
	req := httptest.NewRequest(http.MethodDelete,
		"/api/v1/admin/llm-profiles/ca_llm_profile%3Ag3j30dgw5fhpxav9t4me", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("status: got %d, want 204, body=%s", w.Code, w.Body.String())
	}
	if fake.deletedLast != "ca_llm_profile:g3j30dgw5fhpxav9t4me" {
		t.Errorf("delete: target not propagated, got %q", fake.deletedLast)
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
		{"ca_llm_profile%3Aabc", "ca_llm_profile:abc"},
		{"ca_llm_profile%3Adefault-migrated", "ca_llm_profile:default-migrated"},
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

// ─────────────────────────────────────────────────────────────────────
// Slice 4 polish: in-flight LLM-job count
// ─────────────────────────────────────────────────────────────────────

func TestHandler_ActiveLLMJobCount_NoStoreReturnsZero(t *testing.T) {
	// When the job store is not wired (e.g. embedded mode without
	// SurrealDB), the endpoint must not 500 — it returns 0 so the
	// SwitchProfileDialog renders the no-warning path.
	fake := newFakeStoreAdapter()
	s := newServerWithProfileStore(t, fake)
	// jobStore left nil deliberately.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm-profiles/active-job-count", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var body map[string]int
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["count"] != 0 {
		t.Errorf("count: got %d, want 0", body["count"])
	}
}

func TestHandler_ActiveLLMJobCount_FiltersByLLMProvider(t *testing.T) {
	// LLM-backed = (Status pending|generating) AND (LLMProvider != "").
	// The clustering subsystem reuses the queue but never makes LLM
	// calls, so its jobs have LLMProvider="" and must NOT count.
	fake := newFakeStoreAdapter()
	store := llmgo_NewMemStore()
	for _, j := range []*llmgo_Job{
		// Active + LLM-backed (counts):
		{ID: "j1", Status: llmgo_StatusPending, LLMProvider: "openai", Subsystem: llmgo_SubsystemKnowledge, JobType: "x"},
		{ID: "j2", Status: llmgo_StatusGenerating, LLMProvider: "anthropic", Subsystem: llmgo_SubsystemKnowledge, JobType: "x"},
		// Active + non-LLM (does not count — clustering reuses queue):
		{ID: "j3", Status: llmgo_StatusGenerating, LLMProvider: "", Subsystem: llmgo_SubsystemClustering, JobType: "x"},
		// Terminal + LLM-backed (does not count):
		{ID: "j4", Status: llmgo_StatusReady, LLMProvider: "openai", Subsystem: llmgo_SubsystemKnowledge, JobType: "x"},
		{ID: "j5", Status: llmgo_StatusFailed, LLMProvider: "ollama", Subsystem: llmgo_SubsystemKnowledge, JobType: "x"},
	} {
		_, _ = store.Create(t.Context(), j)
	}
	s := newServerWithProfileStore(t, fake)
	s.jobStore = store

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm-profiles/active-job-count", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var body map[string]int
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["count"] != 2 {
		t.Errorf("count: got %d, want 2 (j1 + j2 are active + LLM-backed; j3 is active but non-LLM; j4/j5 are terminal)", body["count"])
	}
}
