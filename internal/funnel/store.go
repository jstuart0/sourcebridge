// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package funnel provides persistence for first-run funnel and
// workflow-adoption events.  Events are stored locally in the operator's
// own SurrealDB instance; no data is sent to an external service from
// this package.
package funnel

import (
	"context"
	"time"

	surrealdb "github.com/surrealdb/surrealdb.go"

	"github.com/sourcebridge/sourcebridge/internal/db"
)

// FunnelEvent is the domain type for a single funnel or adoption event.
//
// UserID and TenantID are pointer types because server-side bus events
// have no authentication context — they originate from background goroutines
// that run outside an HTTP request.  Frontend-sourced events carry both.
//
// ArtifactID, RequirementID, SymbolID, and ArtifactType are explicit top-level
// fields (not buried in Metadata) so they can be indexed and queried without
// JSON extraction.  Metadata holds freeform per-event payload; the write layer
// is responsible for scrubbing PII before populating this field.
type FunnelEvent struct {
	Event         string
	Source        string
	UserID        *string
	TenantID      *string
	RepoID        string
	ArtifactID    string
	RequirementID string
	SymbolID      string
	ArtifactType  string
	Metadata      map[string]any
	OccurredAt    time.Time
}

// FunnelStore is the persistence interface for funnel events.
// Implementations must be safe for concurrent use.
type FunnelStore interface {
	RecordEvent(ctx context.Context, ev FunnelEvent) error
}

// SurrealFunnelStore persists funnel events to the ca_funnel_event table
// in SurrealDB (provisioned by migration 056).
type SurrealFunnelStore struct {
	client *db.SurrealDB
}

// NewSurrealFunnelStore creates a SurrealFunnelStore backed by client.
// client must already be connected before RecordEvent is called.
func NewSurrealFunnelStore(client *db.SurrealDB) *SurrealFunnelStore {
	return &SurrealFunnelStore{client: client}
}

// RecordEvent inserts a single funnel event into ca_funnel_event.
// A nil receiver is treated as a no-op so callers that hold a nil store
// do not need to guard every call site.
func (s *SurrealFunnelStore) RecordEvent(ctx context.Context, ev FunnelEvent) error {
	if s == nil {
		return nil
	}

	rawDB := s.client.DB()
	if rawDB == nil {
		// Embedded / not-yet-connected mode: silently discard.
		return nil
	}

	occurredAt := ev.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}

	sql := `CREATE ca_funnel_event SET
		event         = $event,
		source        = $source,
		user_id       = $user_id,
		tenant_id     = $tenant_id,
		repo_id       = $repo_id,
		artifact_id   = $artifact_id,
		requirement_id = $requirement_id,
		symbol_id     = $symbol_id,
		artifact_type = $artifact_type,
		metadata      = $metadata,
		occurred_at   = $occurred_at`

	vars := map[string]any{
		"event":          ev.Event,
		"source":         ev.Source,
		"user_id":        ev.UserID,
		"tenant_id":      ev.TenantID,
		"repo_id":        ev.RepoID,
		"artifact_id":    ev.ArtifactID,
		"requirement_id": ev.RequirementID,
		"symbol_id":      ev.SymbolID,
		"artifact_type":  ev.ArtifactType,
		"metadata":       ev.Metadata,
		"occurred_at":    occurredAt.Format(time.RFC3339Nano),
	}

	_, err := surrealdb.Query[any](ctx, rawDB, sql, vars)
	return err
}
