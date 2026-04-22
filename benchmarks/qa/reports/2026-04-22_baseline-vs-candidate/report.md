# QA Parity Report

- Baseline arm: commit `7ca8203` on 2026-04-22 21:39:52.489693+00:00
- Candidate arm: commit `7ca8203` on 2026-04-22 21:27:59.432519+00:00
- Mode: deep vs deep
- Samples: baseline=120 (judged=120, errored=0); candidate=120 (judged=120, errored=0)

## Headline metrics

| Metric | Baseline | Candidate | Delta |
|--------|----------|-----------|-------|
| Answer-useful rate | 64.17% | 63.33% | -0.83% |
| Fallback rate | 0.00% | 0.00% | +0.00% |
| Latency p50 (ms) | 13906 | 6238 | -7667 |
| Latency p95 (ms) | 21278 | 9148 | -12129 |
| Latency p99 (ms) | 24039 | 12088 | -11951 |

## Per-class answer-useful rate

| Class | Baseline | Candidate | Delta | N |
|-------|----------|-----------|-------|---|
| architecture | 80.00% | 64.00% | -16.00% | 25 |
| behavior | 40.00% | 50.00% | +10.00% | 20 |
| cross_cutting | 36.00% | 44.00% | +8.00% | 25 |
| execution_flow | 88.00% | 72.00% | -16.00% | 25 |
| ownership | 72.00% | 84.00% | +12.00% | 25 |

## Top-20 quality regressions (lowest candidate-minus-baseline score)

Human review required before the candidate ships. Sign off in the
Plane epic for the Phase-5 rollout, quoting this section.

| ID | Class | Repo | B | C | Δ | Δlatency (ms) | Fallback change | Judge rationale (candidate) |
|----|-------|------|---|---|---|---------------|-----------------|------------------------------|
| arch-001 | architecture | sourcebridge | 2 | 0 | -2 | +110578 |  | The answer is an HTTP error code (524 timeout) with no content, failing to address the question at all. |
| flow-025 | execution_flow | multi-lang-repo | 3 | 1 | -2 | -4832 |  | The answer punts by saying the context doesn't provide the validation logic, without naming concrete checks, fields, ... |
| cross-004 | cross_cutting | sourcebridge | 3 | 1 | -2 | -10862 |  | The answer punts on the question, explicitly stating the context lacks evidence of the sync mechanism. While it corre... |
| arch-024 | architecture | acme-api | 3 | 2 | -1 | +95 |  | The answer directly addresses the plan-tier system with concrete mechanisms (PLAN_LIMITS constant, MEMBER_LIMIT error... |
| flow-015 | execution_flow | acme-api | 2 | 1 | -1 | -1695 |  | The answer punts on the question, stating the context is insufficient rather than describing the rate-limiting check ... |
| cross-023 | cross_cutting | acme-api | 2 | 1 | -1 | -2346 |  | The answer punts on the question, stating the implementation isn't available rather than providing concrete details a... |
| mix-014 | behavior | acme-api | 2 | 1 | -1 | -2954 |  | The answer punts on the question, stating the JWT verification implementation is not in the provided context rather t... |
| flow-016 | execution_flow | acme-api | 2 | 1 | -1 | -3089 |  | The answer punts on the question, explicitly stating that the implementation of removeTeamMember is not in the provid... |
| flow-023 | execution_flow | multi-lang-repo | 2 | 1 | -1 | -4380 |  | The answer punts by saying the context lacks sufficient evidence, without naming concrete approval logic, thresholds,... |
| flow-021 | execution_flow | multi-lang-repo | 2 | 1 | -1 | -5758 |  | The answer explicitly declines to walk through ProcessPayment, stating the evidence is insufficient. It does not prov... |
| arch-014 | architecture | sourcebridge | 2 | 1 | -1 | -7032 |  | The answer punts on the question, stating the evidence is insufficient rather than describing the three-tier architec... |
| arch-008 | architecture | sourcebridge | 2 | 1 | -1 | -7116 |  | The answer admits it lacks sufficient evidence to describe the MCP tool surface or dispatch mechanism, only partially... |
| arch-010 | architecture | sourcebridge | 2 | 1 | -1 | -7583 |  | The answer punts by saying the provided context doesn't show tenant-filtering logic, rather than identifying the actu... |
| own-009 | ownership | sourcebridge | 2 | 1 | -1 | -7677 |  | The answer punts, stating the registration is not in the provided context rather than identifying where the SSE discu... |
| own-003 | ownership | sourcebridge | 3 | 2 | -1 | -8105 |  | The answer directly identifies a concrete file (internal/api/graphql/discuss_via_orchestrator.go) and method (dispatc... |
| flow-006 | execution_flow | sourcebridge | 2 | 1 | -1 | -8579 |  | The answer punts on the question, stating the context doesn't contain the implementation, rather than providing concr... |
| own-005 | ownership | sourcebridge | 2 | 1 | -1 | -8676 |  | The answer punts by saying no such code is present in the provided evidence, rather than identifying the retired Pyth... |
| cross-007 | cross_cutting | sourcebridge | 2 | 1 | -1 | -10946 |  | The answer punts by saying the context does not contain evidence, without naming concrete handling mechanisms. This i... |
| arch-013 | architecture | sourcebridge | 2 | 1 | -1 | -11099 |  | The answer punts by saying evidence is insufficient, only vaguely mentioning a RequirementLinkCount field and GetLink... |
| cross-020 | cross_cutting | sourcebridge | 3 | 2 | -1 | -12532 |  | The answer directly addresses the prompt injection concern by citing specific prompt files and their grounding direct... |

## Decision Rule check (plan §Phase 4)

- overall answer-useful within ±7%: **PASS** (Δ=-0.83%)
- per-class within ±10%: **FAIL**
- latency p95 within 2× baseline: **PASS**
- top-20 regressions reviewed and signed off by a human: ☐ (tick manually after review)

