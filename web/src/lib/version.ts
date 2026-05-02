// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// Build/runtime version metadata for the web bundle (CA-136).
//
// `buildInfo` is inlined at build time via NEXT_PUBLIC_* env vars set by
// the Makefile (`make build-web`) or the web Dockerfile. Use it for the
// always-visible sidebar footer — zero network cost.
//
// `fetchRuntimeBuildInfo()` calls GET /api/v1/version on the running API
// server to retrieve the runtime fields the web bundle can't bake in
// (goVersion, edition, workerVersion, server-side commit). Use it for
// the admin Build Info panel and the support-ticket "Copy build info"
// button.

import { authFetch } from "@/lib/auth-fetch";

/**
 * Compile-time build info, baked into the bundle when `npm run build`
 * runs with NEXT_PUBLIC_VERSION / NEXT_PUBLIC_COMMIT / NEXT_PUBLIC_BUILD_DATE
 * set. Each field falls back to "dev"/"unknown" when unwired so missing
 * configuration is visible (rather than crashing or silently rendering
 * blank).
 */
export const buildInfo = {
  version: process.env.NEXT_PUBLIC_VERSION || "dev",
  commit: process.env.NEXT_PUBLIC_COMMIT || "unknown",
  buildDate: process.env.NEXT_PUBLIC_BUILD_DATE || "unknown",
} as const;

/**
 * The full payload from GET /api/v1/version. Populated at runtime — the
 * web bundle can't know the server's goVersion or worker availability
 * at build time, only what was baked into its OWN bundle.
 *
 * The server's `version`/`commit`/`buildDate` are sourced from the API
 * server's own `internal/version` package; they may differ from the web
 * bundle's compile-time values during a rolling deploy.
 */
export interface RuntimeBuildInfo {
  version: string;
  commit: string;
  buildDate: string;
  goVersion: string;
  edition: string;
  buildEdition: string;
  workerVersion: string;
}

/**
 * Fetch the running API server's build metadata. Returns null on any
 * fetch failure — caller should fall back to displaying compile-time
 * `buildInfo` only.
 */
export async function fetchRuntimeBuildInfo(): Promise<RuntimeBuildInfo | null> {
  try {
    // /api/v1/version is intentionally unauthenticated, but authFetch
    // is the standard request helper — using it preserves the same
    // base-URL + cookie behavior as the rest of the admin UI.
    const res = await authFetch("/api/v1/version");
    if (!res.ok) return null;
    return (await res.json()) as RuntimeBuildInfo;
  } catch {
    return null;
  }
}

/**
 * Build a human-readable, copy-paste-friendly markdown block for support
 * tickets. The shape is stable so engineers can grep across tickets to
 * correlate reports with builds.
 */
export function formatBuildInfoMarkdown(runtime: RuntimeBuildInfo | null): string {
  const ui = buildInfo;
  const r = runtime;
  const lines = [
    "**SourceBridge build info**",
    `- Web bundle: ${ui.version} (commit ${shortCommit(ui.commit)}, built ${ui.buildDate})`,
  ];
  if (r) {
    lines.push(
      `- API server: ${r.version} (commit ${shortCommit(r.commit)}, built ${r.buildDate})`,
      `- Go runtime: ${r.goVersion}`,
      `- Edition: ${r.edition}${r.buildEdition && r.buildEdition !== r.edition ? ` (build flavor: ${r.buildEdition})` : ""}`,
      `- Worker: ${r.workerVersion || "(unavailable)"}`,
    );
  } else {
    lines.push("- API server: (unavailable — could not reach /api/v1/version)");
  }
  return lines.join("\n");
}

/** First 8 chars of a commit sha; full string if shorter or "unknown". */
export function shortCommit(commit: string): string {
  if (!commit || commit === "unknown") return commit || "unknown";
  return commit.slice(0, 8);
}
