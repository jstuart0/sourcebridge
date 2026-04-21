// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package events

import "time"

// Event types
const (
	EventRepoIndexStarted   = "repo.index.started"
	EventRepoIndexCompleted = "repo.index.completed"
	EventRepoIndexFailed    = "repo.index.failed"
	EventRepoIndexProgress  = "repo.index.progress"

	EventRequirementImported = "requirement.imported"
	EventRequirementLinked   = "requirement.linked"
	EventSpecExtraction      = "spec.extraction.completed"

	EventLinkVerified = "link.verified"
	EventLinkRejected = "link.rejected"

	EventReviewCompleted = "review.completed"

	// Recycle-bin events. Granular (per-row) events use EventTrashChanged;
	// cascade + bulk operations coalesce to EventTrashBulkChanged; the
	// per-repo badge is driven by EventTrashCountChanged.
	EventTrashChanged      = "trash.changed"
	EventTrashBulkChanged  = "trash.bulk_changed"
	EventTrashCountChanged = "trash.count_changed"
)

// Event represents a domain event.
type Event struct {
	Type      string                 `json:"type"`
	Timestamp time.Time              `json:"timestamp"`
	Data      map[string]interface{} `json:"data"`
}

// NewEvent creates a new event.
func NewEvent(eventType string, data map[string]interface{}) Event {
	return Event{
		Type:      eventType,
		Timestamp: time.Now(),
		Data:      data,
	}
}
