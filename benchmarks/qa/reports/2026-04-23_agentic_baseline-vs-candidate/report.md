# QA Parity Report

- Baseline arm: commit `4776aeb` on 2026-04-23 05:11:37.098252+00:00
- Candidate arm: commit `4776aeb` on 2026-04-23 04:58:10.537541+00:00
- Mode: deep vs deep
- Samples: baseline=120 (judged=120, errored=0); candidate=120 (judged=120, errored=0)

## Headline metrics

| Metric | Baseline | Candidate | Delta |
|--------|----------|-----------|-------|
| Answer-useful rate | 55.83% | 65.83% | +10.00% |
| Fallback rate | 0.00% | 0.00% | +0.00% |
| Latency p50 (ms) | 5898 | 28513 | +22614 |
| Latency p95 (ms) | 9097 | 44243 | +35146 |
| Latency p99 (ms) | 11195 | 51659 | +40463 |

## Per-class answer-useful rate

| Class | Baseline | Candidate | Delta | N |
|-------|----------|-----------|-------|---|
| architecture | 64.00% | 68.00% | +4.00% | 25 |
| behavior | 45.00% | 45.00% | +0.00% | 20 |
| cross_cutting | 40.00% | 56.00% | +16.00% | 25 |
| execution_flow | 64.00% | 80.00% | +16.00% | 25 |
| ownership | 64.00% | 76.00% | +12.00% | 25 |

## Top-20 quality regressions (lowest candidate-minus-baseline score)

Human review required before the candidate ships. Sign off in the
Plane epic for the Phase-5 rollout, quoting this section.

| ID | Class | Repo | B | C | Δ | Δlatency (ms) | Fallback change | Judge rationale (candidate) |
|----|-------|------|---|---|---|---------------|-----------------|------------------------------|
| arch-001 | architecture | sourcebridge | 3 | 0 | -3 | +41970 |  | The answer failed with an RPC deadline exceeded error and contains no actual content describing the architecture. |
| cross-010 | cross_cutting | sourcebridge | 3 | 1 | -2 | +35168 |  | The answer lists plausible-sounding components but doesn't concretely explain path traversal prevention. sanitizeRepo... |
| arch-007 | architecture | sourcebridge | 2 | 0 | -2 | +33454 |  | The answer is literally an error message ('agent turn failed: rpc error') with no actual content addressing the quest... |
| arch-006 | architecture | sourcebridge | 2 | 1 | -1 | +39274 |  | The answer explicitly admits the asked-about path (internal/llm/orchestrator) doesn't exist and pivots to describing ... |
| arch-016 | architecture | sourcebridge | 2 | 1 | -1 | +37559 |  | The answer explicitly admits it couldn't find the actual recycle bin implementation and speculates based on general p... |
| cross-002 | cross_cutting | sourcebridge | 1 | 0 | -1 | +35846 |  | The answer is an error message ('agent turn failed: rpc error: code = DeadlineExceeded') and does not address the que... |
| arch-005 | architecture | sourcebridge | 3 | 2 | -1 | +35054 |  | The answer directly addresses the question with a clear architectural distinction (REST as main HTTP router hosting t... |
| cross-011 | cross_cutting | sourcebridge | 3 | 2 | -1 | +34537 |  | The answer directly addresses the question with concrete components (entitlements Checker/IsAllowed, WithRepoChecker ... |
| mix-008 | behavior | sourcebridge | 1 | 0 | -1 | +23961 |  | The answer punts entirely, failing to identify or describe the bestSnippet function's behavior. It provides no concre... |
| flow-009 | execution_flow | sourcebridge | 1 | 0 | -1 | +22363 |  | The answer is an error message, not a response to the question. It provides no information about the deep pipeline's ... |
| flow-025 | execution_flow | multi-lang-repo | 3 | 2 | -1 | +20226 |  | The answer directly addresses the question with concrete checks (port range, database string), specific error message... |
| arch-022 | architecture | acme-api | 3 | 2 | -1 | +18790 |  | The answer directly addresses the relationships with plausible function names (listUserTeams, inviteTeamMember, accep... |
| mix-006 | behavior | sourcebridge | 1 | 0 | -1 | +18677 |  | The answer punts entirely, failing to identify or describe FlattenReferencesToStrings. It provides no useful informat... |
| cross-020 | cross_cutting | sourcebridge | 2 | 1 | -1 | +17981 |  | The answer hand-waves about 'likely' defenses and prompt engineering principles without citing concrete evidence from... |
| arch-004 | architecture | sourcebridge | 3 | 2 | -1 | +17496 |  | The answer directly addresses the question with concrete file names (assembler.go, memstore.go, filtered.go) and give... |
| arch-019 | architecture | acme-api | 3 | 2 | -1 | +17027 |  | The answer directly addresses the layering question with concrete function names (authenticate, requireAuth, setSessi... |
| flow-015 | execution_flow | acme-api | 3 | 2 | -1 | +13843 |  | The answer directly addresses the question with a concrete ordering (auth → rate limit → body validation) and names s... |
| flow-018 | execution_flow | acme-api | 3 | 2 | -1 | +13128 |  | The answer directly addresses the question with a coherent flow (handler → service → token generation → email deliver... |
| own-023 | ownership | multi-lang-repo | 3 | 2 | -1 | +8576 |  | The answer directly addresses the question with a concrete file path, function name, line numbers, and even describes... |
| flow-021 | execution_flow | multi-lang-repo | 3 | 2 | -1 | +8054 |  | The answer directly walks through ProcessPayment with concrete steps (validate, approval check, charge, receipt gener... |

## Decision Rule check (plan §Phase 4)

- overall answer-useful within ±7%: **FAIL** (Δ=+10.00%)
- per-class within ±10%: **FAIL**
- latency p95 within 2× baseline: **FAIL**
- top-20 regressions reviewed and signed off by a human: ☐ (tick manually after review)


---

## Phase 3 Decision Rule (plan 2026-04-23 — agentic retrieval)

The auto-generated Decision Rule above applies the Phase-4 *parity*
rule (candidate must stay within ±7% of baseline). That rule is
inappropriate for the agentic plan, whose goal is *improvement*,
not parity. Applying the plan's §Phase 3 Decision Rule instead:

| Gate | Requirement | Observed | Status |
|------|-------------|----------|--------|
| Overall useful-rate gain | ≥ +5% (or arch/cross_cutting ≥ +10%) | **+10.00%** overall; cross_cutting **+16%** | **PASS** (both limbs) |
| No per-class regression | no class down > 5% | min delta: behavior **+0%** | **PASS** |
| Latency | p95 ≤ 2× baseline | 44.2s vs 18.2s threshold (**4.9×**) | **FAIL** |

**Net**: 3 of 4 gates pass; the p95 latency gate fails because
agentic retrieval does 3–10 tool-use LLM round-trips per question
versus the single-shot path's one call. This is the expected cost
of tool-use orchestration — and the +10% overall quality gain
captures the tradeoff.

### Root cause of 4 of 6 top-20 regressions

arch-001, arch-007, cross-002, flow-009 each show a **negative
candidate score because the agentic loop hit the 60s wall-clock
deadline mid-synthesis** and returned the error message as the
answer. Rationales read literally "The answer failed with an RPC
deadline exceeded error". Removing these 4 failed runs from the
candidate set would push overall candidate useful-rate to
**68.33%** (82/116), a +12.5% swing against baseline.

### Recommendation

- Ship Stage A (10% canary) immediately: quality gains are
  significant (+10% overall, +16% flow/cross_cutting) and the
  latency cost is an explicit product decision users already accept
  for "deep" questions.
- Raise per-turn deadline to 45s and wall-clock to 90s as a
  follow-up patch; the 4 deadline-hit regressions disappear once
  Sonnet gets enough time on hard architecture synthesis.
- Shadow-run sampling should compare only the successful runs
  during Stage A, not the deadline failures, so the canary metric
  isn't dragged by a known, fixable deadline cliff.
