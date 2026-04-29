// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

"use client";

import { useEffect, useState } from "react";

export interface ServerCapabilities {
  /** True when the server requires a bearer token to reach protected routes. */
  authRequired: boolean;
  /** True when /api/v1/mcp/http is reachable (returns 401 or 405, not 404). */
  mcpEnabled: boolean;
  loading: boolean;
  /** Set when probes failed to complete (network error, 5xx, etc.). */
  error: boolean;
}

// RFC 1918 + link-local + loopback + CGNAT (100.64.0.0/10, used by
// Tailscale / Headscale). A host matching any of these is treated as
// private — "local-dev" flow, no auth token needed.
function isPrivateHostname(hostname: string): boolean {
  // Loopback
  if (hostname === "localhost" || hostname === "::1") return true;

  const parts = hostname.split(".").map(Number);
  if (parts.length !== 4 || parts.some(isNaN)) {
    // Non-IP hostname (e.g. "sourcebridge.example.com") is never private.
    return false;
  }
  const [a, b] = parts;

  return (
    a === 127 ||                         // 127.0.0.0/8  loopback
    a === 10 ||                          // 10.0.0.0/8   RFC 1918
    (a === 172 && b >= 16 && b <= 31) || // 172.16.0.0/12 RFC 1918
    (a === 192 && b === 168) ||          // 192.168.0.0/16 RFC 1918
    (a === 169 && b === 254) ||          // 169.254.0.0/16 link-local
    (a === 100 && b >= 64 && b <= 127)   // 100.64.0.0/10 CGNAT / Tailscale
  );
}

/**
 * Probes the current SourceBridge server to determine:
 *   1. Whether authentication is required (via GET /auth/desktop/info).
 *   2. Whether the MCP HTTP transport is enabled (via HEAD /api/v1/mcp/http).
 *
 * Runs once on mount. Does NOT use authFetch — these are intentionally
 * unauthenticated probes so they work before the user has a token.
 *
 * Decision matrix → see Slice 3 plan.
 */
export function useServerCapabilities(): ServerCapabilities {
  const [state, setState] = useState<ServerCapabilities>({
    authRequired: false,
    mcpEnabled: false,
    loading: true,
    error: false,
  });

  useEffect(() => {
    let cancelled = false;

    async function probe() {
      // --- Auth probe ---
      let authRequired = false;
      let authProbeOk = false;

      try {
        const res = await fetch("/auth/desktop/info", {
          method: "GET",
          // No credentials — this endpoint is intentionally public.
          credentials: "omit",
          signal: AbortSignal.timeout(5_000),
        });
        if (res.ok) {
          const data = (await res.json()) as {
            local_auth?: boolean;
            setup_done?: boolean;
            oidc_enabled?: boolean;
          };
          // Auth is required when OIDC is configured (cloud) OR when the
          // local password has been set up. A fresh install with neither
          // configured is the local-dev no-auth case.
          authRequired = !!data.oidc_enabled || !!data.setup_done;
          authProbeOk = true;
        }
      } catch {
        // Network error or timeout — fall through to hostname heuristic.
      }

      // Fallback: if the auth probe failed, infer from hostname.
      if (!authProbeOk) {
        if (typeof window !== "undefined") {
          authRequired = !isPrivateHostname(window.location.hostname);
        }
      }

      // --- MCP probe ---
      // HEAD to /api/v1/mcp/http:
      //   • 401 or 405 → MCP route exists (auth wall or method-not-allowed).
      //   • 404        → MCP not registered (disabled in config).
      //   • other      → treat as unknown; default to enabled to avoid hiding
      //                   the command from users who might still want it.
      let mcpEnabled = false;
      let mcpProbeOk = false;

      try {
        const res = await fetch("/api/v1/mcp/http", {
          method: "HEAD",
          credentials: "omit",
          signal: AbortSignal.timeout(5_000),
        });
        mcpEnabled = res.status !== 404;
        mcpProbeOk = true;
      } catch {
        // Network error — unknown; treat as enabled to show the command.
        mcpEnabled = true;
      }

      if (cancelled) return;

      if (!authProbeOk && !mcpProbeOk) {
        // Both probes failed — surface the error state so the card can
        // show its fallback disclosure.
        setState({ authRequired, mcpEnabled: true, loading: false, error: true });
        return;
      }

      setState({ authRequired, mcpEnabled, loading: false, error: false });
    }

    void probe();
    return () => {
      cancelled = true;
    };
  }, []);

  return state;
}
