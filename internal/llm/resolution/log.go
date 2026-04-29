// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package resolution

import (
	"log/slog"
)

// LogResolved emits the per-call structured log line operators grep for to
// confirm the workspace settings are taking effect (e.g. sources.api_key
// = "workspace" instead of "env_fallback"). NEVER includes the raw API
// key — only api_key_set:bool.
//
// Callers (the llmcall.Caller wrappers) invoke this once per RPC. The log
// format is stable; downstream tooling parses sources.<field> values.
func LogResolved(log *slog.Logger, op, repoID string, snap Snapshot) {
	if log == nil {
		log = slog.Default()
	}
	log.Info("llm config resolved",
		"operation", op,
		"repo_id", repoID,
		"provider", snap.Provider,
		"model", snap.Model,
		"base_url_set", snap.BaseURL != "",
		"api_key_set", snap.APIKey != "",
		"timeout_secs", snap.TimeoutSecs,
		"version", snap.Version,
		"stale", snap.Stale,
		// Per-field source labels — operators verify the fix landed by
		// asserting sources.api_key == "workspace".
		"sources_provider", snap.Sources[FieldProvider],
		"sources_base_url", snap.Sources[FieldBaseURL],
		"sources_api_key", snap.Sources[FieldAPIKey],
		"sources_model", snap.Sources[FieldModel],
		"sources_draft_model", snap.Sources[FieldDraftModel],
		"sources_timeout_secs", snap.Sources[FieldTimeoutSecs],
	)
}
