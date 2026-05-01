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

// NotionSinkWriter implements SinkWriter for the Notion sink.
//
// It wraps an HTTPNotionClient with a credential snapshot, binding the
// per-call Snapshot parameter at construction time so the SinkWriter interface
// can be satisfied.
type NotionSinkWriter struct {
	writer *markdown.NotionWriter
	kind   markdown.SinkKind
}

// snapshotBoundNotionClient bridges HTTPNotionClient (Snapshot per call) to
// the NotionClient interface (no Snapshot). Snapshot fixed at construction
// time — one per job, per the at-most-one-rotation-per-job invariant.
type snapshotBoundNotionClient struct {
	client   *markdown.HTTPNotionClient
	snapshot credentials.Snapshot
}

func (s *snapshotBoundNotionClient) GetPage(ctx context.Context, externalID string) ([]markdown.NotionBlock, markdown.NotionProperties, error) {
	return s.client.GetPage(ctx, s.snapshot, externalID)
}

func (s *snapshotBoundNotionClient) UpsertPage(ctx context.Context, externalID string, blocks []markdown.NotionBlock, properties markdown.NotionProperties) error {
	return s.client.UpsertPage(ctx, s.snapshot, externalID, blocks, properties)
}

func (s *snapshotBoundNotionClient) AppendBlocks(ctx context.Context, pageExternalID string, blocks []markdown.NotionBlock) error {
	return s.client.AppendBlocks(ctx, s.snapshot, pageExternalID, blocks)
}

func (s *snapshotBoundNotionClient) UpdateBlock(ctx context.Context, blockExternalID string, block markdown.NotionBlock) error {
	return s.client.UpdateBlock(ctx, s.snapshot, blockExternalID, block)
}

func (s *snapshotBoundNotionClient) DeleteBlock(ctx context.Context, blockExternalID string) error {
	return s.client.DeleteBlock(ctx, s.snapshot, blockExternalID)
}

// NewNotionSinkWriter constructs a NotionSinkWriter.
//
// databaseID is the Notion database ID that holds SourceBridge pages.
// Leave empty to fall back to title-based search.
// snapshot is the per-job credential snapshot.
func NewNotionSinkWriter(databaseID string, snapshot credentials.Snapshot) *NotionSinkWriter {
	httpClient := markdown.NewHTTPNotionClient(markdown.NotionHTTPConfig{
		DatabaseID: databaseID,
	})
	bound := &snapshotBoundNotionClient{
		client:   httpClient,
		snapshot: snapshot,
	}
	writer := markdown.NewNotionWriter(bound, markdown.NotionWriterConfig{})
	return &NotionSinkWriter{
		writer: writer,
		kind:   markdown.SinkKindNotion,
	}
}

// (newNotionSinkWriterFromClient used to live here as a test
// constructor accepting a pre-built NotionClient; the current notion-
// sink tests build the client + writer inline. Removed to satisfy
// lint.)

// Kind implements SinkWriter.
func (n *NotionSinkWriter) Kind() markdown.SinkKind {
	return n.kind
}

// WritePage implements SinkWriter by delegating to NotionWriter.WritePage.
func (n *NotionSinkWriter) WritePage(ctx context.Context, page ast.Page) error {
	if err := n.writer.WritePage(ctx, page); err != nil {
		return fmt.Errorf("notion sink: %w", err)
	}
	return nil
}

// Compile-time interface check.
var _ SinkWriter = (*NotionSinkWriter)(nil)
