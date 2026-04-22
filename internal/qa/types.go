// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package qa implements the server-side deep-QA orchestrator.
// This file defines the request/response contract consumed by the
// GraphQL ask mutation, REST POST /api/v1/ask, and MCP ask_question
// tool. Every caller serializes against the same internal types so
// shape drift is caught at the type layer, not at the transport layer.
package qa

import "time"

// Mode enumerates the orchestration pipeline variant.
type Mode string

const (
	ModeFast    Mode = "fast"
	ModeDeep    Mode = "deep"
	ModeExplain Mode = "explain"
)

// ReferenceKind enumerates the five canonical reference variants.
type ReferenceKind string

const (
	RefKindSymbol               ReferenceKind = "symbol"
	RefKindFileRange            ReferenceKind = "file_range"
	RefKindRequirement          ReferenceKind = "requirement"
	RefKindUnderstandingSection ReferenceKind = "understanding_section"
	RefKindCrossRepoRef         ReferenceKind = "cross_repo_ref"
)

// AskInput is the canonical input shape for every QA invocation.
// Every transport (GraphQL, REST, MCP, CLI) normalizes its input into
// this struct before calling Orchestrator.Ask.
type AskInput struct {
	RepositoryID string `json:"repositoryId"`
	Question     string `json:"question"`
	Mode         Mode   `json:"mode,omitempty"`

	ConversationID string   `json:"conversationId,omitempty"`
	PriorMessages  []string `json:"priorMessages,omitempty"`

	FilePath      string `json:"filePath,omitempty"`
	Code          string `json:"code,omitempty"`
	Language      string `json:"language,omitempty"`
	ArtifactID    string `json:"artifactId,omitempty"`
	SymbolID      string `json:"symbolId,omitempty"`
	RequirementID string `json:"requirementId,omitempty"`

	IncludeDebug bool `json:"includeDebug,omitempty"`
}

// AskResult is the canonical response envelope returned from every
// orchestrator invocation.
type AskResult struct {
	Answer              string         `json:"answer"`
	References          []AskReference `json:"references"`
	RelatedRequirements []string       `json:"relatedRequirements"`
	Diagnostics         AskDiagnostics `json:"diagnostics"`
	Usage               AskUsage       `json:"usage"`
	Debug               *AskDebug      `json:"debug,omitempty"`
}

// AskReference is the tagged-union reference shape. The Kind field
// discriminates the populated payload; transports flatten or preserve
// the structure as their wire shape allows. The orchestrator is the
// authoritative source of these records.
type AskReference struct {
	Kind ReferenceKind `json:"kind"`

	// Symbol variant
	Symbol *SymbolRef `json:"symbol,omitempty"`

	// FileRange variant
	FileRange *FileRangeRef `json:"fileRange,omitempty"`

	// Requirement variant
	Requirement *RequirementRef `json:"requirement,omitempty"`

	// UnderstandingSection variant
	UnderstandingSection *UnderstandingSectionRef `json:"understandingSection,omitempty"`

	// CrossRepoRef variant
	CrossRepo *CrossRepoRef `json:"crossRepo,omitempty"`

	// Title is a human-readable summary populated for every variant.
	// Used when flattening to the legacy DiscussionResult.References []string wire shape.
	Title string `json:"title,omitempty"`
}

// SymbolRef identifies a code symbol (function, type, variable).
type SymbolRef struct {
	SymbolID      string `json:"symbolId"`
	QualifiedName string `json:"qualifiedName"`
	FilePath      string `json:"filePath,omitempty"`
	StartLine     int    `json:"startLine,omitempty"`
	EndLine       int    `json:"endLine,omitempty"`
	Language      string `json:"language,omitempty"`
}

// FileRangeRef identifies a file byte/line range.
type FileRangeRef struct {
	FilePath  string `json:"filePath"`
	StartLine int    `json:"startLine,omitempty"`
	EndLine   int    `json:"endLine,omitempty"`
	Snippet   string `json:"snippet,omitempty"`
}

// RequirementRef identifies an external requirement.
type RequirementRef struct {
	ExternalID string `json:"externalId"`
	Title      string `json:"title,omitempty"`
	FilePath   string `json:"filePath,omitempty"`
}

// UnderstandingSectionRef identifies a section of the knowledge corpus.
// A section with Kind=="action_cta" (convention) flags a build-understanding
// call-to-action rather than an ordinary reference; the orchestrator sets
// this when the readiness check fails. See Phase 3 / Ledger F7.
type UnderstandingSectionRef struct {
	ArtifactID string `json:"artifactId,omitempty"`
	SectionID  string `json:"sectionId,omitempty"`
	Headline   string `json:"headline,omitempty"`
	Kind       string `json:"kind,omitempty"`
	ActionURL  string `json:"actionUrl,omitempty"`
}

// CrossRepoRef points at content in a different repo. Not populated
// today (cross-repo QA is out of scope) but reserved so the wire shape
// does not need to grow later.
type CrossRepoRef struct {
	RepositoryID string `json:"repositoryId"`
	FilePath     string `json:"filePath,omitempty"`
	Note         string `json:"note,omitempty"`
}

// AskDiagnostics carries the twelve canonical diagnostic keys emitted
// by every orchestrator run. Keys parallel what workers/cli_ask.py
// emits today so callers that inspect diagnostics do not break.
type AskDiagnostics struct {
	QuestionType          string                `json:"questionType,omitempty"`
	UnderstandingStage    string                `json:"understandingStage,omitempty"`
	TreeStatus            string                `json:"treeStatus,omitempty"`
	UnderstandingRevision string                `json:"understandingRevision,omitempty"`
	UnderstandingUsed     bool                  `json:"understandingUsed,omitempty"`
	GraphExpansionUsed    bool                  `json:"graphExpansionUsed,omitempty"`
	FilesConsidered       []string              `json:"filesConsidered,omitempty"`
	FilesUsed             []string              `json:"filesUsed,omitempty"`
	FallbackUsed          string                `json:"fallbackUsed,omitempty"`
	ModelUsed             string                `json:"modelUsed,omitempty"`
	StageTimings          map[string]DurationMs `json:"stageTimings,omitempty"`
	Mode                  string                `json:"mode,omitempty"`
}

// DurationMs is a duration expressed as integer milliseconds on the wire.
// Using int64 (not time.Duration) keeps the JSON shape stable across
// languages and transports.
type DurationMs int64

// FromDuration converts a Go duration to milliseconds.
func FromDuration(d time.Duration) DurationMs {
	return DurationMs(d / time.Millisecond)
}

// AskUsage captures token accounting for the synthesis call.
type AskUsage struct {
	Model        string `json:"model,omitempty"`
	InputTokens  int    `json:"inputTokens,omitempty"`
	OutputTokens int    `json:"outputTokens,omitempty"`
}

// AskDebug is returned only when AskInput.IncludeDebug is true. Never
// included by default — it may contain sensitive snippets from
// retrieval, and callers should treat it as opt-in.
type AskDebug struct {
	Prompt          string           `json:"prompt,omitempty"`
	ContextMarkdown string           `json:"contextMarkdown,omitempty"`
	Candidates      []DebugCandidate `json:"candidates,omitempty"`
}

// DebugCandidate is one retrieval candidate surfaced in debug payloads.
type DebugCandidate struct {
	Source string  `json:"source"`
	ID     string  `json:"id,omitempty"`
	Score  float64 `json:"score,omitempty"`
	Reason string  `json:"reason,omitempty"`
}

// FlattenReferencesToStrings produces the legacy
// DiscussionResult.References []string wire shape from structured
// references. This is a one-way lossy mapping used by the GraphQL
// discussCode adapter for backward compatibility. Clients that want
// structured references use the new ask mutation.
func FlattenReferencesToStrings(refs []AskReference) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if r.Title != "" {
			out = append(out, r.Title)
			continue
		}
		switch r.Kind {
		case RefKindSymbol:
			if r.Symbol != nil {
				out = append(out, r.Symbol.QualifiedName)
			}
		case RefKindFileRange:
			if r.FileRange != nil {
				out = append(out, r.FileRange.FilePath)
			}
		case RefKindRequirement:
			if r.Requirement != nil {
				out = append(out, r.Requirement.ExternalID)
			}
		case RefKindUnderstandingSection:
			if r.UnderstandingSection != nil {
				out = append(out, r.UnderstandingSection.Headline)
			}
		case RefKindCrossRepoRef:
			if r.CrossRepo != nil {
				out = append(out, r.CrossRepo.RepositoryID)
			}
		}
	}
	return out
}
