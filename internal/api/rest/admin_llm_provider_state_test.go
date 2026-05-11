// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newOllamaPSStub(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	return httptest.NewServer(mux)
}

func TestProbeOllamaLoadedModels_ParsesAPIPS(t *testing.T) {
	body := `{"models":[
		{"name":"qwen3:4b-instruct-2507-q4_K_M","size_vram":90305331200,"expires_at":"2318-08-21T11:16:42.914921807-04:00"},
		{"name":"qwen3.6:27b-q4_K_M","size_vram":17179869184,"expires_at":"2026-05-11T13:00:00Z"}
	]}`
	srv := newOllamaPSStub(t, body, http.StatusOK)
	defer srv.Close()

	loaded, err := probeOllamaLoadedModels(context.Background(), srv.URL+"/v1")
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 models, got %d", len(loaded))
	}
	if loaded[0].Name != "qwen3:4b-instruct-2507-q4_K_M" {
		t.Errorf("model name mismatch: %q", loaded[0].Name)
	}
	// 90305331200 bytes / 1024 / 1024 = 86121 MB (~86 GB) — pin exact value
	if loaded[0].SizeVRAMMB != 86121 {
		t.Errorf("size_vram_mb conversion wrong: got %d, expected 86121", loaded[0].SizeVRAMMB)
	}
	if loaded[0].ExpiresAt.Year() != 2318 {
		t.Errorf("expires_at year mismatch: got %d", loaded[0].ExpiresAt.Year())
	}
}

func TestProbeOllamaLoadedModels_StripsV1Suffix(t *testing.T) {
	srv := newOllamaPSStub(t, `{"models":[]}`, http.StatusOK)
	defer srv.Close()

	// Pass the URL with /v1 suffix (the OpenAI-compat shim path). The probe
	// must strip it so the native-API call lands on /api/ps, not /v1/api/ps.
	loaded, err := probeOllamaLoadedModels(context.Background(), srv.URL+"/v1")
	if err != nil {
		t.Fatalf("probe with /v1 suffix failed: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("expected empty model list, got %d", len(loaded))
	}
}

func TestProbeOllamaLoadedModels_HandlesHTTPError(t *testing.T) {
	srv := newOllamaPSStub(t, `internal server error`, http.StatusInternalServerError)
	defer srv.Close()

	_, err := probeOllamaLoadedModels(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error on 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error message to mention status code, got %q", err.Error())
	}
}

func TestProbeOllamaLoadedModels_TimesOutOnHangingServer(t *testing.T) {
	// A "hung" Ollama: accepts the connection, never responds. The probe's
	// 3-second internal timeout must fire.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ps", func(_ http.ResponseWriter, _ *http.Request) {
		time.Sleep(10 * time.Second)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	start := time.Now()
	_, err := probeOllamaLoadedModels(context.Background(), srv.URL)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	// Probe's internal timeout is 3s; require it fires within 5s
	// (account for some GC slack).
	if elapsed > 5*time.Second {
		t.Errorf("probe exceeded 5s ceiling: elapsed %v", elapsed)
	}
}
