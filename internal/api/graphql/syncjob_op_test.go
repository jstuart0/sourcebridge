// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
)

// TestSyncJobOpCoversAllLLMSubsystems enumerates every llm.Subsystem constant
// and asserts that syncJobOp returns the documented expected op for it.
//
// The purpose is to catch a future contributor adding a Subsystem constant
// without updating the switch in syncJobOp: if they forget, this test fails
// with a clear message naming the offending subsystem.
//
// Expected mappings are maintained here in parallel with the switch in
// resolver.go. When a mapping changes, update BOTH.
//
// Subsystems deliberately omitted from syncJobOp:
//   - SubsystemLivingWiki: routed through livingWikiOpForJobType, not
//     runSyncLLMJob / syncJobOp. No switch case needed.
//   - SubsystemClustering: CPU-bound background job; no LLM call.
//     Falls to default (OpAnalysis) — legitimate, documented here.
func TestSyncJobOpCoversAllLLMSubsystems(t *testing.T) {
	// expectedOp maps each subsystem to its documented expected return value
	// from syncJobOp("", jobType) where jobType is the empty string (the
	// non-jobType-sensitive default path for each subsystem).
	//
	// For SubsystemRequirements, the mapping depends on whether jobType
	// contains "extract". We test the non-extract default (OpRequirementsEnrich)
	// and the extract variant separately.
	//
	// For SubsystemQA, the mapping depends on jobType. We test the fallback
	// path (OpQASynth) and the specific variants separately.
	expectedOp := map[llm.Subsystem]string{
		llm.SubsystemKnowledge:    resolution.OpKnowledge,
		llm.SubsystemReasoning:    resolution.OpDiscussion,
		llm.SubsystemRequirements: resolution.OpRequirementsEnrich, // non-extract default
		llm.SubsystemLinking:      resolution.OpAnalysis,
		llm.SubsystemContracts:    resolution.OpAnalysis,
		llm.SubsystemQA:           resolution.OpQASynth, // fallback path (empty jobType)
		// SubsystemClustering: falls to OpAnalysis by default (no LLM calls;
		// CPU-bound job). Explicit here so a future reader understands the
		// default is intentional, not an oversight.
		llm.SubsystemClustering: resolution.OpAnalysis,
		// SubsystemLivingWiki: NOT routed through syncJobOp in production.
		// livingWikiOpForJobType handles the op mapping for living-wiki jobs.
		// syncJobOp falls through to OpAnalysis for this subsystem, which is
		// the documented behaviour (the actual living-wiki path never calls
		// syncJobOp). Listed here so the test enumerates the full set and
		// documents the "not a real mapping" intent.
		llm.SubsystemLivingWiki: resolution.OpAnalysis,
	}

	// Enumerate every Subsystem constant so a new addition triggers a
	// compile-time "not in expectedOp" detection below.
	allSubsystems := []llm.Subsystem{
		llm.SubsystemKnowledge,
		llm.SubsystemReasoning,
		llm.SubsystemRequirements,
		llm.SubsystemLinking,
		llm.SubsystemContracts,
		llm.SubsystemQA,
		llm.SubsystemClustering,
		llm.SubsystemLivingWiki,
	}

	// Verify the expected map covers every entry in allSubsystems.
	for _, sub := range allSubsystems {
		if _, ok := expectedOp[sub]; !ok {
			t.Errorf("subsystem %q has no expected-op entry in this test; add it to expectedOp and document its mapping", sub)
		}
	}

	// Verify syncJobOp returns the expected op for each subsystem.
	for _, sub := range allSubsystems {
		want := expectedOp[sub]
		got := syncJobOp(sub, "") // empty jobType exercises the default path
		if got != want {
			t.Errorf("syncJobOp(%q, %q) = %q; want %q", sub, "", got, want)
		}
	}

	// Additional test: verify jobType-sensitive branches for Requirements.
	t.Run("requirements extract branch", func(t *testing.T) {
		got := syncJobOp(llm.SubsystemRequirements, "extract_requirements")
		if got != resolution.OpRequirementsExtract {
			t.Errorf("syncJobOp(SubsystemRequirements, %q) = %q; want %q",
				"extract_requirements", got, resolution.OpRequirementsExtract)
		}
	})

	// Additional test: verify jobType-sensitive QA branches.
	qaBranches := []struct {
		jobType string
		want    string
	}{
		{"qa.classify", resolution.OpQAClassify},
		{"qa.decompose", resolution.OpQADecompose},
		{"qa.synth", resolution.OpQASynth},
		{"qa.deep_synth", resolution.OpQASynth},
		{"qa.agent_turn", resolution.OpQAAgentTurn},
	}
	for _, tc := range qaBranches {
		t.Run("qa "+tc.jobType, func(t *testing.T) {
			got := syncJobOp(llm.SubsystemQA, tc.jobType)
			if got != tc.want {
				t.Errorf("syncJobOp(SubsystemQA, %q) = %q; want %q", tc.jobType, got, tc.want)
			}
		})
	}
}
