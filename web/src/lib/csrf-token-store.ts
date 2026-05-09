"use client";

// CSRF token store — centralized cookie reading and on-demand refresh.
//
// PHASE 1 GREP AUDIT — every browser fetch/sendBeacon/EventSource call site:
//
// COVERED by authFetch (auth-fetch.ts injects X-CSRF-Token):
//   - All authFetch callers (admin LLM, tokens, export, diagrams, change-password, etc.)
//   - TopBar.tsx /auth/logout   → switched to authFetch in Phase 1
//
// COVERED directly (inject X-CSRF-Token themselves):
//   - askStream.ts /api/v1/discuss/stream  → patched in Phase 1
//   - telemetry.ts /api/v1/telemetry       → sendBeacon replaced with keepalive fetch in Phase 1
//
// COVERED by URQL (graphql/client.ts CSRF-aware fetch wrapper):
//   - All GraphQL mutations via createClient()
//
// SAFE METHODS ONLY (GET/HEAD — no CSRF needed):
//   - login/page.tsx  GET /auth/info          → safe method
//   - login/page.tsx  HEAD /auth/desktop/info  (probe)
//   - use-server-capabilities.ts  GET /auth/desktop/info, HEAD /api/v1/mcp/http
//     — credentials: "omit", intentionally unauthenticated probes
//   - ArchitectureDiagram.tsx  GET /api/v1/diagrams/…/export/mermaid  → safe method
//
// INTENTIONALLY CSRF-EXEMPT (plan Out of scope):
//   - login/page.tsx  POST /auth/login, POST /auth/setup
//     — /auth/login is in the unprotected router group by design; no session yet
//
// SSR PROXY — not browser-executed:
//   - middleware.ts  fetch(upstreamUrl)  → Next.js edge runtime, not a browser call
//
// EventSource (sse.ts) — GET-only by protocol spec; no CSRF needed.
//   GET /api/v1/events?token=… uses query-param auth, outside the CSRF scope.

// Cookie names — probe both OSS and enterprise variants. The backend's
// JWTManager.CSRFCookieName() is the single source of truth for the server-set
// name; this list must match. If a third edition lands, extend the probe order.
const CSRF_COOKIE_NAMES = ["sourcebridge_csrf", "sourcebridge_enterprise_csrf"] as const;

/** The header name the backend reads for CSRF verification. */
export const CSRF_HEADER = "X-CSRF-Token";

/**
 * Read the CSRF token synchronously from document.cookie.
 *
 * Returns undefined in SSR (typeof document === 'undefined') and when neither
 * known CSRF cookie name is present. Callers treat undefined as "no token
 * available yet — call refreshCSRFToken() if needed."
 */
export function getCSRFToken(): string | undefined {
  if (typeof document === "undefined") return undefined;

  const raw = document.cookie;
  for (const name of CSRF_COOKIE_NAMES) {
    const value = parseCookieValue(raw, name);
    if (value !== undefined) return value;
  }
  return undefined;
}

/**
 * Fetch a fresh CSRF token from GET /api/v1/csrf-token and update the cookie.
 *
 * Uses bare fetch (NOT authFetch) to avoid recursive CSRF retry loops.
 * Single-flight: concurrent calls share one in-flight promise. The promise is
 * cleared in finally so a rejected refresh never leaves the singleton stuck.
 *
 * Returns the token string on success; returns undefined on 401/403/5xx/
 * parse-error/timeout/SSR. Callers treat undefined as "cannot recover; surface
 * a session-expired error rather than retrying."
 */
export function refreshCSRFToken(): Promise<string | undefined> {
  if (typeof document === "undefined") return Promise.resolve(undefined);

  if (inFlight) return inFlight;

  inFlight = (async () => {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 5_000);
    try {
      const res = await fetch("/api/v1/csrf-token", {
        method: "GET",
        credentials: "include",
        signal: controller.signal,
      });
      if (!res.ok) return undefined;
      const data = (await res.json()) as { csrf_token?: string };
      return data?.csrf_token ?? undefined;
    } catch {
      return undefined;
    } finally {
      clearTimeout(timer);
      inFlight = null;
    }
  })();

  return inFlight;
}

// ─── Internal ────────────────────────────────────────────────────────────────

/** In-flight refresh promise. Cleared in finally so rejections don't stick. */
let inFlight: Promise<string | undefined> | null = null;

/**
 * Parse a single cookie value by name from a raw document.cookie string.
 * Returns undefined if the name is not found.
 */
function parseCookieValue(raw: string, name: string): string | undefined {
  // cookies are separated by "; " — use a simple scan to avoid allocating many
  // split substrings on each call when the cookie jar is large.
  const prefix = name + "=";
  let start = 0;
  while (start < raw.length) {
    // skip leading spaces
    while (start < raw.length && raw[start] === " ") start++;
    const end = raw.indexOf(";", start);
    const segment = end === -1 ? raw.slice(start) : raw.slice(start, end);
    if (segment.startsWith(prefix)) {
      return decodeURIComponent(segment.slice(prefix.length));
    }
    if (end === -1) break;
    start = end + 1;
  }
  return undefined;
}
