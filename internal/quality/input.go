// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package quality provides template- and audience-scoped validators
// for generated documentation pages.
//
// Architecture note: validators operate on [ValidationInput], an
// interface that today wraps rendered markdown. When D.2's canonical
// Page AST is implemented, a typed AST adapter can be dropped in
// without changing any validator logic. Callers should always
// construct inputs via [NewMarkdownInput].
package quality

import (
	"regexp"
	"strings"
	"unicode"
)

// ValidationInput is the abstraction boundary between validators and
// the page representation they inspect. Today it wraps rendered
// markdown; tomorrow it can wrap the D.2 Page AST.
//
// All methods must be safe to call concurrently after construction.
type ValidationInput interface {
	// RawText returns the full page content as a plain string.
	// For markdown, this is the source; for AST-backed inputs it
	// should be the rendered text without markup.
	RawText() string

	// Markdown returns the raw markdown source (empty string when
	// the input does not originate from markdown).
	Markdown() string

	// WordCount returns the number of prose words, excluding code
	// blocks and headings.
	WordCount() int

	// TopLevelBlocks returns the count of top-level structural
	// blocks: headings at H1/H2 level, fenced code blocks, and
	// blockquotes that immediately follow a heading.
	TopLevelBlocks() int

	// CodeBlocks returns the raw contents of every fenced code
	// block in the page, in document order.
	CodeBlocks() []string

	// SectionBodies returns a map from section title (lowercased,
	// stripped of leading '#' and whitespace) to the prose body of
	// that section (everything after the heading until the next
	// heading).
	SectionBodies() map[string]string

	// Citations returns every citation string found in the page.
	// Citations are substrings matching the pattern
	// "(path:start-end)" or "(sym_...)" per the shared citation
	// contract in internal/citations.
	Citations() []string
}

// MarkdownInput is the concrete [ValidationInput] backed by rendered
// markdown source. Construct via [NewMarkdownInput].
type MarkdownInput struct {
	raw string

	// Lazily computed; guarded by the fact that construction is
	// single-threaded and reads are read-only after that.
	wordCount      int
	topLevelBlocks int
	codeBlocks     []string
	sectionBodies  map[string]string
	citationList   []string
}

// Compile-time interface check.
var _ ValidationInput = (*MarkdownInput)(nil)

var (
	// Citation pattern: (path:N-M) or (sym_...) — capturing the interior
	reCitation = regexp.MustCompile(`\(([a-zA-Z0-9_./-]+:\d+(?:-\d+)?|sym_[A-Za-z0-9_.-]+)\)`)

	// Any heading level
	reHeading = regexp.MustCompile(`(?m)^(#{1,6})\s+(.+)$`)

	// (reFencedBlock and reH1H2 used to live here for an earlier
	// regex-based parser; the current parse() walks lines manually
	// and detects fences with strings.HasPrefix, so the regexes are
	// dead. Removed to satisfy lint.)
)

// NewMarkdownInput parses src and returns a fully-initialized
// [MarkdownInput]. Construction is O(n) in the length of src.
func NewMarkdownInput(src string) *MarkdownInput {
	m := &MarkdownInput{raw: src}
	m.parse()
	return m
}

func (m *MarkdownInput) RawText() string        { return m.raw }
func (m *MarkdownInput) Markdown() string        { return m.raw }
func (m *MarkdownInput) WordCount() int          { return m.wordCount }
func (m *MarkdownInput) TopLevelBlocks() int     { return m.topLevelBlocks }
func (m *MarkdownInput) CodeBlocks() []string    { return m.codeBlocks }
func (m *MarkdownInput) SectionBodies() map[string]string { return m.sectionBodies }
func (m *MarkdownInput) Citations() []string     { return m.citationList }

// parse performs all analysis in one pass over the source.
func (m *MarkdownInput) parse() {
	// Use multiline block matching on the raw string. Code blocks are
	// captured in m.codeBlocks; the byte-offset / line-range tracking
	// (codeSpans / codeLines) used to live here for callers that wanted
	// to exclude code-block bytes from word counting, but nothing reads
	// either slice today. Removed to satisfy lint; reintroduce when a
	// validator that needs the offsets lands.
	lines := strings.Split(m.raw, "\n")

	inFence := false
	fenceChar := ""
	fenceStart := 0
	var curBlockLines []string

	for i, line := range lines {
		stripped := strings.TrimLeft(line, " \t")
		if !inFence {
			if strings.HasPrefix(stripped, "```") || strings.HasPrefix(stripped, "~~~") {
				inFence = true
				fenceChar = stripped[:3]
				fenceStart = i
				curBlockLines = nil
			}
		} else {
			if strings.HasPrefix(stripped, fenceChar) {
				// Closing fence.
				m.codeBlocks = append(m.codeBlocks, strings.Join(curBlockLines, "\n"))
				inFence = false
			} else {
				curBlockLines = append(curBlockLines, line)
			}
		}
	}

	_ = fenceStart // span-tracking would use this; preserved for future use

	// Count top-level blocks: H1+H2 headings and fenced code blocks.
	topLevel := 0
	for _, line := range lines {
		stripped := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(stripped, "# ") || strings.HasPrefix(stripped, "## ") {
			topLevel++
		}
	}
	topLevel += len(m.codeBlocks)
	m.topLevelBlocks = topLevel

	// Count prose words (skip lines that are inside code blocks or
	// are headings).
	inCode := false
	fenceChar2 := ""
	wordTotal := 0
	for _, line := range lines {
		stripped := strings.TrimLeft(line, " \t")
		if !inCode {
			if strings.HasPrefix(stripped, "```") || strings.HasPrefix(stripped, "~~~") {
				inCode = true
				fenceChar2 = stripped[:3]
				continue
			}
			if strings.HasPrefix(stripped, "#") {
				continue // skip headings
			}
			wordTotal += countWords(line)
		} else {
			if strings.HasPrefix(stripped, fenceChar2) {
				inCode = false
			}
		}
	}
	m.wordCount = wordTotal

	// Build section bodies: map heading title → prose between this
	// heading and the next heading at any level.
	m.sectionBodies = make(map[string]string)
	var currentTitle string
	var bodyLines []string
	inCode2 := false
	fenceChar3 := ""

	flush := func() {
		if currentTitle == "" {
			return
		}
		body := strings.TrimSpace(strings.Join(bodyLines, "\n"))
		if existing, ok := m.sectionBodies[currentTitle]; ok {
			// Duplicate heading: append to existing body.
			m.sectionBodies[currentTitle] = existing + "\n" + body
		} else {
			m.sectionBodies[currentTitle] = body
		}
		bodyLines = nil
	}

	for _, line := range lines {
		stripped := strings.TrimLeft(line, " \t")
		if !inCode2 {
			if strings.HasPrefix(stripped, "```") || strings.HasPrefix(stripped, "~~~") {
				inCode2 = true
				fenceChar3 = stripped[:3]
				bodyLines = append(bodyLines, line)
				continue
			}
			if match := reHeading.FindStringSubmatch(line); match != nil {
				flush()
				title := strings.ToLower(strings.TrimSpace(match[2]))
				currentTitle = title
			} else {
				bodyLines = append(bodyLines, line)
			}
		} else {
			bodyLines = append(bodyLines, line)
			if strings.HasPrefix(stripped, fenceChar3) {
				inCode2 = false
			}
		}
	}
	flush()

	// Extract citations.
	matches := reCitation.FindAllStringSubmatch(m.raw, -1)
	for _, m2 := range matches {
		if len(m2) > 1 {
			m.citationList = append(m.citationList, m2[1])
		}
	}
}

// countWords counts space-delimited tokens in a single line that
// contain at least one letter or digit.
func countWords(line string) int {
	n := 0
	for _, field := range strings.FieldsFunc(line, func(r rune) bool {
		return unicode.IsSpace(r)
	}) {
		for _, r := range field {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				n++
				break
			}
		}
	}
	return n
}
