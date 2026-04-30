// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package indexpage_test

import (
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/indexpage"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

const testRepoID = "7c9d4387-5f3f-4acf-ac29-4b89d3f2922f"

func TestIndexPageID(t *testing.T) {
	got := indexpage.IndexPageID(testRepoID)
	want := testRepoID + ".__index__"
	if got != want {
		t.Errorf("IndexPageID(%q) = %q; want %q", testRepoID, got, want)
	}
}

func TestStatusIcon(t *testing.T) {
	cases := []struct {
		status string
		want   string
	}{
		{"ready", "✓"},
		{"generating", "⏳"},
		{"failed", "✗"},
		{"failed_fixup", "✗"},
		{"pending", "·"},
		{"", "·"},
		{"unknown_future_value", "·"},
	}
	for _, tc := range cases {
		if got := indexpage.StatusIcon(tc.status); got != tc.want {
			t.Errorf("StatusIcon(%q) = %q; want %q", tc.status, got, tc.want)
		}
	}
}

// TestRenderIndexPageStableOrdering verifies that rendering the same planned
// set in different input orders produces identical output (pages are sorted
// internally by section + page ID).
func TestRenderIndexPageStableOrdering(t *testing.T) {
	planned := []string{
		testRepoID + ".arch.auth",
		testRepoID + ".arch.gateway",
		testRepoID + ".api_reference",
		testRepoID + ".glossary",
	}
	shuffled := []string{
		testRepoID + ".glossary",
		testRepoID + ".arch.gateway",
		testRepoID + ".api_reference",
		testRepoID + ".arch.auth",
	}
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)

	p1 := indexpage.RenderIndexPage(testRepoID, planned, nil, now)
	p2 := indexpage.RenderIndexPage(testRepoID, shuffled, nil, now)

	if len(p1.Blocks) != len(p2.Blocks) {
		t.Fatalf("block count mismatch: sorted=%d shuffled=%d", len(p1.Blocks), len(p2.Blocks))
	}
	for i := range p1.Blocks {
		if p1.Blocks[i].ID != p2.Blocks[i].ID {
			t.Errorf("block[%d] ID mismatch: sorted=%q shuffled=%q", i, p1.Blocks[i].ID, p2.Blocks[i].ID)
		}
	}
}

// TestRenderIndexPageGroupsBySection verifies that architecture, detailed,
// overview, and repo-wide pages each appear under the correct heading.
func TestRenderIndexPageGroupsBySection(t *testing.T) {
	planned := []string{
		testRepoID + ".detail.internal.api",
		testRepoID + ".overview.auth_subsystem",
		testRepoID + ".api_reference",
		testRepoID + ".glossary",
		testRepoID + ".system_overview",
		testRepoID + ".arch.internal.legacy", // legacy arch.* page
	}
	now := time.Now()
	page := indexpage.RenderIndexPage(testRepoID, planned, nil, now)

	// Collect all heading texts.
	headings := collectHeadings(page.Blocks)
	headingSet := make(map[string]bool, len(headings))
	for _, h := range headings {
		headingSet[h] = true
	}

	// We should see a section for Overview, Detailed, Repo-wide, and Legacy.
	wantSections := []string{"Overview", "Detailed", "Repo-wide pages", "Legacy pages (pre-D2)"}
	for _, s := range wantSections {
		if !headingSet[s] {
			t.Errorf("expected section heading %q but did not find it in headings: %v", s, headings)
		}
	}

	// The page must NOT include the index page itself.
	for _, b := range page.Blocks {
		if b.Kind == ast.BlockKindTable && b.Content.Table != nil {
			for _, row := range b.Content.Table.Rows {
				for _, cell := range row {
					if cell == "__index__" {
						t.Errorf("index page's own ID appeared in table row: %v", row)
					}
				}
			}
		}
	}
}

// TestRenderIndexPageExcludesIndexItself verifies that the index page's own
// external ID never appears in the rendered table rows.
func TestRenderIndexPageExcludesIndexItself(t *testing.T) {
	planned := []string{
		indexpage.IndexPageID(testRepoID), // explicitly include it in input
		testRepoID + ".api_reference",
	}
	page := indexpage.RenderIndexPage(testRepoID, planned, nil, time.Now())
	for _, b := range page.Blocks {
		if b.Kind == ast.BlockKindTable && b.Content.Table != nil {
			for _, row := range b.Content.Table.Rows {
				for _, cell := range row {
					if cell == "__index__" {
						t.Errorf("index page's own ID appeared in table row: %v", row)
					}
				}
			}
		}
	}
}

// TestRenderIndexPageStatusOrdering verifies that the status icons match the
// expected symbol for each status value.
func TestRenderIndexPageStatusOrdering(t *testing.T) {
	planned := []string{
		testRepoID + ".api_reference",
		testRepoID + ".glossary",
		testRepoID + ".system_overview",
	}
	statuses := []livingwiki.PagePublishStatusRow{
		{RepoID: testRepoID, PageID: testRepoID + ".api_reference", SinkKind: "confluence", IntegrationName: "main", Status: "ready"},
		{RepoID: testRepoID, PageID: testRepoID + ".glossary", SinkKind: "confluence", IntegrationName: "main", Status: "failed"},
		{RepoID: testRepoID, PageID: testRepoID + ".system_overview", SinkKind: "confluence", IntegrationName: "main", Status: "generating"},
	}
	page := indexpage.RenderIndexPage(testRepoID, planned, statuses, time.Now())

	// Collect (page label → status icon) from all table rows.
	labelIcon := collectLabelIcons(page.Blocks)

	if got := labelIcon["API Reference"]; got != "✓" {
		t.Errorf("api_reference status icon = %q; want ✓", got)
	}
	if got := labelIcon["Glossary"]; got != "✗" {
		t.Errorf("glossary status icon = %q; want ✗", got)
	}
	if got := labelIcon["System Overview"]; got != "⏳" {
		t.Errorf("system_overview status icon = %q; want ⏳", got)
	}
}

// TestRenderIndexPagePendingWhenNoStatus verifies that pages present in the
// planned list but absent from the status store are shown as Pending.
func TestRenderIndexPagePendingWhenNoStatus(t *testing.T) {
	planned := []string{testRepoID + ".api_reference"}
	page := indexpage.RenderIndexPage(testRepoID, planned, nil /* no statuses */, time.Now())

	labelIcon := collectLabelIcons(page.Blocks)
	if got := labelIcon["API Reference"]; got != "·" {
		t.Errorf("api_reference pending icon = %q; want · (pending)", got)
	}
}

// TestRenderIndexPageOverviewOnlyEnabled verifies that when only overview pages
// are planned, only the Overview section (and repo-wide) is present.
func TestRenderIndexPageOverviewOnlyEnabled(t *testing.T) {
	planned := []string{
		testRepoID + ".overview.auth_subsystem",
		testRepoID + ".overview.data_layer",
		testRepoID + ".api_reference",
	}
	page := indexpage.RenderIndexPage(testRepoID, planned, nil, time.Now())
	headings := collectHeadingSet(page.Blocks)

	if !headings["Overview"] {
		t.Error("expected Overview section")
	}
	if !headings["Repo-wide pages"] {
		t.Error("expected Repo-wide pages section")
	}
	if headings["Detailed"] {
		t.Error("did not expect Detailed section when no detail.* pages are planned")
	}
}

// TestRenderIndexPageDetailedOnlyEnabled verifies that when only detailed pages
// are planned, only the Detailed section (and repo-wide) is present.
func TestRenderIndexPageDetailedOnlyEnabled(t *testing.T) {
	planned := []string{
		testRepoID + ".detail.internal.api",
		testRepoID + ".detail.internal.db",
		testRepoID + ".glossary",
	}
	page := indexpage.RenderIndexPage(testRepoID, planned, nil, time.Now())
	headings := collectHeadingSet(page.Blocks)

	if !headings["Detailed"] {
		t.Error("expected Detailed section")
	}
	if !headings["Repo-wide pages"] {
		t.Error("expected Repo-wide pages section")
	}
	if headings["Overview"] {
		t.Error("did not expect Overview section when no overview.* pages are planned")
	}
}

// TestRenderIndexPageBothModesEnabled verifies that when both overview and
// detailed pages are planned, both sections appear.
func TestRenderIndexPageBothModesEnabled(t *testing.T) {
	planned := []string{
		testRepoID + ".overview.auth_subsystem",
		testRepoID + ".detail.internal.api",
		testRepoID + ".api_reference",
	}
	page := indexpage.RenderIndexPage(testRepoID, planned, nil, time.Now())
	headings := collectHeadingSet(page.Blocks)

	if !headings["Overview"] {
		t.Error("expected Overview section")
	}
	if !headings["Detailed"] {
		t.Error("expected Detailed section")
	}
}

// TestRenderIndexPageEmptyPlan verifies that an empty planned set renders
// gracefully (a single intro paragraph; no section headings).
func TestRenderIndexPageEmptyPlan(t *testing.T) {
	page := indexpage.RenderIndexPage(testRepoID, nil, nil, time.Now())
	if page.ID != indexpage.IndexPageID(testRepoID) {
		t.Errorf("page.ID = %q; want %q", page.ID, indexpage.IndexPageID(testRepoID))
	}
	headings := collectHeadings(page.Blocks)
	if len(headings) > 0 {
		t.Errorf("expected no section headings for empty plan; got %v", headings)
	}
}

// TestRenderIndexPageWorstStatusWinsForMultiSink verifies that when a page has
// rows for two sinks and one shows "generating" while the other shows "failed",
// the rendered status is "failed" (the worst/most-actionable status wins).
func TestRenderIndexPageWorstStatusWinsForMultiSink(t *testing.T) {
	planned := []string{testRepoID + ".api_reference"}
	statuses := []livingwiki.PagePublishStatusRow{
		{RepoID: testRepoID, PageID: testRepoID + ".api_reference", SinkKind: "confluence", IntegrationName: "main", Status: "generating"},
		{RepoID: testRepoID, PageID: testRepoID + ".api_reference", SinkKind: "notion", IntegrationName: "secondary", Status: "failed"},
	}
	page := indexpage.RenderIndexPage(testRepoID, planned, statuses, time.Now())

	labelIcon := collectLabelIcons(page.Blocks)
	if got := labelIcon["API Reference"]; got != "✗" {
		t.Errorf("expected ✗ (failed) when one sink failed; got %q", got)
	}
}

// TestRenderIndexPageLegacyArchPagesFromStatusStore verifies that arch.* pages
// from prior pre-D2 runs appear in the Legacy section when they exist in the
// status store but are not in the current planned list.
func TestRenderIndexPageLegacyArchPagesFromStatusStore(t *testing.T) {
	planned := []string{testRepoID + ".api_reference"}
	statuses := []livingwiki.PagePublishStatusRow{
		{RepoID: testRepoID, PageID: testRepoID + ".arch.internal.legacy", SinkKind: "confluence", IntegrationName: "main", Status: "ready"},
	}
	page := indexpage.RenderIndexPage(testRepoID, planned, statuses, time.Now())
	headings := collectHeadingSet(page.Blocks)

	if !headings["Legacy pages (pre-D2)"] {
		t.Error("expected Legacy pages section for arch.* pages in status store")
	}
}

// TestRenderIndexPageIDIsStable verifies that the returned page.ID is always
// the deterministic IndexPageID for the given repoID.
func TestRenderIndexPageIDIsStable(t *testing.T) {
	page := indexpage.RenderIndexPage(testRepoID, []string{testRepoID + ".glossary"}, nil, time.Now())
	if page.ID != indexpage.IndexPageID(testRepoID) {
		t.Errorf("page.ID = %q; want %q", page.ID, indexpage.IndexPageID(testRepoID))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func collectHeadings(blocks []ast.Block) []string {
	var out []string
	for _, b := range blocks {
		if b.Kind == ast.BlockKindHeading && b.Content.Heading != nil {
			out = append(out, b.Content.Heading.Text)
		}
	}
	return out
}

func collectHeadingSet(blocks []ast.Block) map[string]bool {
	m := make(map[string]bool)
	for _, h := range collectHeadings(blocks) {
		m[h] = true
	}
	return m
}

// collectLabelIcons walks all table blocks and builds a map from the "Page"
// column value to the "Status" icon column value.
func collectLabelIcons(blocks []ast.Block) map[string]string {
	result := make(map[string]string)
	for _, b := range blocks {
		if b.Kind != ast.BlockKindTable || b.Content.Table == nil {
			continue
		}
		tbl := b.Content.Table
		// Expect columns: Status icon, Page label, State text
		// Headers: ["Status", "Page", "State"]
		for _, row := range tbl.Rows {
			if len(row) >= 2 {
				result[row[1]] = row[0]
			}
		}
	}
	return result
}
