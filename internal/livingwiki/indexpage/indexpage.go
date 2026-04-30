// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package indexpage builds the combined Living Wiki status index page (Phase 2
// of the incremental-publish redesign).
//
// The index page is a *meta-page* — it is NOT LLM-generated. It is a
// deterministic, template-free render of a snapshot of lw_page_publish_status
// rows, grouped by mode (Overview / Detailed / Repo-wide). It is published to
// every configured sink at run start (showing all pages as Pending), then
// refreshed every 10 page completions or 30 seconds, and once more at run end.
//
// RenderIndexPage is a pure function — no I/O — so it can be unit-tested
// without a database or sink connection.
package indexpage

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// IndexPageID returns the stable external ID for the combined Living Wiki
// index page for the given repository. The ID follows the existing SourceBridge
// synthetic-ID convention: <repoID>.__<purpose>__.
func IndexPageID(repoID string) string {
	return repoID + ".__index__"
}

// StatusIcon returns a short text symbol for a publish status value.
// Confluence renders these inline in the table cells.
func StatusIcon(status string) string {
	switch status {
	case "ready":
		return "✓"
	case "generating":
		return "⏳"
	case "failed", "failed_fixup":
		return "✗"
	default:
		// "pending" and any unrecognised status.
		return "·"
	}
}

// pageSection classifies a page ID into the section it belongs to.
type pageSection int

const (
	sectionOverview  pageSection = iota
	sectionDetailed  pageSection = iota
	sectionRepoWide  pageSection = iota
	sectionLegacy    pageSection = iota // arch.* pages from pre-D2 runs
	sectionIndex     pageSection = iota // the index page itself — never rendered
)

// classifyPageID maps a page external ID to its section.
// The repo-ID prefix (and the dot that separates it) is stripped before the
// mode-prefix check, matching the convention used by HumanizePageID.
func classifyPageID(pageID string) pageSection {
	// Strip leading repo-ID segment (UUID-like, contains 4 hyphens).
	rest := pageID
	if dotIdx := strings.Index(pageID, "."); dotIdx >= 0 {
		prefix := pageID[:dotIdx]
		if strings.Count(prefix, "-") == 4 {
			rest = pageID[dotIdx+1:]
		}
	}

	switch {
	case rest == "__index__":
		return sectionIndex
	case strings.HasPrefix(rest, "overview."):
		return sectionOverview
	case strings.HasPrefix(rest, "detail."):
		return sectionDetailed
	case strings.HasPrefix(rest, "arch."):
		return sectionLegacy
	case rest == "api_reference" || rest == "glossary" || rest == "system_overview":
		return sectionRepoWide
	default:
		// Synthetic hierarchy pages (__wiki_root__, __section__.architecture) and
		// any other synthetic IDs — lump into repo-wide so they are visible.
		return sectionRepoWide
	}
}

// humanizeSuffix converts the mode-suffix of a page ID into a human-readable
// label. For example "internal.api.middleware" → "internal/api/middleware".
func humanizeSuffix(rest string) string {
	return strings.ReplaceAll(rest, ".", "/")
}

// pageLabel returns a short human label for a page given its rest-ID (after
// stripping the repo-ID prefix).
func pageLabel(rest string) string {
	switch {
	case strings.HasPrefix(rest, "overview."):
		return humanizeSuffix(rest[len("overview."):])
	case strings.HasPrefix(rest, "detail."):
		return humanizeSuffix(rest[len("detail."):])
	case strings.HasPrefix(rest, "arch."):
		return humanizeSuffix(rest[len("arch."):])
	case rest == "api_reference":
		return "API Reference"
	case rest == "glossary":
		return "Glossary"
	case rest == "system_overview":
		return "System Overview"
	default:
		return rest
	}
}

// restOf strips the leading repo-ID UUID prefix from a page ID and returns
// everything after the first dot. Returns the original string unchanged when
// no UUID prefix is detected.
func restOf(pageID string) string {
	if dotIdx := strings.Index(pageID, "."); dotIdx >= 0 {
		prefix := pageID[:dotIdx]
		if strings.Count(prefix, "-") == 4 {
			return pageID[dotIdx+1:]
		}
	}
	return pageID
}

// plannedEntry is one row in the status table — populated from
// either a PlannedPage (for pages not yet in the status store) or from an
// existing PagePublishStatusRow.
type plannedEntry struct {
	pageID  string
	label   string
	status  string // "pending", "generating", "ready", "failed"
	section pageSection
}

// RenderIndexPage builds an ast.Page representing the combined Living Wiki
// index page for repoID. It is a pure function — no I/O.
//
// Parameters:
//   - repoID: the repository ID (used to construct the page ID and filter rows).
//   - plannedPageIDs: all page IDs from the current run's taxonomy (sorted or
//     unsorted — the renderer sorts them internally for stable output). The index
//     page's own ID is automatically excluded.
//   - statuses: all rows currently in lw_page_publish_status for this repo
//     (from PagePublishStatusStore.ListByRepo). Pages that appear in
//     plannedPageIDs but not in statuses are shown as Pending.
//   - now: the current wall-clock time stamped in the "Last updated" line.
//
// Sectioning rules (LD-10):
//   - Pages with IDs containing ".overview." → "Overview" section.
//   - Pages with IDs containing ".detail." → "Detailed" section.
//   - Pages with IDs for api_reference / glossary / system_overview → "Repo-wide" section.
//   - Pages with IDs containing ".arch." (legacy) → "Legacy" section (only shown when
//     such pages exist in the status store from prior pre-D2 runs).
//   - Sections with zero planned pages are omitted entirely.
//   - The index page's own ID is always excluded from the rendered list.
func RenderIndexPage(
	repoID string,
	plannedPageIDs []string,
	statuses []livingwiki.PagePublishStatusRow,
	now time.Time,
) ast.Page {
	indexID := IndexPageID(repoID)

	// Build a lookup: pageID → most-recent status string.
	// When a page has rows for multiple sinks, use the worst status:
	// failed > generating > pending > ready, so the user sees the most
	// actionable signal first.
	statusRank := map[string]int{
		"failed":       4,
		"failed_fixup": 4,
		"generating":   3,
		"pending":      2,
		"ready":        1,
		"":             0,
	}
	pageStatus := make(map[string]string, len(statuses))
	for _, row := range statuses {
		if row.PageID == indexID {
			continue // exclude the index page itself
		}
		cur, ok := pageStatus[row.PageID]
		if !ok || statusRank[row.Status] > statusRank[cur] {
			pageStatus[row.PageID] = row.Status
		}
	}

	// Build entries for every planned page (excluding the index page itself).
	entries := make([]plannedEntry, 0, len(plannedPageIDs))
	seenIDs := make(map[string]bool, len(plannedPageIDs))
	for _, pid := range plannedPageIDs {
		if pid == indexID {
			continue
		}
		seenIDs[pid] = true
		rest := restOf(pid)
		status, ok := pageStatus[pid]
		if !ok {
			status = "pending"
		}
		entries = append(entries, plannedEntry{
			pageID:  pid,
			label:   pageLabel(rest),
			status:  status,
			section: classifyPageID(pid),
		})
	}

	// Also include any status-store pages NOT in the planned set (e.g. legacy
	// arch.* pages from prior runs that are now orphans). These appear in the
	// "Legacy" section so the user can see them and knows they will be cleaned up.
	for _, row := range statuses {
		if row.PageID == indexID || seenIDs[row.PageID] {
			continue
		}
		sec := classifyPageID(row.PageID)
		if sec == sectionIndex {
			continue
		}
		rest := restOf(row.PageID)
		status, ok := pageStatus[row.PageID]
		if !ok {
			status = row.Status
		}
		entries = append(entries, plannedEntry{
			pageID:  row.PageID,
			label:   pageLabel(rest),
			status:  status,
			section: sec,
		})
		seenIDs[row.PageID] = true
	}

	// Stable sort: by section order, then alphabetically within a section.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].section != entries[j].section {
			return entries[i].section < entries[j].section
		}
		return entries[i].pageID < entries[j].pageID
	})

	// Group entries by section.
	type sectionGroup struct {
		title   string
		entries []plannedEntry
	}
	sectionTitles := map[pageSection]string{
		sectionOverview: "Overview",
		sectionDetailed: "Detailed",
		sectionRepoWide: "Repo-wide pages",
		sectionLegacy:   "Legacy pages (pre-D2)",
	}
	var groups []sectionGroup
	var cur *sectionGroup
	for _, e := range entries {
		if e.section == sectionIndex {
			continue
		}
		title := sectionTitles[e.section]
		if cur == nil || cur.title != title {
			groups = append(groups, sectionGroup{title: title})
			cur = &groups[len(groups)-1]
		}
		cur.entries = append(cur.entries, e)
	}

	// Build the ast.Page from the sections.
	var blocks []ast.Block
	blockOrdinal := 0

	addBlock := func(kind ast.BlockKind, content ast.BlockContent) {
		blocks = append(blocks, ast.Block{
			ID:      ast.GenerateBlockID(indexID, "index", kind, blockOrdinal),
			Kind:    kind,
			Content: content,
			Owner:   ast.OwnerGenerated,
		})
		blockOrdinal++
	}

	// Title paragraph (intro text + timestamp).
	addBlock(ast.BlockKindParagraph, ast.BlockContent{
		Paragraph: &ast.ParagraphContent{
			Markdown: fmt.Sprintf(
				"**Living Wiki Status** — last updated %s\n\nThis page shows the current generation status for all planned pages in this repository's living wiki. It is updated automatically every 30 seconds or every 10 page completions during an active run.",
				now.UTC().Format("2006-01-02 15:04:05 UTC"),
			),
		},
	})

	if len(groups) == 0 {
		addBlock(ast.BlockKindParagraph, ast.BlockContent{
			Paragraph: &ast.ParagraphContent{
				Markdown: "_No pages planned for this run._",
			},
		})
	}

	for _, grp := range groups {
		// Section heading.
		addBlock(ast.BlockKindHeading, ast.BlockContent{
			Heading: &ast.HeadingContent{Level: 2, Text: grp.title},
		})

		// Status table.
		rows := make([][]string, len(grp.entries))
		for i, e := range grp.entries {
			rows[i] = []string{
				StatusIcon(e.status),
				e.label,
				strings.ToUpper(e.status[:1]) + e.status[1:],
			}
		}
		addBlock(ast.BlockKindTable, ast.BlockContent{
			Table: &ast.TableContent{
				Headers: []string{"Status", "Page", "State"},
				Rows:    rows,
			},
		})
	}

	return ast.Page{
		ID:     indexID,
		Blocks: blocks,
		Provenance: ast.Provenance{
			GeneratedAt: now,
			ModelID:     "", // index page has no LLM model
		},
	}
}
