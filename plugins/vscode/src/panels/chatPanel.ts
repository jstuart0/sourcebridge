// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * SourceBridge chat panel — a multi-turn conversation surface backed
 * by the MCP `explain_code` tool.
 *
 * Design:
 *   - Single panel instance per workspace (reused on subsequent asks).
 *   - Conversation history lives in the webview; each exchange is a
 *     (question, scope, answer) triple. Answers stream in via
 *     postMessage as the MCP tool runs.
 *   - Scope badge above the input reflects "File:foo.ts" / "Symbol:
 *     getUser" / "Selection (12 lines)" / "Repository".
 *   - References in the answer become clickable jumps via the same
 *     parseFileReference path used by the legacy discussion panel.
 *   - Progress notifications turn the thinking spinner into a live
 *     elapsed timer with any status message the server provides.
 */

import * as vscode from "vscode";
import { createNonce, escapeHtml } from "./utils";
import * as log from "../logging";

export interface ChatScope {
  /** Human-readable label shown above the input. */
  label: string;
  /** Repo id the question is grounded in. */
  repositoryId: string;
  /** Optional workspace-relative path for file/symbol scopes. */
  filePath?: string;
  /** Optional snippet (selection or full file) to seed the tool. */
  code?: string;
  language?: string;
}

export interface ChatExchange {
  id: string;
  scopeLabel: string;
  question: string;
  answer: string;
  streaming: boolean;
  progress?: string;
  references: string[];
  error?: string;
}

export interface ChatPanelHandlers {
  /**
   * Called when the user submits a new question. The handler is
   * responsible for running the MCP call and streaming tokens back
   * via the returned `updateExchange` callback.
   */
  onAsk: (
    exchange: ChatExchange,
    scope: ChatScope,
    update: (patch: Partial<ChatExchange>) => void,
  ) => Promise<void>;
  /** User clicked a reference — jump to file:line in the workspace. */
  onOpenReference: (reference: string) => Promise<void>;
  /** User pressed the "Clear" button. */
  onClear?: () => void;
}

let activePanel: vscode.WebviewPanel | undefined;
let activeScope: ChatScope | undefined;
let exchanges: ChatExchange[] = [];

export function openChatPanel(
  scope: ChatScope,
  handlers: ChatPanelHandlers,
  initialQuestion?: string,
): vscode.WebviewPanel {
  activeScope = scope;

  log.info("chatPanel", `openChatPanel scope=${scope.label} initial=${initialQuestion ? "yes" : "no"}`);
  if (!activePanel) {
    activePanel = vscode.window.createWebviewPanel(
      "sourcebridge.chat",
      "SourceBridge Chat",
      vscode.ViewColumn.Two,
      { enableScripts: true, retainContextWhenHidden: true },
    );
    log.info("chatPanel", "created new webview panel");

    activePanel.onDidDispose(() => {
      activePanel = undefined;
      activeScope = undefined;
      exchanges = [];
    });

    activePanel.webview.onDidReceiveMessage(async (message: unknown) => {
      if (!message || typeof message !== "object" || !("type" in message)) return;
      const typed = message as { type: string; text?: string; reference?: string };
      if (typed.type === "ask" && typed.text) {
        await runAsk(typed.text, handlers);
      }
      if (typed.type === "openRef" && typed.reference) {
        await handlers.onOpenReference(typed.reference);
      }
      if (typed.type === "clear") {
        exchanges = [];
        handlers.onClear?.();
        render();
      }
    });
  } else {
    activePanel.reveal(vscode.ViewColumn.Two, /* preserveFocus */ true);
  }

  render();

  if (initialQuestion) {
    void runAsk(initialQuestion, handlers);
  }

  return activePanel;
}

async function runAsk(question: string, handlers: ChatPanelHandlers): Promise<void> {
  if (!activePanel || !activeScope) return;

  const exchange: ChatExchange = {
    id: `ex_${Date.now()}_${Math.floor(Math.random() * 1000)}`,
    scopeLabel: activeScope.label,
    question,
    answer: "",
    streaming: true,
    progress: "Thinking…",
    references: [],
  };
  exchanges.push(exchange);
  render();

  const update = (patch: Partial<ChatExchange>) => {
    Object.assign(exchange, patch);
    render();
  };

  try {
    await handlers.onAsk(exchange, activeScope, update);
  } catch (err) {
    update({
      streaming: false,
      progress: undefined,
      error: err instanceof Error ? err.message : String(err),
    });
  }
  exchange.streaming = false;
  render();
}

function render(): void {
  if (!activePanel || !activeScope) return;
  activePanel.webview.html = renderHtml(activePanel.webview, activeScope, exchanges);
}

function renderHtml(
  webview: vscode.Webview,
  scope: ChatScope,
  state: ChatExchange[],
): string {
  const nonce = createNonce();
  const exchangesHtml = state.length
    ? state.map(renderExchange).join("")
    : `<div class="empty">Ask a question about <strong>${escapeHtml(scope.label)}</strong>.</div>`;

  return `<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8" />
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}';">
  <style>
    :root { color-scheme: light dark; }
    body {
      font-family: var(--vscode-font-family);
      color: var(--vscode-foreground);
      background: var(--vscode-editor-background);
      padding: 0;
      margin: 0;
      display: flex;
      flex-direction: column;
      height: 100vh;
    }
    header {
      padding: 8px 16px;
      border-bottom: 1px solid var(--vscode-widget-border, var(--vscode-contrastBorder, transparent));
      display: flex;
      justify-content: space-between;
      align-items: center;
      gap: 12px;
    }
    header .scope {
      font-size: 0.9rem;
      color: var(--vscode-descriptionForeground);
    }
    .clear-button {
      background: transparent;
      border: none;
      color: var(--vscode-textLink-foreground);
      cursor: pointer;
      font-size: 0.85rem;
    }
    main {
      flex: 1;
      overflow-y: auto;
      padding: 12px 16px;
      line-height: 1.55;
    }
    .empty {
      color: var(--vscode-descriptionForeground);
      padding: 20px 0;
      text-align: center;
    }
    .exchange { margin-bottom: 20px; }
    .bubble {
      padding: 10px 14px;
      border-radius: 8px;
      margin: 6px 0;
      white-space: pre-wrap;
      word-break: break-word;
    }
    .bubble.user {
      background: var(--vscode-textBlockQuote-background);
      border-left: 3px solid var(--vscode-textBlockQuote-border, var(--vscode-textLink-foreground));
    }
    .bubble.assistant {
      background: var(--vscode-editorWidget-background, var(--vscode-sideBar-background));
      border: 1px solid var(--vscode-widget-border, transparent);
    }
    .bubble.error {
      background: var(--vscode-inputValidation-errorBackground);
      border: 1px solid var(--vscode-inputValidation-errorBorder);
      color: var(--vscode-errorForeground);
    }
    .progress {
      font-size: 0.85rem;
      color: var(--vscode-descriptionForeground);
      margin-left: 4px;
    }
    .progress::after {
      content: "…";
      display: inline-block;
      animation: ellipsis 1.2s steps(3, end) infinite;
    }
    @keyframes ellipsis {
      0% { content: "."; }
      33% { content: ".."; }
      66% { content: "..."; }
    }
    .refs {
      margin-top: 6px;
      font-size: 0.85rem;
    }
    .refs button {
      background: transparent;
      border: none;
      color: var(--vscode-textLink-foreground);
      cursor: pointer;
      padding: 0;
      margin-right: 10px;
      text-decoration: underline;
    }
    footer {
      border-top: 1px solid var(--vscode-widget-border, var(--vscode-contrastBorder, transparent));
      padding: 8px 12px;
      display: flex;
      gap: 6px;
    }
    footer textarea {
      flex: 1;
      resize: none;
      min-height: 34px;
      max-height: 120px;
      padding: 6px 8px;
      font-family: var(--vscode-font-family);
      color: var(--vscode-input-foreground);
      background: var(--vscode-input-background);
      border: 1px solid var(--vscode-input-border, transparent);
      border-radius: 4px;
    }
    footer textarea:focus {
      outline: 2px solid var(--vscode-focusBorder);
      outline-offset: -1px;
    }
    footer button {
      padding: 6px 14px;
      background: var(--vscode-button-background);
      color: var(--vscode-button-foreground);
      border: none;
      border-radius: 4px;
      cursor: pointer;
    }
    footer button:hover { background: var(--vscode-button-hoverBackground); }
    footer button:disabled { opacity: 0.5; cursor: default; }
  </style>
</head>
<body>
  <header>
    <div class="scope" aria-label="Current chat scope">Scope: <strong>${escapeHtml(scope.label)}</strong></div>
    <button class="clear-button" data-action="clear" aria-label="Clear chat history">Clear</button>
  </header>
  <main id="transcript" role="log" aria-live="polite" aria-atomic="false">
    ${exchangesHtml}
  </main>
  <footer>
    <textarea id="input" rows="1" placeholder="Ask SourceBridge about ${escapeHtml(scope.label)}… (Enter to send, Shift+Enter for newline)" aria-label="Ask SourceBridge"></textarea>
    <button id="send" data-action="ask" aria-label="Send question">Send</button>
  </footer>
  <script nonce="${nonce}">
    const vscode = acquireVsCodeApi();
    const input = document.getElementById("input");
    const send = document.getElementById("send");

    function submit() {
      const text = input.value.trim();
      if (!text) return;
      input.value = "";
      vscode.postMessage({ type: "ask", text });
    }

    send.addEventListener("click", submit);
    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        submit();
      }
    });

    document.addEventListener("click", (event) => {
      const target = event.target;
      if (!(target instanceof HTMLElement)) return;
      const actionTarget = target.closest("[data-action]");
      if (!(actionTarget instanceof HTMLElement)) return;
      if (actionTarget.dataset.action === "clear") {
        vscode.postMessage({ type: "clear" });
      }
      if (actionTarget.dataset.action === "ref") {
        vscode.postMessage({ type: "openRef", reference: actionTarget.dataset.ref });
      }
    });

    // Scroll to bottom on any render so new tokens stay in view.
    const transcript = document.getElementById("transcript");
    if (transcript) {
      transcript.scrollTop = transcript.scrollHeight;
    }
    // Autofocus the input so the user can start typing immediately.
    input.focus();
  </script>
</body>
</html>`;
}

function renderExchange(exchange: ChatExchange): string {
  const refsHtml = exchange.references.length
    ? `<div class="refs">${exchange.references
        .map(
          (ref) =>
            `<button data-action="ref" data-ref="${escapeHtml(ref)}" aria-label="Jump to ${escapeHtml(ref)}">${escapeHtml(ref)}</button>`,
        )
        .join("")}</div>`
    : "";

  const assistantBody = exchange.error
    ? `<div class="bubble error" role="alert">${escapeHtml(exchange.error)}</div>`
    : `<div class="bubble assistant">${escapeHtml(exchange.answer || "")}${
        exchange.streaming
          ? `<span class="progress" aria-label="Thinking">${escapeHtml(exchange.progress || "Thinking")}</span>`
          : ""
      }</div>`;

  return `<div class="exchange" data-id="${escapeHtml(exchange.id)}">
    <div class="bubble user" aria-label="Your question"><strong>${escapeHtml(exchange.scopeLabel)}</strong><br>${escapeHtml(exchange.question)}</div>
    ${assistantBody}
    ${refsHtml}
  </div>`;
}
