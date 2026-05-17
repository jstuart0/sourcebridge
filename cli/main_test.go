// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"os"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/usage"
)

// TestMain enforces the CA-400 protocol for the cli package:
// any test in this package that calls TelemetryCounts or otherwise
// increments usage.ArtifactsCounter / usage.QueriesCounter sees a fresh
// counter at process start. The existing t.Cleanup(usage.ResetCountersForTest)
// in serve_telemetry_test.go remains as defense-in-depth between sub-tests.
func TestMain(m *testing.M) {
	usage.ResetCountersForTest()
	code := m.Run()
	os.Exit(code)
}
