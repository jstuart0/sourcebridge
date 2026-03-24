import * as vscode from "vscode";
import { Requirement, RequirementLink } from "../graphql/client";
import { createNonce, escapeHtml } from "./utils";

interface RequirementPanelHandlers {
  onOpenSymbol?: (link: RequirementLink) => void | Promise<void>;
  onVerify?: (linkId: string, verified: boolean) => void | Promise<void>;
}

export function createRequirementPanel(
  requirement: Requirement,
  links: RequirementLink[],
  handlers: RequirementPanelHandlers = {}
): vscode.WebviewPanel {
  const panel = vscode.window.createWebviewPanel(
    "sourcebridge.requirement",
    requirement.externalId || requirement.title,
    vscode.ViewColumn.Two,
    { enableScripts: true }
  );

  panel.webview.html = renderRequirementHtml(panel.webview, requirement, links);
  panel.webview.onDidReceiveMessage(async (message: unknown) => {
    if (!message || typeof message !== "object" || !("type" in message)) {
      return;
    }
    const typed = message as { type: string; link?: RequirementLink; linkId?: string; verified?: boolean };
    if (typed.type === "openSymbol" && typed.link) {
      await handlers.onOpenSymbol?.(typed.link);
    }
    if (typed.type === "verifyLink" && typed.linkId && typeof typed.verified === "boolean") {
      await handlers.onVerify?.(typed.linkId, typed.verified);
    }
  });
  return panel;
}

function renderRequirementHtml(
  webview: vscode.Webview,
  requirement: Requirement,
  links: RequirementLink[]
): string {
  const nonce = createNonce();
  const linksHtml = links
    .map((link) => {
      const payload = escapeHtml(JSON.stringify(link));
      return `<li>
        <button class="link-button" data-action="open-symbol" data-link="${payload}">
          ${escapeHtml(link.symbol?.name || link.symbolId)}
        </button>
        <span class="meta">${escapeHtml(link.confidence)}${link.verified ? " · verified" : ""}</span>
        <button class="verify-button" data-action="verify-link" data-link-id="${escapeHtml(
          link.id
        )}" data-verified="${String(!link.verified)}">
          ${link.verified ? "Reject" : "Verify"}
        </button>
      </li>`;
    })
    .join("");

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
  </style>
</head>
<body>
  <h1>${escapeHtml(requirement.externalId || requirement.title)}</h1>
  <p>${escapeHtml(requirement.title)}</p>
  <p>${escapeHtml(requirement.description)}</p>
  <p class="meta">Source: ${escapeHtml(requirement.source)}${requirement.priority ? ` · Priority: ${escapeHtml(requirement.priority)}` : ""}</p>
  <h2>Linked Code</h2>
  <ul>${linksHtml || "<li>No linked symbols.</li>"}</ul>
  <script nonce="${nonce}">
    const vscode = acquireVsCodeApi();
    document.addEventListener("click", (event) => {
      const target = event.target;
      if (!(target instanceof HTMLElement)) return;
      const actionTarget = target.closest("[data-action]");
      if (!(actionTarget instanceof HTMLElement)) return;
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
    });
  </script>
</body>
</html>`;
}
