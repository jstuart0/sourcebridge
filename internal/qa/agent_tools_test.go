// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"
	"encoding/json"
	"testing"
)

// fakeSearcherTools lets us exercise the search_evidence tool
// without standing up the full hybrid retrieval service.
type fakeSearcherTools struct {
	hits []SearchHit
	err  error
}

func (f *fakeSearcherTools) SearchForQA(_ context.Context, _ string, _ string, _ int) ([]SearchHit, error) {
	return f.hits, f.err
}

// Minimal orchestrator + dispatcher factory for tests.
func newTestDispatcher(t *testing.T) *AgentToolDispatcher {
	t.Helper()
	o := New(nil, nil, nil, DefaultConfig())
	return NewAgentToolDispatcher(o, "repo-1")
}

// TestAvailableToolsIsSixAndStable confirms we ship the v1 catalog
// exactly. The LLM routes on these names; drift is a wire break.
func TestAvailableToolsIsSixAndStable(t *testing.T) {
	d := newTestDispatcher(t)
	tools := d.AvailableTools()
	if len(tools) != 6 {
		t.Fatalf("expected 6 tools in v1 catalog, got %d", len(tools))
	}
	got := make([]string, len(tools))
	for i, s := range tools {
		got[i] = s.Name
	}
	want := []string{
		ToolSearchEvidence, ToolReadFile, ToolGetCallers,
		ToolGetCallees, ToolGetSummary, ToolGetRequirements,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestEverySchemaIsValidJSON is a compile-time-ish guarantee —
// init() already panics on bad schemas, but confirm the schema
// payloads parse with the same schema validator Anthropic will use.
func TestEverySchemaIsValidJSON(t *testing.T) {
	d := newTestDispatcher(t)
	for _, s := range d.AvailableTools() {
		var schema map[string]any
		if err := json.Unmarshal([]byte(s.InputSchemaJSON), &schema); err != nil {
			t.Errorf("tool %s has invalid schema JSON: %v", s.Name, err)
		}
		if schema["type"] != "object" {
			t.Errorf("tool %s schema is not type=object", s.Name)
		}
	}
}

// TestDispatchUnknownTool returns an ok:false ToolResult, never a
// Go error. The loop needs tool errors to be recoverable signals.
func TestDispatchUnknownTool(t *testing.T) {
	d := newTestDispatcher(t)
	tr := d.Dispatch(context.Background(), ToolCall{
		CallID: "c1",
		Name:   "nonexistent",
		Args:   json.RawMessage(`{}`),
	})
	if tr.OK {
		t.Error("expected ok:false for unknown tool")
	}
	if tr.Error != ErrInvalidArgs {
		t.Errorf("expected ErrInvalidArgs, got %q", tr.Error)
	}
	if tr.CallID != "c1" {
		t.Errorf("CallID not echoed back")
	}
}

// TestSearchEvidenceRejectsEmptyQuery confirms the input guard.
func TestSearchEvidenceRejectsEmptyQuery(t *testing.T) {
	d := newTestDispatcher(t)
	tr := d.Dispatch(context.Background(), ToolCall{
		CallID: "c1",
		Name:   ToolSearchEvidence,
		Args:   json.RawMessage(`{"query": "  "}`),
	})
	if tr.OK {
		t.Error("expected ok:false on empty query")
	}
	if tr.Error != ErrQueryEmpty {
		t.Errorf("expected ErrQueryEmpty, got %q", tr.Error)
	}
}

// TestSearchEvidenceLimitOutOfRange verifies the cap enforcement.
func TestSearchEvidenceLimitOutOfRange(t *testing.T) {
	d := newTestDispatcher(t)
	tr := d.Dispatch(context.Background(), ToolCall{
		CallID: "c1",
		Name:   ToolSearchEvidence,
		Args:   json.RawMessage(`{"query": "hi", "limit": 100}`),
	})
	if tr.OK {
		t.Error("expected ok:false on limit=100")
	}
	if tr.Error != ErrLimitOutOfRange {
		t.Errorf("expected ErrLimitOutOfRange, got %q", tr.Error)
	}
}

// TestSearchEvidenceNoSearcher surfaces the unavailable path.
func TestSearchEvidenceNoSearcher(t *testing.T) {
	d := newTestDispatcher(t)
	tr := d.Dispatch(context.Background(), ToolCall{
		CallID: "c1",
		Name:   ToolSearchEvidence,
		Args:   json.RawMessage(`{"query": "something"}`),
	})
	if tr.OK {
		t.Error("expected ok:false when no searcher wired")
	}
	if tr.Error != ErrServiceUnavailable {
		t.Errorf("expected ErrServiceUnavailable, got %q", tr.Error)
	}
}

// TestSearchEvidenceHappyPathWithHandles confirms that every row
// carries a stable handle the LLM can cite.
func TestSearchEvidenceHappyPathWithHandles(t *testing.T) {
	searcher := &fakeSearcherTools{
		hits: []SearchHit{
			{
				EntityType: "symbol",
				EntityID:   "abc123",
				Title:      "signIn",
				Subtitle:   "services.auth.signIn",
				FilePath:   "src/auth.ts",
				StartLine:  10,
				EndLine:    40,
				Score:      0.92,
				Signals:    []string{"exact", "lexical"},
			},
			{
				EntityType: "file",
				EntityID:   "src/auth.ts",
				Title:      "src/auth.ts",
				FilePath:   "src/auth.ts",
				StartLine:  1,
				EndLine:    80,
			},
		},
	}
	o := New(nil, nil, nil, DefaultConfig()).WithSearcher(searcher)
	d := NewAgentToolDispatcher(o, "repo-1")
	tr := d.Dispatch(context.Background(), ToolCall{
		CallID: "c1",
		Name:   ToolSearchEvidence,
		Args:   json.RawMessage(`{"query": "auth", "limit": 5}`),
	})
	if !tr.OK {
		t.Fatalf("expected ok:true, got error %q", tr.Error)
	}
	var res searchEvidenceResult
	if err := json.Unmarshal(tr.Data, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(res.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(res.Results))
	}
	if res.Results[0].Handle != "sym_abc123" {
		t.Errorf("symbol handle wrong: %q", res.Results[0].Handle)
	}
	if res.Results[1].Handle != "src/auth.ts:1-80" {
		t.Errorf("file handle wrong: %q", res.Results[1].Handle)
	}
}

// TestReadFileNoReaderReturnsUnavailable catches the nil-config path.
func TestReadFileNoReaderReturnsUnavailable(t *testing.T) {
	d := newTestDispatcher(t)
	tr := d.Dispatch(context.Background(), ToolCall{
		CallID: "c1",
		Name:   ToolReadFile,
		Args:   json.RawMessage(`{"path": "foo.go"}`),
	})
	if tr.OK {
		t.Error("expected ok:false")
	}
	if tr.Error != ErrServiceUnavailable {
		t.Errorf("expected ErrServiceUnavailable, got %q", tr.Error)
	}
}

// TestReadFileRejectsEmptyPath confirms input validation.
func TestReadFileRejectsEmptyPath(t *testing.T) {
	d := newTestDispatcher(t)
	tr := d.Dispatch(context.Background(), ToolCall{
		CallID: "c1",
		Name:   ToolReadFile,
		Args:   json.RawMessage(`{"path": ""}`),
	})
	if tr.OK {
		t.Error("expected ok:false")
	}
	if tr.Error != ErrInvalidArgs {
		t.Errorf("expected ErrInvalidArgs, got %q", tr.Error)
	}
}

// fakeFileReaderTool returns a fixed body with line count the test
// can predict.
type fakeFileReaderTool struct{ body string }

func (f *fakeFileReaderTool) ReadRepoFile(_ string, _ string) (string, error) {
	return f.body, nil
}

// TestReadFileHappyPathEmitsHandle validates the citation handle
// format (§Source-Handle Contract).
func TestReadFileHappyPathEmitsHandle(t *testing.T) {
	body := ""
	for i := 1; i <= 100; i++ {
		body += "line\n"
	}
	o := New(nil, nil, nil, DefaultConfig()).WithFileReader(&fakeFileReaderTool{body: body})
	d := NewAgentToolDispatcher(o, "repo-1")
	tr := d.Dispatch(context.Background(), ToolCall{
		CallID: "c1",
		Name:   ToolReadFile,
		Args:   json.RawMessage(`{"path": "a/b.go", "start_line": 10, "end_line": 20}`),
	})
	if !tr.OK {
		t.Fatalf("expected ok, got error %q", tr.Error)
	}
	var res readFileResult
	if err := json.Unmarshal(tr.Data, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Handle != "a/b.go:10-20" {
		t.Errorf("handle wrong: %q", res.Handle)
	}
	if res.Lines != 11 {
		t.Errorf("expected 11 lines, got %d", res.Lines)
	}
}

// TestReadFileTooLargeRejects confirms the 500-line cap.
func TestReadFileTooLargeRejects(t *testing.T) {
	o := New(nil, nil, nil, DefaultConfig()).WithFileReader(&fakeFileReaderTool{body: "x\n"})
	d := NewAgentToolDispatcher(o, "repo-1")
	tr := d.Dispatch(context.Background(), ToolCall{
		CallID: "c1",
		Name:   ToolReadFile,
		Args:   json.RawMessage(`{"path": "foo", "start_line": 1, "end_line": 600}`),
	})
	if tr.OK {
		t.Error("expected ok:false")
	}
	if tr.Error != ErrFileTooLarge {
		t.Errorf("expected ErrFileTooLarge, got %q", tr.Error)
	}
}

// fakeGraphTools for the callers/callees path.
type fakeGraphTools struct {
	callers []GraphNeighbor
	callees []GraphNeighbor
}

func (f *fakeGraphTools) GetCallers(_ string) []GraphNeighbor { return f.callers }
func (f *fakeGraphTools) GetCallees(_ string) []GraphNeighbor { return f.callees }

// TestGetCallersEmitsHandles validates the symbol handle format.
func TestGetCallersEmitsHandles(t *testing.T) {
	o := New(nil, nil, nil, DefaultConfig()).WithGraphExpander(&fakeGraphTools{
		callers: []GraphNeighbor{
			{SymbolID: "x1", QualifiedName: "pkg.A", FilePath: "a.go", StartLine: 1, EndLine: 10},
			{SymbolID: "sym_y2", QualifiedName: "pkg.B", FilePath: "b.go", StartLine: 20, EndLine: 40},
		},
	})
	d := NewAgentToolDispatcher(o, "repo-1")
	tr := d.Dispatch(context.Background(), ToolCall{
		CallID: "c1",
		Name:   ToolGetCallers,
		Args:   json.RawMessage(`{"symbol_id": "focal"}`),
	})
	if !tr.OK {
		t.Fatalf("expected ok, got %q", tr.Error)
	}
	var res graphResult
	if err := json.Unmarshal(tr.Data, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(res.Neighbors) != 2 {
		t.Fatalf("expected 2 neighbors, got %d", len(res.Neighbors))
	}
	// First neighbor has a raw id; dispatcher should prefix it.
	if res.Neighbors[0].Handle != "sym_x1" {
		t.Errorf("expected sym_x1, got %q", res.Neighbors[0].Handle)
	}
	// Second neighbor already carries the prefix; dispatcher must not double.
	if res.Neighbors[1].Handle != "sym_y2" {
		t.Errorf("expected sym_y2 (no double prefix), got %q", res.Neighbors[1].Handle)
	}
}

// TestNoSizeBasedTruncationSentinel guarantees no tool ever returns
// a `truncated: true` sentinel. This is the v5 contract (plan §H9).
func TestNoSizeBasedTruncationSentinel(t *testing.T) {
	// Verify every tool schema description does not promise a
	// truncation field (which would contradict the no-silent-loss
	// rule). This is a light lint — real enforcement is that the
	// code simply does not emit the field.
	d := newTestDispatcher(t)
	for _, s := range d.AvailableTools() {
		if containsCI(s.Description, "truncated") {
			t.Errorf("tool %s description mentions truncated; plan forbids size-based truncation", s.Name)
		}
	}
}

func containsCI(s, sub string) bool {
	// simple case-insensitive substring check without importing strings
	if len(sub) == 0 || len(s) < len(sub) {
		return len(sub) == 0
	}
	lower := func(b byte) byte {
		if b >= 'A' && b <= 'Z' {
			return b + ('a' - 'A')
		}
		return b
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			if lower(s[i+j]) != lower(sub[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
