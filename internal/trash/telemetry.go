// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package trash

// Telemetry counters for the recycle bin. Increment from anywhere in
// the codebase; the telemetry tracker reads the snapshot on its next
// ping. Counters are cumulative since process start.
//
// Per CLAUDE.md: adding a new counter here also requires updating the
// sourcebridge-telemetry repo (schema.sql, worker handler, new
// migration) and TELEMETRY.md's collected-fields table.

import "sync/atomic"

var (
	// MovesTotal counts every successful moveToTrash.
	MovesTotal atomic.Int64
	// RestoresTotal counts every successful restoreFromTrash.
	RestoresTotal atomic.Int64
	// ConflictsTotal counts restore attempts that hit a natural-key
	// conflict (includes both cancelled and renamed outcomes).
	ConflictsTotal atomic.Int64
	// PermanentDeletesTotal counts user-initiated hard-deletes via
	// permanentlyDelete.
	PermanentDeletesTotal atomic.Int64
	// PurgesTotal counts rows removed by the retention worker.
	PurgesTotal atomic.Int64
	// SizeSamples is updated by an optional sampler; its latest value
	// is a rough point-in-time count of items in trash across all
	// tracked types.
	SizeSamples atomic.Int64
)

// Counters returns a snapshot suitable for merging into the
// telemetry Counts map. The caller owns the resulting map.
func Counters() map[string]int {
	return map[string]int{
		"trash_moves_total":             int(MovesTotal.Load()),
		"trash_restores_total":          int(RestoresTotal.Load()),
		"trash_conflicts_total":         int(ConflictsTotal.Load()),
		"trash_permanent_deletes_total": int(PermanentDeletesTotal.Load()),
		"trash_purges_total":            int(PurgesTotal.Load()),
		"trash_size_gauge":              int(SizeSamples.Load()),
	}
}
