import { describe, it, expect } from "vitest";

import {
  understandingStageHasReadableContent,
  understandingStageIsRunning,
  type RepositoryUnderstandingStage,
} from "./understanding";

describe("understandingStageIsRunning", () => {
  // Mirrors `RepositoryUnderstandingStage.IsRunning()` in
  // internal/knowledge/models.go. If a Go-side stage is reclassified
  // (running ↔ terminal) the matching row must be flipped here too.
  const cases: Array<[RepositoryUnderstandingStage, boolean]> = [
    ["BUILDING_TREE", true],
    ["DEEPENING", true],
    ["FIRST_PASS_READY", false],
    ["NEEDS_REFRESH", false],
    ["READY", false],
    ["FAILED", false],
  ];

  it.each(cases)("classifies %s as running=%s", (stage, expected) => {
    expect(understandingStageIsRunning(stage)).toBe(expected);
  });

  it("returns false for null", () => {
    expect(understandingStageIsRunning(null)).toBe(false);
  });

  it("returns false for undefined", () => {
    expect(understandingStageIsRunning(undefined)).toBe(false);
  });

  // Defensive: an unknown string (e.g., a stage the server added but the
  // client codegen hasn't picked up yet) must NOT be treated as running.
  // Showing an indefinite spinner for an unrecognised state is the bug
  // class this whole module exists to prevent.
  it("returns false for unknown stage strings", () => {
    expect(understandingStageIsRunning("WHATEVER" as RepositoryUnderstandingStage)).toBe(false);
    expect(understandingStageIsRunning("")).toBe(false);
  });
});

describe("understandingStageHasReadableContent", () => {
  it("returns true for FIRST_PASS_READY", () => {
    expect(understandingStageHasReadableContent("FIRST_PASS_READY")).toBe(true);
  });

  it("returns true for READY", () => {
    expect(understandingStageHasReadableContent("READY")).toBe(true);
  });

  it.each<RepositoryUnderstandingStage>(["BUILDING_TREE", "DEEPENING", "NEEDS_REFRESH", "FAILED"])(
    "returns false for %s",
    (stage) => {
      expect(understandingStageHasReadableContent(stage)).toBe(false);
    },
  );

  it("returns false for null/undefined", () => {
    expect(understandingStageHasReadableContent(null)).toBe(false);
    expect(understandingStageHasReadableContent(undefined)).toBe(false);
  });
});
