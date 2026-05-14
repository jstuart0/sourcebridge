// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package config

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

// captureLogger returns a JSON-backed slog.Logger writing to buf.
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestWarnCSRFDisabled_FiresWhenFalse(t *testing.T) {
	cfg := *Defaults()
	cfg.Security.CSRFFullCoverageEnabled = false

	var buf bytes.Buffer
	WarnCSRFDisabled(cfg, captureLogger(&buf))

	if buf.Len() == 0 {
		t.Fatal("expected a log record when CSRFFullCoverageEnabled=false, got none")
	}

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("log output is not valid JSON: %v\noutput: %s", err, buf.String())
	}

	if got, ok := record["level"].(string); !ok || got != "ERROR" {
		t.Errorf("expected level=ERROR, got %v", record["level"])
	}
	if got, ok := record["csrf_full_coverage_enabled"].(bool); !ok || got != false {
		t.Errorf("expected csrf_full_coverage_enabled=false in record, got %v", record["csrf_full_coverage_enabled"])
	}
}

func TestWarnCSRFDisabled_QuietWhenTrue(t *testing.T) {
	cfg := *Defaults()
	cfg.Security.CSRFFullCoverageEnabled = true

	var buf bytes.Buffer
	WarnCSRFDisabled(cfg, captureLogger(&buf))

	if buf.Len() != 0 {
		t.Errorf("expected no log record when CSRFFullCoverageEnabled=true, got: %s", buf.String())
	}
}

func TestWarnAllowPrivateBaseURL_FiresWhenTrue(t *testing.T) {
	cfg := *Defaults()
	cfg.LLM.AllowPrivateBaseURL = true

	var buf bytes.Buffer
	WarnAllowPrivateBaseURL(cfg, captureLogger(&buf))

	if buf.Len() == 0 {
		t.Fatal("expected a log record when AllowPrivateBaseURL=true, got none")
	}

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("log output is not valid JSON: %v\noutput: %s", err, buf.String())
	}

	if got, ok := record["level"].(string); !ok || got != "WARN" {
		t.Errorf("expected level=WARN, got %v", record["level"])
	}
	if got, ok := record["allow_private_base_url"].(bool); !ok || got != true {
		t.Errorf("expected allow_private_base_url=true in record, got %v", record["allow_private_base_url"])
	}
}

func TestWarnAllowPrivateBaseURL_QuietWhenFalse(t *testing.T) {
	cfg := *Defaults()
	cfg.LLM.AllowPrivateBaseURL = false

	var buf bytes.Buffer
	WarnAllowPrivateBaseURL(cfg, captureLogger(&buf))

	if buf.Len() != 0 {
		t.Errorf("expected no log record when AllowPrivateBaseURL=false, got: %s", buf.String())
	}
}
