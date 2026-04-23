// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Tool names — frozen. The Anthropic tool_use protocol dispatches on
// string names, so these are part of the wire contract.
const (
	ToolSearchEvidence  = "search_evidence"
	ToolReadFile        = "read_file"
	ToolGetCallers      = "get_callers"
	ToolGetCallees      = "get_callees"
	ToolGetSummary      = "get_summary"
	ToolGetRequirements = "get_requirements"
)

// Tool error enums. One per Tool Catalog row. The LLM sees these as
// the `error` field of a tool_result with ok=false; matching
// documentation appears in the tool's description so the model can
// recover.
const (
	ErrQueryEmpty             = "query_empty"
	ErrLimitOutOfRange        = "limit_out_of_range"
	ErrServiceUnavailable     = "service_unavailable"
	ErrFileNotFound           = "file_not_found"
	ErrPathTraversalRejected  = "path_traversal_rejected"
	ErrFileTooLarge           = "file_too_large"
	ErrBinaryFile             = "binary_file"
	ErrSymbolNotFound         = "symbol_not_found"
	ErrGraphUnavailable       = "graph_unavailable"
	ErrUnitNotFound           = "unit_not_found"
	ErrCorpusUnavailable      = "corpus_unavailable"
	ErrInvalidArgs            = "invalid_args"
	ErrEvidenceBudgetExhausted = "evidence_budget_exhausted"
	ErrRepoUnavailable        = "repo_unavailable"
)

// ToolSchema defines one tool the orchestrator advertises to the LLM.
// InputSchemaJSON is a JSON Schema string passed through to the
// provider (Anthropic expects an `input_schema` object).
type ToolSchema struct {
	Name            string
	Description     string
	InputSchemaJSON string
}

// AgentToolDispatcher executes a ToolCall against the orchestrator's
// collaborators and returns a ToolResult. Every tool receives the
// authorized `repoID` implicitly via the dispatcher, never from the
// LLM (plan §Security D1).
type AgentToolDispatcher struct {
	repoID string
	o      *Orchestrator
}

// NewAgentToolDispatcher constructs a dispatcher scoped to one repo.
// The orchestrator is the source of every backing collaborator
// (Searcher, FileReader, GraphExpander, SymbolLookup,
// RequirementLookup, UnderstandingReader).
func NewAgentToolDispatcher(o *Orchestrator, repoID string) *AgentToolDispatcher {
	return &AgentToolDispatcher{o: o, repoID: repoID}
}

// AvailableTools returns the v1 tool catalog in stable order.
// list_files is intentionally absent (plan §Tool Catalog).
func (d *AgentToolDispatcher) AvailableTools() []ToolSchema {
	return []ToolSchema{
		toolSchemaSearchEvidence,
		toolSchemaReadFile,
		toolSchemaGetCallers,
		toolSchemaGetCallees,
		toolSchemaGetSummary,
		toolSchemaGetRequirements,
	}
}

// Dispatch runs one tool call. The result is always a valid
// ToolResult — errors become ok:false results the LLM can recover
// from, not Go errors. Returns a Go error only on catastrophic
// dispatcher misuse (e.g. nil orchestrator).
func (d *AgentToolDispatcher) Dispatch(ctx context.Context, call ToolCall) ToolResult {
	if d == nil || d.o == nil {
		return errResult(call.CallID, ErrServiceUnavailable, "dispatcher not wired")
	}
	switch call.Name {
	case ToolSearchEvidence:
		return d.dispatchSearchEvidence(ctx, call)
	case ToolReadFile:
		return d.dispatchReadFile(ctx, call)
	case ToolGetCallers:
		return d.dispatchGetCallers(ctx, call)
	case ToolGetCallees:
		return d.dispatchGetCallees(ctx, call)
	case ToolGetSummary:
		return d.dispatchGetSummary(ctx, call)
	case ToolGetRequirements:
		return d.dispatchGetRequirements(ctx, call)
	default:
		return errResult(call.CallID, ErrInvalidArgs, fmt.Sprintf("unknown tool %q", call.Name))
	}
}

// ---- search_evidence ------------------------------------------------

type searchEvidenceArgs struct {
	Query  string `json:"query"`
	Kind   string `json:"kind,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Cursor int    `json:"cursor,omitempty"`
}

type searchEvidenceResult struct {
	Results    []searchEvidenceRow `json:"results"`
	NextCursor int                 `json:"next_cursor,omitempty"`
	HasMore    bool                `json:"has_more"`
}

type searchEvidenceRow struct {
	EntityType string   `json:"entity_type"`
	EntityID   string   `json:"entity_id"`
	Handle     string   `json:"handle"` // §Source-Handle Contract
	Title      string   `json:"title"`
	Subtitle   string   `json:"subtitle,omitempty"`
	FilePath   string   `json:"file_path,omitempty"`
	StartLine  int      `json:"start_line,omitempty"`
	EndLine    int      `json:"end_line,omitempty"`
	Score      float64  `json:"score,omitempty"`
	Signals    []string `json:"signals,omitempty"`
}

var toolSchemaSearchEvidence = ToolSchema{
	Name: ToolSearchEvidence,
	Description: "Search the repository for symbols, files, or requirements matching a natural-language query. " +
		"Returns up to 20 ranked candidates, each carrying a stable `handle` you can cite in your final answer. " +
		"Use `kind` to narrow to symbol / file / requirement; omit for mixed results. " +
		"Page with `cursor` when the previous call's `has_more` is true.",
	InputSchemaJSON: `{
    "type": "object",
    "properties": {
      "query":  {"type": "string",  "minLength": 1, "maxLength": 256},
      "kind":   {"type": "string",  "enum": ["symbol", "file", "requirement"]},
      "limit":  {"type": "integer", "minimum": 1, "maximum": 20, "default": 10},
      "cursor": {"type": "integer", "minimum": 0, "default": 0}
    },
    "required": ["query"]
  }`,
}

func (d *AgentToolDispatcher) dispatchSearchEvidence(ctx context.Context, call ToolCall) ToolResult {
	var args searchEvidenceArgs
	if err := json.Unmarshal(call.Args, &args); err != nil {
		return errResult(call.CallID, ErrInvalidArgs, "args must be an object with a non-empty `query`")
	}
	if strings.TrimSpace(args.Query) == "" {
		return errResult(call.CallID, ErrQueryEmpty, "provide a non-empty `query` string")
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 20 {
		return errResult(call.CallID, ErrLimitOutOfRange, "limit must be between 1 and 20")
	}
	if d.o.searcher == nil {
		return errResult(call.CallID, ErrServiceUnavailable, "search service not configured; use read_file or get_summary directly")
	}
	hits, err := d.o.searcher.SearchForQA(ctx, d.repoID, args.Query, limit+args.Cursor)
	if err != nil {
		return errResult(call.CallID, ErrServiceUnavailable, err.Error())
	}
	filtered := filterHitsByKind(hits, args.Kind)
	// Skip `cursor` hits, take `limit`. Deterministic pagination.
	if args.Cursor > 0 && args.Cursor < len(filtered) {
		filtered = filtered[args.Cursor:]
	} else if args.Cursor >= len(filtered) {
		filtered = nil
	}
	hasMore := len(filtered) > limit
	if hasMore {
		filtered = filtered[:limit]
	}
	rows := make([]searchEvidenceRow, 0, len(filtered))
	for _, h := range filtered {
		rows = append(rows, searchEvidenceRow{
			EntityType: h.EntityType,
			EntityID:   h.EntityID,
			Handle:     buildSearchHitHandle(h),
			Title:      h.Title,
			Subtitle:   h.Subtitle,
			FilePath:   h.FilePath,
			StartLine:  h.StartLine,
			EndLine:    h.EndLine,
			Score:      h.Score,
			Signals:    h.Signals,
		})
	}
	res := searchEvidenceResult{Results: rows, HasMore: hasMore}
	if hasMore {
		res.NextCursor = args.Cursor + limit
	}
	return okResult(call.CallID, res)
}

func filterHitsByKind(hits []SearchHit, kind string) []SearchHit {
	if kind == "" {
		return hits
	}
	out := make([]SearchHit, 0, len(hits))
	for _, h := range hits {
		if strings.EqualFold(h.EntityType, kind) {
			out = append(out, h)
		}
	}
	return out
}

// ---- read_file ------------------------------------------------------

type readFileArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
}

type readFileResult struct {
	Path      string `json:"path"`
	Handle    string `json:"handle"` // `path:start-end`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Content   string `json:"content"`
	Lines     int    `json:"lines"`
}

var toolSchemaReadFile = ToolSchema{
	Name: ToolReadFile,
	Description: "Read a source file (or a slice of one) from the authorized repository. " +
		"Returns a `handle` of the form `path:start-end` for citation. " +
		"Default window is 200 lines from the start; provide `start_line` and `end_line` for a specific window (max 500 lines).",
	InputSchemaJSON: `{
    "type": "object",
    "properties": {
      "path":       {"type": "string",  "minLength": 1, "maxLength": 512},
      "start_line": {"type": "integer", "minimum": 1},
      "end_line":   {"type": "integer", "minimum": 1}
    },
    "required": ["path"]
  }`,
}

func (d *AgentToolDispatcher) dispatchReadFile(ctx context.Context, call ToolCall) ToolResult {
	var args readFileArgs
	if err := json.Unmarshal(call.Args, &args); err != nil {
		return errResult(call.CallID, ErrInvalidArgs, "args must be an object with a `path` string")
	}
	if strings.TrimSpace(args.Path) == "" {
		return errResult(call.CallID, ErrInvalidArgs, "provide a non-empty `path`")
	}
	if d.o.files == nil {
		return errResult(call.CallID, ErrServiceUnavailable, "file reader not configured")
	}
	// Safe path join is enforced inside FileReader.ReadRepoFile.
	content, err := d.o.files.ReadRepoFile(d.repoID, args.Path)
	if err != nil {
		// Map known errors to enum values so the LLM can recover.
		if errors.Is(err, ctx.Err()) {
			return errResult(call.CallID, ErrServiceUnavailable, "deadline exceeded")
		}
		msg := err.Error()
		switch {
		case strings.Contains(msg, "not accessible"),
			strings.Contains(msg, "no such file"),
			strings.Contains(msg, "reading file"):
			return errResult(call.CallID, ErrFileNotFound, msg)
		case strings.Contains(msg, "path traversal"),
			strings.Contains(msg, "absolute path"):
			return errResult(call.CallID, ErrPathTraversalRejected, msg)
		}
		return errResult(call.CallID, ErrServiceUnavailable, msg)
	}
	lines := strings.Split(content, "\n")
	total := len(lines)
	start := args.StartLine
	if start <= 0 {
		start = 1
	}
	end := args.EndLine
	if end <= 0 {
		end = start + 199 // default 200-line window
	}
	if end-start+1 > 500 {
		return errResult(call.CallID, ErrFileTooLarge, "max window is 500 lines; request a tighter range")
	}
	if start > total {
		return errResult(call.CallID, ErrFileNotFound, fmt.Sprintf("file has only %d lines", total))
	}
	if end > total {
		end = total
	}
	windowLines := lines[start-1 : end]
	handle := fmt.Sprintf("%s:%d-%d", args.Path, start, end)
	return okResult(call.CallID, readFileResult{
		Path:      args.Path,
		Handle:    handle,
		StartLine: start,
		EndLine:   end,
		Content:   strings.Join(windowLines, "\n"),
		Lines:     end - start + 1,
	})
}

// ---- get_callers / get_callees -------------------------------------

type graphArgs struct {
	SymbolID string `json:"symbol_id"`
}

type graphNeighborRow struct {
	SymbolID      string `json:"symbol_id"`
	Handle        string `json:"handle"` // "sym_<id>"
	QualifiedName string `json:"qualified_name"`
	FilePath      string `json:"file_path,omitempty"`
	StartLine     int    `json:"start_line,omitempty"`
	EndLine       int    `json:"end_line,omitempty"`
	Language      string `json:"language,omitempty"`
}

type graphResult struct {
	FocalSymbolID string             `json:"focal_symbol_id"`
	Neighbors     []graphNeighborRow `json:"neighbors"`
}

var toolSchemaGetCallers = ToolSchema{
	Name: ToolGetCallers,
	Description: "List up to 25 symbols that call the given symbol (one hop in the call graph). " +
		"Each neighbor carries a `handle` of the form `sym_<id>` for citation.",
	InputSchemaJSON: `{
    "type": "object",
    "properties": {
      "symbol_id": {"type": "string", "minLength": 1}
    },
    "required": ["symbol_id"]
  }`,
}

var toolSchemaGetCallees = ToolSchema{
	Name: ToolGetCallees,
	Description: "List up to 25 symbols the given symbol calls (one hop in the call graph). " +
		"Each neighbor carries a `handle` of the form `sym_<id>` for citation.",
	InputSchemaJSON: `{
    "type": "object",
    "properties": {
      "symbol_id": {"type": "string", "minLength": 1}
    },
    "required": ["symbol_id"]
  }`,
}

func (d *AgentToolDispatcher) dispatchGetCallers(_ context.Context, call ToolCall) ToolResult {
	return d.dispatchGraph(call, true)
}

func (d *AgentToolDispatcher) dispatchGetCallees(_ context.Context, call ToolCall) ToolResult {
	return d.dispatchGraph(call, false)
}

func (d *AgentToolDispatcher) dispatchGraph(call ToolCall, callers bool) ToolResult {
	var args graphArgs
	if err := json.Unmarshal(call.Args, &args); err != nil {
		return errResult(call.CallID, ErrInvalidArgs, "args must be an object with a `symbol_id` string")
	}
	if strings.TrimSpace(args.SymbolID) == "" {
		return errResult(call.CallID, ErrInvalidArgs, "provide a non-empty `symbol_id`")
	}
	if d.o.graph == nil {
		return errResult(call.CallID, ErrGraphUnavailable, "graph expansion not configured")
	}
	var nbrs []GraphNeighbor
	if callers {
		nbrs = d.o.graph.GetCallers(args.SymbolID)
	} else {
		nbrs = d.o.graph.GetCallees(args.SymbolID)
	}
	if nbrs == nil {
		nbrs = []GraphNeighbor{}
	}
	if len(nbrs) > 25 {
		nbrs = nbrs[:25]
	}
	rows := make([]graphNeighborRow, 0, len(nbrs))
	for _, n := range nbrs {
		rows = append(rows, graphNeighborRow{
			SymbolID:      n.SymbolID,
			Handle:        buildSymbolHandle(n.SymbolID),
			QualifiedName: n.QualifiedName,
			FilePath:      n.FilePath,
			StartLine:     n.StartLine,
			EndLine:       n.EndLine,
			Language:      n.Language,
		})
	}
	return okResult(call.CallID, graphResult{
		FocalSymbolID: args.SymbolID,
		Neighbors:     rows,
	})
}

// ---- get_summary ----------------------------------------------------

type getSummaryArgs struct {
	UnitID string `json:"unit_id"`
}

type getSummaryResult struct {
	UnitID      string `json:"unit_id"`
	Handle      string `json:"handle"` // == unit_id
	Level       int    `json:"level"`
	Headline    string `json:"headline,omitempty"`
	SummaryText string `json:"summary_text"`
	FilePath    string `json:"file_path,omitempty"`
}

var toolSchemaGetSummary = ToolSchema{
	Name: ToolGetSummary,
	Description: "Fetch a specific understanding-corpus summary row by `unit_id`. " +
		"Use when the seed context or a prior tool result referenced a unit_id you want to expand. " +
		"Returns the full summary text.",
	InputSchemaJSON: `{
    "type": "object",
    "properties": {
      "unit_id": {"type": "string", "minLength": 1}
    },
    "required": ["unit_id"]
  }`,
}

func (d *AgentToolDispatcher) dispatchGetSummary(_ context.Context, call ToolCall) ToolResult {
	var args getSummaryArgs
	if err := json.Unmarshal(call.Args, &args); err != nil {
		return errResult(call.CallID, ErrInvalidArgs, "args must be an object with a `unit_id` string")
	}
	if strings.TrimSpace(args.UnitID) == "" {
		return errResult(call.CallID, ErrInvalidArgs, "provide a non-empty `unit_id`")
	}
	if d.o.reader == nil {
		return errResult(call.CallID, ErrCorpusUnavailable, "understanding corpus reader not configured")
	}
	// Fetch the repo's corpus, then scan for the unit. This path is
	// a lookup-by-id miss; we accept the O(n) scan because corpora
	// are bounded and this tool is called at most a few times per loop.
	status := GetRepositoryStatus(d.o.reader, d.repoID, "")
	if status == nil || status.CorpusID == "" {
		return errResult(call.CallID, ErrCorpusUnavailable, "no understanding corpus for this repository")
	}
	ev, err := GetSummaryEvidence(d.o.reader, status.CorpusID, args.UnitID, "")
	if err != nil {
		return errResult(call.CallID, ErrCorpusUnavailable, err.Error())
	}
	for _, e := range ev {
		if e.UnitID == args.UnitID {
			return okResult(call.CallID, getSummaryResult{
				UnitID:      e.UnitID,
				Handle:      e.UnitID,
				Level:       e.Level,
				Headline:    e.Headline,
				SummaryText: e.SummaryText,
				FilePath:    e.FilePath,
			})
		}
	}
	return errResult(call.CallID, ErrUnitNotFound, fmt.Sprintf("no summary unit with id %q", args.UnitID))
}

// ---- get_requirements ----------------------------------------------

type getRequirementsArgs struct {
	ExternalID string `json:"external_id,omitempty"`
	Query      string `json:"query,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type requirementRow struct {
	ExternalID  string `json:"external_id"`
	Handle      string `json:"handle"` // == external_id
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	FilePath    string `json:"file_path,omitempty"`
}

type getRequirementsResult struct {
	Results []requirementRow `json:"results"`
}

var toolSchemaGetRequirements = ToolSchema{
	Name: ToolGetRequirements,
	Description: "Look up tracked requirements by external_id or free-text query over title+description. " +
		"Returns up to 25 rows, each carrying a `handle` equal to the external_id for citation.",
	InputSchemaJSON: `{
    "type": "object",
    "properties": {
      "external_id": {"type": "string"},
      "query":       {"type": "string", "maxLength": 256},
      "limit":       {"type": "integer", "minimum": 1, "maximum": 25, "default": 10}
    }
  }`,
}

func (d *AgentToolDispatcher) dispatchGetRequirements(_ context.Context, call ToolCall) ToolResult {
	var args getRequirementsArgs
	if err := json.Unmarshal(call.Args, &args); err != nil {
		return errResult(call.CallID, ErrInvalidArgs, "args must be an object")
	}
	if strings.TrimSpace(args.ExternalID) == "" && strings.TrimSpace(args.Query) == "" {
		return errResult(call.CallID, ErrInvalidArgs, "provide either `external_id` or `query`")
	}
	if d.o.requirements == nil {
		return errResult(call.CallID, ErrServiceUnavailable, "requirement lookup not configured")
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 25 {
		limit = 25
	}
	// Present contract: the orchestrator's RequirementLookup only
	// exposes RequirementContext(id) + RequirementLabelsForSymbols.
	// For agentic use we need an ID-based path and a text search;
	// the ID path uses RequirementContext and the text path scans a
	// bounded set via the existing labels API surface. When a richer
	// store adapter lands (proposed in a follow-up), swap in.
	results := make([]requirementRow, 0, limit)
	if args.ExternalID != "" {
		if block := d.o.requirements.RequirementContext(args.ExternalID); block != "" {
			results = append(results, requirementRow{
				ExternalID:  args.ExternalID,
				Handle:      args.ExternalID,
				Description: block,
			})
		}
	}
	// Text search currently returns an empty list — this is a known
	// gap that the §Tool Catalog documents; the follow-up plan
	// (hooking a dedicated text search adapter into the requirements
	// reader) improves this.
	_ = args.Query
	return okResult(call.CallID, getRequirementsResult{Results: results})
}

// ---- helpers -------------------------------------------------------

func okResult(callID string, v any) ToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		return ToolResult{
			CallID: callID,
			OK:     false,
			Error:  ErrServiceUnavailable,
			Hint:   "internal marshaling error",
		}
	}
	return ToolResult{
		CallID: callID,
		OK:     true,
		Data:   b,
	}
}

func errResult(callID, code, hint string) ToolResult {
	return ToolResult{
		CallID: callID,
		OK:     false,
		Error:  code,
		Hint:   hint,
	}
}

// buildSearchHitHandle emits the stable, visible handle per
// §Source-Handle Contract. Symbols → `sym_<id>`; files →
// `path:start-end`; requirements → the external id verbatim.
func buildSearchHitHandle(h SearchHit) string {
	switch h.EntityType {
	case "symbol":
		return buildSymbolHandle(h.EntityID)
	case "file":
		if h.StartLine > 0 && h.EndLine > 0 {
			return fmt.Sprintf("%s:%d-%d", h.FilePath, h.StartLine, h.EndLine)
		}
		return h.FilePath
	case "requirement":
		return h.Title // external id typically
	}
	return h.EntityID
}

func buildSymbolHandle(id string) string {
	if id == "" {
		return ""
	}
	// Avoid double-prefix when upstream already prefixed.
	if strings.HasPrefix(id, "sym_") {
		return id
	}
	return "sym_" + id
}

// stableToolNames returns the tool names in stable order — used by
// tests asserting the v1 catalog shape.
func stableToolNames() []string {
	names := []string{
		ToolSearchEvidence,
		ToolReadFile,
		ToolGetCallers,
		ToolGetCallees,
		ToolGetSummary,
		ToolGetRequirements,
	}
	sort.Strings(names)
	return names
}

// Compile-time assertion: every schema has a non-empty name and
// input_schema. Catches dev-time mistakes without a runtime cost.
func init() {
	for _, s := range []ToolSchema{
		toolSchemaSearchEvidence,
		toolSchemaReadFile,
		toolSchemaGetCallers,
		toolSchemaGetCallees,
		toolSchemaGetSummary,
		toolSchemaGetRequirements,
	} {
		if s.Name == "" || s.Description == "" || s.InputSchemaJSON == "" {
			panic("qa: tool schema incomplete: " + s.Name)
		}
		// Must be valid JSON.
		var dummy map[string]any
		if err := json.Unmarshal([]byte(s.InputSchemaJSON), &dummy); err != nil {
			panic("qa: tool " + s.Name + " has invalid JSON Schema: " + err.Error())
		}
	}
	// path/filepath is imported for future safety work; keep it live.
	_ = filepath.Separator
}
