// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package markdown

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
)

// Parse parses a markdown document (including YAML frontmatter) previously
// written by [Write] and reconstructs the [ast.Page].
//
// Blocks are demarcated by <!-- sourcebridge:block … --> comments. Any text
// outside a block marker pair is collected into an implicit freeform block
// with a generated ID.
//
// Provenance is not stored in markdown and is therefore zero in the returned
// Page; callers that need Provenance should restore it from their own storage.
func Parse(src []byte) (ast.Page, error) {
	m, body, err := manifest.ParseFrontmatter(src)
	if err != nil {
		return ast.Page{}, fmt.Errorf("markdown.Parse: frontmatter: %w", err)
	}

	blocks, parseErr := parseBlocks(body, m.PageID)
	if parseErr != nil {
		return ast.Page{}, fmt.Errorf("markdown.Parse: %w", parseErr)
	}

	return ast.Page{
		ID:       m.PageID,
		Manifest: m,
		Blocks:   blocks,
	}, nil
}

// reBlockOpen matches the opening block marker, capturing id, kind, owner.
// <!-- sourcebridge:block id="b3f7..." kind="paragraph" owner="generated" -->
var reBlockOpen = regexp.MustCompile(
	`<!--\s*sourcebridge:block\s+id="([^"]+)"\s+kind="([^"]+)"\s+owner="([^"]+)"\s*-->`,
)

// reBlockClose matches the closing block marker.
var reBlockClose = regexp.MustCompile(`<!--\s*/sourcebridge:block\s*-->`)

// reFreeformOpen matches the freeform section open marker.
var reFreeformOpen = regexp.MustCompile(`<!--\s*sourcebridge:freeform\s*-->`)

// reFreeformClose matches the freeform section close marker.
var reFreeformClose = regexp.MustCompile(`<!--\s*/sourcebridge:freeform\s*-->`)

// reEmbedPage matches <!-- embed page="..." --> (without block).
var reEmbedPage = regexp.MustCompile(`<!--\s*embed\s+page="([^"]+)"\s*-->`)

// reEmbedPageBlock matches <!-- embed page="..." block="..." -->.
var reEmbedPageBlock = regexp.MustCompile(`<!--\s*embed\s+page="([^"]+)"\s+block="([^"]+)"\s*-->`)

// parseBlocks splits body into blocks based on the embedded markers.
func parseBlocks(body []byte, pageID string) ([]ast.Block, error) {
	scanner := bufio.NewScanner(bytes.NewReader(body))

	var blocks []ast.Block
	var outsideLines []string // lines between block markers → implicit freeform

	inBlock := false
	var currentID ast.BlockID
	var currentKind ast.BlockKind
	var currentOwner ast.Owner
	var currentLines []string

	flushOutside := func() {
		text := strings.TrimSpace(strings.Join(outsideLines, "\n"))
		if text == "" {
			outsideLines = nil
			return
		}
		// Emit as an implicit freeform block with a generated ID.
		id := ast.GenerateBlockID(pageID, "implicit", ast.BlockKindFreeform, len(blocks))
		blocks = append(blocks, ast.Block{
			ID:   id,
			Kind: ast.BlockKindFreeform,
			Content: ast.BlockContent{
				Freeform: &ast.FreeformContent{Raw: text},
			},
			Owner: ast.OwnerHumanOnly,
		})
		outsideLines = nil
	}

	flushBlock := func() error {
		if currentID == "" {
			return nil
		}
		content, err := parseBlockContent(currentKind, currentLines)
		if err != nil {
			return fmt.Errorf("block %q (%s): %w", currentID, currentKind, err)
		}
		blocks = append(blocks, ast.Block{
			ID:      currentID,
			Kind:    currentKind,
			Content: content,
			Owner:   currentOwner,
		})
		currentID = ""
		currentKind = ""
		currentOwner = ""
		currentLines = nil
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()

		if !inBlock {
			if m := reBlockOpen.FindStringSubmatch(line); m != nil {
				flushOutside()
				currentID = ast.BlockID(m[1])
				currentKind = ast.BlockKind(m[2])
				currentOwner = ast.Owner(m[3])
				currentLines = nil
				inBlock = true
				continue
			}
			outsideLines = append(outsideLines, line)
		} else {
			if reBlockClose.MatchString(line) {
				if err := flushBlock(); err != nil {
					return nil, err
				}
				inBlock = false
				continue
			}
			currentLines = append(currentLines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Flush any unclosed block or trailing outside content.
	if inBlock {
		if err := flushBlock(); err != nil {
			return nil, err
		}
	}
	flushOutside()

	return blocks, nil
}

// parseBlockContent parses the lines inside a block marker into typed content.
func parseBlockContent(kind ast.BlockKind, lines []string) (ast.BlockContent, error) {
	raw := strings.Join(lines, "\n")
	// Trim trailing newline introduced by the writer.
	raw = strings.TrimRight(raw, "\n")

	switch kind {
	case ast.BlockKindHeading:
		return parseHeading(raw)
	case ast.BlockKindParagraph:
		return ast.BlockContent{
			Paragraph: &ast.ParagraphContent{Markdown: strings.TrimSpace(raw)},
		}, nil
	case ast.BlockKindCode:
		return parseCode(raw)
	case ast.BlockKindTable:
		return parseTable(lines)
	case ast.BlockKindCallout:
		return parseCallout(raw)
	case ast.BlockKindEmbed:
		return parseEmbed(raw)
	case ast.BlockKindFreeform:
		return parseFreeform(raw)
	case ast.BlockKindStaleBanner:
		return parseStaleBanner(raw)
	default:
		// Unknown kind — preserve as freeform.
		return ast.BlockContent{
			Freeform: &ast.FreeformContent{Raw: raw},
		}, nil
	}
}

// parseHeading parses "## Heading text" → HeadingContent.
var reHeadingLine = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)

func parseHeading(raw string) (ast.BlockContent, error) {
	trimmed := strings.TrimSpace(raw)
	m := reHeadingLine.FindStringSubmatch(trimmed)
	if m == nil {
		return ast.BlockContent{
			Heading: &ast.HeadingContent{Level: 1, Text: trimmed},
		}, nil
	}
	return ast.BlockContent{
		Heading: &ast.HeadingContent{
			Level: len(m[1]),
			Text:  strings.TrimSpace(m[2]),
		},
	}, nil
}

// parseCode parses a fenced code block.
var reCodeFence = regexp.MustCompile("(?s)^```([^\n]*)\n(.*)\n```\\s*$")

func parseCode(raw string) (ast.BlockContent, error) {
	trimmed := strings.TrimSpace(raw)
	m := reCodeFence.FindStringSubmatch(trimmed)
	if m == nil {
		// Malformed fence — store body as-is with no language.
		return ast.BlockContent{
			Code: &ast.CodeContent{Body: trimmed},
		}, nil
	}
	return ast.BlockContent{
		Code: &ast.CodeContent{
			Language: strings.TrimSpace(m[1]),
			Body:     m[2],
		},
	}, nil
}

// parseTable parses a markdown pipe table.
func parseTable(lines []string) (ast.BlockContent, error) {
	// Filter out the block marker lines that may have been included.
	var tableLines []string
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		tableLines = append(tableLines, l)
	}
	if len(tableLines) < 2 {
		return ast.BlockContent{Table: &ast.TableContent{}}, nil
	}

	parseRow := func(line string) []string {
		// Split on | and trim.
		parts := strings.Split(line, "|")
		var cells []string
		for _, p := range parts {
			trimmed := strings.TrimSpace(p)
			if trimmed == "" {
				continue
			}
			cells = append(cells, trimmed)
		}
		return cells
	}

	headers := parseRow(tableLines[0])
	// tableLines[1] is the separator row — skip it.
	var rows [][]string
	for _, line := range tableLines[2:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		rows = append(rows, parseRow(line))
	}

	return ast.BlockContent{
		Table: &ast.TableContent{
			Headers: headers,
			Rows:    rows,
		},
	}, nil
}

// parseCallout parses a blockquote callout: > **KIND:** body text.
var reCalloutLine = regexp.MustCompile(`^>\s*\*\*([A-Z]+):\*\*\s*(.*)$`)

func parseCallout(raw string) (ast.BlockContent, error) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) == 0 {
		return ast.BlockContent{Callout: &ast.CalloutContent{}}, nil
	}

	m := reCalloutLine.FindStringSubmatch(lines[0])
	kind := "note"
	var bodyLines []string

	if m != nil {
		kind = strings.ToLower(m[1])
		bodyLines = append(bodyLines, m[2])
	} else {
		// Fallback: strip leading "> " and use as body.
		body := strings.TrimPrefix(lines[0], "> ")
		bodyLines = append(bodyLines, body)
	}

	for _, line := range lines[1:] {
		bodyLines = append(bodyLines, strings.TrimPrefix(line, "> "))
	}

	return ast.BlockContent{
		Callout: &ast.CalloutContent{
			Kind: kind,
			Body: strings.Join(bodyLines, "\n"),
		},
	}, nil
}

// parseEmbed parses an embed comment.
func parseEmbed(raw string) (ast.BlockContent, error) {
	trimmed := strings.TrimSpace(raw)
	if m := reEmbedPageBlock.FindStringSubmatch(trimmed); m != nil {
		return ast.BlockContent{
			Embed: &ast.EmbedContent{
				TargetPageID:  m[1],
				TargetBlockID: ast.BlockID(m[2]),
			},
		}, nil
	}
	if m := reEmbedPage.FindStringSubmatch(trimmed); m != nil {
		return ast.BlockContent{
			Embed: &ast.EmbedContent{TargetPageID: m[1]},
		}, nil
	}
	return ast.BlockContent{
		Embed: &ast.EmbedContent{},
	}, nil
}

// parseFreeform parses a freeform block, stripping the freeform markers if present.
func parseFreeform(raw string) (ast.BlockContent, error) {
	// Strip <!-- sourcebridge:freeform --> markers if present.
	trimmed := strings.TrimSpace(raw)
	if reFreeformOpen.MatchString(trimmed) {
		loc := reFreeformOpen.FindStringIndex(trimmed)
		trimmed = trimmed[loc[1]:]
	}
	if reFreeformClose.MatchString(trimmed) {
		loc := reFreeformClose.FindStringIndex(trimmed)
		trimmed = trimmed[:loc[0]]
	}
	return ast.BlockContent{
		Freeform: &ast.FreeformContent{Raw: strings.TrimSpace(trimmed)},
	}, nil
}

// parseStaleBanner reconstructs a StaleBannerContent from the rendered blockquote.
// Since the banner is rendered prose, we do a best-effort parse. Full
// reconstruction of all fields requires the metadata stored alongside the
// AST; the markdown form is the human-visible representation.
func parseStaleBanner(raw string) (ast.BlockContent, error) {
	// Attempt to extract commit SHA from backtick-quoted text.
	reSHA := regexp.MustCompile("`([0-9a-f]{7,40})`")
	var sha string
	if m := reSHA.FindStringSubmatch(raw); m != nil {
		sha = m[1]
	}

	// Extract refresh URL.
	reURL := regexp.MustCompile(`\[Refresh from source\]\(([^)]+)\)`)
	var url string
	if m := reURL.FindStringSubmatch(raw); m != nil {
		url = m[1]
	}

	return ast.BlockContent{
		StaleBanner: &ast.StaleBannerContent{
			TriggeringCommit: sha,
			RefreshURL:       url,
		},
	}, nil
}

