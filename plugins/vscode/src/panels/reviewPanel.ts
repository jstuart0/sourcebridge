import * as vscode from "vscode";
import { createNonce, escapeHtml } from "./utils";

export interface ReviewFinding {
  severity: string;
  category: string;
  message: string;
  filePath?: string;
  line?: number;
  suggestion?: string;
}

export interface ReviewData {
  template: string;
  score: number;
  findings: ReviewFinding[];
  sourceLabel?: string;
  sourceNote?: string;
}

export function createReviewPanel(
  data: ReviewData,
  onOpenFinding?: (finding: ReviewFinding) => void | Promise<void>
): vscode.WebviewPanel {
  const panel = vscode.window.createWebviewPanel(
    "sourcebridge.review",
    `Review: ${data.template}`,
    vscode.ViewColumn.Two,
    { enableScripts: true }
  );

  panel.webview.html = renderReviewHtml(panel.webview, data);
  if (onOpenFinding) {
    panel.webview.onDidReceiveMessage(async (message: unknown) => {
      if (
        message &&
        typeof message === "object" &&
        "type" in message &&
        (message as { type?: string }).type === "openFinding" &&
        "finding" in message
      ) {
        const finding = (message as { finding: ReviewFinding }).finding;
        await onOpenFinding(finding);
      }
    });
  }
  return panel;
}

function renderReviewHtml(webview: vscode.Webview, data: ReviewData): string {
  const nonce = createNonce();
  const findingsHtml = data.findings
    .map((finding, index) => {
      const payload = escapeHtml(JSON.stringify(finding));
      return `<div class="finding ${escapeHtml(finding.severity.toLowerCase())}">
        <div class="finding-top">
          <span class="severity">${escapeHtml(finding.severity)}</span>
          <span class="category">${escapeHtml(finding.category)}</span>
          ${
            finding.filePath
              ? `<button class="jump" data-finding="${payload}">Open ${escapeHtml(
                  `${finding.filePath}${finding.line ? `:${finding.line}` : ""}`
                )}</button>`
              : ""
          }
        </div>
        <p>${escapeHtml(finding.message)}</p>
        ${finding.suggestion ? `<p class="suggestion">${escapeHtml(finding.suggestion)}</p>` : ""}
        <small>Finding ${index + 1}</small>
      </div>`;
    })
    .join("\n");

  return `<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8" />
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}';">
  <style>
    body { font-family: var(--vscode-font-family); color: var(--vscode-foreground); padding: 16px; }
    h1 { font-size: 1.4em; }
    .meta { margin: 6px 0 16px; color: var(--vscode-descriptionForeground); font-size: 0.95em; }
    .pill { display: inline-block; padding: 4px 10px; border-radius: 999px; background: var(--vscode-badge-background); color: var(--vscode-badge-foreground); font-weight: 600; margin-right: 8px; }
    .score { font-size: 2em; font-weight: bold; }
    .finding { padding: 12px; margin: 12px 0; border-left: 3px solid #666; border-radius: 6px; }
    .finding.critical { border-color: #ef4444; background: rgba(239, 68, 68, 0.1); }
    .finding.high { border-color: #f97316; background: rgba(249, 115, 22, 0.1); }
    .finding.medium { border-color: #eab308; background: rgba(234, 179, 8, 0.1); }
    .finding.low { border-color: #94a3b8; background: rgba(148, 163, 184, 0.1); }
    .finding-top { display: flex; gap: 0.75rem; align-items: center; flex-wrap: wrap; }
    .severity { font-weight: bold; text-transform: uppercase; font-size: 0.75em; }
    .category { font-size: 0.75em; color: var(--vscode-descriptionForeground); }
    .suggestion { font-style: italic; opacity: 0.8; }
    .jump { margin-left: auto; border: none; background: transparent; color: var(--vscode-textLink-foreground); cursor: pointer; }
  </style>
</head>
<body>
  <h1>${escapeHtml(data.template)} Review</h1>
  ${
    data.sourceLabel
      ? `<div class="meta"><span class="pill">${escapeHtml(data.sourceLabel)}</span>${data.sourceNote ? escapeHtml(data.sourceNote) : ""}</div>`
      : ""
  }
  <p>Score: <span class="score">${Math.round(data.score * 100)}%</span></p>
  <h2>Findings (${data.findings.length})</h2>
  ${findingsHtml || "<p>No findings.</p>"}
  <script nonce="${nonce}">
    const vscode = acquireVsCodeApi();
    document.addEventListener("click", (event) => {
      const target = event.target;
      if (!(target instanceof HTMLElement)) return;
      const button = target.closest("[data-finding]");
      if (!(button instanceof HTMLElement)) return;
      const raw = button.dataset.finding;
      if (!raw) return;
      vscode.postMessage({ type: "openFinding", finding: JSON.parse(raw) });
    });
  </script>
</body>
</html>`;
}
