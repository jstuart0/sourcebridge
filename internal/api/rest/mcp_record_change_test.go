// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/changewatch"
)

// stubRecordChangeDispatcher is a tiny in-test ChangeEventDispatcher.
// Identical shape to stubChangeDispatcher in connectors_test.go but
// kept local to make this test file self-contained — readers exploring
// the record_change tool don't have to chase another file's helper.
type stubRecordChangeDispatcher struct {
	mu      sync.Mutex
	calls   []*changewatch.ChangeEvent
	outcome changewatch.SubmitOutcome
	err     error
}

func (s *stubRecordChangeDispatcher) Submit(_ context.Context, ev *changewatch.ChangeEvent) (changewatch.SubmitOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := *ev
	clone.Files = append([]changewatch.FileChange(nil), ev.Files...)
	s.calls = append(s.calls, &clone)
	if s.outcome == "" {
		return changewatch.OutcomeIndexing, s.err
	}
	return s.outcome, s.err
}

func (s *stubRecordChangeDispatcher) lastCall() *changewatch.ChangeEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return nil
	}
	return s.calls[len(s.calls)-1]
}

func (s *stubRecordChangeDispatcher) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// invokeRecordChange runs the tool through the standard MCP harness so
// the test exercises the same dispatch path production traffic uses
// (tools/call → handleToolsCallCtx → callRecordChange).
func invokeRecordChange(t *testing.T, h *mcpTestHarness, sess *mcpSession, args map[string]interface{}) (recordChangeResponse, *mcpToolResult) {
	t.Helper()
	resp := h.sendRPC(sess, 100, "tools/call", map[string]interface{}{
		"name":      "record_change",
		"arguments": args,
	})
	if resp.Error != nil {
		t.Fatalf("record_change RPC error: %s", resp.Error.Message)
	}
	b, _ := json.Marshal(resp.Result)
	var tr mcpToolResult
	if err := json.Unmarshal(b, &tr); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if len(tr.Content) == 0 {
		t.Fatalf("tool result has no content")
	}
	if tr.IsError {
		// Caller asserts on tr.Content[0].Text (the human-readable
		// MCP error message). Return the tool result so they can
		// pin error behavior.
		return recordChangeResponse{}, &tr
	}
	var out recordChangeResponse
	if err := json.Unmarshal([]byte(tr.Content[0].Text), &out); err != nil {
		t.Fatalf("decode record_change response: %v (text=%q)", err, tr.Content[0].Text)
	}
	return out, &tr
}

// TestRecordChange_HiddenWhenDispatcherUnwired — when the
// changeDispatcher is nil (the Phase 1.D default before the umbrella
// flag flips), the tool must NOT appear in tools/list. This is the
// adoption-posture contract: agents discovering tools shouldn't see a
// no-op tool that would mislead their planner.
func TestRecordChange_HiddenWhenDispatcherUnwired(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()
	resp := h.sendRPC(sess, 1, "initialize", map[string]interface{}{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "test", "version": "0.0.0"},
	})
	if resp.Error != nil {
		t.Fatalf("initialize: %s", resp.Error.Message)
	}

	listResp := h.sendRPC(sess, 2, "tools/list", nil)
	if listResp.Error != nil {
		t.Fatalf("tools/list: %s", listResp.Error.Message)
	}
	b, _ := json.Marshal(listResp.Result)
	var listed struct {
		Tools []mcpToolDefinition `json:"tools"`
	}
	if err := json.Unmarshal(b, &listed); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "record_change" {
			t.Errorf("tools/list includes record_change but dispatcher is nil — must be hidden")
		}
	}
}

// TestRecordChange_VisibleWhenDispatcherWired — the inverse contract:
// when the dispatcher is present, record_change MUST appear in
// tools/list with the documented description.
func TestRecordChange_VisibleWhenDispatcherWired(t *testing.T) {
	h := newTestHarness(t)
	disp := &stubRecordChangeDispatcher{}
	h.handler.changeDispatcher = disp
	sess := h.createSession()
	if _, err := json.Marshal(sess); err != nil { /* keep linter happy */
	}
	resp := h.sendRPC(sess, 1, "initialize", map[string]interface{}{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "test", "version": "0.0.0"},
	})
	if resp.Error != nil {
		t.Fatalf("initialize: %s", resp.Error.Message)
	}
	listResp := h.sendRPC(sess, 2, "tools/list", nil)
	if listResp.Error != nil {
		t.Fatalf("tools/list: %s", listResp.Error.Message)
	}
	b, _ := json.Marshal(listResp.Result)
	var listed struct {
		Tools []mcpToolDefinition `json:"tools"`
	}
	if err := json.Unmarshal(b, &listed); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	var found *mcpToolDefinition
	for i := range listed.Tools {
		if listed.Tools[i].Name == "record_change" {
			found = &listed.Tools[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("tools/list does NOT include record_change despite dispatcher being wired")
	}
	if !strings.Contains(found.Description, "Optional") {
		t.Errorf("record_change description must lead with Optional. (adoption-posture contract). Got: %q", found.Description)
	}
	if !strings.Contains(found.Description, "never required") {
		t.Errorf("record_change description must say 'never required' (the non-goal). Got: %q", found.Description)
	}
}

// TestRecordChange_HappyPath — well-formed args reach the dispatcher
// with the right ChangeEvent shape: schema version stamped, source
// kind=mcp_record_change, trust=in_process, actor derived from the
// session (not from the tool args), and the response carries
// accepted=true.
func TestRecordChange_HappyPath(t *testing.T) {
	h := newTestHarness(t)
	disp := &stubRecordChangeDispatcher{outcome: changewatch.OutcomeIndexing}
	h.handler.changeDispatcher = disp
	sess := h.createSession()

	out, _ := invokeRecordChange(t, h, sess, map[string]interface{}{
		"repository_id": h.repoID,
		"branch":        "main",
		"intent":        "refactor extract method",
		"files": []map[string]interface{}{
			{"path": "src/main.go", "status": "modified"},
		},
	})

	if !out.Accepted {
		t.Errorf("Accepted=false, want true")
	}
	if out.RoutedTo != "indexing" {
		t.Errorf("RoutedTo=%q, want indexing", out.RoutedTo)
	}
	if out.ChangeID == "" {
		t.Errorf("ChangeID is empty; tool must stamp a connector-side event id")
	}
	if !strings.HasPrefix(out.ChangeID, "rc-") {
		t.Errorf("ChangeID=%q does not have rc- prefix (record_change contract)", out.ChangeID)
	}
	if out.FileCount != 1 {
		t.Errorf("FileCount=%d, want 1", out.FileCount)
	}

	if disp.callCount() != 1 {
		t.Fatalf("dispatcher call count = %d, want 1", disp.callCount())
	}
	got := disp.lastCall()
	if got.Source.Kind != changewatch.SourceKindMCPRecordChange {
		t.Errorf("source.kind=%q, want %q", got.Source.Kind, changewatch.SourceKindMCPRecordChange)
	}
	if got.SchemaVersion != changewatch.ChangeEventSchemaVersion {
		t.Errorf("schema_version=%q, want %q", got.SchemaVersion, changewatch.ChangeEventSchemaVersion)
	}
	if got.Trust.ReceivedVia != "in_process" {
		t.Errorf("trust.received_via=%q, want in_process (NEVER http_ingress for the in-process tool)", got.Trust.ReceivedVia)
	}
	if got.Trust.VerificationMethod != "in_process" {
		t.Errorf("trust.verification_method=%q, want in_process", got.Trust.VerificationMethod)
	}
	if !strings.HasPrefix(got.Source.Actor, "human:") {
		t.Errorf("source.actor=%q, want human:* (derived from session, not args)", got.Source.Actor)
	}
	if got.Source.Intent != "refactor extract method" {
		t.Errorf("source.intent=%q, want %q", got.Source.Intent, "refactor extract method")
	}
}

// TestRecordChange_StatusDefaultsToModified — the most common agent
// flow ("I just edited these files") should not require status=modified
// per call; it defaults at the tool boundary.
func TestRecordChange_StatusDefaultsToModified(t *testing.T) {
	h := newTestHarness(t)
	disp := &stubRecordChangeDispatcher{}
	h.handler.changeDispatcher = disp
	sess := h.createSession()

	out, _ := invokeRecordChange(t, h, sess, map[string]interface{}{
		"repository_id": h.repoID,
		"branch":        "main",
		"files": []map[string]interface{}{
			{"path": "src/main.go"}, // status omitted on purpose
		},
	})

	if !out.Accepted {
		t.Errorf("Accepted=false, want true")
	}
	got := disp.lastCall()
	if got == nil {
		t.Fatalf("dispatcher not called")
	}
	if got.Files[0].Status != changewatch.FileChangeModified {
		t.Errorf("default status=%q, want %q", got.Files[0].Status, changewatch.FileChangeModified)
	}
}

// TestRecordChange_RejectsBadPath — the path-normalization contract
// is enforced at the tool boundary (cleaner MCP error than letting the
// router reject with rejected_invalid_paths).
func TestRecordChange_RejectsBadPath(t *testing.T) {
	h := newTestHarness(t)
	disp := &stubRecordChangeDispatcher{}
	h.handler.changeDispatcher = disp
	sess := h.createSession()

	_, tr := invokeRecordChange(t, h, sess, map[string]interface{}{
		"repository_id": h.repoID,
		"branch":        "main",
		"files": []map[string]interface{}{
			{"path": "../escape.go", "status": "modified"},
		},
	})
	if tr == nil || !tr.IsError {
		t.Fatalf("expected tool error for traversal path; got nothing")
	}
	if disp.callCount() != 0 {
		t.Errorf("dispatcher called despite invalid path; want 0 calls (rejected at tool boundary)")
	}
}

// TestRecordChange_RejectsRenameWithoutOldPath — semantic validation:
// status=renamed requires old_path.
func TestRecordChange_RejectsRenameWithoutOldPath(t *testing.T) {
	h := newTestHarness(t)
	disp := &stubRecordChangeDispatcher{}
	h.handler.changeDispatcher = disp
	sess := h.createSession()

	_, tr := invokeRecordChange(t, h, sess, map[string]interface{}{
		"repository_id": h.repoID,
		"branch":        "main",
		"files": []map[string]interface{}{
			{"path": "src/new.go", "status": "renamed"}, // no old_path
		},
	})
	if tr == nil || !tr.IsError {
		t.Fatalf("expected tool error for rename without old_path")
	}
	if !strings.Contains(tr.Content[0].Text, "old_path") {
		t.Errorf("error must mention old_path; got %q", tr.Content[0].Text)
	}
	if disp.callCount() != 0 {
		t.Errorf("dispatcher called despite missing old_path")
	}
}

// TestRecordChange_RejectsEmptyFiles — guardrail #1 (delta-only
// invariant) enforced at the tool boundary.
func TestRecordChange_RejectsEmptyFiles(t *testing.T) {
	h := newTestHarness(t)
	disp := &stubRecordChangeDispatcher{}
	h.handler.changeDispatcher = disp
	sess := h.createSession()

	_, tr := invokeRecordChange(t, h, sess, map[string]interface{}{
		"repository_id": h.repoID,
		"branch":        "main",
		"files":         []map[string]interface{}{},
	})
	if tr == nil || !tr.IsError {
		t.Fatalf("expected tool error for empty files")
	}
	if disp.callCount() != 0 {
		t.Errorf("dispatcher called despite empty files")
	}
}

// TestRecordChange_TenantBoundary — a caller targeting a repo they
// can't access gets the standard "not found" error and the dispatcher
// is NOT invoked. Multi-tenant isolation contract.
func TestRecordChange_TenantBoundary(t *testing.T) {
	h := newTestHarness(t)
	disp := &stubRecordChangeDispatcher{}
	h.handler.changeDispatcher = disp
	// Restrict the handler to allow ONLY a different repo than what
	// the test harness indexed. Any record_change for h.repoID should
	// be rejected as "not found / not accessible".
	h.handler.allowedRepos = map[string]bool{"some-other-repo": true}
	sess := h.createSession()

	_, tr := invokeRecordChange(t, h, sess, map[string]interface{}{
		"repository_id": h.repoID,
		"branch":        "main",
		"files": []map[string]interface{}{
			{"path": "src/main.go", "status": "modified"},
		},
	})
	if tr == nil || !tr.IsError {
		t.Fatalf("expected tool error for unauthorized repo")
	}
	if disp.callCount() != 0 {
		t.Errorf("dispatcher called despite tenant-boundary rejection (cross-tenant leak risk)")
	}
}

// TestRecordChange_DispatcherDispositions — every router outcome maps
// to the right `accepted` boolean and routes through to the response.
func TestRecordChange_DispatcherDispositions(t *testing.T) {
	cases := []struct {
		outcome      changewatch.SubmitOutcome
		err          error
		wantAccepted bool
	}{
		{changewatch.OutcomeIndexing, nil, true},
		{changewatch.OutcomeDeduped, nil, true},
		{changewatch.OutcomeRateLimited, errors.New("rate"), false},
		{changewatch.OutcomeBreakerTripped, errors.New("breaker"), false},
		{changewatch.OutcomeRejectedBranchMismatch, errors.New("branch"), false},
		{changewatch.OutcomeRejectedUnknownRepo, errors.New("repo"), false},
	}
	for _, c := range cases {
		t.Run(string(c.outcome), func(t *testing.T) {
			h := newTestHarness(t)
			disp := &stubRecordChangeDispatcher{outcome: c.outcome, err: c.err}
			h.handler.changeDispatcher = disp
			sess := h.createSession()
			out, _ := invokeRecordChange(t, h, sess, map[string]interface{}{
				"repository_id": h.repoID,
				"branch":        "main",
				"files": []map[string]interface{}{
					{"path": "src/main.go", "status": "modified"},
				},
			})
			if out.Accepted != c.wantAccepted {
				t.Errorf("outcome=%q Accepted=%v, want %v", c.outcome, out.Accepted, c.wantAccepted)
			}
			if out.RoutedTo != string(c.outcome) {
				t.Errorf("outcome=%q RoutedTo=%q, want %q", c.outcome, out.RoutedTo, c.outcome)
			}
		})
	}
}

// TestRecordChange_ActorDerivedFromSession — even when a malicious
// caller stuffs an Actor field somewhere clever, the source.actor is
// always derived from session.claims. The wire shape doesn't expose
// actor at all, so we exercise this by asserting the field is set on
// every call regardless of args.
func TestRecordChange_ActorDerivedFromSession(t *testing.T) {
	h := newTestHarness(t)
	disp := &stubRecordChangeDispatcher{}
	h.handler.changeDispatcher = disp
	sess := h.createSession()

	_, _ = invokeRecordChange(t, h, sess, map[string]interface{}{
		"repository_id": h.repoID,
		"branch":        "main",
		"files":         []map[string]interface{}{{"path": "src/x.go"}},
	})

	got := disp.lastCall()
	if got == nil {
		t.Fatal("dispatcher not called")
	}
	wantPrefix := "human:"
	if !strings.HasPrefix(got.Source.Actor, wantPrefix) {
		t.Errorf("source.actor=%q, want prefix %q (must derive from session.claims, not args)", got.Source.Actor, wantPrefix)
	}
	// The session was created with UserID=user-1 OrgID=org-1, so the
	// actor must reflect both.
	if !strings.Contains(got.Source.Actor, "user-1") {
		t.Errorf("source.actor=%q, want to contain user-1", got.Source.Actor)
	}
}

// TestRecordChange_DefenseInDepthOnNilDispatcher — when the dispatcher
// is nil, a hand-crafted tools/call (an agent that didn't refresh its
// tools/list cache after the operator turned change-watch off) gets a
// structured CAPABILITY_DISABLED error rather than crashing or
// silently no-op'ing. The tool is hidden from tools/list (the primary
// surface) but the handler defends in depth.
func TestRecordChange_DefenseInDepthOnNilDispatcher(t *testing.T) {
	h := newTestHarness(t)
	// Note: NOT setting h.handler.changeDispatcher.
	sess := h.createSession()

	resp := h.sendRPC(sess, 100, "tools/call", map[string]interface{}{
		"name": "record_change",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"branch":        "main",
			"files":         []map[string]interface{}{{"path": "src/main.go"}},
		},
	})
	if resp.Error != nil {
		t.Fatalf("expected tool result (with isError=true), got transport error: %s", resp.Error.Message)
	}
	b, _ := json.Marshal(resp.Result)
	var tr mcpToolResult
	if err := json.Unmarshal(b, &tr); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if !tr.IsError {
		t.Fatalf("expected isError=true; got success: %+v", tr)
	}
	// The structured envelope must carry the CAPABILITY_DISABLED code
	// so capability-aware callers can fall back gracefully.
	sb, ok := tr.Meta["sourcebridge"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool result _meta.sourcebridge missing or wrong shape: %+v", tr.Meta)
	}
	if sb["code"] != MCPErrCapabilityDisabled {
		t.Errorf("_meta.sourcebridge.code=%v, want %q", sb["code"], MCPErrCapabilityDisabled)
	}
}

// TestRecordChange_RejectsOversizedFiles — defense against a runaway
// agent or malicious caller flooding the dispatcher with a single
// massive call. The cap is generous (1024) but it exists.
func TestRecordChange_RejectsOversizedFiles(t *testing.T) {
	h := newTestHarness(t)
	disp := &stubRecordChangeDispatcher{}
	h.handler.changeDispatcher = disp
	sess := h.createSession()

	files := make([]map[string]interface{}, recordChangeMaxFiles+1)
	for i := range files {
		files[i] = map[string]interface{}{
			"path":   "src/file" + itoa(i) + ".go",
			"status": "modified",
		}
	}
	_, tr := invokeRecordChange(t, h, sess, map[string]interface{}{
		"repository_id": h.repoID,
		"branch":        "main",
		"files":         files,
	})
	if tr == nil || !tr.IsError {
		t.Fatalf("expected tool error for oversized files; got nothing")
	}
	if disp.callCount() != 0 {
		t.Errorf("dispatcher called despite oversized files; want 0 calls")
	}
}

// TestRecordChange_RejectsOversizedIntent — Intent flows into log lines
// and the freshness envelope. A malicious agent that sends a megabyte
// of intent should be rejected at the boundary, not after it lands in
// the audit trail.
func TestRecordChange_RejectsOversizedIntent(t *testing.T) {
	h := newTestHarness(t)
	disp := &stubRecordChangeDispatcher{}
	h.handler.changeDispatcher = disp
	sess := h.createSession()

	bigIntent := strings.Repeat("a", recordChangeMaxIntentLen+1)
	_, tr := invokeRecordChange(t, h, sess, map[string]interface{}{
		"repository_id": h.repoID,
		"branch":        "main",
		"intent":        bigIntent,
		"files":         []map[string]interface{}{{"path": "src/main.go"}},
	})
	if tr == nil || !tr.IsError {
		t.Fatalf("expected tool error for oversized intent")
	}
	if disp.callCount() != 0 {
		t.Errorf("dispatcher called despite oversized intent")
	}
}

// TestRecordChange_StripsControlCharsFromIntent — log-injection
// defense. ASCII control bytes and terminal escapes are stripped at
// the boundary so the structured-log entry stays clean.
func TestRecordChange_StripsControlCharsFromIntent(t *testing.T) {
	h := newTestHarness(t)
	disp := &stubRecordChangeDispatcher{}
	h.handler.changeDispatcher = disp
	sess := h.createSession()

	dirty := "refactor\x1b[31m red \x1b[0m \x00 done\x07"
	_, _ = invokeRecordChange(t, h, sess, map[string]interface{}{
		"repository_id": h.repoID,
		"branch":        "main",
		"intent":        dirty,
		"files":         []map[string]interface{}{{"path": "src/main.go"}},
	})
	got := disp.lastCall()
	if got == nil {
		t.Fatal("dispatcher not called")
	}
	if strings.ContainsAny(got.Source.Intent, "\x1b\x00\x07") {
		t.Errorf("source.intent retained control chars: %q", got.Source.Intent)
	}
	// Whitespace tabs and newlines should survive — they're benign.
	multiLine := "refactor\nextract method"
	disp.calls = nil
	_, _ = invokeRecordChange(t, h, sess, map[string]interface{}{
		"repository_id": h.repoID,
		"branch":        "main",
		"intent":        multiLine,
		"files":         []map[string]interface{}{{"path": "src/main.go"}},
	})
	got = disp.lastCall()
	if !strings.Contains(got.Source.Intent, "\n") {
		t.Errorf("source.intent stripped a benign newline: %q", got.Source.Intent)
	}
}

// TestRecordChange_RejectsOversizedRequirementIDs — same defense for
// the requirement_ids attribution list.
func TestRecordChange_RejectsOversizedRequirementIDs(t *testing.T) {
	h := newTestHarness(t)
	disp := &stubRecordChangeDispatcher{}
	h.handler.changeDispatcher = disp
	sess := h.createSession()

	ids := make([]string, recordChangeMaxRequirementIDs+1)
	for i := range ids {
		ids[i] = "REQ-" + itoa(i)
	}
	_, tr := invokeRecordChange(t, h, sess, map[string]interface{}{
		"repository_id":   h.repoID,
		"branch":          "main",
		"requirement_ids": ids,
		"files":           []map[string]interface{}{{"path": "src/main.go"}},
	})
	if tr == nil || !tr.IsError {
		t.Fatalf("expected tool error for oversized requirement_ids")
	}
	if disp.callCount() != 0 {
		t.Errorf("dispatcher called despite oversized requirement_ids")
	}
}

// itoa is a tiny inline shim — the changewatch package has its own
// version; keeping a local one here so we don't depend on package
// internals.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(byte('0'+n%10)) + s
		n /= 10
	}
	return s
}

// TestNewRecordChangeEventID_FormatAndUniqueness — small white-box
// check on the connector-side ID stamper. Format must be "rc-<32hex>"
// and IDs must collide-resist over a tight loop.
func TestNewRecordChangeEventID_FormatAndUniqueness(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := newRecordChangeEventID()
		if !strings.HasPrefix(id, "rc-") {
			t.Errorf("id=%q does not have rc- prefix", id)
		}
		if len(id) != 3+32 {
			t.Errorf("id=%q has length %d, want 35 (rc- + 32hex)", id, len(id))
		}
		if seen[id] {
			t.Errorf("duplicate id=%q in 1000-iteration loop", id)
		}
		seen[id] = true
	}
}
