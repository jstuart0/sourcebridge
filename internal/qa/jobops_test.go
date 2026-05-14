// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
)

func TestJobTypeToOp(t *testing.T) {
	cases := []struct {
		jobType string
		want    string
	}{
		{"qa.classify", resolution.OpQAClassify},
		{"qa.decompose", resolution.OpQADecompose},
		{"qa.deep_synth", resolution.OpQADeepSynth},
		{"qa.agent_turn", resolution.OpQAAgentTurn},
		{"unknown", resolution.OpQASynth},
	}
	for _, tc := range cases {
		t.Run(tc.jobType, func(t *testing.T) {
			got := JobTypeToOp(tc.jobType)
			if got != tc.want {
				t.Errorf("JobTypeToOp(%q) = %q; want %q", tc.jobType, got, tc.want)
			}
		})
	}
}

// TestJobTypeToOpCanary_QADeepSynth is the load-bearing single-package
// regression gate for CA-327. qa.deep_synth must map to OpQADeepSynth
// across all transports — REST, MCP, and GraphQL all call JobTypeToOp
// as their single source of truth.
func TestJobTypeToOpCanary_QADeepSynth(t *testing.T) {
	got := JobTypeToOp("qa.deep_synth")
	if got != resolution.OpQADeepSynth {
		t.Errorf("qa.deep_synth must map to OpQADeepSynth across all transports (CA-327); got %s", got)
	}
}
