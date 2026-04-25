// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package markdown provides the markdown sink adapter for the living-wiki
// AST (Workstream D.2). It converts a [ast.Page] to CommonMark markdown and
// back, preserving block IDs as HTML comments so round-trips are lossless.
//
// # Block ID embedding
//
// Each block is wrapped in a pair of HTML comments:
//
//	<!-- sourcebridge:block id="b3f7a1..." kind="paragraph" owner="generated" -->
//	…block content…
//	<!-- /sourcebridge:block -->
//
// Parsers read these comments to reconstruct block IDs and ownership without
// needing a separate sidecar file. The content between the markers is
// standard CommonMark and renders cleanly in any markdown viewer.
//
// # Round-trip guarantee
//
// Write followed by Parse returns an AST equal to the original modulo:
//   - Whitespace normalization within prose blocks.
//   - The Provenance field, which is not serialized into markdown.
//
// # Confluence / Notion stubs
//
// These sinks are part of A1.P4 and are not implemented here.
// See confluence.go and notion.go for the interface declaration and stubs.
package markdown

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
)

// Write renders page to w as CommonMark markdown with embedded block-ID
// comments and a YAML frontmatter block.
//
// The output is suitable for committing to a git repository or publishing to
// a GitHub/GitLab wiki.
func Write(w io.Writer, page ast.Page) error {
	bw := bufio.NewWriter(w)

	// Write frontmatter.
	if err := manifest.WriteFrontmatter(bw, page.Manifest, nil); err != nil {
		return fmt.Errorf("markdown.Write: frontmatter: %w", err)
	}

	for i, blk := range page.Blocks {
		if i > 0 {
			if _, err := bw.WriteString("\n"); err != nil {
				return err
			}
		}
		if err := writeBlock(bw, blk); err != nil {
			return fmt.Errorf("markdown.Write: block %q: %w", blk.ID, err)
		}
	}

	return bw.Flush()
}

// writeBlock renders one block to w, wrapped in block-ID comments.
func writeBlock(w io.Writer, blk ast.Block) error {
	// Opening marker.
	if _, err := fmt.Fprintf(w, "<!-- sourcebridge:block id=%q kind=%q owner=%q -->\n",
		blk.ID, blk.Kind, blk.Owner); err != nil {
		return err
	}

	// Block content.
	if err := writeBlockContent(w, blk.Kind, blk.Content); err != nil {
		return err
	}

	// Closing marker.
	if _, err := fmt.Fprintf(w, "<!-- /sourcebridge:block -->\n"); err != nil {
		return err
	}

	return nil
}

func writeBlockContent(w io.Writer, kind ast.BlockKind, content ast.BlockContent) error {
	switch kind {
	case ast.BlockKindHeading:
		if content.Heading == nil {
			return nil
		}
		prefix := strings.Repeat("#", content.Heading.Level)
		_, err := fmt.Fprintf(w, "%s %s\n", prefix, content.Heading.Text)
		return err

	case ast.BlockKindParagraph:
		if content.Paragraph == nil {
			return nil
		}
		_, err := fmt.Fprintf(w, "%s\n", content.Paragraph.Markdown)
		return err

	case ast.BlockKindCode:
		if content.Code == nil {
			return nil
		}
		_, err := fmt.Fprintf(w, "```%s\n%s\n```\n", content.Code.Language, content.Code.Body)
		return err

	case ast.BlockKindTable:
		if content.Table == nil {
			return nil
		}
		return writeTable(w, content.Table)

	case ast.BlockKindCallout:
		if content.Callout == nil {
			return nil
		}
		// Render as a blockquote with a bold kind label.
		lines := strings.Split(content.Callout.Body, "\n")
		_, err := fmt.Fprintf(w, "> **%s:** %s\n", strings.ToUpper(content.Callout.Kind), lines[0])
		if err != nil {
			return err
		}
		for _, line := range lines[1:] {
			if _, err := fmt.Fprintf(w, "> %s\n", line); err != nil {
				return err
			}
		}
		return nil

	case ast.BlockKindEmbed:
		if content.Embed == nil {
			return nil
		}
		if content.Embed.TargetBlockID != "" {
			_, err := fmt.Fprintf(w, "<!-- embed page=%q block=%q -->\n",
				content.Embed.TargetPageID, content.Embed.TargetBlockID)
			return err
		}
		_, err := fmt.Fprintf(w, "<!-- embed page=%q -->\n", content.Embed.TargetPageID)
		return err

	case ast.BlockKindFreeform:
		if content.Freeform == nil {
			return nil
		}
		_, err := fmt.Fprintf(w, "<!-- sourcebridge:freeform -->\n%s\n<!-- /sourcebridge:freeform -->\n",
			content.Freeform.Raw)
		return err

	case ast.BlockKindStaleBanner:
		if content.StaleBanner == nil {
			return nil
		}
		return writeStaleBanner(w, content.StaleBanner)

	default:
		// Unknown kind — write as a freeform comment so it survives round-trips.
		_, err := fmt.Fprintf(w, "<!-- sourcebridge:unknown kind=%q -->\n", kind)
		return err
	}
}

func writeTable(w io.Writer, t *ast.TableContent) error {
	if len(t.Headers) == 0 {
		return nil
	}

	// Header row.
	if _, err := fmt.Fprintf(w, "| %s |\n", strings.Join(t.Headers, " | ")); err != nil {
		return err
	}

	// Separator row.
	seps := make([]string, len(t.Headers))
	for i := range seps {
		seps[i] = "---"
	}
	if _, err := fmt.Fprintf(w, "| %s |\n", strings.Join(seps, " | ")); err != nil {
		return err
	}

	// Data rows.
	for _, row := range t.Rows {
		if _, err := fmt.Fprintf(w, "| %s |\n", strings.Join(row, " | ")); err != nil {
			return err
		}
	}
	return nil
}

func writeStaleBanner(w io.Writer, s *ast.StaleBannerContent) error {
	syms := strings.Join(s.TriggeringSymbols, ", ")
	line := fmt.Sprintf(
		"> **This page may be out of date.** Recent changes to `%s` (commit `%s`) may affect this content.",
		syms, s.TriggeringCommit,
	)
	if s.RefreshURL != "" {
		line += fmt.Sprintf(" [Refresh from source](%s).", s.RefreshURL)
	}
	if s.NextRegenWindow != "" {
		line += fmt.Sprintf(" Next scheduled regen: %s.", s.NextRegenWindow)
	}
	_, err := fmt.Fprintln(w, line)
	return err
}
