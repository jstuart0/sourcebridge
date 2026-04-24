// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"
	"path"
	"regexp"
	"sort"
	"strings"
)

// TestHit describes one unit-test discovery returned by find_tests.
// Fields parallel the tool-result JSON shape; internal callers use
// this value directly.
type TestHit struct {
	Handle          string   `json:"handle"`
	FilePath        string   `json:"file_path"`
	StartLine       int      `json:"start_line"`
	EndLine         int      `json:"end_line"`
	TestName        string   `json:"test_name"`
	SubjectName     string   `json:"subject_name,omitempty"`
	SubjectFilePath string   `json:"subject_file_path,omitempty"`
	Content         string   `json:"content"`
	Assertions      []string `json:"assertions,omitempty"`
	Score           float64  `json:"score,omitempty"`
}

// testFrame is an internal (pre-ranked) per-function slice.
type testFrame struct {
	FilePath   string
	StartLine  int
	EndLine    int
	TestName   string
	Body       string
	SubjectHit int // how many times the subject name appears in the body
	Adjacent   bool
}

// Test-file path patterns, ordered by specificity. A file is a
// candidate test when any pattern matches.
var testFilePatterns = []*regexp.Regexp{
	regexp.MustCompile(`_test\.go$`),
	regexp.MustCompile(`(^|/)test_[^/]+\.py$`),
	regexp.MustCompile(`_test\.py$`),
	regexp.MustCompile(`\.test\.tsx?$`),
	regexp.MustCompile(`\.spec\.tsx?$`),
	regexp.MustCompile(`(^|/)tests?/[^/]+\.(py|go|ts|tsx|js|jsx)$`),
	regexp.MustCompile(`Tests?\.(java|cs|kt|swift)$`),
	regexp.MustCompile(`_spec\.rb$`),
}

// assertionRe matches the first token of common assertion APIs.
// Kept cheap — the goal is to flag "this test body contains
// assertions", not fully parse them.
var assertionRe = regexp.MustCompile(
	`(?m)^\s*(?:` +
		`assert[A-Za-z]*\s*\(|` +
		`expect\s*\(|` +
		`require\.\w+\s*\(|` +
		`t\.(?:Errorf|Fatalf|Fatal|Error)\s*\(|` +
		`assertEqual\s*\(|` +
		`assertTrue\s*\(|` +
		`\bshould\.|` +
		`ok\s*\(|` +
		`is\s*\(` +
		`)`,
)

// isTestFile returns true when the path looks like a test file.
func isTestFile(filePath string) bool {
	p := strings.ReplaceAll(filePath, "\\", "/")
	for _, re := range testFilePatterns {
		if re.MatchString(p) {
			return true
		}
	}
	return false
}

// adjacentTestCandidates returns plausible test-file paths for a
// given source file, using language convention rather than a live
// filesystem walk. Paths returned are repo-relative; the caller
// must read and filter by existence.
//
// Examples:
//
//	internal/qa/pipeline.go →
//	  - internal/qa/pipeline_test.go
//	workers/foo/bar.py →
//	  - workers/foo/test_bar.py
//	  - workers/foo/bar_test.py
//	  - workers/tests/test_bar.py
//	  - workers/foo/tests/test_bar.py
//	web/src/auth/session.ts →
//	  - web/src/auth/session.test.ts
//	  - web/src/auth/session.spec.ts
//	  - web/src/auth/__tests__/session.test.ts
// AdjacentTestCandidates is the exported alias used by the MCP
// accessor tools (get_tests_for_symbol). Keeping both names avoids
// a sweeping rename of the 10-ish internal call sites; the exported
// form is the public contract.
func AdjacentTestCandidates(filePath string) []string {
	return adjacentTestCandidates(filePath)
}

func adjacentTestCandidates(filePath string) []string {
	filePath = strings.ReplaceAll(filePath, "\\", "/")
	dir, base := path.Split(filePath)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)

	var out []string
	switch ext {
	case ".go":
		out = append(out, dir+stem+"_test.go")
	case ".py":
		out = append(out,
			dir+"test_"+stem+".py",
			dir+stem+"_test.py",
			// tests/ next to the source file
			dir+"tests/test_"+stem+".py",
			// a sibling tests/ directory
			siblingDir(dir, "tests")+"test_"+stem+".py",
		)
	case ".ts", ".tsx":
		out = append(out,
			dir+stem+".test"+ext,
			dir+stem+".spec"+ext,
			dir+"__tests__/"+stem+".test"+ext,
			dir+"__tests__/"+stem+".spec"+ext,
		)
	case ".js", ".jsx":
		out = append(out,
			dir+stem+".test"+ext,
			dir+stem+".spec"+ext,
		)
	case ".java":
		// Java tests typically mirror src/main/java → src/test/java.
		out = append(out,
			strings.Replace(filePath, "src/main/java", "src/test/java", 1),
			dir+stem+"Test.java",
		)
	case ".rb":
		out = append(out, dir+stem+"_spec.rb")
	}
	return uniqueStrings(out)
}

// siblingDir returns the path "../<name>/" relative to `dir`.
// Used to build "tests/" folders one level up from the source dir.
func siblingDir(dir, name string) string {
	if dir == "" {
		return name + "/"
	}
	clean := strings.TrimRight(dir, "/")
	parentSlash := strings.LastIndex(clean, "/")
	if parentSlash < 0 {
		return name + "/"
	}
	return clean[:parentSlash+1] + name + "/"
}

// findTestFunctions scans a test file's content for top-level test
// declarations. Returns one testFrame per test. Language-agnostic
// regex bundle; false positives are OK (rejected by the name
// filter later) and are preferable to missing a test.
func findTestFunctions(filePath, body string) []testFrame {
	lines := strings.Split(body, "\n")
	frames := []testFrame{}

	// Language detection by extension.
	ext := strings.ToLower(path.Ext(filePath))
	var decl *regexp.Regexp
	switch ext {
	case ".go":
		decl = regexp.MustCompile(`^func\s+(Test[A-Z][A-Za-z0-9_]*)\s*\(`)
	case ".py":
		decl = regexp.MustCompile(`^\s*def\s+(test_[A-Za-z0-9_]+)\s*\(`)
	case ".ts", ".tsx", ".js", ".jsx":
		// describe( / it( / test( — capture the label.
		decl = regexp.MustCompile(`^\s*(?:it|test|describe)\s*\(\s*['"` + "`" + `]([^'"` + "`" + `]+)['"` + "`" + `]`)
	case ".java":
		decl = regexp.MustCompile(`^\s*(?:public|private|protected)?\s*(?:static\s+)?(?:void|[A-Z][A-Za-z0-9_<>]+)\s+(test[A-Za-z0-9_]+)\s*\(`)
	case ".rb":
		decl = regexp.MustCompile(`^\s*it\s+['"]([^'"]+)['"]`)
	default:
		return frames
	}

	// Scan for matches; compute a rough end-line by brace-balancing
	// or dedent. A cheap heuristic works here — we're showing the
	// LLM a slice, not performing AST analysis.
	for i, line := range lines {
		m := decl.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		start := i + 1
		end := endOfBlock(lines, i, ext)
		if end-start+1 > 120 {
			end = start + 120 - 1
		}
		if end > len(lines) {
			end = len(lines)
		}
		frames = append(frames, testFrame{
			FilePath:  filePath,
			StartLine: start,
			EndLine:   end,
			TestName:  m[1],
			Body:      strings.Join(lines[start-1:end], "\n"),
		})
	}
	return frames
}

// endOfBlock estimates the last line of a test function starting at
// `startIdx`. Go / Java / JS use brace counting; Python uses dedent.
// Returns a 1-based line number.
func endOfBlock(lines []string, startIdx int, ext string) int {
	switch ext {
	case ".go", ".java", ".ts", ".tsx", ".js", ".jsx":
		depth := 0
		seenOpen := false
		for i := startIdx; i < len(lines); i++ {
			for _, ch := range lines[i] {
				if ch == '{' {
					depth++
					seenOpen = true
				} else if ch == '}' {
					depth--
					if seenOpen && depth <= 0 {
						return i + 1
					}
				}
			}
		}
	case ".py":
		// Indent of the `def` line sets the baseline; the block ends
		// at the first non-empty line at or below that indent after
		// the block started.
		baseIndent := indentOf(lines[startIdx])
		for i := startIdx + 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "" {
				continue
			}
			if indentOf(lines[i]) <= baseIndent {
				return i
			}
		}
	case ".rb":
		// it 'x' do ... end — count `do`/`end`.
		depth := 1
		for i := startIdx + 1; i < len(lines); i++ {
			if regexp.MustCompile(`\bdo\b|\bdef\b`).MatchString(lines[i]) {
				depth++
			}
			if regexp.MustCompile(`\bend\b`).MatchString(lines[i]) {
				depth--
				if depth <= 0 {
					return i + 1
				}
			}
		}
	}
	return startIdx + 60 // fall-through cap so we always return something
}

func indentOf(line string) int {
	n := 0
	for _, r := range line {
		if r == ' ' {
			n++
		} else if r == '\t' {
			n += 4
		} else {
			break
		}
	}
	return n
}

// extractAssertions pulls up to 5 assertion lines out of a test
// body. Trims leading whitespace; drops duplicates.
func extractAssertions(body string) []string {
	matches := assertionRe.FindAllStringIndex(body, -1)
	if len(matches) == 0 {
		return nil
	}
	lines := strings.Split(body, "\n")
	offsets := []int{0}
	for i, l := range lines {
		if i == 0 {
			continue
		}
		offsets = append(offsets, offsets[len(offsets)-1]+len(lines[i-1])+1)
		_ = l
	}
	seen := map[string]struct{}{}
	out := []string{}
	for _, m := range matches {
		lineIdx := sort.SearchInts(offsets, m[0])
		if lineIdx > len(lines) {
			continue
		}
		if lineIdx < 0 || lineIdx >= len(lines) {
			continue
		}
		l := strings.TrimSpace(lines[lineIdx])
		if l == "" {
			continue
		}
		if len(l) > 200 {
			l = l[:200] + "..."
		}
		if _, dup := seen[l]; dup {
			continue
		}
		seen[l] = struct{}{}
		out = append(out, l)
		if len(out) >= 5 {
			break
		}
	}
	return out
}

// TestFinder locates and slices tests relevant to a target symbol
// or file. It uses the orchestrator's FileReader for content and
// optionally the Searcher for cross-package discovery.
type TestFinder struct {
	o      *Orchestrator
	repoID string
}

// NewTestFinder binds a finder to one repo.
func NewTestFinder(o *Orchestrator, repoID string) *TestFinder {
	return &TestFinder{o: o, repoID: repoID}
}

// FindForSymbol returns the top N tests exercising a given symbol.
// Strategy: (1) read adjacent test files based on the symbol's
// source path, (2) rank test functions by subject-name
// co-occurrence, (3) fall back to searcher when adjacent misses.
func (f *TestFinder) FindForSymbol(ctx context.Context, symbolID string, limit int) ([]TestHit, error) {
	if f == nil || f.o == nil || f.o.symbols == nil {
		return nil, nil
	}
	sourcePath := f.o.symbols.SymbolFilePath(symbolID)
	if sourcePath == "" {
		return nil, nil
	}
	// Find the symbol's display name for content matching.
	var name string
	for _, ref := range f.o.symbols.SymbolsInFile(f.repoID, sourcePath) {
		if ref.ID == symbolID {
			name = ref.Name
			break
		}
	}
	if name == "" {
		return nil, nil
	}
	frames := f.collectAdjacentFrames(sourcePath, name)
	frames = f.augmentFromSearch(ctx, name, frames)

	hits := rankAndSlice(frames, name, sourcePath, limit)
	return hits, nil
}

// FindForFile returns tests likely to cover code in the given file.
// Adjacent test files + ranked by mentions of any symbol from the
// file.
func (f *TestFinder) FindForFile(ctx context.Context, filePath string, limit int) ([]TestHit, error) {
	if f == nil || f.o == nil {
		return nil, nil
	}
	// Collect symbol names defined in the source file, used as
	// content-match needles in candidate test bodies.
	var needles []string
	if f.o.symbols != nil {
		for _, ref := range f.o.symbols.SymbolsInFile(f.repoID, filePath) {
			if ref.Name != "" {
				needles = append(needles, ref.Name)
			}
		}
	}
	// Use the source file's base name as a fallback needle so tests
	// that import or touch the module (but don't mention a specific
	// symbol) still surface.
	stem := strings.TrimSuffix(path.Base(filePath), path.Ext(filePath))
	if stem != "" {
		needles = append(needles, stem)
	}
	if len(needles) == 0 {
		return nil, nil
	}

	frames := f.collectAdjacentFrames(filePath, needles[0])
	// Re-score each frame by how many of the file's symbols it
	// actually mentions, so tests covering several symbols from the
	// file rank above single-mention hits.
	for i := range frames {
		count := 0
		for _, n := range needles {
			if strings.Contains(frames[i].Body, n) {
				count++
			}
		}
		frames[i].SubjectHit = count
	}
	hits := rankAndSlice(frames, needles[0], filePath, limit)
	return hits, nil
}

// collectAdjacentFrames reads every adjacent test file candidate
// for a source path and returns the contained test functions. Files
// that aren't readable (don't exist, binary, traversal rejected)
// are silently skipped — the adjacent heuristic is best-effort.
func (f *TestFinder) collectAdjacentFrames(sourcePath, subject string) []testFrame {
	out := []testFrame{}
	for _, cand := range adjacentTestCandidates(sourcePath) {
		if cand == sourcePath { // defensive
			continue
		}
		if f.o.files == nil {
			break
		}
		body, err := f.o.files.ReadRepoFile(f.repoID, cand)
		if err != nil {
			continue
		}
		frames := findTestFunctions(cand, body)
		for i := range frames {
			frames[i].Adjacent = true
			frames[i].SubjectHit = strings.Count(frames[i].Body, subject)
		}
		out = append(out, frames...)
	}
	return out
}

// augmentFromSearch uses the hybrid searcher (when available) to
// find test files that mention the subject in a different
// package/dir than the adjacent heuristic covers. The searcher's
// kind filter doesn't distinguish test files from source files, so
// we post-filter by path pattern.
func (f *TestFinder) augmentFromSearch(ctx context.Context, subject string, existing []testFrame) []testFrame {
	if f.o.searcher == nil || subject == "" {
		return existing
	}
	query := "test " + subject
	hits, err := f.o.searcher.SearchForQA(ctx, f.repoID, query, 20)
	if err != nil {
		return existing
	}
	existingPaths := map[string]struct{}{}
	for _, fr := range existing {
		existingPaths[fr.FilePath] = struct{}{}
	}
	for _, h := range hits {
		if h.FilePath == "" || !isTestFile(h.FilePath) {
			continue
		}
		if _, dup := existingPaths[h.FilePath]; dup {
			continue
		}
		body, readErr := f.o.files.ReadRepoFile(f.repoID, h.FilePath)
		if readErr != nil {
			continue
		}
		frames := findTestFunctions(h.FilePath, body)
		for i := range frames {
			frames[i].Adjacent = false
			frames[i].SubjectHit = strings.Count(frames[i].Body, subject)
		}
		existing = append(existing, frames...)
		existingPaths[h.FilePath] = struct{}{}
		if len(existing) > 40 {
			break
		}
	}
	return existing
}

// rankAndSlice orders frames by (a) subject-name co-occurrence,
// (b) adjacency bonus, (c) assertion density, then caps at `limit`.
// Frames that don't mention the subject at all are dropped — they
// don't belong in a subject-scoped find_tests response.
func rankAndSlice(frames []testFrame, subject, subjectFile string, limit int) []TestHit {
	if limit <= 0 {
		limit = 5
	}
	type scored struct {
		frame testFrame
		score float64
	}
	scoredFrames := []scored{}
	for _, fr := range frames {
		if fr.SubjectHit == 0 {
			continue
		}
		sc := float64(fr.SubjectHit) * 2.0
		if fr.Adjacent {
			sc += 3.0
		}
		asserts := extractAssertions(fr.Body)
		sc += float64(len(asserts)) * 0.5
		// Shorter, focused tests rank higher than epic-sized ones.
		bodyLines := fr.EndLine - fr.StartLine + 1
		if bodyLines > 0 && bodyLines < 30 {
			sc += 1.0
		}
		scoredFrames = append(scoredFrames, scored{frame: fr, score: sc})
	}
	sort.SliceStable(scoredFrames, func(i, j int) bool {
		return scoredFrames[i].score > scoredFrames[j].score
	})
	if len(scoredFrames) > limit {
		scoredFrames = scoredFrames[:limit]
	}
	out := make([]TestHit, 0, len(scoredFrames))
	for _, s := range scoredFrames {
		body := s.frame.Body
		if len(body) > 4000 {
			body = body[:4000] + "\n// ... truncated"
		}
		out = append(out, TestHit{
			Handle:          buildTestHandle(s.frame),
			FilePath:        s.frame.FilePath,
			StartLine:       s.frame.StartLine,
			EndLine:         s.frame.EndLine,
			TestName:        s.frame.TestName,
			SubjectName:     subject,
			SubjectFilePath: subjectFile,
			Content:         body,
			Assertions:      extractAssertions(s.frame.Body),
			Score:           s.score,
		})
	}
	return out
}

// buildTestHandle returns the Source-Handle-Contract-compliant
// citation handle for a test hit. Prefixed with "test:" so the
// Monitor UI and the citation resolver can render test refs
// distinctly from file ranges.
func buildTestHandle(fr testFrame) string {
	return "test:" + fr.FilePath + ":" + formatRange(fr.StartLine, fr.EndLine)
}

func formatRange(start, end int) string {
	if end <= 0 {
		return intToString(start)
	}
	return intToString(start) + "-" + intToString(end)
}

func intToString(n int) string {
	// Tiny allocation-free integer formatter; avoids pulling strconv
	// across the call path where this is the only use.
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	buf := [12]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
