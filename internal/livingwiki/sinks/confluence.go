// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package sinks

import (
	"context"
	"fmt"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/credentials"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/markdown"
)

// ConfluenceSinkWriter implements SinkWriter for the Confluence Cloud sink.
//
// It wraps an HTTPConfluenceClient with a credential snapshot, binding the
// client's per-call Snapshot parameter at construction time so the SinkWriter
// interface (which has no Snapshot parameter) can be satisfied.
//
// The ConfluenceWriter underneath performs the full read-diff-write reconciliation
// cycle, preserving human-edited blocks.
//
// ConfluenceSinkWriter also implements [OrphanCleaner] so the dispatch layer
// can delete Confluence pages that were published in a previous job but are
// absent from the current taxonomy.
type ConfluenceSinkWriter struct {
	writer *markdown.ConfluenceWriter
	client markdown.ConfluenceClient // kept for orphan-cleanup operations
	kind   markdown.SinkKind
}

// snapshotBoundConfluenceClient bridges HTTPConfluenceClient (which takes a
// Snapshot per call) to the ConfluenceClient interface (which does not). The
// Snapshot is fixed at construction time — one snapshot per job, capturing
// credentials at job start per the at-most-one-rotation-per-job invariant.
type snapshotBoundConfluenceClient struct {
	client   *markdown.HTTPConfluenceClient
	snapshot credentials.Snapshot
}

func (s *snapshotBoundConfluenceClient) GetPage(ctx context.Context, externalID string) ([]byte, markdown.ConfluenceProperties, error) {
	return s.client.GetPage(ctx, s.snapshot, externalID)
}

func (s *snapshotBoundConfluenceClient) UpsertPage(ctx context.Context, externalID string, xhtml []byte, metadata markdown.ConfluenceProperties) error {
	return s.client.UpsertPage(ctx, s.snapshot, externalID, xhtml, metadata)
}

func (s *snapshotBoundConfluenceClient) GetBlockByExternalID(ctx context.Context, pageExternalID string, blockExternalID ast.BlockID) ([]byte, bool, error) {
	return s.client.GetBlockByExternalID(ctx, s.snapshot, pageExternalID, blockExternalID)
}

func (s *snapshotBoundConfluenceClient) ListPagesByExternalIDPrefix(ctx context.Context, prefix string) ([]string, error) {
	return s.client.ListPagesByExternalIDPrefix(ctx, s.snapshot, prefix)
}

func (s *snapshotBoundConfluenceClient) DeletePage(ctx context.Context, externalID string) error {
	return s.client.DeletePage(ctx, s.snapshot, externalID)
}

func (s *snapshotBoundConfluenceClient) EnsurePage(ctx context.Context, externalID, title, parentExternalID string, body []byte) (string, error) {
	return s.client.EnsurePage(ctx, s.snapshot, externalID, title, parentExternalID, body)
}

// NewConfluenceSinkWriter constructs a ConfluenceSinkWriter.
//
// site is the Atlassian Cloud subdomain (e.g. "mycompany").
// spaceKey is the Confluence space key (e.g. "ENG").
// parentPageID is the optional parent page ID for new pages (empty = space root).
// repoID is the SourceBridge repository ID used to derive hierarchy page IDs.
// repoName is the human-readable repository name used as the root page title.
// snapshot is the per-job credential snapshot.
func NewConfluenceSinkWriter(site, spaceKey, parentPageID, repoID, repoName string, snapshot credentials.Snapshot) *ConfluenceSinkWriter {
	httpClient := markdown.NewHTTPConfluenceClient(markdown.ConfluenceHTTPConfig{
		Site:         site,
		SpaceKey:     spaceKey,
		ParentPageID: parentPageID,
	})
	bound := &snapshotBoundConfluenceClient{
		client:   httpClient,
		snapshot: snapshot,
	}
	writer := markdown.NewConfluenceWriter(bound, markdown.ConfluenceWriterConfig{
		SpaceKey:     spaceKey,
		ParentPageID: parentPageID,
		RepoID:       repoID,
		RepoName:     repoName,
	})
	return &ConfluenceSinkWriter{
		writer: writer,
		client: bound,
		kind:   markdown.SinkKindConfluence,
	}
}

// NewConfluenceSinkWriterFromClient constructs a ConfluenceSinkWriter from an
// existing ConfluenceClient. Used in tests to inject a fake or in-memory client
// in place of the real HTTPConfluenceClient.
func NewConfluenceSinkWriterFromClient(client markdown.ConfluenceClient, cfg markdown.ConfluenceWriterConfig) *ConfluenceSinkWriter {
	return &ConfluenceSinkWriter{
		writer: markdown.NewConfluenceWriter(client, cfg),
		client: client,
		kind:   markdown.SinkKindConfluence,
	}
}

// ListPagesByExternalIDPrefix implements [OrphanCleaner] by delegating to the
// underlying ConfluenceClient.
func (c *ConfluenceSinkWriter) ListPagesByExternalIDPrefix(ctx context.Context, prefix string) ([]string, error) {
	return c.client.ListPagesByExternalIDPrefix(ctx, prefix)
}

// DeletePage implements [OrphanCleaner] by delegating to the underlying
// ConfluenceClient.
func (c *ConfluenceSinkWriter) DeletePage(ctx context.Context, externalID string) error {
	return c.client.DeletePage(ctx, externalID)
}

// Kind implements SinkWriter.
func (c *ConfluenceSinkWriter) Kind() markdown.SinkKind {
	return c.kind
}

// WritePage implements SinkWriter by delegating to ConfluenceWriter.WritePage,
// which performs the full read-diff-write reconciliation cycle.
func (c *ConfluenceSinkWriter) WritePage(ctx context.Context, page ast.Page) error {
	if err := c.writer.WritePage(ctx, page); err != nil {
		return fmt.Errorf("confluence sink: %w", err)
	}
	return nil
}

// Compile-time interface check.
var _ SinkWriter = (*ConfluenceSinkWriter)(nil)
