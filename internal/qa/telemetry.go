// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"sync"
	"sync/atomic"
	"time"
)

// --- telemetry counters ---
//
// The public telemetry dashboard aggregates a few QA signals so we
// can watch adoption and regressions in the wild. Two axes:
//   - qa_server_side_enabled (bool): reported via the Features slice
//   - qa_asks_total_14d (int): reported via Counts
//
// The 14-day window is enforced here as a simple ring of per-day
// counters. A fresh process starts with zeros; the count grows until
// a deployment runs for 14+ days, which matches our reporting
// cadence (the dashboard only looks at ~rolling windows).

var (
	serverSideEnabled atomic.Bool

	asksMu   sync.Mutex
	asksRing [14]askBucket // one per UTC day
)

type askBucket struct {
	day   int64 // UTC days since Unix epoch, 0 = empty slot
	count int64
}

// SetServerSideEnabled is called at startup so telemetry can report
// whether the operator flipped the QA flag.
func SetServerSideEnabled(on bool) { serverSideEnabled.Store(on) }

// ServerSideEnabled returns the last set value.
func ServerSideEnabled() bool { return serverSideEnabled.Load() }

// CountAsk increments the running 14-day ask counter. Call once per
// Orchestrator.Ask invocation, regardless of success or fallback —
// we want total volume.
func CountAsk() {
	today := utcDay(time.Now())
	asksMu.Lock()
	defer asksMu.Unlock()
	slot := today % int64(len(asksRing))
	if asksRing[slot].day != today {
		asksRing[slot] = askBucket{day: today, count: 0}
	}
	asksRing[slot].count++
}

// AsksTotal14d returns the sum across the 14-day ring. Slots whose
// `day` is outside the last 14 UTC days are ignored (ensures that
// if a process sat idle for months, a stale slot doesn't pollute the
// count).
func AsksTotal14d() int {
	today := utcDay(time.Now())
	asksMu.Lock()
	defer asksMu.Unlock()
	var total int64
	for _, b := range asksRing {
		if b.day == 0 || today-b.day >= int64(len(asksRing)) {
			continue
		}
		total += b.count
	}
	return int(total)
}

// resetForTest zeros the ring. Exposed only for tests in this
// package; callers shouldn't need it.
func resetForTest() {
	serverSideEnabled.Store(false)
	asksMu.Lock()
	defer asksMu.Unlock()
	for i := range asksRing {
		asksRing[i] = askBucket{}
	}
}

func utcDay(t time.Time) int64 {
	return t.UTC().Unix() / 86400
}
