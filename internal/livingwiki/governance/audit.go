// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package governance

import (
	"context"
	"sync"
	"time"
)

// AuditEntry records one governance event: a block promotion or a sync-PR
// disposition. Every call to [PromoteToCanonical] and [ResolveSyncPR] appends
// one or more entries to the provided [AuditLog].
//
// The audit trail feeds the SourceBridge compliance change-management story
// and is surfaced in the UI's audit log view.
type AuditEntry struct {
	// BlockID is the stable ID of the block that was acted on.
	BlockID string

	// SourceSink is the integration ID of the sink where the edit originated
	// (e.g. "confluence-acme-space").
	SourceSink string

	// SourceUser is the user identifier from the sink who made the edit.
	// May be empty when the source user is not known.
	SourceUser string

	// TargetCanonicalState is the resulting owner state in the canonical AST
	// after the action: "human-edited", "unchanged", or "generated".
	TargetCanonicalState string

	// Timestamp is when the governance action was recorded.
	Timestamp time.Time

	// Reviewer is the engineer who reviewed the sync-PR, if any.
	// Empty for non-PR promotions and force-overwrites.
	Reviewer string

	// Decision is a short label describing the action:
	//   "promote_to_canonical" — direct promotion via promote_to_canonical policy
	//   "sync_pr_merge"        — sync-PR was merged
	//   "sync_pr_reject"       — sync-PR was rejected; edit stays local_to_sink
	//   "sync_pr_force_overwrite" — admin discarded the sink edit
	Decision string
}

// AuditFilter limits the results of [AuditLog.Query].
// Zero-value fields are treated as "no filter" (match everything).
type AuditFilter struct {
	// BlockID filters to entries for a specific block.
	BlockID string

	// SourceSink filters to entries from a specific sink.
	SourceSink string

	// Decision filters to entries with a specific decision label.
	Decision string

	// Since filters to entries at or after this time. Zero means no lower bound.
	Since time.Time

	// Until filters to entries before or at this time. Zero means no upper bound.
	Until time.Time
}

// AuditLog is the append-only store for governance audit entries.
// Implementations must be safe for concurrent use.
//
// Persistence is caller-managed. The in-memory implementation [MemoryAuditLog]
// is suitable for tests. Production wires in a database-backed implementation
// during the A1 phases.
type AuditLog interface {
	// Append records one governance event. Returns an error only when the
	// implementation encounters a storage failure; callers should treat audit
	// failures as operational alerts but must not silently ignore them.
	Append(ctx context.Context, entry AuditEntry) error

	// Query returns all entries matching the filter. Entries are returned in
	// ascending timestamp order. An empty filter returns all entries.
	Query(ctx context.Context, filter AuditFilter) ([]AuditEntry, error)
}

// MemoryAuditLog is the in-memory implementation of [AuditLog].
// It is intended for tests and development; it does not persist across process
// restarts.
type MemoryAuditLog struct {
	mu      sync.RWMutex
	entries []AuditEntry
}

// NewMemoryAuditLog creates an empty in-memory audit log.
func NewMemoryAuditLog() *MemoryAuditLog {
	return &MemoryAuditLog{}
}

// Append implements [AuditLog]. It is safe for concurrent use.
func (m *MemoryAuditLog) Append(_ context.Context, entry AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
	return nil
}

// Query implements [AuditLog]. It returns entries matching the filter in
// ascending timestamp order. The returned slice is a copy — mutating it does
// not affect the log.
func (m *MemoryAuditLog) Query(_ context.Context, f AuditFilter) ([]AuditEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []AuditEntry
	for _, e := range m.entries {
		if f.BlockID != "" && e.BlockID != f.BlockID {
			continue
		}
		if f.SourceSink != "" && e.SourceSink != f.SourceSink {
			continue
		}
		if f.Decision != "" && e.Decision != f.Decision {
			continue
		}
		if !f.Since.IsZero() && e.Timestamp.Before(f.Since) {
			continue
		}
		if !f.Until.IsZero() && e.Timestamp.After(f.Until) {
			continue
		}
		result = append(result, e)
	}
	return result, nil
}

// Compile-time interface check.
var _ AuditLog = (*MemoryAuditLog)(nil)
