// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"os"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/usage"
)

// TestMain enforces the CA-400 protocol for the graphql package:
// any test in this package that calls markArtifactReady or otherwise
// increments usage.ArtifactsCounter / usage.QueriesCounter sees a fresh
// counter at start. The existing t.Cleanup(usage.ResetCountersForTest)
// in mark_artifact_ready_test.go remains as defense-in-depth between
// sub-tests (CA-484 / T-M3).
func TestMain(m *testing.M) {
	usage.ResetCountersForTest()
	code := m.Run()
	os.Exit(code)
}
