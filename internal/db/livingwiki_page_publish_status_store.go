// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// LivingWikiPagePublishStatusStore persists per-page, per-sink dispatch state
// for the Living Wiki cold-start runner. Each row in lw_page_publish_status
// represents one (repo_id, page_id, sink_kind, integration_name) tuple.
//
// API shape — two methods, NOT one (CR9):
//
//   SetReady   — called after a successful sink dispatch. Writes status='ready'
//                along with content_fingerprint, has_stubs, stub_target_page_ids,
//                and fixup_status. This is the ONLY path that writes the fingerprint.
//
//   SetNonReady — called for generating/failed/failed_fixup transitions. Writes
//                 ONLY status + error_msg. Does NOT touch content_fingerprint,
//                 has_stubs, stub_target_page_ids, or fixup_status. SurrealDB's
//                 UPSERT-without-clause leaves unmentioned fields unchanged on
//                 existing rows; for new rows the migration DEFAULT values apply.
//                 This enforces LD-7's preserve-on-failure promise via API shape:
//                 a failed dispatch cannot erase the last-known-good fingerprint.
//
// Plan: thoughts/shared/plans/2026-04-29-livingwiki-incremental-publish-redesign.md
// — CR4 (3-way smart-resume, stub fields), CR9 (type::thing UPSERT, split API).

package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	surrealdb "github.com/surrealdb/surrealdb.go"

	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// LivingWikiPagePublishStatusStore persists per-page publish state for the
// Living Wiki stream-dispatch feature.
type LivingWikiPagePublishStatusStore struct {
	client *SurrealDB
}

// NewLivingWikiPagePublishStatusStore creates a store backed by the given client.
func NewLivingWikiPagePublishStatusStore(client *SurrealDB) *LivingWikiPagePublishStatusStore {
	return &LivingWikiPagePublishStatusStore{client: client}
}

// Compile-time interface check.
var _ livingwiki.PagePublishStatusStore = (*LivingWikiPagePublishStatusStore)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// SurrealDB DTO
// ─────────────────────────────────────────────────────────────────────────────

type surrealPagePublishStatus struct {
	RepoID             string      `json:"repo_id"`
	PageID             string      `json:"page_id"`
	SinkKind           string      `json:"sink_kind"`
	IntegrationName    string      `json:"integration_name"`
	Status             string      `json:"status"`
	ErrorMsg           string      `json:"error_msg"`
	ContentFingerprint string      `json:"content_fingerprint"`
	HasStubs           bool        `json:"has_stubs"`
	StubTargetPageIDs  []string    `json:"stub_target_page_ids"`
	FixupStatus        string      `json:"fixup_status"`
	UpdatedAt          surrealTime `json:"updated_at"`
}

func (r surrealPagePublishStatus) toRow() livingwiki.PagePublishStatusRow {
	stubs := r.StubTargetPageIDs
	if stubs == nil {
		stubs = []string{}
	}
	return livingwiki.PagePublishStatusRow{
		RepoID:             r.RepoID,
		PageID:             r.PageID,
		SinkKind:           r.SinkKind,
		IntegrationName:    r.IntegrationName,
		Status:             r.Status,
		ErrorMsg:           r.ErrorMsg,
		ContentFingerprint: r.ContentFingerprint,
		HasStubs:           r.HasStubs,
		StubTargetPageIDs:  stubs,
		FixupStatus:        r.FixupStatus,
		UpdatedAt:          r.UpdatedAt.Time,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PagePublishStatusStore interface implementation
// ─────────────────────────────────────────────────────────────────────────────

// SetReady records a successful sink dispatch. This is the only method that
// writes content_fingerprint, has_stubs, stub_target_page_ids, and fixup_status
// (CR9: only SetReady touches these fields; SetNonReady preserves them).
func (s *LivingWikiPagePublishStatusStore) SetReady(ctx context.Context, args livingwiki.SetReadyArgs) error {
	if args.IntegrationName == "" {
		return fmt.Errorf("lw_page_publish_status: integration_name must not be empty (CR9)")
	}
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("lw_page_publish_status: database not connected")
	}
	fixupStatus := args.FixupStatus
	if fixupStatus == "" {
		if args.HasStubs {
			fixupStatus = livingwiki.FixupStatusPending
		} else {
			fixupStatus = livingwiki.FixupStatusNone
		}
	}
	stubIDs := args.StubTargetIDs
	if stubIDs == nil {
		stubIDs = []string{}
	}
	sql := `UPSERT type::thing('lw_page_publish_status', [$repo_id, $page_id, $sink_kind, $integration_name]) SET
		repo_id              = $repo_id,
		page_id              = $page_id,
		sink_kind            = $sink_kind,
		integration_name     = $integration_name,
		status               = 'ready',
		error_msg            = '',
		content_fingerprint  = $content_fingerprint,
		has_stubs            = $has_stubs,
		stub_target_page_ids = $stub_target_page_ids,
		fixup_status         = $fixup_status,
		updated_at           = time::now()`
	_, err := surrealdb.Query[interface{}](ctx, db, sql, map[string]any{
		"repo_id":              args.RepoID,
		"page_id":              args.PageID,
		"sink_kind":            args.SinkKind,
		"integration_name":     args.IntegrationName,
		"content_fingerprint":  args.Fingerprint,
		"has_stubs":            args.HasStubs,
		"stub_target_page_ids": stubIDs,
		"fixup_status":         fixupStatus,
	})
	if err != nil {
		return fmt.Errorf("lw_page_publish_status SetReady: %w", err)
	}
	return nil
}

// SetNonReady records a non-ready status transition (generating, failed,
// failed_fixup). Does NOT touch content_fingerprint, has_stubs,
// stub_target_page_ids, or fixup_status (CR9: preserve-on-failure contract).
func (s *LivingWikiPagePublishStatusStore) SetNonReady(ctx context.Context, args livingwiki.SetNonReadyArgs) error {
	if args.IntegrationName == "" {
		return fmt.Errorf("lw_page_publish_status: integration_name must not be empty (CR9)")
	}
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("lw_page_publish_status: database not connected")
	}
	// Note: no assignments to content_fingerprint, has_stubs, stub_target_page_ids,
	// fixup_status. SurrealDB UPSERT-without-clause leaves unmentioned fields
	// unchanged on existing rows; for new rows the migration DEFAULT values apply:
	//   content_fingerprint = '', has_stubs = false, stub_target_page_ids = [],
	//   fixup_status = 'none'.
	sql := `UPSERT type::thing('lw_page_publish_status', [$repo_id, $page_id, $sink_kind, $integration_name]) SET
		repo_id          = $repo_id,
		page_id          = $page_id,
		sink_kind        = $sink_kind,
		integration_name = $integration_name,
		status           = $status,
		error_msg        = $error_msg,
		updated_at       = time::now()`
	_, err := surrealdb.Query[interface{}](ctx, db, sql, map[string]any{
		"repo_id":          args.RepoID,
		"page_id":          args.PageID,
		"sink_kind":        args.SinkKind,
		"integration_name": args.IntegrationName,
		"status":           args.Status,
		"error_msg":        args.ErrorMsg,
	})
	if err != nil {
		return fmt.Errorf("lw_page_publish_status SetNonReady: %w", err)
	}
	return nil
}

// LoadFingerprints loads all publish-status rows for the given repoID and
// returns a map keyed by pageID → sinkKey → row. Used by smart-resume to
// evaluate per-page, per-sink fingerprint staleness and fixup state (CR4, LD-7).
//
// sinkKey is formatted as "<sink_kind>/<integration_name>".
func (s *LivingWikiPagePublishStatusStore) LoadFingerprints(ctx context.Context, repoID string) (map[string]map[string]livingwiki.PagePublishStatusRow, error) {
	db := s.client.DB()
	if db == nil {
		return map[string]map[string]livingwiki.PagePublishStatusRow{}, nil
	}
	sql := `SELECT * FROM lw_page_publish_status WHERE repo_id = $repo_id`
	rows, err := queryOne[[]surrealPagePublishStatus](ctx, db, sql, map[string]any{
		"repo_id": repoID,
	})
	if err != nil {
		// queryOne returns an error on empty result; treat as empty for smart-resume.
		slog.Debug("lw_page_publish_status: LoadFingerprints empty or error",
			"repo_id", repoID, "error", err)
		return map[string]map[string]livingwiki.PagePublishStatusRow{}, nil
	}
	result := make(map[string]map[string]livingwiki.PagePublishStatusRow, len(rows))
	for _, r := range rows {
		if _, ok := result[r.PageID]; !ok {
			result[r.PageID] = make(map[string]livingwiki.PagePublishStatusRow)
		}
		sinkKey := r.SinkKind + "/" + r.IntegrationName
		result[r.PageID][sinkKey] = r.toRow()
	}
	return result, nil
}

// ListByRepo returns all publish-status rows for the given repoID. Used by the
// livingWikiPublishStatus GraphQL query (Phase 1 schema surface, groundwork for
// Phase 2's index page).
func (s *LivingWikiPagePublishStatusStore) ListByRepo(ctx context.Context, repoID string) ([]livingwiki.PagePublishStatusRow, error) {
	db := s.client.DB()
	if db == nil {
		return nil, nil
	}
	sql := `SELECT * FROM lw_page_publish_status WHERE repo_id = $repo_id ORDER BY updated_at DESC`
	rows, err := queryOne[[]surrealPagePublishStatus](ctx, db, sql, map[string]any{
		"repo_id": repoID,
	})
	if err != nil {
		return nil, nil // empty is fine
	}
	result := make([]livingwiki.PagePublishStatusRow, 0, len(rows))
	for _, r := range rows {
		result = append(result, r.toRow())
	}
	return result, nil
}

// UpdateFixupStatus updates only the fixup_status (and optionally has_stubs) for
// a specific (repo_id, page_id, sink_kind, integration_name) row. Used by Phase 3's
// fix-up pass after successfully re-rendering stub pages.
func (s *LivingWikiPagePublishStatusStore) UpdateFixupStatus(ctx context.Context, args livingwiki.UpdateFixupStatusArgs) error {
	if args.IntegrationName == "" {
		return fmt.Errorf("lw_page_publish_status: integration_name must not be empty")
	}
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("lw_page_publish_status: database not connected")
	}
	sql := `UPDATE type::thing('lw_page_publish_status', [$repo_id, $page_id, $sink_kind, $integration_name]) SET
		fixup_status = $fixup_status,
		has_stubs    = $has_stubs,
		updated_at   = time::now()`
	_, err := surrealdb.Query[interface{}](ctx, db, sql, map[string]any{
		"repo_id":          args.RepoID,
		"page_id":          args.PageID,
		"sink_kind":        args.SinkKind,
		"integration_name": args.IntegrationName,
		"fixup_status":     args.FixupStatus,
		"has_stubs":        args.HasStubs,
	})
	if err != nil {
		return fmt.Errorf("lw_page_publish_status UpdateFixupStatus: %w", err)
	}
	return nil
}

// unused import guard for time; used by surrealPagePublishStatus.UpdatedAt.
var _ = time.Now
