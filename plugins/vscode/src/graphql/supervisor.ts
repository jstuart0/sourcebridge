// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * ConnectionSupervisor holds the extension's single source of truth
 * for "is the server reachable?". Before this module existed we ran
 * one health check at activation and then went mute — no
 * reconnect, no signal to the UI, no way for providers to know when
 * to retry.
 *
 * Model:
 *
 *  * {@link ConnectionState} enumerates the six states we surface in
 *    the status bar.
 *  * A heartbeat fires every 30 s while connected. On failure we
 *    transition to `offline` and start an exponential backoff
 *    (5 s → 15 s → 45 s → 2 min → 5 min cap).
 *  * On reconnect, {@link onReconnect} fires so providers and tree
 *    views can refresh stale data.
 *  * Callers force an immediate probe with `retryNow()`.
 */

import * as vscode from "vscode";
import { SourceBridgeClient } from "./client";
import * as log from "../logging";

export type ConnectionState =
  | "connecting"
  | "connected"
  | "offline"
  | "auth-required"
  | "no-workspace"
  | "no-repo";

const HEARTBEAT_MS = 30_000;
const BACKOFF_LADDER_MS = [5_000, 15_000, 45_000, 120_000, 300_000];

export class ConnectionSupervisor implements vscode.Disposable {
  private state: ConnectionState = "connecting";
  private readonly _onDidChange = new vscode.EventEmitter<ConnectionState>();
  readonly onDidChange = this._onDidChange.event;
  private readonly _onReconnect = new vscode.EventEmitter<void>();
  readonly onReconnect = this._onReconnect.event;

  private timer: NodeJS.Timeout | undefined;
  private backoffIdx = 0;
  private destroyed = false;
  /** Monotonically incremented; lets us drop stale probe results. */
  private probeToken = 0;
  /** Wall-clock instant the next probe fires. Powers the UI countdown. */
  private nextProbeAt = 0;

  constructor(private client: SourceBridgeClient) {}

  /** Current state. */
  get current(): ConnectionState {
    return this.state;
  }

  /** ms until the next scheduled probe — for the offline countdown. */
  get msUntilRetry(): number {
    if (this.state !== "offline") return 0;
    return Math.max(0, this.nextProbeAt - Date.now());
  }

  /** Kick off the first probe; schedules all subsequent probes. */
  async start(): Promise<void> {
    await this.probeOnce(/* isHeartbeat */ false);
  }

  /** User-triggered immediate retry (status bar "retry now" action). */
  async retryNow(): Promise<void> {
    this.clearTimer();
    this.backoffIdx = 0;
    await this.probeOnce(false);
  }

  /** Mark the state as auth-required explicitly — we know we're unauthenticated. */
  markAuthRequired(): void {
    this.clearTimer();
    this.setState("auth-required");
  }

  /** Mark as no-workspace or no-repo explicitly. */
  markContext(state: Extract<ConnectionState, "no-workspace" | "no-repo">): void {
    this.clearTimer();
    this.setState(state);
  }

  private async probeOnce(isHeartbeat: boolean): Promise<void> {
    if (this.destroyed) return;
    if (!isHeartbeat) this.setState("connecting");
    const token = ++this.probeToken;
    let ok: boolean;
    try {
      ok = await this.client.isServerRunning();
    } catch {
      ok = false;
    }
    // Stale result from an older probe: ignore.
    if (token !== this.probeToken) return;

    if (ok) {
      const wasOffline = this.state !== "connected";
      this.backoffIdx = 0;
      this.setState("connected");
      this.scheduleHeartbeat();
      if (wasOffline) {
        log.info("supervisor", "Reconnected");
        this._onReconnect.fire();
      }
    } else {
      this.setState("offline");
      this.scheduleBackoff();
    }
  }

  private scheduleHeartbeat(): void {
    this.clearTimer();
    this.nextProbeAt = Date.now() + HEARTBEAT_MS;
    this.timer = setTimeout(() => {
      this.timer = undefined;
      void this.probeOnce(true);
    }, HEARTBEAT_MS);
  }

  private scheduleBackoff(): void {
    this.clearTimer();
    const delay = BACKOFF_LADDER_MS[Math.min(this.backoffIdx, BACKOFF_LADDER_MS.length - 1)];
    this.backoffIdx = Math.min(this.backoffIdx + 1, BACKOFF_LADDER_MS.length - 1);
    this.nextProbeAt = Date.now() + delay;
    this.timer = setTimeout(() => {
      this.timer = undefined;
      void this.probeOnce(false);
    }, delay);
  }

  private clearTimer(): void {
    if (this.timer !== undefined) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
    this.nextProbeAt = 0;
  }

  private setState(s: ConnectionState): void {
    if (this.state === s) return;
    this.state = s;
    this._onDidChange.fire(s);
  }

  dispose(): void {
    this.destroyed = true;
    this.clearTimer();
    this._onDidChange.dispose();
    this._onReconnect.dispose();
  }
}
