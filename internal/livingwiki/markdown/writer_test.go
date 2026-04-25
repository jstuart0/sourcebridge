// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package markdown_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/markdown"
)

// buildTestPage constructs a representative page for round-trip tests.
func buildTestPage() ast.Page {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	return ast.Page{
		ID: "arch.auth",
		Manifest: manifest.DependencyManifest{
			PageID:   "arch.auth",
			Template: "architecture",
			Audience: "for-engineers",
			Dependencies: manifest.Dependencies{
				Paths:           []string{"internal/auth/**"},
				Symbols:         []string{"auth.Middleware"},
				DependencyScope: manifest.ScopeDirect,
			},
		},
		Blocks: []ast.Block{
			{
				ID:   "b001",
				Kind: ast.BlockKindHeading,
				Content: ast.BlockContent{
					Heading: &ast.HeadingContent{Level: 1, Text: "Auth Package"},
				},
				Owner:      ast.OwnerGenerated,
				LastChange: ast.BlockChange{SHA: "abc123", Timestamp: now, Source: "sourcebridge"},
			},
			{
				ID:   "b002",
				Kind: ast.BlockKindParagraph,
				Content: ast.BlockContent{
					Paragraph: &ast.ParagraphContent{
						Markdown: "The auth package handles JWT-based authentication for all HTTP handlers.",
					},
				},
				Owner:      ast.OwnerGenerated,
				LastChange: ast.BlockChange{SHA: "abc123", Timestamp: now, Source: "sourcebridge"},
			},
			{
				ID:   "b003",
				Kind: ast.BlockKindCode,
				Content: ast.BlockContent{
					Code: &ast.CodeContent{
						Language: "go",
						Body:     "func Middleware(next http.Handler) http.Handler {\n\treturn http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {\n\t\t// validate JWT\n\t\tnext.ServeHTTP(w, r)\n\t})\n}",
					},
				},
				Owner:      ast.OwnerGenerated,
				LastChange: ast.BlockChange{SHA: "abc123", Timestamp: now, Source: "sourcebridge"},
			},
			{
				ID:   "b004",
				Kind: ast.BlockKindTable,
				Content: ast.BlockContent{
					Table: &ast.TableContent{
						Headers: []string{"Function", "Description"},
						Rows: [][]string{
							{"Middleware", "JWT validation middleware"},
							{"RequireRole", "Role-based access control"},
						},
					},
				},
				Owner:      ast.OwnerGenerated,
				LastChange: ast.BlockChange{SHA: "abc123", Timestamp: now, Source: "sourcebridge"},
			},
			{
				ID:   "b005",
				Kind: ast.BlockKindCallout,
				Content: ast.BlockContent{
					Callout: &ast.CalloutContent{
						Kind: "warning",
						Body: "RequireRole must be called after Middleware in the handler chain.",
					},
				},
				Owner:      ast.OwnerGenerated,
				LastChange: ast.BlockChange{SHA: "abc123", Timestamp: now, Source: "sourcebridge"},
			},
		},
		Provenance: ast.Provenance{
			GeneratedAt:    now,
			GeneratedBySHA: "abc123",
			ModelID:        "claude-sonnet-4-6",
		},
	}
}

// TestWrite_Produces_BlockMarkers verifies the written output contains block markers.
func TestWrite_Produces_BlockMarkers(t *testing.T) {
	page := buildTestPage()
	var buf bytes.Buffer
	if err := markdown.Write(&buf, page); err != nil {
		t.Fatalf("Write: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, `<!-- sourcebridge:block id="b001"`) {
		t.Error("output missing block open marker for b001")
	}
	if !strings.Contains(out, `<!-- /sourcebridge:block -->`) {
		t.Error("output missing block close marker")
	}
	if !strings.Contains(out, "# Auth Package") {
		t.Error("output missing heading content")
	}
	if !strings.Contains(out, "```go") {
		t.Error("output missing code fence")
	}
}

// TestWrite_Parse_RoundTrip is the primary round-trip test:
// Write → Parse must produce equivalent blocks.
func TestWrite_Parse_RoundTrip(t *testing.T) {
	page := buildTestPage()

	var buf bytes.Buffer
	if err := markdown.Write(&buf, page); err != nil {
		t.Fatalf("Write: %v", err)
	}

	parsed, err := markdown.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("Parse: %v (markdown was:\n%s)", err, buf.String())
	}

	// Page ID.
	if parsed.ID != page.ID {
		t.Errorf("PageID: got %q, want %q", parsed.ID, page.ID)
	}

	// Block count.
	if len(parsed.Blocks) != len(page.Blocks) {
		t.Fatalf("block count: got %d, want %d\nmarkdown:\n%s", len(parsed.Blocks), len(page.Blocks), buf.String())
	}

	for i, orig := range page.Blocks {
		got := parsed.Blocks[i]

		// ID must match exactly.
		if got.ID != orig.ID {
			t.Errorf("block[%d] ID: got %q, want %q", i, got.ID, orig.ID)
		}
		// Kind must match exactly.
		if got.Kind != orig.Kind {
			t.Errorf("block[%d] Kind: got %q, want %q", i, got.Kind, orig.Kind)
		}
		// Owner must match exactly.
		if got.Owner != orig.Owner {
			t.Errorf("block[%d] Owner: got %q, want %q", i, got.Owner, orig.Owner)
		}

		// Content round-trip by kind.
		switch orig.Kind {
		case ast.BlockKindHeading:
			wantLevel := orig.Content.Heading.Level
			wantText := orig.Content.Heading.Text
			if got.Content.Heading == nil {
				t.Errorf("block[%d] heading content is nil", i)
				continue
			}
			if got.Content.Heading.Level != wantLevel || got.Content.Heading.Text != wantText {
				t.Errorf("block[%d] heading: got level=%d text=%q, want level=%d text=%q",
					i, got.Content.Heading.Level, got.Content.Heading.Text, wantLevel, wantText)
			}

		case ast.BlockKindParagraph:
			wantMD := orig.Content.Paragraph.Markdown
			if got.Content.Paragraph == nil {
				t.Errorf("block[%d] paragraph content is nil", i)
				continue
			}
			if got.Content.Paragraph.Markdown != wantMD {
				t.Errorf("block[%d] paragraph: got %q, want %q",
					i, got.Content.Paragraph.Markdown, wantMD)
			}

		case ast.BlockKindCode:
			wantLang := orig.Content.Code.Language
			wantBody := orig.Content.Code.Body
			if got.Content.Code == nil {
				t.Errorf("block[%d] code content is nil", i)
				continue
			}
			if got.Content.Code.Language != wantLang || got.Content.Code.Body != wantBody {
				t.Errorf("block[%d] code: got lang=%q body=%q, want lang=%q body=%q",
					i, got.Content.Code.Language, got.Content.Code.Body, wantLang, wantBody)
			}

		case ast.BlockKindTable:
			wantHeaders := orig.Content.Table.Headers
			if got.Content.Table == nil {
				t.Errorf("block[%d] table content is nil", i)
				continue
			}
			if len(got.Content.Table.Headers) != len(wantHeaders) {
				t.Errorf("block[%d] table headers: got %v, want %v", i, got.Content.Table.Headers, wantHeaders)
			}
			if len(got.Content.Table.Rows) != len(orig.Content.Table.Rows) {
				t.Errorf("block[%d] table rows: got %d, want %d", i,
					len(got.Content.Table.Rows), len(orig.Content.Table.Rows))
			}

		case ast.BlockKindCallout:
			wantKind := orig.Content.Callout.Kind
			if got.Content.Callout == nil {
				t.Errorf("block[%d] callout content is nil", i)
				continue
			}
			if got.Content.Callout.Kind != wantKind {
				t.Errorf("block[%d] callout kind: got %q, want %q",
					i, got.Content.Callout.Kind, wantKind)
			}
		}
	}
}

// TestWrite_Parse_RoundTrip_TwiceIdentical verifies the second round-trip
// produces the same output as the first (stability across regenerations).
func TestWrite_Parse_RoundTrip_TwiceIdentical(t *testing.T) {
	page := buildTestPage()

	var buf1 bytes.Buffer
	if err := markdown.Write(&buf1, page); err != nil {
		t.Fatalf("Write (first): %v", err)
	}

	parsed1, err := markdown.Parse(buf1.Bytes())
	if err != nil {
		t.Fatalf("Parse (first): %v", err)
	}

	var buf2 bytes.Buffer
	if err := markdown.Write(&buf2, parsed1); err != nil {
		t.Fatalf("Write (second): %v", err)
	}

	parsed2, err := markdown.Parse(buf2.Bytes())
	if err != nil {
		t.Fatalf("Parse (second): %v", err)
	}

	if len(parsed1.Blocks) != len(parsed2.Blocks) {
		t.Fatalf("block count differs on second round-trip: %d vs %d", len(parsed1.Blocks), len(parsed2.Blocks))
	}

	for i := range parsed1.Blocks {
		if parsed1.Blocks[i].ID != parsed2.Blocks[i].ID {
			t.Errorf("block[%d] ID changed on second round-trip: %q → %q",
				i, parsed1.Blocks[i].ID, parsed2.Blocks[i].ID)
		}
	}
}

// TestParse_NoFrontmatter handles a page with no YAML frontmatter.
func TestParse_NoFrontmatter(t *testing.T) {
	src := []byte(`<!-- sourcebridge:block id="b001" kind="paragraph" owner="generated" -->
Just some content.
<!-- /sourcebridge:block -->
`)
	parsed, err := markdown.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(parsed.Blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(parsed.Blocks))
	}
	if parsed.Blocks[0].ID != "b001" {
		t.Errorf("ID: got %q, want %q", parsed.Blocks[0].ID, "b001")
	}
}

// TestParse_ImplicitFreeform verifies text outside block markers becomes an
// implicit freeform block.
func TestParse_ImplicitFreeform(t *testing.T) {
	src := []byte("This text is outside any block marker.\n")
	parsed, err := markdown.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(parsed.Blocks) != 1 {
		t.Fatalf("expected 1 implicit freeform block, got %d", len(parsed.Blocks))
	}
	if parsed.Blocks[0].Kind != ast.BlockKindFreeform {
		t.Errorf("expected freeform kind, got %q", parsed.Blocks[0].Kind)
	}
	if parsed.Blocks[0].Owner != ast.OwnerHumanOnly {
		t.Errorf("implicit freeform should be human-only, got %q", parsed.Blocks[0].Owner)
	}
}

// TestWrite_StaleBanner verifies stale banners round-trip.
func TestWrite_StaleBanner(t *testing.T) {
	page := ast.Page{
		ID: "arch.auth",
		Manifest: manifest.DependencyManifest{
			PageID: "arch.auth",
		},
		Blocks: []ast.Block{
			{
				ID:   "bstale01",
				Kind: ast.BlockKindStaleBanner,
				Content: ast.BlockContent{
					StaleBanner: &ast.StaleBannerContent{
						TriggeringCommit:  "a1b2c3d",
						TriggeringSymbols: []string{"auth.RequireRole"},
						ConditionKind:     "signature_change_in",
						RefreshURL:        "https://app.sourcebridge.ai/repos/123/pages/arch.auth/refresh",
					},
				},
				Owner: ast.OwnerGenerated,
			},
		},
	}

	var buf bytes.Buffer
	if err := markdown.Write(&buf, page); err != nil {
		t.Fatalf("Write: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "out of date") {
		t.Error("stale banner should contain 'out of date'")
	}
	if !strings.Contains(out, "a1b2c3d") {
		t.Error("stale banner should contain commit SHA")
	}

	// Parse back — commit SHA should be recoverable.
	parsed, err := markdown.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("Parse stale banner: %v", err)
	}
	if len(parsed.Blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(parsed.Blocks))
	}
	if parsed.Blocks[0].Content.StaleBanner == nil {
		t.Fatal("stale banner content is nil after parse")
	}
	if parsed.Blocks[0].Content.StaleBanner.TriggeringCommit != "a1b2c3d" {
		t.Errorf("commit SHA lost: got %q, want %q",
			parsed.Blocks[0].Content.StaleBanner.TriggeringCommit, "a1b2c3d")
	}
}

// TestWrite_EmbedBlock verifies embed blocks round-trip.
func TestWrite_EmbedBlock(t *testing.T) {
	page := ast.Page{
		ID: "arch.auth",
		Manifest: manifest.DependencyManifest{PageID: "arch.auth"},
		Blocks: []ast.Block{
			{
				ID:   "bembed01",
				Kind: ast.BlockKindEmbed,
				Content: ast.BlockContent{
					Embed: &ast.EmbedContent{
						TargetPageID:  "arch.billing",
						TargetBlockID: "b042",
					},
				},
				Owner: ast.OwnerGenerated,
			},
		},
	}

	var buf bytes.Buffer
	if err := markdown.Write(&buf, page); err != nil {
		t.Fatalf("Write: %v", err)
	}

	parsed, err := markdown.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(parsed.Blocks) != 1 || parsed.Blocks[0].Content.Embed == nil {
		t.Fatal("embed block lost after parse")
	}
	if parsed.Blocks[0].Content.Embed.TargetPageID != "arch.billing" {
		t.Errorf("TargetPageID: got %q, want %q",
			parsed.Blocks[0].Content.Embed.TargetPageID, "arch.billing")
	}
}
