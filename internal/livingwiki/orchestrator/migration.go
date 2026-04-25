// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// This file implements the A1.P6 page-level move and rename machinery:
//
//   - [TaxonomyEntry] — one entry in a page taxonomy snapshot.
//   - [MoveDetector] — compares old vs new taxonomy and emits [ast.BlockMigration] ops.
//   - [RepoRenameWriter] — interface for git-sink renames.
//   - [APISinkWriter] — interface for API-sink (Confluence/Notion) page moves.
//   - [Orchestrator.ApplyMigrations] — applies a list of [ast.BlockMigration] ops.

package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
)

// ─────────────────────────────────────────────────────────────────────────────
// Taxonomy snapshot
// ─────────────────────────────────────────────────────────────────────────────

// TaxonomyEntry is one page's position in the wiki taxonomy. The [MoveDetector]
// compares two snapshots (old and new) to detect moves and renames.
type TaxonomyEntry struct {
	// PageID is the stable page ID (e.g. "arch.internal.auth").
	PageID string

	// Path is the file path in the git sink (e.g. "wiki/arch.internal.auth.md").
	// Empty for API sinks.
	Path string

	// PackagePath is the source-package path this page documents
	// (e.g. "internal/auth"). Used to detect package renames.
	PackagePath string

	// Title is the page title as it appears in the sink (e.g. "Authentication").
	Title string
}

// ─────────────────────────────────────────────────────────────────────────────
// MoveDetector
// ─────────────────────────────────────────────────────────────────────────────

// MoveDetector compares old and new page taxonomies to identify pages that
// should move because the underlying package was renamed.
//
// Detection rules:
//  1. A page's PackagePath changed (e.g. internal/auth → internal/identity).
//     This is a package rename: emit MigrationRenamed with the old and new IDs.
//  2. A page was in the old taxonomy but is absent from the new one.
//     This is a page deletion — not currently emitted as a migration (out of scope).
//  3. A page is new in the new taxonomy — not a migration.
//
// The stable block IDs in each page are preserved across the move.
type MoveDetector struct{}

// NewMoveDetector returns a zero-allocation MoveDetector.
func NewMoveDetector() *MoveDetector { return &MoveDetector{} }

// Detect compares oldTaxonomy and newTaxonomy and returns any block migrations
// that are needed. Each returned [ast.BlockMigration] has Op == MigrationRenamed
// and carries the old page ID (FromID converted to BlockID form for
// compatibility with the BlockMigration type) and the new page ID.
//
// In practice, a page rename is expressed as a single BlockMigration with:
//   - FromID: the old page ID (cast to BlockID)
//   - ToIDs:  [new page ID] (cast to BlockID)
//   - Rationale: human-readable explanation of why the move happened.
func (d *MoveDetector) Detect(oldTaxonomy, newTaxonomy []TaxonomyEntry) []ast.BlockMigration {
	// Build lookup of old entries by PackagePath.
	oldByPkg := make(map[string]TaxonomyEntry, len(oldTaxonomy))
	oldByPageID := make(map[string]TaxonomyEntry, len(oldTaxonomy))
	for _, e := range oldTaxonomy {
		oldByPkg[e.PackagePath] = e
		oldByPageID[e.PageID] = e
	}

	// Build lookup of new entries by PackagePath.
	newByPkg := make(map[string]TaxonomyEntry, len(newTaxonomy))
	for _, e := range newTaxonomy {
		newByPkg[e.PackagePath] = e
	}

	var migrations []ast.BlockMigration

	for _, newEntry := range newTaxonomy {
		// Skip pages that existed in the old taxonomy under the same page ID.
		if _, sameID := oldByPageID[newEntry.PageID]; sameID {
			continue
		}
		// Check whether a page at a different package path has been renamed.
		// We look for an old entry whose PageID derived from the package path
		// is different from the new one, meaning the package was renamed.
		if newEntry.PackagePath == "" {
			continue
		}
		// Find the old entry for this package path (if any).
		oldEntry, hadOldPkg := oldByPkg[newEntry.PackagePath]
		if !hadOldPkg {
			// Brand-new package — not a rename.
			continue
		}
		if oldEntry.PageID == newEntry.PageID {
			// Same page ID — no rename.
			continue
		}
		// Old entry at same package path but different page ID = rename.
		migration := ast.BlockMigration{
			Op:      ast.MigrationRenamed,
			FromID:  ast.BlockID(oldEntry.PageID),
			ToIDs:   []ast.BlockID{ast.BlockID(newEntry.PageID)},
			Rationale: fmt.Sprintf("package %q was renamed; page %q moves to %q", newEntry.PackagePath, oldEntry.PageID, newEntry.PageID),
		}
		migrations = append(migrations, migration)
	}

	// Also detect pure package-path renames: same page ID base, different package path.
	// E.g. arch.internal.auth → arch.internal.identity when internal/auth → internal/identity.
	for _, oldEntry := range oldTaxonomy {
		if _, stillExists := newByPkg[oldEntry.PackagePath]; stillExists {
			continue // package path unchanged — not a rename
		}
		// Old package path is gone. Find a new entry that most closely matches.
		// Heuristic: same page ID prefix (everything before the last dot-segment).
		prefix := pageIDPrefix(oldEntry.PageID)
		for _, newEntry := range newTaxonomy {
			if pageIDPrefix(newEntry.PageID) != prefix {
				continue
			}
			if newEntry.PageID == oldEntry.PageID {
				continue
			}
			if _, alreadyMapped := newByPkg[oldEntry.PackagePath]; alreadyMapped {
				continue
			}
			// Found a candidate: same prefix, different suffix, old package gone.
			migration := ast.BlockMigration{
				Op:     ast.MigrationRenamed,
				FromID: ast.BlockID(oldEntry.PageID),
				ToIDs:  []ast.BlockID{ast.BlockID(newEntry.PageID)},
				Rationale: fmt.Sprintf("package path %q no longer exists; page %q is likely %q", oldEntry.PackagePath, oldEntry.PageID, newEntry.PageID),
			}
			migrations = append(migrations, migration)
			break
		}
	}

	return dedupeMigrations(migrations)
}

// pageIDPrefix returns everything before the last dot-delimited segment.
// "arch.internal.auth" → "arch.internal"
// "arch.auth" → "arch"
// "auth" → ""
func pageIDPrefix(id string) string {
	idx := strings.LastIndex(id, ".")
	if idx < 0 {
		return ""
	}
	return id[:idx]
}

// dedupeMigrations removes migrations with the same FromID (keeps first occurrence).
func dedupeMigrations(migrations []ast.BlockMigration) []ast.BlockMigration {
	seen := make(map[ast.BlockID]bool, len(migrations))
	out := migrations[:0]
	for _, m := range migrations {
		if seen[m.FromID] {
			continue
		}
		seen[m.FromID] = true
		out = append(out, m)
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Sink writer interfaces for migrations
// ─────────────────────────────────────────────────────────────────────────────

// RepoRenameWriter renames a file in a git-based sink within a single commit.
// The rename preserves block IDs (the content is written verbatim).
type RepoRenameWriter interface {
	// RenameFile renames oldPath to newPath in a single commit with the given message.
	// content is the file content to write at newPath after the rename.
	RenameFile(ctx context.Context, oldPath, newPath string, content []byte, commitMessage string) error
}

// APISinkWriter handles page-level moves in API sinks (Confluence, Notion).
// It deletes the old page and creates a new one with the same block IDs.
type APISinkWriter interface {
	// MovePage deletes oldPageID and creates a new page at newPageID with the
	// given content and a backlink to the old page ID for existing external links.
	MovePage(ctx context.Context, oldPageID, newPageID string, content []byte) error
}

// MigrationApplyResult records the outcome of applying one migration.
type MigrationApplyResult struct {
	// Migration is the migration that was applied.
	Migration ast.BlockMigration
	// Applied is true when the migration was successfully applied.
	Applied bool
	// Err is non-nil when the migration failed.
	Err error
}

// ApplyMigrations applies a list of block (page-level) migrations to the sinks.
//
// For git sinks: calls RepoRenameWriter.RenameFile to emit git mv + content
// update in the same commit. Block IDs are preserved (content written verbatim).
//
// For API sinks: calls APISinkWriter.MovePage to delete the old page and create
// a new one at the new path, with a backlink and preserved block IDs.
//
// The migration log entry is stored via MigrationLog parameter.
//
// Non-fatal errors per migration are collected and returned; the caller should
// inspect Results for per-migration status.
func (o *Orchestrator) ApplyMigrations(
	ctx context.Context,
	repoID string,
	migrations []ast.BlockMigration,
	log *ast.MigrationLog,
	gitWriter RepoRenameWriter,
	apiWriter APISinkWriter,
) ([]MigrationApplyResult, error) {
	results := make([]MigrationApplyResult, 0, len(migrations))

	for _, m := range migrations {
		result := MigrationApplyResult{Migration: m}

		if len(m.ToIDs) == 0 {
			result.Err = fmt.Errorf("migration from %q has no ToIDs", m.FromID)
			results = append(results, result)
			continue
		}

		fromPageID := string(m.FromID)
		toPageID := string(m.ToIDs[0])

		// Load the canonical page at fromPageID.
		page, ok, err := o.store.GetCanonical(ctx, repoID, fromPageID)
		if err != nil || !ok {
			result.Err = fmt.Errorf("canonical page %q not found for migration: %w", fromPageID, err)
			results = append(results, result)
			continue
		}

		// Render the page content — used for both git and API moves.
		rendered, renderErr := renderPage(page)
		if renderErr != nil {
			result.Err = fmt.Errorf("rendering page %q for migration: %w", fromPageID, renderErr)
			results = append(results, result)
			continue
		}

		var applyErr error

		// Git sink path: rename the file.
		if gitWriter != nil {
			oldPath := wikiFilePath(fromPageID)
			newPath := wikiFilePath(toPageID)
			commitMsg := fmt.Sprintf("wiki: rename %s → %s (%s)", fromPageID, toPageID, m.Rationale)
			applyErr = gitWriter.RenameFile(ctx, oldPath, newPath, rendered, commitMsg)
		}

		// API sink path: delete old page, create new.
		if applyErr == nil && apiWriter != nil {
			applyErr = apiWriter.MovePage(ctx, fromPageID, toPageID, rendered)
		}

		if applyErr != nil {
			result.Err = applyErr
			results = append(results, result)
			continue
		}

		// Update canonical store: remove old ID, store under new ID.
		page.ID = toPageID
		if page.Manifest.PageID == fromPageID {
			page.Manifest.PageID = toPageID
		}
		if storeErr := o.store.SetCanonical(ctx, repoID, page); storeErr != nil {
			result.Err = fmt.Errorf("storing renamed page %q: %w", toPageID, storeErr)
			results = append(results, result)
			continue
		}
		if delErr := o.store.DeleteCanonical(ctx, repoID, fromPageID); delErr != nil {
			result.Err = fmt.Errorf("deleting old page %q after rename: %w", fromPageID, delErr)
			results = append(results, result)
			continue
		}

		// Record the migration in the log.
		if log != nil {
			log.Add(m)
		}

		result.Applied = true
		results = append(results, result)
	}

	return results, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// In-memory sink writer stubs for tests
// ─────────────────────────────────────────────────────────────────────────────

// MemoryRepoRenameWriter records rename operations for test inspection.
type MemoryRepoRenameWriter struct {
	Renames []RenameOp
}

// RenameOp is one recorded rename.
type RenameOp struct {
	OldPath string
	NewPath string
	Content []byte
	Message string
}

// Compile-time interface check.
var _ RepoRenameWriter = (*MemoryRepoRenameWriter)(nil)

func (m *MemoryRepoRenameWriter) RenameFile(_ context.Context, oldPath, newPath string, content []byte, commitMessage string) error {
	cp := make([]byte, len(content))
	copy(cp, content)
	m.Renames = append(m.Renames, RenameOp{OldPath: oldPath, NewPath: newPath, Content: cp, Message: commitMessage})
	return nil
}

// MemoryAPISinkWriter records move operations for test inspection.
type MemoryAPISinkWriter struct {
	Moves []PageMoveOp
}

// PageMoveOp is one recorded API page move.
type PageMoveOp struct {
	OldPageID string
	NewPageID string
	Content   []byte
}

// Compile-time interface check.
var _ APISinkWriter = (*MemoryAPISinkWriter)(nil)

func (m *MemoryAPISinkWriter) MovePage(_ context.Context, oldPageID, newPageID string, content []byte) error {
	cp := make([]byte, len(content))
	copy(cp, content)
	m.Moves = append(m.Moves, PageMoveOp{OldPageID: oldPageID, NewPageID: newPageID, Content: cp})
	return nil
}
