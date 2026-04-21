import * as vscode from "vscode";

let outputChannel: vscode.OutputChannel | undefined;
let debugEnabled = false;

function timestamp(): string {
  return new Date().toISOString();
}

function isDebug(): boolean {
  return debugEnabled;
}

function formatMessage(level: string, tag: string, message: string): string {
  return `[${timestamp()}] [${level}] [${tag}] ${message}`;
}

export function initLogger(channel: vscode.OutputChannel): void {
  outputChannel = channel;
  debugEnabled = vscode.workspace
    .getConfiguration("sourcebridge")
    .get("debug", false);

  vscode.workspace.onDidChangeConfiguration((e) => {
    if (e.affectsConfiguration("sourcebridge.debug")) {
      debugEnabled = vscode.workspace
        .getConfiguration("sourcebridge")
        .get("debug", false);
      info("config", `Debug mode ${debugEnabled ? "enabled" : "disabled"}`);
    }
  });
}

export function debug(tag: string, message: string): void {
  if (!isDebug() || !outputChannel) return;
  outputChannel.appendLine(formatMessage("DEBUG", tag, message));
}

export function info(tag: string, message: string): void {
  if (!outputChannel) return;
  outputChannel.appendLine(formatMessage("INFO", tag, message));
}

export function warn(tag: string, message: string): void {
  if (!outputChannel) return;
  outputChannel.appendLine(formatMessage("WARN", tag, message));
}

export function error(tag: string, message: string, err?: unknown): void {
  if (!outputChannel) return;
  let line = formatMessage("ERROR", tag, message);
  if (err instanceof Error) {
    line += ` | ${err.message}`;
    if (isDebug() && err.stack) {
      line += `\n${err.stack}`;
    }
  } else if (err !== undefined) {
    line += ` | ${String(err)}`;
  }
  outputChannel.appendLine(line);
  if (isDebug()) {
    outputChannel.show(true);
  }
}

export function showChannel(): void {
  outputChannel?.show(true);
}
