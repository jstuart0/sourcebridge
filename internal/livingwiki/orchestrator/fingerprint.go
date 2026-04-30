// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// fingerprint.go — pure helpers for content fingerprinting of Living Wiki
// planned pages.
//
// The fingerprint is a sha256 hash of the page's deterministic inputs. It is
// computed once per planned page during smart-resume and stored in
// lw_page_publish_status when a page is successfully dispatched to a sink.
// On the next run, smart-resume compares the stored fingerprint against the
// freshly-computed one: mismatch → regenerate, match → skip (LD-7).
//
// Algorithm (v3, post-CR6):
//
//   sha256(
//     "v3\n"                                  // schema version — bump to invalidate all
//     + modelIdentity + "\n"                  // e.g. "anthropic/claude-sonnet-4-20250514"
//     + planned.TemplateID + "\n"             // template choice
//     + string(planned.Audience) + "\n"       // audience profile
//     + TemplateVersion(planned.TemplateID)   // prompt/structure version
//     + "\n"
//     + repoSourceRev                         // Repository.CommitSHA or LastIndexedAt fallback
//     + "\n"
//     + plannedInputFingerprint(planned)       // per-page-type stable inputs
//   )
//
// "v3" is the current schema version. Prior runs used "v2" (no knowledge
// artifacts) and "v1" (no modelIdentity). Every existing fingerprint mismatches
// on first deploy after a schema bump — by design (one-time regen cost).
//
// modelIdentity format: "<provider>/<model>", e.g.
// "anthropic/claude-sonnet-4-20250514" or "ollama/mistral:7b". Empty provider
// or model resolves to "unresolved/unresolved" (fail-loud sentinel per C1).
//
// plannedInputFingerprint for architecture pages includes:
//   - planned.ID
//   - PackageInfo.Package + sorted MemberPackages + sorted Callers + sorted Callees
//   - sorted [artifact.ID + "|" + artifact.RevisionFp + "|" + artifact.ScopeType
//             + "|" + artifact.ScopePath + "|" + artifact.Depth
//             for artifact in pkg.KnowledgeArtifacts]  (CR6)
//
// For non-architecture pages (api_reference, glossary, system_overview):
//   - planned.ID only (repoSourceRev is sufficient; they cover the whole repo)
//
// Plan: thoughts/shared/plans/2026-04-29-livingwiki-incremental-publish-redesign.md
// — LD-7, C1, CR6.

package orchestrator

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
)

// ComputePageFingerprint returns the content fingerprint for a single planned
// page. It is exported for use by the cold-start runner in the graphql package
// (which calls it once per planned page before smart-resume splits buckets).
// Internal callers (fingerprint_test.go) use the unexported alias below.
//
// Inputs:
//
//   - planned:       the planned page (ID, TemplateID, Audience, PackageInfo).
//   - modelIdentity: the resolved LLM identity at run start ("provider/model").
//   - repoSourceRev: the repo's current CommitSHA or LastIndexedAt nanoseconds.
//
// The function is pure (no I/O) and deterministic for a given input set.
// Table-driven tests in fingerprint_test.go cover every input dimension.
func ComputePageFingerprint(planned PlannedPage, modelIdentity, repoSourceRev string) string {
	if modelIdentity == "" {
		modelIdentity = "unresolved/unresolved"
	}

	var sb strings.Builder
	sb.WriteString("v3\n")
	sb.WriteString(modelIdentity)
	sb.WriteByte('\n')
	sb.WriteString(planned.TemplateID)
	sb.WriteByte('\n')
	sb.WriteString(string(planned.Audience))
	sb.WriteByte('\n')
	sb.WriteString(templates.TemplateVersion(planned.TemplateID))
	sb.WriteByte('\n')
	sb.WriteString(repoSourceRev)
	sb.WriteByte('\n')
	sb.WriteString(plannedInputFingerprint(planned))

	sum := sha256.Sum256([]byte(sb.String()))
	return fmt.Sprintf("%x", sum)
}

// computePlannedPageFingerprint is the unexported alias used by package-internal
// tests (fingerprint_test.go). It delegates to the exported function.
func computePlannedPageFingerprint(planned PlannedPage, modelIdentity, repoSourceRev string) string {
	return ComputePageFingerprint(planned, modelIdentity, repoSourceRev)
}

// plannedInputFingerprint returns the stable per-page-type input string used
// in computePlannedPageFingerprint. For architecture pages it includes all
// structural inputs (package, members, callers, callees, knowledge artifacts).
// For non-architecture pages it is just the page ID (repo-wide, so repoSourceRev suffices).
func plannedInputFingerprint(planned PlannedPage) string {
	if planned.TemplateID != "architecture" || planned.PackageInfo == nil {
		// Non-architecture pages are repo-wide; repoSourceRev in the outer hash
		// captures any code change. Use just the ID for stable per-page identity.
		return planned.ID
	}

	pkg := planned.PackageInfo

	// Sort mutable slices for determinism (map/slice iteration order is not stable).
	members := sortedCopy(pkg.MemberPackages)
	callers := sortedCopy(pkg.Callers)
	callees := sortedCopy(pkg.Callees)

	// Knowledge artifact inputs (CR6): include ID, RevisionFp, ScopeType,
	// ScopePath, Depth so that a re-run of understanding at the same CommitSHA
	// (same repoSourceRev) invalidates the fingerprint when artifact content changes.
	artifactStrings := make([]string, 0, len(pkg.KnowledgeArtifacts))
	for _, art := range pkg.KnowledgeArtifacts {
		artifactStrings = append(artifactStrings,
			art.ID+"|"+art.RevisionFp+"|"+art.ScopeType+"|"+art.ScopePath+"|"+art.Depth)
	}
	sort.Strings(artifactStrings)

	var sb strings.Builder
	sb.WriteString(planned.ID)
	sb.WriteByte('\n')
	sb.WriteString(pkg.Package)
	sb.WriteByte('\n')
	sb.WriteString(strings.Join(members, ","))
	sb.WriteByte('\n')
	sb.WriteString(strings.Join(callers, ","))
	sb.WriteByte('\n')
	sb.WriteString(strings.Join(callees, ","))
	sb.WriteByte('\n')
	sb.WriteString(strings.Join(artifactStrings, ";"))
	return sb.String()
}

// sortedCopy returns a sorted copy of ss (preserving the original).
func sortedCopy(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	cp := make([]string, len(ss))
	copy(cp, ss)
	sort.Strings(cp)
	return cp
}
