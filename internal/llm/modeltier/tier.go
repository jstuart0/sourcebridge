// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package modeltier defines the QualityGateTier calibration axis used by
// Living Wiki quality validators.
//
// QualityGateTier is intentionally distinct from the REST-layer PriceTier in
// internal/api/rest/model_metadata.go and from the per-model capability grades
// in internal/settings/comprehension/models.go.
//
// This package is strictly pure — it does NOT import
// internal/settings/comprehension. Registry-backed classification lives in the
// cold-start consumer (internal/api/graphql/living_wiki_coldstart.go).
package modeltier

import (
	"context"
	"strings"
)

// QualityGateTier is a coarse calibration axis that Living Wiki quality
// validators use to select appropriate gate thresholds. Frontier-class models
// (Claude Opus/Sonnet, GPT-4-class) are held to strict prose and citation
// bars; mid-tier and local models receive relaxed thresholds so that OSS
// installs using smaller open-weights models produce usable output instead of
// a 100% rejection run.
type QualityGateTier string

const (
	// TierUnknown is the explicit sentinel for "tier not yet resolved."
	// No code path treats TierUnknown as TierFrontier silently.
	// Callers MUST normalize to a known tier before passing to DefaultProfile.
	TierUnknown QualityGateTier = ""

	// TierFrontier covers Claude Opus/Sonnet, GPT-4-class models, Gemini
	// Pro/Ultra, and similarly capable hosted frontier models.
	TierFrontier QualityGateTier = "frontier"

	// TierMid covers capable open-weights models ≥70B parameters and
	// efficient mid-size hosted variants (e.g. gpt-4o-mini, gemini-flash).
	TierMid QualityGateTier = "mid"

	// TierLocal covers open-weights models <70B parameters typically run via
	// Ollama, vLLM, llama.cpp, or similar local inference servers. The default
	// OSS install (config.toml.example: qwen3:32b) maps here so citation-density
	// and vagueness gates are relaxed and fresh installs cannot hit the
	// "all pages rejected" outage symptom.
	TierLocal QualityGateTier = "local"
)

// String implements fmt.Stringer. Returns the empty string for TierUnknown.
func (t QualityGateTier) String() string { return string(t) }

// IsValid reports whether t is one of the four defined tier values.
func (t QualityGateTier) IsValid() bool {
	switch t {
	case TierUnknown, TierFrontier, TierMid, TierLocal:
		return true
	}
	return false
}

// Parse converts a string to a QualityGateTier, case-insensitively and
// with leading/trailing whitespace trimmed.
// Returns (tier, true) for a recognized value; ("", false) for anything
// unrecognized. The empty string maps to (TierUnknown, true).
func Parse(s string) (QualityGateTier, bool) {
	t := QualityGateTier(strings.ToLower(strings.TrimSpace(s)))
	if t.IsValid() {
		return t, true
	}
	return TierUnknown, false
}

// Resolution is the structured result returned by a TierFunc. On Err != nil,
// Tier is still populated (pattern fallback ran); callers should log Err at
// warn level and continue with the fallback tier.
type Resolution struct {
	// Tier is the resolved quality-gate calibration tier. Always populated
	// (TierLocal as last-resort default when no pattern matched).
	Tier QualityGateTier

	// Source describes how Tier was determined. Values:
	//   "registry" — resolved from the Model Registry (comprehension store)
	//   "pattern"  — resolved by ClassifyByPattern (no registry hit)
	//   "fallback" — last-resort default (no pattern matched; TierLocal assigned)
	Source string

	// Err is non-nil when a registry lookup attempt failed. The caller
	// (living_wiki_coldstart.go) chose pattern fallback; Err is included
	// so the per-run resolution log can carry the error string.
	Err error
}

// TierFunc is the function type injected into Living Wiki cold-start for
// tier resolution. The context.Context is kept even though ClassifyByPattern
// doesn't use it — the registry-backed implementation does I/O and must
// accept a context. Callers use a single TierFunc throughout a run so that
// tier is computed exactly once per page generation job.
type TierFunc func(ctx context.Context, provider, model string) Resolution
