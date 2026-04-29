// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package resolution provides the runtime LLM-config resolver that all worker
// LLM calls flow through. The resolver is the single source of truth for
// which provider, API key, model, base URL, and timeout the worker uses for
// any given operation, replacing the legacy boot-time merge that allowed
// k8s configmap env vars to silently override DB-saved admin settings.
//
// Resolution order (per Resolve call):
//
//  1. Per-repo override (when the repo has an LLMOverride row; applies
//     to every repo-scoped LLM op as of R2 — see slice 1 of plan
//     2026-04-29-workspace-llm-source-of-truth-r2.md)
//  2. Workspace settings (ca_llm_config), version-keyed cache so a save
//     on replica A is visible to replica B on the very next Resolve
//  3. Env-var bootstrap (cfg.LLM, populated at boot from SOURCEBRIDGE_LLM_*)
//  4. Built-in defaults
//
// Every Resolve produces a Snapshot stamped with per-field Sources so the
// per-call structured log line can show exactly which layer supplied each
// value. This is how operators verify the fix landed in production.
package resolution

// Op enumerates the LLM-bearing operations the resolver knows about. The
// resolver asserts the op string is in KnownOps so a typo in any caller
// surfaces at test time rather than silently picking up the wrong defaults.
//
// When you add a new LLM-bearing RPC call site:
//  1. Add a new constant here.
//  2. Add it to KnownOps below.
//  3. Update the AST lint test's protectedWorkerMethods list if the new op
//     introduces a new wrapper method on llmcall.Caller.
const (
	OpDiscussion           = "discussion"
	OpReview               = "review"
	OpAnalysis             = "analysis"
	OpKnowledge            = "knowledge"
	OpArchitectureDiagram  = "architecture_diagram"
	OpLivingWikiColdStart  = "living_wiki.coldstart"
	OpLivingWikiRegen      = "living_wiki.regen"
	OpLivingWikiAssembly   = "living_wiki.assembly"
	OpQAClassify           = "qa.classify"
	OpQADecompose          = "qa.decompose"
	OpQASynth              = "qa.synth"
	OpQADeepSynth          = "qa.deep_synth"
	OpQAAgentTurn          = "qa.agent_turn"
	OpMCPExplain           = "mcp.explain"
	OpMCPDiscussStream     = "mcp.discuss_stream"
	OpDiscussStream        = "discuss.stream"
	OpClusteringRelabel    = "clustering.relabel"
	OpReportGenerate       = "report.generate"
	OpModelsList           = "models.list"
	OpProviderCapabilities = "provider.capabilities"
	OpRequirementsEnrich   = "requirements.enrich"
	OpRequirementsExtract  = "requirements.extract"
)

// KnownOps is the set of accepted op strings. Resolve returns an error for
// any op not in this set. Centralized so a new caller can't silently use a
// typo'd op string and quietly bypass the known-op assertion.
var KnownOps = map[string]struct{}{
	OpDiscussion:           {},
	OpReview:               {},
	OpAnalysis:             {},
	OpKnowledge:            {},
	OpArchitectureDiagram:  {},
	OpLivingWikiColdStart:  {},
	OpLivingWikiRegen:      {},
	OpLivingWikiAssembly:   {},
	OpQAClassify:           {},
	OpQADecompose:          {},
	OpQASynth:              {},
	OpQADeepSynth:          {},
	OpQAAgentTurn:          {},
	OpMCPExplain:           {},
	OpMCPDiscussStream:     {},
	OpDiscussStream:        {},
	OpClusteringRelabel:    {},
	OpReportGenerate:       {},
	OpModelsList:           {},
	OpProviderCapabilities: {},
	OpRequirementsEnrich:   {},
	OpRequirementsExtract:  {},
}

// IsLivingWikiOp reports whether op is in the living_wiki.* family.
//
// Historical note: the parent delivery used this as a gate inside the
// resolver's per-repo override pass — only living-wiki ops applied the
// override. R2 widens the override to apply to every repo-scoped op
// (mirroring the workspace area list), so the resolver no longer
// consults this predicate. Kept exported for any non-resolver caller
// that wants to ask "is this a living-wiki op" for unrelated reasons
// (telemetry, dispatch routing, etc.).
func IsLivingWikiOp(op string) bool {
	switch op {
	case OpLivingWikiColdStart, OpLivingWikiRegen, OpLivingWikiAssembly:
		return true
	default:
		return false
	}
}

// Source is the per-field origin label stamped onto a Snapshot.
type Source string

const (
	SourceRepoOverride Source = "repo_override"
	SourceWorkspace    Source = "workspace"
	SourceEnvFallback  Source = "env_fallback"
	SourceBuiltin      Source = "builtin"
)

// Field names used in Snapshot.Sources.
const (
	FieldProvider    = "provider"
	FieldBaseURL     = "base_url"
	FieldAPIKey      = "api_key"
	FieldModel       = "model"
	FieldDraftModel  = "draft_model"
	FieldTimeoutSecs = "timeout_secs"
)
