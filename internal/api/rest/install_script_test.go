// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/sourcebridge/sourcebridge/internal/installassets"
)

// TestInstallScriptRoute verifies that GET /install.sh returns the embedded
// SourceBridge installer with the expected headers. The full Server.Routes()
// pipeline is heavy to spin up in tests; here we wire just the handler onto a
// fresh chi router to assert the contract independently of the rest of the
// server boot.
func TestInstallScriptRoute(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/install.sh", installassets.Handler())

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/install.sh")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d; want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/x-shellscript") {
		t.Errorf("Content-Type = %q; want text/x-shellscript", ct)
	}
	if cto := resp.Header.Get("X-Content-Type-Options"); cto != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q; want nosniff", cto)
	}
	body := make([]byte, 1024)
	n, _ := resp.Body.Read(body)
	if !bytes.HasPrefix(body[:n], []byte("#!/bin/sh")) {
		t.Errorf("body does not start with #!/bin/sh; got: %q", string(body[:n]))
	}
}
