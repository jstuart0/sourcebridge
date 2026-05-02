// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package quality

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"

	"github.com/sourcebridge/sourcebridge/internal/citations"
)

// ValidatorID is the stable string identifier for a validator.
// These IDs appear in profiles, results, and PR descriptions.
type ValidatorID string

const (
	ValidatorVagueness            ValidatorID = "vagueness"
	ValidatorEmptyHeadline        ValidatorID = "empty_headline"
	ValidatorCodeExamplePresent   ValidatorID = "code_example_present"
	ValidatorCitationDensity      ValidatorID = "citation_density"
	ValidatorReadingLevel         ValidatorID = "reading_level"
	ValidatorArchitecturalRelevance ValidatorID = "architectural_relevance"
	ValidatorFactualGrounding     ValidatorID = "factual_grounding"
	ValidatorBlockCount           ValidatorID = "block_count"
)

// Violation records a single issue found during validation.
type Violation struct {
	// Line is the 1-based line number where the issue was found, or 0
	// when the issue applies to the page as a whole.
	Line int
	// Column is the 0-based byte offset within the line, or 0 when
	// not applicable.
	Column int
	// Message is a human-readable description of the problem.
	Message string
	// Suggestion is a short actionable fix. May be empty.
	Suggestion string
	// Excerpt is the offending text, truncated to 100 bytes.
	Excerpt string
}

// Validator is the interface every quality check implements.
// Each Validator is stateless and safe for concurrent use.
type Validator interface {
	// ID returns the stable identifier for this validator.
	ID() ValidatorID
	// Validate runs the check against input and returns any violations.
	// An empty slice means the check passed.
	Validate(input ValidationInput, cfg ValidatorConfig) []Violation
}

// ValidatorConfig carries per-template, per-validator thresholds.
// Fields are optional; validators fall back to defaults when zero.
type ValidatorConfig struct {
	// CitationDensityWordsPerCitation is the maximum words-per-citation
	// ratio (lower = denser). E.g. 200 means ≥1 citation per 200 words.
	// Defaults to 200 when zero.
	CitationDensityWordsPerCitation int

	// ReadingLevelFloor is the minimum Flesch reading-ease score.
	// Scores below this floor trigger a violation. Defaults to 50.
	ReadingLevelFloor float64

	// ArchRelevanceMinPageRefs is the minimum number of other pages
	// that must reference this page's subject.
	// Defaults to 1 when zero.
	ArchRelevanceMinPageRefs int

	// ArchRelevanceMinGraphRelations is the minimum graph-relation count.
	// Defaults to 3 when zero.
	ArchRelevanceMinGraphRelations int

	// BlockCountMin is the minimum number of top-level blocks.
	// Defaults to 2 when zero.
	BlockCountMin int

	// BlockCountMax is the maximum number of top-level blocks.
	// 0 means no upper limit.
	BlockCountMax int

	// PageReferenceCount is the number of other pages that reference
	// this page's subject. Supplied by the caller from the graph store.
	// Validators that need this field must document that the caller
	// is responsible for providing it.
	PageReferenceCount int

	// GraphRelationCount is the number of graph relations the subject
	// participates in.
	GraphRelationCount int
}

// --- Vagueness validator ---

// vaguenessValidator flags imprecise quantifiers that appear without
// an adjacent numeral. The set covers the plan's listed terms plus
// obvious variants.
type vaguenessValidator struct{}

func (vaguenessValidator) ID() ValidatorID { return ValidatorVagueness }

var (
	// Terms that are vague when not immediately followed by a digit.
	vaguenessTerms = []string{
		"various", "many", "several", "a number of", "in some cases",
		"some cases", "in certain cases", "some situations", "sometimes",
		"often", "occasionally", "numerous", "multiple", "a lot",
	}

	// Digit nearby: a numeral within 30 chars before or after the term.
	reNearbyDigit = regexp.MustCompile(`\d`)
)

func (v vaguenessValidator) Validate(input ValidationInput, _ ValidatorConfig) []Violation {
	var violations []Violation
	lines := strings.Split(input.Markdown(), "\n")
	inCode := false
	fenceChar := ""

	for lineNum, line := range lines {
		stripped := strings.TrimLeft(line, " \t")
		if !inCode {
			if strings.HasPrefix(stripped, "```") || strings.HasPrefix(stripped, "~~~") {
				inCode = true
				fenceChar = stripped[:3]
				continue
			}
		} else {
			if strings.HasPrefix(stripped, fenceChar) {
				inCode = false
			}
			continue
		}

		lower := strings.ToLower(line)
		for _, term := range vaguenessTerms {
			idx := strings.Index(lower, term)
			for idx >= 0 {
				// Check for a nearby digit in a 30-char window.
				windowStart := max(0, idx-30)
				windowEnd := min(len(line), idx+len(term)+30)
				window := line[windowStart:windowEnd]
				if !reNearbyDigit.MatchString(window) {
					excerpt := line
					if len(excerpt) > 100 {
						excerpt = excerpt[:100]
					}
					violations = append(violations, Violation{
						Line:       lineNum + 1,
						Column:     idx,
						Message:    fmt.Sprintf("vague quantifier %q without adjacent numeral", term),
						Suggestion: "Replace with a specific count or qualifier, or delete.",
						Excerpt:    excerpt,
					})
				}
				// Find next occurrence.
				rest := idx + len(term)
				if rest >= len(lower) {
					break
				}
				nextIdx := strings.Index(lower[rest:], term)
				if nextIdx < 0 {
					break
				}
				idx = rest + nextIdx
			}
		}
	}
	return violations
}

// --- EmptyHeadline validator ---

type emptyHeadlineValidator struct{}

func (emptyHeadlineValidator) ID() ValidatorID { return ValidatorEmptyHeadline }

// Titles that are suspect when their body has ≤1 non-empty sentence.
var emptyHeadlineTitles = map[string]bool{
	"overview": true, "notes": true, "background": true,
	"introduction": true, "summary": true, "additional notes": true,
	"note": true, "misc": true, "miscellaneous": true, "other": true,
}

// (reSentenceEnd used to live here for splitting bodies on sentence
// boundaries; the current empty-headline validator counts non-empty
// lines instead of sentences. Removed to satisfy lint.)

func (e emptyHeadlineValidator) Validate(input ValidationInput, _ ValidatorConfig) []Violation {
	var violations []Violation
	bodies := input.SectionBodies()

	// Build line-number index for headings.
	lines := strings.Split(input.Markdown(), "\n")
	headingLines := map[string]int{}
	for i, line := range lines {
		if m := reHeading.FindStringSubmatch(line); m != nil {
			title := strings.ToLower(strings.TrimSpace(m[2]))
			if _, exists := headingLines[title]; !exists {
				headingLines[title] = i + 1
			}
		}
	}

	for title, body := range bodies {
		baseTitle := strings.TrimRight(title, " \t*_")
		baseTitle = strings.TrimLeft(baseTitle, " \t*_")
		if !emptyHeadlineTitles[baseTitle] {
			continue
		}
		// Strip code blocks from body before counting sentences.
		prose := stripCodeBlocks(body)
		sentences := countSentences(prose)
		if sentences <= 1 {
			lineNum := headingLines[title]
			violations = append(violations, Violation{
				Line:    lineNum,
				Message: fmt.Sprintf("section %q has a generic title and ≤1 sentence of content", title),
				Suggestion: "Rename the section to describe its specific content, or expand it to at " +
					"least 2 substantive sentences.",
				Excerpt: truncate(body, 100),
			})
		}
	}
	return violations
}

func countSentences(text string) int {
	// Count sentence-ending punctuation as a proxy for sentence count.
	// This is intentionally simple — we care about "roughly zero content"
	// detection, not precise NLP.
	count := 0
	for _, ch := range text {
		if ch == '.' || ch == '!' || ch == '?' {
			count++
		}
	}
	return count
}

func stripCodeBlocks(text string) string {
	lines := strings.Split(text, "\n")
	var out []string
	inCode := false
	fenceChar := ""
	for _, line := range lines {
		stripped := strings.TrimLeft(line, " \t")
		if !inCode {
			if strings.HasPrefix(stripped, "```") || strings.HasPrefix(stripped, "~~~") {
				inCode = true
				fenceChar = stripped[:3]
				continue
			}
			out = append(out, line)
		} else {
			if strings.HasPrefix(stripped, fenceChar) {
				inCode = false
			}
		}
	}
	return strings.Join(out, "\n")
}

// --- CodeExamplePresent validator ---

type codeExamplePresentValidator struct{}

func (codeExamplePresentValidator) ID() ValidatorID { return ValidatorCodeExamplePresent }

func (c codeExamplePresentValidator) Validate(input ValidationInput, _ ValidatorConfig) []Violation {
	if len(input.CodeBlocks()) > 0 {
		return nil
	}
	return []Violation{{
		Line:    0,
		Message: "page has no code blocks",
		Suggestion: "Add at least one code block extracted from the indexed repository " +
			"to ground the documentation in real examples.",
	}}
}

// --- CitationDensity validator ---

type citationDensityValidator struct{}

func (citationDensityValidator) ID() ValidatorID { return ValidatorCitationDensity }

func (c citationDensityValidator) Validate(input ValidationInput, cfg ValidatorConfig) []Violation {
	threshold := cfg.CitationDensityWordsPerCitation
	if threshold <= 0 {
		threshold = 200
	}

	words := input.WordCount()
	citationStrings := input.Citations()

	// Also count citations via the canonical citations package.
	parsedCount := 0
	for _, s := range citationStrings {
		if _, ok := citations.Parse(s); ok {
			parsedCount++
		}
	}

	if words == 0 {
		return nil // no prose to check
	}

	required := int(math.Ceil(float64(words) / float64(threshold)))
	if parsedCount >= required {
		return nil
	}

	var msg string
	if parsedCount == 0 {
		msg = fmt.Sprintf(
			"citation density too low: 0 citations for %d words (need ≥1 per %d words)",
			words, threshold,
		)
	} else {
		msg = fmt.Sprintf(
			"citation density too low: %d citation(s) for %d words (need ≥1 per %d words; have 1 per ~%d words)",
			parsedCount, words, threshold, wordsPerCitation(words, parsedCount),
		)
	}
	return []Violation{{
		Line:       0,
		Message:    msg,
		Suggestion: "Add source citations in the format (path:start-end) to ground assertions in indexed code.",
	}}
}

// wordsPerCitation returns the words-per-citation ratio. Returns 0 when
// there are no citations — denotes "no citations" rather than misleading
// callers into thinking 1-per-words is the actual ratio.
func wordsPerCitation(words, citations int) int {
	if citations == 0 {
		return 0
	}
	return words / citations
}

// --- ReadingLevel validator ---

type readingLevelValidator struct{}

func (readingLevelValidator) ID() ValidatorID { return ValidatorReadingLevel }

func (r readingLevelValidator) Validate(input ValidationInput, cfg ValidatorConfig) []Violation {
	floor := cfg.ReadingLevelFloor
	if floor <= 0 {
		floor = 50.0
	}

	prose := stripCodeBlocks(input.Markdown())
	score := fleschReadingEase(prose)

	if score >= floor {
		return nil
	}

	return []Violation{{
		Line: 0,
		Message: fmt.Sprintf(
			"reading level too low: Flesch score %.1f (floor %.1f) — prose is dense or uses complex sentences",
			score, floor,
		),
		Suggestion: "Shorten sentences and prefer common words. Aim for ≤25 words per sentence " +
			"and ≤3 syllables for technical terms that have shorter equivalents.",
	}}
}

// fleschReadingEase computes the Flesch reading-ease score for text.
// The formula: 206.835 - 1.015*(words/sentences) - 84.6*(syllables/words)
// Higher scores = easier reading. Typical technical prose: 30–60.
func fleschReadingEase(text string) float64 {
	words := 0
	sentences := 0
	syllables := 0

	for _, para := range strings.Split(text, "\n") {
		para = strings.TrimSpace(para)
		if para == "" || strings.HasPrefix(para, "#") {
			continue
		}
		for _, word := range strings.Fields(para) {
			// Strip punctuation.
			clean := strings.TrimFunc(word, func(r rune) bool {
				return !unicode.IsLetter(r) && !unicode.IsDigit(r)
			})
			if clean == "" {
				continue
			}
			words++
			syllables += syllableCount(clean)

			// Count sentence endings.
			if strings.HasSuffix(word, ".") || strings.HasSuffix(word, "!") ||
				strings.HasSuffix(word, "?") || strings.HasSuffix(word, "...") {
				sentences++
			}
		}
	}

	if words == 0 || sentences == 0 {
		return 0
	}

	asl := float64(words) / float64(sentences)
	asw := float64(syllables) / float64(words)
	return 206.835 - 1.015*asl - 84.6*asw
}

// syllableCount approximates English syllable count using vowel-group
// heuristics. Not linguistically rigorous, but reproducible and
// directionally correct for technical prose.
func syllableCount(word string) int {
	word = strings.ToLower(word)
	count := 0
	prevVowel := false
	vowels := "aeiouy"
	for i, ch := range word {
		isVowel := strings.ContainsRune(vowels, ch)
		if isVowel && !prevVowel {
			count++
		}
		prevVowel = isVowel
		_ = i
	}
	// Silent trailing 'e' often reduces a syllable.
	if strings.HasSuffix(word, "e") && count > 1 {
		count--
	}
	if count == 0 {
		count = 1
	}
	return count
}

// --- ArchitecturalRelevance validator ---

type architecturalRelevanceValidator struct{}

func (architecturalRelevanceValidator) ID() ValidatorID { return ValidatorArchitecturalRelevance }

func (a architecturalRelevanceValidator) Validate(input ValidationInput, cfg ValidatorConfig) []Violation {
	minRefs := cfg.ArchRelevanceMinPageRefs
	if minRefs <= 0 {
		minRefs = 1
	}
	minRelations := cfg.ArchRelevanceMinGraphRelations
	if minRelations <= 0 {
		minRelations = 3
	}

	pageRefs := cfg.PageReferenceCount
	graphRels := cfg.GraphRelationCount

	if pageRefs >= minRefs || graphRels >= minRelations {
		return nil
	}

	return []Violation{{
		Line: 0,
		Message: fmt.Sprintf(
			"page subject has low architectural relevance: %d page reference(s) (need ≥%d) and "+
				"%d graph relation(s) (need ≥%d); consider whether this subject deserves its own page",
			pageRefs, minRefs, graphRels, minRelations,
		),
		Suggestion: "This subject may be better as a subsection of its parent package's page " +
			"rather than a standalone page.",
	}}
}

// --- FactualGrounding validator ---

type factualGroundingValidator struct{}

func (factualGroundingValidator) ID() ValidatorID { return ValidatorFactualGrounding }

// reAssertionPattern matches sentences that make factual claims about
// code behavior: "returns", "accepts", "throws", "emits", "stores",
// etc. These claims require a citation.
var reAssertionPattern = regexp.MustCompile(
	`(?i)\b(returns?|accepts?|throws?|panics?|emits?|stores?|writes?|reads?|sends?|receives?|calls?|invokes?|validates?|checks?|ensures?|guarantees?)\b`,
)

func (f factualGroundingValidator) Validate(input ValidationInput, _ ValidatorConfig) []Violation {
	var violations []Violation

	// Index which line numbers have a citation in the same paragraph.
	// A citation on any line within a paragraph covers all assertion
	// lines in that paragraph.
	lines := strings.Split(input.Markdown(), "\n")
	inCode := false
	fenceChar := ""

	// Group lines into paragraphs (separated by blank lines or headings).
	type paragraph struct {
		startLine int
		lines     []string
	}
	var paragraphs []paragraph
	var cur paragraph
	cur.startLine = 1

	for i, line := range lines {
		stripped := strings.TrimLeft(line, " \t")
		if !inCode {
			if strings.HasPrefix(stripped, "```") || strings.HasPrefix(stripped, "~~~") {
				inCode = true
				fenceChar = stripped[:3]
				// Flush current paragraph.
				if len(cur.lines) > 0 {
					paragraphs = append(paragraphs, cur)
					cur = paragraph{startLine: i + 2}
				}
				continue
			}
			isBlank := strings.TrimSpace(line) == ""
			isHeading := strings.HasPrefix(stripped, "#")
			if isBlank || isHeading {
				if len(cur.lines) > 0 {
					paragraphs = append(paragraphs, cur)
					cur = paragraph{startLine: i + 2}
				}
			} else {
				if len(cur.lines) == 0 {
					cur.startLine = i + 1
				}
				cur.lines = append(cur.lines, line)
			}
		} else {
			if strings.HasPrefix(stripped, fenceChar) {
				inCode = false
				cur = paragraph{startLine: i + 2}
			}
		}
	}
	if len(cur.lines) > 0 {
		paragraphs = append(paragraphs, cur)
	}

	for _, para := range paragraphs {
		paraText := strings.Join(para.lines, " ")

		// If the paragraph has a citation anywhere, it is grounded.
		if reCitation.MatchString(paraText) {
			continue
		}

		// If the paragraph makes assertion-pattern claims, flag it.
		if reAssertionPattern.MatchString(paraText) {
			excerpt := paraText
			if len(excerpt) > 100 {
				excerpt = excerpt[:100]
			}
			violations = append(violations, Violation{
				Line:    para.startLine,
				Message: "paragraph makes behavioral assertions without a citation",
				Suggestion: "Add a source citation (path:start-end) for each behavioral claim, " +
					"or rephrase as architecture-level description rather than behavioral assertion.",
				Excerpt: excerpt,
			})
		}
	}

	return violations
}

// --- BlockCount validator ---

type blockCountValidator struct{}

func (blockCountValidator) ID() ValidatorID { return ValidatorBlockCount }

func (b blockCountValidator) Validate(input ValidationInput, cfg ValidatorConfig) []Violation {
	minBlocks := cfg.BlockCountMin
	if minBlocks <= 0 {
		minBlocks = 2
	}
	maxBlocks := cfg.BlockCountMax // 0 = no upper limit

	count := input.TopLevelBlocks()

	if count < minBlocks {
		return []Violation{{
			Line: 0,
			Message: fmt.Sprintf(
				"page has only %d top-level block(s); minimum is %d — this is likely a stub",
				count, minBlocks,
			),
			Suggestion: "Expand the page or merge it as a subsection of its parent.",
		}}
	}
	if maxBlocks > 0 && count > maxBlocks {
		return []Violation{{
			Line: 0,
			Message: fmt.Sprintf(
				"page has %d top-level blocks; maximum is %d — this page may need to be split",
				count, maxBlocks,
			),
			Suggestion: "Consider extracting subsystems into their own pages and linking from here.",
		}}
	}
	return nil
}

// --- Registry ---

// allValidators holds every registered validator in a fixed order.
// This order determines the order violations appear in results.
var allValidators = []Validator{
	vaguenessValidator{},
	emptyHeadlineValidator{},
	codeExamplePresentValidator{},
	citationDensityValidator{},
	readingLevelValidator{},
	architecturalRelevanceValidator{},
	factualGroundingValidator{},
	blockCountValidator{},
}

// ValidatorByID returns the named validator, or (nil, false) when not found.
func ValidatorByID(id ValidatorID) (Validator, bool) {
	for _, v := range allValidators {
		if v.ID() == id {
			return v, true
		}
	}
	return nil, false
}

// AllValidators returns all registered validators in deterministic order.
func AllValidators() []Validator {
	out := make([]Validator, len(allValidators))
	copy(out, allValidators)
	return out
}

// --- Helpers ---

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
