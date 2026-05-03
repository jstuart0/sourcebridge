// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/indexer"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
)

// ---------------------------------------------------------------------------
// Mock: mockReviewCaller
//
// Satisfies BOTH mcpWorkerCaller AND workerReviewCaller. Used for the
// happy-path and degraded-not-available tests. The intentional "interface
// mismatch" test uses the existing mockWorkerCaller (mcp_test.go:32) which
// only satisfies mcpWorkerCaller — that is by design.
// ---------------------------------------------------------------------------

type mockReviewCaller struct {
	available  bool
	reviewFunc func(ctx context.Context, req *reasoningv1.ReviewFileRequest) (*reasoningv1.ReviewFileResponse, error)
}

func (m *mockReviewCaller) IsAvailable() bool { return m.available }

func (m *mockReviewCaller) AnswerQuestion(ctx context.Context, req *reasoningv1.AnswerQuestionRequest) (*reasoningv1.AnswerQuestionResponse, error) {
	return &reasoningv1.AnswerQuestionResponse{Answer: "mock"}, nil
}

func (m *mockReviewCaller) ReviewFile(ctx context.Context, req *reasoningv1.ReviewFileRequest) (*reasoningv1.ReviewFileResponse, error) {
	if m.reviewFunc != nil {
		return m.reviewFunc(ctx, req)
	}
	return &reasoningv1.ReviewFileResponse{
		Template: req.GetTemplate(),
		Findings: []*reasoningv1.ReviewFinding{},
	}, nil
}

// Compile-time checks: mockReviewCaller satisfies both interfaces.
var _ mcpWorkerCaller = (*mockReviewCaller)(nil)
var _ workerReviewCaller = (*mockReviewCaller)(nil)

// ---------------------------------------------------------------------------
// Helpers shared across this file
// ---------------------------------------------------------------------------

// newReviewHarnessWithFiles constructs a test harness whose graph store
// contains a fresh repository with the given file names (each with one
// exported Go symbol named after the file). Returns the harness and the
// newly-created repo ID.
func newReviewHarnessWithFiles(t *testing.T, files []string) (*mcpTestHarness, string) {
	t.Helper()
	h := newTestHarness(t) // creates the default "test-repo"
	// Create a separate review-specific repo so we don't collide with
	// the default test-repo fixture.
	fileResults := make([]indexer.FileResult, 0, len(files))
	for _, fp := range files {
		baseName := fp
		if idx := strings.LastIndexByte(fp, '/'); idx >= 0 {
			baseName = fp[idx+1:]
		}
		// Strip .go suffix for the symbol name.
		symName := strings.TrimSuffix(baseName, ".go")
		// Capitalise first letter to make it a public symbol.
		if len(symName) > 0 {
			symName = strings.ToUpper(symName[:1]) + symName[1:]
		}
		fileResults = append(fileResults, indexer.FileResult{
			Path:     fp,
			Language: "go",
			Symbols: []indexer.Symbol{
				{Name: symName, QualifiedName: "pkg." + symName,
					Kind: "function", Language: "go", FilePath: fp,
					StartLine: 1, EndLine: 5},
			},
		})
	}
	result := &indexer.IndexResult{
		RepoName: "review-test-repo",
		RepoPath: "/tmp/review-test-repo-" + t.Name(),
		Files:    fileResults,
	}
	repo, err := h.store.StoreIndexResult(result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}
	return h, repo.ID
}

// parseReviewResult unmarshals a tools/call response for get_review_for_diff
// into a generic map.
func parseReviewResult(t *testing.T, resp jsonRPCResponse) map[string]interface{} {
	t.Helper()
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("unexpected tool error: %s", text)
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("json.Unmarshal reviewForDiffResult: %v (text: %s)", err, text)
	}
	return result
}

// sendReviewRPC sends a tools/call for get_review_for_diff with the given
// extra arguments merged in.
func sendReviewRPC(h *mcpTestHarness, sess *mcpSession, repoID string, extra map[string]interface{}) jsonRPCResponse {
	args := map[string]interface{}{
		"repository_id": repoID,
		"files":         []string{"api.go"},
	}
	for k, v := range extra {
		args[k] = v
	}
	return h.sendRPC(sess, 1, "tools/call", map[string]interface{}{
		"name":      "get_review_for_diff",
		"arguments": args,
	})
}

// ---------------------------------------------------------------------------
// Test: StructuralOnly (include_ai_review: false)
// ---------------------------------------------------------------------------

// TestMCP_GetReviewForDiff_StructuralOnly verifies that with
// include_ai_review: false (the default) no AI calls are made and the
// structural payload is returned with findings: [] and degraded: false.
func TestMCP_GetReviewForDiff_StructuralOnly(t *testing.T) {
	h, repoID := newReviewHarnessWithFiles(t, []string{"api.go", "util.go"})

	// Replace the worker with a mockReviewCaller that would panic if
	// ReviewFile were called — confirming no AI calls happen.
	panicIfCalled := &mockReviewCaller{
		available: true,
		reviewFunc: func(_ context.Context, _ *reasoningv1.ReviewFileRequest) (*reasoningv1.ReviewFileResponse, error) {
			panic("ReviewFile must not be called when include_ai_review is false")
		},
	}
	h.handler.worker = panicIfCalled
	sess := h.createSession()

	resp := sendReviewRPC(h, sess, repoID, map[string]interface{}{
		"include_ai_review": false,
	})

	result := parseReviewResult(t, resp)

	// Structural fields present.
	if _, ok := result["touched_files"]; !ok {
		t.Error("missing touched_files")
	}
	if _, ok := result["linked_requirements"]; !ok {
		t.Error("missing linked_requirements")
	}
	if _, ok := result["unlinked_public_surface"]; !ok {
		t.Error("missing unlinked_public_surface")
	}

	// findings must be an empty array (not nil / missing).
	findings, ok := result["findings"]
	if !ok {
		t.Error("missing findings field")
	}
	findingsSlice, _ := findings.([]interface{})
	if len(findingsSlice) != 0 {
		t.Errorf("expected empty findings, got %d", len(findingsSlice))
	}

	// degraded must be false.
	if degraded, _ := result["degraded"].(bool); degraded {
		t.Error("expected degraded: false")
	}

	// risk_score present.
	if _, ok := result["risk_score"]; !ok {
		t.Error("missing risk_score")
	}
}

// ---------------------------------------------------------------------------
// Test: AI path happy path
// ---------------------------------------------------------------------------

// TestMCP_GetReviewForDiff_AIPath_HappyPath verifies that when the worker
// satisfies workerReviewCaller and IsAvailable() is true, ReviewFile is
// called per file/template and findings are aggregated.
func TestMCP_GetReviewForDiff_AIPath_HappyPath(t *testing.T) {
	h, repoID := newReviewHarnessWithFiles(t, []string{"api.go"})

	reviewCaller := &mockReviewCaller{
		available: true,
		reviewFunc: func(_ context.Context, req *reasoningv1.ReviewFileRequest) (*reasoningv1.ReviewFileResponse, error) {
			return &reasoningv1.ReviewFileResponse{
				Template: req.GetTemplate(),
				Findings: []*reasoningv1.ReviewFinding{
					{
						Severity: "HIGH",
						Message:  "potential SQL injection",
						Category: "security",
					},
					{
						Severity: "LOW",
						Message:  "missing error wrap",
						Category: "maintainability",
					},
				},
			}, nil
		},
	}
	h.handler.worker = reviewCaller
	sess := h.createSession()

	resp := sendReviewRPC(h, sess, repoID, map[string]interface{}{
		"include_ai_review": true,
		"templates":         []string{"security"},
		"max_files":         5,
	})

	result := parseReviewResult(t, resp)

	// findings must be non-empty.
	findings, ok := result["findings"].([]interface{})
	if !ok || len(findings) == 0 {
		t.Fatalf("expected at least one finding, got: %v", result["findings"])
	}

	// Verify first finding has expected fields.
	first, _ := findings[0].(map[string]interface{})
	if first["severity"] == "" {
		t.Error("finding missing severity")
	}
	if first["message"] == "" {
		t.Error("finding missing message")
	}
	if first["file_path"] == "" {
		t.Error("finding missing file_path")
	}

	// degraded must be false.
	if degraded, _ := result["degraded"].(bool); degraded {
		t.Error("expected degraded: false on happy path")
	}

	// templates_used must be echoed.
	tmplsUsed, _ := result["templates_used"].([]interface{})
	if len(tmplsUsed) == 0 {
		t.Error("expected templates_used to be populated")
	}
}

// ---------------------------------------------------------------------------
// Test: Degraded — worker nil
// ---------------------------------------------------------------------------

// TestMCP_GetReviewForDiff_AIDegraded_WorkerNil verifies that when h.worker
// is nil and include_ai_review is true, the response carries degraded: true
// with the "worker not connected" reason.
func TestMCP_GetReviewForDiff_AIDegraded_WorkerNil(t *testing.T) {
	h, repoID := newReviewHarnessWithFiles(t, []string{"api.go"})
	h.handler.worker = nil // explicitly nil
	sess := h.createSession()

	resp := sendReviewRPC(h, sess, repoID, map[string]interface{}{
		"include_ai_review": true,
	})

	result := parseReviewResult(t, resp)

	if degraded, _ := result["degraded"].(bool); !degraded {
		t.Error("expected degraded: true when worker is nil")
	}
	reason, _ := result["degraded_reason"].(string)
	if reason == "" {
		t.Error("expected non-empty degraded_reason")
	}
	if !strings.Contains(reason, "not connected") {
		t.Errorf("expected 'not connected' in degraded_reason, got: %q", reason)
	}

	// Structural payload still present.
	if _, ok := result["touched_files"]; !ok {
		t.Error("expected touched_files even in degraded response")
	}
}

// ---------------------------------------------------------------------------
// Test: Degraded — interface mismatch
// ---------------------------------------------------------------------------

// TestMCP_GetReviewForDiff_AIDegraded_InterfaceMismatch verifies that when
// h.worker satisfies mcpWorkerCaller but NOT workerReviewCaller (i.e. the
// existing mockWorkerCaller from mcp_test.go:32), the response carries
// degraded: true with the "interface not implemented" reason.
func TestMCP_GetReviewForDiff_AIDegraded_InterfaceMismatch(t *testing.T) {
	h, repoID := newReviewHarnessWithFiles(t, []string{"api.go"})
	// mockWorkerCaller only implements mcpWorkerCaller, NOT workerReviewCaller.
	h.handler.worker = &mockWorkerCaller{available: true}
	sess := h.createSession()

	resp := sendReviewRPC(h, sess, repoID, map[string]interface{}{
		"include_ai_review": true,
	})

	result := parseReviewResult(t, resp)

	if degraded, _ := result["degraded"].(bool); !degraded {
		t.Error("expected degraded: true on interface mismatch")
	}
	reason, _ := result["degraded_reason"].(string)
	if !strings.Contains(reason, "interface not implemented") {
		t.Errorf("expected 'interface not implemented' in degraded_reason, got: %q", reason)
	}
}

// ---------------------------------------------------------------------------
// Test: Degraded — IsAvailable returns false
// ---------------------------------------------------------------------------

// TestMCP_GetReviewForDiff_AIDegraded_NotAvailable verifies that when the
// worker implements workerReviewCaller but IsAvailable() returns false, the
// response carries degraded: true.
func TestMCP_GetReviewForDiff_AIDegraded_NotAvailable(t *testing.T) {
	h, repoID := newReviewHarnessWithFiles(t, []string{"api.go"})
	h.handler.worker = &mockReviewCaller{available: false}
	sess := h.createSession()

	resp := sendReviewRPC(h, sess, repoID, map[string]interface{}{
		"include_ai_review": true,
	})

	result := parseReviewResult(t, resp)

	if degraded, _ := result["degraded"].(bool); !degraded {
		t.Error("expected degraded: true when worker not available")
	}
	reason, _ := result["degraded_reason"].(string)
	if !strings.Contains(reason, "not connected") {
		t.Errorf("expected 'not connected' in degraded_reason, got: %q", reason)
	}
}

// ---------------------------------------------------------------------------
// Test: Templates cap enforcement
// ---------------------------------------------------------------------------

// TestMCP_GetReviewForDiff_TemplatesCap verifies that providing more than 5
// templates returns errInvalidArguments.
func TestMCP_GetReviewForDiff_TemplatesCap(t *testing.T) {
	h, repoID := newReviewHarnessWithFiles(t, []string{"api.go"})
	sess := h.createSession()

	resp := h.sendRPC(sess, 1, "tools/call", map[string]interface{}{
		"name": "get_review_for_diff",
		"arguments": map[string]interface{}{
			"repository_id": repoID,
			"files":         []string{"api.go"},
			"templates":     []string{"security", "solid", "maintainability", "reliability", "performance", "extra"},
		},
	})

	_, isErr := parseToolText(resp)
	if !isErr {
		t.Error("expected tool error when templates length > 5")
	}
}

// ---------------------------------------------------------------------------
// Test: max_files cap (structural test — AI path only reviews capped set)
// ---------------------------------------------------------------------------

// TestMCP_GetReviewForDiff_MaxFilesCap verifies that when the diff touches
// more files than max_files (default 5), only max_files are reviewed in the
// AI pass and truncated is set to true.
func TestMCP_GetReviewForDiff_MaxFilesCap(t *testing.T) {
	// Seed 10 files into the repo.
	files := make([]string, 10)
	for i := range files {
		files[i] = []string{
			"a.go", "b.go", "c.go", "d.go", "e.go",
			"f.go", "g.go", "h.go", "i.go", "j.go",
		}[i]
	}
	h, repoID := newReviewHarnessWithFiles(t, files)

	callCount := 0
	reviewer := &mockReviewCaller{
		available: true,
		reviewFunc: func(_ context.Context, req *reasoningv1.ReviewFileRequest) (*reasoningv1.ReviewFileResponse, error) {
			callCount++
			return &reasoningv1.ReviewFileResponse{}, nil
		},
	}
	h.handler.worker = reviewer
	sess := h.createSession()

	resp := h.sendRPC(sess, 1, "tools/call", map[string]interface{}{
		"name": "get_review_for_diff",
		"arguments": map[string]interface{}{
			"repository_id":    repoID,
			"files":            files, // all 10 files
			"include_ai_review": true,
			"max_files":        5,
			"templates":        []string{"security"},
		},
	})

	result := parseReviewResult(t, resp)

	// truncated must be true because 10 files > max_files of 5.
	if truncated, _ := result["truncated"].(bool); !truncated {
		t.Error("expected truncated: true when files > max_files")
	}

	// AI was invoked for at most max_files × len(templates) = 5 × 1 = 5 calls.
	if callCount > 5 {
		t.Errorf("ReviewFile called %d times, expected at most 5 (max_files=5, templates=1)", callCount)
	}

	// The structural report still reflects all 10 touched files.
	tfs, _ := result["touched_files"].([]interface{})
	if len(tfs) != 10 {
		t.Errorf("expected 10 touched_files in structural report, got %d", len(tfs))
	}
}

// ---------------------------------------------------------------------------
// Test: Neither commit_range nor files provided
// ---------------------------------------------------------------------------

// TestMCP_GetReviewForDiff_NeitherInputProvided verifies that omitting both
// commit_range and files returns errInvalidArguments.
func TestMCP_GetReviewForDiff_NeitherInputProvided(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 1, "tools/call", map[string]interface{}{
		"name": "get_review_for_diff",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			// no commit_range or files
		},
	})

	_, isErr := parseToolText(resp)
	if !isErr {
		t.Error("expected tool error when neither commit_range nor files is provided")
	}
}

// ---------------------------------------------------------------------------
// Test: Repository not found
// ---------------------------------------------------------------------------

// TestMCP_GetReviewForDiff_RepoNotFound verifies that passing an unknown
// repository_id returns a tool error.
func TestMCP_GetReviewForDiff_RepoNotFound(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 1, "tools/call", map[string]interface{}{
		"name": "get_review_for_diff",
		"arguments": map[string]interface{}{
			"repository_id": "repo-does-not-exist",
			"files":         []string{"main.go"},
		},
	})

	_, isErr := parseToolText(resp)
	if !isErr {
		t.Error("expected tool error for unknown repository_id")
	}
}

// ---------------------------------------------------------------------------
// Test: Response shape (JSON round-trip)
// ---------------------------------------------------------------------------

// TestMCP_GetReviewForDiff_ResponseShape verifies that the JSON response
// contains the promoted embedded fields (touched_files, linked_requirements,
// unlinked_public_surface, summary), plus the AI-review layer fields.
func TestMCP_GetReviewForDiff_ResponseShape(t *testing.T) {
	h, repoID := newReviewHarnessWithFiles(t, []string{"api.go"})
	h.handler.worker = &mockReviewCaller{available: true}
	sess := h.createSession()

	resp := sendReviewRPC(h, sess, repoID, map[string]interface{}{
		"include_ai_review": true,
		"templates":         []string{"security"},
	})

	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("unexpected tool error: %s", text)
	}

	jsonBytes := []byte(text)

	requiredFields := []string{
		"touched_files",
		"linked_requirements",
		"unlinked_public_surface",
		"findings",
		"risk_score",
		"degraded",
	}
	for _, field := range requiredFields {
		if !strings.Contains(string(jsonBytes), `"`+field+`"`) {
			t.Errorf("response JSON missing field %q", field)
		}
	}

	// Round-trip decode to confirm valid JSON structure.
	var result map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &result); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	// touched_files must be an array (even if empty).
	if tfs := result["touched_files"]; tfs == nil {
		t.Error("touched_files must not be null")
	}
	if findings := result["findings"]; findings == nil {
		t.Error("findings must not be null")
	}
}

// ---------------------------------------------------------------------------
// Test: Deadline respected (truncated when ReviewFile hangs)
// ---------------------------------------------------------------------------

// TestMCP_GetReviewForDiff_DeadlineRespected verifies that when ReviewFile
// hangs until context cancellation, the tool returns within ~90 seconds with
// truncated: true. The test uses a sub-second mock deadline to avoid slow CI.
//
// This test patches the handler's callGetReviewForDiff via a direct call
// (bypassing HTTP dispatch) so it can inject its own context with a tight
// deadline.
func TestMCP_GetReviewForDiff_DeadlineRespected(t *testing.T) {
	h, repoID := newReviewHarnessWithFiles(t, []string{"api.go"})

	// mock that blocks until the context is cancelled.
	blockingReviewer := &mockReviewCaller{
		available: true,
		reviewFunc: func(ctx context.Context, _ *reasoningv1.ReviewFileRequest) (*reasoningv1.ReviewFileResponse, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	h.handler.worker = blockingReviewer
	sess := h.createSession()

	// Inject a 200ms context deadline so the test completes quickly.
	// The handler's own 90s deadline is overridden by the tighter parent ctx.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	args, _ := json.Marshal(map[string]interface{}{
		"repository_id":    repoID,
		"files":            []string{"api.go"},
		"include_ai_review": true,
		"templates":        []string{"security"},
	})

	start := time.Now()
	raw, err := h.handler.callGetReviewForDiff(ctx, sess, args)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("callGetReviewForDiff returned unexpected error: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("handler took %v — should have returned promptly after context deadline", elapsed)
	}

	// Marshal to map for inspection.
	b, _ := json.Marshal(raw)
	var result map[string]interface{}
	_ = json.Unmarshal(b, &result)

	if truncated, _ := result["truncated"].(bool); !truncated {
		t.Error("expected truncated: true when ReviewFile was blocked and context deadline fired")
	}
}
