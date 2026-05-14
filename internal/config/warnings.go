// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package config

import "log/slog"

// WarnCSRFDisabled emits a slog.Error line when CSRF full coverage has been
// explicitly disabled by operator. Extracted for testability (TES-M1 + CA-334).
func WarnCSRFDisabled(cfg Config, logger *slog.Logger) {
	if cfg.Security.CSRFFullCoverageEnabled {
		return
	}
	logger.Error("CSRF full coverage is DISABLED",
		"csrf_full_coverage_enabled", false,
		"remediation", "remove SOURCEBRIDGE_SECURITY_CSRF_FULL_COVERAGE_ENABLED=false; this exposes the admin route group to CSRF (CA-334)")
}

// WarnAllowPrivateBaseURL emits a slog.Warn line when LLM private-IP base
// URLs are accepted (the default; preserves Ollama local-LLM workflows).
// Operators not running a local LLM should set the flag to false.
// Extracted for testability (TES-M1 + CA-336); Phase 3 wires this at the
// cli/serve.go startup-warnings call site.
func WarnAllowPrivateBaseURL(cfg Config, logger *slog.Logger) {
	if !cfg.LLM.AllowPrivateBaseURL {
		return
	}
	logger.Warn("LLM private-IP base URLs are allowed",
		"allow_private_base_url", true,
		"remediation", "set SOURCEBRIDGE_LLM_ALLOW_PRIVATE_BASE_URL=false if you do not use a local LLM (Ollama/vLLM)")
}
