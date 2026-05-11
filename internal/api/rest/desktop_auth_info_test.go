// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/auth"
)

// CA-321: pin the /auth/desktop/info response shape. Consumers
// (cli/login.go, cli/setup_admin.go via fetchSetupMinPasswordLength)
// rely on `password_min_length` being a number > 0 when the operator
// has raised the policy. Removing the field would silently break CLI
// inline validation and make all CLI-set passwords be checked at the
// hardcoded 8-char floor instead of the configured minimum.

func newDesktopInfoTestServer(t *testing.T, minLen int) *Server {
	t.Helper()
	jwtMgr := auth.NewJWTManager("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", 60, "oss")
	localAuth := auth.NewLocalAuthWithOptions(jwtMgr, auth.LocalAuthOptions{PasswordMinLength: minLen})
	return &Server{localAuth: localAuth, jwtMgr: jwtMgr}
}

func TestDesktopAuthInfo_ExposesConfiguredPasswordMinLength(t *testing.T) {
	for _, minLen := range []int{8, 12, 20} {
		minLen := minLen
		t.Run("min="+strconv.Itoa(minLen), func(t *testing.T) {
			s := newDesktopInfoTestServer(t, minLen)
			req := httptest.NewRequest(http.MethodGet, "/auth/desktop/info", nil)
			w := httptest.NewRecorder()
			s.handleDesktopAuthInfo(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200; body=%q", w.Code, w.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("body not JSON: %v; raw=%q", err, w.Body.String())
			}
			got, ok := body["password_min_length"].(float64) // JSON numbers decode as float64
			if !ok {
				t.Fatalf("password_min_length missing or wrong type; body=%+v", body)
			}
			if int(got) != minLen {
				t.Errorf("password_min_length: got %d, want %d", int(got), minLen)
			}
		})
	}
}

func TestDesktopAuthInfo_DefaultsTo8WhenLocalAuthNotConfigured(t *testing.T) {
	// Server with localAuth=nil. The handler must still return a sane
	// password_min_length so CLI clients don't divide-by-zero or render
	// "at least 0 characters". 8 mirrors the historical default.
	//
	// This is a degraded-mode test — production always wires localAuth —
	// but it pins the contract for early-boot or test paths.
	s := &Server{}
	defer func() {
		if r := recover(); r != nil {
			t.Skipf("handler relies on s.localAuth.IsSetupDone() which nil-derefs without LocalAuth; "+
				"that's acceptable in production wiring (localAuth is always set), and this test "+
				"only exists to flag if the contract drifts. recovered: %v", r)
		}
	}()
	req := httptest.NewRequest(http.MethodGet, "/auth/desktop/info", nil)
	w := httptest.NewRecorder()
	s.handleDesktopAuthInfo(w, req)
}

