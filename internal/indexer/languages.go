// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package indexer

import (
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/rust"
	tsts "github.com/smacker/go-tree-sitter/typescript/typescript"
)

// LanguageConfig holds tree-sitter configuration for a language.
type LanguageConfig struct {
	Name     string
	Language *sitter.Language
	// Queries for extracting symbols
	FunctionQuery string
	ClassQuery    string
	ImportQuery   string
	MethodQuery   string
	// Queries for call graph and doc comments
	CallQuery       string
	DocCommentQuery string
	// Test file patterns
	TestFilePatterns []string
	TestFuncPattern  string
}

// Registry maps language names to their tree-sitter configurations.
var Registry = map[string]*LanguageConfig{
	"go":         goConfig(),
	"python":     pythonConfig(),
	"typescript": typescriptConfig(),
	"javascript": javascriptConfig(),
	"java":       javaConfig(),
	"rust":       rustConfig(),
}

func goConfig() *LanguageConfig {
	return &LanguageConfig{
		Name:     "go",
		Language: golang.GetLanguage(),
		FunctionQuery: `(function_declaration
			name: (identifier) @name) @func`,
		ClassQuery: `(type_declaration
			(type_spec
				name: (type_identifier) @name
				type: (struct_type))) @struct`,
		ImportQuery: `(import_spec
			path: (interpreted_string_literal) @path)`,
		MethodQuery: `(method_declaration
			receiver: (parameter_list
				(parameter_declaration
					type: [(pointer_type (type_identifier) @receiver) (type_identifier) @receiver]))
			name: (field_identifier) @name) @method`,
		CallQuery: `(call_expression
			function: [
				(identifier) @callee
				(selector_expression field: (field_identifier) @callee)
			]) @call`,
		DocCommentQuery: `(comment) @comment`,
		TestFilePatterns: []string{"_test.go"},
		TestFuncPattern:  "^Test",
	}
}

func pythonConfig() *LanguageConfig {
	return &LanguageConfig{
		Name:     "python",
		Language: python.GetLanguage(),
		FunctionQuery: `(function_definition
			name: (identifier) @name) @func`,
		ClassQuery: `(class_definition
			name: (identifier) @name) @class`,
		ImportQuery: `[
			(import_statement
				name: (dotted_name) @path)
			(import_from_statement
				module_name: (dotted_name) @path)
		]`,
		MethodQuery: `(class_definition
			body: (block
				(function_definition
					name: (identifier) @name) @method))`,
		CallQuery: `(call
			function: [
				(identifier) @callee
				(attribute attribute: (identifier) @callee)
			]) @call`,
		DocCommentQuery: `(expression_statement (string) @docstring)`,
		TestFilePatterns: []string{"test_", "_test.py"},
		TestFuncPattern:  "^test_",
	}
}

func typescriptConfig() *LanguageConfig {
	return &LanguageConfig{
		Name:     "typescript",
		Language: tsts.GetLanguage(),
		FunctionQuery: `[
			(function_declaration
				name: (identifier) @name) @func
			(lexical_declaration
				(variable_declarator
					name: (identifier) @name
					value: (arrow_function))) @func
		]`,
		ClassQuery: `(class_declaration
			name: (type_identifier) @name) @class`,
		ImportQuery: `(import_statement
			source: (string) @path)`,
		MethodQuery: `(class_declaration
			body: (class_body
				(method_definition
					name: (property_identifier) @name) @method))`,
		CallQuery: `(call_expression
			function: [
				(identifier) @callee
				(member_expression property: (property_identifier) @callee)
			]) @call`,
		DocCommentQuery: `(comment) @comment`,
		TestFilePatterns: []string{".test.ts", ".test.tsx", ".spec.ts", ".spec.tsx"},
		TestFuncPattern:  "^(test|it|describe)",
	}
}

func javascriptConfig() *LanguageConfig {
	return &LanguageConfig{
		Name:     "javascript",
		Language: javascript.GetLanguage(),
		FunctionQuery: `[
			(function_declaration
				name: (identifier) @name) @func
			(lexical_declaration
				(variable_declarator
					name: (identifier) @name
					value: (arrow_function))) @func
		]`,
		ClassQuery: `(class_declaration
			name: (identifier) @name) @class`,
		ImportQuery: `(import_statement
			source: (string) @path)`,
		MethodQuery: `(class_declaration
			body: (class_body
				(method_definition
					name: (property_identifier) @name) @method))`,
		CallQuery: `(call_expression
			function: [
				(identifier) @callee
				(member_expression property: (property_identifier) @callee)
			]) @call`,
		DocCommentQuery: `(comment) @comment`,
		TestFilePatterns: []string{".test.js", ".test.jsx", ".spec.js"},
		TestFuncPattern:  "^(test|it|describe)",
	}
}

func javaConfig() *LanguageConfig {
	return &LanguageConfig{
		Name:     "java",
		Language: java.GetLanguage(),
		FunctionQuery: `(method_declaration
			name: (identifier) @name) @func`,
		ClassQuery: `(class_declaration
			name: (identifier) @name) @class`,
		ImportQuery: `(import_declaration
			(scoped_identifier) @path)`,
		MethodQuery: `(class_declaration
			body: (class_body
				(method_declaration
					name: (identifier) @name) @method))`,
		CallQuery: `(method_invocation
			name: (identifier) @callee) @call`,
		DocCommentQuery: `(block_comment) @comment`,
		TestFilePatterns: []string{"Test.java", "Tests.java"},
		TestFuncPattern:  "^test",
	}
}

func rustConfig() *LanguageConfig {
	return &LanguageConfig{
		Name:     "rust",
		Language: rust.GetLanguage(),
		FunctionQuery: `(function_item
			name: (identifier) @name) @func`,
		ClassQuery: `[
			(struct_item
				name: (type_identifier) @name) @struct
			(enum_item
				name: (type_identifier) @name) @enum
			(trait_item
				name: (type_identifier) @name) @trait
		]`,
		ImportQuery: `(use_declaration
			argument: (_) @path)`,
		MethodQuery: `(impl_item
			body: (declaration_list
				(function_item
					name: (identifier) @name) @method))`,
		CallQuery: `(call_expression
			function: [
				(identifier) @callee
				(field_expression field: (field_identifier) @callee)
			]) @call`,
		DocCommentQuery: `(line_comment) @comment`,
		TestFilePatterns: []string{},
		TestFuncPattern:  "^test_",
	}
}

// GetLanguageConfig returns the config for a language name, or nil if unsupported.
func GetLanguageConfig(lang string) *LanguageConfig {
	return Registry[lang]
}

// SupportedLanguages returns the list of supported language names.
func SupportedLanguages() []string {
	langs := make([]string, 0, len(Registry))
	for name := range Registry {
		langs = append(langs, name)
	}
	return langs
}
