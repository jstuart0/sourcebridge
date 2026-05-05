# Living Wiki orchestrator contract

The Living Wiki cold-start pipeline uses two nested orchestrators. This document
describes their responsibilities, the contract between them, and the design
decisions operators and contributors need to know.

## Two-orchestrator nesting

### Outer orchestrator (`internal/llm/orchestrator`)

Owns the **queue slot**, the persistent **job record**, and the **Admin Monitor
page entry**. It enqueues a single-attempt job (`MaxAttempts: 1`) whose
`RunWithContext` closure invokes the inner orchestrator's `Generate`.

### Inner orchestrator (`internal/livingwiki/orchestrator`)

Owns **page-level parallelism**, **quality-gate evaluation**, and the **publish
flow** within that queue slot.

The inner orchestrator is always invoked inside the outer orchestrator's
`RunWithContext` closure. It is not directly enqueued.

## Why MaxAttempts is 1

`MaxAttempts: 1` is intentional on the outer enqueue. The inner orchestrator
has its own per-page retry and quality-gate policy. Double-retrying at the outer
level would rerun the entire generation from scratch if a single page failed
quality gates — incorrect behavior. The inner orchestrator handles partial
failure and retry itself.

## What the inner orchestrator does

1. Receives the run context from the outer queue slot.
2. Generates wiki pages in parallel (bounded concurrency).
3. Evaluates each page against quality gates. Quality gate thresholds are
   tier-aware: a `local` model gets relaxed thresholds; a `frontier` model gets
   strict ones. See [`docs/admin/llm-config.md`](../admin/llm-config.md#capability-tiers-and-quality-gates).
4. Pages that pass gates are published. Pages that fail retry up to the inner
   policy limit, then are marked failed without retrying the outer job.

## Operator implications

- Restarting the outer job (via Admin Monitor → retry) re-runs the **entire**
  cold-start, including pages that already passed and were published. This is
  expected; the inner orchestrator is designed for idempotent re-runs.
- If a cold-start is stuck (inner orchestrator hung), restart the pod. The
  outer queue's heartbeat-stale reaper will detect the job as stuck in
  approximately 5 minutes (CA-141) and make it retriable.
- Per-page visibility during a run is available in Admin Monitor (CA-144).

## Source reference

The authoritative package-level description is in `internal/livingwiki/orchestrator/doc.go`
(as of commit `89c85f3`). The audit context that prompted this documentation is
in `thoughts/shared/audits/2026-05-04-system-audit-refactor.bob.md` (finding A-C1).

## Related

- Plane ticket: [CA-155](https://plane.xmojo.net/agile-solutions-group/projects/d3fa4bd8-1177-4364-88a7-aae69698b75d/issues/797d0038-6493-49dc-8307-d7c54d3f6611/) (Phase 2 — Living Wiki typing)
- Plan: [`thoughts/shared/plans/2026-05-04-system-audit-refactor.md`](../../thoughts/shared/plans/2026-05-04-system-audit-refactor.md) Phase 2
- Code: [`internal/livingwiki/orchestrator/doc.go`](../../internal/livingwiki/orchestrator/doc.go)

---
*Documented by scott on 2026-05-04.*
