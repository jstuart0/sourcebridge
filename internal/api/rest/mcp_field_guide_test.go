// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/knowledge"
)

// ---------------------------------------------------------------------------
// Phase 2b: get_field_guide tests
// ---------------------------------------------------------------------------
//
// Coverage:
//   - Happy-path for each of the four format variants (cliff_notes,
//     learning_path, code_tour, workflow_story)
//   - Invalid format string → errInvalidArguments
//   - Cache miss (artifact not yet generated) → message-only response
//   - Unknown repository_id → errRepositoryNotIndexed

// seedFieldGuideArtifact stores a StatusReady artifact of the given type in
// the test harness knowledge store and returns it. The scope defaults to
// repository-level with developer/medium defaults, matching the tool's
// defaulting logic.
func seedFieldGuideArtifact(h *mcpTestHarness, id string, artifactType knowledge.ArtifactType) *knowledge.Artifact {
	scope := knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository}.NormalizePtr()
	a := &knowledge.Artifact{
		ID:           id,
		RepositoryID: h.repoID,
		Type:         artifactType,
		Audience:     knowledge.AudienceDeveloper,
		Depth:        knowledge.DepthMedium,
		Scope:        scope,
		Status:       knowledge.StatusReady,
		GeneratedAt:  time.Now(),
		Sections: []knowledge.Section{
			{
				Title:      "Introduction",
				Content:    "This is the introduction section.",
				Summary:    "Intro summary.",
				Confidence: knowledge.ConfidenceHigh,
			},
			{
				Title:   "Details",
				Content: "Detailed content goes here.",
			},
		},
	}
	h.ks.artifacts[id] = a
	return a
}

// ---------------------------------------------------------------------------
// Test 1: HappyPath — cliff_notes
// ---------------------------------------------------------------------------

func TestMCP_GetFieldGuide_HappyPath_CliffNotes(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	seedFieldGuideArtifact(h, "fg-cn-1", knowledge.ArtifactCliffNotes)

	resp := h.sendRPC(sess, 10, "tools/call", map[string]interface{}{
		"name": "get_field_guide",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"format":        "cliff_notes",
		},
	})

	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result struct {
		RepositoryID  string              `json:"repository_id"`
		Format        string              `json:"format"`
		Content       string              `json:"content"`
		Sections      []fieldGuideSection `json:"sections"`
		ArtifactMeta  map[string]interface{} `json:"_artifact_meta"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if result.Format != "cliff_notes" {
		t.Errorf("expected format=cliff_notes, got %q", result.Format)
	}
	if result.RepositoryID != h.repoID {
		t.Errorf("expected repository_id=%q, got %q", h.repoID, result.RepositoryID)
	}
	if len(result.Sections) != 2 {
		t.Errorf("expected 2 sections, got %d", len(result.Sections))
	}
	if result.Sections[0].Title != "Introduction" {
		t.Errorf("expected first section title 'Introduction', got %q", result.Sections[0].Title)
	}
	if result.Content == "" {
		t.Error("expected non-empty content field")
	}
	if !strings.Contains(result.Content, "Introduction") {
		t.Errorf("content should contain section title, got: %q", result.Content)
	}
	if result.ArtifactMeta == nil {
		t.Error("expected _artifact_meta to be present")
	}
}

// ---------------------------------------------------------------------------
// Test 2: HappyPath — learning_path
// ---------------------------------------------------------------------------

func TestMCP_GetFieldGuide_HappyPath_LearningPath(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	seedFieldGuideArtifact(h, "fg-lp-1", knowledge.ArtifactLearningPath)

	resp := h.sendRPC(sess, 11, "tools/call", map[string]interface{}{
		"name": "get_field_guide",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"format":        "learning_path",
		},
	})

	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result struct {
		Format   string              `json:"format"`
		Sections []fieldGuideSection `json:"sections"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if result.Format != "learning_path" {
		t.Errorf("expected format=learning_path, got %q", result.Format)
	}
	if len(result.Sections) == 0 {
		t.Error("expected at least one section")
	}
}

// ---------------------------------------------------------------------------
// Test 3: HappyPath — code_tour
// ---------------------------------------------------------------------------

func TestMCP_GetFieldGuide_HappyPath_CodeTour(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	seedFieldGuideArtifact(h, "fg-ct-1", knowledge.ArtifactCodeTour)

	resp := h.sendRPC(sess, 12, "tools/call", map[string]interface{}{
		"name": "get_field_guide",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"format":        "code_tour",
		},
	})

	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result struct {
		Format   string              `json:"format"`
		Sections []fieldGuideSection `json:"sections"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if result.Format != "code_tour" {
		t.Errorf("expected format=code_tour, got %q", result.Format)
	}
	if len(result.Sections) == 0 {
		t.Error("expected at least one section")
	}
}

// ---------------------------------------------------------------------------
// Test 4: HappyPath — workflow_story
// ---------------------------------------------------------------------------

func TestMCP_GetFieldGuide_HappyPath_WorkflowStory(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	seedFieldGuideArtifact(h, "fg-ws-1", knowledge.ArtifactWorkflowStory)

	resp := h.sendRPC(sess, 13, "tools/call", map[string]interface{}{
		"name": "get_field_guide",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"format":        "workflow_story",
		},
	})

	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result struct {
		Format   string              `json:"format"`
		Sections []fieldGuideSection `json:"sections"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if result.Format != "workflow_story" {
		t.Errorf("expected format=workflow_story, got %q", result.Format)
	}
	if len(result.Sections) == 0 {
		t.Error("expected at least one section")
	}
}

// ---------------------------------------------------------------------------
// Test 5: InvalidFormat — unknown format string returns errInvalidArguments
// ---------------------------------------------------------------------------

func TestMCP_GetFieldGuide_InvalidFormat(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 14, "tools/call", map[string]interface{}{
		"name": "get_field_guide",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"format":        "summary_notes",
		},
	})

	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected tool error for invalid format, got success: %s", text)
	}
	if !strings.Contains(strings.ToLower(text), "unknown format") {
		t.Errorf("expected 'unknown format' in error message, got: %s", text)
	}
	if !strings.Contains(text, "summary_notes") {
		t.Errorf("expected the bad format value in error message, got: %s", text)
	}

	// Verify structured error code is INVALID_ARGUMENTS.
	b, _ := json.Marshal(resp.Result)
	var tr mcpToolResult
	if err := json.Unmarshal(b, &tr); err == nil && tr.Meta != nil {
		if sb, ok := tr.Meta["sourcebridge"].(map[string]interface{}); ok {
			if code, _ := sb["code"].(string); code != MCPErrInvalidArguments {
				t.Errorf("expected error code %q, got %q", MCPErrInvalidArguments, code)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Test 6: CacheMiss — artifact not yet generated returns message-only response
// ---------------------------------------------------------------------------

func TestMCP_GetFieldGuide_CacheMiss(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	// Seed a cliff_notes artifact but request learning_path — the learning_path
	// has never been generated for this scope.
	seedFieldGuideArtifact(h, "fg-cn-only", knowledge.ArtifactCliffNotes)

	resp := h.sendRPC(sess, 15, "tools/call", map[string]interface{}{
		"name": "get_field_guide",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"format":        "learning_path",
		},
	})

	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("cache-miss should not return a tool error, got: %s", text)
	}

	var result struct {
		Artifact interface{} `json:"artifact"`
		Message  string      `json:"message"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("failed to parse cache-miss response: %v", err)
	}
	if result.Artifact != nil {
		t.Error("expected null artifact on cache miss")
	}
	if result.Message == "" {
		t.Error("expected non-empty message on cache miss")
	}
	// Verify the message matches the spec: no GraphQL mutation names.
	if strings.Contains(result.Message, "GenerateLearningPath") ||
		strings.Contains(result.Message, "GenerateCodeTour") ||
		strings.Contains(result.Message, "GenerateWorkflowStory") ||
		strings.Contains(result.Message, "GenerateCliffNotes") {
		t.Errorf("cache-miss message must not mention GraphQL mutation names, got: %q", result.Message)
	}
	// Must mention the format.
	if !strings.Contains(result.Message, "learning path") {
		t.Errorf("cache-miss message should mention the format 'learning path', got: %q", result.Message)
	}
	// Must direct to the web UI.
	if !strings.Contains(result.Message, "web UI") {
		t.Errorf("cache-miss message should mention the web UI, got: %q", result.Message)
	}
}

// ---------------------------------------------------------------------------
// Test 7: RepoNotFound — unknown repository_id returns an error
// ---------------------------------------------------------------------------

func TestMCP_GetFieldGuide_RepoNotFound(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 16, "tools/call", map[string]interface{}{
		"name": "get_field_guide",
		"arguments": map[string]interface{}{
			"repository_id": "repo-does-not-exist-xyz",
			"format":        "cliff_notes",
		},
	})

	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected tool error for unknown repo, got success: %s", text)
	}
	// checkRepoAccess returns "Repository not found or not accessible"
	if !strings.Contains(strings.ToLower(text), "not found") &&
		!strings.Contains(strings.ToLower(text), "not accessible") &&
		!strings.Contains(strings.ToLower(text), "not indexed") {
		t.Errorf("error should mention repo not found/accessible/indexed, got: %s", text)
	}
}
