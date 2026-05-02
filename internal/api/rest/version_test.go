// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/version"
)

// minimalServer constructs a Server with just the fields handleVersion
// reads. Skips NewServer to avoid pulling the entire dependency graph
// (auth, JWT, store, worker client, etc.) into a unit test of one
// public read-only endpoint.
func minimalServerForVersionTest(edition string, lookup *versionLookup) *Server {
	return &Server{
		cfg:                 &config.Config{Edition: edition},
		workerVersionLookup: lookup,
	}
}

func TestHandleVersion_NilLookup_ReturnsEmptyWorker(t *testing.T) {
	s := minimalServerForVersionTest("oss", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	rec := httptest.NewRecorder()

	s.handleVersion(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]string{
		"version":       version.Version,
		"commit":        version.Commit,
		"buildDate":     version.BuildDate,
		"goVersion":     runtime.Version(),
		"edition":       "oss",
		"buildEdition":  version.Edition,
		"workerVersion": "",
	}
	for k, v := range want {
		got, _ := body[k].(string)
		if got != v {
			t.Errorf("field %q: got %q, want %q", k, got, v)
		}
	}
}

func TestHandleVersion_HealthyWorker_PopulatesWorkerVersion(t *testing.T) {
	probe := func(ctx context.Context) (string, error) {
		return "v0.9.0-test+gabc1234", nil
	}
	lookup := newVersionLookup(30*time.Second, probe)
	s := minimalServerForVersionTest("oss", lookup)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	rec := httptest.NewRecorder()
	s.handleVersion(rec, req)

	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if got, _ := body["workerVersion"].(string); got != "v0.9.0-test+gabc1234" {
		t.Errorf("workerVersion: got %q, want %q", got, "v0.9.0-test+gabc1234")
	}
}

func TestHandleVersion_UnreachableWorker_ReturnsEmptyWorker(t *testing.T) {
	probe := func(ctx context.Context) (string, error) {
		return "", errors.New("connection refused")
	}
	lookup := newVersionLookup(30*time.Second, probe)
	s := minimalServerForVersionTest("oss", lookup)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	rec := httptest.NewRecorder()
	s.handleVersion(rec, req)

	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if got, _ := body["workerVersion"].(string); got != "" {
		t.Errorf("workerVersion on unreachable worker: got %q, want empty", got)
	}
}

func TestHandleVersion_SlowWorker_TimesOutToEmpty(t *testing.T) {
	probe := func(ctx context.Context) (string, error) {
		// Block until the context's 250ms timeout elapses.
		<-ctx.Done()
		return "", ctx.Err()
	}
	lookup := newVersionLookup(30*time.Second, probe)
	s := minimalServerForVersionTest("oss", lookup)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	rec := httptest.NewRecorder()

	start := time.Now()
	s.handleVersion(rec, req)
	elapsed := time.Since(start)

	// Allow some slack for CI scheduler jitter, but we must have
	// returned within ~500ms — much less than e.g. a default gRPC
	// dial timeout. The 250ms cap is the contract.
	if elapsed > 500*time.Millisecond {
		t.Errorf("handleVersion took %v with slow worker; expected ≤500ms (probe cap is 250ms)", elapsed)
	}

	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if got, _ := body["workerVersion"].(string); got != "" {
		t.Errorf("workerVersion on slow worker: got %q, want empty", got)
	}
}

func TestHandleVersion_EmptyEditionFallsBackToUnknown(t *testing.T) {
	s := minimalServerForVersionTest("", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	rec := httptest.NewRecorder()
	s.handleVersion(rec, req)

	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if got, _ := body["edition"].(string); got != "unknown" {
		t.Errorf("edition: got %q, want %q", got, "unknown")
	}
}

func TestVersionLookup_CacheAvoidsRepeatedProbes(t *testing.T) {
	var probeCount int32
	probe := func(ctx context.Context) (string, error) {
		atomic.AddInt32(&probeCount, 1)
		return "v0.0.0-cached", nil
	}
	lookup := newVersionLookup(30*time.Second, probe)

	for i := 0; i < 100; i++ {
		_ = lookup.get(context.Background())
	}

	if got := atomic.LoadInt32(&probeCount); got != 1 {
		t.Errorf("probe was called %d times across 100 cached reads; want 1", got)
	}
}

func TestVersionLookup_ConcurrentReadsAreRaceFree(t *testing.T) {
	// Asserts no race under -race when many goroutines read concurrently.
	probe := func(ctx context.Context) (string, error) {
		return "v0.0.0-conc", nil
	}
	lookup := newVersionLookup(time.Millisecond, probe)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = lookup.get(context.Background())
			}
		}()
	}
	wg.Wait()
}
