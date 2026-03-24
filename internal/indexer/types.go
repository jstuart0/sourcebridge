// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package indexer

// SymbolKind represents the type of code symbol.
type SymbolKind string

const (
	SymbolFunction  SymbolKind = "function"
	SymbolMethod    SymbolKind = "method"
	SymbolClass     SymbolKind = "class"
	SymbolStruct    SymbolKind = "struct"
	SymbolInterface SymbolKind = "interface"
	SymbolEnum      SymbolKind = "enum"
	SymbolTrait     SymbolKind = "trait"
	SymbolModule    SymbolKind = "module"
	SymbolTest      SymbolKind = "test"
)

// Symbol represents a code symbol extracted by the parser.
type Symbol struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	QualifiedName string     `json:"qualified_name"`
	Kind          SymbolKind `json:"kind"`
	Language      string     `json:"language"`
	FilePath      string     `json:"file_path"`
	StartLine     int        `json:"start_line"`
	EndLine       int        `json:"end_line"`
	StartCol      int        `json:"start_col"`
	EndCol        int        `json:"end_col"`
	Signature     string     `json:"signature,omitempty"`
	DocComment    string     `json:"doc_comment,omitempty"`
	Receiver      string     `json:"receiver,omitempty"` // For methods
	IsTest        bool       `json:"is_test,omitempty"`
}

// Import represents an import statement.
type Import struct {
	Path     string `json:"path"`
	FilePath string `json:"file_path"`
	Line     int    `json:"line"`
}

// Relation represents a relationship between symbols.
type Relation struct {
	SourceID string       `json:"source_id"`
	TargetID string       `json:"target_id"`
	Type     RelationType `json:"type"`
}

// RelationType represents the type of relationship.
type RelationType string

const (
	RelationCalls      RelationType = "calls"
	RelationImports    RelationType = "imports"
	RelationContains   RelationType = "contains"
	RelationExtends    RelationType = "extends"
	RelationImplements RelationType = "implements"
	RelationTests      RelationType = "tests"
	RelationPartOf     RelationType = "part_of"
)

// CallSite represents a function call within a symbol's scope.
type CallSite struct {
	CallerID   string `json:"caller_id"`   // ID of the enclosing symbol
	CalleeName string `json:"callee_name"` // Name of the called function/method
	FilePath   string `json:"file_path"`
	Line       int    `json:"line"`
}

// FileResult contains the parsing result for a single file.
type FileResult struct {
	Path        string     `json:"path"`
	Language    string     `json:"language"`
	LineCount   int        `json:"line_count"`
	ContentHash string     `json:"content_hash,omitempty"`
	Symbols     []Symbol   `json:"symbols"`
	Imports     []Import   `json:"imports"`
	Calls       []CallSite `json:"calls,omitempty"`
	Errors      []string   `json:"errors,omitempty"`
	AIScore     float64    `json:"ai_score"`              // 0.0-1.0 AI-generated confidence
	AISignals   []string   `json:"ai_signals,omitempty"`  // which heuristics fired
}

// IndexResult contains the full indexing result for a repository.
type IndexResult struct {
	RepoName       string       `json:"repo_name"`
	RepoPath       string       `json:"repo_path"`
	Files          []FileResult `json:"files"`
	TotalFiles     int          `json:"total_files"`
	TotalSymbols   int          `json:"total_symbols"`
	TotalRelations int          `json:"total_relations"`
	Relations      []Relation   `json:"relations,omitempty"`
	Modules        []Module     `json:"modules"`
	Errors         []string     `json:"errors,omitempty"`
}

// Module represents a code module derived from directory structure.
type Module struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	FileCount int   `json:"file_count"`
}

// ProgressEvent is emitted during indexing for real-time updates.
type ProgressEvent struct {
	RepoID      string  `json:"repo_id"`
	Phase       string  `json:"phase"` // "scanning", "parsing", "storing", "complete"
	Current     int     `json:"current"`
	Total       int     `json:"total"`
	File        string  `json:"file,omitempty"`
	Description string  `json:"description,omitempty"`
	Progress    float64 `json:"progress"` // 0.0 to 1.0
}
