// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Thin aggregator for the command registration split.
 *
 * The legacy monolith (21 command handlers + 14 helpers in one file)
 * was split in 0.3.0 into per-feature modules:
 *
 *   - auth    — signIn / signOut / configure
 *   - review  — discussCode / runReview
 *   - explain — explainSystem / explainFile / explainSymbol
 *   - knowledge — generateCliffNotes* / generateLearningPath /
 *     generateCodeTour / openKnowledgeScope / showKnowledge /
 *     refreshKnowledge / setKnowledgeLens
 *   - requirements view — showRequirements / showLinkedRequirements /
 *     showRequirementDetail / filterRequirements /
 *     clearRequirementsFilter / toggleRequirementsGrouping
 *   - repository — switchRepository / showImpactReport
 *
 * Shared helpers live in `common.ts`. The CRUD, ask, and scoped-palette
 * modules are registered separately from extension.ts and do not flow
 * through this aggregator.
 */

import * as vscode from "vscode";
import { SourceBridgeClient } from "../graphql/client";
import { CommandDependencies } from "./common";
import { registerAuthCommands } from "./authCommands";
import { registerReviewCommands } from "./reviewCommands";
import { registerExplainCommands } from "./explainCommands";
import { registerKnowledgeCommands } from "./knowledgeCommands";
import { registerRequirementsViewCommands } from "./requirementsViewCommands";
import { registerRepositoryCommands } from "./repositoryCommands";

export type { CommandDependencies } from "./common";

export function registerCommands(
  context: vscode.ExtensionContext,
  client: SourceBridgeClient,
  deps: CommandDependencies = {},
): void {
  registerAuthCommands(context, client);
  registerReviewCommands(context, client, deps);
  registerExplainCommands(context, client);
  registerKnowledgeCommands(context, client, deps);
  registerRequirementsViewCommands(context, client, deps);
  registerRepositoryCommands(context, client, deps);
}
