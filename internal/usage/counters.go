// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package usage

// QueriesCounter is the process-local 30-day rolling counter for QA
// invocations (every Orchestrator.Ask call). Incremented from
// internal/qa/pipeline.go alongside the existing qa.CountAsk() call.
//
// Process-local: resets to zero on agent restart.
var QueriesCounter = NewRollingDayCounter(30)

// ArtifactsCounter is the process-local 30-day rolling counter for
// knowledge artifacts (cliff notes, architecture diagram, learning path,
// code tour, workflow story) that successfully transitioned from
// GENERATING to READY via user-requested generation.
//
// Process-local: resets to zero on agent restart.
var ArtifactsCounter = NewRollingDayCounter(30)

// Counters returns both rolling counters as a map[string]int suitable for
// merging into the telemetry Counts blob. Keys match the keys disclosed in
// TELEMETRY.md and read by the collector via json_extract.
func Counters() map[string]int {
	return map[string]int{
		"queries_30d":             int(QueriesCounter.Total()),
		"artifacts_generated_30d": int(ArtifactsCounter.Total()),
	}
}

// ResetCountersForTest resets both package-level counters to zero. Call from
// t.Cleanup to keep tests isolated from one another.
func ResetCountersForTest() {
	QueriesCounter.ResetForTest()
	ArtifactsCounter.ResetForTest()
}
