import * as vscode from "vscode";
import { ImpactReport } from "../graphql/client";
import { createNonce, escapeHtml } from "./utils";

export function createImpactPanel(
  report: ImpactReport,
  onOpenFile?: (filePath: string) => void | Promise<void>
): vscode.WebviewPanel {
  const panel = vscode.window.createWebviewPanel(
    "sourcebridge.impact",
    "Change Risk",
    vscode.ViewColumn.Two,
    { enableScripts: true }
  );

  panel.webview.html = renderImpactHtml(panel.webview, report);
  if (onOpenFile) {
    panel.webview.onDidReceiveMessage(async (message: unknown) => {
      if (
        message &&
        typeof message === "object" &&
        "type" in message &&
        (message as { type?: string }).type === "openFile" &&
        "filePath" in message &&
        typeof (message as { filePath?: unknown }).filePath === "string"
      ) {
        await onOpenFile((message as { filePath: string }).filePath);
      }
    });
  }
  return panel;
}

function renderImpactHtml(webview: vscode.Webview, report: ImpactReport): string {
  const nonce = createNonce();
  const files = report.filesChanged
    .map(
      (file) => `<li>
        <button class="link-button" data-path="${escapeHtml(file.path)}" aria-label="Open ${escapeHtml(file.path)} in editor">${escapeHtml(file.path)}</button>
        <span class="meta">${escapeHtml(file.status)} · +${file.additions} / -${file.deletions}</span>
      </li>`
    )
    .join("");

  const requirements = report.affectedRequirements
    .map(
      (req) => `<li>${escapeHtml(req.externalId || req.requirementId)} · ${escapeHtml(
        req.title
      )} <span class="meta">${req.affectedLinks}/${req.totalLinks} links</span></li>`
    )
    .join("");

  return `<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8" />
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}';">
  <style>
    body { font-family: var(--vscode-font-family); color: var(--vscode-foreground); padding: 16px; line-height: 1.6; }
    .meta { color: var(--vscode-descriptionForeground); }
    .link-button { border: none; background: transparent; color: var(--vscode-textLink-foreground); cursor: pointer; padding: 0; }
  </style>
</head>
<body>
  <h1>Change Risk</h1>
  <p class="meta">Computed: ${escapeHtml(new Date(report.computedAt).toLocaleString())}</p>
  <h2>Changed Files</h2>
  <ul>${files || "<li>No changed files.</li>"}</ul>
  <h2>Affected Requirements</h2>
  <ul>${requirements || "<li>No impacted requirements.</li>"}</ul>
  <script nonce="${nonce}">
    const vscode = acquireVsCodeApi();
    document.addEventListener("click", (event) => {
      const target = event.target;
      if (!(target instanceof HTMLElement)) return;
      const button = target.closest("[data-path]");
      if (!(button instanceof HTMLElement)) return;
      vscode.postMessage({ type: "openFile", filePath: button.dataset.path });
    });
  </script>
</body>
</html>`;
}
