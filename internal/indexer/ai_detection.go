// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package indexer

import (
	"regexp"
	"strings"
)

// AIDetectionResult holds the confidence score and signals for AI-generated code.
type AIDetectionResult struct {
	Score   float64  `json:"score"`   // 0.0 (human) to 1.0 (AI)
	Signals []string `json:"signals"` // which heuristics fired
}

// DetectAIGenerated runs heuristic checks to estimate if file content was AI-generated.
// Score is the sum of weights for fired signals, clamped to [0, 1].
func DetectAIGenerated(content string, language string, symbols []Symbol) AIDetectionResult {
	result := AIDetectionResult{Signals: []string{}}
	lines := strings.Split(content, "\n")

	if len(lines) < 10 {
		return result
	}

	// 1. AI attribution markers (weight: 0.30)
	if hasAIAttribution(content) {
		result.Score += 0.30
		result.Signals = append(result.Signals, "ai_attribution")
	}

	// 2. Generic comments (weight: 0.15)
	if hasGenericComments(lines, language) {
		result.Score += 0.15
		result.Signals = append(result.Signals, "generic_comments")
	}

	// 3. Boilerplate density (weight: 0.15)
	if hasHighBoilerplateDensity(lines, language) {
		result.Score += 0.15
		result.Signals = append(result.Signals, "boilerplate_density")
	}

	// 4. Uniform style (weight: 0.10)
	if hasUniformStyle(lines) {
		result.Score += 0.10
		result.Signals = append(result.Signals, "uniform_style")
	}

	// 5. Excessive error handling (weight: 0.10)
	if hasExcessiveErrorHandling(lines, language) {
		result.Score += 0.10
		result.Signals = append(result.Signals, "excessive_error_handling")
	}

	// 6. Suspiciously complete docs (weight: 0.10)
	if hasSuspiciouslyCompleteDocs(symbols) {
		result.Score += 0.10
		result.Signals = append(result.Signals, "suspiciously_complete_docs")
	}

	// 7. No TODO/FIXME/HACK/XXX (weight: 0.05)
	if len(lines) > 50 && hasNoTodoComments(content) {
		result.Score += 0.05
		result.Signals = append(result.Signals, "no_todo_or_hack")
	}

	// 8. Template function names (weight: 0.05)
	if hasTemplateFunctionNames(symbols) {
		result.Score += 0.05
		result.Signals = append(result.Signals, "template_function_names")
	}

	if result.Score > 1.0 {
		result.Score = 1.0
	}

	return result
}

var aiAttributionPatterns = regexp.MustCompile(`(?i)(generated\s+by|ai[- ]generated|copilot|chatgpt|claude|github\s+copilot|openai|auto[- ]generated\s+code)`)

func hasAIAttribution(content string) bool {
	return aiAttributionPatterns.MatchString(content)
}

var genericCommentPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)//\s*(loop through|iterate over|return the result|check if|set the|get the|create a new|initialize the|handle the)`),
	regexp.MustCompile(`(?i)#\s*(loop through|iterate over|return the result|check if|set the|get the|create a new|initialize the|handle the)`),
}

func hasGenericComments(lines []string, language string) bool {
	commentCount := 0
	genericCount := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		isComment := strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "*")
		if !isComment {
			continue
		}
		commentCount++
		for _, pat := range genericCommentPatterns {
			if pat.MatchString(line) {
				genericCount++
				break
			}
		}
	}
	if commentCount < 5 {
		return false
	}
	return float64(genericCount)/float64(commentCount) > 0.4
}

var boilerplatePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^\s*(get|set)[A-Z]\w*\s*\(`),
	regexp.MustCompile(`(?i)^\s*this\.\w+\s*=\s*\w+;?\s*$`),
	regexp.MustCompile(`(?i)^\s*self\.\w+\s*=\s*\w+\s*$`),
}

func hasHighBoilerplateDensity(lines []string, language string) bool {
	if len(lines) < 20 {
		return false
	}
	boilerCount := 0
	for _, line := range lines {
		for _, pat := range boilerplatePatterns {
			if pat.MatchString(line) {
				boilerCount++
				break
			}
		}
	}
	return float64(boilerCount)/float64(len(lines)) > 0.15
}

func hasUniformStyle(lines []string) bool {
	if len(lines) < 30 {
		return false
	}
	// Check indentation consistency: count lines by indent level
	indentCounts := make(map[int]int)
	nonEmpty := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		nonEmpty++
		indent := 0
		for _, ch := range line {
			if ch == ' ' {
				indent++
			} else if ch == '\t' {
				indent += 4
			} else {
				break
			}
		}
		indentCounts[indent]++
	}
	if nonEmpty < 20 {
		return false
	}
	// If more than 80% of lines use only 2-3 distinct indent levels, flag it
	maxCount := 0
	for _, c := range indentCounts {
		if c > maxCount {
			maxCount = c
		}
	}
	// Very uniform: top indent level covers > 60% of lines and few distinct levels
	return len(indentCounts) <= 4 && float64(maxCount)/float64(nonEmpty) > 0.5
}

var errorHandlingPatterns = []*regexp.Regexp{
	regexp.MustCompile(`if\s+err\s*!=\s*nil`),
	regexp.MustCompile(`(?i)catch\s*\(`),
	regexp.MustCompile(`(?i)except\s*:`),
	regexp.MustCompile(`(?i)\.catch\(`),
}

func hasExcessiveErrorHandling(lines []string, language string) bool {
	funcCount := 0
	errorCount := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "def ") ||
			strings.HasPrefix(trimmed, "function ") || strings.Contains(trimmed, "=> {") {
			funcCount++
		}
		for _, pat := range errorHandlingPatterns {
			if pat.MatchString(line) {
				errorCount++
				break
			}
		}
	}
	if funcCount < 3 {
		return false
	}
	// If error handling blocks roughly equal function count, every function handles errors
	return float64(errorCount)/float64(funcCount) >= 0.9
}

func hasSuspiciouslyCompleteDocs(symbols []Symbol) bool {
	if len(symbols) < 5 {
		return false
	}
	documented := 0
	for _, sym := range symbols {
		if sym.Kind != SymbolFunction && sym.Kind != SymbolMethod {
			continue
		}
		if strings.TrimSpace(sym.DocComment) != "" {
			documented++
		}
	}
	funcs := 0
	for _, sym := range symbols {
		if sym.Kind == SymbolFunction || sym.Kind == SymbolMethod {
			funcs++
		}
	}
	if funcs < 5 {
		return false
	}
	// 100% doc coverage on 5+ functions is suspicious
	return documented == funcs
}

var todoPattern = regexp.MustCompile(`(?i)(TODO|FIXME|HACK|XXX|WORKAROUND|KLUDGE)`)

func hasNoTodoComments(content string) bool {
	return !todoPattern.MatchString(content)
}

var templateNamePattern = regexp.MustCompile(`^(handle|process|validate|create|update|delete|get|set|fetch|parse|format|render|build|init|load|save|check|convert|transform|generate|compute|calculate|normalize|sanitize)[A-Z]`)

func hasTemplateFunctionNames(symbols []Symbol) bool {
	if len(symbols) < 5 {
		return false
	}
	templateCount := 0
	funcCount := 0
	for _, sym := range symbols {
		if sym.Kind != SymbolFunction && sym.Kind != SymbolMethod {
			continue
		}
		funcCount++
		if templateNamePattern.MatchString(sym.Name) {
			templateCount++
		}
	}
	if funcCount < 5 {
		return false
	}
	return float64(templateCount)/float64(funcCount) > 0.7
}
