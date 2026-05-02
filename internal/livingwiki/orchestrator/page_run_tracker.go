// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator

import (
	"sort"
	"sync"
	"time"
)

// InFlightPage describes a page that is currently being generated within a
// living-wiki cold-start run. Returned by [Orchestrator.InFlightPages].
type InFlightPage struct {
	// PageID is the planned page's ID (e.g. "arch.auth").
	PageID string

	// TemplateID is the template used for this page.
	TemplateID string

	// Attempt is the current attempt number (1 for first attempt, 2 for retry).
	// Resets to 2 when a page enters its retry attempt.
	Attempt int

	// StartedAt is when the current attempt began.
	StartedAt time.Time
}

// pageRunTracker tracks in-flight pages across concurrent generation runs,
// partitioned by jobID so multiple concurrent cold-start runs on the same
// per-process Orchestrator cannot cross-contaminate each other's snapshots.
//
// All methods are safe for concurrent use.
type pageRunTracker struct {
	mu      sync.Mutex
	pages   map[string]map[string]InFlightPage // outer=jobID, inner=pageID
	windows map[string]*completionWindow       // per-job rolling completion durations
}

// completionWindow is a capped ring buffer of completed-page durations used
// to compute a rolling median for the stuck-page warn threshold.
const completionWindowCap = 64

type completionWindow struct {
	durations []int64 // milliseconds; up to completionWindowCap entries
	head      int     // next write position
	size      int     // number of valid entries
}

func newPageRunTracker() *pageRunTracker {
	return &pageRunTracker{
		pages:   make(map[string]map[string]InFlightPage),
		windows: make(map[string]*completionWindow),
	}
}

// Start records a new in-flight page entry. If the page is already tracked
// (i.e. this is a retry), the entry is replaced with the new startedAt and
// attempt number.
func (t *pageRunTracker) Start(jobID, pageID, templateID string, attempt int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pages[jobID] == nil {
		t.pages[jobID] = make(map[string]InFlightPage)
	}
	t.pages[jobID][pageID] = InFlightPage{
		PageID:     pageID,
		TemplateID: templateID,
		Attempt:    attempt,
		StartedAt:  time.Now(),
	}
}

// Remove removes a single page from the in-flight set for the given job.
// A no-op when the page or job is not present.
func (t *pageRunTracker) Remove(jobID, pageID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if pages, ok := t.pages[jobID]; ok {
		delete(pages, pageID)
	}
}

// RemoveMany removes a set of pages from the in-flight set. Used by the
// deferred error-path sweep in Generate.
func (t *pageRunTracker) RemoveMany(jobID string, pageIDs []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	pages := t.pages[jobID]
	if pages == nil {
		return
	}
	for _, id := range pageIDs {
		delete(pages, id)
	}
}

// Snapshot returns a defensive copy of the in-flight pages for one job,
// sorted by StartedAt ascending. Returns an empty (non-nil) slice when the
// job is not tracked.
func (t *pageRunTracker) Snapshot(jobID string) []InFlightPage {
	t.mu.Lock()
	defer t.mu.Unlock()
	pages := t.pages[jobID]
	out := make([]InFlightPage, 0, len(pages))
	for _, p := range pages {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out
}

// RecordCompletion appends a page-completion duration (in milliseconds) to the
// per-job rolling window, capped at completionWindowCap entries.
func (t *pageRunTracker) RecordCompletion(jobID string, durationMs int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	w := t.windows[jobID]
	if w == nil {
		w = &completionWindow{durations: make([]int64, completionWindowCap)}
		t.windows[jobID] = w
	}
	w.durations[w.head] = durationMs
	w.head = (w.head + 1) % completionWindowCap
	if w.size < completionWindowCap {
		w.size++
	}
}

// MedianCompletedMs returns the median completion duration in milliseconds for
// the given job. Returns (0, false) when fewer than 3 completions have been
// recorded — callers should fall back to a flat threshold in that case.
func (t *pageRunTracker) MedianCompletedMs(jobID string) (int64, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	w := t.windows[jobID]
	if w == nil || w.size < 3 {
		return 0, false
	}
	// Build a sorted copy of valid entries.
	vals := make([]int64, w.size)
	for i := 0; i < w.size; i++ {
		// head points to the NEXT write position; reading backwards from
		// head gives us the most-recent w.size entries in the ring.
		idx := ((w.head - 1 - i) + completionWindowCap) % completionWindowCap
		vals[i] = w.durations[idx]
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	mid := w.size / 2
	if w.size%2 == 1 {
		return vals[mid], true
	}
	return (vals[mid-1] + vals[mid]) / 2, true
}

// Clear drops all in-flight entries and the completion window for the given
// job. Called unconditionally at the end of Generate to release per-job memory.
func (t *pageRunTracker) Clear(jobID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.pages, jobID)
	delete(t.windows, jobID)
}
