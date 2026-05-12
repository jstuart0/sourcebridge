/**
 * CA-180: regression test for understandingProgressJobView.
 *
 * Before the fix the function gated on status === "pending" | "generating"
 * and synthesised a fake status:"generating" view for any other live-job
 * status (including "failed"). This meant the repo screen always showed
 * "generating" when a build_repository_understanding job had failed.
 *
 * After the fix the function returns liveJob verbatim whenever it is non-null,
 * and only synthesises a "generating" placeholder when liveJob is null.
 */

import { describe, it, expect } from "vitest";
import { understandingProgressJobView } from "./knowledge-tab";
import type { LLMJobView } from "@/lib/llm/job-types";

// Minimal understanding stub — only fields the synthesised view reads.
const stubUnderstanding = {
  id: "u-1",
  updatedAt: new Date().toISOString(),
  progress: 0.5,
  progressPhase: "building",
  progressMessage: "hang tight",
} as Parameters<typeof understandingProgressJobView>[1];

function makeJob(status: LLMJobView["status"]): LLMJobView {
  return {
    id: "job-1",
    subsystem: "knowledge",
    job_type: "build_repository_understanding",
    status,
    progress: 0.99,
    elapsed_ms: 1000,
    updated_at: new Date().toISOString(),
  };
}

describe("understandingProgressJobView", () => {
  it("returns a failed live job verbatim — does not mask as generating", () => {
    const liveJob = makeJob("failed");
    const result = understandingProgressJobView(liveJob, stubUnderstanding);
    expect(result.status).toBe("failed");
    expect(result.id).toBe(liveJob.id);
  });

  it("returns a generating live job verbatim", () => {
    const liveJob = makeJob("generating");
    const result = understandingProgressJobView(liveJob, stubUnderstanding);
    expect(result.status).toBe("generating");
    expect(result.id).toBe(liveJob.id);
  });

  it("returns a pending live job verbatim", () => {
    const liveJob = makeJob("pending");
    const result = understandingProgressJobView(liveJob, stubUnderstanding);
    expect(result.status).toBe("pending");
    expect(result.id).toBe(liveJob.id);
  });

  it("returns a cancelled live job verbatim", () => {
    const liveJob = makeJob("cancelled");
    const result = understandingProgressJobView(liveJob, stubUnderstanding);
    expect(result.status).toBe("cancelled");
    expect(result.id).toBe(liveJob.id);
  });

  // CA-292 (T-L2): a live job with status "ready" is the documented contract
  // for understandingProgressJobView — it should be returned verbatim, not
  // synthesised as "generating". `ready` is terminal so it doesn't normally
  // appear as a liveJob, but the contract must be pinned so a future
  // simplification can't silently regress it.
  it("returns a ready live job verbatim — does not synthesise generating", () => {
    const liveJob = makeJob("ready");
    const result = understandingProgressJobView(liveJob, stubUnderstanding);
    expect(result.status).toBe("ready");
    expect(result.id).toBe(liveJob.id);
  });

  it("synthesises a generating placeholder when liveJob is null", () => {
    const result = understandingProgressJobView(null, stubUnderstanding);
    expect(result.status).toBe("generating");
    expect(result.id).toBe(stubUnderstanding!.id);
  });

  it("synthesises a generating placeholder when liveJob is undefined", () => {
    const result = understandingProgressJobView(undefined, stubUnderstanding);
    expect(result.status).toBe("generating");
  });
});
