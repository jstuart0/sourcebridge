# QA Parity Report

- Baseline arm: commit `4776aeb` on 2026-04-23 04:58:10.537541+00:00
- Candidate arm: commit `5235a57` on 2026-04-23 18:17:20.314893+00:00
- Mode: deep vs deep
- Samples: baseline=120 (judged=120, errored=0); candidate=120 (judged=120, errored=0)

## Headline metrics

| Metric | Baseline | Candidate | Delta |
|--------|----------|-----------|-------|
| Answer-useful rate | 65.83% | 67.50% | +1.67% |
| Fallback rate | 0.00% | 0.00% | +0.00% |
| Latency p50 (ms) | 28513 | 29015 | +502 |
| Latency p95 (ms) | 44243 | 65524 | +21281 |
| Latency p99 (ms) | 51659 | 80445 | +28786 |

## Per-class answer-useful rate

| Class | Baseline | Candidate | Delta | N |
|-------|----------|-----------|-------|---|
| architecture | 68.00% | 84.00% | +16.00% | 25 |
| behavior | 45.00% | 50.00% | +5.00% | 20 |
| cross_cutting | 56.00% | 56.00% | +0.00% | 25 |
| execution_flow | 80.00% | 72.00% | -8.00% | 25 |
| ownership | 76.00% | 72.00% | -4.00% | 25 |

## Top-20 quality regressions (lowest candidate-minus-baseline score)

Human review required before the candidate ships. Sign off in the
Plane epic for the Phase-5 rollout, quoting this section.

| ID | Class | Repo | B | C | Δ | Δlatency (ms) | Fallback change | Judge rationale (candidate) |
|----|-------|------|---|---|---|---------------|-----------------|------------------------------|
| cross-025 | cross_cutting | acme-api | 3 | 1 | -2 | +2175 |  | The answer lists plausible function names but doesn't actually explain how owner-only enforcement works—no specific r... |
| flow-003 | execution_flow | sourcebridge | 3 | 1 | -2 | -7124 |  | The answer explicitly admits it cannot determine the decision logic and asks the user to examine the file themselves.... |
| cross-007 | cross_cutting | sourcebridge | 2 | 0 | -2 | -8556 |  | The answer explicitly admits it could not retrieve sufficient evidence and speculates about possible behaviors rather... |
| cross-019 | cross_cutting | sourcebridge | 2 | 1 | -1 | +11005 |  | The answer describes selective invalidation as a way to reduce the scope of staleness marking, but the question asks ... |
| own-003 | ownership | sourcebridge | 3 | 2 | -1 | +9177 |  | The answer directly identifies a concrete file path (internal/api/graphql/schema.resolvers.go) and function name (Dis... |
| mix-015 | behavior | acme-api | 3 | 2 | -1 | +8471 |  | The answer directly addresses the question with concrete return values, early-return logic, and a referenced file pat... |
| flow-013 | execution_flow | acme-api | 3 | 2 | -1 | +7638 |  | The answer provides a concrete, coherent walkthrough with specific function names and file paths across the handler, ... |
| arch-025 | architecture | acme-api | 3 | 2 | -1 | +7081 |  | The answer directly addresses both magic link and invitation flows with concrete function names (sendMagicLinkEmail, ... |
| mix-001 | behavior | sourcebridge | 1 | 0 | -1 | +7056 |  | The answer admits it could not find the PathBoosts function and fails to answer the question about what it does or wh... |
| cross-001 | cross_cutting | sourcebridge | 2 | 1 | -1 | +6736 |  | The answer punts on the actual question, repeatedly stating it could not verify the implementation and instead offeri... |
| cross-003 | cross_cutting | sourcebridge | 2 | 1 | -1 | +6441 |  | The answer hand-waves with vague generalities ('likely', 'suggests') rather than concretely describing the deep-QA fa... |
| mix-005 | behavior | sourcebridge | 1 | 0 | -1 | +5188 |  | The answer explicitly punts, stating it cannot answer due to evidence budget limits, and only speculates about possib... |
| cross-022 | cross_cutting | acme-api | 2 | 1 | -1 | +4765 |  | The answer hedges and essentially concludes there is no centralized enforcement, which is a non-answer to 'what enfor... |
| own-024 | ownership | multi-lang-repo | 3 | 2 | -1 | +3059 |  | The answer directly names a concrete file path for the StartServer entry point. While the specific location cannot be... |
| arch-015 | architecture | sourcebridge | 3 | 2 | -1 | +1486 |  | The answer directly addresses the question with concrete components (trackEvent, posthog.capture, handleTelemetryEven... |
| flow-019 | execution_flow | acme-api | 3 | 2 | -1 | +1387 |  | The answer directly addresses the question with a coherent step-by-step flow naming concrete functions (handleSignIn,... |
| flow-024 | execution_flow | multi-lang-repo | 3 | 2 | -1 | -339 |  | The answer directly addresses the startup sequence with concrete function names (main, NewConfig, StartServer, Valida... |
| flow-005 | execution_flow | sourcebridge | 2 | 1 | -1 | -495 |  | The answer explicitly admits it couldn't retrieve the specific handler and provides only a generic, hypothetical flow... |
| own-010 | ownership | sourcebridge | 2 | 1 | -1 | -601 |  | The answer admits it didn't find the actual .proto files and only speculates about possible locations. It doesn't con... |
| mix-007 | behavior | sourcebridge | 1 | 0 | -1 | -1687 |  | The answer admits it ran out of retrieval budget and fabricates plausible-sounding function names (`best_deep_files`,... |

## Decision Rule check (plan §Phase 4)

- overall answer-useful within ±7%: **PASS** (Δ=+1.67%)
- per-class within ±10%: **FAIL**
- latency p95 within 2× baseline: **PASS**
- top-20 regressions reviewed and signed off by a human: ☐ (tick manually after review)


---

## Surgical rollout analysis (post-Phase-5.1)

Baseline: Phase-3 agentic candidate (65.83% useful-rate).
Candidate: surgical config — agentic + prompt caching + decomposition-for-architecture-only + smart classifier with `file_candidates` suppressed.

### Comparison of all three configs against agentic baseline

| Metric | Agentic (B3) | Full quality push | **Surgical** |
|--------|--------------|-------------------|--------------|
| Overall | 65.83% | 69.17% (+3.33%) | **67.50% (+1.67%)** |
| architecture | 68% | 84% (+16%) | **84% (+16%)** |
| behavior | 45% | 50% (+5%) | **50% (+5%)** |
| cross_cutting | 56% | 56% (+0%) | **56% (+0%)** |
| execution_flow | 80% | 84% (+4%) | **72% (-8%)** |
| ownership | 76% | 68% (-8%) | **72% (-4%)** |
| p95 latency | 44.2s | 70.5s | **65.5s** |

### What the surgical config fixed

- **Ownership improved from -8% to -4%**: dropping `file_candidates` from the seed removed the fabrication failure mode in 4 of the 8 regressed ownership questions. Judge rationales no longer say "plausible path... appears fabricated".

### What the surgical config broke

- **Execution_flow regressed from +4% to -8%**: the same `file_candidates` that caused ownership fabrication were *helpful* for execution_flow. Judge rationales now say things like "admits it couldn't retrieve the specific handler" and "explicitly admits it cannot determine the decision logic". The model traded fabrication for honest punting — good for ownership, bad for questions that need a starting file to trace a flow through.

### Root cause of the tradeoff

`file_candidates` has opposite effects by class:

- Ownership ("where is X defined?"): treats candidates as citation anchors → fabricates. Hiding them forces real evidence.
- Execution_flow ("how does request Y flow through the stack?"): needs a seed file to trace from. Hiding candidates leaves the model searching without anchoring, often missing or picking the wrong entry point.

### Next fix: class-conditional `file_candidates` surfacing

Surface `file_candidates` only when the profiled kind benefits from a seed entry point (architecture, execution_flow, cross_cutting), hide them for classes that reward verification (ownership, behavior). The smart-classifier's `file_candidates` field stays populated; the seed-builder decides what to render.

Expected next-run profile vs this surgical run:
- architecture: unchanged at 84%
- execution_flow: recover to ~80-84% (file_candidates shown, flow traceable)
- ownership: hold at ~72-76% (file_candidates hidden, no fabrication)
- behavior: hold at ~50% (file_candidates hidden)
- cross_cutting: slight lift possible (file_candidates shown)

Net: overall should land 70%+ with no class down > 4%.
