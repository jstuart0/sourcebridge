// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package jobs

import "github.com/sourcebridge/sourcebridge/internal/db"

// Queue manages background job processing.
type Queue struct {
	cache db.Cache
}

// NewQueue creates a new job queue backed by cache.
func NewQueue(cache db.Cache) *Queue {
	return &Queue{cache: cache}
}
