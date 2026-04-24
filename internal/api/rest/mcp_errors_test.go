// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMCP_ErrorEnvelope_Structured asserts that tools returning an
// *mcpToolError produce the full structured envelope: isError=true,
// a content[].text with the human message, AND a _meta.sourcebridge
// block with code + remediation.
func TestMCP_ErrorEnvelope_Structured(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	// get_callees for an unknown symbol — resolveSymbol returns
	// errSymbolNotFound, which carries MCPErrSymbolNotFound and a
	// remediation hint.
	seedCallGraphTestData(t, h)
	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "get_callees",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "main.go",
			"symbol_name":   "DoesNotExist",
		},
	})

	if resp.Error != nil {
		t.Fatalf("RPC error (expected tool-level error instead): %v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)

	var result mcpToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected isError=true")
	}

	// Vanilla-client path: content[].text is a complete sentence.
	if len(result.Content) == 0 {
		t.Fatal("expected at least one content item")
	}
	if !strings.Contains(result.Content[0].Text, "DoesNotExist") {
		t.Errorf("content text should mention the symbol name, got: %s", result.Content[0].Text)
	}

	// Structured-client path: _meta.sourcebridge.code + remediation.
	if result.Meta == nil {
		t.Fatal("expected _meta to be populated for structured errors")
	}
	sb, ok := result.Meta["sourcebridge"].(map[string]interface{})
	if !ok {
		t.Fatalf("_meta.sourcebridge missing or wrong shape: %T", result.Meta["sourcebridge"])
	}
	code, _ := sb["code"].(string)
	if code != MCPErrSymbolNotFound {
		t.Errorf("expected code=%s, got %s", MCPErrSymbolNotFound, code)
	}
	remediation, _ := sb["remediation"].(string)
	if remediation == "" {
		t.Error("expected remediation to be non-empty")
	}
}

// TestMCP_ErrorEnvelope_PlainError asserts that handlers returning a
// plain error (fmt.Errorf, not *mcpToolError) still produce a valid
// envelope — content is complete, _meta is omitted. Vanilla clients
// work either way; structured clients just don't get metadata when
// the code path hasn't been migrated yet.
func TestMCP_ErrorEnvelope_PlainError(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	// get_file_imports with missing file_path returns a plain
	// fmt.Errorf. That exercises the "no _meta" path of the dispatch.
	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "get_file_imports",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
		},
	})
	raw, _ := json.Marshal(resp.Result)
	var result mcpToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !result.IsError {
		t.Error("expected isError=true")
	}
	if len(result.Content) == 0 || result.Content[0].Text == "" {
		t.Error("expected a complete content[].text even for plain errors")
	}
	// Meta may be nil for plain errors — both outcomes are valid MCP.
}
