// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package capabilities

// Registry is the single source of truth for capabilities offered
// across editions. Every surface (GraphQL, REST, MCP) reads from this
// list rather than branching on edition strings directly.
//
// To add a capability:
//
//   1. Append a Capability to Registry below.
//   2. Declare which editions offer it via the Editions field.
//   3. List any MCP tools it gates in MCPToolNames (MCP tools/list
//      filter will hide them on non-matching editions).
//   4. List any GraphQL fields in GraphQLFields (resolver can check
//      availability to return typed nulls instead of panicking).
//   5. List any REST path prefixes in RESTRoutes (router can 404
//      cleanly instead of returning 500s).
//
// registry_test.go asserts that every declared MCP tool actually
// exists in the real mcpHandler.baseTools() surface. That's the
// drift-detection the plan calls out.
var Registry = []Capability{
	// ---- Core indexer + retrieval — always on ----
	{
		Name:         "repository_indexing",
		Description:  "Index local and remote git repositories into the understanding graph.",
		Editions:     []Edition{EditionOSS, EditionEnterprise},
		LatencyClass: "indexing_op",
	},
	{
		Name:         "hybrid_search",
		Description:  "Hybrid search combining FTS, vector, and structural signals with RRF fusion.",
		Editions:     []Edition{EditionOSS, EditionEnterprise},
		MCPToolNames: []string{"search_symbols"},
		LatencyClass: "search",
	},

	// ---- Comprehension artifacts ----
	{
		Name:          "cliff_notes",
		Description:   "Multi-scope AI-generated cliff notes with audience + depth controls.",
		Editions:      []Edition{EditionOSS, EditionEnterprise},
		MCPToolNames:  []string{"get_cliff_notes"},
		RequiresModel: true,
		LatencyClass:  "llm",
	},
	{
		Name:          "architecture_diagram",
		Description:   "Mermaid and structured-JSON architecture diagrams generated from the graph.",
		Editions:      []Edition{EditionOSS, EditionEnterprise},
		MCPToolNames:  []string{"get_architecture_diagram"},
		RequiresModel: true,
		LatencyClass:  "fast_read",
	},
	{
		Name:          "explain_code",
		Description:   "AI-generated explanation of code at file or snippet level, streaming.",
		Editions:      []Edition{EditionOSS, EditionEnterprise},
		MCPToolNames:  []string{"explain_code"},
		RequiresModel: true,
		LatencyClass:  "llm",
	},
	{
		Name:          "agentic_retrieval",
		Description:   "Server-side deep-QA orchestrator with agentic retrieval loop and grounded citations.",
		Editions:      []Edition{EditionOSS, EditionEnterprise},
		MCPToolNames:  []string{"ask_question"},
		RequiresModel: true,
		LatencyClass:  "llm",
	},

	// ---- Call graph / imports / entry points (Phase 1a + 1b) ----
	{
		Name:         "call_graph",
		Description:  "Read the stored call graph — callers and callees of a symbol, N hops.",
		Editions:     []Edition{EditionOSS, EditionEnterprise},
		MCPToolNames: []string{"get_callers", "get_callees"},
		LatencyClass: "fast_read",
	},
	// Decision D3 (CA-151): one capability gates both new tools rather than
	// splitting them. get_symbol_context's primary value is the source payload;
	// callers/callees/imports are an enrichment layer that degrades gracefully
	// via the `degraded` flag when call_graph or file_imports is disabled.
	// Splitting into multiple capabilities would expose two tools that do
	// identical work in degraded mode — wasteful for capability-constrained
	// clients. Matches the existing convention: call_graph gates two tools,
	// indexing_lifecycle gates three.
	{
		Name:         "symbol_source",
		Description:  "Read a symbol's source bytes and (optionally) a bundled call-graph + imports context.",
		Editions:     []Edition{EditionOSS, EditionEnterprise},
		MCPToolNames: []string{"get_symbol_source", "get_symbol_context"},
		LatencyClass: "fast_read",
	},
	{
		Name:         "file_imports",
		Description:  "Read direct and transitive import relationships for a file.",
		Editions:     []Edition{EditionOSS, EditionEnterprise},
		MCPToolNames: []string{"get_file_imports"},
		LatencyClass: "fast_read",
	},
	{
		Name:         "git_history",
		Description:  "Read recent commits affecting a path (file-level today; symbol-level in Phase 1b).",
		Editions:     []Edition{EditionOSS, EditionEnterprise},
		MCPToolNames: []string{"get_recent_changes"},
		LatencyClass: "fast_read",
	},
	{
		Name:         "test_linkage",
		Description:  "Resolve tests that exercise a symbol via persisted edges, adjacent-test heuristics, and text-reference fallback.",
		Editions:     []Edition{EditionOSS, EditionEnterprise},
		MCPToolNames: []string{"get_tests_for_symbol"},
		LatencyClass: "fast_read",
	},
	{
		Name:         "entry_points",
		Description:  "Classify symbols as entry points (main funcs, HTTP routes, Grails controller actions, …) with basic or framework-aware precision.",
		Editions:     []Edition{EditionOSS, EditionEnterprise},
		MCPToolNames: []string{"get_entry_points"},
		LatencyClass: "search",
	},
	{
		Name:         "indexing_lifecycle",
		Description:  "Agent-driveable indexing lifecycle: register a repo, poll its status, and trigger re-indexing.",
		Editions:     []Edition{EditionOSS, EditionEnterprise},
		MCPToolNames: []string{"index_repository", "get_index_status", "refresh_repository"},
		LatencyClass: "indexing_op",
	},
	{
		Name:         "compound_workflows",
		Description:  "Server-orchestrated workflow tools: review a diff against linked requirements, summarize impact, onboard a new contributor.",
		Editions:     []Edition{EditionOSS, EditionEnterprise},
		MCPToolNames: []string{"review_diff_against_requirements", "impact_summary", "onboard_new_contributor"},
		LatencyClass: "search",
	},

	// ---- Requirements + impact ----
	{
		Name:         "requirements",
		Description:  "List and link requirements to code symbols (traceability).",
		Editions:     []Edition{EditionOSS, EditionEnterprise},
		MCPToolNames: []string{"get_requirements"},
		LatencyClass: "fast_read",
	},
	{
		Name:         "change_impact",
		Description:  "Change-impact reports over recent commits.",
		Editions:     []Edition{EditionOSS, EditionEnterprise},
		MCPToolNames: []string{"get_impact_report"},
		LatencyClass: "fast_read",
	},

	// ---- Requirement linking (Phase 1a, CA-153) ----
	// get_changed_requirements (Phase 2d) will be appended to MCPToolNames
	// when that phase ships.
	{
		Name:         "requirement_linking",
		Description:  "Bidirectional traceability between code symbols and requirements: forward (code→spec) and inverse (spec→code).",
		Editions:     []Edition{EditionOSS, EditionEnterprise},
		MCPToolNames: []string{"get_requirements_for_symbol", "get_symbols_for_requirement"},
		LatencyClass: "fast_read",
	},

	// ---- Subsystem clustering ----
	{
		Name:         "subsystem_clustering",
		Description:  "Label-propagation subsystem clustering on the call graph. Exposes three MCP tools for navigating code subsystems and a web UI Subsystems tab.",
		Editions:     []Edition{EditionOSS, EditionEnterprise},
		MCPToolNames: []string{"get_subsystems", "get_subsystem_by_id", "get_subsystem"},
		LatencyClass: "fast_read",
	},

	// ---- Agent setup (Claude Code integration) ----
	{
		Name:        "agent_setup",
		Description: "Generate a .claude/CLAUDE.md skill card and .mcp.json configuration for Claude Code integration.",
		Editions:    []Edition{EditionOSS, EditionEnterprise},
	},

	// ---- Enterprise-only capabilities (no MCP tools yet — Phase 3 adds them) ----
	{
		Name:        "enterprise_reports",
		Description: "Long-form enterprise reports: architecture baseline, SWOT, due diligence, portfolio, compliance.",
		Editions:    []Edition{EditionEnterprise},
		RESTRoutes:  []string{"/api/v1/enterprise/reports", "/api/v1/enterprise/compliance"},
	},
	{
		Name:        "sso_identity",
		Description: "SSO via OIDC or SAML.",
		Editions:    []Edition{EditionEnterprise},
		RESTRoutes:  []string{"/api/v1/enterprise/sso"},
	},
	{
		Name:        "audit_log",
		Description: "Persistent audit log for user and agent actions.",
		Editions:    []Edition{EditionEnterprise},
		RESTRoutes:  []string{"/api/v1/enterprise/audit"},
	},
	{
		Name:        "notifications",
		Description: "SMTP-backed notification settings + impact notifications.",
		Editions:    []Edition{EditionEnterprise},
		RESTRoutes:  []string{"/api/v1/enterprise/notifications"},
	},
	{
		Name:        "team_management",
		Description: "Invite + manage team members with roles.",
		Editions:    []Edition{EditionEnterprise},
		RESTRoutes:  []string{"/api/v1/enterprise/team"},
	},
	{
		Name:        "org_settings",
		Description: "Organization-level settings: retention, default role, AI code policy.",
		Editions:    []Edition{EditionEnterprise},
		RESTRoutes:  []string{"/api/v1/enterprise/settings"},
	},
	{
		Name:         "cross_repo_impact",
		Description:  "Cross-repository impact analysis over configured repo-dependency edges.",
		Editions:     []Edition{EditionEnterprise},
		MCPToolNames: []string{"get_cross_repo_impact"},
		RESTRoutes:   []string{"/api/v1/enterprise/dependencies"},
		LatencyClass: "search",
	},
	{
		Name:         "per_op_models",
		Description:  "Per-operation model overrides (review_model, ask_model, knowledge_model, report_model, …).",
		Editions:     []Edition{EditionEnterprise},
		GraphQLFields: []string{"llmReportModel"},
	},
}
