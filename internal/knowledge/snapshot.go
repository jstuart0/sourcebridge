// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

// KnowledgeSnapshot is the deterministic intermediate representation of a
// repository's knowledge, assembled from indexed data before any LLM
// generation. It serves as the input contract for all knowledge generators.
type KnowledgeSnapshot struct {
	RepositoryID   string         `json:"repository_id"`
	RepositoryName string         `json:"repository_name"`
	SourceRevision SourceRevision `json:"source_revision"`
	ScopeContext   *ScopeContext  `json:"scope_context,omitempty"`

	// Structural summaries
	Languages   []LanguageSummary `json:"languages"`
	Modules     []ModuleSummary   `json:"modules"`
	FileCount   int               `json:"file_count"`
	SymbolCount int               `json:"symbol_count"`
	TestCount   int               `json:"test_count"`

	// Key symbols grouped by role
	EntryPoints    []SymbolRef `json:"entry_points"`
	PublicAPI      []SymbolRef `json:"public_api"`
	TestSymbols    []SymbolRef `json:"test_symbols"`
	ComplexSymbols []SymbolRef `json:"complex_symbols"`

	// Call graph highlights
	HighFanOutSymbols []SymbolRef `json:"high_fan_out_symbols"`
	HighFanInSymbols  []SymbolRef `json:"high_fan_in_symbols"`

	// Requirements and traceability
	Requirements  []RequirementRef `json:"requirements"`
	Links         []LinkRef        `json:"links"`
	CoverageRatio float64          `json:"coverage_ratio"`

	// Documentation found in repo
	Docs []DocRef `json:"docs"`
}

// ScopeContext captures the focused scope details for scoped artifacts.
type ScopeContext struct {
	ScopeType         string              `json:"scope_type"`
	ScopePath         string              `json:"scope_path"`
	FocusSummary      string              `json:"focus_summary,omitempty"`
	TargetFile        *FileRef            `json:"target_file,omitempty"`
	TargetSymbol      *SymbolRef          `json:"target_symbol,omitempty"`
	TargetRequirement *RequirementContext `json:"target_requirement,omitempty"`
	KeySymbols        []SymbolRef         `json:"key_symbols,omitempty"`
	LinkedFiles       []FileRef           `json:"linked_files,omitempty"`
	SiblingSymbols    []SymbolRef         `json:"sibling_symbols,omitempty"`
	Callers           []SymbolRef         `json:"callers,omitempty"`
	Callees           []SymbolRef         `json:"callees,omitempty"`
}

// RequirementContext is the full requirement context for requirement-scoped artifacts.
// Separate from RequirementRef to include Description without bloating repo-scope snapshots.
type RequirementContext struct {
	ID          string   `json:"id"`
	ExternalID  string   `json:"external_id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Priority    string   `json:"priority,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// LanguageSummary captures per-language file/symbol counts.
type LanguageSummary struct {
	Language    string `json:"language"`
	FileCount   int    `json:"file_count"`
	SymbolCount int    `json:"symbol_count"`
}

// ModuleSummary captures a module/package boundary.
type ModuleSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	FileCount int    `json:"file_count"`
}

// FileRef is a lightweight reference to a file for scoped guidance.
type FileRef struct {
	Path       string `json:"path"`
	Language   string `json:"language,omitempty"`
	LineCount  int    `json:"line_count,omitempty"`
	ModulePath string `json:"module_path,omitempty"`
}

// SymbolRef is a lightweight reference to a symbol for snapshot purposes.
type SymbolRef struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Kind          string `json:"kind"`
	Signature     string `json:"signature,omitempty"`
	FilePath      string `json:"file_path"`
	StartLine     int    `json:"start_line"`
	EndLine       int    `json:"end_line"`
	DocComment    string `json:"doc_comment,omitempty"`
	FanOut        int    `json:"fan_out,omitempty"`
	FanIn         int    `json:"fan_in,omitempty"`
	LineCount     int    `json:"line_count,omitempty"`
}

// RequirementRef is a lightweight reference to a requirement.
type RequirementRef struct {
	ID          string `json:"id"`
	ExternalID  string `json:"external_id"`
	Title       string `json:"title"`
	Priority    string `json:"priority,omitempty"`
	LinkedCount int    `json:"linked_count"`
}

// LinkRef is a lightweight reference to a requirement-code link.
type LinkRef struct {
	RequirementID string  `json:"requirement_id"`
	SymbolID      string  `json:"symbol_id"`
	Confidence    float64 `json:"confidence"`
	Source        string  `json:"source"`
}

// DocRef represents a documentation file found in the repository.
type DocRef struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
}
