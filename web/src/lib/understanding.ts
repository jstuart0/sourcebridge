// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * UI-side mirror of the Go `RepositoryUnderstandingStage.IsRunning()` contract
 * defined in `internal/knowledge/models.go`. Living the same invariant here
 * is intentional: previously, a UI surface read `progress` (a heartbeat field
 * that is zeroed by the store on terminal stages) to decide "is generating?"
 * and got stuck showing "Generating..." forever for any repo whose
 * understanding had finished its first pass but not yet been deepened.
 *
 * The documented contract (see `internal/knowledge/models.go:301-312` and the
 * SurrealStore enforcement at `internal/db/knowledge_store.go:1128-1142`):
 *
 *   - Only `BUILDING_TREE` and `DEEPENING` are running stages.
 *   - On any non-running stage write the store zeroes progress / phase /
 *     message — those fields carry live heartbeat text only.
 *   - The UI must therefore key the "is busy?" decision on `stage`, never on
 *     `progress`, `progressPhase`, or `progressMessage`.
 *
 * Every UI check for "should we render a progress spinner / disable the
 * Build button?" must go through `understandingStageIsRunning`. Adding a
 * new stage on the Go side without classifying it here will be caught by
 * the union-type exhaustiveness check below.
 */

// Mirrors `RepositoryUnderstandingStage` GraphQL enum values. Update this
// union whenever the schema (`internal/api/graphql/models_gen.go`) adds a
// stage; the exhaustiveness check at the bottom of the file will block
// compilation if this union and the running/terminal classifications
// fall out of sync.
export type RepositoryUnderstandingStage =
  | "BUILDING_TREE"
  | "FIRST_PASS_READY"
  | "NEEDS_REFRESH"
  | "DEEPENING"
  | "READY"
  | "FAILED";

const RUNNING_STAGES = ["BUILDING_TREE", "DEEPENING"] as const satisfies readonly RepositoryUnderstandingStage[];

// Listed alongside `RUNNING_STAGES` so that the union below is exhaustive
// against `RepositoryUnderstandingStage`. Any new stage on the Go side that
// isn't placed in one of the two lists will fail the `_Exhaustive` type
// alias evaluation and break the build, forcing the author to classify it.
type RunningStage = (typeof RUNNING_STAGES)[number];
type TerminalStage = "FIRST_PASS_READY" | "NEEDS_REFRESH" | "READY" | "FAILED";

// Compile-time exhaustiveness: this type alias resolves to `true` only when
// the running/terminal classifications cover the full stage union exactly.
// A missing or extra stage produces `false` and a downstream `never`-style
// usage failure.
type _Exhaustive = RunningStage | TerminalStage extends RepositoryUnderstandingStage
  ? RepositoryUnderstandingStage extends RunningStage | TerminalStage
    ? true
    : false
  : false;
const _exhaustive: _Exhaustive = true;
void _exhaustive;

/**
 * Returns true when an understanding build is actively running for the
 * given stage. Mirrors `RepositoryUnderstandingStage.IsRunning()` in
 * `internal/knowledge/models.go`.
 *
 * Pass `null` / `undefined` (e.g. when the understanding hasn't been built
 * yet) and the result is `false` — there is nothing running.
 */
export function understandingStageIsRunning(
  stage: RepositoryUnderstandingStage | string | null | undefined,
): boolean {
  if (!stage) return false;
  return (RUNNING_STAGES as readonly string[]).includes(stage);
}

/**
 * Returns true when the given stage represents a build that has produced
 * data the user can read (first-pass summary or fully-deepened
 * understanding). Used by post-mutation dedupe-note logic to recognise
 * "your click was joined to a build that is/was already in flight, and
 * data is available now".
 */
export function understandingStageHasReadableContent(
  stage: RepositoryUnderstandingStage | string | null | undefined,
): boolean {
  return stage === "FIRST_PASS_READY" || stage === "READY";
}
