import * as vscode from "vscode";
import { Requirement, RequirementLink } from "../graphql/client";
import { createNonce, escapeHtml } from "./utils";

export interface RequirementPanelHandlers {
  onOpenSymbol?: (link: RequirementLink) => void | Promise<void>;
  onVerify?: (linkId: string, verified: boolean) => void | Promise<void>;
  onGenerateFieldGuide?: () => void | Promise<void>;
  onAskQuestion?: () => void | Promise<void>;
  // Phase 2 CRUD additions. All three open native VS Code input/picker
  // flows (more accessible + discoverable than webview forms).
  onEdit?: () => void | Promise<void>;
  onDelete?: () => void | Promise<void>;
  onUnlink?: (linkId: string) => void | Promise<void>;
}

export type ArtifactHint =
  | { state: "none" }
  | { state: "ready"; stale: boolean }
  | { state: "generating" };

export function createRequirementPanel(
  requirement: Requirement,
  links: RequirementLink[],
  handlers: RequirementPanelHandlers = {},
  artifactHint: ArtifactHint = { state: "none" }
): vscode.WebviewPanel {
  const panel = vscode.window.createWebviewPanel(
    "sourcebridge.requirement",
    requirement.externalId || requirement.title,
    vscode.ViewColumn.Two,
    { enableScripts: true }
  );

  panel.webview.html = renderRequirementHtml(panel.webview, requirement, links, artifactHint);
  panel.webview.onDidReceiveMessage(async (message: unknown) => {
    if (!message || typeof message !== "object" || !("type" in message)) {
      return;
    }
    const typed = message as {
      type: string;
      link?: RequirementLink;
      linkId?: string;
      verified?: boolean;
    };
    if (typed.type === "openSymbol" && typed.link) {
      await handlers.onOpenSymbol?.(typed.link);
    }
    if (typed.type === "verifyLink" && typed.linkId && typeof typed.verified === "boolean") {
      await handlers.onVerify?.(typed.linkId, typed.verified);
    }
    if (typed.type === "generateFieldGuide") {
      await handlers.onGenerateFieldGuide?.();
    }
    if (typed.type === "askQuestion") {
      await handlers.onAskQuestion?.();
    }
    if (typed.type === "editRequirement") {
      await handlers.onEdit?.();
    }
    if (typed.type === "deleteRequirement") {
      await handlers.onDelete?.();
    }
    if (typed.type === "unlinkSymbol" && typed.linkId) {
      await handlers.onUnlink?.(typed.linkId);
    }
  });
  return panel;
}

function fieldGuideButtonLabel(hint: ArtifactHint): string {
  switch (hint.state) {
    case "ready":
      return hint.stale ? "View Field Guide (stale)" : "View Field Guide";
    case "generating":
      return "Field Guide generating\u2026";
    default:
      return "Generate Field Guide";
  }
}

function renderRequirementHtml(
  webview: vscode.Webview,
  requirement: Requirement,
  links: RequirementLink[],
  artifactHint: ArtifactHint
): string {
  const nonce = createNonce();
  const hasLinks = links.length > 0;
  const linksHtml = links
    .map((link) => {
      const payload = escapeHtml(JSON.stringify(link));
      const linkIdAttr = escapeHtml(link.id);
      return `<li>
        <button class="link-button" data-action="open-symbol" data-link="${payload}" aria-label="Open ${escapeHtml(link.symbol?.name || link.symbolId)} in editor">
          ${escapeHtml(link.symbol?.name || link.symbolId)}
        </button>
        <span class="meta">${escapeHtml(link.confidence)}${link.verified ? " · verified" : ""}</span>
        <button class="verify-button" data-action="verify-link" data-link-id="${linkIdAttr}" data-verified="${String(!link.verified)}" aria-label="${link.verified ? "Reject link" : "Verify link"}">
          ${link.verified ? "Reject" : "Verify"}
        </button>
        <button class="verify-button unlink-button" data-action="unlink-symbol" data-link-id="${linkIdAttr}" aria-label="Remove this link">
          Unlink
        </button>
      </li>`;
    })
    .join("");

  const fgLabel = fieldGuideButtonLabel(artifactHint);
  const fgDisabled = artifactHint.state === "generating" ? " disabled" : "";
  const fgClass = artifactHint.state === "ready" && artifactHint.stale ? " stale" : "";

  const aiActionsHtml = hasLinks
    ? `<div class="actions">
        <button class="action-button${fgClass}" data-action="generate-field-guide"${fgDisabled}>${escapeHtml(fgLabel)}</button>
        <button class="action-button" data-action="ask-question">Ask a Question</button>
      </div>`
    : "";
  // CRUD actions are always available (even when no links exist yet) so
  // users can rename / describe / retire a freshly-created requirement.
  const crudActionsHtml = `<div class="actions crud-actions">
      <button class="action-button" data-action="edit-requirement" aria-label="Edit this requirement">Edit</button>
      <button class="action-button danger" data-action="delete-requirement" aria-label="Move this requirement to the recycle bin">Delete…</button>
    </div>`;
  const actionsHtml = `${crudActionsHtml}${aiActionsHtml}`;

  return `<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8" />
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}';">
  <style>
    body { font-family: var(--vscode-font-family); color: var(--vscode-foreground); padding: 16px; line-height: 1.6; }
    .meta { color: var(--vscode-descriptionForeground); margin-left: 0.5rem; }
    .link-button, .verify-button { border: none; background: transparent; color: var(--vscode-textLink-foreground); cursor: pointer; padding: 0; }
    .verify-button { margin-left: 0.75rem; }
    .actions { display: flex; gap: 0.75rem; margin: 1rem 0; }
    .action-button {
      padding: 6px 14px;
      border: 1px solid var(--vscode-button-border, var(--vscode-contrastBorder, transparent));
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
      cursor: pointer;
      border-radius: 2px;
    }
    .action-button:hover { background: var(--vscode-button-secondaryHoverBackground); }
    .action-button:disabled { opacity: 0.5; cursor: default; }
    .action-button.stale { color: var(--vscode-editorWarning-foreground); }
    .action-button.danger { color: var(--vscode-errorForeground); }
    .unlink-button { margin-left: 0.75rem; color: var(--vscode-errorForeground); }
  </style>
</head>
<body>
  <h1>${escapeHtml(requirement.externalId || requirement.title)}</h1>
  <p>${escapeHtml(requirement.title)}</p>
  <p>${escapeHtml(requirement.description)}</p>
  <p class="meta">Source: ${escapeHtml(requirement.source)}${requirement.priority ? ` · Priority: ${escapeHtml(requirement.priority)}` : ""}</p>
  ${actionsHtml}
  <h2>Linked Code</h2>
  <ul>${linksHtml || "<li>No linked symbols.</li>"}</ul>
  <script nonce="${nonce}">
    const vscode = acquireVsCodeApi();
    document.addEventListener("click", (event) => {
      const target = event.target;
      if (!(target instanceof HTMLElement)) return;
      const actionTarget = target.closest("[data-action]");
      if (!(actionTarget instanceof HTMLElement)) return;
      if (actionTarget.disabled) return;
      const action = actionTarget.dataset.action;
      if (action === "open-symbol") {
        const raw = actionTarget.dataset.link;
        if (raw) vscode.postMessage({ type: "openSymbol", link: JSON.parse(raw) });
      }
      if (action === "verify-link") {
        vscode.postMessage({
          type: "verifyLink",
          linkId: actionTarget.dataset.linkId,
          verified: actionTarget.dataset.verified === "true"
        });
      }
      if (action === "generate-field-guide") {
        vscode.postMessage({ type: "generateFieldGuide" });
      }
      if (action === "ask-question") {
        vscode.postMessage({ type: "askQuestion" });
      }
      if (action === "edit-requirement") {
        vscode.postMessage({ type: "editRequirement" });
      }
      if (action === "delete-requirement") {
        vscode.postMessage({ type: "deleteRequirement" });
      }
      if (action === "unlink-symbol") {
        vscode.postMessage({
          type: "unlinkSymbol",
          linkId: actionTarget.dataset.linkId
        });
      }
    });
  </script>
</body>
</html>`;
}
