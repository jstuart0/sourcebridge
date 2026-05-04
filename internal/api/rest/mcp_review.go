// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	commonv1 "github.com/sourcebridge/sourcebridge/gen/go/common/v1"
	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
)

// Phase 3 — get_review_for_diff.
//
// Extends review_diff_against_requirements (Phase 2.1) with an optional
// AI-review pass. The structural payload (touched_files, linked_requirements,
// unlinked_public_surface, summary) is built by reusing resolveDiffTouchedSymbols
// and the same aggregation logic as callReviewDiffAgainstRequirements.
//
// The AI pass is gated behind include_ai_review (default false) and requires
// that h.worker implements workerReviewCaller. Three distinct degraded sub-cases:
//
//   1. h.worker is nil  → degraded: "AI review unavailable: worker not connected"
//   2. h.worker does not satisfy workerReviewCaller → degraded:
//      "AI review unavailable: worker interface not implemented"
//   3. r.IsAvailable() returns false → degraded: "AI review unavailable: worker not connected"
//
// All three produce degraded:true with a non-empty degraded_reason and return
// the structural payload unchanged so the response is still useful.
//
// Caps: templates ≤ 5 (bob M1), max_files ≤ 20 (bob L2). Per-file×per-template
// ReviewFile RPCs run serially under a 90-second context deadline (bob L2 /
// sarah brief). The deadline covers the entire AI pass; if it expires the
// response carries truncated:true.

// ---------------------------------------------------------------------------
// Result types
// ---------------------------------------------------------------------------

// reviewFinding is one AI-generated finding for a file+template pair.
type reviewFinding struct {
	FilePath    string `json:"file_path"`
	Template    string `json:"template"`
	Category    string `json:"category,omitempty"`
	Severity    string `json:"severity"`
	Message     string `json:"message"`
	StartLine   int32  `json:"start_line,omitempty"`
	EndLine     int32  `json:"end_line,omitempty"`
	Suggestion  string `json:"suggestion,omitempty"`
}

// reviewForDiffResult embeds diffReviewResult (per bob H5 — preserves the
// touched_files array and all existing structural fields) and adds the
// AI-review layer.
type reviewForDiffResult struct {
	diffReviewResult

	Findings       []reviewFinding `json:"findings"`
	RiskScore      float64         `json:"risk_score"`
	Degraded       bool            `json:"degraded"`
	DegradedReason string          `json:"degraded_reason,omitempty"`
	Truncated      bool            `json:"truncated"`
	TemplatesUsed  []string        `json:"templates_used"`
}

// ---------------------------------------------------------------------------
// Tool definition
// ---------------------------------------------------------------------------

// reviewToolDefs returns the tool definition for get_review_for_diff.
// Called from baseTools() alongside the other Phase-3 tool defs.
func (h *mcpHandler) reviewToolDefs() []mcpToolDefinition {
	return []mcpToolDefinition{
		{
			Name: "get_review_for_diff",
			Description: "AI-augmented diff review. Given a commit range or explicit file list, returns the full structural diff report (touched files, linked requirements, unlinked public surface) plus optional AI findings per file per template. " +
				"Use include_ai_review: false (default) for a fast structural-only report. " +
				"Use include_ai_review: true for AI findings — note this may take 30-90 seconds. " +
				"Coexists with review_diff_against_requirements (legacy); prefer this tool for new integrations.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{
						"type":        "string",
						"description": "Repository ID",
					},
					"commit_range": map[string]interface{}{
						"type":        "string",
						"description": "Git commit range (e.g. \"HEAD~3..HEAD\"). One of commit_range or files is required.",
					},
					"files": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Explicit repo-relative file paths to review. Overrides commit_range when both are provided. One of commit_range or files is required.",
					},
					"include_ai_review": map[string]interface{}{
						"type":        "boolean",
						"description": "Run an AI review pass per file per template (default false). With include_ai_review: true this tool may take 30-90 seconds. If the client has a short timeout, call with include_ai_review: false first.",
					},
					"templates": map[string]interface{}{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
						"description": "Review templates to apply (default [\"security\", \"solid\", \"maintainability\"]). " +
							"Maximum 5 templates.",
					},
					"max_files": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of files to include in the AI review pass (default 5, cap 20). The structural report always covers all touched files.",
					},
					"max_templates": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of templates to apply per file (default 3, cap 5). Applied after the templates list is validated.",
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

const (
	reviewDefaultMaxFiles    = 5
	reviewCapMaxFiles        = 20
	reviewDefaultMaxTemplates = 3
	reviewCapMaxTemplates    = 5
	reviewAIDeadline         = 90 * time.Second
)

var reviewDefaultTemplates = []string{"security", "solid", "maintainability"}

func (h *mcpHandler) callGetReviewForDiff(ctx context.Context, session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID    string   `json:"repository_id"`
		CommitRange     string   `json:"commit_range"`
		Files           []string `json:"files"`
		IncludeAIReview bool     `json:"include_ai_review"`
		Templates       []string `json:"templates"`
		MaxFiles        int      `json:"max_files"`
		MaxTemplates    int      `json:"max_templates"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	// Require at least one diff anchor.
	if params.CommitRange == "" && len(params.Files) == 0 {
		return nil, errInvalidArguments("one of commit_range or files is required")
	}

	// Resolve and apply templates with cap enforcement.
	templates := params.Templates
	if len(templates) == 0 {
		templates = reviewDefaultTemplates
	}
	if len(templates) > reviewCapMaxTemplates {
		return nil, errInvalidArguments(fmt.Sprintf(
			"templates length %d exceeds maximum of %d", len(templates), reviewCapMaxTemplates,
		))
	}

	// Resolve and apply max_files.
	maxFiles := params.MaxFiles
	if maxFiles <= 0 {
		maxFiles = reviewDefaultMaxFiles
	}
	if maxFiles > reviewCapMaxFiles {
		maxFiles = reviewCapMaxFiles
	}

	// Resolve and apply max_templates.
	maxTemplates := params.MaxTemplates
	if maxTemplates <= 0 {
		maxTemplates = reviewDefaultMaxTemplates
	}
	if maxTemplates > reviewCapMaxTemplates {
		maxTemplates = reviewCapMaxTemplates
	}
	if len(templates) > maxTemplates {
		templates = templates[:maxTemplates]
	}

	// Build structural payload (reuses resolveDiffTouchedSymbols + the same
	// aggregation logic as callReviewDiffAgainstRequirements).
	structural, err := h.buildDiffReviewStructural(params.RepositoryID, params.CommitRange, params.Files)
	if err != nil {
		return nil, err
	}

	result := reviewForDiffResult{
		diffReviewResult: *structural,
		Findings:         []reviewFinding{},
		RiskScore:        structuralRiskScore(structural),
		TemplatesUsed:    templates,
	}

	if !params.IncludeAIReview {
		return result, nil
	}

	// AI pass — nil-check worker first (per bob L3).
	if h.worker == nil {
		result.Degraded = true
		result.DegradedReason = "AI review unavailable: worker not connected"
		return result, nil
	}

	// Type-assert to workerReviewCaller.
	r, ok := h.worker.(workerReviewCaller)
	if !ok {
		result.Degraded = true
		result.DegradedReason = "AI review unavailable: worker interface not implemented"
		return result, nil
	}

	// Check IsAvailable().
	if !r.IsAvailable() {
		result.Degraded = true
		result.DegradedReason = "AI review unavailable: worker not connected"
		return result, nil
	}

	// Build per-file slice, capped at maxFiles.
	filesToReview := structural.TouchedFiles
	if len(filesToReview) > maxFiles {
		filesToReview = filesToReview[:maxFiles]
		result.Truncated = true
	}

	// Apply 90-second deadline to the entire AI pass.
	aiCtx, aiCancel := context.WithTimeout(ctx, reviewAIDeadline)
	defer aiCancel()

	for _, tf := range filesToReview {
		// Read file content — required for the worker to produce meaningful
		// findings. When h.fileReader is nil (no file reader configured) or
		// the read fails, skip this file+template pair rather than sending
		// an empty content block to the worker.
		var fileContent string
		if h.fileReader != nil {
			var readErr error
			fileContent, readErr = h.fileReader.ReadRepoFile(params.RepositoryID, tf.FilePath)
			if readErr != nil {
				// Best-effort: skip files we cannot read rather than aborting the
				// whole review. The worker would produce no useful findings on an
				// empty content block anyway.
				continue
			}
		}
		fileLang := filePathToProtoLanguage(tf.FilePath)
		for _, tmpl := range templates {
			req := &reasoningv1.ReviewFileRequest{
				RepositoryId: params.RepositoryID,
				FilePath:     tf.FilePath,
				Language:     fileLang,
				Content:      fileContent,
				Template:     tmpl,
			}
			resp, reviewErr := r.ReviewFile(aiCtx, req)
			if reviewErr != nil {
				// Context deadline: mark truncated and stop.
				if aiCtx.Err() != nil {
					result.Truncated = true
					return result, nil
				}
				// Other errors: skip this file+template pair, continue.
				continue
			}
			for _, f := range resp.GetFindings() {
				result.Findings = append(result.Findings, reviewFinding{
					FilePath:   tf.FilePath,
					Template:   tmpl,
					Category:   f.GetCategory(),
					Severity:   f.GetSeverity(),
					Message:    f.GetMessage(),
					StartLine:  f.GetStartLine(),
					EndLine:    f.GetEndLine(),
					Suggestion: f.GetSuggestion(),
				})
			}
		}
	}

	return result, nil
}

// filePathToProtoLanguage maps a file path's extension to the proto Language
// enum expected by ReviewFileRequest. Mirrors the GraphQL path's deriveLanguage
// helper (internal/api/graphql/helpers.go) but operates within the rest package
// to avoid a cross-package import of the graphql resolver.
func filePathToProtoLanguage(filePath string) commonv1.Language {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".go":
		return commonv1.Language_LANGUAGE_GO
	case ".py":
		return commonv1.Language_LANGUAGE_PYTHON
	case ".ts", ".tsx":
		return commonv1.Language_LANGUAGE_TYPESCRIPT
	case ".js", ".jsx":
		return commonv1.Language_LANGUAGE_JAVASCRIPT
	case ".java":
		return commonv1.Language_LANGUAGE_JAVA
	case ".rs":
		return commonv1.Language_LANGUAGE_RUST
	case ".cs":
		return commonv1.Language_LANGUAGE_CSHARP
	case ".cpp", ".cc", ".cxx", ".h", ".hpp":
		return commonv1.Language_LANGUAGE_CPP
	case ".rb":
		return commonv1.Language_LANGUAGE_RUBY
	case ".php":
		return commonv1.Language_LANGUAGE_PHP
	default:
		return commonv1.Language_LANGUAGE_UNSPECIFIED
	}
}

// buildDiffReviewStructural constructs the diffReviewResult for a given repo
// and diff anchor. It is the structural half of callReviewDiffAgainstRequirements,
// extracted so get_review_for_diff can reuse it without calling the legacy tool.
func (h *mcpHandler) buildDiffReviewStructural(repoID, commitRange string, files []string) (*diffReviewResult, error) {
	touchedFileEntries, touchedSymbolIDs, err := h.resolveDiffTouchedSymbols(repoID, commitRange, files)
	if err != nil {
		return nil, err
	}

	result := &diffReviewResult{
		RepositoryID: repoID,
		CommitRange:  commitRange,
		TouchedFiles: touchedFileEntries,
	}

	// Linked requirements.
	linkedReqIDs := map[string]bool{}
	symToReqs := map[string][]string{}
	for _, symID := range touchedSymbolIDs {
		for _, link := range h.store.GetLinksForSymbol(symID, false) {
			if link.RequirementID != "" && !linkedReqIDs[link.RequirementID] {
				linkedReqIDs[link.RequirementID] = true
			}
			symToReqs[symID] = append(symToReqs[symID], link.RequirementID)
		}
	}
	if len(linkedReqIDs) > 0 {
		ids := make([]string, 0, len(linkedReqIDs))
		for id := range linkedReqIDs {
			ids = append(ids, id)
		}
		reqs := h.store.GetRequirementsByIDs(ids)
		for _, req := range reqs {
			if req == nil {
				continue
			}
			result.LinkedRequirements = append(result.LinkedRequirements, map[string]interface{}{
				"id":          req.ID,
				"external_id": req.ExternalID,
				"title":       req.Title,
				"priority":    req.Priority,
			})
		}
	}

	// Unlinked public surface.
	for _, symID := range touchedSymbolIDs {
		sym := h.store.GetSymbol(symID)
		if sym == nil || len(sym.Name) == 0 {
			continue
		}
		if !isLikelyPublicSymbol(sym.Name, sym.Language) {
			continue
		}
		if len(symToReqs[symID]) > 0 {
			continue
		}
		result.UnlinkedPublicSurface = append(result.UnlinkedPublicSurface, map[string]interface{}{
			"symbol_id":   sym.ID,
			"symbol_name": sym.Name,
			"file_path":   sym.FilePath,
			"kind":        sym.Kind,
		})
	}

	return result, nil
}

// structuralRiskScore computes a 0–1 heuristic risk score from the structural
// payload. The score penalizes having many unlinked public symbols relative to
// the total touched surface, which is a proxy for "how much changed code lacks
// requirements traceability."
//
//   score = unlinked_public / max(total_symbols, 1)
//
// Clamped to [0, 1]. Returns 0.0 when the structural payload is nil.
func structuralRiskScore(r *diffReviewResult) float64 {
	if r == nil {
		return 0.0
	}
	totalSymbols := 0
	for _, tf := range r.TouchedFiles {
		totalSymbols += len(tf.Symbols)
	}
	if totalSymbols == 0 {
		return 0.0
	}
	unlinked := len(r.UnlinkedPublicSurface)
	score := float64(unlinked) / float64(totalSymbols)
	if score > 1.0 {
		score = 1.0
	}
	return score
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

// reviewToolsList returns []mcpTool pairing the Phase 3 get_review_for_diff
// definition with its ctx-bearing handler. Used by registerReviewTools.
func (h *mcpHandler) reviewToolsList() []mcpTool {
	defs := h.reviewToolDefs()
	defByName := make(map[string]mcpToolDefinition, len(defs))
	for _, d := range defs {
		defByName[d.Name] = d
	}
	return []mcpTool{
		// get_review_for_diff uses withCtxHandler (MCP-2) so the live request
		// context threads through for the 90-second AI-pass deadline.
		{Definition: defByName["get_review_for_diff"], Handler: withCtxHandler((*mcpHandler).callGetReviewForDiff)},
	}
}

// registerReviewTools registers the Phase 3 get_review_for_diff tool into
// the handler's dispatch map. Called from newMCPHandlerWithEdition after
// registerChangeImpactTools. get_review_for_diff is registered directly
// (not via noCtxHandler) because its handler needs the live request context
// for the 90-second AI-pass deadline.
func registerReviewTools(h *mcpHandler) {
	for _, t := range h.reviewToolsList() {
		h.registerTool(t)
	}
}
