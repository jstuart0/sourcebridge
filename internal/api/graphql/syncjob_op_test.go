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
// The test iterates llm.AllSubsystems (the canonical list maintained in
// internal/llm/job.go) instead of a local hand-rolled slice. Adding a new
// Subsystem constant requires updating llm.AllSubsystems in the same commit,
// which means the test will fail for the new subsystem until an expectedOp
// entry is also added here — closing the gap that Slice 7 left open.
//
// Subsystems in the explicit OpAnalysis allowlist below fall to the syncJobOp
// default for a documented reason (not an accidental omission):
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
		// SubsystemClustering falls to OpAnalysis by default. ALLOWLISTED: CPU-bound
		// background job with no LLM calls; the OpAnalysis default is intentional.
		// Do NOT remove from expectedOp — it must appear here to confirm the intent
		// rather than silently passing because the switch has no case for it.
		llm.SubsystemClustering: resolution.OpAnalysis,
		// SubsystemLivingWiki falls to OpAnalysis by default. ALLOWLISTED: NOT
		// routed through syncJobOp in production — livingWikiOpForJobType handles
		// the op mapping for living-wiki jobs. The actual living-wiki path never
		// calls syncJobOp; the fallthrough to OpAnalysis is the documented,
		// expected behavior. Listed here (not omitted) so the test enumerates the
		// full set and documents the "not a real mapping" intent.
		llm.SubsystemLivingWiki: resolution.OpAnalysis,
	}

	// Iterate llm.AllSubsystems — the canonical list from internal/llm/job.go.
	// A new Subsystem constant that is not added to AllSubsystems will not be
	// discovered here, but the AllSubsystems maintenance contract (documented
	// in job.go) means the constant and AllSubsystems must change together.
	// This is simpler and more explicit than AST parsing, and the compile-time
	// contract means a missing AllSubsystems update is detectable at code review.
	for _, sub := range llm.AllSubsystems {
		if _, ok := expectedOp[sub]; !ok {
			t.Errorf("subsystem %q is in llm.AllSubsystems but has no expected-op entry in this test; "+
				"add it to expectedOp and document its mapping (or add to the OpAnalysis allowlist above "+
				"if it legitimately falls through)", sub)
		}
	}

	// Verify syncJobOp returns the expected op for each subsystem.
	for _, sub := range llm.AllSubsystems {
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
