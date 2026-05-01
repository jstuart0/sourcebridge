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

	"github.com/sourcebridge/sourcebridge/internal/db"
)

// fakeKnowledgeSettingsStore is a thread-safe in-memory impl of
// rest.KnowledgeSettingsStore for testing the REST handlers.
type fakeKnowledgeSettingsStore struct {
	mu        sync.Mutex
	timeout   time.Duration
	notFound  bool
	getErr    error
	putErr    error
	putCalls  int
	lastValue int
	lastBy    string
}

func (f *fakeKnowledgeSettingsStore) Get(ctx context.Context) (time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return 0, f.getErr
	}
	if f.notFound {
		return 0, db.ErrKnowledgeSettingsNotFound
	}
	return f.timeout, nil
}

func (f *fakeKnowledgeSettingsStore) Put(ctx context.Context, secs int, updatedBy string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return f.putErr
	}
	f.putCalls++
	f.lastValue = secs
	f.lastBy = updatedBy
	f.timeout = time.Duration(secs) * time.Second
	f.notFound = false
	return nil
}

func newServerWithKnowledgeSettings(store KnowledgeSettingsStore) *Server {
	return &Server{
		knowledgeSettingsStore: store,
	}
}

func TestHandleGetKnowledgeTimeout_NoStore(t *testing.T) {
	s := newServerWithKnowledgeSettings(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/knowledge/timeout", nil)
	w := httptest.NewRecorder()
	s.handleGetKnowledgeTimeout(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", w.Code)
	}
}

func TestHandleGetKnowledgeTimeout_NotFoundReturnsDefault(t *testing.T) {
	store := &fakeKnowledgeSettingsStore{notFound: true}
	s := newServerWithKnowledgeSettings(store)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/knowledge/timeout", nil)
	w := httptest.NewRecorder()
	s.handleGetKnowledgeTimeout(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	var resp knowledgeTimeoutResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Seconds != db.KnowledgeTimeoutDefaultSecs {
		t.Errorf("got %d, want default %d", resp.Seconds, db.KnowledgeTimeoutDefaultSecs)
	}
	if resp.Notice == "" {
		t.Error("expected notice on not-found path")
	}
}

func TestHandleGetKnowledgeTimeout_StoreOutage(t *testing.T) {
	store := &fakeKnowledgeSettingsStore{getErr: errors.New("transient surreal outage")}
	s := newServerWithKnowledgeSettings(store)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/knowledge/timeout", nil)
	w := httptest.NewRecorder()
	s.handleGetKnowledgeTimeout(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", w.Code)
	}
}

func TestHandleGetKnowledgeTimeout_HappyPath(t *testing.T) {
	store := &fakeKnowledgeSettingsStore{timeout: 7200 * time.Second}
	s := newServerWithKnowledgeSettings(store)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/knowledge/timeout", nil)
	w := httptest.NewRecorder()
	s.handleGetKnowledgeTimeout(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	var resp knowledgeTimeoutResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Seconds != 7200 {
		t.Errorf("got %d, want 7200", resp.Seconds)
	}
	if resp.Notice != "" {
		t.Error("notice should be empty on happy path")
	}
}

func TestHandlePutKnowledgeTimeout_NoStore(t *testing.T) {
	s := newServerWithKnowledgeSettings(nil)
	body := bytes.NewBufferString(`{"seconds":7200}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/knowledge/timeout", body)
	w := httptest.NewRecorder()
	s.handlePutKnowledgeTimeout(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", w.Code)
	}
}

func TestHandlePutKnowledgeTimeout_BelowMin_400(t *testing.T) {
	store := &fakeKnowledgeSettingsStore{}
	s := newServerWithKnowledgeSettings(store)
	body := bytes.NewBufferString(`{"seconds":1799}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/knowledge/timeout", body)
	w := httptest.NewRecorder()
	s.handlePutKnowledgeTimeout(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "OUT_OF_RANGE") {
		t.Errorf("expected OUT_OF_RANGE code, got %q", w.Body.String())
	}
	if store.putCalls != 0 {
		t.Errorf("Put should not have been called, got %d calls", store.putCalls)
	}
}

func TestHandlePutKnowledgeTimeout_AboveMax_400(t *testing.T) {
	store := &fakeKnowledgeSettingsStore{}
	s := newServerWithKnowledgeSettings(store)
	body := bytes.NewBufferString(`{"seconds":86401}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/knowledge/timeout", body)
	w := httptest.NewRecorder()
	s.handlePutKnowledgeTimeout(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", w.Code)
	}
	if store.putCalls != 0 {
		t.Errorf("Put should not have been called, got %d calls", store.putCalls)
	}
}

func TestHandlePutKnowledgeTimeout_AtBoundaries(t *testing.T) {
	for _, secs := range []int{1800, 86400} {
		store := &fakeKnowledgeSettingsStore{}
		s := newServerWithKnowledgeSettings(store)
		body := bytes.NewBufferString(`{"seconds":` + itoa(secs) + `}`)
		req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/knowledge/timeout", body)
		w := httptest.NewRecorder()
		s.handlePutKnowledgeTimeout(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("seconds=%d: got %d, want 200", secs, w.Code)
		}
		if store.lastValue != secs {
			t.Errorf("seconds=%d: store got %d", secs, store.lastValue)
		}
	}
}

func TestHandlePutKnowledgeTimeout_InvalidJSON_400(t *testing.T) {
	store := &fakeKnowledgeSettingsStore{}
	s := newServerWithKnowledgeSettings(store)
	body := bytes.NewBufferString(`not json`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/knowledge/timeout", body)
	w := httptest.NewRecorder()
	s.handlePutKnowledgeTimeout(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", w.Code)
	}
}

func TestHandlePutKnowledgeTimeout_HappyPath(t *testing.T) {
	store := &fakeKnowledgeSettingsStore{}
	s := newServerWithKnowledgeSettings(store)
	body := bytes.NewBufferString(`{"seconds":7200}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/knowledge/timeout", body)
	w := httptest.NewRecorder()
	s.handlePutKnowledgeTimeout(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if store.putCalls != 1 {
		t.Errorf("Put calls: got %d, want 1", store.putCalls)
	}
	if store.lastValue != 7200 {
		t.Errorf("stored value: got %d, want 7200", store.lastValue)
	}
}

func TestHandlePutKnowledgeTimeout_StoreError_500(t *testing.T) {
	store := &fakeKnowledgeSettingsStore{putErr: errors.New("write failed")}
	s := newServerWithKnowledgeSettings(store)
	body := bytes.NewBufferString(`{"seconds":7200}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/knowledge/timeout", body)
	w := httptest.NewRecorder()
	s.handlePutKnowledgeTimeout(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("got %d, want 500", w.Code)
	}
}

func TestHandlePutKnowledgeTimeout_StoreOutOfRangeError_400(t *testing.T) {
	// If the validation in the handler somehow lets a value through
	// and the store catches it, the response should still be 400.
	// The handler maps ErrKnowledgeTimeoutOutOfRange -> 400 with the
	// same OUT_OF_RANGE code.
	store := &fakeKnowledgeSettingsStore{putErr: db.ErrKnowledgeTimeoutOutOfRange}
	s := newServerWithKnowledgeSettings(store)
	// Use a value the handler will accept, so the store's "out of
	// range" path is what produces the rejection.
	body := bytes.NewBufferString(`{"seconds":7200}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/knowledge/timeout", body)
	w := httptest.NewRecorder()
	s.handlePutKnowledgeTimeout(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", w.Code)
	}
}

// itoa avoids strconv import in tests above; same shape.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	buf := [20]byte{}
	idx := len(buf)
	for i > 0 {
		idx--
		buf[idx] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		idx--
		buf[idx] = '-'
	}
	return string(buf[idx:])
}
