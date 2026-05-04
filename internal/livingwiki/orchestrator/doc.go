// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package orchestrator implements the inner per-run execution engine for the
// living-wiki cold-start pipeline. It is always invoked inside an
// llm.EnqueueRequest.RunWithContext closure that wraps it.
//
// The two-orchestrator nesting:
//
//   - The outer llm.Orchestrator (internal/llm/orchestrator) owns the queue
//     slot, the job record, and the Monitor page entry. It enqueues a
//     single-attempt job (MaxAttempts: 1) whose RunWithContext closure invokes
//     this package's Generate.
//
//   - The inner orchestrator (this package) owns page-level parallelism,
//     quality gates, and the PR flow within that slot.
//
// MaxAttempts is intentionally 1 on the outer enqueue: the inner orchestrator
// has its own per-page retry/quality-gate policy and should NOT be
// double-retried by the outer queue.
//
// See thoughts/shared/audits/2026-05-04-system-audit-refactor.bob.md (A-C1)
// for the audit context that prompted this doc.
package orchestrator
