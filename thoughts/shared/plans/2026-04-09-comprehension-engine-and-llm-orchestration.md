# Corpus-Agnostic Comprehension Engine & LLM Orchestration Plan

**Date:** 2026-04-09
**Author:** Jay Stuart (drafted with Claude)
**Status:** Proposed
**Scope:** Go API (`internal/`, `cmd/`) · Python worker (`workers/`) · Web UI (`web/`) · Proto (`proto/`) · Settings / DB / config
**Supersedes:** [`2026-04-09-cliff-notes-generation-ux-reliability.md`](./2026-04-09-cliff-notes-generation-ux-reliability.md)

## Overview

SourceBridge currently generates whole-corpus artifacts (cliff notes, learning paths, code tours, workflow stories) by stuffing a serialized knowledge snapshot into a single LLM call. On any real-world repository this exceeds the model's context window and the pipeline silently fails — especially against the local Ollama deployment on `thor`, which is what prompted this plan. The current system has no concurrency control, no progress streaming, no operator-facing monitor, no retry policy, and no model-capability awareness.

This plan rewrites the generation path as a **comprehension engine** with a single production contract and multiple optional optimization paths. **Hierarchical Summarization is the guaranteed baseline**: it must work on very large corpora and weaker models. **RAPTOR, GraphRAG, and Long-Context Direct are progressive enhancements** that are introduced only after the baseline is proven and only graduate into automatic selection if benchmarking shows they materially outperform the baseline for specific artifact types, corpus sizes, and model classes.

The work builds on top of a generic `LLMOrchestrator` layer (bounded queue, retry policy, scope-aware timeouts, streaming progress, in-flight deduplication, error persistence) that all LLM-using subsystems share — not just knowledge artifact generation, but also reasoning, requirements extraction, and linking. The end result is a single operational surface for every LLM call the system makes, with a live Monitor page showing what's running, what's queued, and what has failed.

## Guiding principles

1. **Global sensemaking, not selective retrieval.** Artifacts are whole-corpus outputs. Every piece of source contributes. This rules out classic RAG as the primary strategy; it is kept only as an interactive drill-down path.
2. **Designed for extension, proven on code first.** The engine should support future corpus types, but the code path must be validated before broader abstractions are allowed to drive the design.
3. **One production strategy, several enhancement strategies.** Hierarchical is the compatibility contract. Additional strategies are introduced behind flags and must beat the baseline before becoming part of automatic selection.
4. **Model-capability-aware.** The system knows what each model can do and warns (or refuses) when a selected model is incompatible with the chosen strategy.
5. **Dynamic configuration, sensible defaults.** Every knob is settable in settings UI; the out-of-the-box experience still works without any configuration.
6. **Fail loudly, retry sparingly, degrade gracefully.** Every failure is visible and actionable. Advanced strategies fall back to the baseline when they are not viable or do not justify their added cost.
7. **Operator observability is first-class.** A live Monitor page is part of v1, not a follow-up.
8. **If it isn't intuitive, it won't be used.** Every operator surface — Monitor, Settings, error messages, empty states — is designed for a user who has never read the docs and doesn't want to. Power-user depth is available but always behind progressive disclosure.
9. **Evidence gates architecture.** Each phase must prove or falsify the next architectural bet through benchmarked quality, latency, and reliability measurements before the next phase expands scope.

## UI/UX principles (applies to every operator-facing surface)

This plan exposes a lot of machinery to operators: comprehension strategies, model capabilities, fallback chains, token budgets, retry policies, inheritance hierarchies, live job queues. If these surfaces feel like an enterprise admin console, they will be abandoned and people will stay on the broken default path. Every screen in this plan is held to the following rules:

1. **One-sentence "what is this" at the top of every page.** No operator should have to read documentation to understand what a page is for. Example for the Monitor page: *"Live view of every AI job SourceBridge is running — what's working, what's queued, what failed."*
2. **Smart defaults that just work.** The out-of-box experience requires zero configuration. A new workspace ships with `["hierarchical", "single_shot"]` as the strategy chain, sane budgets, and the currently-configured model — and the first artifact generation succeeds without anyone touching settings.
3. **Progressive disclosure.** The first thing a user sees is the **10% they'll touch 90% of the time**. "Advanced" knobs are one click away, not on the main screen. The settings page has a **Simple** mode by default and an **Advanced** toggle; Simple mode hides everything most users don't need.
4. **Recommend, don't just allow.** When the user picks a model, the system immediately tells them which strategies will work well, which will work in degraded mode, and which will fail. A prominent "Recommended setup" card shows a single "Use recommended settings" button.
5. **Visual first, text second.** Status is a color + a shape + a word, not a paragraph. Green circle = healthy, yellow triangle = degraded, red octagon = broken. Operators scan, not read.
6. **Show, don't tell.** Before saving a settings change, show a preview of what the effective configuration will look like. Before running GraphRAG on a new corpus, show an estimated cost and time. Surprises are user-hostile.
7. **Inline contextual help, not a manual.** Every non-obvious field has a "?" icon that opens a tiny tooltip with one sentence of explanation and one link for more. Nothing requires leaving the page to understand.
8. **Safe to experiment.** Every settings change has an "Undo" affordance that lasts 30 seconds after save. Every scope has a "Reset to defaults" button. Every destructive operation has a confirmation step that tells the user exactly what will happen. Operators should feel free to poke.
9. **Empty states that teach.** The first time an operator opens the Monitor with no jobs running, they see a friendly "No AI jobs are running right now. The next time you generate cliff notes, you'll see the job here — click it to watch progress live." — not a blank table.
10. **Errors are a UX surface, not a log entry.** Every error code maps to a human-readable title, a one-sentence explanation, a suggested fix, and optionally a one-click remediation button. `LLM_EMPTY` is not "`LLM returned empty content`" — it's "**Your model returned nothing.** The repository may be too large for this model's context window. [Switch to hierarchical strategy →] [Use a larger-context model →]".
11. **Live feedback beats "click save and wait."** Capability badges re-evaluate instantly as the user changes a setting. Token budgets show a live estimate as the operator drags a slider. The Monitor page updates without a refresh.
12. **The happy path takes three clicks max.** Any core operator workflow — "generate cliff notes for this repo," "see why that job failed," "switch to a bigger model" — should be reachable in ≤3 clicks from the dashboard.

## Delivery model

This plan is intended to ship as a sequence of **tagged, testable phases**. After each phase:

1. The current system is benchmarked first to establish a comparison baseline.
2. The phase is implemented in reviewable check-ins.
3. Benchmarks and acceptance tests are rerun after the phase.
4. The branch is committed and pushed when the change set reaches a coherent, reviewable checkpoint.
5. A git tag is cut for the initial baseline and again at the end of the phase for rollback and comparison.
6. The next phase proceeds only if the acceptance criteria show that the preceding phase's assumptions were correct.

This matters because rollback reduces deployment risk but does not reduce design risk. Each phase must therefore answer a concrete architecture question:

- **Can we make LLM execution bounded, visible, and failure-explicit?**
- **Can hierarchical summarization deliver acceptable quality on large repos with weak models?**
- **Can persisted intermediate structure materially improve repeat generation cost/latency?**
- **Do advanced strategies beat the baseline enough to justify their complexity?**
- **Does broader corpus support pay for its abstraction cost?**

If a phase fails to demonstrate clear value, the plan stops there and the previous tagged phase remains the product path.

### Source control and benchmark discipline

Before Phase A begins:

1. Run the benchmark suite against the existing system.
2. Capture the outputs as the **baseline benchmark report**.
3. Commit any benchmark harness changes needed to make the run repeatable.
4. Tag the repository at the starting point, for example `comprehension-baseline-pre-phase-a`.
5. Push the branch and the tag so the baseline is preserved externally.

For every later phase:

1. Run the pre-phase benchmark on the current tagged system.
2. Implement the phase in one or more coherent commits.
3. Push whenever the branch reaches a reviewable or recoverable checkpoint.
4. Rerun the same benchmark suite after the phase.
5. Write a short before/after phase report with benchmark deltas.
6. Tag the result, for example `comprehension-phase-a`, `comprehension-phase-b`, and so on.
7. Push commits and tags before starting the next phase.

The important rule is that no phase advances without a reproducible before/after benchmark comparison against the previous tag.

### OSS release discipline

This work must stay compatible with the eventual OSS release shape of the repository. That means phase commits and tags are not just rollback points; they are also candidates for public release checkpoints.

Rules:

1. **Only commit OSS-safe artifacts.**
   - Benchmark harness code, manifests, fixture-based results, docs, and make targets can be committed.
   - Internal-only corpus data, private repo outputs, secrets, hostnames, access tokens, customer identifiers, and environment-specific logs must not be committed.

2. **Prefer public fixtures over private benchmark data.**
   - The canonical committed benchmark suite should run on in-repo fixtures and OSS-safe corpora.
   - Real-provider runs against private repos such as `MACU Helpdesk` can inform phase decisions, but their raw outputs should stay outside the public tree unless sanitized.

3. **Separate committed benchmark artifacts from private benchmark artifacts.**
   - Committed reports should contain aggregate metrics, pass/fail status, and sanitized observations.
   - Any private detailed outputs should live in an ignored local path or external internal storage, not under version control.

4. **Every phase tag should be releaseable.**
   - Before cutting a phase tag, verify that the tree contains only content that could appear in the OSS release.
   - If a phase depends on internal-only evidence, summarize that evidence in sanitized form before tagging.

5. **Commit and push at OSS-appropriate checkpoints.**
   - Push benchmark harness, tests, docs, and implementation in coherent increments that make sense in the public history.
   - Do not intermingle experimental private notes or one-off operational artifacts with the phase commits intended for release.

## Current State Analysis

> The live-log root cause analysis is captured in full in the superseded plan ([`2026-04-09-cliff-notes-generation-ux-reliability.md`](./2026-04-09-cliff-notes-generation-ux-reliability.md) §Current State Analysis). The findings are reproduced here in condensed form.

### Confirmed root causes (from thor logs, 2026-04-09)

- **LLM returns empty content silently.** The worker runs Ollama. Repository-level snapshots exceed Ollama's configured `num_ctx` and the provider returns an empty string. `workers/common/llm/openai_compat.py:98` coerces the empty response to `""`. `workers/knowledge/cliff_notes.py:232-246` falls through to a fabricated stub that the pipeline marks **READY**. End users see stub content labeled "success."
- **600s worker timeout is being hit.** `internal/worker/client.go:36` (`TimeoutKnowledge = 600s`) fires under Ollama saturation.
- **Unbounded concurrent goroutines.** `internal/api/graphql/schema.resolvers.go:1400` spawns a new goroutine per mutation with no queue, no semaphore, and no in-flight deduplication. Logs show three calls against the same artifact within three seconds.
- **Workflow story `NoneType` crash.** `workers/knowledge/workflow_story.py` helper path fails with `'NoneType' object is not subscriptable` repeatedly.
- **Progress bar stutters.** Only three progress values are emitted (`0.1`, `0.8`, `1.0`) and the frontend polls every 5 seconds, so the bar sits at 10% for most of the LLM call.

### LLM-using subsystems in the worker (full inventory)

A grep across `workers/` identified every LLM callsite. The new orchestrator must govern **all** of these, not just knowledge artifact generation:

| Subsystem | Files | Purpose |
|---|---|---|
| Knowledge | `workers/knowledge/cliff_notes.py`, `learning_path.py`, `code_tour.py`, `workflow_story.py`, `explain_system.py`, `servicer.py` | Whole-corpus artifact generation |
| Reasoning | `workers/reasoning/reviewer.py`, `discussion.py`, `summarizer.py`, `explainer.py`, `servicer.py` | Q&A, code review, discussion, explanation |
| Requirements | `workers/requirements/spec_extraction.py`, `servicer.py` | Extracting structured requirements from text |
| Linking | `workers/linking/servicer.py` | Code ↔ requirements linking |
| LLM plumbing | `workers/common/llm/openai_compat.py`, `anthropic.py`, `router.py`, `factory.py`, `fake.py`, `provider.py`, `config.py` | Shared provider abstraction |

All of these currently bypass the orchestration this plan introduces. None of them persist errors, stream progress, or participate in concurrency control today.

### Data model (current)

- `ca_knowledge_artifact` (SurrealDB): `id`, `repo_id`, `type`, `audience`, `depth`, `scope_type`, `scope_path`, `scope_key`, `status`, `progress`, `source_revision_*`, `stale`, `generated_at`, `created_at`, `updated_at`. No `error_message`, no `error_code`, no `retry_count`, no `progress_phase`, no strategy metadata, no model metadata.
- `ca_knowledge_section`, `ca_knowledge_evidence` — section/evidence tables keyed to artifact id.
- No table for LLM jobs broader than knowledge artifacts (reasoning/requirements/linking run in-memory).
- No table for model capabilities, no table for strategy configuration.

### Why we can't stay where we are

1. Every current failure mode is silent or misleading (empty content → stub; timeout → no detail; crash → no error surfaced).
2. A single Ollama saturation event cascades into duplicate goroutines and timeouts across the whole system, not just the failing artifact.
3. Reasoning, requirements, and linking share the same fragile provider layer and will eventually exhibit the same failures under load.
4. There is no path to support cloud models alongside Ollama without a capability registry, because operators cannot tell which models are viable for which strategies.
5. Generating artifacts by stuffing a snapshot into one call cannot scale to any non-trivial corpus. This is an architectural dead end.

## Desired End State

### User experience
- **An operator who has never read the docs can pick a model and have a working setup in under 60 seconds** — open settings, pick a model from the dropdown, click "Use recommended setup," done. No reading, no decisions about strategies, no capability matrix.
- Progress bars move smoothly and show a phase label ("Building summary tree level 2", "Generating cliff notes from root summary"). A user watching a generation never wonders if the system is stuck.
- Failures show an explicit, human-readable title, an actionable remediation, and a one-click fix button when possible — never a silent stub, never a raw error code, never "Refresh failed."
- Users pick an artifact type and depth and get a high-quality result regardless of corpus size, because the engine decomposes large inputs automatically. They never need to understand the difference between hierarchical summarization and GraphRAG to use the product.
- Power users and operators can change the comprehension strategy, the model, the concurrency limits, the caching behavior, the fallback chain, and the token budgets — all from the settings UI, without redeploying — but these knobs are behind an Advanced toggle and don't clutter the simple path.
- Every settings change is reversible with one click for 30 seconds after save. Every scope can be reset to defaults. Operators feel safe to experiment.
- The Monitor page tells an operator at a glance — in green/yellow/red — whether the system is healthy, and if it isn't, exactly what to do about it.

### Operational behavior
- A single Monitor page shows every in-flight LLM call across every subsystem — knowledge, reasoning, requirements, linking — with live progress, elapsed time, queue depth, and recent failures.
- All LLM work flows through a bounded queue with configurable concurrency (default 3). Excess work queues as `PENDING`; duplicate work for the same target deduplicates.
- Transient failures retry once with backoff. Non-retryable failures fail fast with structured error codes.
- Per-scope timeouts prevent a single runaway call from holding a queue slot for 10 minutes.
- All errors persist with full messages and codes on the relevant job record.

### Comprehension quality
- Repository-level cliff notes for `MACU Helpdesk` on thor succeed on the existing Ollama instance without any model upgrade, because the hierarchical strategy never sends a single prompt larger than the configured leaf budget.
- When a cloud model is available (Claude, Gemini), the system can optionally use GraphRAG for global sensemaking queries and long-context direct for smaller corpora, picking the best strategy per request.
- Generating all four artifact types for the same repo shares the underlying summary tree so successive generations are cheap.
- Requirements documents and standalone docs can be comprehended by the same engine with different corpus adapters — no code duplication.

### Model awareness
- Every model used by SourceBridge has a declared capability profile: context window, instruction-following grade, JSON mode support, tool-use support, extraction grade (for GraphRAG entity extraction), creative grade (for long-form narrative).
- When an operator selects a strategy/model combination, the settings UI **warns or refuses** based on capability requirements. Example: selecting GraphRAG with Ollama's `llama3:4096-ctx` shows "This model has only 4K context and medium instruction-following. GraphRAG entity extraction requires ≥16K context and high instruction-following. Recommended models: `claude-sonnet-4-6`, `gpt-4.1`, or `llama3.3:70b-32k`. You can still select it, but generations may fail."
- A probe endpoint tests unknown models at configuration time and populates the registry automatically.

### Verification
- `MACU Helpdesk` repo-level cliff notes succeed end-to-end on thor with current Ollama.
- Firing 10 concurrent artifact generations results in at most 3 in-flight LLM calls; the rest queue as `PENDING`.
- Selecting GraphRAG in settings while the current model is a 4K-context Ollama shows a capability warning and lets the user either change strategy or change model from the same dialog.
- The Monitor page shows a reasoning request, a requirements extraction, and a knowledge generation simultaneously in the same activity feed.
- A regression test generates cliff notes for a synthetic 2M-token corpus using the hierarchical strategy and completes successfully.

## What We're NOT Doing

- **Switching LLM providers.** Ollama stays as the default local provider. Cloud providers (Anthropic, OpenAI-compatible) are added as optional alternatives, not replacements.
- **Building a general-purpose task queue.** The orchestrator is scoped to LLM jobs. Non-LLM work stays where it is.
- **Fine-tuning models.** No Code Graph Model-style fine-tuning. We use off-the-shelf models and pick strategies that match their capabilities.
- **Auto-scaling worker pods.** Horizontal scaling of the Python worker is out of scope; the orchestrator protects a single-worker deployment.
- **Per-user rate limiting or fair queuing.** Fairness between users is a future concern.
- **Rewriting the indexing infrastructure.** Existing graph store and snapshot assembly stay. They become inputs to the new engine, not replacements for it.
- **Sunsetting the current single-shot path immediately.** It remains available as a legacy strategy during the migration and can be re-enabled per-artifact if operators prefer it for small corpora.

## Architecture

This section describes the **target state architecture**. It is intentionally broader than the immediate implementation scope. Phase A and Phase B implement only the subset required for bounded execution, observability, and the hierarchical code baseline. Nothing in this section should be read as authorization to front-load abstractions that are explicitly deferred by the phase plan.

### System diagram (logical)

```
┌─────────────────────────────────────────────────────────────────────┐
│                           GraphQL / REST                           │
│  generateArtifact  ·  monitor  ·  settings  ·  capability probe    │
└───────────────────────────┬─────────────────────────────────────────┘
                            │
┌───────────────────────────┴─────────────────────────────────────────┐
│                        LLMOrchestrator (Go)                         │
│  ┌─────────┐  ┌──────────────┐  ┌────────────┐  ┌──────────────┐  │
│  │ Queue   │  │ Retry Policy │  │ Timeouts   │  │ Dedupe       │  │
│  └─────────┘  └──────────────┘  └────────────┘  └──────────────┘  │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │     Provider-call concurrency governor (actual bottleneck)  │   │
│  └─────────────────────────────────────────────────────────────┘   │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │            LLMJob persistence (ca_llm_job table)            │   │
│  └─────────────────────────────────────────────────────────────┘   │
└───────────────────────────┬─────────────────────────────────────────┘
                            │
┌───────────────────────────┴─────────────────────────────────────────┐
│                   Comprehension Engine (Python worker)              │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │                      Strategy Selector                       │   │
│  │  (reads settings, checks model capability, picks strategy)   │   │
│  └──────────────────────────────────────────────────────────────┘   │
│  ┌──────────┐ ┌──────────┐ ┌──────────────┐ ┌──────────────────┐   │
│  │ Hierarch │ │ RAPTOR   │ │ GraphRAG     │ │ Long-Context     │   │
│  │ Strategy │ │ Strategy │ │ Strategy     │ │ Direct Strategy  │   │
│  └──────────┘ └──────────┘ └──────────────┘ └──────────────────┘   │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │                   SummaryTree store                          │   │
│  │    (shared by hierarchical + RAPTOR; persisted, incremental) │   │
│  └──────────────────────────────────────────────────────────────┘   │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │                    Knowledge Graph store                     │   │
│  │          (entities, relations, community summaries)          │   │
│  └──────────────────────────────────────────────────────────────┘   │
└───────────────────────────┬─────────────────────────────────────────┘
                            │
┌───────────────────────────┴─────────────────────────────────────────┐
│                         CorpusSource adapters                       │
│  CodeCorpus · RequirementsCorpus · DocumentCorpus · (future)        │
└───────────────────────────┬─────────────────────────────────────────┘
                            │
┌───────────────────────────┴─────────────────────────────────────────┐
│                          LLM Provider Layer                         │
│  Ollama · Anthropic · OpenAI-compatible · Fake                      │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │                 Model Capability Registry                    │   │
│  │    (context, instr-grade, json, tool-use, extraction grade)  │   │
│  └──────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────┘
```

### Core abstractions

**Phase scope note:** in Phase A and Phase B, only the abstractions required for code corpora and the hierarchical baseline should be implemented. Multi-corpus adapters, advanced strategy interfaces, and richer capability semantics remain target-state concepts until later phase gates justify them.

#### `CorpusSource` (Python, worker)

A corpus-agnostic representation of "a body of text with a hierarchy and metadata."

```python
class CorpusSource(Protocol):
    corpus_id: str
    corpus_type: str  # "code", "requirements", "document", ...

    def root_units(self) -> Iterable[CorpusUnit]: ...
    def children(self, unit: CorpusUnit) -> Iterable[CorpusUnit]: ...
    def leaf_content(self, unit: CorpusUnit) -> str: ...
    def metadata(self, unit: CorpusUnit) -> dict[str, Any]: ...
    def revision_fingerprint(self) -> str: ...  # for incremental invalidation

@dataclass
class CorpusUnit:
    id: str
    kind: str                  # "repo"|"package"|"file"|"segment"  or  "doc"|"section"|"paragraph"
    parent_id: str | None
    label: str                 # human-readable
    size_tokens: int           # pre-computed for budget decisions
```

Adapters (ship in v1):
- **`CodeCorpus`** — wraps the existing graph/snapshot store. Units: `repo → package → file → segment (function/class)`. Leaf content = raw source for segments plus doc comments.
- **`RequirementsCorpus`** — wraps the existing requirements model. Units: `document → section → requirement`. Leaf content = requirement text plus linked evidence.
- **`DocumentCorpus`** — wraps ingested markdown / plain text docs (Wiki.js imports, README files, uploaded files). Units: `collection → document → section → paragraph`.

Future adapters (not in v1 but the interface supports them): legal contracts, meeting transcripts, chat histories, customer support tickets.

#### `SummaryTree` (Python, worker + persistence)

A persisted tree where each node stores a summary of its children plus metadata.

```python
@dataclass
class SummaryNode:
    id: str
    corpus_id: str
    unit_id: str
    level: int                       # 0 = leaf, N = root
    parent_id: str | None
    child_ids: list[str]
    summary_text: str
    summary_tokens: int
    source_tokens: int               # total tokens below this node
    model_used: str
    strategy: str                    # "hierarchical"|"raptor"|"graphrag"
    revision_fp: str                 # for incremental invalidation
    generated_at: datetime
```

Persisted in a new SurrealDB table `ca_summary_node`. Shared across all strategies that need a tree (Hierarchical, RAPTOR). Incremental updates use a Merkle-style fingerprint (Cursor-style) — when a corpus unit's fingerprint changes, only its ancestor path is re-summarized, not the whole tree.

#### `ComprehensionStrategy` (Python, worker)

The strategy interface every comprehension technique implements.

```python
class ComprehensionStrategy(Protocol):
    name: str                    # "hierarchical" | "raptor" | "graphrag" | "long_context"

    def capability_requirements(self) -> ModelCapabilityRequirements:
        """Minimum model capability to run this strategy."""

    async def build_index(
        self,
        corpus: CorpusSource,
        progress: ProgressCallback,
    ) -> StrategyIndex:
        """Idempotent: builds or reuses persisted state (summary tree, graph, etc.)"""

    async def render_artifact(
        self,
        index: StrategyIndex,
        artifact_type: str,
        audience: str,
        depth: str,
        scope: ArtifactScope,
        progress: ProgressCallback,
    ) -> ArtifactResult:
        """Produces the final artifact from the persisted index."""
```

Four implementations ship in v1:

##### 1. `HierarchicalStrategy` (default, works with any model)

4-level bottom-up map-reduce tree:

- **Level 0 (leaf):** segment-sized chunks (function/class for code, paragraph for text). Token budget per leaf = 75% of the model's effective context.
- **Level 1:** file/document summaries produced from level-0 siblings. Each call summarizes one file's children.
- **Level 2:** package/section summaries. One call per package from its file summaries.
- **Level 3 (root):** corpus summary. One call from all package summaries.

Every LLM call is small enough for any model, including 4K-context Ollama. Coverage is mechanically guaranteed. A "refine" pass is available (configurable) that lets later sibling summaries see earlier ones to reduce cross-reference loss.

Rendering: artifact prompts consume some combination of root + level-2 summaries, with optional drill-down into level-1 summaries for specific scopes. Cliff notes read the root and top-N level-2 summaries; code tours read a targeted subtree; workflow stories read a subtree defined by an execution path.

**Capability requirements:** context ≥ 2048 tokens (every model), instruction-following grade ≥ low. This strategy is the floor — it must work on any model SourceBridge supports.

##### 2. `RAPTORStrategy` (optional, needs embeddings)

Same tree concept, but with soft clustering at each level:

- Embed each leaf with the configured embedding model.
- Gaussian Mixture Model clustering groups related leaves even across file/package boundaries.
- Summaries are generated per cluster, not per physical parent.
- The tree is queryable at multiple granularities — artifact rendering can pull the single "most relevant" cluster summary or aggregate across the tree.

**Capability requirements:** context ≥ 4096 tokens, embedding model available, instruction-following ≥ medium. Heavier upfront cost than Hierarchical; pays off when the same corpus serves many artifact types and many queries.

##### 3. `GraphRAGStrategy` (optional, needs capable model)

Microsoft GraphRAG pattern adapted for generic corpora:

- **Entity extraction pass:** iterate leaf units, extract typed entities and relationships via an LLM call per chunk. Types are corpus-specific (code: `Symbol`, `File`, `Module`, `Requirement`; requirements: `Actor`, `Goal`, `Constraint`, `System`; documents: configurable).
- **Graph assembly:** merge duplicate entities, link cross-document references.
- **Community detection:** Leiden clustering over the graph produces hierarchical communities.
- **Community summaries:** one summary per community at each level, stored in `ca_community_summary`.
- **Query-time map-reduce:** artifact prompts fan out across relevant community summaries in parallel, then reduce into the final artifact.

This is the most powerful strategy for "how does everything relate to everything else" queries — and the hungriest. The entity extraction step fails catastrophically on weak instruction-following models, which is why capability gating matters.

**Capability requirements:** context ≥ 8192 tokens, instruction-following ≥ high, JSON mode ≥ supported. The capability registry enforces this; if an operator selects GraphRAG with an incompatible model, settings refuse the combination unless "allow unsafe configuration" is explicitly enabled.

##### 4. `LongContextDirectStrategy` (fallback for small corpora + strong models)

Dumps the entire corpus into a single prompt. Only viable if:
- Total corpus tokens < configured threshold (default: 60% of model's declared effective window to leave headroom for output and prompt scaffolding).
- Model's effective window is large (≥32K).
- Model's "long-context quality" grade is not degraded at the target size (based on published benchmarks like LongCodeBench / context rot research — we encode known degradation curves in the registry).

Used as:
- **Primary** for small corpora where hierarchical setup overhead is wasteful.
- **Fallback** when a higher-tier strategy fails transiently and we want to retry with a different approach before surfacing a failure.

**Capability requirements:** context ≥ 32768 tokens, long-context quality grade ≥ medium at target token count.

##### Legacy: `SingleShotStrategy`

The current stuff-everything-into-one-call path is preserved as `SingleShotStrategy` behind a feature flag for backwards compatibility during rollout and for operators who want to force it for small corpora. It receives the same capability gating and the same error handling as the new strategies.

#### `ArtifactRenderer` (Python, worker)

Each artifact type is a thin renderer on top of a strategy's index.

```python
class ArtifactRenderer(Protocol):
    artifact_type: str           # "cliff_notes" | "learning_path" | "code_tour" | "workflow_story" | "requirements_summary" | ...

    def scope_policy(self, scope: ArtifactScope) -> RenderPolicy:
        """How much of the index to pull for this scope/depth."""

    async def render(
        self,
        strategy: ComprehensionStrategy,
        index: StrategyIndex,
        scope: ArtifactScope,
        policy: RenderPolicy,
        progress: ProgressCallback,
    ) -> ArtifactResult: ...
```

Renderers that ship in v1:

- **`CliffNotesRenderer`** — reads the root summary + top-N children from the strategy index; prompt-engineers the existing cliff notes section structure. Must work with the hierarchical baseline and can later support advanced strategies where they prove useful.
- **`LearningPathRenderer`** — reads the root summary + a traversal order over level-2 nodes ordered by dependency.
- **`CodeTourRenderer`** — reads a targeted subtree rooted at the requested scope.
- **`WorkflowStoryRenderer`** — reads a subtree defined by an execution path (existing input), falling back to the existing fallback helper when the path is empty.
- **`RequirementsSummaryRenderer`** — new artifact type for `RequirementsCorpus`; produces a structured summary of a requirements document or section.
- **`DocDigestRenderer`** — new artifact type for `DocumentCorpus`; produces a cliff-notes-equivalent for generic text documents.

#### `ModelCapabilityRegistry` (Python + Go, shared)

Per-model capability profiles and per-strategy requirements.

```python
@dataclass
class ModelCapabilities:
    model_id: str
    provider: str                          # "ollama" | "anthropic" | "openai_compat"
    declared_context_tokens: int           # what the provider reports
    effective_context_tokens: int          # what we've measured as reliable (≤ declared)
    long_context_quality: dict[int, str]   # {32000: "high", 128000: "medium", 256000: "low"}
    instruction_following: str             # "low" | "medium" | "high"
    json_mode: str                         # "none" | "prompted" | "native"
    tool_use: str                          # "none" | "supported" | "native"
    extraction_grade: str                  # grade for structured-extraction tasks
    creative_grade: str                    # grade for long-form narrative
    embedding_model: bool                  # true if this is an embedding model
    cost_per_1k_input: float | None
    cost_per_1k_output: float | None
    last_probed_at: datetime | None
    source: str                            # "builtin" | "probed" | "manual"
```

```python
@dataclass
class ModelCapabilityRequirements:
    min_context_tokens: int
    min_instruction_following: str    # "low" | "medium" | "high"
    requires_json_mode: bool = False
    requires_tool_use: bool = False
    min_extraction_grade: str | None = None
    min_creative_grade: str | None = None
```

The registry ships with built-in profiles for common models (Claude Sonnet 4.6, Opus 4.6, Haiku 4.5, GPT-4.1 family, Gemini 2.5 family, Llama 3.3 family, Qwen 2.5 coder family, Mistral family, Nomic Embed Code). Unknown models fall through to a **probe routine** that runs a fixed capability-test prompt and grades the response — instruction following, JSON fidelity, context size echo test.

Manual overrides are allowed via settings: operators can mark a specific local model as "instruction_following: high" if they have empirical evidence it performs well on extraction tasks.

The registry is persisted in a new SurrealDB table `ca_model_capabilities` and cached in memory with a TTL.

#### `StrategySelector` (Python, worker)

Given (corpus, artifact type, scope, user-configured preferences, current model), pick a strategy:

```
1. Load configured preference chain for (corpus_type, artifact_type).
   e.g., ["graphrag", "hierarchical", "single_shot"]
2. For each strategy in the chain:
   a. Look up strategy.capability_requirements()
   b. Check if current model meets those requirements
   c. If yes → select and return
   d. If no → emit a "strategy skipped" event and continue
3. If no strategy passes capability gating:
   a. Emit a structured STRATEGY_NONE_VIABLE error
   b. Return the error to the user with remediation instructions
      ("Your current model does not satisfy any configured strategy.
        Recommended: switch to model X or enable HierarchicalStrategy.")
```

This is the runtime strategy negotiation engine. Operators configure the preference chain; the selector applies capability rules. Both parts are overrideable at the workspace, corpus, artifact, and per-call level.

#### `LLMOrchestrator` (Go, `internal/llm/orchestrator/`)

The shared layer every LLM-using subsystem goes through.

```go
type Orchestrator struct {
    store       *db.Store
    worker      *worker.Client
    queue       *Queue
    retryPolicy RetryPolicy
    dedupe      *InFlightRegistry
    metrics     *Metrics
}

type LLMJob struct {
    ID             string
    Subsystem      string           // "knowledge" | "reasoning" | "requirements" | "linking"
    JobType        string           // subsystem-specific ("cliff_notes", "review", "extract_reqs", ...)
    TargetKey      string           // used for dedupe (repo+scope+audience+depth for knowledge; doc+section for reqs)
    Strategy       string           // selected strategy name
    Model          string
    Status         JobStatus        // pending | generating | ready | failed
    Progress       float64
    ProgressPhase  string
    ProgressMsg    string
    ErrorCode      string
    ErrorMessage   string
    RetryCount     int
    MaxAttempts    int
    TimeoutSec     int
    CreatedAt      time.Time
    StartedAt      time.Time
    UpdatedAt      time.Time
    CompletedAt    time.Time
    InputTokens    int
    OutputTokens   int
    SnapshotBytes  int
}

// Enqueue claims the job, persists it, checks dedupe, and runs it on a bounded pool.
// Callers receive an immediate job record and can subscribe to progress via the Monitor.
func (o *Orchestrator) Enqueue(ctx context.Context, req EnqueueRequest) (*LLMJob, error)

// Subscribe returns a channel of job state updates for a given job or filter.
func (o *Orchestrator) Subscribe(filter JobFilter) (<-chan LLMJobEvent, func(), error)
```

Every mutation in `internal/api/graphql/schema.resolvers.go` that currently calls `r.Worker.*` directly is refactored to call `r.Orchestrator.Enqueue(...)` instead. The orchestrator handles dedupe, queueing, retries, timeouts, progress persistence, and structured error reporting.

A new SurrealDB table `ca_llm_job` persists the full job lifecycle. `ca_knowledge_artifact` retains the final result and a pointer to the job id.

### Data model changes

#### New tables

```
ca_llm_job
  id uuid primary
  subsystem string
  job_type string
  target_key string                    -- dedupe key
  strategy string
  model string
  status string                        -- pending|generating|ready|failed
  progress float
  progress_phase string
  progress_message string
  error_code string
  error_message string
  retry_count int
  max_attempts int
  timeout_sec int
  input_tokens int
  output_tokens int
  snapshot_bytes int
  created_at datetime
  started_at datetime
  updated_at datetime
  completed_at datetime
  index on (subsystem, status, updated_at)
  index on (target_key, status)

ca_summary_node
  id uuid primary
  corpus_id string
  corpus_type string
  unit_id string
  level int
  parent_id uuid | null
  child_ids array<uuid>
  summary_text string
  summary_tokens int
  source_tokens int
  model_used string
  strategy string
  revision_fp string
  generated_at datetime
  index on (corpus_id, level)
  index on (corpus_id, unit_id)

ca_graph_entity           -- GraphRAG
  id uuid primary
  corpus_id string
  entity_type string
  canonical_name string
  aliases array<string>
  first_seen_unit_id string
  attributes object
  created_at datetime
  index on (corpus_id, entity_type)

ca_graph_relation         -- GraphRAG
  id uuid primary
  corpus_id string
  source_entity_id uuid
  target_entity_id uuid
  relation_type string
  weight float
  evidence array<object>
  index on (corpus_id, source_entity_id)
  index on (corpus_id, target_entity_id)

ca_graph_community        -- GraphRAG community summaries
  id uuid primary
  corpus_id string
  level int
  parent_community_id uuid | null
  entity_ids array<uuid>
  summary_text string
  summary_tokens int
  model_used string
  generated_at datetime
  index on (corpus_id, level)

ca_model_capabilities
  model_id string primary
  provider string
  declared_context_tokens int
  effective_context_tokens int
  long_context_quality object             -- {32000: "high", 128000: "medium"}
  instruction_following string
  json_mode string
  tool_use string
  extraction_grade string
  creative_grade string
  embedding_model bool
  cost_per_1k_input float | null
  cost_per_1k_output float | null
  last_probed_at datetime | null
  source string                           -- "builtin" | "probed" | "manual"
  notes string | null
  updated_at datetime

ca_strategy_settings
  id uuid primary
  scope_type string                       -- "workspace" | "corpus_type" | "artifact_type" | "user"
  scope_key string
  strategy_preference_chain array<string> -- ordered list of strategy names
  model_id string
  max_concurrency int
  max_prompt_tokens int
  leaf_budget_tokens int
  refine_pass_enabled bool
  long_context_max_tokens int
  graphrag_entity_types array<string>
  cache_enabled bool
  allow_unsafe_combinations bool
  updated_at datetime
  updated_by string
  index on (scope_type, scope_key)
```

#### Additive columns on `ca_knowledge_artifact`

```
error_message string | null
error_code string | null
progress_phase string | null
progress_message string | null
retry_count int default 0
strategy string | null
model string | null
job_id uuid | null                       -- FK to ca_llm_job
```

All additive; no backfill required; existing rows read as nulls/zeros.

### Settings surface

This section also describes a **target-state** operator surface. Before Phase E, the implementation should stay intentionally smaller: only the controls needed to support the active phase and its benchmark loop should be exposed. Rich inheritance, broad strategy editing, and full capability management are deferred until the underlying behavior has been proven.

Every knob listed in `ca_strategy_settings` is exposed in the settings UI. Configuration hierarchy (most specific wins):

1. **Per-call override** — passed in the mutation input (power-user API, not exposed in standard UI).
2. **Per-user** — a user's default strategy and model for their own generations.
3. **Per-artifact-type** — e.g., "always use GraphRAG for workflow stories if model allows."
4. **Per-corpus-type** — e.g., "use RAPTOR for code, hierarchical for requirements."
5. **Workspace default** — the baseline for everything.
6. **Built-in defaults** — ship with `["hierarchical", "single_shot"]` for all artifact types, conservative concurrency (3), conservative token budgets, caching off.

#### New settings page: `/admin/settings/comprehension`

Laid out as a tabbed page:

- **Strategies tab** — per (corpus type × artifact type) matrix with a strategy-preference-chain editor. Drag-and-drop to reorder. Each row shows the effective chain, the currently-selected model, and a capability status badge (green / yellow warning / red incompatible). Click a red badge → modal explains the mismatch and offers remediation: "Change model" | "Add fallback strategy" | "Allow unsafe (advanced)."
- **Models tab** — model capability registry viewer and editor. Shows built-in models, probed models, and operator-edited profiles. Buttons: "Probe a new model" | "Edit capability profile" | "Run health check." Each row displays the capability scores, cost, and which strategies currently require this model.
- **Orchestration tab** — concurrency limits (global, per-subsystem), timeouts (per scope/job type), retry policy (attempts, backoff), dedupe window, cache on/off.
- **Budgets tab** — token budgets (leaf budget, max prompt tokens, long-context threshold), configured per model if the operator wants to override defaults.
- **Advanced tab** — feature flags (enable GraphRAG, enable RAPTOR, enable long-context fallback, enable refine pass), unsafe overrides, debug logging toggles.

Every setting has an "inherited from" indicator showing which level of the hierarchy is actually applying the value.

#### New GraphQL mutations for settings

```graphql
updateComprehensionSettings(scope: SettingsScopeInput!, patch: ComprehensionSettingsPatch!): ComprehensionSettings!
resetComprehensionSettings(scope: SettingsScopeInput!): ComprehensionSettings!
probeModel(modelId: String!, provider: String!): ModelCapabilities!
updateModelCapabilities(modelId: String!, patch: ModelCapabilitiesPatch!): ModelCapabilities!
```

### Monitor surface

The Generation Monitor page from the superseded plan becomes a **generic LLM job monitor**, designed under the UI/UX principles above.

**The page at a glance** — when an operator opens `/admin/llm` for the first time, they see exactly three zones, top to bottom:

1. **Health banner** (one line, full width). Green / yellow / red pill with a plain-English state:
   - 🟢 *"Everything's running smoothly — 2 jobs active, 8 completed in the last hour, 0 failures."*
   - 🟡 *"Degraded — Ollama has been slow for the last 10 minutes (p95 68s, normally 18s). Jobs are still completing."*
   - 🔴 *"Problem — 4 of the last 5 jobs failed with `LLM_EMPTY`. The model is returning nothing. [Investigate →]"*

   The banner is the **executive summary**. An operator who glances for 2 seconds and sees green knows they don't need to do anything. One who sees red gets a direct link to the thing they need to fix.

2. **Now running** section — visual cards (not a table) for each active job. Each card shows: repo/corpus name, artifact type icon, model, a **live progress bar with phase label**, elapsed time, and a subtle cancel X in the corner. Cards animate in/out as jobs start and finish. When nothing's running, the empty state says *"No jobs right now. Generate cliff notes for a repo and you'll see it here."* with a button linking to the repo list.

3. **Recent history** section — collapsible table, default showing the last 20 terminal jobs. Columns are minimal: status icon, repo, type, duration, and an "error summary" column for failures that shows a one-line human error ("Ran out of context — try hierarchical strategy") instead of the raw error code. Clicking a row opens the detail drawer.

**Detail drawer** (right-side slide-in, not a separate page): shows the full job record as grouped sections with labels like *"Where it ran"* (subsystem, job type, strategy, model), *"How it went"* (phase timings as a horizontal timeline, not a table), *"What it produced"* (token counts, artifact link if success), and *"What went wrong"* (error with remediation buttons if failure). The retry button is prominent if the job failed; absent otherwise. A "view related jobs on this corpus" link filters the main view.

**Filter bar** (collapsible, default collapsed): subsystem chips, status toggles, corpus dropdown. Filters apply instantly to both Now Running and Recent History. Collapsed by default so first-time users don't see a wall of filters they don't understand.

**Live updates**: the page uses Server-Sent Events (`/api/v1/admin/llm/stream`) so Now Running updates every few hundred milliseconds without a refresh. Progress bars animate smoothly, not in 5-second jumps. If SSE fails, fall back silently to 2-second polling.

**Error UX specifically**: every failed job row displays a **plain-English error title** derived from the error code, not the raw gRPC message. Examples:
- `LLM_EMPTY` → *"Model returned nothing"* with remediation *"Try the hierarchical strategy for this size of repo"* + button.
- `DEADLINE_EXCEEDED` → *"Took too long"* with remediation *"The worker had 10 minutes and didn't finish. The LLM may be overloaded."*
- `SNAPSHOT_TOO_LARGE` → *"Too much content for this model"* with remediation *"Switch to hierarchical strategy, or pick a larger-context model."* Both remediation options are clickable buttons that prefill the settings change.
- `STRATEGY_NONE_VIABLE` → *"No strategy works with your current model"* with a button that opens the model picker.

**Repo-scoped popover**: on the repository detail page, a small "AI jobs" button in the header opens a compact popover showing the same Now Running + last 5 for *this* repo only. Reuses the same endpoint via `?target_prefix=<repo_id>`.

**What a first-time operator sees**: open `/admin/llm` → green banner at top, one card animating with a progress bar, a small recent-history table below. Zero reading required. If they're curious what GraphRAG is, they can click any job → drawer explains strategy trace in plain English: *"Hierarchical strategy was chosen because GraphRAG needs a model with high instruction-following, and your current model (`llama3:4k`) is rated medium."*

**Data source**: new REST endpoint `/api/v1/admin/llm/activity` that reads from `ca_llm_job`. SSE at `/api/v1/admin/llm/stream`. Detail drawer hits `/api/v1/admin/llm/jobs/:id`. All endpoints return JSON with pre-rendered human-readable fields so the frontend doesn't have to duplicate error-message mapping logic.

## Phases

The plan ships as a sequence of **evidence-gated phases**. Each phase leaves the system in a usable state, is tagged before the next phase begins, and includes tests whose results determine whether further building is justified.

### Phase A — Orchestration, Failure Visibility, and Provider Protection (≈3-5 days)

**Goal:** Make LLM execution bounded, deduplicated, observable, and failure-explicit before changing the comprehension algorithm.

This phase is the operational foundation. It does not attempt to improve comprehension quality yet. It exists to ensure the system can safely host later phases.

#### Changes

1. **Fail loudly on empty LLM responses** across knowledge, reasoning, requirements, and linking.
2. **Fail loudly on pre-flight token overflow** using a shared budget guard.
3. **Persist and expose errors** on `ca_knowledge_artifact` and future `ca_llm_job` records.
4. **Introduce provider-call protection, not just job protection.**
   - Add bounded queueing and dedupe in Go.
   - Add a provider-call semaphore enforced at the worker/provider layer so one job cannot saturate Ollama by fanning out internally.
   - Record both `job_concurrency` and `provider_call_concurrency` metrics.
5. **Route knowledge generation through `LLMOrchestrator`** first.
   - Reasoning/requirements/linking remain on the old path in this phase unless they can be migrated with low risk.
6. **Minimal Monitor page** with:
   - active jobs
   - recent failures
   - queue depth
   - provider saturation indicators
7. **Fix confirmed crashers** such as workflow story `NoneType`.

#### Validation suite

- **Unit tests**
  - `require_nonempty`
  - `check_prompt_budget`
  - error classification
  - workflow story regression
  - dedupe key logic
- **Integration tests**
  - rapid duplicate artifact requests dedupe correctly
  - queued jobs never exceed configured provider-call concurrency
  - failed jobs persist actionable error code/message
- **Operational checks on thor**
  - 10 concurrent artifact requests produce bounded execution
  - no silent stub successes
  - monitor shows queued/running/failed states correctly

#### Exit criteria

- [ ] `MACU Helpdesk` generation either succeeds or fails visibly; no stub success path remains.
- [ ] 10 concurrent requests never exceed the configured provider-call concurrency by more than 0 calls at any observation point.
- [ ] At least 95% of failed jobs are classifiable into a stable error code rather than falling into `INTERNAL`.
- [ ] The monitor identifies whether the bottleneck is queueing, timeout, or provider empties for 100% of sampled failures in the phase report.
- [ ] The team can explain current failure modes from job records alone.

#### Decision gate

Proceed to Phase B only if the system is operationally trustworthy. If provider protection and observability are still weak, stop here and harden further before changing summarization architecture.

### Phase B — Code-Only Hierarchical Baseline (≈5-8 days)

**Goal:** Establish a single production comprehension path that works on very large codebases and weaker models.

This is the most important phase in the plan. If it fails, the rest of the strategy work should not proceed.

#### Changes

1. **New package `workers/comprehension/`** with only the baseline pieces:
   - `corpus.py`
   - `tree.py`
   - `strategy.py`
   - `hierarchical.py`
   - `renderers.py`
2. **`CodeCorpus` adapter only.**
   - Do not build `RequirementsCorpus` or `DocumentCorpus` yet.
3. **`HierarchicalStrategy` only.**
   - AST-aware chunking where available
   - fallback chunking where not available
   - persisted `SummaryTree`
   - artifact renderers for the current code artifacts
4. **Conservative strategy selection.**
   - `HierarchicalStrategy` is always selected for code artifacts in this phase.
   - `SingleShotStrategy` remains available only as a rollback flag.
5. **Progress reporting by phase** for segment/file/package/root summarization.

#### Validation suite

- **Synthetic benchmark corpus**
  - very large multi-package repo shape
  - mixed language inputs
  - oversized files and degenerate files
- **Real-repo benchmark set**
  - `MACU Helpdesk`
  - at least 2 additional repos of different size/shape
- **Quality review harness**
  - compare generated artifacts against a rubric:
    - coverage
    - factual consistency
    - usefulness
    - cross-cutting understanding
  - use blind human review for a small fixed sample
- **Regression tests**
  - persisted tree reuse
  - artifact rendering from existing tree
  - large-corpus generation with 4K-context fake model

#### Exit criteria

- [ ] Repo-level cliff notes succeed on thor with the current Ollama deployment.
- [ ] A second generation from the same revision reuses persisted state and reduces wall-clock generation time by at least 50% versus the first run on the same corpus and model.
- [ ] Large synthetic corpora complete without context overflows in 100% of benchmark runs.
- [ ] Blind-review quality on the benchmark set averages at least 3.5/5 on coverage and 3.5/5 on usefulness, with no repo scoring below 3/5 on factual consistency.
- [ ] Provider-call concurrency remains within the configured ceiling even during leaf-level summarization fanout.

#### Decision gate

If hierarchical quality is not acceptable, do not add more strategies yet. Fix chunking, prompting, tree construction, and rendering first. Additional strategies are not a substitute for a weak baseline.

### Phase C — Baseline Optimization and Reuse (≈3-5 days)

**Goal:** Improve the proven baseline's latency and operating cost before expanding strategic scope.

#### Changes

1. **Incremental rebuilds** via fingerprints / invalidation.
2. **Prompt/cache reuse** where the provider supports it.
3. **Provider-specific operational tuning** such as Ollama `keep_alive`.
4. **Benchmark harness automation** so quality/latency/cost regressions are measured in CI or scheduled runs.

#### Validation suite

- repeated generation benchmark on unchanged corpus
- small-change benchmark on modified corpus
- cache hit instrumentation where applicable
- latency and cost comparison against Phase B tags

#### Exit criteria

- [ ] Unchanged-corpus reruns are at least 70% faster than the Phase B first-run baseline for the same repo/model pair.
- [ ] A change touching 3 files or fewer causes recomputation of no more than 15 summary nodes in the benchmark corpus.
- [ ] Benchmark results are captured and comparable across phase tags.

#### Decision gate

Proceed only if the baseline is now both reliable and efficient enough to serve as the fallback contract for every future advanced strategy.

### Phase D — Experimental Strategy Bakeoff (≈variable, feature-flagged)

**Goal:** Determine whether advanced strategies actually outperform the baseline for defined scenarios.

This phase is explicitly experimental. RAPTOR, GraphRAG, and Long-Context Direct are not yet product defaults.

#### Changes

1. **Implement advanced strategies behind flags**:
   - `LongContextDirectStrategy`
   - `RAPTORStrategy`
   - `GraphRAGStrategy`
2. **Introduce a minimal capability registry** focused on:
   - effective context
   - structured-output reliability
   - streaming support
   - observed latency/error rates
3. **Add strategy trace recording** for debugging selection and fallback.
4. **Run bakeoff evaluations** by artifact type, corpus size, and model class.

#### Evaluation rules

An advanced strategy can only graduate beyond experimental if it beats hierarchical on a benchmark slice by an agreed threshold, for example:

- quality improvement of at least 0.5 points on the 5-point review rubric for the targeted workload, or
- latency improvement of at least 30% for the targeted workload, or
- cost reduction of at least 25% without any statistically meaningful quality regression

If it is merely different or more impressive architecturally, it does not graduate.

#### Validation suite

- head-to-head benchmark runs:
  - hierarchical vs long-context on small corpora with strong models
  - hierarchical vs RAPTOR on cross-cutting repos
  - hierarchical vs GraphRAG on relationship-heavy tasks
- per-strategy failure-rate and cost tracking
- human review on a fixed rubric

#### Exit criteria

- [ ] Each advanced strategy has a written benchmark result against hierarchical.
- [ ] At least one advanced strategy demonstrates one of the required threshold improvements on a defined workload.
- [ ] No advanced strategy becomes default without benchmark evidence and a rollback flag.

#### Decision gate

Only strategies that prove their value move forward into automatic selection. The rest remain experimental or are dropped.

### Phase E — Automatic Progressive Enhancement (≈3-5 days)

**Goal:** Use strong models better without compromising the baseline contract.

#### Changes

1. **Strategy selector** chooses:
   - baseline hierarchical by default
   - advanced strategies only where benchmark-backed rules say they are better
2. **Fallback chain**
   - advanced strategy → hierarchical baseline
   - never the reverse for correctness
3. **Operator UI**
   - monitor strategy trace
   - simple settings for model selection
   - advanced settings remain hidden behind feature flags and explicit advanced mode

#### Validation suite

- model/strategy selection tests
- fallback behavior tests
- benchmark replay on weak and strong models
- first-time operator usability test on the reduced settings surface

#### Exit criteria

- [ ] Strong models use advanced strategies only where they cleared the Phase D threshold for the relevant workload class.
- [ ] Weak models and large corpora still land on hierarchical automatically.
- [ ] Fallback to hierarchical succeeds in at least 95% of forced-fallback test runs and is visible in the strategy trace.

#### Decision gate

If automatic selection creates confusion or unstable outcomes, keep advanced strategies as manual opt-ins and stop here.

### Phase F — Broader Corpus Support (optional, after code path maturity)

**Goal:** Extend the proven engine to requirements and generic documents only after the code path is mature.

#### Changes

1. **`RequirementsCorpus` adapter**
2. **`DocumentCorpus` adapter**
3. **New renderers** only where there is a demonstrated product need
4. **Scope-specific benchmarks** for non-code corpora

#### Validation suite

- domain-specific quality rubrics
- corpus-specific chunking tests
- comparison against simpler dedicated summarizers

#### Exit criteria

- [ ] Non-code corpus support demonstrates at least parity with a simpler corpus-specific summarizer on the agreed rubric, or a measurable maintainability/product advantage documented in the phase report.
- [ ] Shared abstractions still fit without distorting the code path.

#### Decision gate

If broader corpus support introduces too much abstraction pressure, split it into a separate track rather than forcing a universal engine.

## Ready Next Steps

The immediate next implementation target is **Phase A**. The work should be prepared as the following check-in sequence:

1. **A0: Baseline benchmark + initial tag**
   - run the benchmark suite on the existing system
   - capture the baseline report
   - commit any harness/config required for repeatability
   - ensure only OSS-safe benchmark artifacts are staged
   - tag the current state before implementation begins
   - push branch and tag
2. **A1: Failure surfacing**
   - empty-response guard
   - token budget guard
   - persisted artifact errors
   - workflow story crash fix
3. **A2: Bounded execution**
   - orchestrator package
   - job persistence
   - dedupe
   - provider-call semaphore
4. **A3: Minimal monitor**
   - activity endpoint
   - SSE/polling path
   - active/recent failure view
5. **A4: Thor validation + phase tag**
   - rerun the same benchmark suite used in A0
   - run phase benchmark/checklist
   - compare against the baseline report
   - document observed latency/failure/concurrency
   - sanitize any private benchmark evidence into OSS-safe summaries before commit/tag
   - commit the phase report if it lives in-repo
   - cut the phase tag
   - push commits and tags before Phase B

Each of these check-ins should be independently reviewable, testable, and revertable.

### A0 benchmark harness plan

The repo already contains useful building blocks for A0:

- `tests/fixtures/multi-lang-repo/` — stable multi-language fixture corpus
- `workers/common/llm/fake.py` — deterministic fake provider for repeatable worker-side runs
- `workers/tests/test_cliff_notes.py`, `workers/tests/test_learning_path.py`, `workers/tests/test_code_tour.py`, `workers/tests/test_workflow_story.py` — existing artifact coverage tests
- `workers/tests/test_evaluation.py` — precedent for a fixture-backed evaluation harness
- `Makefile` targets: `test`, `test-worker`, `integration-test`, `smoke-test`

#### A0 deliverables

1. **Benchmark manifest**
   - Add a small in-repo benchmark manifest, for example `benchmarks/comprehension/manifest.yaml`, that defines:
     - corpus id
     - generation type (`cliff_notes`, `learning_path`, `code_tour`, `workflow_story`)
     - provider mode (`fake`, `local_ollama`)
     - audience/depth
     - expected output checks

2. **Benchmark runner**
   - Add a repeatable runner, preferably in Python near the worker tests, for example:
     - `workers/benchmarks/run_comprehension_bench.py`
   - The runner should:
     - execute the configured artifact generations
     - record start/end timestamps
     - capture success/failure
     - capture input/output token counts when available
     - capture output sizes and section counts
     - write machine-readable JSON results

3. **Benchmark report location**
   - Store benchmark outputs under a stable path such as:
     - `benchmarks/results/<tag-or-date>/summary.json`
     - `benchmarks/results/<tag-or-date>/<case>.json`
   - Keep a short human-readable markdown report alongside the JSON:
     - `benchmarks/results/<tag-or-date>/report.md`
   - Only OSS-safe fixture outputs and sanitized summaries belong in committed benchmark directories.
   - Private repo benchmark outputs should be kept outside version control or under ignored local/internal storage.

4. **Make targets**
   - Add explicit make targets such as:
     - `make benchmark-comprehension-fake`
     - `make benchmark-comprehension-local`
     - `make benchmark-comprehension-report`
   - `A0` should use these rather than ad hoc shell history.

#### Benchmark suites

Run two suites at minimum:

1. **Deterministic regression suite**
   - Provider: `FakeLLMProvider`
   - Corpus: `tests/fixtures/multi-lang-repo/`
   - Artifact types:
     - cliff notes
     - learning path
     - code tour
     - workflow story
   - Purpose:
     - catch shape regressions
     - verify repeatability
     - provide stable before/after comparisons for non-provider changes

2. **Real-provider operational suite**
   - Provider: current configured local Ollama on thor
   - Corpus set:
     - `MACU Helpdesk`
     - at least one small repo
     - at least one medium or multi-package repo
   - Artifact types:
     - cliff notes required in every phase
     - learning path / code tour / workflow story as time permits in A0, but all four should be added early
   - Purpose:
     - measure real latency and failure behavior
     - expose provider saturation and timeout patterns
     - compare baseline vs post-phase behavior on actual infrastructure
   - Commit only sanitized aggregate metrics from this suite unless the corpus is OSS-safe.

#### Metrics to capture

Every benchmark case should record at least:

- commit SHA
- branch name
- tag name if present
- phase label
- provider name
- model id
- corpus id
- artifact type
- audience/depth/scope
- success/failure
- error code and error message if failed
- wall-clock duration
- queue wait time if available
- provider execution time if available
- input tokens/output tokens if available
- output structure counts:
  - section count
  - evidence count
  - stop count for code tours
  - step count for learning paths

For real-provider runs, also record:

- empty-response occurrence
- timeout occurrence
- retry count
- concurrent requests in the run

#### Quality checks

The initial benchmark harness does not need full semantic grading, but it should include objective output checks:

- `cliff_notes`
  - all required sections present
  - at least one evidence-bearing section
- `learning_path`
  - non-empty ordered steps
- `code_tour`
  - non-empty ordered stops with file paths
- `workflow_story`
  - non-empty structured output

For the deterministic fixture suite, these checks should be automated and blocking.

For the real-provider suite, add a lightweight manual review sheet in the markdown report:

- coverage: 1-5
- usefulness: 1-5
- factual consistency: 1-5
- notes on obvious misses or hallucinations

#### A0 execution order

1. Add the benchmark runner, manifest, and make targets.
2. Run the deterministic fixture suite and save results.
3. Run the real-provider suite on thor and save results.
4. Write `benchmarks/results/<baseline-tag>/report.md`.
5. Commit benchmark harness changes.
6. Sanitize or exclude any private benchmark artifacts.
7. Tag the repo baseline.
8. Push commits and tags.

Only after that should A1 implementation begin.

## Test Program

The plan requires a standing test program, not ad hoc verification:

1. **Deterministic automated tests**
   - unit tests
   - integration tests
   - fake-model regression tests
2. **Benchmark suite**
   - fixed corpora
   - fixed prompts
   - fixed rubric
   - captures latency, error rate, approximate cost, and output quality
   - is run on the unmodified system before each phase and rerun after the phase against the same inputs
   - keeps committed artifacts OSS-safe and stores private benchmark outputs separately when needed
3. **Human evaluation loop**
   - small fixed sample per phase
   - blind review where practical
   - explicit pass/fail notes checked into the repo alongside the plan or benchmark outputs
4. **Phase gate artifact**
   - each phase ends with a short report:
     - what changed
     - what was measured
     - what improved
     - what did not
     - whether the next phase is justified
5. **Repository checkpoints**
   - initial baseline tag before implementation begins
   - one phase-completion tag after each accepted phase
   - commits and pushes at coherent checkpoints, not only at the very end
   - every phase tag must point to a tree that is safe to carry forward into the OSS release

---

## Performance Considerations

- **Summary tree storage.** A 10K-file repo at 4 levels produces ~10K leaf summaries + ~1K file summaries + ~100 package summaries + 1 root = ~11K rows in `ca_summary_node`. SurrealDB handles this easily. Indexed on `(corpus_id, level)` for efficient level traversal.
- **GraphRAG graph size.** Entity extraction typically produces 2-10 entities per leaf; a 10K-file repo yields ~50K entities and 100K relations. Still trivial for SurrealDB; the bottleneck is the extraction pass itself (one LLM call per leaf), which is why GraphRAG is gated behind capability checks and runs as a background reindex rather than on every generate request.
- **Concurrency interactions.** The orchestrator's global semaphore (default 3) protects a single LLM provider. When operating against a single local Ollama, this is the correct ceiling. When operating against Claude Sonnet 4.6 cloud, the ceiling can be raised to 10+ via settings. Per-job parallelism (e.g., hierarchical leaf summarization) is a *local* semaphore scoped to one job and doesn't interact with the global ceiling — a single job can fan out 20 parallel leaf calls while holding one global slot.
- **Prompt caching savings.** Hitting Anthropic prompt cache on a 50K-token prefix drops input cost from ~$0.15 per call to ~$0.015 per call (90% savings on the cached portion). On a multi-artifact run (4 artifacts × same corpus), total savings are ~85% of input tokens.
- **Long-context escape valve cost.** Using Claude Sonnet 4.6 long-context as a fallback for a 200K-token corpus costs ~$0.60 per generation. Hierarchical against the same corpus costs ~$0.08 because every individual call is small. Hierarchical is cheaper *and* better-quality at scale; long-context is kept only for the small-corpus case where the overhead of building a tree is wasteful.
- **Monitor page load.** The active-jobs endpoint queries `ca_llm_job WHERE status IN ('pending', 'generating')` which stays small (≤ concurrency × recent buffer). Recent-jobs endpoint caps at 50 rows with an index on `(subsystem, status, updated_at DESC)`.

## Migration Notes

- **Database migrations are additive only.** Every new table is independent; every new column on `ca_knowledge_artifact` is nullable with safe defaults. No backfill, no downtime, no schema-version lockstep with Go/Python.
- **Feature flags for strategy rollout.** Phase B ships `HierarchicalStrategy` as the default code path, with `SingleShotStrategy` retained as a rollback flag. Phase D strategies ship disabled by default (`enable_graphrag_strategy: false`, `enable_raptor_strategy: false`, `enable_long_context_strategy: true`) and remain experimental until benchmark evidence justifies wider use.
- **Legacy artifact compatibility.** Existing `ca_knowledge_artifact` rows continue to work. The new `strategy` / `model` / `job_id` columns are populated on regeneration only; legacy rows show "unknown" in the Monitor page.
- **Worker backward compatibility during rollout.** Phase A's orchestrator can call existing unary gRPC methods while the baseline comprehension engine is introduced in Phase B. Old and new workers can coexist during the transition.
- **Settings bootstrap.** On first deploy, the workspace-default row is seeded automatically via a startup migration: `strategy_preference_chain=["hierarchical", "single_shot"]`, `model_id=<current configured model>`, conservative budgets.
- **Provider config migration.** The existing `llm_provider: "ollama"` config in `config.toml` is preserved; the new `[comprehension]` section adds optional overrides for strategy defaults and capability probing.
- **Thor deployment sequence.** Phase A ships first to establish bounded, observable execution. Phase B then ships with a canary: enable hierarchical generation for one repo, validate, then flip the workspace default for code artifacts. Later phases follow the same canary pattern and proceed only if their decision gates pass.

## Risks & Open Questions

- **RAPTOR clustering on very small corpora** may produce degenerate clusters (one cluster with all leaves). Mitigation: fall back to pure hierarchical when leaf count < 16.
- **GraphRAG entity extraction quality on Ollama** is untested. Mitigation: capability registry gates GraphRAG behind `instruction_following=high`, which current small Ollama models do not satisfy. GraphRAG is effectively a cloud-only strategy for v1.
- **Token counting accuracy.** We currently approximate `tokens = len(text) / 4`. For tight budget decisions we should use `tiktoken` or provider-specific tokenizers. Mitigation: add provider-aware tokenization in Phase A for Anthropic and OpenAI-compatible providers; keep heuristic fallback for Ollama where provider-aware counting isn't available.
- **Incremental reindex correctness under concurrent writes.** If a repo ingest and a generation run at the same time, the generation may see a partial tree. Mitigation: tree writes are atomic per node; generations read with a snapshot fingerprint and refuse if the fingerprint changes mid-run (retry from the top of the queue).
- **Settings inheritance cycles.** The scope hierarchy (`workspace → corpus_type → artifact_type → user`) is a fixed tree, so no cycles are possible by construction. Verify in the store layer anyway via a recursive-depth cap.
- **Model probe cost.** Probing a commercial model costs a few cents per probe. Mitigation: probes are manual-trigger only in settings UI, not automatic.
- **Long-context quality curves.** LongCodeBench and Chroma's context-rot research give us published degradation curves for Claude and GPT. We don't have curves for every Ollama model variant. Mitigation: conservative defaults (mark all long-context quality as `medium` above 32K for unknown models), with a manual override in settings.

## References

### Research and prior art

- [Microsoft GraphRAG project](https://www.microsoft.com/en-us/research/project/graphrag/) — the global-sensemaking framing this plan builds on.
- [GraphRAG default dataflow documentation](https://microsoft.github.io/graphrag/index/default_dataflow/)
- [nano-graphrag](https://github.com/gusye1234/nano-graphrag) — lightweight reference implementation.
- [LightRAG (EMNLP 2025)](https://aclanthology.org/2025.findings-emnlp.568/)
- [RAPTOR arxiv 2401.18059](https://arxiv.org/abs/2401.18059)
- [Hierarchical repo-level code summarization arxiv 2501.07857](https://arxiv.org/html/2501.07857v1)
- [RepoAgent arxiv 2402.16667](https://arxiv.org/abs/2402.16667) — OpenBMB's hierarchical repo summarization.
- [LongCodeBench arxiv 2505.07897](https://arxiv.org/html/2505.07897v1) — context-rot quantification for code.
- [Chroma Research: Context Rot](https://www.trychroma.com/research/context-rot)
- [Lost in the Middle arxiv 2307.03172](https://arxiv.org/abs/2307.03172)
- [Aider repo-map documentation](https://aider.chat/docs/repomap.html) — PageRank over symbol graph.
- [Cursor secure codebase indexing](https://cursor.com/blog/secure-codebase-indexing) — Merkle-tree incremental reindex.
- [Anthropic prompt caching documentation](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)
- [Anthropic effective context engineering for agents](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
- [cAST: AST-aware chunking (EMNLP 2025)](https://aclanthology.org/2025.findings-emnlp.430/)
- [LSPRAG (ICSE 2026)](https://conf.researchr.org/details/icse-2026/icse-2026-research-track/147/LSPRAG-LSP-Guided-RAG-for-Language-Agnostic-Real-Time-Unit-Test-Generation)
- [Nomic Embed Code announcement](https://www.nomic.ai/news/introducing-state-of-the-art-nomic-embed-code)
- [voyage-code-3 launch](https://blog.voyageai.com/2024/12/04/voyage-code-3/)

### Relevant files in this repo

- `internal/api/graphql/schema.resolvers.go:1305-1495` — current `GenerateCliffNotes` mutation.
- `internal/worker/client.go:36` — current uniform 600s timeout.
- `internal/db/knowledge_store.go` — `ca_knowledge_artifact` model.
- `internal/api/rest/admin_knowledge.go:56-113` — current admin knowledge endpoint.
- `workers/knowledge/servicer.py:81-161` — current knowledge gRPC servicer.
- `workers/knowledge/cliff_notes.py:208-324` — current cliff notes pipeline.
- `workers/knowledge/workflow_story.py:369-498` — current workflow story pipeline (NoneType bug lives here).
- `workers/common/llm/openai_compat.py:98` — empty-response coercion site.
- `workers/common/llm/provider.py` — `LLMProvider` protocol + `LLMResponse`.
- `workers/common/llm/anthropic.py` — Anthropic provider (prompt caching hook).
- `workers/reasoning/`, `workers/requirements/`, `workers/linking/` — other LLM-using subsystems that will route through the orchestrator.
- `web/src/app/(app)/repositories/[id]/page.tsx:1820,1840-1848` — progress bar + failed badge.
- `web/src/app/(app)/admin/page.tsx` — admin dashboard (new tab added in Phase A).

### Live evidence

- Thor API pod logs (`sourcebridge-api-664c554746-48nt9`, 2026-04-09): `refresh cliff notes failed` with `DeadlineExceeded`, `workflow story generation failed` with `'NoneType' object is not subscriptable`.
- Thor worker pod logs (`sourcebridge-worker-784d8db9dc-mqb2f`, 2026-04-09): `cliff_notes_parse_fallback` with empty LLM responses, `generate_cliff_notes` firing three times within three seconds for the same repository scope, quality metrics showing 7 stub sections with 38 avg char length.

---

## Implementation status addendum (2026-04-09)

Phases have shipped on the `main` branch in the order below. The commit
history records the full detail; this addendum reconciles the
implementation's internal phase numbering (1-7) with the plan's
lettered phases (A-F) and documents what is explicitly deferred.

### Shipped

| Impl phase | Plan phase | Status | Commits |
|---|---|---|---|
| Phase 1 (close gaps) | A1 + A2 minus orchestrator | ✅ | `892a57b` |
| Phase 2a (orchestrator foundation) | A2 | ✅ | `a221603` |
| Phase 2b (wire knowledge mutations) | A2 | ✅ | `f4480d6` |
| Phase 2c (Monitor REST + SSE) | A3 | ✅ | `04a6d59` |
| Phase 2d (Monitor page UI) | A3 | ✅ | `c98aa9b` |
| Phase 2e (route synchronous LLM mutations) | A2 | ✅ | `28e8d8f` |
| Phase 3 (Comprehension engine + HierarchicalStrategy) | B | ✅ | `adf11d6` |
| Phase 4a-c (strategies + selector + servicer dispatch) | D partial, E partial | ✅ | `f9d83e4` |
| Phase 4d-e (RequirementsCorpus + DocumentCorpus) | F | ✅ | (this commit) |

### Deferred from Phase 4

Two of the advanced strategies in Phase D are intentionally **not
shipped** in this pass:

1. **RAPTORStrategy** — requires `scikit-learn` for Gaussian Mixture
   clustering. That adds ~50MB of Python dependencies to the worker
   image, which is a non-trivial change for every deployment. Until
   a benchmark clearly demonstrates RAPTOR beats hierarchical on a
   real workload (Phase D's evaluation rules), the extra dep weight
   is not justified. Shipping plan: land in a dedicated commit that
   also updates the worker Dockerfile and includes a bakeoff report.

2. **GraphRAGStrategy** — requires `networkx` + `python-leidenalg`
   (and the latter needs C compilation that can fail on ARM builds),
   plus an entity-extraction pass whose quality on small Ollama
   models is untested. GraphRAG is also capability-gated to
   `min_instruction_following=high` + native JSON mode, which rules
   out every local Ollama model in our current registry. Shipping
   plan: land when (a) the deployment standardizes on a capable
   model and (b) a benchmark slice shows GraphRAG meaningfully
   improves "how does everything relate" queries.

Both deferrals live behind the existing `StrategySelector` preference
chain, so enabling them later is additive: new strategy registered,
new entry in `DEFAULT_CLIFF_NOTES_CHAIN`, no changes to any existing
code path.

### What Phase 4 does ship

- `SingleShotStrategy` — legacy path formalized as a first-class strategy.
- `LongContextDirectStrategy` — single-call path with a pre-flight
  budget guard and a runtime fallback to hierarchical when the guard
  trips. Gated on `min_context_tokens=32k` and `medium` instruction
  following, so only cloud models and large local models pass.
- `ModelCapabilityRegistry` with builtin profiles for 13 production
  models (Claude Opus/Sonnet/Haiku 4.x, GPT-4.1 family, Gemini 2.5
  Pro/Flash, Llama 3.3 at multiple context sizes, Qwen 2.5 Coder,
  Nomic Embed Code, voyage-code-3).
- `StrategySelector` with capability gating, fallback chains, and a
  plain-English `SelectionTrace` that the Monitor page can render.
- Comma-separated preference chain via
  `SOURCEBRIDGE_CLIFF_NOTES_STRATEGY`. Default:
  `hierarchical,single_shot`.
- `RequirementsCorpus` adapter — wraps requirements collections
  (single or multiple documents, with or without sections) as the
  CorpusSource protocol, enabling hierarchical summarization of
  requirements with zero strategy changes.
- `DocumentCorpus` adapter — wraps plain-text and markdown documents.
  Splits markdown by heading level, falls back to paragraph splitting
  for plain text, caps paragraphs per section so runaway docs don't
  spawn thousands of leaf calls.

New renderers for the two new corpus types (`RequirementsDigestRenderer`,
`DocumentDigestRenderer`) are **also deferred** — the existing
`CliffNotesRenderer` can consume any `SummaryTree` regardless of
corpus type, so the new adapters are immediately useful via the
existing hierarchical path when operators point the cliff notes
servicer at a requirements or document source. Dedicated renderers
with scope-specific section templates land when the servicer gains
a new RPC or the existing one learns to route by corpus type.

### Tests

  - Phase 4a: 7 new tests (`test_single_shot_strategy.py`)
  - Phase 4b: 9 new tests (`test_selector.py`)
  - Phase 4c: 4 new tests added to `test_hierarchical_servicer.py`
  - Phase 4d+4e: 14 new tests (`test_corpus_adapters.py`)
  - Total new in Phase 4: **34 tests**, all passing
  - Full comprehension suite: **70 tests passing**, ruff clean.
