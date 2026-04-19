# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Artifact renderers.

A renderer takes a :class:`SummaryTree` and produces a final artifact
(cliff notes, learning path, code tour, workflow story). Renderers are
strategy-agnostic — they consume the tree shape, not knowledge of how
it was built — so the same CliffNotesRenderer works against a
HierarchicalStrategy output today and a RAPTOR/GraphRAG output later.

Each renderer does one final LLM call: it feeds the model the root
summary plus a small number of the most substantial child summaries
and asks for structured JSON sections that match the existing cliff
notes UI contract. This keeps the final prompt small enough for any
model even when the original repo was huge.
"""

from __future__ import annotations

import asyncio
import json
import re
from dataclasses import dataclass
from time import monotonic

import structlog

from workers.common.llm.provider import (
    LLMProvider,
    LLMResponse,
    check_prompt_budget,
    complete_with_optional_model,
    require_nonempty,
)
from workers.comprehension.tree import SummaryNode, SummaryTree
from workers.knowledge.evidence import (
    evaluate_evidence_gate,
    extract_section_evidence_refs,
    find_section_weakness_phrases,
    relevance_penalty,
    strip_forbidden_phrase_sentences,
    strip_speculative_sentences,
    strip_unsupported_claim_sentences,
)
from workers.knowledge.parse_utils import coerce_section, parse_evidence, parse_json_sections
from workers.knowledge.prompts.cliff_notes import (
    CLIFF_NOTES_SYSTEM,
    REQUIRED_SECTIONS,
    REQUIRED_SECTIONS_DEEP_REPOSITORY,
    REQUIRED_SECTIONS_BY_SCOPE,
)
from workers.knowledge.thresholds import DEEP_MIN_EVIDENCE, TITLE_SUMMARY_MAX_CHARS
from workers.knowledge.types import CliffNotesResult, CliffNotesSection
from workers.reasoning.types import LLMUsageRecord

log = structlog.get_logger()

SECTION_KEYWORDS: dict[str, tuple[str, ...]] = {
    "System Purpose": ("entry", "serve", "web", "page", "ui", "browser", "api", "worker", "product", "graphql", "knowledge", "artifact"),
    "Architecture Overview": ("api", "graphql", "worker", "web", "page", "ui", "service", "orchestr", "artifact", "knowledge"),
    "External Dependencies": ("openai", "anthropic", "openrouter", "surreal", "docker", "cloudflare", "ollama", "kubectl"),
    "Domain Model": ("repository", "artifact", "understanding", "job", "section", "knowledge", "graph", "requirement"),
    "Core System Flows": ("generate", "build", "render", "index", "queue", "orchestr", "mutation", "servicer"),
    "Code Structure": ("internal", "workers", "web", "cli", "deploy", "component", "resolver", "package"),
    "Security Model": ("auth", "token", "login", "permission", "secret", "credential", "jwt", "session"),
    "Error Handling Patterns": ("error", "retry", "fallback", "fail", "status", "exception", "degraded"),
    "Data Flow & Request Lifecycle": ("request", "response", "graphql", "grpc", "persist", "store", "artifact", "mutation"),
    "Concurrency & State Management": ("worker", "queue", "background", "async", "state", "resume", "cache", "lock"),
    "Configuration & Feature Flags": ("config", "env", "flag", "setting", "model", "provider", "override"),
    "Testing Strategy": ("test", "pytest", "_test.go", "benchmark", "mock", "fixture"),
    "Key Abstractions": ("store", "servicer", "renderer", "strategy", "resolver", "artifact", "provider"),
    "Module Deep Dives": ("internal/", "workers/", "web/", "cli/", "knowledge", "graphql", "architecture"),
    "Complexity & Risk Areas": ("migration", "fallback", "retry", "stale", "cache", "quality", "benchmark", "deep"),
    "Suggested Starting Points": ("main", "entry", "resolver", "servicer", "page", "index.go", "readme"),
}

DEEP_SECTION_GROUPS: tuple[tuple[str, ...], ...] = (
    ("System Purpose", "Architecture Overview", "Core System Flows", "Suggested Starting Points"),
    ("Domain Model", "Key Abstractions", "Code Structure", "Module Deep Dives"),
    ("External Dependencies", "Security Model", "Configuration & Feature Flags", "Error Handling Patterns"),
    ("Data Flow & Request Lifecycle", "Concurrency & State Management", "Testing Strategy", "Complexity & Risk Areas"),
)

DEEP_REPAIR_SECTION_TITLES: tuple[str, ...] = (
    "Domain Model",
    "Testing Strategy",
    "Key Abstractions",
    "Complexity & Risk Areas",
)

SECTION_MULTI_HINT_SEED_LIMITS: dict[str, int] = {
    "Domain Model": 3,
    "Key Abstractions": 3,
    "External Dependencies": 2,
}

GROUP_INSTRUCTIONS: dict[tuple[str, ...], str] = {
    DEEP_SECTION_GROUPS[0]: (
        "IMPORTANT: Treat this as the system-shape slice. Do not over-center any single interface such as the CLI "
        "unless the selected evidence is overwhelmingly CLI-only. If the evidence spans API, workers, and web code, "
        "describe the repository as a multi-surface code intelligence system and explain how those surfaces connect."
    ),
    DEEP_SECTION_GROUPS[1]: (
        "IMPORTANT: Treat this as the code-and-model slice. Focus on repository entities, abstractions, and module "
        "responsibilities. Prefer stable internal concepts over temporary tooling or test scaffolding."
    ),
    DEEP_SECTION_GROUPS[2]: (
        "IMPORTANT: Treat this as the operational safeguards slice. Be concrete about auth, configuration, external "
        "systems, retries, and failure modes. Do not invent infrastructure beyond the selected evidence."
    ),
    DEEP_SECTION_GROUPS[3]: (
        "IMPORTANT: Treat this as the runtime-behavior slice. Explain request lifecycles, background execution, "
        "state, concurrency, and testing boundaries using the selected evidence only."
    ),
}

SYSTEM_GROUP_PREFERRED_AREAS: tuple[str, ...] = ("internal_api", "workers", "web", "cli")

SECTION_AREA_PRIORITIES: dict[str, tuple[str, ...]] = {
    "System Purpose": ("internal_api", "web", "workers", "cli"),
    "Architecture Overview": ("internal_api", "workers", "web", "cli"),
    "Core System Flows": ("workers", "internal_api", "web", "cli"),
    "Domain Model": ("internal", "workers", "internal_api", "web"),
    "Code Structure": ("internal", "workers", "web", "cli"),
    "Security Model": ("internal_api", "internal", "workers"),
    "Error Handling Patterns": ("internal_api", "workers", "internal"),
    "Data Flow & Request Lifecycle": ("internal_api", "workers", "internal", "web"),
    "Concurrency & State Management": ("workers", "internal", "internal_api"),
    "Configuration & Feature Flags": ("internal_api", "internal", "workers"),
    "Testing Strategy": ("workers", "web", "internal"),
    "Key Abstractions": ("internal", "workers", "internal_api"),
    "Module Deep Dives": ("internal_api", "workers", "web", "cli"),
    "Complexity & Risk Areas": ("workers", "internal", "internal_api"),
    "Suggested Starting Points": ("cli", "internal_api", "workers", "web", "internal"),
}

SECTION_PATH_HINTS: dict[str, tuple[str, ...]] = {
    "System Purpose": (
        "cli/serve.go",
        "internal/api/graphql/schema.resolvers.go",
        "web/src/components/architecture/",
        "workers/comprehension/",
    ),
    "Architecture Overview": (
        "cli/serve.go",
        "internal/api/graphql/schema.resolvers.go",
        "internal/api/rest/router.go",
        "web/src/components/architecture/",
        "workers/knowledge/servicer.py",
    ),
    "External Dependencies": (
        "internal/db/surreal.go",
        "internal/api/rest/llm_config.go",
        "internal/db/llm_config_store.go",
        "workers/common/surreal.py",
        "workers/common/llm/",
    ),
    "Domain Model": (
        "internal/knowledge/",
        "internal/knowledge/models.go",
        "internal/knowledge/understanding.go",
        "internal/llm/",
        "internal/llm/job.go",
        "internal/api/graphql/schema.graphqls",
        "workers/knowledge/",
        "workers/knowledge/types.py",
    ),
    "Core System Flows": (
        "cli/serve.go",
        "internal/api/graphql/schema.resolvers.go",
        "workers/knowledge/servicer.py",
        "internal/db/knowledge_store.go",
        "internal/knowledge/rendering.go",
    ),
    "Code Structure": (
        "cli/",
        "internal/api/",
        "internal/knowledge/",
        "workers/knowledge/",
        "web/src/components/architecture/",
    ),
    "Security Model": (
        "internal/api/middleware/repo_access.go",
        "internal/api/middleware/tenant.go",
        "internal/auth/",
        "internal/api/rest/router.go",
    ),
    "Error Handling Patterns": (
        "internal/api/middleware/repo_access.go",
        "internal/api/graphql/knowledge_errors.go",
        "internal/knowledge/quality.go",
        "workers/knowledge/servicer.py",
    ),
    "Data Flow & Request Lifecycle": (
        "cli/serve.go",
        "internal/api/graphql/schema.resolvers.go",
        "workers/knowledge/servicer.py",
        "internal/db/knowledge_store.go",
        "internal/knowledge/rendering.go",
    ),
    "Concurrency & State Management": (
        "internal/llm/job.go",
        "internal/jobs/queue.go",
        "workers/__main__.py",
        "workers/knowledge/job_state.py",
        "internal/db/llm_job_store.go",
    ),
    "Configuration & Feature Flags": (
        "internal/api/rest/llm_config.go",
        "internal/db/llm_config_store.go",
        "internal/config/",
        "workers/comprehension/hierarchical.py",
    ),
    "Testing Strategy": (
        "workers/tests/",
        "web/src/components/architecture/",
        "internal/api/graphql/",
        "internal/api/graphql/knowledge_support_test.go",
        "workers/common/llm/fake.py",
        "workers/benchmarks/",
    ),
    "Key Abstractions": (
        "internal/knowledge/",
        "internal/knowledge/models.go",
        "internal/llm/",
        "internal/llm/job.go",
        "workers/knowledge/",
        "workers/knowledge/servicer.py",
        "workers/comprehension/renderers.py",
        "internal/api/graphql/schema.resolvers.go",
    ),
    "Module Deep Dives": (
        "internal/api/graphql/",
        "workers/knowledge/",
        "workers/comprehension/",
        "web/src/components/architecture/",
    ),
    "Complexity & Risk Areas": (
        "workers/comprehension/hierarchical.py",
        "internal/knowledge/quality.go",
        "internal/db/knowledge_store.go",
        "internal/api/graphql/schema.resolvers.go",
        "workers/benchmarks/",
    ),
    "Suggested Starting Points": (
        "cmd/sourcebridge/main.go",
        "cli/serve.go",
        "internal/api/graphql/schema.resolvers.go",
        "internal/knowledge/models.go",
        "workers/knowledge/servicer.py",
        "web/src/components/architecture/ArchitectureDiagram.tsx",
    ),
}

SECTION_FOCUS_NOTES: dict[str, str] = {
    "System Purpose": "state what SourceBridge produces for users and operators first: repository understanding, knowledge artifacts, reports, diagrams, and review or QA workflows; do not frame the repo as primarily a REST API or generic code intelligence platform when web, GraphQL, worker, and CLI evidence are present",
    "Architecture Overview": "explain the cooperating surfaces in this order when evidence exists: web product, GraphQL or API entrypoints, workers, persistence, CLI; do not lead with router wiring alone",
    "Domain Model": "focus on repositories, scopes, knowledge artifacts, understanding revisions, jobs, reports, and diagrams; avoid centering scanners unless they dominate the evidence",
    "External Dependencies": "mention only concrete external systems such as SurrealDB, configured LLM providers, and explicit service boundaries; do not list internal APIs, renderers, or diagrams as dependencies, and do not infer gRPC or distributed boundaries from servicer names alone",
    "Data Flow & Request Lifecycle": "trace request entrypoints through API layers into stores or workers; prefer concrete request-to-worker-to-store flows over generic service summaries",
    "Concurrency & State Management": "focus on worker loops, queued jobs, resume state, and persistence-backed state transitions rather than generic async language",
    "Testing Strategy": "mention explicit test directories, fake providers, or benchmark scripts when present; otherwise say testing evidence is limited",
    "Key Abstractions": "prefer SourceBridge abstractions such as artifacts, understanding revisions, jobs, renderers, orchestrators, stores, and servicers; avoid reducing the section to generic database or graph helper types unless they dominate the evidence",
    "Complexity & Risk Areas": "focus on hierarchical summarization, orchestration/reuse, persistence consistency, and renderer quality risks",
}

SECTION_SIGNAL_HINTS: dict[str, tuple[str, ...]] = {
    "System Purpose": ("api", "web", "worker", "route"),
    "Architecture Overview": ("api", "web", "worker", "store", "route"),
    "External Dependencies": ("integration", "store", "config"),
    "Domain Model": ("store", "worker"),
    "Core System Flows": ("api", "worker", "route", "store"),
    "Security Model": ("auth", "api"),
    "Error Handling Patterns": ("worker", "api"),
    "Data Flow & Request Lifecycle": ("route", "api", "worker", "store"),
    "Concurrency & State Management": ("worker", "store"),
    "Configuration & Feature Flags": ("config", "integration"),
    "Testing Strategy": ("worker", "web"),
    "Key Abstractions": ("store", "worker", "api"),
    "Complexity & Risk Areas": ("worker", "integration", "store"),
    "Suggested Starting Points": ("api", "web", "worker", "route"),
}

SECTION_DEPENDENCY_HINTS: dict[str, tuple[str, ...]] = {
    "External Dependencies": ("surreal", "graphql", "grpc", "openai", "anthropic", "openrouter", "ollama", "docker", "cloudflare"),
    "Configuration & Feature Flags": ("openai", "anthropic", "openrouter", "ollama", "surreal"),
    "Data Flow & Request Lifecycle": ("graphql", "grpc", "surreal"),
    "Concurrency & State Management": ("surreal", "redis", "kafka", "sqs"),
    "Complexity & Risk Areas": ("surreal", "graphql", "grpc", "openrouter", "ollama"),
}

SECTION_ENTITY_HINTS: dict[str, tuple[str, ...]] = {
    "Domain Model": ("repository", "knowledge_artifact", "understanding", "job", "requirement", "report", "diagram"),
    "Key Abstractions": ("knowledge_artifact", "understanding", "job", "report", "diagram", "graph"),
    "External Dependencies": ("job", "report", "diagram"),
}

SECTION_DISFAVORED_PATH_SNIPPETS: dict[str, tuple[str, ...]] = {
    "System Purpose": (
        "workers/requirements/scanners/",
        "workers/common/llm/fake.py",
        "workers/cli_review.py",
        "internal/api/rest/router.go",
    ),
    "Architecture Overview": ("workers/requirements/scanners/", "workers/common/llm/fake.py", "workers/cli_review.py"),
    "Core System Flows": ("workers/requirements/scanners/", "workers/common/llm/fake.py"),
    "External Dependencies": (
        "workers/common/llm/fake.py",
        "workers/requirements/scanners/",
        "workers/knowledge/architecture_diagram.py",
        "workers/cli_review.py",
        "workers/knowledge/servicer.py",
        "workers/reasoning/servicer.py",
        "internal/api/graphql/schema.resolvers.go",
        "internal/api/rest/router.go",
    ),
    "Domain Model": ("internal/db/surreal.go", "internal/db/store.go", "internal/graph/store.go"),
    "Key Abstractions": ("internal/db/store.go", "internal/graph/store.go"),
}

GROUP_FEWSHOT_EXAMPLES: dict[tuple[str, ...], str] = {
    DEEP_SECTION_GROUPS[0]: """\
=== Quality examples for this slice ===
Good System Purpose example:
- "This repository builds and serves SourceBridge knowledge artifacts for a codebase. API and GraphQL surfaces
  coordinate requests, background workers index repositories and generate understanding, reports, or diagrams,
  the web app visualizes those results, and the CLI provides operator and developer workflows."

Bad System Purpose example:
- "This repository is mainly a REST API with various components."

Good Architecture Overview example:
- "The architecture combines serve or API entrypoints, GraphQL resolvers, background workers, persistence, and a
  web product surface. API and GraphQL requests schedule or fetch artifact work, workers build understanding and
  render outputs, stores persist revisions and jobs, and web or CLI surfaces consume the resulting artifacts."

Bad Architecture Overview example:
- "The architecture is a REST router plus some helpers."
""",
    DEEP_SECTION_GROUPS[1]: """\
=== Quality examples for this slice ===
Good Domain Model example:
- "The domain centers on repositories, scopes, generated knowledge artifacts, understanding revisions, reports,
  diagrams, and background jobs. Explain those internal entities directly instead of drifting into transport code,
  scanners, or generic storage helpers."

Bad Domain Model example:
- "The domain model is mainly API schemas and comments."

Good Key Abstractions example:
- "Name the repository-specific abstractions that organize work: understanding trees, artifact renderers,
  orchestration jobs, knowledge servicers, and persistence stores. Explain what each abstraction is for."

Bad Key Abstractions example:
- "The key abstractions are the database client and a few helpers."

Good Code Structure example:
- "Organize the explanation around product-facing slices such as `internal/api`, `internal/knowledge`, `workers/*`,
  `web/*`, and CLI entrypoints, then explain how those modules cooperate."

Bad Code Structure example:
- "The repository has several folders with various files."
""",
    DEEP_SECTION_GROUPS[2]: """\
=== Quality examples for this slice ===
Good External Dependencies example:
- "Call out concrete dependencies like SurrealDB, LLM provider configuration, and gRPC boundaries only when the
  selected files show them directly."

Bad External Dependencies example:
- "The system probably uses several cloud services and external APIs."

Bad External Dependencies example:
- "Servicer files imply there is probably a gRPC service boundary."

Good Security example:
- "Ground security claims in middleware, auth stores, token handling, and tenant/repo access enforcement."

Bad Security example:
- "The system appears secure because it has authentication."
""",
    DEEP_SECTION_GROUPS[3]: """\
=== Quality examples for this slice ===
Good Data Flow example:
- "Trace a request from serve/API entrypoints through resolvers or handlers into worker or store operations, then
  explain where asynchronous work or persistence happens."

Bad Data Flow example:
- "Requests go through the system and eventually reach the backend."

Good Testing example:
- "Mention concrete test directories, fake providers, and benchmark scripts when they are present."

Bad Testing example:
- "The repository likely has unit and integration tests."
""",
}


def _is_provider_compute_error(exc: Exception) -> bool:
    text = str(exc).lower()
    return "compute error" in text or "server_error" in text


CLIFF_NOTES_RENDER_TEMPLATE = """\
You are writing a detailed field guide for developers joining this codebase. \
The repository has been analyzed — use the summaries below to write \
thorough, grounded documentation for each required section.

Repository: {repository_name}
Audience: {audience}
Depth: {depth}
Scope: {scope_type}{scope_path_suffix}

=== Repository summary ===
{root_summary}

{section_evidence_plan_block}

=== Notable subsystems ===
{group_summaries}

=== Notable files ===
{file_summaries}

{pre_analysis_block}

{few_shot_examples_block}

{system_shape_guardrail_block}

=== Active relevance profile ===
{relevance_profile}

=== Task ===
{depth_instructions}
Write a JSON array of {section_count} section objects.

Each section object has these keys:
  - "title": one of the required section titles listed below (string)
  - "content": a detailed markdown paragraph of 8-12 sentences. Name specific \
    files, components, functions, and patterns. Explain HOW things work, not \
    just WHAT they are. Target 180-220 words per section — do not stop short \
    once the section has been introduced; elaborate on the named abstractions, \
    cite each file with the specific function or type defined there, and \
    describe at least one concrete interaction between components. (string)
  - "summary": a single-sentence takeaway (string)
  - "confidence": "high", "medium", or "low" (string)
  - "inferred": true if you're extrapolating beyond the summaries (boolean)
  - "evidence": array of objects with keys: source_type, source_id, file_path, \
    line_start, line_end, rationale. Every evidence entry must include an actual \
    repository file path from the summaries above. Include at least one entry \
    per file that appears in the section evidence plan for the section you are \
    writing (unless that file is genuinely off-topic for the section).

Required section titles (produce every one, in this order):
{required_sections}

Confidence rules (mechanical — apply exactly):
- Set confidence to "high" and inferred to false when you cite AT LEAST THREE \
  real repository file paths from the section evidence plan AND name at least \
  three specific functions, types, methods, or test identifiers from them.
- Set confidence to "medium" when you can cite at least one real repo file path \
  but are extending beyond what the summaries make explicit.
- Set confidence to "low" only when no file in the section evidence plan is \
  relevant and you must rely on general inference.
- Do NOT self-downgrade below high when the mechanical criteria above are met — \
  the criteria are the ground truth.

Citation rules:
- Treat the section evidence plan as the primary routing guide. For each \
  section, include at least one evidence entry for every file path that appears \
  in that section's plan line (unless the file is genuinely off-topic for the \
  section you're writing). Evidence entries MUST use real file paths from the \
  plan or the subsystem/file summaries.
- The summaries are context, not evidence by themselves. Synthetic references \
  like "repository_summary", "subsystem_auth", or other non-file labels are \
  invalid.
- Never invent a file path. If you have not seen a path in the plan or \
  summaries above, do not cite it.

Output rules:
- Return ONLY the JSON array — no text before or after it.
- No markdown fences around the JSON.
- Every required title must appear exactly once.
"""

CLIFF_NOTES_SECTION_REPAIR_TEMPLATE = """\
You are repairing one section in a DEEP repository field guide.

Repository: {repository_name}
Audience: {audience}
Section: {section_title}

=== Repository summary ===
{root_summary}

=== Section evidence plan ===
{section_evidence_plan}

=== Notable subsystems ===
{group_summaries}

=== Notable files ===
{file_summaries}

=== Current draft ===
{current_draft}

=== Task ===
Rewrite ONLY the `{section_title}` section as a stronger, more grounded version.
- Keep the section narrowly focused on the section evidence plan.
- Prefer concrete internal entities, stores, jobs, artifacts, tests, or worker flows when the evidence shows them.
- Remove broad filler and unsupported abstractions.
- Cite only real repository file paths from the evidence above.

Return ONLY a JSON array with exactly one object using these keys:
- "title"
- "content"
- "summary"
- "confidence"
- "inferred"
- "evidence"
"""


@dataclass
class CliffNotesRenderer:
    """Renders cliff notes from a hierarchical summary tree.

    ``max_group_summaries`` bounds how many level-2 (package/subsystem)
    summaries are fed into the final prompt. This is the main knob for
    final-prompt size — keeping it small ensures the render call fits
    any model even when the tree itself is enormous.
    """

    provider: LLMProvider
    max_group_summaries: int = 8
    max_file_summaries: int = 12
    max_tokens_per_call: int = 16384  # thinking models need headroom for <think> chains before the JSON output
    model_override: str | None = None
    deep_parallelism: int = 2
    deep_repair_parallelism: int = 2

    async def render(
        self,
        tree: SummaryTree,
        *,
        repository_name: str,
        audience: str = "developer",
        depth: str = "medium",
        scope_type: str = "repository",
        scope_path: str = "",
        pre_analysis: list[dict[str, str]] | None = None,
        required_section_titles: list[str] | None = None,
        relevance_profile: str = "product_core",
    ) -> tuple[CliffNotesResult, LLMUsageRecord]:
        """Render cliff notes from the supplied tree.

        Returns the structured result plus an LLM usage record so the
        servicer can persist billing metrics the same way the legacy
        single-shot path does.
        """
        if required_section_titles:
            required_sections = list(required_section_titles)
        elif depth == "deep" and (scope_type or "repository") == "repository":
            required_sections = list(REQUIRED_SECTIONS_DEEP_REPOSITORY)
        else:
            required_sections = list(REQUIRED_SECTIONS_BY_SCOPE.get(scope_type or "repository", REQUIRED_SECTIONS))
        root = tree.root()
        if root is None:
            raise ValueError("cannot render cliff notes from an empty summary tree")
        render_started = monotonic()

        # Deep mode: widen the context window — include more summaries
        # and leaf-level detail so the output is noticeably richer, but
        # keep the prompt selective enough that render latency and drift
        # stay bounded on medium-large repos.
        if depth == "deep":
            effective_max_groups = min(len(tree.at_level(2)), 10)
            effective_max_files = min(len(tree.at_level(1)), 16)
        else:
            effective_max_groups = self.max_group_summaries
            effective_max_files = self.max_file_summaries

        all_group_nodes = tree.at_level(2)
        all_file_nodes = tree.at_level(1)

        group_nodes = self._select_groups(
            tree,
            root,
            max_n=effective_max_groups,
            relevance_profile=relevance_profile,
            scope_path=scope_path,
        )
        file_nodes = self._select_files(
            tree,
            group_nodes,
            max_n=effective_max_files,
            relevance_profile=relevance_profile,
            scope_path=scope_path,
        )
        section_evidence_plan = ""
        if depth == "deep" and (scope_type or "repository") == "repository":
            section_evidence_plan = self._build_section_evidence_plan(
                required_sections,
                all_group_nodes=all_group_nodes,
                all_file_nodes=all_file_nodes,
                relevance_profile=relevance_profile,
                scope_path=scope_path,
            )

        # Build pre-analysis block from repository-level cliff notes (deep mode)
        pa_block = ""
        if pre_analysis:
            lines = []
            for section in pre_analysis:
                title = section.get("title", "")
                content = section.get("content", "")
                if title and content:
                    lines.append(f"### {title}\n{content}")
            if lines:
                pa_block = (
                    "=== Repository-level analysis (from existing field guide) ===\n"
                    "Use this as PRIMARY grounded context. Sections that build on "
                    "this analysis should have confidence: high.\n\n" + "\n\n".join(lines)
                )

        evidence_store_text = "\n\n".join(
            part
            for part in [
                root.summary_text,
                *(node.summary_text for node in group_nodes),
                *(node.summary_text for node in file_nodes),
                *(section.get("content", "") for section in (pre_analysis or [])),
            ]
            if part
        )

        depth_instructions = {
            "summary": (
                "IMPORTANT: Keep sections concise — 2-3 sentences each, ~800 words total. "
                "Focus on the most important facts only."
            ),
            "medium": (
                "IMPORTANT: Your total output must be at least 1500 words across all sections. "
                "Each section MUST contain detailed, specific content — not vague summaries."
            ),
            "deep": (
                "IMPORTANT: This is a DEEP field guide. Produce evidence-dense sections, not broad filler. "
                "Every section must reference concrete files, functions, types, or line ranges from the summaries. "
                "Avoid generic phrases like 'the system handles' or 'various components'. "
                "For repository scope, return all 16 required sections with operationally useful guidance."
            ),
        }.get(depth, "Your total output must be at least 1500 words across all sections.")

        log.info(
            "cliff_notes_renderer_started",
            repository=repository_name,
            scope_type=scope_type,
            tree_nodes=len(tree.nodes),
            selected_groups=len(group_nodes),
            selected_files=len(file_nodes),
        )

        try:
            if depth == "deep" and (scope_type or "repository") == "repository":
                targeted_titles = [title for title in required_sections if title in DEEP_REPAIR_SECTION_TITLES]
                if targeted_titles and len(targeted_titles) == len(required_sections) and len(required_sections) <= 2:
                    sections, usage = await self._render_targeted_deep_sections(
                        repository_name=repository_name,
                        audience=audience,
                        scope_type=scope_type,
                        scope_path=scope_path,
                        root=root,
                        required_sections=required_sections,
                        all_group_nodes=all_group_nodes,
                        all_file_nodes=all_file_nodes,
                        relevance_profile=relevance_profile,
                        evidence_store_text=evidence_store_text,
                    )
                else:
                    sections, usage = await self._render_deep_repository_groups(
                        repository_name=repository_name,
                        audience=audience,
                        scope_type=scope_type,
                        scope_path=scope_path,
                        root=root,
                        required_sections=required_sections,
                        all_group_nodes=all_group_nodes,
                        all_file_nodes=all_file_nodes,
                        pre_analysis_block=pa_block,
                        relevance_profile=relevance_profile,
                        evidence_store_text=evidence_store_text,
                    )
            else:
                prompt = self._build_render_prompt(
                    repository_name=repository_name,
                    audience=audience,
                    depth=depth,
                    scope_type=scope_type,
                    scope_path=scope_path,
                    root_summary=root.summary_text or "(no repository summary available)",
                    section_evidence_plan=section_evidence_plan,
                    group_nodes=group_nodes,
                    file_nodes=file_nodes,
                    pre_analysis_block=pa_block,
                    few_shot_examples_block="",
                    system_shape_guardrail_block="",
                    relevance_profile=relevance_profile,
                    depth_instructions=depth_instructions,
                    required_sections=required_sections,
                )
                response = await self._render_with_retry(
                    prompt=prompt,
                    scope_type=scope_type,
                )
                sections = self._parse_sections(
                    response.content,
                    required_sections,
                    evidence_store_text=evidence_store_text,
                    apply_deep_quality_gates=(depth == "deep" and (scope_type or "repository") == "repository"),
                )
                usage = LLMUsageRecord(
                    provider="llm",
                    model=response.model,
                    input_tokens=response.input_tokens,
                    output_tokens=response.output_tokens,
                    operation="cliff_notes_render",
                    entity_name=repository_name,
                )
        except Exception as exc:
            log.warning(
                "cliff_notes_renderer_fallback",
                repository=repository_name,
                scope_type=scope_type,
                error=str(exc),
            )
            sections = self._fallback_sections(
                required_sections=required_sections,
                root=root,
                groups=group_nodes,
                files=file_nodes,
                scope_type=scope_type,
                scope_path=scope_path,
            )
            usage = LLMUsageRecord(
                provider="llm",
                model=self.model_override or "fallback",
                input_tokens=0,
                output_tokens=0,
                operation="cliff_notes_render_fallback",
                entity_name=repository_name,
            )

        log.info(
            "cliff_notes_renderer_completed",
            repository=repository_name,
            scope_type=scope_type,
            sections=len(sections),
            tree_nodes=len(tree.nodes),
            group_summaries=len(group_nodes),
            file_summaries=len(file_nodes),
            elapsed_ms=int((monotonic() - render_started) * 1000),
        )

        if depth == "deep" and (scope_type or "repository") == "repository":
            sections = self._normalize_repository_opening_sections(
                sections,
                repository_name=repository_name,
                all_group_nodes=all_group_nodes,
                all_file_nodes=all_file_nodes,
            )

        return CliffNotesResult(sections=sections), usage

    async def _render_targeted_deep_sections(
        self,
        *,
        repository_name: str,
        audience: str,
        scope_type: str,
        scope_path: str,
        root: SummaryNode,
        required_sections: list[str],
        all_group_nodes: list[SummaryNode],
        all_file_nodes: list[SummaryNode],
        relevance_profile: str,
        evidence_store_text: str,
    ) -> tuple[list[CliffNotesSection], LLMUsageRecord]:
        semaphore = asyncio.Semaphore(self.deep_repair_parallelism)

        async def render_one(title: str) -> tuple[str, CliffNotesSection, LLMResponse | None, bool]:
            async with semaphore:
                group_nodes = self._select_group_nodes_for_sections(
                    [title],
                    all_group_nodes=all_group_nodes,
                    relevance_profile=relevance_profile,
                    scope_path=scope_path,
                    limit=3,
                )
                file_nodes = self._select_file_nodes_for_sections(
                    [title],
                    all_file_nodes=all_file_nodes,
                    relevance_profile=relevance_profile,
                    scope_path=scope_path,
                    limit=6,
                )
                prompt = CLIFF_NOTES_SECTION_REPAIR_TEMPLATE.format(
                    repository_name=repository_name or "repository",
                    audience=audience,
                    section_title=title,
                    root_summary=root.summary_text or "(no repository summary available)",
                    section_evidence_plan=self._build_section_evidence_plan(
                        [title],
                        all_group_nodes=group_nodes,
                        all_file_nodes=file_nodes,
                        relevance_profile=relevance_profile,
                        scope_path=scope_path,
                    ),
                    group_summaries=self._format_summaries(group_nodes, label_prefix="Subsystem"),
                    file_summaries=self._format_summaries(file_nodes, label_prefix="File"),
                    current_draft=self._seed_targeted_section_draft(
                        title,
                        root=root,
                        group_nodes=group_nodes,
                        file_nodes=file_nodes,
                    ),
                )
                check_prompt_budget(
                    prompt,
                    system=CLIFF_NOTES_SYSTEM,
                    context=f"hierarchical_render:cliff_notes:targeted:{title}",
                )
                try:
                    response = await self._render_with_retry(
                        prompt=prompt,
                        scope_type=f"{scope_type}:targeted:{title}",
                    )
                    section = self._parse_sections(
                        response.content,
                        [title],
                        evidence_store_text=evidence_store_text,
                        apply_deep_quality_gates=True,
                    )[0]
                    return title, section, response, False
                except Exception as exc:
                    log.warning(
                        "cliff_notes_renderer_targeted_fallback",
                        repository=repository_name,
                        scope_type=scope_type,
                        section_title=title,
                        error=str(exc),
                    )
                    fallback = self._fallback_sections(
                        required_sections=[title],
                        root=root,
                        groups=group_nodes,
                        files=file_nodes,
                        scope_type=f"{scope_type}:targeted",
                        scope_path=scope_path,
                    )[0]
                    return title, fallback, None, True

        rendered = await asyncio.gather(*(render_one(title) for title in required_sections))
        by_title: dict[str, CliffNotesSection] = {}
        input_tokens = 0
        output_tokens = 0
        model_used = self.model_override or "llm"
        used_fallback = False
        for title, section, response, fell_back in rendered:
            by_title[title] = section
            used_fallback = used_fallback or fell_back
            if response is not None:
                input_tokens += response.input_tokens
                output_tokens += response.output_tokens
                model_used = response.model or model_used
        usage = LLMUsageRecord(
            provider="llm",
            model=model_used,
            input_tokens=input_tokens,
            output_tokens=output_tokens,
            operation=(
                "cliff_notes_render_targeted_partial"
                if used_fallback
                else "cliff_notes_render_targeted"
            ),
            entity_name=repository_name,
        )
        return [by_title[title] for title in required_sections], usage

    async def _render_deep_repository_groups(
        self,
        *,
        repository_name: str,
        audience: str,
        scope_type: str,
        scope_path: str,
        root: SummaryNode,
        required_sections: list[str],
        all_group_nodes: list[SummaryNode],
        all_file_nodes: list[SummaryNode],
        pre_analysis_block: str,
        relevance_profile: str,
        evidence_store_text: str,
    ) -> tuple[list[CliffNotesSection], LLMUsageRecord]:
        semaphore = asyncio.Semaphore(self.deep_parallelism)

        async def render_group(
            section_group: tuple[str, ...],
        ) -> tuple[list[CliffNotesSection], LLMResponse | None, bool]:
            async with semaphore:
                group_nodes = self._select_group_nodes_for_sections(
                    section_group,
                    all_group_nodes=all_group_nodes,
                    relevance_profile=relevance_profile,
                    scope_path=scope_path,
                    limit=4,
                )
                file_nodes = self._select_file_nodes_for_sections(
                    section_group,
                    all_file_nodes=all_file_nodes,
                    relevance_profile=relevance_profile,
                    scope_path=scope_path,
                    limit=8,
                )
                section_plan = self._build_section_evidence_plan(
                    list(section_group),
                    all_group_nodes=group_nodes,
                    all_file_nodes=file_nodes,
                    relevance_profile=relevance_profile,
                    scope_path=scope_path,
                )
                prompt = self._build_render_prompt(
                    repository_name=repository_name,
                    audience=audience,
                    depth="deep",
                    scope_type=scope_type,
                    scope_path=scope_path,
                    root_summary=self._theme_root_summary(root, section_group),
                    section_evidence_plan=section_plan,
                    group_nodes=group_nodes,
                    file_nodes=file_nodes,
                    pre_analysis_block=pre_analysis_block,
                    few_shot_examples_block=GROUP_FEWSHOT_EXAMPLES.get(section_group, ""),
                    system_shape_guardrail_block=self._build_section_group_guardrail(
                        section_group,
                        group_nodes=group_nodes,
                        file_nodes=file_nodes,
                    ),
                    relevance_profile=relevance_profile,
                    depth_instructions=(
                        "IMPORTANT: This is a DEEP themed field guide slice. Produce evidence-dense sections, "
                        "not broad filler. Stay inside the requested sections only and cite concrete repo files.\n"
                        f"{GROUP_INSTRUCTIONS.get(section_group, '')}"
                    ),
                    required_sections=list(section_group),
                )
                try:
                    response = await self._render_with_retry(
                        prompt=prompt,
                        scope_type=f"{scope_type}:deep_group",
                    )
                    sections = self._parse_sections(
                        response.content,
                        list(section_group),
                        evidence_store_text=evidence_store_text,
                        apply_deep_quality_gates=True,
                    )
                    return sections, response, False
                except Exception as exc:
                    log.warning(
                        "cliff_notes_renderer_group_fallback",
                        repository=repository_name,
                        scope_type=scope_type,
                        section_group=section_group,
                        error=str(exc),
                    )
                    return (
                        self._fallback_sections(
                            required_sections=list(section_group),
                            root=root,
                            groups=group_nodes,
                            files=file_nodes,
                            scope_type=f"{scope_type}:deep_group",
                            scope_path=scope_path,
                        ),
                        None,
                        True,
                    )

        tasks = [
            render_group(group)
            for group in DEEP_SECTION_GROUPS
            if any(section in required_sections for section in group)
        ]
        rendered = await asyncio.gather(*tasks)

        by_title: dict[str, CliffNotesSection] = {}
        input_tokens = 0
        output_tokens = 0
        model_used = self.model_override or "llm"
        used_fallback = False
        for sections, response, group_fallback in rendered:
            used_fallback = used_fallback or group_fallback
            if response is not None:
                input_tokens += response.input_tokens
                output_tokens += response.output_tokens
                model_used = response.model or model_used
            for section in sections:
                by_title[section.title] = section

        ordered_sections = [
            by_title.get(
                title,
                CliffNotesSection(
                    title=title,
                    content="*Insufficient data to generate this section.*",
                    summary="Not enough information available.",
                    confidence="low",
                    inferred=True,
                ),
            )
            for title in required_sections
        ]
        ordered_sections = await self._repair_deep_sections(
            repository_name=repository_name,
            audience=audience,
            root=root,
            sections=ordered_sections,
            all_group_nodes=all_group_nodes,
            all_file_nodes=all_file_nodes,
            relevance_profile=relevance_profile,
            scope_path=scope_path,
            evidence_store_text=evidence_store_text,
        )
        usage = LLMUsageRecord(
            provider="llm",
            model=model_used,
            input_tokens=input_tokens,
            output_tokens=output_tokens,
            operation=(
                "cliff_notes_render_parallel_repaired_partial"
                if used_fallback
                else "cliff_notes_render_parallel_repaired"
            ),
            entity_name=repository_name,
        )
        return ordered_sections, usage

    async def _repair_deep_sections(
        self,
        *,
        repository_name: str,
        audience: str,
        root: SummaryNode,
        sections: list[CliffNotesSection],
        all_group_nodes: list[SummaryNode],
        all_file_nodes: list[SummaryNode],
        relevance_profile: str,
        scope_path: str,
        evidence_store_text: str,
    ) -> list[CliffNotesSection]:
        by_title = {section.title: section for section in sections}
        semaphore = asyncio.Semaphore(self.deep_repair_parallelism)

        async def repair_one(title: str) -> tuple[str, CliffNotesSection | None]:
            async with semaphore:
                current = by_title.get(title)
                if current is None:
                    return title, None
                group_nodes = self._select_group_nodes_for_sections(
                    [title],
                    all_group_nodes=all_group_nodes,
                    relevance_profile=relevance_profile,
                    scope_path=scope_path,
                    limit=3,
                )
                file_nodes = self._select_file_nodes_for_sections(
                    [title],
                    all_file_nodes=all_file_nodes,
                    relevance_profile=relevance_profile,
                    scope_path=scope_path,
                    limit=6,
                )
                prompt = CLIFF_NOTES_SECTION_REPAIR_TEMPLATE.format(
                    repository_name=repository_name or "repository",
                    audience=audience,
                    section_title=title,
                    root_summary=root.summary_text or "(no repository summary available)",
                    section_evidence_plan=self._build_section_evidence_plan(
                        [title],
                        all_group_nodes=group_nodes,
                        all_file_nodes=file_nodes,
                        relevance_profile=relevance_profile,
                        scope_path=scope_path,
                    ),
                    group_summaries=self._format_summaries(group_nodes, label_prefix="Subsystem"),
                    file_summaries=self._format_summaries(file_nodes, label_prefix="File"),
                    current_draft=current.content,
                )
                check_prompt_budget(
                    prompt,
                    system=CLIFF_NOTES_SYSTEM,
                    context=f"hierarchical_render:cliff_notes:repair:{title}",
                )
                try:
                    response = await self._render_with_retry(
                        prompt=prompt,
                        scope_type=f"repository:repair:{title}",
                    )
                    repaired = self._parse_sections(
                        response.content,
                        [title],
                        evidence_store_text=evidence_store_text,
                        apply_deep_quality_gates=True,
                    )[0]
                    if self._should_accept_repaired_section(current, repaired):
                        return title, repaired
                except Exception as exc:
                    log.warning(
                        "cliff_notes_renderer_repair_skipped",
                        section_title=title,
                        error=str(exc),
                    )
                return title, None

        repaired_pairs = await asyncio.gather(
            *(repair_one(title) for title in DEEP_REPAIR_SECTION_TITLES if title in by_title)
        )
        for title, repaired in repaired_pairs:
            if repaired is not None:
                by_title[title] = repaired
        return [by_title[section.title] for section in sections]

    def _seed_targeted_section_draft(
        self,
        title: str,
        *,
        root: SummaryNode,
        group_nodes: list[SummaryNode],
        file_nodes: list[SummaryNode],
    ) -> str:
        return (
            f"Focus section: {title}.\n\n"
            f"Repository orientation:\n{(root.summary_text or '(no repository summary available)').strip()}\n\n"
            f"Notable subsystems:\n{self._summary_bullets(group_nodes, max_items=3)}\n\n"
            f"Notable files:\n{self._summary_bullets(file_nodes, max_items=5)}"
        )

    def _should_accept_repaired_section(
        self,
        current: CliffNotesSection,
        repaired: CliffNotesSection,
    ) -> bool:
        current_refs = len(extract_section_evidence_refs(current.evidence))
        repaired_refs = len(extract_section_evidence_refs(repaired.evidence))
        current_low = current.confidence.lower() == "low"
        repaired_low = repaired.confidence.lower() == "low"
        if current_refs > 0 and repaired_refs == 0:
            return False
        if repaired_low and not current_low:
            return False
        if repaired_refs < current_refs and len(repaired.content) <= len(current.content):
            return False
        return True

    def _build_render_prompt(
        self,
        *,
        repository_name: str,
        audience: str,
        depth: str,
        scope_type: str,
        scope_path: str,
        root_summary: str,
        section_evidence_plan: str,
        group_nodes: list[SummaryNode],
        file_nodes: list[SummaryNode],
        pre_analysis_block: str,
        few_shot_examples_block: str,
        system_shape_guardrail_block: str,
        relevance_profile: str,
        depth_instructions: str,
        required_sections: list[str],
    ) -> str:
        prompt = CLIFF_NOTES_RENDER_TEMPLATE.format(
            repository_name=repository_name or "repository",
            audience=audience,
            depth=depth,
            scope_type=scope_type,
            scope_path_suffix=f" ({scope_path})" if scope_path else "",
            root_summary=root_summary,
            section_evidence_plan_block=(
                "=== Section evidence plan ===\n"
                f"{section_evidence_plan}\n"
                if section_evidence_plan
                else ""
            ),
            group_summaries=self._format_summaries(group_nodes, label_prefix="Subsystem"),
            file_summaries=self._format_summaries(file_nodes, label_prefix="File"),
            pre_analysis_block=pre_analysis_block,
            few_shot_examples_block=few_shot_examples_block,
            system_shape_guardrail_block=system_shape_guardrail_block,
            relevance_profile=relevance_profile,
            depth_instructions=depth_instructions,
            section_count=len(required_sections),
            required_sections="\n".join(f"- {t}" for t in required_sections),
        )
        check_prompt_budget(
            prompt,
            system=CLIFF_NOTES_SYSTEM,
            context=f"hierarchical_render:cliff_notes:{scope_type}",
        )
        return prompt

    async def _render_with_retry(
        self,
        *,
        prompt: str,
        scope_type: str,
    ) -> LLMResponse:
        context = f"hierarchical_render:cliff_notes:{scope_type}"
        last_exc: Exception | None = None
        for attempt in range(1, 4):
            try:
                return require_nonempty(
                    await complete_with_optional_model(
                        self.provider,
                        prompt,
                        system=CLIFF_NOTES_SYSTEM,
                        temperature=0.0,
                        max_tokens=self.max_tokens_per_call,
                        model=self.model_override,
                    ),
                    context=context,
                )
            except Exception as exc:
                last_exc = exc
                if _is_provider_compute_error(exc) and attempt < 3:
                    delay = 0.4 * (2 ** (attempt - 1))
                    log.warning(
                        "cliff_notes_renderer_retry",
                        scope_type=scope_type,
                        attempt=attempt,
                        delay_s=delay,
                        error=str(exc),
                    )
                    import asyncio

                    await asyncio.sleep(delay)
                    continue
                break
        assert last_exc is not None
        raise last_exc

    # ------------------------------------------------------------------
    # Selection helpers

    def _select_groups(
        self,
        tree: SummaryTree,
        root: SummaryNode,
        max_n: int | None = None,
        *,
        relevance_profile: str,
        scope_path: str,
    ) -> list[SummaryNode]:
        """Pick up to N level-2 children under the root, preferring the
        ones with the most source tokens (roughly "biggest subsystems
        first"). Falls back to insertion order when source_tokens are
        all zero."""
        limit = max_n if max_n is not None else self.max_group_summaries
        children = tree.children_of(root.unit_id)
        ordered = sorted(
            enumerate(children),
            key=lambda pair: (
                relevance_penalty(_node_label(pair[1]), profile=relevance_profile, scope_path=scope_path),
                -pair[1].source_tokens,
                pair[0],
            ),
        )
        return [pair[1] for pair in ordered[:limit]]

    def _select_files(
        self,
        tree: SummaryTree,
        group_nodes: list[SummaryNode],
        max_n: int | None = None,
        *,
        relevance_profile: str,
        scope_path: str,
    ) -> list[SummaryNode]:
        """Pick up to N level-1 summaries across the selected groups.

        We round-robin across the groups so a single dominant package
        doesn't eat the whole file budget.
        """
        limit = max_n if max_n is not None else self.max_file_summaries
        per_group: list[list[SummaryNode]] = []
        for group in group_nodes:
            files = sorted(
                enumerate(tree.children_of(group.unit_id)),
                key=lambda pair: (
                    relevance_penalty(_node_label(pair[1]), profile=relevance_profile, scope_path=scope_path),
                    -pair[1].source_tokens,
                    pair[0],
                ),
            )
            per_group.append([pair[1] for pair in files])

        picked: list[SummaryNode] = []
        idx = 0
        while len(picked) < limit and any(per_group):
            any_progress = False
            for bucket in per_group:
                if idx < len(bucket) and len(picked) < limit:
                    picked.append(bucket[idx])
                    any_progress = True
            if not any_progress:
                break
            idx += 1
        return picked

    def _build_section_evidence_plan(
        self,
        required_sections: list[str],
        *,
        all_group_nodes: list[SummaryNode],
        all_file_nodes: list[SummaryNode],
        relevance_profile: str,
        scope_path: str,
    ) -> str:
        lines: list[str] = []
        for title in required_sections:
            if title in {"System Purpose", "Architecture Overview"}:
                groups, files = self._build_system_shape_section_plan_nodes(
                    title,
                    all_group_nodes=all_group_nodes,
                    all_file_nodes=all_file_nodes,
                    relevance_profile=relevance_profile,
                    scope_path=scope_path,
                )
            elif title in {"Domain Model", "Key Abstractions"}:
                groups, files = self._build_domain_section_plan_nodes(
                    title,
                    all_group_nodes=all_group_nodes,
                    all_file_nodes=all_file_nodes,
                    relevance_profile=relevance_profile,
                    scope_path=scope_path,
                )
            else:
                groups = self._rank_nodes_for_section(
                    title,
                    nodes=all_group_nodes,
                    kind="group",
                    limit=2,
                    relevance_profile=relevance_profile,
                    scope_path=scope_path,
                )
                files = self._rank_nodes_for_section(
                    title,
                    nodes=all_file_nodes,
                    kind="file",
                    limit=4,
                    relevance_profile=relevance_profile,
                    scope_path=scope_path,
                )
            if not groups and not files:
                continue
            group_part = ", ".join(_node_label(node) for node in groups) or "none"
            file_part = ", ".join(_file_with_top_symbols(node) for node in files) or "none"
            focus_note = SECTION_FOCUS_NOTES.get(title)
            focus_part = f" | focus [{focus_note}]" if focus_note else ""
            lines.append(f"- {title}: subsystems [{group_part}] | files [{file_part}]{focus_part}")
        return "\n".join(lines)

    def _build_system_shape_section_plan_nodes(
        self,
        title: str,
        *,
        all_group_nodes: list[SummaryNode],
        all_file_nodes: list[SummaryNode],
        relevance_profile: str,
        scope_path: str,
    ) -> tuple[list[SummaryNode], list[SummaryNode]]:
        groups = self._rank_nodes_for_section(
            title,
            nodes=all_group_nodes,
            kind="group",
            limit=2,
            relevance_profile=relevance_profile,
            scope_path=scope_path,
        )
        files: list[SummaryNode] = []
        seen: set[str] = set()
        disfavored_paths = tuple(snippet.lower() for snippet in SECTION_DISFAVORED_PATH_SNIPPETS.get(title, ()))
        for area in ("internal_api", "web", "workers", "cli"):
            seeded = self._best_area_seed_for_sections(
                area,
                sections=(title,),
                nodes=all_file_nodes,
                relevance_profile=relevance_profile,
                scope_path=scope_path,
            )
            if seeded is None or seeded.unit_id in seen:
                continue
            files.append(seeded)
            seen.add(seeded.unit_id)
        for node in self._rank_nodes_for_section(
            title,
            nodes=all_file_nodes,
            kind="file",
            limit=6,
            relevance_profile=relevance_profile,
            scope_path=scope_path,
        ):
            if node.unit_id in seen:
                continue
            label = _node_label(node).lower()
            if disfavored_paths and any(snippet in label for snippet in disfavored_paths):
                continue
            files.append(node)
            seen.add(node.unit_id)
            if len(files) >= 4:
                break
        return groups, files[:4]

    def _build_domain_section_plan_nodes(
        self,
        title: str,
        *,
        all_group_nodes: list[SummaryNode],
        all_file_nodes: list[SummaryNode],
        relevance_profile: str,
        scope_path: str,
    ) -> tuple[list[SummaryNode], list[SummaryNode]]:
        """Pick files for Domain Model / Key Abstractions with subsystem breadth.

        Strategy: take the top-ranked section files first (preserving
        per-section specificity), then fill with one file per
        underrepresented surface (``internal``, ``workers``,
        ``internal_api``, ``web``). This prevents the prior regression
        where the ranker fixated on whichever subsystem had the most
        keyword matches and the LLM produced a narrow, diagram-only
        Domain Model.
        """

        groups = self._rank_nodes_for_section(
            title,
            nodes=all_group_nodes,
            kind="group",
            limit=3,
            relevance_profile=relevance_profile,
            scope_path=scope_path,
        )
        ranked_candidates = self._rank_nodes_for_section(
            title,
            nodes=all_file_nodes,
            kind="file",
            limit=6,
            relevance_profile=relevance_profile,
            scope_path=scope_path,
        )
        files: list[SummaryNode] = list(ranked_candidates[:4])
        seen: set[str] = {node.unit_id for node in files}
        seen_areas: set[str] = {area for node in files if (area := _node_area(node))}
        for area in ("internal", "workers", "internal_api", "web"):
            if area in seen_areas or len(files) >= 6:
                continue
            seeded = self._best_area_seed_for_sections(
                area,
                sections=(title,),
                nodes=all_file_nodes,
                relevance_profile=relevance_profile,
                scope_path=scope_path,
            )
            if seeded is None or seeded.unit_id in seen:
                continue
            files.append(seeded)
            seen.add(seeded.unit_id)
            seen_areas.add(area)
        return groups, files[:6]

    def _select_group_nodes_for_sections(
        self,
        sections: tuple[str, ...] | list[str],
        *,
        all_group_nodes: list[SummaryNode],
        relevance_profile: str,
        scope_path: str,
        limit: int,
    ) -> list[SummaryNode]:
        selected: list[SummaryNode] = []
        seen: set[str] = set()
        for title in sections:
            for seeded in self._hint_seed_nodes_for_section(
                title,
                nodes=all_group_nodes,
                relevance_profile=relevance_profile,
                scope_path=scope_path,
            ):
                if seeded.unit_id in seen:
                    continue
                selected.append(seeded)
                seen.add(seeded.unit_id)
                if len(selected) >= limit:
                    return selected
        for title in sections:
            seeded = self._best_section_seed(
                title,
                nodes=all_group_nodes,
                kind="group",
                relevance_profile=relevance_profile,
                scope_path=scope_path,
            )
            if seeded is None or seeded.unit_id in seen:
                continue
            selected.append(seeded)
            seen.add(seeded.unit_id)
            if len(selected) >= limit:
                return selected
        for title in sections:
            for node in self._rank_nodes_for_section(
                title,
                nodes=all_group_nodes,
                kind="group",
                limit=2,
                relevance_profile=relevance_profile,
                scope_path=scope_path,
            ):
                if node.unit_id in seen:
                    continue
                selected.append(node)
                seen.add(node.unit_id)
                if len(selected) >= limit:
                    return selected
        return selected

    def _select_file_nodes_for_sections(
        self,
        sections: tuple[str, ...] | list[str],
        *,
        all_file_nodes: list[SummaryNode],
        relevance_profile: str,
        scope_path: str,
        limit: int,
    ) -> list[SummaryNode]:
        selected: list[SummaryNode] = []
        seen: set[str] = set()
        seen_areas: set[str] = set()
        system_slice = any(
            section in {"System Purpose", "Architecture Overview", "Core System Flows", "Suggested Starting Points"}
            for section in sections
        )
        diversify = system_slice
        for title in sections:
            for seeded in self._hint_seed_nodes_for_section(
                title,
                nodes=all_file_nodes,
                relevance_profile=relevance_profile,
                scope_path=scope_path,
            ):
                if seeded.unit_id in seen:
                    continue
                selected.append(seeded)
                seen.add(seeded.unit_id)
                area = _node_area(seeded)
                if area:
                    seen_areas.add(area)
                if len(selected) >= limit:
                    return selected
        if system_slice:
            for area in SYSTEM_GROUP_PREFERRED_AREAS:
                seeded = self._best_area_seed_for_sections(
                    area,
                    sections=sections,
                    nodes=all_file_nodes,
                    relevance_profile=relevance_profile,
                    scope_path=scope_path,
                )
                if seeded is None or seeded.unit_id in seen:
                    continue
                selected.append(seeded)
                seen.add(seeded.unit_id)
                seen_areas.add(area)
                if len(selected) >= limit:
                    return selected
        if not system_slice:
            for title in sections:
                seeded = self._best_section_seed(
                    title,
                    nodes=all_file_nodes,
                    kind="file",
                    relevance_profile=relevance_profile,
                    scope_path=scope_path,
                )
                if seeded is None or seeded.unit_id in seen:
                    continue
                selected.append(seeded)
                seen.add(seeded.unit_id)
                if len(selected) >= limit:
                    return selected
        for title in sections:
            for node in self._rank_nodes_for_section(
                title,
                nodes=all_file_nodes,
                kind="file",
                limit=4,
                relevance_profile=relevance_profile,
                scope_path=scope_path,
            ):
                if node.unit_id in seen:
                    continue
                area = _node_area(node)
                if diversify and area in seen_areas:
                    continue
                selected.append(node)
                seen.add(node.unit_id)
                if diversify and area:
                    seen_areas.add(area)
                if len(selected) >= limit:
                    return selected
        if diversify and len(selected) < limit:
            for title in sections:
                for node in self._rank_nodes_for_section(
                    title,
                    nodes=all_file_nodes,
                    kind="file",
                    limit=4,
                    relevance_profile=relevance_profile,
                    scope_path=scope_path,
                ):
                    if node.unit_id in seen:
                        continue
                    selected.append(node)
                    seen.add(node.unit_id)
                    if len(selected) >= limit:
                        return selected
        return selected

    def _theme_root_summary(self, root: SummaryNode, section_group: tuple[str, ...]) -> str:
        base = (root.summary_text or "(no repository summary available)").strip()
        fact_block = _root_fact_orientation(root)
        if section_group == DEEP_SECTION_GROUPS[0]:
            headline = root.headline or _first_line(base) or "Repository overview"
            text = (
                f"{headline}\n\n"
                "Use the selected subsystem and file evidence below as the primary basis for system purpose and "
                "architecture claims. Treat this repository summary as orientation only."
            )
            if fact_block:
                text += f"\n\n{fact_block}"
            return text
        if fact_block:
            return f"{base}\n\n{fact_block}"
        return base

    def _build_section_group_guardrail(
        self,
        section_group: tuple[str, ...],
        *,
        group_nodes: list[SummaryNode],
        file_nodes: list[SummaryNode],
    ) -> str:
        if section_group == DEEP_SECTION_GROUPS[0]:
            area_examples: dict[str, str] = {}
            for node in [*file_nodes, *group_nodes]:
                area = _node_area(node)
                if not area or area in area_examples:
                    continue
                area_examples[area] = _node_label(node)

            preferred_order = ("internal_api", "web", "workers", "cli")
            surface_names = {
                "internal_api": "API/GraphQL surface",
                "web": "web product surface",
                "workers": "background worker surface",
                "cli": "CLI surface",
            }
            bullets = []
            for area in preferred_order:
                example = area_examples.get(area)
                if example:
                    bullets.append(f"- {surface_names[area]}: `{example}`")
            if not bullets:
                return ""
            return (
                "=== System shape guardrail ===\n"
                "Treat these evidence anchors as the primary basis for repo-purpose and architecture framing.\n"
                "If API/web/worker evidence is present, describe the repository as a multi-surface code intelligence "
                "system and mention CLI as one access path rather than the dominant purpose.\n"
                "If a web product surface is present, mention it explicitly instead of reducing the repository to APIs "
                "and workers alone.\n"
                "Do not let specialized scanner, benchmark, or fake-provider files define the repository purpose when "
                "broader API, web, and knowledge-service evidence is present.\n"
                + "\n".join(bullets)
                + "\n"
            )
        if section_group == DEEP_SECTION_GROUPS[1]:
            entity_examples: dict[str, str] = {}
            for node in [*file_nodes, *group_nodes]:
                metadata = node.metadata or {}
                for entity in _metadata_values(
                    metadata,
                    "fact_entity_signals",
                    "fact_package_entities",
                    "fact_root_entities",
                    "entity_signals",
                ):
                    if entity not in entity_examples:
                        entity_examples[entity] = _node_label(node)
            bullets = []
            for entity in (
                "repository",
                "knowledge_artifact",
                "understanding",
                "job",
                "requirement",
                "report",
                "diagram",
            ):
                example = entity_examples.get(entity)
                if example:
                    bullets.append(f"- {entity}: `{example}`")
            inventory = _format_selected_fact_inventory(group_nodes, file_nodes)
            if not bullets and not inventory:
                return ""
            parts: list[str] = [
                "=== Domain-model guardrail ===",
                "Treat these entity anchors and typed-fact signals as the primary basis for `Domain Model`, "
                "`Key Abstractions`, `Code Structure`, and `Module Deep Dives`.",
                "Prefer repositories, knowledge artifacts, understanding revisions, jobs, requirements, and report/"
                "diagram abstractions over generic stores or resolvers unless the selected evidence is overwhelmingly "
                "storage-only. Cite the symbol names below by their actual file paths rather than inventing types.",
            ]
            if bullets:
                parts.append("Entity anchors (from selected evidence):\n" + "\n".join(bullets))
            if inventory:
                parts.append(inventory)
            return "\n\n".join(parts) + "\n"
        if section_group == DEEP_SECTION_GROUPS[3]:
            inventory = _format_selected_fact_inventory(group_nodes, file_nodes)
            if not inventory:
                return ""
            return (
                "=== Runtime-behavior guardrail ===\n"
                "Treat the typed-fact signals below as the primary basis for `Data Flow & Request Lifecycle`, "
                "`Concurrency & State Management`, `Testing Strategy`, and `Complexity & Risk Areas`. Ground each "
                "claim in a specific symbol or file from this inventory rather than generic descriptions of tests, "
                "queues, or risks.\n\n"
                f"{inventory}\n"
            )
        return ""

    def _best_section_seed(
        self,
        title: str,
        *,
        nodes: list[SummaryNode],
        kind: str,
        relevance_profile: str,
        scope_path: str,
    ) -> SummaryNode | None:
        ranked = self._rank_nodes_for_section(
            title,
            nodes=nodes,
            kind=kind,
            limit=1,
            relevance_profile=relevance_profile,
            scope_path=scope_path,
        )
        return ranked[0] if ranked else None

    def _hint_seed_nodes_for_section(
        self,
        title: str,
        *,
        nodes: list[SummaryNode],
        relevance_profile: str,
        scope_path: str,
    ) -> list[SummaryNode]:
        hints = SECTION_PATH_HINTS.get(title, ())
        if not hints:
            return []
        limit = SECTION_MULTI_HINT_SEED_LIMITS.get(title, 1)
        ranked_nodes = sorted(
            nodes,
            key=lambda node: (
                relevance_penalty(_node_label(node), profile=relevance_profile, scope_path=scope_path),
                -node.source_tokens,
            ),
        )
        selected: list[SummaryNode] = []
        seen: set[str] = set()
        for hint in hints:
            hint_lower = hint.lower()
            for node in ranked_nodes:
                if node.unit_id in seen:
                    continue
                text = " ".join(
                    part
                    for part in [
                        _node_label(node),
                        node.headline or "",
                        str((node.metadata or {}).get("module_label", "") or ""),
                    ]
                    if part
                ).lower()
                if hint_lower in text:
                    selected.append(node)
                    seen.add(node.unit_id)
                    if len(selected) >= limit:
                        return selected
                    break
        return selected

    def _best_area_seed_for_sections(
        self,
        area: str,
        *,
        sections: tuple[str, ...] | list[str],
        nodes: list[SummaryNode],
        relevance_profile: str,
        scope_path: str,
    ) -> SummaryNode | None:
        disfavored_paths = tuple(
            snippet.lower()
            for title in sections
            for snippet in SECTION_DISFAVORED_PATH_SNIPPETS.get(title, ())
        )
        candidates = [
            node
            for node in nodes
            if _node_area(node) == area
            and relevance_penalty(_node_label(node), profile=relevance_profile, scope_path=scope_path) == 0
        ]
        if not candidates:
            return None
        preferred_candidates = [
            node for node in candidates if not any(snippet in _node_label(node).lower() for snippet in disfavored_paths)
        ]
        if preferred_candidates:
            candidates = preferred_candidates
        best_lists = [
            self._rank_nodes_for_section(
                title,
                nodes=candidates,
                kind="file",
                limit=1,
                relevance_profile=relevance_profile,
                scope_path=scope_path,
            )
            for title in sections
        ]
        for ranked in best_lists:
            if ranked:
                return ranked[0]
        return candidates[0]

    def _rank_nodes_for_section(
        self,
        title: str,
        *,
        nodes: list[SummaryNode],
        kind: str,
        limit: int,
        relevance_profile: str,
        scope_path: str,
    ) -> list[SummaryNode]:
        keywords = SECTION_KEYWORDS.get(title, ())
        area_priorities = SECTION_AREA_PRIORITIES.get(title, ())
        path_hints = SECTION_PATH_HINTS.get(title, ())
        signal_hints = SECTION_SIGNAL_HINTS.get(title, ())
        dependency_hints = SECTION_DEPENDENCY_HINTS.get(title, ())
        entity_hints = SECTION_ENTITY_HINTS.get(title, ())
        disfavored_paths = SECTION_DISFAVORED_PATH_SNIPPETS.get(title, ())
        scored: list[tuple[int, int, int, int, int, int, int, int, SummaryNode]] = []
        for idx, node in enumerate(nodes):
            label = _node_label(node)
            penalty = relevance_penalty(label, profile=relevance_profile, scope_path=scope_path)
            area = _node_area(node)
            area_rank = area_priorities.index(area) if area in area_priorities else len(area_priorities)
            metadata = node.metadata or {}
            path_signals = _metadata_values(
                metadata,
                "fact_path_signals",
                "fact_package_signals",
                "fact_root_signals",
                "path_signals",
            )
            dependency_signals = _metadata_values(
                metadata,
                "fact_external_dependencies",
                "external_dependency_signals",
            )
            entity_signals = _metadata_values(
                metadata,
                "fact_entity_signals",
                "fact_package_entities",
                "fact_root_entities",
                "entity_signals",
            )
            text = " ".join(
                part
                for part in [
                    label,
                    node.headline or "",
                    node.summary_text or "",
                    str(metadata.get("module_label", "") or ""),
                    " ".join(path_signals),
                    " ".join(entity_signals),
                    " ".join(dependency_signals),
                    " ".join(_metadata_values(metadata, "fact_roles", "fact_package_roles", "fact_root_roles")),
                ]
                if part
            ).lower()
            hint_hits = sum(1 for hint in path_hints if hint.lower() in text)
            signal_hits = sum(1 for hint in signal_hints if hint.lower() in path_signals or hint.lower() in text)
            entity_hits = sum(1 for hint in entity_hints if hint.lower() in entity_signals or hint.lower() in text)
            dependency_hits = sum(
                1 for hint in dependency_hints if hint.lower() in dependency_signals or hint.lower() in text
            )
            disfavored_hits = sum(1 for snippet in disfavored_paths if snippet.lower() in text)
            keyword_hits = sum(1 for keyword in keywords if keyword in text)
            if kind == "file" and "/" in label:
                keyword_hits += 1
            scored.append(
                (
                    penalty + (disfavored_hits * 10),
                    area_rank,
                    -entity_hits,
                    -signal_hits,
                    -dependency_hits,
                    -hint_hits,
                    -keyword_hits,
                    -node.source_tokens,
                    idx,
                    node,
                )
            )
        ranked = [item[-1] for item in sorted(scored)]
        if relevance_profile == "product_core":
            preferred = [
                node
                for node in ranked
                if relevance_penalty(_node_label(node), profile=relevance_profile, scope_path=scope_path) == 0
            ]
            if preferred:
                return preferred[:limit]
        return ranked[:limit]

    def _format_summaries(
        self,
        nodes: list[SummaryNode],
        *,
        label_prefix: str,
    ) -> str:
        if not nodes:
            return f"(no {label_prefix.lower()} summaries available)"
        lines: list[str] = []
        for node in nodes:
            label = _node_label(node) or label_prefix
            headline = node.headline or _first_line(node.summary_text)
            body = node.summary_text.strip()
            trimmed_headline = headline[:180]
            trimmed_body = _truncate_body(body, max_chars=320 if label_prefix == "Subsystem" else 220)
            lines.append(f"{label_prefix}: {label}\n  {trimmed_headline}\n  {trimmed_body}")
        return "\n\n".join(lines)

    def _fallback_sections(
        self,
        *,
        required_sections: list[str],
        root: SummaryNode,
        groups: list[SummaryNode],
        files: list[SummaryNode],
        scope_type: str,
        scope_path: str,
    ) -> list[CliffNotesSection]:
        scope_label = scope_path or scope_type or "repository"
        root_summary = (root.summary_text or "No repository summary was available.").strip()
        group_lines = self._summary_bullets(groups, max_items=4)
        file_lines = self._summary_bullets(files, max_items=6)
        fallback_note = (
            "The model backend failed during the final render step, so this section was assembled "
            "from the hierarchical summaries that were already produced."
        )
        sections: list[CliffNotesSection] = []
        for title in required_sections:
            content = (
                f"{root_summary}\n\n"
                f"Scope: {scope_label}.\n\n"
                f"Notable subsystems:\n{group_lines}\n\n"
                f"Notable files:\n{file_lines}\n\n"
                f"{fallback_note}"
            )
            sections.append(
                CliffNotesSection(
                    title=title,
                    content=content,
                    summary=f"Fallback summary for {title.lower()} built from hierarchical repository notes.",
                    confidence="low",
                    inferred=True,
                    evidence=[],
                )
            )
        return sections

    def _summary_bullets(self, nodes: list[SummaryNode], *, max_items: int) -> str:
        if not nodes:
            return "- No grounded summaries were available."
        lines: list[str] = []
        for node in nodes[:max_items]:
            label = _node_label(node)
            summary = (node.summary_text or node.headline or "").strip().replace("\n", " ")
            if not summary:
                summary = "Summary unavailable."
            lines.append(f"- {label}: {summary[:220]}")
        return "\n".join(lines)

    def _parse_sections(
        self,
        raw_content: str,
        required_sections: list[str],
        *,
        evidence_store_text: str = "",
        apply_deep_quality_gates: bool = False,
    ) -> list[CliffNotesSection]:
        """Parse the LLM JSON output into typed sections.

        Reuses the shared parser from the legacy path so behavior
        stays consistent — tolerant of markdown fences, <think> blocks,
        and preamble/postamble text.
        """
        try:
            raw_sections = parse_json_sections(raw_content)
        except (json.JSONDecodeError, ValueError, TypeError) as exc:
            log.warning("hierarchical_render_parse_fallback", error=str(exc))
            raw_sections = [
                {
                    "title": required_sections[0] if required_sections else "System Purpose",
                    "content": raw_content,
                    "summary": "LLM output could not be parsed into structured sections.",
                    "confidence": "low",
                    "inferred": True,
                    "evidence": [],
                }
            ]

        sections: list[CliffNotesSection] = []
        seen_titles: set[str] = set()
        for index, raw in enumerate(raw_sections):
            fallback_title = required_sections[index] if index < len(required_sections) else f"Section {index + 1}"
            normalized = coerce_section(raw, fallback_title=fallback_title, title_summary_max_chars=TITLE_SUMMARY_MAX_CHARS)
            title = str(normalized.get("title", fallback_title))
            if title not in required_sections:
                continue
            evidence_raw = normalized.get("evidence", [])
            if not isinstance(evidence_raw, list):
                evidence_raw = []
            seen_titles.add(title)
            sections.append(
                CliffNotesSection(
                    title=title,
                    content=str(normalized.get("content", "")),
                    summary=str(normalized.get("summary", "")),
                    confidence=str(normalized.get("confidence", "medium")),
                    inferred=bool(normalized.get("inferred", False)),
                    evidence=parse_evidence(evidence_raw),
                )
            )

        for req_title in required_sections:
            if req_title not in seen_titles:
                sections.append(
                    CliffNotesSection(
                        title=req_title,
                        content="*Insufficient data to generate this section.*",
                        summary="Not enough information available.",
                        confidence="low",
                        inferred=True,
                    )
                )

        if apply_deep_quality_gates:
            for section in sections:
                minimum = DEEP_MIN_EVIDENCE.get(section.title, 3)
                gate = evaluate_evidence_gate(
                    text=f"{section.summary}\n{section.content}",
                    evidence=extract_section_evidence_refs(section.evidence),
                    minimum=minimum,
                    evidence_store_text=evidence_store_text,
                )
                if gate.unsupported_claim_terms:
                    section.content = strip_unsupported_claim_sentences(section.content, gate.unsupported_claim_terms)
                    section.summary = strip_unsupported_claim_sentences(section.summary, gate.unsupported_claim_terms)
                    section.confidence = "low"
                    section.inferred = True
                    section.refinement_status = "unsupported_claims"
                    continue
                cleaned_content = strip_speculative_sentences(section.content)
                cleaned_summary = strip_speculative_sentences(section.summary)
                if cleaned_content != section.content or cleaned_summary != section.summary:
                    section.content = cleaned_content
                    section.summary = cleaned_summary
                    section.confidence = "low" if section.confidence == "high" else section.confidence
                    section.inferred = True
                    section.refinement_status = "unsupported_claims"
                if gate.below_threshold or gate.forbidden_phrases:
                    if gate.forbidden_phrases:
                        section.content = strip_forbidden_phrase_sentences(section.content, gate.forbidden_phrases)
                        section.summary = strip_forbidden_phrase_sentences(section.summary, gate.forbidden_phrases)
                    section.confidence = "low"
                    section.refinement_status = "needs_evidence"
                weakness_phrases = find_section_weakness_phrases(
                    section.title,
                    f"{section.summary}\n{section.content}",
                )
                if weakness_phrases:
                    section.content = strip_forbidden_phrase_sentences(section.content, weakness_phrases)
                    section.summary = strip_forbidden_phrase_sentences(section.summary, weakness_phrases)
                    section.confidence = "low"
                    section.inferred = True
                    section.refinement_status = "needs_evidence"

            _enforce_deep_confidence_floor(sections)

        return sections

    def _normalize_repository_opening_sections(
        self,
        sections: list[CliffNotesSection],
        *,
        repository_name: str,
        all_group_nodes: list[SummaryNode],
        all_file_nodes: list[SummaryNode],
    ) -> list[CliffNotesSection]:
        repo_label = _display_repository_name(repository_name)
        paths = {
            (_node_label(node) or "").lower()
            for node in [*all_group_nodes, *all_file_nodes]
        }
        has_web = any(path.startswith("web/") for path in paths)
        has_graphql = any("graphql" in path for path in paths)
        has_rest = any(path.startswith("internal/api/rest/") for path in paths)
        has_workers = any(path.startswith("workers/") for path in paths)
        has_cli = any(path.startswith("cli/") or path.startswith("cmd/") for path in paths)
        has_persistence = any(path.startswith("internal/db/") or "/store" in path for path in paths)

        system_surfaces: list[str] = []
        if has_web:
            system_surfaces.append("web product")
        if has_graphql and has_rest:
            system_surfaces.append("GraphQL and REST entrypoints")
        elif has_graphql:
            system_surfaces.append("GraphQL entrypoints")
        elif has_rest:
            system_surfaces.append("API entrypoints")
        if has_workers:
            system_surfaces.append("background workers")
        if has_cli:
            system_surfaces.append("CLI workflows")

        architecture_surfaces = list(system_surfaces)
        if has_persistence:
            insert_at = 3 if len(architecture_surfaces) >= 3 else len(architecture_surfaces)
            architecture_surfaces.insert(insert_at, "persistence-backed stores")

        system_lead = (
            f"{repo_label} builds repository understanding and generated knowledge artifacts such as reports, "
            f"diagrams, and review outputs."
        )
        if system_surfaces:
            system_lead += f" It exposes that work through {_join_surface_labels(system_surfaces)}."

        architecture_lead = f"{repo_label}'s architecture combines {_join_surface_labels(architecture_surfaces or ['background processing surfaces'])}."

        by_title = {section.title: section for section in sections}
        system = by_title.get("System Purpose")
        if system:
            system.content = _replace_opening_sentence(system.content, system_lead)
            system.summary = system_lead
        architecture = by_title.get("Architecture Overview")
        if architecture:
            architecture.content = _replace_opening_sentence(architecture.content, architecture_lead)
            architecture.summary = architecture_lead
        return sections


def _replace_opening_sentence(content: str, replacement: str) -> str:
    body = (content or "").strip()
    if not body:
        return replacement
    pieces = re.split(r"(?<=[.!?])\s+", body, maxsplit=1)
    if len(pieces) == 1:
        return replacement
    return f"{replacement} {pieces[1].strip()}"


def _display_repository_name(repository_name: str) -> str:
    name = (repository_name or "This repository").strip()
    name = re.sub(r"-deterministic-v\d+$", "", name, flags=re.IGNORECASE)
    name = re.sub(r"-v\d+$", "", name, flags=re.IGNORECASE)
    if name.lower() == "sourcebridge":
        return "SourceBridge"
    return name or "This repository"


def _join_surface_labels(labels: list[str]) -> str:
    if not labels:
        return "its available surfaces"
    if len(labels) == 1:
        return labels[0]
    if len(labels) == 2:
        return f"{labels[0]} and {labels[1]}"
    return ", ".join(labels[:-1]) + f", and {labels[-1]}"


def _first_line(text: str) -> str:
    for line in (text or "").splitlines():
        line = line.strip()
        if line:
            return line[:140]
    return ""


def _truncate_body(text: str, *, max_chars: int) -> str:
    content = " ".join((text or "").split())
    if len(content) <= max_chars:
        return content
    return content[: max_chars - 3].rstrip() + "..."


def _node_label(node: SummaryNode) -> str:
    meta = node.metadata or {}
    return (
        str(meta.get("file_path")) or str(meta.get("module_label")) or str(meta.get("repository_name")) or node.unit_id
    )


def _metadata_values(metadata: dict[str, object], *keys: str) -> list[str]:
    values: list[str] = []
    for key in keys:
        raw = metadata.get(key)
        if isinstance(raw, list):
            for item in raw:
                text = str(item).strip().lower()
                if text:
                    values.append(text)
        elif raw:
            text = str(raw).strip().lower()
            if text:
                values.append(text)
    deduped: list[str] = []
    seen: set[str] = set()
    for value in values:
        if value in seen:
            continue
        seen.add(value)
        deduped.append(value)
    return deduped


def _root_fact_orientation(root: SummaryNode) -> str:
    metadata = root.metadata or {}
    signals = _metadata_values(metadata, "fact_root_signals")
    entities = _metadata_values(metadata, "fact_root_entities")
    roles = _metadata_values(metadata, "fact_root_roles")
    dependencies = _metadata_values(metadata, "fact_external_dependencies")
    key_files = _metadata_values(metadata, "fact_key_files")
    lines: list[str] = []
    if signals:
        lines.append(f"Structured repository signals: {', '.join(signals[:5])}")
    if entities:
        lines.append(f"Structured repository entities: {', '.join(entities[:6])}")
    if roles:
        lines.append(f"Structured repository roles: {', '.join(roles[:5])}")
    if dependencies:
        lines.append(f"Structured external dependency hints: {', '.join(dependencies[:6])}")
    if key_files:
        lines.append(f"Structured key files: {', '.join(key_files[:6])}")
    if not lines:
        return ""
    return "\n".join(lines)


_SPECIFIC_IDENTIFIER_RE = re.compile(r"`([A-Za-z_][A-Za-z0-9_]{2,})`")
_CONFIDENCE_ENFORCE_MIN_UNIQUE_FILES = 3
# Ops-heavy sections (External Dependencies, Security Model, Complexity & Risk Areas)
# lean on file references more than named identifiers. Requiring only two keeps
# well-grounded sections from being downgraded while still ruling out sections
# that barely name any specific code element.
_CONFIDENCE_ENFORCE_MIN_IDENTIFIERS = 2


def _enforce_deep_confidence_floor(sections: list[CliffNotesSection]) -> None:
    """Override LLM self-reported confidence when mechanical criteria are met.

    The DEEP render prompt asks for "high" whenever a section cites multiple
    real file paths and names multiple specific identifiers. Flash-class
    models routinely understate and return "low" even when those criteria
    hold. On top of that the earlier quality gate flags ``refinement_status``
    aggressively — stripping a single "may"/"likely" sentence is enough to
    mark a section as "unsupported_claims" even when the surviving content
    is well-grounded. So enforcement evaluates the post-strip text directly:
    if the section still cites enough real files and names enough
    identifiers, we upgrade. The gate's strip already removed the risky
    prose.
    """

    for section in sections:
        if (section.confidence or "").lower() == "high":
            continue
        unique_paths = {
            ev.file_path.strip()
            for ev in (section.evidence or [])
            if ev.file_path and ev.file_path.strip()
        }
        if len(unique_paths) < _CONFIDENCE_ENFORCE_MIN_UNIQUE_FILES:
            continue
        specific_ids = {
            match.group(1)
            for match in _SPECIFIC_IDENTIFIER_RE.finditer(section.content or "")
        }
        if len(specific_ids) < _CONFIDENCE_ENFORCE_MIN_IDENTIFIERS:
            log.info(
                "deep_confidence_floor_skipped",
                section_title=section.title,
                reason="too_few_identifiers",
                unique_paths=len(unique_paths),
                identifier_count=len(specific_ids),
            )
            continue
        log.info(
            "deep_confidence_floor_applied",
            section_title=section.title,
            from_confidence=section.confidence,
            from_refinement_status=section.refinement_status,
            unique_paths=len(unique_paths),
            identifier_count=len(specific_ids),
        )
        section.confidence = "high"
        section.inferred = False
        # Clear the refinement flag: we've mechanically verified the section
        # is grounded. Leaving "unsupported_claims" or "needs_evidence" on a
        # HIGH-confidence section would be inconsistent and make downstream
        # deepening pick up work that no longer applies.
        section.refinement_status = ""


def _case_preserving_metadata_values(metadata: dict[str, object], key: str) -> list[str]:
    """Read a list-valued metadata key without lowercasing.

    ``_metadata_values`` lowercases everything, which is correct for
    token-like signals (``api``, ``store``, ``auth``) but destroys the
    case of symbol identifiers (``KnowledgeServicer``, ``SaveArtifact``).
    """

    raw = metadata.get(key)
    if not raw:
        return []
    if isinstance(raw, list):
        items = [str(item).strip() for item in raw]
    else:
        items = [str(raw).strip()]
    ordered: list[str] = []
    seen: set[str] = set()
    for item in items:
        if not item or item in seen:
            continue
        seen.add(item)
        ordered.append(item)
    return ordered


def _file_with_top_symbols(node: SummaryNode, *, max_symbols: int = 3) -> str:
    """Format a file node as "path (Sym1, Sym2, Sym3)" for per-section plan lines.

    Per-section routing through the section evidence plan is stronger when
    the LLM sees concrete symbols ranked for that specific section, not
    just a file path. Symbol identifiers are case-preserved.
    """

    label = _node_label(node)
    if not label:
        return ""
    metadata = node.metadata or {}
    symbols = _case_preserving_metadata_values(metadata, "fact_symbol_names")[:max_symbols]
    if not symbols:
        return label
    return f"{label} ({', '.join(symbols)})"


def _format_selected_fact_inventory(
    group_nodes: list[SummaryNode],
    file_nodes: list[SummaryNode],
    *,
    max_files: int = 8,
) -> str:
    """Aggregate group-level typed facts for middle-section guardrails.

    Per-file symbol listings live in the section evidence plan (see
    ``_file_with_top_symbols``) because each section routes to a
    different top-ranked file set. This block is the group-level
    summary: role distribution and external dependency aggregate. Kept
    short so the section-level plan stays the primary routing signal.
    """

    role_counts: dict[str, int] = {}
    for node in file_nodes[:max_files]:
        metadata = node.metadata or {}
        for role in _metadata_values(metadata, "fact_roles", "fact_package_roles"):
            role_counts[role] = role_counts.get(role, 0) + 1
    for node in group_nodes:
        metadata = node.metadata or {}
        for role in _metadata_values(metadata, "fact_package_roles"):
            role_counts[role] = role_counts.get(role, 0) + 1
    role_lines: list[str] = []
    for role, count in sorted(role_counts.items(), key=lambda pair: (-pair[1], pair[0]))[:4]:
        role_lines.append(f"- {role} ({count} occurrence{'s' if count != 1 else ''})")

    dependency_set: list[str] = []
    seen_deps: set[str] = set()
    for node in [*file_nodes[:max_files], *group_nodes]:
        metadata = node.metadata or {}
        for dep in _metadata_values(metadata, "fact_external_dependencies"):
            if dep in seen_deps:
                continue
            seen_deps.add(dep)
            dependency_set.append(dep)
        if len(dependency_set) >= 8:
            break

    sections: list[str] = []
    if role_lines:
        sections.append("Role distribution across selected evidence:\n" + "\n".join(role_lines))
    if dependency_set:
        sections.append(
            "External dependencies seen in selected evidence: "
            + ", ".join(dependency_set[:8])
        )
    if not sections:
        return ""
    return "\n\n".join(sections)


def _node_area(node: SummaryNode) -> str:
    label = _node_label(node).strip().lstrip("/")
    if not label:
        return ""
    if label.startswith("web/src/"):
        return "web"
    if label.startswith("internal/api/"):
        return "internal_api"
    if label.startswith("internal/"):
        return "internal"
    if label.startswith("workers/"):
        return "workers"
    if label.startswith("cli/"):
        return "cli"
    head = label.split("/", 1)[0]
    return head
