// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// CA-146 Phase 0: shared helpers for Living Wiki plan preview and cold-start.
//
// This file holds constants, classification helpers, and the cap/signature
// functions that are consumed by BOTH living_wiki_coldstart.go and the
// (Phase 1) living_wiki_plan_preview.go resolver. Extracting them here
// removes inline duplication and gives the preview resolver a stable,
// tested foundation.

package graphql

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
)

// templateIDArchitecture is the template ID for architecture (subsystem) pages.
const templateIDArchitecture = "architecture"

// repoWideTemplateIDs is the set of template IDs that ALWAYS generate
// regardless of user selection. These three pages define the wiki's
// navigation skeleton.
var repoWideTemplateIDs = map[string]bool{
	"api_reference":   true,
	"system_overview": true,
	"glossary":        true,
}

// GraphQL LivingWikiPageType enum string values (must match
// LivingWikiPageType in schema.graphqls).
const (
	pageTypeRepoWide     = "REPO_WIDE"
	pageTypeArchitecture = "ARCHITECTURE"
	pageTypeTopLevelDir  = "TOP_LEVEL_DIR"
)

// classifyPageType returns the LivingWikiPageType enum value for a template
// ID. Used by both cold-start tally and the preview resolver.
func classifyPageType(templateID string) string {
	if repoWideTemplateIDs[templateID] {
		return pageTypeRepoWide
	}
	if templateID == templateIDArchitecture {
		return pageTypeArchitecture
	}
	return pageTypeTopLevelDir
}

// computePlanSignature returns a deterministic hex-encoded sha256 over the
// (sorted page IDs, mode, effectiveCap) tuple. Used identically by the
// preview resolver AND by EnableLivingWikiForRepo's signature validation path
// so symmetry is mechanical, not a discipline note.
//
// effectiveCap convention (pinned for both call sites):
//
//	*pageCountOverride if non-nil
//	maxPagesPerJob     if maxPagesPerJob > 0
//	0                  otherwise (no cap)
func computePlanSignature(pageIDs []string, mode string, effectiveCap int) string {
	ids := append([]string(nil), pageIDs...)
	sort.Strings(ids)
	h := sha256.New()
	h.Write([]byte(strings.Join(ids, "\n")))
	h.Write([]byte("|"))
	h.Write([]byte(mode))
	h.Write([]byte("|"))
	h.Write([]byte(strconv.Itoa(effectiveCap)))
	return hex.EncodeToString(h.Sum(nil))
}

// applyPageCap applies the effective page count cap to the planned page list.
// Repo-wide pages are always retained; architecture/top-level-dir pages are
// truncated in stable order. Mirrors the inline logic that previously lived
// at coldstart.go:286-328 (now rewritten to call this helper).
//
// When excludedOnlyRetry is true no cap is applied — the caller named specific
// pages and silently discarding them would betray that intent.
//
// Returns:
//
//	out        — post-cap page slice (same slice when no truncation)
//	capSource  — "none" | "repo_setting" | "per_run_override"
//	capValue   — the numeric cap used; 0 when capSource == "none"
//	effectiveCap — the resolved cap (0 = no cap)
//	preCap     — len(pages) before truncation
func applyPageCap(
	pages []lworch.PlannedPage,
	maxPagesPerJob int,
	pageCountOverride *int,
	excludedOnlyRetry bool,
) (out []lworch.PlannedPage, capSource string, capValue int, effectiveCap int, preCap int) {
	preCap = len(pages)
	capSource = "none"
	capValue = 0
	effectiveCap = 0

	if excludedOnlyRetry {
		return pages, capSource, capValue, effectiveCap, preCap
	}

	if pageCountOverride != nil {
		effectiveCap = *pageCountOverride
		capSource = "per_run_override"
	} else if maxPagesPerJob > 0 {
		effectiveCap = maxPagesPerJob
		capSource = "repo_setting"
	}

	if effectiveCap > 0 && preCap > effectiveCap {
		capValue = effectiveCap
		var repoWide, rest []lworch.PlannedPage
		for _, p := range pages {
			if repoWideTemplateIDs[p.TemplateID] {
				repoWide = append(repoWide, p)
			} else {
				rest = append(rest, p)
			}
		}
		allowForRest := effectiveCap - len(repoWide)
		if allowForRest < 0 {
			allowForRest = 0
		}
		if len(rest) > allowForRest {
			rest = rest[:allowForRest]
		}
		return append(repoWide, rest...), capSource, capValue, effectiveCap, preCap
	}

	// Fits within cap (or no cap) — no truncation.
	return pages, "none", 0, effectiveCap, preCap
}

// modeTooltip returns the operator-facing tooltip for a mode string.
// Switch on the GenerationMode* constants (not a map literal, for
// compiler exhaustiveness).
func modeTooltip(mode string) string {
	switch mode {
	case GenerationModeLWDetailed:
		return "Detailed mode — one architecture doc per subsystem cluster, plus the 3 repo-wide pages."
	case GenerationModeLWOverview:
		return "Overview mode — subsystem summaries only, no per-package drill-downs. Fewer pages, broader audience."
	}
	return ""
}
