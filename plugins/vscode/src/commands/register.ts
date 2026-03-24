import * as vscode from "vscode";
import {
  SourceBridgeClient,
  KnowledgeArtifact,
  Repository,
  Requirement,
  RequirementLink,
  ScopeType,
  SymbolNode,
} from "../graphql/client";
import { DiscussionTreeProvider } from "../views/discussionTree";
import { KnowledgeTreeProvider, isKnowledgeArtifact, isKnowledgeScopeContext } from "../views/knowledgeTree";
import { RequirementsTreeProvider } from "../views/requirementsTree";
import { createDiscussionPanel, DiscussionData } from "../panels/discussionPanel";
import { createReviewPanel, ReviewData, ReviewFinding } from "../panels/reviewPanel";
import { createExplainPanel, createKnowledgePanel, updateKnowledgePanel } from "../panels/knowledgePanel";
import { createRequirementPanel } from "../panels/requirementPanel";
import { createImpactPanel } from "../panels/impactPanel";
import { getCapabilities } from "../context/capabilities";
import {
  createFileScope,
  createRepositoryScope,
  inferFileScope,
  inferSymbolScope,
  ScopeContext,
  toGraphQLScopeType,
} from "../context/scope";
import {
  getCurrentWorkspaceFolder,
  resolveRepository,
  switchRepository,
  toRelativePosixPath,
} from "../context/repositories";
import { openWorkspaceLocation, parseFileReference } from "../panels/utils";

interface CommandDependencies {
  discussionTree?: DiscussionTreeProvider;
  knowledgeTree?: KnowledgeTreeProvider;
  requirementsTree?: RequirementsTreeProvider;
}

function classifyError(err: unknown): string {
  const msg = err instanceof Error ? err.message : String(err);
  if (
    msg.includes("AI features are unavailable") ||
    msg.includes("source unavailable")
  ) {
    return "AI features are currently unavailable on the server. Check that the LLM backend is configured and running.";
  }
  if (msg.includes("GraphQL request failed")) {
    return `SourceBridge server returned an error: ${msg}`;
  }
  return msg;
}

async function ensureServer(client: SourceBridgeClient): Promise<boolean> {
  const running = await client.isServerRunning();
  if (!running) {
    vscode.window.showErrorMessage(
      "SourceBridge server not running. Start it with `sourcebridge serve`."
    );
    return false;
  }
  return true;
}

async function resolveWorkspaceRepository(
  context: vscode.ExtensionContext,
  client: SourceBridgeClient,
  workspaceFolder?: vscode.WorkspaceFolder
): Promise<{ repository: Repository; workspaceFolder: vscode.WorkspaceFolder } | undefined> {
  const folder = workspaceFolder || getCurrentWorkspaceFolder();
  if (!folder) {
    vscode.window.showErrorMessage("No workspace folder open.");
    return undefined;
  }
  const repository = await resolveRepository(client, folder, context);
  if (!repository) {
    return undefined;
  }
  return { repository, workspaceFolder: folder };
}

async function resolveEditorRepository(
  context: vscode.ExtensionContext,
  client: SourceBridgeClient
): Promise<{
  editor: vscode.TextEditor;
  repository: Repository;
  workspaceFolder: vscode.WorkspaceFolder;
} | undefined> {
  const editor = vscode.window.activeTextEditor;
  if (!editor) {
    vscode.window.showWarningMessage("No active editor");
    return undefined;
  }
  const workspaceFolder = vscode.workspace.getWorkspaceFolder(editor.document.uri);
  if (!workspaceFolder) {
    vscode.window.showErrorMessage("The active file is not inside a workspace folder.");
    return undefined;
  }
  const repository = await resolveRepository(client, workspaceFolder, context);
  if (!repository) {
    return undefined;
  }
  return { editor, repository, workspaceFolder };
}

function getActiveEditorContext(): {
  editor: vscode.TextEditor;
  workspaceFolder: vscode.WorkspaceFolder;
} | undefined {
  const editor = vscode.window.activeTextEditor;
  if (!editor) {
    vscode.window.showWarningMessage("No active editor");
    return undefined;
  }
  const workspaceFolder = vscode.workspace.getWorkspaceFolder(editor.document.uri);
  if (!workspaceFolder) {
    vscode.window.showErrorMessage("The active file is not inside a workspace folder.");
    return undefined;
  }
  return { editor, workspaceFolder };
}

async function ensureScopedKnowledge(client: SourceBridgeClient): Promise<boolean> {
  const capabilities = await getCapabilities(client);
  if (!capabilities.scopedKnowledge) {
    vscode.window.showWarningMessage("Scoped knowledge is not available on this server yet.");
    return false;
  }
  return true;
}

async function ensureScopedExplain(client: SourceBridgeClient): Promise<boolean> {
  const capabilities = await getCapabilities(client);
  if (!capabilities.scopedExplain) {
    vscode.window.showWarningMessage("Scoped explain is not available on this server yet.");
    return false;
  }
  return true;
}

async function ensureImpactReports(client: SourceBridgeClient): Promise<boolean> {
  const capabilities = await getCapabilities(client);
  if (!capabilities.impactReports) {
    vscode.window.showWarningMessage("Impact reports are not available on this server yet.");
    return false;
  }
  return true;
}

async function openDiscussionReference(
  workspaceFolder: vscode.WorkspaceFolder,
  reference: string
): Promise<void> {
  const { filePath, line } = parseFileReference(reference);
  await openWorkspaceLocation(workspaceFolder, filePath, line);
}

async function openReviewFinding(
  workspaceFolder: vscode.WorkspaceFolder,
  finding: ReviewFinding
): Promise<void> {
  if (!finding.filePath) {
    return;
  }
  await openWorkspaceLocation(workspaceFolder, finding.filePath, finding.line);
}

async function getCurrentSymbol(
  client: SourceBridgeClient,
  repository: Repository,
  workspaceFolder: vscode.WorkspaceFolder,
  editor: vscode.TextEditor
): Promise<SymbolNode | undefined> {
  const filePath = toRelativePosixPath(editor.document.uri.fsPath, workspaceFolder);
  const symbols = await client.getSymbolsForFile(repository.id, filePath);
  const activeLine = editor.selection.active.line + 1;
  return symbols
    .filter((symbol) => activeLine >= symbol.startLine && activeLine <= symbol.endLine)
    .sort((a, b) => (a.endLine - a.startLine) - (b.endLine - b.startLine))[0];
}

async function openRequirementDetail(
  context: vscode.ExtensionContext,
  client: SourceBridgeClient,
  requirement: Requirement,
  links: RequirementLink[],
  scope: { workspaceFolder: vscode.WorkspaceFolder; repository: Repository },
  deps: CommandDependencies
): Promise<void> {
  createRequirementPanel(requirement, links, {
    onOpenSymbol: async (link) => {
      if (link.symbol?.filePath) {
        await openWorkspaceLocation(
          scope.workspaceFolder,
          link.symbol.filePath,
          link.symbol.startLine
        );
      }
    },
    onVerify: async (linkId, verified) => {
      await client.verifyLink(linkId, verified);
      deps.requirementsTree?.refresh();
      const refreshedLinks = await client.getRequirementToCode(requirement.id);
      await openRequirementDetail(context, client, requirement, refreshedLinks, scope, deps);
    },
  });
}

async function generateArtifactForScope(
  client: SourceBridgeClient,
  scope: ScopeContext,
  type: string,
  audience?: string,
  depth?: string
): Promise<KnowledgeArtifact> {
  switch (type) {
    case "learning_path":
      return client.generateLearningPath(scope.repositoryId, audience, depth);
    case "code_tour":
      return client.generateCodeTour(scope.repositoryId, audience, depth);
    default:
      return client.generateCliffNotes(
        scope.repositoryId,
        audience,
        depth,
        toGraphQLScopeType(scope.scopeType),
        scope.scopePath
      );
  }
}

async function findArtifactForLens(
  client: SourceBridgeClient,
  scope: ScopeContext,
  type: string,
  audience: string,
  depth: string
): Promise<KnowledgeArtifact | undefined> {
  const artifacts = await client.getKnowledgeArtifacts(
    scope.repositoryId,
    toGraphQLScopeType(scope.scopeType),
    scope.scopePath
  );
  return artifacts.find(
    (artifact) =>
      artifact.type === type &&
      artifact.audience === audience &&
      artifact.depth === depth
  );
}

async function chooseRequirement(
  client: SourceBridgeClient,
  links: RequirementLink[]
): Promise<{ requirement: Requirement; links: RequirementLink[] } | undefined> {
  type RequirementPick = vscode.QuickPickItem & {
    requirement: Requirement;
    links: RequirementLink[];
  };
  const grouped = new Map<string, RequirementLink[]>();
  for (const link of links) {
    const key = link.requirementId;
    const list = grouped.get(key) || [];
    list.push(link);
    grouped.set(key, list);
  }

  const entries = [...grouped.entries()];
  if (entries.length === 0) {
    return undefined;
  }

  if (entries.length === 1) {
    const [requirementId, reqLinks] = entries[0];
    const requirement = await client.getRequirement(requirementId);
    if (!requirement) {
      return undefined;
    }
    return { requirement, links: reqLinks };
  }

  const items = (
    await Promise.all(
      entries.map(async ([requirementId, reqLinks]) => {
        const requirement = await client.getRequirement(requirementId);
        if (!requirement) {
          return undefined;
        }
        const item: RequirementPick = {
          label: requirement.externalId || requirement.title,
          description: requirement.title,
          detail: requirement.description,
          requirement,
          links: reqLinks,
        };
        return item;
      })
    )
  ).filter((item): item is RequirementPick => !!item);

  const picked = await vscode.window.showQuickPick(items, {
    placeHolder: "Select a requirement",
  });

  if (!picked) {
    return undefined;
  }
  return { requirement: picked.requirement, links: picked.links };
}

async function openKnowledgeScopePanel(
  context: vscode.ExtensionContext,
  client: SourceBridgeClient,
  scope: ScopeContext,
  deps: CommandDependencies,
  artifact?: KnowledgeArtifact
): Promise<void> {
  let currentArtifact =
    artifact;

  if (!currentArtifact) {
    try {
      currentArtifact = (
        await client.getKnowledgeArtifacts(
          scope.repositoryId,
          toGraphQLScopeType(scope.scopeType),
          scope.scopePath
        )
      )[0];
    } catch {
      currentArtifact = undefined;
    }
  }

  if (!currentArtifact) {
    currentArtifact = await client.generateCliffNotes(
      scope.repositoryId,
      undefined,
      undefined,
      toGraphQLScopeType(scope.scopeType),
      scope.scopePath
    );
  }

  const loadChildScopes = async (): Promise<Array<{ label: string; scopeType: ScopeType; scopePath?: string }>> => {
    let capabilities;
    try {
      capabilities = await getCapabilities(client);
    } catch {
      return [];
    }
    if (!capabilities.scopedKnowledge) {
      return [];
    }
    const children = await client.getKnowledgeScopeChildren(
      scope.repositoryId,
      toGraphQLScopeType(scope.scopeType),
      scope.scopePath || "",
      deps.knowledgeTree?.getLens().audience,
      deps.knowledgeTree?.getLens().depth
    );
    return children.map((child) => ({
      label: child.label,
      scopeType: child.scopeType.toLowerCase() as ScopeType,
      scopePath: child.scopePath,
    }));
  };

  const panel = createKnowledgePanel(currentArtifact, scope, {
    onOpenLocation: async (filePath, line) => {
      await openWorkspaceLocation(scope.workspaceFolder, filePath, line);
    },
    onRefresh: async () => {
      if (currentArtifact) {
        const refreshed = await client.getKnowledgeArtifact(currentArtifact.id);
        if (refreshed) {
          currentArtifact = refreshed;
          updateKnowledgePanel(panel, currentArtifact, scope, await loadChildScopes());
          deps.knowledgeTree?.refresh();
        }
      }
    },
    onRegenerate: async () => {
      const artifactType = currentArtifact!.type;
      currentArtifact = await generateArtifactForScope(
        client,
        scope,
        artifactType,
        deps.knowledgeTree?.getLens().audience,
        deps.knowledgeTree?.getLens().depth
      );
      updateKnowledgePanel(panel, currentArtifact, scope, await loadChildScopes());
      deps.knowledgeTree?.refresh();
      if (currentArtifact.status === "GENERATING") {
        startKnowledgePolling(client, currentArtifact.id, panel, scope, deps, loadChildScopes);
      }
    },
    onSetLens: async (audience, depth) => {
      deps.knowledgeTree?.setLens(audience, depth);
      const artifactType = currentArtifact!.type;
      currentArtifact =
        (await findArtifactForLens(client, scope, artifactType, audience, depth)) ||
        (await generateArtifactForScope(client, scope, artifactType, audience, depth));
      updateKnowledgePanel(panel, currentArtifact, scope, await loadChildScopes());
      deps.knowledgeTree?.refresh();
      if (currentArtifact.status === "GENERATING") {
        startKnowledgePolling(client, currentArtifact.id, panel, scope, deps, loadChildScopes);
      }
    },
    onOpenChildScope: async (scopeType, scopePath) => {
      const nextScope: ScopeContext = {
        ...scope,
        scopeType,
        scopePath,
      };
      if (scopeType === "file") {
        nextScope.filePath = scopePath;
      }
      if (scopeType === "symbol" && scopePath) {
        const [filePath, symbolName] = scopePath.split("#");
        nextScope.filePath = filePath;
        nextScope.symbolName = symbolName;
      }
      await openKnowledgeScopePanel(context, client, nextScope, deps);
    },
  }, await loadChildScopes());

  if (currentArtifact.status === "GENERATING") {
    startKnowledgePolling(client, currentArtifact.id, panel, scope, deps, loadChildScopes);
  }
}

function startKnowledgePolling(
  client: SourceBridgeClient,
  artifactId: string,
  panel: vscode.WebviewPanel,
  scope: ScopeContext,
  deps: CommandDependencies,
  loadChildScopes: () => Promise<Array<{ label: string; scopeType: ScopeType; scopePath?: string }>>
): void {
  let cancelled = false;
  panel.onDidDispose(() => {
    cancelled = true;
  });

  const poll = async () => {
    if (cancelled) {
      return;
    }
    const latest = await client.getKnowledgeArtifact(artifactId);
    if (!latest) {
      return;
    }
    updateKnowledgePanel(panel, latest, scope, await loadChildScopes());
    deps.knowledgeTree?.refresh();
    if (latest.status === "GENERATING") {
      setTimeout(() => {
        void poll();
      }, 4000);
    }
  };

  setTimeout(() => {
    void poll();
  }, 4000);
}

export function registerCommands(
  context: vscode.ExtensionContext,
  client: SourceBridgeClient,
  deps: CommandDependencies = {}
): void {
  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.discussCode", async () => {
      const active = getActiveEditorContext();
      if (!active) {
        return;
      }
      if (!(await ensureServer(client))) {
        return;
      }
      const editorRepo = await resolveEditorRepository(context, client);
      if (!editorRepo) {
        return;
      }

      const question = await vscode.window.showInputBox({
        prompt: "Ask a question about this code",
        placeHolder: "e.g., What does this function do?",
      });
      if (!question) {
        return;
      }

      const selection = editorRepo.editor.selection;
      const code = editorRepo.editor.document.getText(
        selection.isEmpty ? undefined : selection
      );
      const filePath = toRelativePosixPath(
        editorRepo.editor.document.uri.fsPath,
        editorRepo.workspaceFolder
      );

      try {
        await vscode.window.withProgress(
          {
            location: vscode.ProgressLocation.Notification,
            title: "SourceBridge: discussing code...",
            cancellable: false,
          },
          async () => {
            const result = await client.discussCode(
              editorRepo.repository.id,
              question,
              filePath,
              code,
              editorRepo.editor.document.languageId
            );

            const discussionData: DiscussionData = {
              question,
              answer: result.answer,
              references: result.references ?? [],
              sourceLabel: selection.isEmpty ? "Current editor contents" : "Current selection",
              sourceNote: editorRepo.editor.document.isDirty
                ? "Includes unsaved changes."
                : "Uses the code currently open in your editor.",
            };
            deps.discussionTree?.addDiscussion(question, result.answer);
            createDiscussionPanel(discussionData, async (reference) => {
              await openDiscussionReference(editorRepo.workspaceFolder, reference);
            });
          }
        );
      } catch (err) {
        vscode.window.showErrorMessage(`Code discussion failed: ${classifyError(err)}`);
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.runReview", async () => {
      const active = getActiveEditorContext();
      if (!active) {
        return;
      }
      if (!(await ensureServer(client))) {
        return;
      }
      const editorRepo = await resolveEditorRepository(context, client);
      if (!editorRepo) {
        return;
      }

      const template = await vscode.window.showQuickPick(
        ["security", "solid", "performance", "reliability", "maintainability"],
        { placeHolder: "Select review template" }
      );
      if (!template) {
        return;
      }

      const filePath = toRelativePosixPath(
        editorRepo.editor.document.uri.fsPath,
        editorRepo.workspaceFolder
      );
      const code = editorRepo.editor.document.getText();

      try {
        await vscode.window.withProgress(
          {
            location: vscode.ProgressLocation.Notification,
            title: `SourceBridge: running ${template} review...`,
            cancellable: false,
          },
          async () => {
            const result = await client.reviewCode(
              editorRepo.repository.id,
              filePath,
              template,
              code,
              editorRepo.editor.document.languageId
            );
            const reviewData: ReviewData = {
              template: result.template,
              score: result.score,
              findings: result.findings.map((finding) => ({
                severity: finding.severity,
                category: finding.category,
                message: finding.message,
                line: finding.startLine,
                filePath: finding.filePath || filePath,
                suggestion: finding.suggestion,
              })),
              sourceLabel: "Current editor contents",
              sourceNote: editorRepo.editor.document.isDirty
                ? "Includes unsaved changes."
                : "Uses the code currently open in your editor.",
            };
            createReviewPanel(reviewData, async (finding) => {
              await openReviewFinding(editorRepo.workspaceFolder, finding);
            });
          }
        );
      } catch (err) {
        vscode.window.showErrorMessage(`Code review failed: ${classifyError(err)}`);
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.showRequirements", async () => {
      if (!(await ensureServer(client))) {
        return;
      }
      await vscode.commands.executeCommand("sourcebridge.requirements.focus");
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.showLinkedRequirements", async (symbolId?: string) => {
      const active = getActiveEditorContext();
      if (!active) {
        return;
      }
      if (!(await ensureServer(client))) {
        return;
      }
      const editorRepo = await resolveEditorRepository(context, client);
      if (!editorRepo) {
        return;
      }

      const effectiveSymbolId =
        symbolId ||
        (await getCurrentSymbol(
          client,
          editorRepo.repository,
          editorRepo.workspaceFolder,
          editorRepo.editor
        ))?.id;
      if (!effectiveSymbolId) {
        vscode.window.showWarningMessage("No symbol selected at the current cursor position.");
        return;
      }

      const links = await client.getCodeToRequirements(effectiveSymbolId);
      const choice = await chooseRequirement(client, links);
      if (!choice) {
        vscode.window.showInformationMessage("No linked requirements found.");
        return;
      }

      await openRequirementDetail(
        context,
        client,
        choice.requirement,
        await client.getRequirementToCode(choice.requirement.id),
        editorRepo,
        deps
      );
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.signIn", async () => {
      const config = vscode.workspace.getConfiguration("sourcebridge");
      const currentUrl = config.get<string>("apiUrl", "http://localhost:8080");
      const url = await vscode.window.showInputBox({
        prompt: "SourceBridge server URL",
        value: currentUrl,
        placeHolder: "http://localhost:8080",
      });
      if (!url) {
        return;
      }

      await config.update("apiUrl", url, vscode.ConfigurationTarget.Workspace);
      await client.reloadConfiguration();

      try {
        const authInfo = await client.getDesktopAuthInfo();
        let token = "";

        if (authInfo.oidc_enabled) {
          const choice = await vscode.window.showQuickPick(
            [
              { label: "Sign In With Browser", mode: "oidc" },
              ...(authInfo.local_auth && authInfo.setup_done
                ? [{ label: "Sign In With Password", mode: "local" as const }]
                : []),
            ],
            { placeHolder: "Choose a sign-in method" }
          );
          if (!choice) {
            return;
          }
          if (choice.mode === "oidc") {
            const session = await client.startDesktopOIDC();
            await vscode.env.openExternal(vscode.Uri.parse(session.auth_url));
            token = await vscode.window.withProgress(
              {
                location: vscode.ProgressLocation.Notification,
                title: "SourceBridge: waiting for browser sign-in...",
                cancellable: false,
              },
              async (progress) => {
                const started = Date.now();
                while (Date.now() - started < session.expires_in * 1000) {
                  progress.report({ message: "Complete sign-in in your browser" });
                  const poll = await client.pollDesktopOIDC(session.session_id);
                  if (poll.status === "complete" && poll.token) {
                    return poll.token;
                  }
                  await new Promise((resolve) => setTimeout(resolve, 2000));
                }
                throw new Error("browser sign-in timed out");
              }
            );
          } else {
            const password = await vscode.window.showInputBox({
              prompt: "SourceBridge password",
              password: true,
            });
            if (!password) {
              return;
            }
            token = await client.desktopLocalLogin(password, "VS Code");
          }
        } else if (authInfo.local_auth && authInfo.setup_done) {
          const password = await vscode.window.showInputBox({
            prompt: "SourceBridge password",
            password: true,
          });
          if (!password) {
            return;
          }
          token = await client.desktopLocalLogin(password, "VS Code");
        } else {
          vscode.window.showWarningMessage("This server does not expose a desktop sign-in flow yet.");
          return;
        }

        await client.storeToken(token);
        vscode.window.showInformationMessage(`Signed in to SourceBridge at ${url}`);
      } catch (err) {
        vscode.window.showErrorMessage(`Sign-in failed: ${classifyError(err)}`);
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.signOut", async () => {
      try {
        await client.revokeCurrentToken();
      } catch (err) {
        vscode.window.showWarningMessage(`SourceBridge sign-out could not revoke the current session: ${classifyError(err)}`);
      }
      await client.clearStoredToken();
      vscode.window.showInformationMessage("Signed out of SourceBridge.");
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.configure", async () => {
      const config = vscode.workspace.getConfiguration("sourcebridge");
      const currentUrl = config.get<string>("apiUrl", "http://localhost:8080");

      const url = await vscode.window.showInputBox({
        prompt: "SourceBridge server URL",
        value: currentUrl,
        placeHolder: "http://localhost:8080",
      });
      if (url === undefined) {
        return;
      }

      const token = await vscode.window.showInputBox({
        prompt: "Authentication token (leave empty for no auth)",
        password: true,
        placeHolder: "paste your JWT token",
      });
      if (token === undefined) {
        return;
      }

      await config.update("apiUrl", url, vscode.ConfigurationTarget.Workspace);
      await client.storeToken(token);

      const testClient = new SourceBridgeClient(context);
      if (await testClient.isServerRunning()) {
        vscode.window.showInformationMessage(`Connected to SourceBridge server at ${url}`);
      } else {
        vscode.window.showWarningMessage(
          `Saved settings but could not reach server at ${url}. Make sure the server is running.`
        );
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.explainSystem", async () => {
      if (!(await ensureServer(client))) {
        return;
      }
      const repoCtx = await resolveWorkspaceRepository(context, client);
      if (!repoCtx) {
        return;
      }
      const question = await vscode.window.showInputBox({
        prompt: "Ask a question about this codebase",
        placeHolder: "e.g., How does the authentication flow work?",
      });
      if (!question) {
        return;
      }

      try {
        const result = await client.explainSystem(repoCtx.repository.id, question);
        createExplainPanel(question, result.explanation, createRepositoryScope(repoCtx.repository, repoCtx.workspaceFolder));
      } catch (err) {
        vscode.window.showErrorMessage(`System explanation failed: ${classifyError(err)}`);
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.explainFile", async () => {
      const active = getActiveEditorContext();
      if (!active) {
        return;
      }
      if (!(await ensureServer(client)) || !(await ensureScopedExplain(client))) {
        return;
      }
      const editorRepo = await resolveEditorRepository(context, client);
      if (!editorRepo) {
        return;
      }
      const scope = await inferFileScope(
        editorRepo.repository,
        editorRepo.workspaceFolder,
        editorRepo.editor
      );
      const question = await vscode.window.showInputBox({
        prompt: "Ask a question about this file",
        placeHolder: "e.g., What responsibilities live here?",
      });
      if (!question) {
        return;
      }
      const result = await client.explainSystem(
        scope.repositoryId,
        question,
        undefined,
        toGraphQLScopeType(scope.scopeType),
        scope.scopePath
      );
      createExplainPanel(question, result.explanation, scope);
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.explainSymbol", async () => {
      const active = getActiveEditorContext();
      if (!active) {
        return;
      }
      if (!(await ensureServer(client)) || !(await ensureScopedExplain(client))) {
        return;
      }
      const editorRepo = await resolveEditorRepository(context, client);
      if (!editorRepo) {
        return;
      }
      const scope = await inferSymbolScope(
        client,
        editorRepo.repository,
        editorRepo.workspaceFolder,
        editorRepo.editor
      );
      const question = await vscode.window.showInputBox({
        prompt: "Ask a question about this symbol",
        placeHolder: "e.g., How is this symbol used?",
      });
      if (!question) {
        return;
      }
      const result = await client.explainSystem(
        scope.repositoryId,
        question,
        undefined,
        toGraphQLScopeType(scope.scopeType),
        scope.scopePath
      );
      createExplainPanel(question, result.explanation, scope);
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.generateCliffNotes", async () => {
      if (!(await ensureServer(client))) {
        return;
      }
      const repoCtx = await resolveWorkspaceRepository(context, client);
      if (!repoCtx) {
        return;
      }
      const scope = createRepositoryScope(repoCtx.repository, repoCtx.workspaceFolder);
      const artifact = await client.generateCliffNotes(repoCtx.repository.id);
      await openKnowledgeScopePanel(context, client, scope, deps, artifact);
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.generateCliffNotesForFile", async () => {
      const active = getActiveEditorContext();
      if (!active) {
        return;
      }
      if (!(await ensureServer(client)) || !(await ensureScopedKnowledge(client))) {
        return;
      }
      const editorRepo = await resolveEditorRepository(context, client);
      if (!editorRepo) {
        return;
      }
      const scope = createFileScope(
        editorRepo.repository,
        editorRepo.workspaceFolder,
        toRelativePosixPath(editorRepo.editor.document.uri.fsPath, editorRepo.workspaceFolder)
      );
      const artifact = await client.generateCliffNotes(
        scope.repositoryId,
        undefined,
        undefined,
        toGraphQLScopeType(scope.scopeType),
        scope.scopePath
      );
      await openKnowledgeScopePanel(context, client, scope, deps, artifact);
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.generateCliffNotesForSymbol", async () => {
      const active = getActiveEditorContext();
      if (!active) {
        return;
      }
      if (!(await ensureServer(client)) || !(await ensureScopedKnowledge(client))) {
        return;
      }
      const editorRepo = await resolveEditorRepository(context, client);
      if (!editorRepo) {
        return;
      }
      const scope = await inferSymbolScope(
        client,
        editorRepo.repository,
        editorRepo.workspaceFolder,
        editorRepo.editor
      );
      const artifact = await client.generateCliffNotes(
        scope.repositoryId,
        undefined,
        undefined,
        toGraphQLScopeType(scope.scopeType),
        scope.scopePath
      );
      await openKnowledgeScopePanel(context, client, scope, deps, artifact);
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.generateLearningPath", async () => {
      if (!(await ensureServer(client))) {
        return;
      }
      const repoCtx = await resolveWorkspaceRepository(context, client);
      if (!repoCtx) {
        return;
      }
      const artifact = await client.generateLearningPath(repoCtx.repository.id);
      await openKnowledgeScopePanel(
        context,
        client,
        createRepositoryScope(repoCtx.repository, repoCtx.workspaceFolder),
        deps,
        artifact
      );
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.generateCodeTour", async () => {
      if (!(await ensureServer(client))) {
        return;
      }
      const repoCtx = await resolveWorkspaceRepository(context, client);
      if (!repoCtx) {
        return;
      }
      const artifact = await client.generateCodeTour(repoCtx.repository.id);
      await openKnowledgeScopePanel(
        context,
        client,
        createRepositoryScope(repoCtx.repository, repoCtx.workspaceFolder),
        deps,
        artifact
      );
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.openKnowledgeScope", async (arg1?: unknown, arg2?: unknown) => {
      if (!(await ensureServer(client))) {
        return;
      }
      const scope = isKnowledgeScopeContext(arg1) ? arg1 : undefined;
      const artifact = isKnowledgeArtifact(arg2) ? arg2 : isKnowledgeArtifact(arg1) ? arg1 : undefined;
      if (!scope) {
        const repoCtx = await resolveWorkspaceRepository(context, client);
        if (!repoCtx) {
          return;
        }
        await openKnowledgeScopePanel(
          context,
          client,
          createRepositoryScope(repoCtx.repository, repoCtx.workspaceFolder),
          deps,
          artifact
        );
        return;
      }
      await openKnowledgeScopePanel(context, client, scope, deps, artifact);
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.showKnowledge", async () => {
      if (!(await ensureServer(client))) {
        return;
      }
      await vscode.commands.executeCommand("sourcebridge.knowledge.focus");
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.refreshKnowledge", async () => {
      deps.knowledgeTree?.refresh();
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.setKnowledgeLens", async () => {
      const audience = await vscode.window.showQuickPick(["DEVELOPER", "BEGINNER"], {
        placeHolder: "Knowledge audience",
      });
      if (!audience) {
        return;
      }
      const depth = await vscode.window.showQuickPick(["SUMMARY", "MEDIUM", "DEEP"], {
        placeHolder: "Knowledge depth",
      });
      if (!depth) {
        return;
      }
      deps.knowledgeTree?.setLens(audience, depth);
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.switchRepository", async () => {
      if (!(await ensureServer(client))) {
        return;
      }
      const workspaceFolder = getCurrentWorkspaceFolder();
      if (!workspaceFolder) {
        vscode.window.showErrorMessage("No workspace folder open.");
        return;
      }
      const repository = await switchRepository(client, context, workspaceFolder);
      if (!repository) {
        return;
      }
      vscode.window.showInformationMessage(
        `Using repository ${repository.name} for ${workspaceFolder.name}`
      );
      deps.requirementsTree?.refresh();
      deps.knowledgeTree?.refresh();
      client.clearCaches();
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.showImpactReport", async () => {
      if (!(await ensureServer(client)) || !(await ensureImpactReports(client))) {
        return;
      }
      const repoCtx = await resolveWorkspaceRepository(context, client);
      if (!repoCtx) {
        return;
      }
      const report = await client.getLatestImpactReport(repoCtx.repository.id);
      if (!report) {
        vscode.window.showInformationMessage("No impact report available for this repository.");
        return;
      }
      createImpactPanel(report, async (filePath) => {
        await openWorkspaceLocation(repoCtx.workspaceFolder, filePath);
      });
    })
  );
}
