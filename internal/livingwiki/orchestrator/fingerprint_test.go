// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/quality"
)

// TestComputePlannedPageFingerprintStable verifies that identical inputs across
// two calls produce the same fingerprint string.
func TestComputePlannedPageFingerprintStable(t *testing.T) {
	t.Parallel()
	planned := archPlannedPage("repo", "cluster_auth", []string{"internal/auth", "internal/session"})
	fp1 := computePlannedPageFingerprint(planned, "anthropic/claude-sonnet-4-20250514", "sha-abc123")
	fp2 := computePlannedPageFingerprint(planned, "anthropic/claude-sonnet-4-20250514", "sha-abc123")
	if fp1 != fp2 {
		t.Fatalf("fingerprint not stable: %q vs %q", fp1, fp2)
	}
	if fp1 == "" {
		t.Fatal("fingerprint must not be empty")
	}
}

// TestFingerprintIncludesModelIdentity is the C1 regression test. Two runs with
// different modelIdentity values must produce different fingerprints for the
// same PlannedPage, ensuring that switching the LLM provider invalidates every
// cached page.
func TestFingerprintIncludesModelIdentity(t *testing.T) {
	t.Parallel()
	planned := archPlannedPage("repo", "cluster_auth", nil)
	fp1 := computePlannedPageFingerprint(planned, "anthropic/claude-sonnet-4-20250514", "sha-abc123")
	fp2 := computePlannedPageFingerprint(planned, "ollama/mistral:7b", "sha-abc123")
	if fp1 == fp2 {
		t.Fatal("fingerprints must differ when modelIdentity differs")
	}
}

// TestComputePlannedPageFingerprintDiffersOnInputs verifies that each distinct
// input dimension produces a different fingerprint.
func TestComputePlannedPageFingerprintDiffersOnInputs(t *testing.T) {
	t.Parallel()

	base := archPlannedPage("repo", "cluster_auth", []string{"internal/auth"})
	baseModel := "anthropic/claude-sonnet-4-20250514"
	baseRev := "sha-abc123"
	baseline := computePlannedPageFingerprint(base, baseModel, baseRev)

	cases := []struct {
		name    string
		planned PlannedPage
		model   string
		rev     string
	}{
		{
			name:    "different package",
			planned: archPlannedPage("repo", "cluster_storage", []string{"internal/auth"}),
			model:   baseModel,
			rev:     baseRev,
		},
		{
			name:    "different member packages",
			planned: archPlannedPage("repo", "cluster_auth", []string{"internal/auth", "internal/oauth"}),
			model:   baseModel,
			rev:     baseRev,
		},
		{
			name:    "different audience",
			planned: withAudience(base, quality.AudienceProduct),
			model:   baseModel,
			rev:     baseRev,
		},
		{
			name:    "different repoSourceRev",
			planned: base,
			model:   baseModel,
			rev:     "sha-def456",
		},
		{
			name:    "different modelIdentity",
			planned: base,
			model:   "ollama/llama3",
			rev:     baseRev,
		},
		{
			name:    "empty model resolves to unresolved/unresolved",
			planned: base,
			model:   "",
			rev:     baseRev,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fp := computePlannedPageFingerprint(tc.planned, tc.model, tc.rev)
			if fp == baseline {
				t.Errorf("%s: fingerprint must differ from baseline but was identical (%q)", tc.name, fp)
			}
		})
	}
}

// TestFingerprintIncludesKnowledgeArtifactRev verifies CR6: same CommitSHA but
// different artifact RevisionFp produces different fingerprints.
func TestFingerprintIncludesKnowledgeArtifactRev(t *testing.T) {
	t.Parallel()
	base := archPlannedPage("repo", "cluster_auth", nil)
	withArt := func(revFp string) PlannedPage {
		p := base
		pi := *p.PackageInfo
		pi.KnowledgeArtifacts = []KnowledgeArtifactSummary{{
			ID:        "art-1",
			RevisionFp: revFp,
			ScopeType: "module",
			ScopePath: "internal/auth",
			Depth:     "deep",
		}}
		p.PackageInfo = &pi
		return p
	}
	fp1 := computePlannedPageFingerprint(withArt("rev-a"), "model/x", "sha-1")
	fp2 := computePlannedPageFingerprint(withArt("rev-b"), "model/x", "sha-1")
	if fp1 == fp2 {
		t.Fatal("fingerprints must differ when artifact RevisionFp differs")
	}
}

// TestFingerprintIncludesKnowledgeArtifactID verifies CR6: same RevisionFP but
// different artifact ID (artifact deleted and recreated) produces different fingerprints.
func TestFingerprintIncludesKnowledgeArtifactID(t *testing.T) {
	t.Parallel()
	base := archPlannedPage("repo", "cluster_auth", nil)
	withArt := func(id string) PlannedPage {
		p := base
		pi := *p.PackageInfo
		pi.KnowledgeArtifacts = []KnowledgeArtifactSummary{{
			ID:        id,
			RevisionFp: "rev-fixed",
			ScopeType: "module",
			ScopePath: "internal/auth",
			Depth:     "summary",
		}}
		p.PackageInfo = &pi
		return p
	}
	fp1 := computePlannedPageFingerprint(withArt("art-1"), "model/x", "sha-1")
	fp2 := computePlannedPageFingerprint(withArt("art-2"), "model/x", "sha-1")
	if fp1 == fp2 {
		t.Fatal("fingerprints must differ when artifact ID differs")
	}
}

// TestNonArchitectureFingerprintUsesOnlyPageID verifies that non-architecture
// pages (glossary, api_reference) use only planned.ID in their input fingerprint.
func TestNonArchitectureFingerprintUsesOnlyPageID(t *testing.T) {
	t.Parallel()
	glossary := PlannedPage{
		ID:         "repo.glossary",
		TemplateID: "glossary",
		Audience:   quality.AudienceEngineers,
	}
	fp1 := computePlannedPageFingerprint(glossary, "model/x", "sha-abc")
	fp2 := computePlannedPageFingerprint(glossary, "model/x", "sha-abc")
	if fp1 != fp2 {
		t.Fatal("non-architecture fingerprint must be stable")
	}
	// Changing the ID produces a different fingerprint.
	glossary2 := glossary
	glossary2.ID = "repo.api_reference"
	glossary2.TemplateID = "api_reference"
	fp3 := computePlannedPageFingerprint(glossary2, "model/x", "sha-abc")
	if fp1 == fp3 {
		t.Fatal("non-architecture fingerprints must differ for different IDs")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

func archPlannedPage(repoID, clusterLabel string, members []string) PlannedPage {
	return PlannedPage{
		ID:         repoID + ".arch." + clusterLabel,
		TemplateID: "architecture",
		Audience:   quality.AudienceEngineers,
		PackageInfo: &ArchitecturePackageInfo{
			Package:        clusterLabel,
			MemberPackages: members,
		},
	}
}

func withAudience(p PlannedPage, a quality.Audience) PlannedPage {
	p.Audience = a
	return p
}
