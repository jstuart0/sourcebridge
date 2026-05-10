// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/qa"
)

// TestAskInputToQA_NilModeMapsToEmpty pins that the adapter passes nil
// Mode through as qa.Mode("") rather than defaulting to fast or deep.
// Defaulting is the pipeline's job (internal/qa/pipeline.go); the
// adapter is a pure mapper and must not make that decision itself.
func TestAskInputToQA_NilModeMapsToEmpty(t *testing.T) {
	in := AskInput{
		RepositoryID: "repo-1",
		Question:     "What does NewRandom do?",
		Mode:         nil, // caller omitted mode
	}
	out := askInputToQA(in)
	if out.Mode != qa.Mode("") {
		t.Errorf("askInputToQA with nil Mode: got %q, want empty string", out.Mode)
	}
}

// TestAskDiagnosticsFromQA_PropagatesMode pins that the diagnostics
// mapper round-trips Mode without clearing it. A future "helpful
// normalization" that resets Mode in the response shape would silently
// hide which path ran from callers.
func TestAskDiagnosticsFromQA_PropagatesMode(t *testing.T) {
	d := qa.AskDiagnostics{Mode: "deep"}
	out := askDiagnosticsFromQA(d)
	if out.Mode == nil {
		t.Fatal("askDiagnosticsFromQA: Mode is nil, want non-nil pointer")
	}
	if *out.Mode != "deep" {
		t.Errorf("askDiagnosticsFromQA: *Mode = %q, want %q", *out.Mode, "deep")
	}
}
