// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package markdown

import (
	"fmt"
	"io"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
)

// SinkWriter is the interface every sink adapter must implement.
// A SinkWriter converts an [ast.Page] into the target format and writes the
// result to w.
//
// Each implementation is expected to:
//   - Embed block IDs in the target format's native metadata field
//     (HTML comment for markdown, ac:macro for Confluence, external_id for Notion).
//   - Preserve human-edited blocks by reading back existing content and
//     applying block-level reconciliation before writing.
type SinkWriter interface {
	WritePage(w io.Writer, page ast.Page) error
}

// MarkdownWriter is the markdown sink adapter. It is the reference
// implementation of SinkWriter and the only fully implemented adapter in D.2.
// Confluence and Notion adapters ship in A1.P4.
type MarkdownWriter struct{}

// WritePage implements [SinkWriter] using [Write].
func (MarkdownWriter) WritePage(w io.Writer, page ast.Page) error {
	return Write(w, page)
}

// confluenceWriter is the Confluence storage-XHTML sink adapter.
//
// TODO(A1.P4): implement
// Block IDs must be preserved as ac:macro parameters.
// Pages are reconciled by external_id stored in Confluence metadata.
// The managed-page marker must be emitted as a Confluence info macro.
type confluenceWriter struct{}

// WritePage is a stub. It returns an error to prevent accidental use before
// the full implementation ships in A1.P4.
func (confluenceWriter) WritePage(_ io.Writer, _ ast.Page) error {
	return errNotImplemented("confluenceWriter", "A1.P4")
}

// notionWriter is the Notion blocks sink adapter.
//
// TODO(A1.P4): implement
// Block IDs must be preserved as the external_id property on each block.
// Pages are reconciled by a page-level external_id property.
// The managed-page marker must be emitted as a Notion callout block.
type notionWriter struct{}

// WritePage is a stub. It returns an error to prevent accidental use before
// the full implementation ships in A1.P4.
func (notionWriter) WritePage(_ io.Writer, _ ast.Page) error {
	return errNotImplemented("notionWriter", "A1.P4")
}

// errNotImplemented returns a descriptive error for unimplemented sink adapters.
func errNotImplemented(adapter, workstream string) error {
	return fmt.Errorf("%s: not implemented — see %s", adapter, workstream)
}

// Compile-time interface checks.
var (
	_ SinkWriter = MarkdownWriter{}
	_ SinkWriter = confluenceWriter{}
	_ SinkWriter = notionWriter{}
)
