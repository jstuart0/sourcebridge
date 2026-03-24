import * as vscode from "vscode";
import { KnowledgeArtifact } from "../graphql/client";
import { ScopeContext, getScopeBreadcrumbs, getScopeLabel } from "../context/scope";
import { createNonce, escapeHtml } from "./utils";

interface ChildScopeAction {
  label: string;
  scopeType: ScopeContext["scopeType"];
  scopePath?: string;
}

interface KnowledgePanelHandlers {
  onOpenLocation: (filePath: string, line?: number) => void | Promise<void>;
  onRefresh?: () => void | Promise<void>;
  onRegenerate?: () => void | Promise<void>;
  onSetLens?: (audience: string, depth: string) => void | Promise<void>;
  onOpenChildScope?: (scopeType: ScopeContext["scopeType"], scopePath?: string) => void | Promise<void>;
}

export function createKnowledgePanel(
  artifact: KnowledgeArtifact,
  scope: ScopeContext,
  handlers: KnowledgePanelHandlers,
  childScopes: ChildScopeAction[] = []
): vscode.WebviewPanel {
  const panel = vscode.window.createWebviewPanel(
    "sourcebridge.knowledge",
    panelTitle(artifact, scope),
    vscode.ViewColumn.Two,
    { enableScripts: true, retainContextWhenHidden: true }
  );

  panel.webview.html = renderKnowledgeHtml(panel.webview, artifact, scope, childScopes);
  panel.webview.onDidReceiveMessage(async (message: unknown) => {
    if (!isMessage(message)) {
      return;
    }
    switch (message.type) {
      case "openLocation":
        if (message.filePath) {
          await handlers.onOpenLocation(message.filePath, message.line);
        }
        break;
      case "refresh":
        await handlers.onRefresh?.();
        break;
      case "regenerate":
        await handlers.onRegenerate?.();
        break;
      case "setLens":
        if (message.audience && message.depth) {
          await handlers.onSetLens?.(message.audience, message.depth);
        }
        break;
      case "openChildScope":
        if (message.scopeType) {
          await handlers.onOpenChildScope?.(message.scopeType, message.scopePath);
        }
        break;
      default:
        break;
    }
  });

  return panel;
}

export function updateKnowledgePanel(
  panel: vscode.WebviewPanel,
  artifact: KnowledgeArtifact,
  scope: ScopeContext,
  childScopes: ChildScopeAction[] = []
): void {
  panel.title = panelTitle(artifact, scope);
  panel.webview.html = renderKnowledgeHtml(panel.webview, artifact, scope, childScopes);
}

export function createExplainPanel(
  question: string,
  explanation: string,
  scope?: ScopeContext
): vscode.WebviewPanel {
  const panel = vscode.window.createWebviewPanel(
    "sourcebridge.explain",
    scope ? `Explain: ${getScopeLabel(scope)}` : "System Explanation",
    vscode.ViewColumn.Two,
    { enableScripts: false }
  );

  panel.webview.html = renderExplainHtml(question, explanation, scope);
  return panel;
}

function panelTitle(artifact: KnowledgeArtifact, scope: ScopeContext): string {
  const kind = artifactTypeLabel(artifact.type);
  return `${kind}: ${getScopeLabel(scope)}`;
}

function artifactTypeLabel(type: string): string {
  switch (type) {
    case "cliff_notes":
      return "Field Guide";
    case "learning_path":
      return "Learning Path";
    case "code_tour":
      return "Code Tour";
    default:
      return type;
  }
}

function renderKnowledgeHtml(
  webview: vscode.Webview,
  artifact: KnowledgeArtifact,
  scope: ScopeContext,
  childScopes: ChildScopeAction[]
): string {
  const nonce = createNonce();
  const breadcrumbs = getScopeBreadcrumbs(scope)
    .map((item) => `<span class="crumb">${escapeHtml(item.label)}</span>`)
    .join('<span class="sep">/</span>');

  const sorted = [...artifact.sections].sort((a, b) => a.orderIndex - b.orderIndex);
  const sectionsHtml = sorted
    .map((section) => {
      const confidenceValue =
        typeof section.confidence === "string"
          ? section.confidence
          : section.confidence >= 0.8
            ? "HIGH"
            : section.confidence >= 0.5
              ? "MEDIUM"
              : "LOW";
      const confidenceClass = confidenceValue.toLowerCase();
      const evidenceHtml = section.evidence
        .map((evidence) => {
          const location = evidence.filePath
            ? `<button class="link-button" data-action="open-location" data-file-path="${escapeHtml(
                evidence.filePath
              )}" data-line="${evidence.lineStart || ""}">${escapeHtml(
                `${evidence.filePath}${evidence.lineStart ? `:${evidence.lineStart}` : ""}`
              )}</button>`
            : "";
          return `<li>${location}${
            evidence.rationale ? ` <span class="muted">${escapeHtml(evidence.rationale)}</span>` : ""
          }</li>`;
        })
        .join("");

      return `<section class="section">
        <header class="section-header">
          <h2>${escapeHtml(section.title)}</h2>
          <span class="confidence ${confidenceClass}">${escapeHtml(confidenceValue)}</span>
        </header>
        ${section.summary ? `<p class="summary">${escapeHtml(section.summary)}</p>` : ""}
        <div class="content">${escapeHtml(section.content)}</div>
        ${
          section.evidence.length > 0
            ? `<div class="evidence"><h3>Evidence</h3><ul>${evidenceHtml}</ul></div>`
            : ""
        }
      </section>`;
    })
    .join("");

  const childrenHtml = childScopes
    .map(
      (child) => `<button
        class="chip"
        data-action="open-child-scope"
        data-scope-type="${escapeHtml(child.scopeType)}"
        data-scope-path="${escapeHtml(child.scopePath || "")}"
      >${escapeHtml(child.label)}</button>`
    )
    .join("");

  const lensButtons = [
    { group: "audience", value: "DEVELOPER", label: "Developer", active: artifact.audience === "DEVELOPER" },
    { group: "audience", value: "BEGINNER", label: "Beginner", active: artifact.audience === "BEGINNER" },
    { group: "depth", value: "SUMMARY", label: "Summary", active: artifact.depth === "SUMMARY" },
    { group: "depth", value: "MEDIUM", label: "Medium", active: artifact.depth === "MEDIUM" },
    { group: "depth", value: "DEEP", label: "Deep", active: artifact.depth === "DEEP" },
  ]
    .map(
      (lens) => `<button class="lens ${lens.active ? "active" : ""}" data-action="set-lens" data-group="${lens.group}" data-value="${lens.value}">
        ${lens.label}
      </button>`
    )
    .join("");

  return `<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8" />
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}';">
  <style>
    body { font-family: var(--vscode-font-family); color: var(--vscode-foreground); padding: 18px; line-height: 1.6; }
    h1 { font-size: 1.35rem; margin-bottom: 0.5rem; }
    h2 { font-size: 1rem; margin: 0; }
    h3 { font-size: 0.85rem; margin: 0 0 0.5rem; text-transform: uppercase; letter-spacing: 0.08em; color: var(--vscode-descriptionForeground); }
    .toolbar { display: flex; gap: 0.5rem; margin: 1rem 0; }
    .toolbar button, .chip, .link-button {
      border: 1px solid var(--vscode-panel-border);
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
      border-radius: 999px;
      padding: 0.35rem 0.75rem;
      cursor: pointer;
    }
    .link-button { padding: 0; border: none; background: transparent; color: var(--vscode-textLink-foreground); }
    .crumbs { color: var(--vscode-descriptionForeground); font-size: 0.85rem; }
    .sep { margin: 0 0.45rem; opacity: 0.6; }
    .meta { display: flex; gap: 0.5rem; flex-wrap: wrap; color: var(--vscode-descriptionForeground); font-size: 0.85rem; }
    .badge { display: inline-flex; border: 1px solid var(--vscode-panel-border); border-radius: 999px; padding: 0.2rem 0.6rem; }
    .badge.stale { border-color: var(--vscode-editorWarning-foreground); color: var(--vscode-editorWarning-foreground); }
    .section { border-top: 1px solid var(--vscode-panel-border); padding-top: 1rem; margin-top: 1rem; }
    .section-header { display: flex; align-items: center; justify-content: space-between; gap: 1rem; }
    .confidence { font-size: 0.75rem; text-transform: uppercase; border-radius: 999px; padding: 0.2rem 0.55rem; border: 1px solid var(--vscode-panel-border); }
    .confidence.high, .confidence.verified { color: #22c55e; }
    .confidence.medium { color: #eab308; }
    .confidence.low { color: #ef4444; }
    .summary { color: var(--vscode-descriptionForeground); font-style: italic; }
    .content { white-space: pre-wrap; }
    .muted { color: var(--vscode-descriptionForeground); }
    .children { display: flex; flex-wrap: wrap; gap: 0.5rem; margin-top: 1rem; }
    .empty { color: var(--vscode-descriptionForeground); }
    .lens-row { display: flex; flex-wrap: wrap; gap: 0.5rem; margin: 0.75rem 0 1rem; }
    .lens { border: 1px solid var(--vscode-panel-border); background: transparent; color: var(--vscode-descriptionForeground); border-radius: 999px; padding: 0.3rem 0.7rem; cursor: pointer; }
    .lens.active { background: var(--vscode-button-secondaryBackground); color: var(--vscode-foreground); }
  </style>
</head>
<body>
  <div class="crumbs">${breadcrumbs}</div>
  <h1>${escapeHtml(artifactTypeLabel(artifact.type))}</h1>
  <div class="meta">
    <span class="badge">${escapeHtml(getScopeLabel(scope))}</span>
    <span class="badge">${escapeHtml(artifact.status)}</span>
    ${artifact.stale ? '<span class="badge stale">Stale</span>' : ""}
  </div>
  <div class="lens-row">${lensButtons}</div>
  <div class="toolbar">
    <button data-action="refresh">Refresh</button>
    <button data-action="regenerate">Regenerate ${escapeHtml(artifactTypeLabel(artifact.type))}</button>
  </div>
  ${
    childScopes.length > 0
      ? `<div><h3>Explore Deeper</h3><div class="children">${childrenHtml}</div></div>`
      : ""
  }
  ${
    sectionsHtml ||
    `<p class="empty">${
      artifact.status === "GENERATING"
        ? "Generating knowledge for this scope..."
        : artifact.status === "FAILED"
          ? "Generation failed. Try regenerating this scope."
          : "No sections available yet."
    }</p>`
  }
  <script nonce="${nonce}">
    const vscode = acquireVsCodeApi();
    document.addEventListener("click", (event) => {
      const target = event.target;
      if (!(target instanceof HTMLElement)) return;
      const actionTarget = target.closest("[data-action]");
      if (!(actionTarget instanceof HTMLElement)) return;
      const action = actionTarget.dataset.action;
      if (action === "open-location") {
        vscode.postMessage({
          type: "openLocation",
          filePath: actionTarget.dataset.filePath,
          line: actionTarget.dataset.line ? Number(actionTarget.dataset.line) : undefined
        });
      }
      if (action === "refresh") vscode.postMessage({ type: "refresh" });
      if (action === "regenerate") vscode.postMessage({ type: "regenerate" });
      if (action === "set-lens") {
        const group = actionTarget.dataset.group;
        const value = actionTarget.dataset.value;
        if (!group || !value) return;
        const activeAudience = document.querySelector('[data-group="audience"].active')?.dataset.value || ${JSON.stringify(
          artifact.audience
        )};
        const activeDepth = document.querySelector('[data-group="depth"].active')?.dataset.value || ${JSON.stringify(
          artifact.depth
        )};
        const audience = group === "audience" ? value : activeAudience;
        const depth = group === "depth" ? value : activeDepth;
        vscode.postMessage({ type: "setLens", audience, depth });
      }
      if (action === "open-child-scope") {
        vscode.postMessage({
          type: "openChildScope",
          scopeType: actionTarget.dataset.scopeType,
          scopePath: actionTarget.dataset.scopePath || undefined
        });
      }
    });
  </script>
</body>
</html>`;
}

function renderExplainHtml(question: string, explanation: string, scope?: ScopeContext): string {
  return `<!DOCTYPE html>
<html>
<head>
  <style>
    body { font-family: var(--vscode-font-family); color: var(--vscode-foreground); padding: 18px; line-height: 1.6; }
    .question { background: var(--vscode-textBlockQuote-background); padding: 12px; border-radius: 8px; margin-bottom: 14px; }
    .answer { white-space: pre-wrap; }
    .scope { color: var(--vscode-descriptionForeground); margin-bottom: 8px; }
  </style>
</head>
<body>
  <h1>System Explanation</h1>
  ${scope ? `<div class="scope">${escapeHtml(getScopeLabel(scope))}</div>` : ""}
  <div class="question"><strong>Question:</strong> ${escapeHtml(question)}</div>
  <div class="answer">${escapeHtml(explanation)}</div>
</body>
</html>`;
}

function isMessage(
  message: unknown
): message is {
  type: string;
  filePath?: string;
  line?: number;
  audience?: string;
  depth?: string;
  scopeType?: ScopeContext["scopeType"];
  scopePath?: string;
} {
  return !!message && typeof message === "object" && "type" in message;
}
