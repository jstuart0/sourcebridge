// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"fmt"
)

// Phase 2.2 — prompts surface.
//
// MCP's prompts/list + prompts/get expose reusable templates that a
// capability-aware client can render to the user as multi-step
// workflows. Not every MCP client surfaces prompts well, which is
// why the primary workflow ergonomics surface is compound tools (see
// Phase 2.1). Prompts are a secondary convenience for clients with
// good prompt UX.
//
// Each prompt below is a curated template for a *question* the user
// is likely to ask — the prompt fills in the SourceBridge-specific
// wording that gets the best grounded answer from ask_question. The
// user provides the shape-shifting input (a file path, a requirement
// id, a test file), and the server fills the rest.

// ---------------------------------------------------------------------------
// Wire types
// ---------------------------------------------------------------------------

type mcpPromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type mcpPromptDefinition struct {
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Arguments   []mcpPromptArgument `json:"arguments,omitempty"`
}

// mcpPromptMessage is one message in a prompts/get response. Shape
// matches what MCP clients feed into their LLM invocation: role +
// content[].{type,text}.
type mcpPromptMessage struct {
	Role    string       `json:"role"` // "user" or "assistant"
	Content mcpContent   `json:"content"`
}

type mcpPromptGetResult struct {
	Description string             `json:"description,omitempty"`
	Messages    []mcpPromptMessage `json:"messages"`
}

// ---------------------------------------------------------------------------
// Prompt registry
// ---------------------------------------------------------------------------

type promptRenderer func(args map[string]string) (*mcpPromptGetResult, error)

type promptEntry struct {
	def      mcpPromptDefinition
	renderer promptRenderer
}

func (h *mcpHandler) prompts() []promptEntry {
	return []promptEntry{
		{
			def: mcpPromptDefinition{
				Name:        "review_security_of_file",
				Description: "Ask ask_question for a security-focused review of a specific file, with attention to authentication, authorization, input validation, and secret handling.",
				Arguments: []mcpPromptArgument{
					{Name: "repository_id", Description: "Repository ID", Required: true},
					{Name: "file_path", Description: "Repo-relative file path to review", Required: true},
				},
			},
			renderer: func(args map[string]string) (*mcpPromptGetResult, error) {
				if args["repository_id"] == "" || args["file_path"] == "" {
					return nil, errInvalidArguments("review_security_of_file requires repository_id and file_path")
				}
				text := fmt.Sprintf(
					"Review %s for security issues. Focus on:\n"+
						"- Authentication and authorization logic\n"+
						"- Input validation on every external boundary\n"+
						"- SQL/command/template injection surfaces\n"+
						"- Secret handling (no hardcoded credentials; safe env-var + key-file paths)\n"+
						"- Error handling that doesn't leak internals\n\n"+
						"Please cite specific lines via the repo's understanding graph. If there are no security-relevant concerns, say so directly. Use repository_id=%s.",
					args["file_path"], args["repository_id"],
				)
				return &mcpPromptGetResult{
					Description: "Security review of " + args["file_path"],
					Messages: []mcpPromptMessage{{
						Role:    "user",
						Content: mcpContent{Type: "text", Text: text},
					}},
				}, nil
			},
		},
		{
			def: mcpPromptDefinition{
				Name:        "explain_requirement_implementation",
				Description: "Ask ask_question how a specific requirement is implemented in the codebase, using the requirement's linked symbols as the grounding anchor.",
				Arguments: []mcpPromptArgument{
					{Name: "repository_id", Description: "Repository ID", Required: true},
					{Name: "requirement_id", Description: "Requirement ID (the server-generated UUID or the external ID)", Required: true},
				},
			},
			renderer: func(args map[string]string) (*mcpPromptGetResult, error) {
				if args["repository_id"] == "" || args["requirement_id"] == "" {
					return nil, errInvalidArguments("explain_requirement_implementation requires repository_id and requirement_id")
				}
				text := fmt.Sprintf(
					"Explain how requirement %s is implemented in repository_id=%s.\n\n"+
						"Please:\n"+
						"- Start by calling get_requirements with include_links=true to find the requirement and its linked code symbols.\n"+
						"- Summarize the implementation flow across the linked symbols — entry points first, then the key functions that carry the business logic, then any downstream effects.\n"+
						"- Call out any linked symbols whose code doesn't actually implement the requirement (drift between the spec and the code).\n"+
						"- If the requirement has no linked symbols, say so plainly rather than guessing.",
					args["requirement_id"], args["repository_id"],
				)
				return &mcpPromptGetResult{
					Description: "Implementation walkthrough for " + args["requirement_id"],
					Messages: []mcpPromptMessage{{
						Role:    "user",
						Content: mcpContent{Type: "text", Text: text},
					}},
				}, nil
			},
		},
		{
			def: mcpPromptDefinition{
				Name:        "review_diff_with_sourcebridge",
				Description: "Run a full AI-augmented diff review via get_review_for_diff, then follow up on each affected symbol using get_requirements_for_symbol.",
				Arguments: []mcpPromptArgument{
					{Name: "repository_id", Description: "Repository ID", Required: true},
					{Name: "commit_range", Description: "Git commit range (e.g. \"HEAD~3..HEAD\"). One of commit_range or files is required.", Required: false},
					{Name: "files", Description: "Comma-separated list of repo-relative file paths to review. One of commit_range or files is required.", Required: false},
				},
			},
			renderer: func(args map[string]string) (*mcpPromptGetResult, error) {
				if args["repository_id"] == "" {
					return nil, errInvalidArguments("review_diff_with_sourcebridge requires repository_id")
				}
				if args["commit_range"] == "" && args["files"] == "" {
					return nil, errInvalidArguments("review_diff_with_sourcebridge requires one of commit_range or files")
				}
				rangeOrFiles := ""
				if args["commit_range"] != "" {
					rangeOrFiles = fmt.Sprintf("commit_range: \"%s\"", args["commit_range"])
				} else {
					rangeOrFiles = fmt.Sprintf("files: [%q]", args["files"])
				}
				text := fmt.Sprintf(
					"Run a full AI-augmented diff review for repository_id=%s.\n\n"+
						"Step 1: Call get_review_for_diff with:\n"+
						"  repository_id: %q\n"+
						"  %s\n"+
						"  include_ai_review: true\n\n"+
						"Note: with include_ai_review: true, this tool may take 30-90 seconds. "+
						"If the client has a short timeout, consider calling with include_ai_review: false first "+
						"to get the structural report immediately, then re-call with include_ai_review: true for AI findings.\n\n"+
						"Step 2: For each finding in the response:\n"+
						"  - Note the file_path and severity.\n"+
						"  - For HIGH and MEDIUM severity findings, call get_requirements_for_symbol for the affected symbol "+
						"to surface which requirements may be impacted.\n\n"+
						"Step 3: Review the unlinked_public_surface list. For any public symbol with no linked requirement, "+
						"consider whether the change creates new user-facing behavior that should be traced to a requirement.\n\n"+
						"Step 4: Summarize:\n"+
						"  - Total findings by severity (HIGH / MEDIUM / LOW / INFO).\n"+
						"  - Which requirements are at risk (from linked_requirements and the per-symbol follow-up).\n"+
						"  - Recommended actions before merging.",
					args["repository_id"], args["repository_id"], rangeOrFiles,
				)
				return &mcpPromptGetResult{
					Description: fmt.Sprintf("AI-augmented diff review for repository %s", args["repository_id"]),
					Messages: []mcpPromptMessage{{
						Role:    "user",
						Content: mcpContent{Type: "text", Text: text},
					}},
				}, nil
			},
		},
		{
			def: mcpPromptDefinition{
				Name:        "why_is_this_test_failing",
				Description: "Ask ask_question to diagnose a failing test file, using call graph + test linkage to identify what it actually exercises.",
				Arguments: []mcpPromptArgument{
					{Name: "repository_id", Description: "Repository ID", Required: true},
					{Name: "test_file", Description: "Repo-relative path to the failing test file", Required: true},
					{Name: "failure_output", Description: "Optional test runner output (stack trace, assertion message, etc.)", Required: false},
				},
			},
			renderer: func(args map[string]string) (*mcpPromptGetResult, error) {
				if args["repository_id"] == "" || args["test_file"] == "" {
					return nil, errInvalidArguments("why_is_this_test_failing requires repository_id and test_file")
				}
				failure := args["failure_output"]
				failureBlock := ""
				if failure != "" {
					failureBlock = fmt.Sprintf("\n\nTest runner output:\n```\n%s\n```", failure)
				}
				text := fmt.Sprintf(
					"The test file %s is failing in repository_id=%s. Diagnose the root cause.%s\n\n"+
						"Walk through this:\n"+
						"1. Identify what the test actually exercises — look at imported symbols and test-body references.\n"+
						"2. For each non-test symbol the test references, surface its most recent changes (agents can call get_recent_changes with a symbol-level filter).\n"+
						"3. Name the most likely cause. Be specific about file + line when you can.\n"+
						"4. If the cause isn't clear from the code, say so and list the next diagnostic steps.",
					args["test_file"], args["repository_id"], failureBlock,
				)
				return &mcpPromptGetResult{
					Description: "Diagnose failing test: " + args["test_file"],
					Messages: []mcpPromptMessage{{
						Role:    "user",
						Content: mcpContent{Type: "text", Text: text},
					}},
				}, nil
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func (h *mcpHandler) handlePromptsList(_ *mcpSession, msg jsonRPCRequest) jsonRPCResponse {
	prompts := h.prompts()
	defs := make([]mcpPromptDefinition, 0, len(prompts))
	for _, p := range prompts {
		defs = append(defs, p.def)
	}
	return successResponse(msg.ID, map[string]interface{}{"prompts": defs})
}

func (h *mcpHandler) handlePromptsGet(_ *mcpSession, msg jsonRPCRequest) jsonRPCResponse {
	var params struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return errorResponse(msg.ID, -32602, "Invalid params: "+err.Error())
	}

	if params.Arguments == nil {
		params.Arguments = map[string]string{}
	}

	for _, p := range h.prompts() {
		if p.def.Name != params.Name {
			continue
		}
		result, err := p.renderer(params.Arguments)
		if err != nil {
			return errorResponse(msg.ID, -32602, err.Error())
		}
		return successResponse(msg.ID, result)
	}

	return errorResponse(msg.ID, -32602, fmt.Sprintf("Unknown prompt: %s", params.Name))
}
