import * as vscode from "vscode";
import { createNonce, escapeHtml } from "./utils";

export interface DiscussionData {
  question: string;
  answer: string;
  references: string[];
  sourceLabel?: string;
  sourceNote?: string;
}

export function createDiscussionPanel(
  data: DiscussionData,
  onOpenReference?: (reference: string) => void | Promise<void>
): vscode.WebviewPanel {
  const panel = vscode.window.createWebviewPanel(
    "sourcebridge.discussion",
    "Code Discussion",
    vscode.ViewColumn.Two,
    { enableScripts: true }
  );

  panel.webview.html = renderDiscussionHtml(panel.webview, data);
  if (onOpenReference) {
    panel.webview.onDidReceiveMessage(async (message: unknown) => {
      if (
        message &&
        typeof message === "object" &&
        "type" in message &&
        (message as { type?: string }).type === "openReference" &&
        "reference" in message &&
        typeof (message as { reference?: unknown }).reference === "string"
      ) {
        await onOpenReference((message as { reference: string }).reference);
      }
    });
  }
  return panel;
}

function renderDiscussionHtml(webview: vscode.Webview, data: DiscussionData): string {
  const nonce = createNonce();
  const refsHtml = data.references
    .map(
      (reference) => `<li><button class="link-button" data-ref="${escapeHtml(reference)}">${escapeHtml(
        reference
      )}</button></li>`
    )
    .join("\n");

  return `<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8" />
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}';">
  <style>
    body { font-family: var(--vscode-font-family); color: var(--vscode-foreground); padding: 16px; line-height: 1.6; }
    .meta { margin: 6px 0 16px; color: var(--vscode-descriptionForeground); font-size: 0.95em; }
    .pill { display: inline-block; padding: 4px 10px; border-radius: 999px; background: var(--vscode-badge-background); color: var(--vscode-badge-foreground); font-weight: 600; margin-right: 8px; }
    .question { background: var(--vscode-textBlockQuote-background); padding: 12px; border-radius: 6px; margin-bottom: 16px; }
    .answer { white-space: pre-wrap; }
    .refs { margin-top: 16px; padding-top: 16px; border-top: 1px solid var(--vscode-panel-border); }
    .link-button { border: none; background: transparent; color: var(--vscode-textLink-foreground); padding: 0; cursor: pointer; }
  </style>
</head>
<body>
  <h1>Code Discussion</h1>
  ${
    data.sourceLabel
      ? `<div class="meta"><span class="pill">${escapeHtml(data.sourceLabel)}</span>${data.sourceNote ? escapeHtml(data.sourceNote) : ""}</div>`
      : ""
  }
  <div class="question"><strong>Question:</strong> ${escapeHtml(data.question)}</div>
  <div class="answer">${escapeHtml(data.answer)}</div>
  ${
    data.references.length > 0
      ? `<div class="refs"><h3>References</h3><ul>${refsHtml}</ul></div>`
      : ""
  }
  <script nonce="${nonce}">
    const vscode = acquireVsCodeApi();
    document.addEventListener("click", (event) => {
      const target = event.target;
      if (!(target instanceof HTMLElement)) return;
      const button = target.closest("[data-ref]");
      if (!(button instanceof HTMLElement)) return;
      vscode.postMessage({ type: "openReference", reference: button.dataset.ref });
    });
  </script>
</body>
</html>`;
}
