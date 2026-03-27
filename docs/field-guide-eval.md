# Field Guide Evaluation Rubric

This rubric provides quantitative and qualitative criteria for evaluating
generated Field Guide artifacts (Cliff Notes, Learning Paths, Code Tours,
Workflow Stories) across all scope levels. It was created from the initial
dogfood evaluation of the SourceBridge repository (2026-03-23) and should be
updated as quality targets evolve.

## Scope Definitions

| Scope | Description |
|---|---|
| Repository | Whole-repo summary — "start here" orientation |
| File | Single-file guide — ownership, state, likely edits |
| Symbol | Single function/class/method — purpose, impact, safety |

## Success Rate Targets

| Scope | Target | Baseline (2026-03-23) |
|---|---|---|
| Repository | >= 99% | ~100% (no failures observed) |
| File | >= 95% | ~100% (limited sample) |
| Symbol | >= 90% | 0% (crash on first attempt) |

A "success" means the artifact reaches READY state with all required sections
populated and no worker errors.

## Structural Completeness

Every artifact must include all required sections for its type. Missing or
placeholder sections count against completeness.

### Cliff Notes Required Sections

**Repository scope:** System Purpose, Architecture Overview, Key Components,
Data Flow, Testing Strategy, Deployment & Infrastructure, Development Workflow

**File scope:** Purpose, Behavior, Dependencies, Complexity Notes, Impact
Analysis

**Symbol scope:** Purpose, Behavior, Dependencies, Complexity Notes, Impact
Analysis

### Section Population

| Metric | Target | Baseline |
|---|---|---|
| Sections with real content | 100% of required | Repo: 100%, File: 100%, Symbol: N/A |
| Stub/placeholder sections | 0 | Repo: 0, File: 0, Symbol: N/A |

## Word Count Targets

Word counts should fall within these ranges. Below the floor suggests
insufficient content; above the ceiling suggests rambling or filler.

| Artifact | Depth | Floor | Ceiling | Baseline |
|---|---|---|---|---|
| Repo Cliff Notes | Medium | 450 | 650 | 517 |
| Repo Cliff Notes | Deep | 650 | 900 | 719 |
| File Cliff Notes | Medium | 250 | 450 | 347-372 |
| Symbol Cliff Notes | Medium | 120 | 260 | N/A |
| Learning Path | — | 500 | 800 | 674 |
| Code Tour | — | 250 | 500 | 348 |
| Workflow Story | — | 300 | 600 | N/A |

## Evidence Quality

Evidence references ground the generated content in actual code. Each evidence
item references a source_type (symbol, file, requirement) and specific
location.

### Evidence Count Targets

| Scope | Depth | Minimum | Baseline |
|---|---|---|---|
| Repository | Medium | 20 | 28 |
| Repository | Deep | 25 | 28 |
| File | Medium | 8 | 10-12 |
| Symbol | Medium | 4 | N/A |
| Learning Path | — | 40 | 73 |
| Code Tour | — | 6 | 8 |

### Evidence Mix Targets

Evidence should lean toward code-local sources (symbols, files) rather than
requirements. Requirements evidence is valid when it clarifies business intent,
but should not dominate.

| Scope | Code-local (symbol + file) | Requirement | Baseline |
|---|---|---|---|
| Repository | >= 60% | <= 40% | 61% code / 39% req |
| File | >= 70% | <= 30% | 75-100% code |
| Symbol | >= 80% | <= 20% | N/A |

## Narrative Quality (Manual Scoring)

Each artifact is manually scored on four dimensions using a 1-5 scale.

| Dimension | Description |
|---|---|
| Practical usefulness | Would a developer save time by reading this? |
| Specificity | Does it reference concrete code elements, not generic patterns? |
| Maintainer guidance | Does it orient toward safe modification, not just comprehension? |
| Trustworthiness | Are claims grounded in evidence? Does it avoid hallucination? |

### Narrative Score Targets

| Scope | Average across 4 dimensions | Baseline |
|---|---|---|
| Repository | >= 4.0 | ~3.5 (good but architecture-heavy) |
| File | >= 4.0 | ~3.0 (generic, stock AI phrasing) |
| Symbol | >= 3.5 | N/A |

## Grounding Rules (Symbol Scope)

Symbol-scope artifacts have the strictest grounding requirements:

1. Only describe parameter types, return types, and signatures that appear
   literally in the snapshot
2. If parameter types are not shown, write "Parameter types not available in
   snapshot" — do not guess
3. Do not invent runtime infrastructure (databases, caches, queues, HTTP
   clients) unless the snapshot shows explicit references
4. Do not describe what happens "downstream" beyond the direct callees listed
   in scope_context
5. Every claim must trace back to a symbol name, file path, or caller/callee
   relationship visible in the snapshot

## Coverage Targets

| Metric | Target | Baseline |
|---|---|---|
| Repository-level artifacts generated | Cliff Notes + Learning Path + Code Tour | 4 artifacts |
| File-level coverage | >= 5% of files with > 100 lines | ~0.7% |
| Symbol-level coverage | >= 1% of exported symbols | 0% |
| knowledgeScopeChildren (repo) | Non-empty | 0 (broken) |

## Freshness

| Metric | Target |
|---|---|
| Artifact age vs HEAD | Displayed in UI |
| Stale indicator | Visible when source revision != current HEAD |
| Line reference alignment | References valid for displayed revision |

## Re-Evaluation Checklist

After each quality improvement cycle, answer these questions:

- [ ] Does the handleLogin symbol artifact generate successfully?
- [ ] Are repository scope children non-empty?
- [ ] Do guides sound like maintainer-oriented guidance (not architecture docs)?
- [ ] Is requirements evidence present but not dominant?
- [ ] Is scoped artifact coverage materially higher than baseline?
- [ ] Do all quality metrics meet or exceed targets above?

Any "no" means the Field Guide positioning is ahead of the product quality.

## Instrumentation

Quality metrics are captured via structured logging in the Python worker:

- `cliff_notes_quality_metrics` — per-generation event with section counts,
  evidence distribution, confidence levels, content lengths
- `workflow_story_quality_metrics` — per-generation event with placeholder
  detection, execution path step counts, evidence distribution

These events enable automated before/after comparison without manual artifact
inspection.
