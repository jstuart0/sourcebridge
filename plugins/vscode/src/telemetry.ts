// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Opt-in anonymous usage telemetry.
 *
 * **Off by default.** Only flips on when the user sets
 * `sourcebridge.telemetry.enabled = true`. We log to the output
 * channel in-process and also post compact events to the configured
 * API's `/v1/telemetry/vscode` endpoint when reachable; failures are
 * silently swallowed — telemetry must never interrupt UX.
 *
 * What we send:
 *   - `event`: command ID or lifecycle name (e.g. "activate",
 *     "createRequirement.success").
 *   - `version`: extension version from package.json.
 *   - `platform`: VS Code's os identifier.
 *   - `duration_ms` (optional): wall-clock duration of the tracked
 *     operation.
 *
 * What we never send: file paths, requirement bodies, source code,
 * GraphQL payloads, auth tokens, workspace names, or any PII.
 *
 * Disclosure lives in the package.json setting description so it's
 * visible in the VS Code settings UI alongside the toggle itself.
 */

import * as vscode from "vscode";
import * as log from "./logging";

export class Telemetry {
  private version: string;
  private platform: string;
  private apiUrl: string;

  constructor(context: vscode.ExtensionContext) {
    this.version = context.extension?.packageJSON?.version || "0.0.0";
    this.platform = process.platform;
    this.apiUrl = vscode.workspace
      .getConfiguration("sourcebridge")
      .get<string>("apiUrl", "http://localhost:8080");
  }

  private enabled(): boolean {
    return vscode.workspace
      .getConfiguration("sourcebridge")
      .get<boolean>("telemetry.enabled", false);
  }

  /** Emit a named event with optional duration metadata. */
  event(name: string, durationMs?: number): void {
    if (!this.enabled()) return;
    const payload = {
      event: name,
      version: this.version,
      platform: this.platform,
      duration_ms: durationMs,
      ts: new Date().toISOString(),
    };
    log.debug("telemetry", `emit ${JSON.stringify(payload)}`);
    // Fire-and-forget. Cannot await inside sync command handlers.
    void this.sendBeacon(payload);
  }

  /** Wrap an async operation so its duration is captured. */
  async track<T>(name: string, fn: () => Promise<T>): Promise<T> {
    if (!this.enabled()) return fn();
    const start = Date.now();
    try {
      const result = await fn();
      this.event(`${name}.success`, Date.now() - start);
      return result;
    } catch (err) {
      this.event(`${name}.error`, Date.now() - start);
      throw err;
    }
  }

  private async sendBeacon(payload: Record<string, unknown>): Promise<void> {
    try {
      await fetch(`${this.apiUrl.replace(/\/$/, "")}/v1/telemetry/vscode`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
    } catch (err) {
      // Swallow — telemetry never interrupts UX.
      log.debug("telemetry", `beacon failed: ${(err as Error).message}`);
    }
  }
}
