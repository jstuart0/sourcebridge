// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMCP_PromptsList(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 2, "prompts/list", nil)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	var body struct {
		Prompts []mcpPromptDefinition `json:"prompts"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("parse: %v", err)
	}

	expected := []string{"review_security_of_file", "explain_requirement_implementation", "why_is_this_test_failing"}
	names := make(map[string]bool, len(body.Prompts))
	for _, p := range body.Prompts {
		names[p.Name] = true
	}
	for _, e := range expected {
		if !names[e] {
			t.Errorf("missing prompt: %s", e)
		}
	}
}

func TestMCP_PromptsGet_Renders(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 2, "prompts/get", map[string]interface{}{
		"name": "review_security_of_file",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "main.go",
		},
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	raw, _ := json.Marshal(resp.Result)
	var body mcpPromptGetResult
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(body.Messages) == 0 {
		t.Fatal("expected at least one message in rendered prompt")
	}
	if body.Messages[0].Role != "user" {
		t.Errorf("expected user role, got %q", body.Messages[0].Role)
	}
	if !strings.Contains(body.Messages[0].Content.Text, "main.go") {
		t.Errorf("rendered text should mention the file_path argument; got: %s", body.Messages[0].Content.Text)
	}
}

func TestMCP_PromptsGet_MissingRequiredArg(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 2, "prompts/get", map[string]interface{}{
		"name":      "review_security_of_file",
		"arguments": map[string]interface{}{"repository_id": h.repoID},
		// file_path missing
	})
	if resp.Error == nil {
		t.Fatal("expected JSON-RPC error when required argument is missing")
	}
}

func TestMCP_PromptsGet_UnknownPrompt(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 2, "prompts/get", map[string]interface{}{
		"name":      "nonexistent_prompt",
		"arguments": map[string]interface{}{},
	})
	if resp.Error == nil {
		t.Fatal("expected JSON-RPC error for unknown prompt")
	}
}

func TestMCP_PromptsGet_WhyTestFailing_WithFailureOutput(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 2, "prompts/get", map[string]interface{}{
		"name": "why_is_this_test_failing",
		"arguments": map[string]interface{}{
			"repository_id":  h.repoID,
			"test_file":      "main_test.go",
			"failure_output": "--- FAIL: TestFoo (0.01s)\n    main_test.go:15: expected 1, got 2",
		},
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	var body mcpPromptGetResult
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strings.Contains(body.Messages[0].Content.Text, "expected 1, got 2") {
		t.Error("expected failure_output to be inlined into the rendered prompt")
	}
}
