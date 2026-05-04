// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// CA-146 Phase 0+2: shared helpers for Living Wiki plan preview and cold-start.
//
// This file holds constants, classification helpers, and the cap/signature
// functions that are consumed by BOTH living_wiki_coldstart.go and the
// (Phase 1) living_wiki_plan_preview.go resolver. Extracting them here
// removes inline duplication and gives the preview resolver a stable,
// tested foundation.
//
// GQL-5: applyPageCap and applyPageSelection have moved to
// internal/livingwiki/coldstart/runner.go (exported as ApplyPageCap and
// ApplyPageSelection). The package-private shims in living_wiki_coldstart.go
// delegate to those exports so existing call sites remain unchanged.

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

// classifyPageType returns the LivingWikiPageType enum for a planned page.
// It prefers the explicit Kind field; falls back to TemplateID heuristics for
// legacy persisted plans (smart-resume) where Kind == PageKindUnknown.
//
// TODO: remove the PageKindUnknown fallback once all persisted plans (smart-
// resume cache, audit logs) populate Kind.
func classifyPageType(p lworch.PlannedPage) LivingWikiPageType {
	switch p.Kind {
	case lworch.PageKindRepoWide:
		return LivingWikiPageTypeRepoWide
	case lworch.PageKindCluster:
		return LivingWikiPageTypeArchitecture
	case lworch.PageKindTopLevelDir:
		return LivingWikiPageTypeTopLevelDir
	}
	// Legacy fallback: PageKindUnknown (zero value). Used for persisted plans
	// that predate the Kind field. Remove once all call paths populate Kind.
	if repoWideTemplateIDs[p.TemplateID] {
		return LivingWikiPageTypeRepoWide
	}
	if p.TemplateID == templateIDArchitecture {
		return LivingWikiPageTypeArchitecture
	}
	return LivingWikiPageTypeTopLevelDir
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

// buildPlanFromPages constructs a LivingWikiPlan from a cap-applied page slice
// and the resolved cap metadata. Used to populate the freshPlan extension on
// LIVING_WIKI_PLAN_STALE errors so the client can present the current plan
// without a second round-trip.
func buildPlanFromPages(
	pages []lworch.PlannedPage,
	mode string,
	effectiveCap int,
	capSource string,
	capValue int,
	preCap int,
	summary string,
) *LivingWikiPlan {
	planSig := computePlanSignature(pageIDsOf(pages), mode, effectiveCap)
	gqlPages := make([]*LivingWikiPlanPage, 0, len(pages))
	for _, p := range pages {
		gqlPages = append(gqlPages, plannedPageToGQL(p))
	}
	return &LivingWikiPlan{
		PlanSignature: planSig,
		Mode:          mode,
		ModeTooltip:   modeTooltip(mode),
		Summary:       summary,
		TotalPages:    len(pages),
		PreCap:        preCap,
		CapSource:     capSource,
		CapValue:      capValue,
		Pages:         gqlPages,
	}
}

// pageIDsOf extracts IDs from a page slice; helper for buildPlanFromPages and
// computePlanSignature call sites.
func pageIDsOf(pages []lworch.PlannedPage) []string {
	ids := make([]string, len(pages))
	for i, p := range pages {
		ids[i] = p.ID
	}
	return ids
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
