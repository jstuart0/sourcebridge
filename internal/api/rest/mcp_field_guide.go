// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/knowledge"
)

// Phase 2b — get_field_guide.
//
// A single tool with a format enum that routes to one of four pre-seeded
// artifact types: cliff_notes, learning_path, code_tour, workflow_story.
//
// All four are synchronous reads from the knowledge store — the same
// GetArtifactByKey path that callGetCliffNotes uses, parameterised by the
// format. No on-demand generation; the artifacts are seeded by
// seedRepositoryFieldGuide at index time.
//
// Supersedes onboard_new_contributor for multi-format field guides.

// fieldGuideSection is the canonical section shape returned by get_field_guide.
//
// Fields with omitempty are not universally populated across artifact types:
//   - Anchor:           present on LearningPath, absent on CliffNotes.
//   - EstimatedReadMin: present on LearningPath, absent on others.
//   - Summary:          present on CliffNotes, present-but-optional on others.
//   - Confidence:       present on CliffNotes, absent on CodeTour and WorkflowStory.
//
// All fields are sourced directly from knowledge.Section; no heuristic layering.
type fieldGuideSection struct {
	Title            string `json:"title"`
	Anchor           string `json:"anchor,omitempty"`
	EstimatedReadMin int    `json:"estimated_read_min,omitempty"`
	Content          string `json:"content,omitempty"`
	Summary          string `json:"summary,omitempty"`
	Confidence       string `json:"confidence,omitempty"`
}

// ---------------------------------------------------------------------------
// Tool definitions
// ---------------------------------------------------------------------------

// fieldGuideToolDefs returns the tool definition for get_field_guide. Called
// from baseTools() alongside the other Phase-2 tool defs.
func (h *mcpHandler) fieldGuideToolDefs() []mcpToolDefinition {
	return []mcpToolDefinition{
		{
			Name: "get_field_guide",
			Description: "Fetch a field-guide artifact for a repository scope: cliff notes, " +
				"learning path, code tour, or workflow story. " +
				"All variants are pre-generated at index time and returned synchronously from the knowledge store — no LLM call. " +
				"Supersedes onboard_new_contributor for multi-format field guides. " +
				"Use the SourceBridge web UI (Admin → Comprehension) to trigger generation when an artifact has not been created yet.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{
						"type":        "string",
						"description": "Repository ID",
					},
					"format": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"cliff_notes", "learning_path", "code_tour", "workflow_story"},
						"description": "Field-guide format (default: cliff_notes)",
					},
					"scope_type": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"repository", "module", "file", "symbol", "requirement"},
						"description": "Scope type (default: repository)",
					},
					"scope_path": map[string]interface{}{
						"type":        "string",
						"description": "Scope path (file path, module path, or symbol path like 'file.go#FuncName')",
					},
					"audience": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"beginner", "developer"},
						"description": "Target audience (default: developer)",
					},
					"depth": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"summary", "medium", "deep"},
						"description": "Level of detail (default: medium)",
					},
				},
				"required": []string{"repository_id"},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// callGetFieldGuide implements the get_field_guide MCP tool. It maps the
// format enum to a knowledge.ArtifactType and reads from the knowledge store
// using the same GetArtifactByKey path as callGetCliffNotes.
func (h *mcpHandler) callGetFieldGuide(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
		Format       string `json:"format"`
		ScopeType    string `json:"scope_type"`
		ScopePath    string `json:"scope_path"`
		Audience     string `json:"audience"`
		Depth        string `json:"depth"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	if h.knowledgeStore == nil {
		return nil, fmt.Errorf("Knowledge store is not configured. Field guides require knowledge persistence.")
	}

	// Validate format against the four-value allowlist before mapping to
	// ArtifactType. An unrecognised value (e.g. "code-tour") returns a
	// structured INVALID_ARGUMENTS error rather than silently returning the
	// wrong artifact type.
	switch params.Format {
	case "", "cliff_notes", "learning_path", "code_tour", "workflow_story":
		// ok — empty defaults to cliff_notes below
	default:
		return nil, errInvalidArguments("unknown format: " + params.Format)
	}

	// Map format → ArtifactType (empty defaults to cliff_notes, matching
	// callGetCliffNotes behaviour for the same defaulting logic).
	artifactType := fieldGuideFormatToArtifactType(params.Format)

	// Apply defaults for optional fields.
	scopeType := knowledge.ScopeType(params.ScopeType)
	if scopeType == "" {
		scopeType = knowledge.ScopeRepository
	}
	audience := knowledge.Audience(params.Audience)
	if audience == "" {
		audience = knowledge.AudienceDeveloper
	}
	depth := knowledge.Depth(params.Depth)
	if depth == "" {
		depth = knowledge.DepthMedium
	}

	key := knowledge.ArtifactKey{
		RepositoryID: params.RepositoryID,
		Type:         artifactType,
		Audience:     audience,
		Depth:        depth,
		Scope: knowledge.ArtifactScope{
			ScopeType: scopeType,
			ScopePath: params.ScopePath,
		},
	}

	// Normalise the format label for display in messages (empty → "cliff_notes").
	formatLabel := params.Format
	if formatLabel == "" {
		formatLabel = "cliff_notes"
	}

	artifact := h.knowledgeStore.GetArtifactByKey(context.Background(), key)
	if artifact == nil {
		return map[string]interface{}{
			"artifact": nil,
			"message": fmt.Sprintf(
				"No %s field guide has been generated for this scope yet. "+
					"Trigger generation via the SourceBridge web UI (Admin → Comprehension).",
				strings.ReplaceAll(formatLabel, "_", " "),
			),
		}, nil
	}

	if artifact.Status == knowledge.StatusGenerating {
		return map[string]interface{}{
			"artifact": nil,
			"message": fmt.Sprintf(
				"The %s field guide is currently being generated. Please try again in a moment.",
				strings.ReplaceAll(formatLabel, "_", " "),
			),
		}, nil
	}

	if artifact.Status != knowledge.StatusReady {
		return map[string]interface{}{
			"artifact": nil,
			"message": fmt.Sprintf(
				"The %s field guide is in '%s' state.",
				strings.ReplaceAll(formatLabel, "_", " "),
				artifact.Status,
			),
		}, nil
	}

	sections := make([]fieldGuideSection, 0, len(artifact.Sections))
	for _, s := range artifact.Sections {
		sections = append(sections, fieldGuideSection{
			Title:      s.Title,
			Content:    s.Content,
			Summary:    s.Summary,
			Confidence: string(s.Confidence),
		})
	}

	// Join section content into a single markdown body for the top-level
	// content field. Sections retain their individual shape.
	var contentParts []string
	for _, s := range sections {
		if s.Title != "" || s.Content != "" {
			part := ""
			if s.Title != "" {
				part = "## " + s.Title + "\n\n"
			}
			part += s.Content
			contentParts = append(contentParts, strings.TrimSpace(part))
		}
	}
	content := strings.Join(contentParts, "\n\n")

	scopePath := ""
	if artifact.Scope != nil {
		scopePath = artifact.Scope.ScopePath
	}

	return map[string]interface{}{
		"repository_id": artifact.RepositoryID,
		"scope_path":    scopePath,
		"format":        formatLabel,
		"content":       content,
		"sections":      sections,
		// diagram and diagram_format are omitted: the knowledge.Artifact struct
		// does not carry a per-artifact diagram payload. A follow-up CA will
		// wire through the architecture_diagram artifact as an optional companion.
		"_artifact_meta": map[string]interface{}{
			"id":           artifact.ID,
			"scope_type":   string(artifact.Scope.ScopeType),
			"audience":     string(artifact.Audience),
			"depth":        string(artifact.Depth),
			"stale":        artifact.Stale,
			"generated_at": artifact.GeneratedAt.Format(time.RFC3339),
		},
	}, nil
}

// fieldGuideFormatToArtifactType maps the format enum string to a
// knowledge.ArtifactType. Empty string defaults to ArtifactCliffNotes.
// The caller must validate the format string before calling this helper.
func fieldGuideFormatToArtifactType(format string) knowledge.ArtifactType {
	switch format {
	case "learning_path":
		return knowledge.ArtifactLearningPath
	case "code_tour":
		return knowledge.ArtifactCodeTour
	case "workflow_story":
		return knowledge.ArtifactWorkflowStory
	default:
		// empty string and "cliff_notes" both map to ArtifactCliffNotes
		return knowledge.ArtifactCliffNotes
	}
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

// fieldGuideToolsList returns []mcpTool pairing the Phase 2b get_field_guide
// definition with its handler. Used by registerFieldGuideTools.
func (h *mcpHandler) fieldGuideToolsList() []mcpTool {
	defs := h.fieldGuideToolDefs()
	defByName := make(map[string]mcpToolDefinition, len(defs))
	for _, d := range defs {
		defByName[d.Name] = d
	}
	return []mcpTool{
		{Definition: defByName["get_field_guide"], Handler: noCtxHandler((*mcpHandler).callGetFieldGuide)},
	}
}

// registerFieldGuideTools populates h.toolDispatch with the Phase 2b
// get_field_guide tool. Called after registerCoreTools and the Phase 1
// tool registrations inside newMCPHandlerWithEdition.
func registerFieldGuideTools(h *mcpHandler) {
	for _, t := range h.fieldGuideToolsList() {
		h.registerTool(t)
	}
}
