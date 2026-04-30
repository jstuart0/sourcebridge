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
// Slice 1 of the LLM provider profiles plan adds active_profile_id to
// the line so operators can grep for the currently-active profile.
// Empty string when the dual-read legacy-fallback path is in effect.
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
		"active_profile_id", snap.ActiveProfileID,
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

// LogProfileSwitched is emitted by the activate handler whenever an
// admin promotes a different profile to active (bob-M2 / ruby-L1).
// Operators grep for this on multi-replica clusters to correlate
// "why did the resolved config change?" with admin actions.
func LogProfileSwitched(log *slog.Logger, oldID, newID, by string, version uint64) {
	if log == nil {
		log = slog.Default()
	}
	log.Info("llm profile switched",
		"old_profile_id", oldID,
		"new_profile_id", newID,
		"by", by,
		"workspace_version", version)
}

// LogLegacyWriteReconciled is emitted by the resolver's reconciliation
// path (codex-H2 / r1c) whenever a new-code resolver detects an old-pod
// legacy write and successfully writes-through the legacy contents to
// the active profile. INFO level so it's visible during rolling
// deploys without being noisy in steady state — once the rolling
// deploy completes, this line should not appear.
func LogLegacyWriteReconciled(log *slog.Logger, activeID string, fromWatermark, toWatermark uint64) {
	if log == nil {
		log = slog.Default()
	}
	log.Info("llm legacy write reconciled",
		"active_profile_id", activeID,
		"from_watermark", fromWatermark,
		"to_watermark", toWatermark)
}
