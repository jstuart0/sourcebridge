// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package indexer

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	sitter "github.com/smacker/go-tree-sitter"
)

// Parser extracts symbols from source code using tree-sitter.
type Parser struct{}

// NewParser creates a new Parser.
func NewParser() *Parser {
	return &Parser{}
}

// ParseFile parses a source file and extracts symbols and imports.
func (p *Parser) ParseFile(ctx context.Context, filePath, language string, content []byte) (*FileResult, error) {
	langConfig := GetLanguageConfig(language)
	if langConfig == nil {
		return &FileResult{
			Path:     filePath,
			Language: language,
			Errors:   []string{fmt.Sprintf("unsupported language: %s", language)},
		}, nil
	}

	parser := sitter.NewParser()
	parser.SetLanguage(langConfig.Language)

	tree, err := parser.ParseCtx(ctx, nil, content)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", filePath, err)
	}
	defer tree.Close()

	root := tree.RootNode()
	lineCount := int(root.EndPoint().Row) + 1

	result := &FileResult{
		Path:      filePath,
		Language:  language,
		LineCount: lineCount,
	}

	// Extract functions
	funcs, err := p.extractSymbols(langConfig.Language, content, langConfig.FunctionQuery, filePath, language, SymbolFunction)
	if err == nil {
		result.Symbols = append(result.Symbols, funcs...)
	}

	// Extract classes/structs
	classes, err := p.extractSymbols(langConfig.Language, content, langConfig.ClassQuery, filePath, language, SymbolClass)
	if err == nil {
		result.Symbols = append(result.Symbols, classes...)
	}

	// Extract methods
	methods, err := p.extractSymbols(langConfig.Language, content, langConfig.MethodQuery, filePath, language, SymbolMethod)
	if err == nil {
		result.Symbols = append(result.Symbols, methods...)
	}

	// Extract imports
	imports, err := p.extractImports(langConfig.Language, content, langConfig.ImportQuery, filePath)
	if err == nil {
		result.Imports = imports
	}

	// Extract call sites
	if langConfig.CallQuery != "" {
		calls, err := p.extractCalls(langConfig.Language, content, langConfig.CallQuery, filePath, result.Symbols)
		if err == nil {
			result.Calls = calls
		}
	}

	// Extract doc comments
	if langConfig.DocCommentQuery != "" {
		p.extractDocComments(langConfig.Language, content, langConfig.DocCommentQuery, language, result)
	}

	// Mark test symbols
	p.markTestSymbols(result, langConfig)

	return result, nil
}

func (p *Parser) extractSymbols(lang *sitter.Language, content []byte, queryStr, filePath, language string, kind SymbolKind) ([]Symbol, error) {
	if queryStr == "" {
		return nil, nil
	}

	query, err := sitter.NewQuery([]byte(queryStr), lang)
	if err != nil {
		return nil, fmt.Errorf("compiling query for %s %s: %w", language, kind, err)
	}
	defer query.Close()

	parser := sitter.NewParser()
	parser.SetLanguage(lang)
	tree, err := parser.ParseCtx(context.Background(), nil, content)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	cursor := sitter.NewQueryCursor()
	cursor.Exec(query, tree.RootNode())

	var symbols []Symbol
	seen := make(map[string]bool) // Avoid duplicate symbols from overlapping captures

	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}

		var name string
		var node *sitter.Node
		for _, capture := range match.Captures {
			captureName := query.CaptureNameForId(capture.Index)
			if captureName == "name" {
				name = capture.Node.Content(content)
			}
			// Use the broadest capture (func, class, method, struct, etc.) for position
			if captureName == "func" || captureName == "class" || captureName == "method" ||
				captureName == "struct" || captureName == "enum" || captureName == "trait" {
				node = capture.Node
			}
		}

		if name == "" || node == nil {
			continue
		}

		key := fmt.Sprintf("%s:%d:%s", filePath, node.StartPoint().Row, name)
		if seen[key] {
			continue
		}
		seen[key] = true

		// Determine actual kind for multi-pattern queries
		actualKind := kind
		if kind == SymbolClass && node.Type() == "struct_item" {
			actualKind = SymbolStruct
		} else if kind == SymbolClass && node.Type() == "enum_item" {
			actualKind = SymbolEnum
		} else if kind == SymbolClass && strings.Contains(node.Type(), "struct") {
			actualKind = SymbolStruct
		}

		// Build signature from first line
		startLine := int(node.StartPoint().Row)
		endLine := int(node.EndPoint().Row)
		sig := extractSignature(content, startLine)

		symbols = append(symbols, Symbol{
			ID:            uuid.New().String(),
			Name:          name,
			QualifiedName: fmt.Sprintf("%s:%s", filePath, name),
			Kind:          actualKind,
			Language:      language,
			FilePath:      filePath,
			StartLine:     startLine + 1, // 1-indexed
			EndLine:       endLine + 1,
			StartCol:      int(node.StartPoint().Column),
			EndCol:        int(node.EndPoint().Column),
			Signature:     sig,
		})
	}

	return symbols, nil
}

func (p *Parser) extractImports(lang *sitter.Language, content []byte, queryStr, filePath string) ([]Import, error) {
	if queryStr == "" {
		return nil, nil
	}

	query, err := sitter.NewQuery([]byte(queryStr), lang)
	if err != nil {
		return nil, fmt.Errorf("compiling import query: %w", err)
	}
	defer query.Close()

	parser := sitter.NewParser()
	parser.SetLanguage(lang)
	tree, err := parser.ParseCtx(context.Background(), nil, content)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	cursor := sitter.NewQueryCursor()
	cursor.Exec(query, tree.RootNode())

	var imports []Import
	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}

		for _, capture := range match.Captures {
			captureName := query.CaptureNameForId(capture.Index)
			if captureName == "path" {
				path := capture.Node.Content(content)
				path = strings.Trim(path, `"'`)
				imports = append(imports, Import{
					Path:     path,
					FilePath: filePath,
					Line:     int(capture.Node.StartPoint().Row) + 1,
				})
			}
		}
	}

	return imports, nil
}

// extractCalls finds function call expressions and maps them to the enclosing symbol.
func (p *Parser) extractCalls(lang *sitter.Language, content []byte, queryStr, filePath string, symbols []Symbol) ([]CallSite, error) {
	query, err := sitter.NewQuery([]byte(queryStr), lang)
	if err != nil {
		return nil, fmt.Errorf("compiling call query: %w", err)
	}
	defer query.Close()

	parser := sitter.NewParser()
	parser.SetLanguage(lang)
	tree, err := parser.ParseCtx(context.Background(), nil, content)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	cursor := sitter.NewQueryCursor()
	cursor.Exec(query, tree.RootNode())

	var calls []CallSite
	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}

		var calleeName string
		var callLine int
		for _, capture := range match.Captures {
			captureName := query.CaptureNameForId(capture.Index)
			if captureName == "callee" {
				calleeName = capture.Node.Content(content)
			}
			if captureName == "call" {
				callLine = int(capture.Node.StartPoint().Row) + 1
			}
		}

		if calleeName == "" {
			continue
		}

		// Find the enclosing symbol for this call site
		callerID := findEnclosingSymbol(symbols, callLine)
		if callerID == "" {
			continue // call is at top-level, outside any function
		}

		calls = append(calls, CallSite{
			CallerID:   callerID,
			CalleeName: calleeName,
			FilePath:   filePath,
			Line:       callLine,
		})
	}

	return calls, nil
}

// findEnclosingSymbol returns the ID of the symbol that contains the given line.
func findEnclosingSymbol(symbols []Symbol, line int) string {
	var best *Symbol
	for i := range symbols {
		s := &symbols[i]
		if s.Kind == SymbolClass || s.Kind == SymbolStruct || s.Kind == SymbolEnum || s.Kind == SymbolTrait {
			continue // only match function/method-like symbols as callers
		}
		if line >= s.StartLine && line <= s.EndLine {
			if best == nil || (s.EndLine-s.StartLine) < (best.EndLine-best.StartLine) {
				best = s // prefer the tightest enclosing scope
			}
		}
	}
	if best != nil {
		return best.ID
	}
	return ""
}

// extractDocComments finds doc comments and attaches them to the nearest following symbol.
func (p *Parser) extractDocComments(lang *sitter.Language, content []byte, queryStr, language string, result *FileResult) {
	query, err := sitter.NewQuery([]byte(queryStr), lang)
	if err != nil {
		return
	}
	defer query.Close()

	parser := sitter.NewParser()
	parser.SetLanguage(lang)
	tree, err := parser.ParseCtx(context.Background(), nil, content)
	if err != nil {
		return
	}
	defer tree.Close()

	cursor := sitter.NewQueryCursor()
	cursor.Exec(query, tree.RootNode())

	// Collect all comments with their end lines
	type commentInfo struct {
		text    string
		endLine int
	}
	var comments []commentInfo

	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}

		for _, capture := range match.Captures {
			text := strings.TrimSpace(capture.Node.Content(content))
			endLine := int(capture.Node.EndPoint().Row) + 1
			comments = append(comments, commentInfo{text: text, endLine: endLine})
		}
	}

	if len(comments) == 0 {
		return
	}

	// For Python docstrings: they appear INSIDE the function body (line after def).
	// For Go/TS/JS/Rust: comments appear BEFORE the symbol declaration.
	isPython := language == "python"

	for i := range result.Symbols {
		sym := &result.Symbols[i]
		if sym.DocComment != "" {
			continue // already populated
		}

		if isPython {
			// Python: docstring is the first expression_statement(string) inside the function body
			// It will be on the line right after the function def (sym.StartLine + 1 or + 2)
			for _, c := range comments {
				// Docstring endLine should be within the symbol body
				if c.endLine > sym.StartLine && c.endLine <= sym.EndLine {
					sym.DocComment = cleanDocComment(c.text, language)
					break
				}
			}
		} else {
			// Other languages: find the comment block that ends just before the symbol starts
			var bestComment string
			for _, c := range comments {
				// Comment must end on the line immediately before the symbol (or same line for single-line)
				if c.endLine == sym.StartLine-1 || c.endLine == sym.StartLine {
					bestComment = c.text
				}
			}
			if bestComment != "" {
				sym.DocComment = cleanDocComment(bestComment, language)
			}
		}
	}
}

// cleanDocComment strips comment markers from a doc comment.
func cleanDocComment(raw, language string) string {
	lines := strings.Split(raw, "\n")
	var cleaned []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "///"):
			line = strings.TrimPrefix(line, "///")
		case strings.HasPrefix(line, "//!"):
			line = strings.TrimPrefix(line, "//!")
		case strings.HasPrefix(line, "//"):
			line = strings.TrimPrefix(line, "//")
		case strings.HasPrefix(line, "/**"):
			line = strings.TrimPrefix(line, "/**")
		case strings.HasPrefix(line, "*/"):
			continue
		case strings.HasPrefix(line, "*"):
			line = strings.TrimPrefix(line, "*")
		case strings.HasPrefix(line, `"""`):
			line = strings.Trim(line, `"`)
		case strings.HasPrefix(line, "'''"):
			line = strings.Trim(line, "'")
		}
		line = strings.TrimSpace(line)
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}
	return strings.Join(cleaned, " ")
}

func (p *Parser) markTestSymbols(result *FileResult, config *LanguageConfig) {
	isTestFile := false
	for _, pattern := range config.TestFilePatterns {
		if strings.Contains(result.Path, pattern) {
			isTestFile = true
			break
		}
	}

	if !isTestFile && config.TestFuncPattern == "" {
		return
	}

	var testRe *regexp.Regexp
	if config.TestFuncPattern != "" {
		testRe = regexp.MustCompile(config.TestFuncPattern)
	}

	for i := range result.Symbols {
		if isTestFile {
			result.Symbols[i].IsTest = true
		}
		if testRe != nil && testRe.MatchString(result.Symbols[i].Name) {
			result.Symbols[i].IsTest = true
			result.Symbols[i].Kind = SymbolTest
		}
	}
}

func extractSignature(content []byte, startLine int) string {
	lines := strings.Split(string(content), "\n")
	if startLine >= len(lines) {
		return ""
	}
	sig := strings.TrimSpace(lines[startLine])
	// Truncate long signatures
	if len(sig) > 200 {
		sig = sig[:200] + "..."
	}
	return sig
}
