"use client";

import { clearStoredToken, getStoredToken } from "@/lib/auth-token-store";
import { isTokenExpired } from "@/lib/auth-utils";
import { CSRF_HEADER, getCSRFToken, refreshCSRFToken } from "@/lib/csrf-token-store";

export type AuthFetchErrorKind = "network" | "unauthorized";

export class AuthFetchError extends Error {
  kind: AuthFetchErrorKind;
  status?: number;

  constructor(kind: AuthFetchErrorKind, message: string, status?: number) {
    super(message);
    this.kind = kind;
    this.status = status;
  }
}

function emitAuthFetchEvent(type: "401" | "429" | "503" | "network-fail", detail?: Record<string, unknown>) {
  if (typeof window === "undefined") return;
  window.postMessage({ source: "auth-fetch", type, ...detail }, window.location.origin);
}

export interface AuthFetchOptions extends RequestInit {
  /** Optional base URL prepended to path. Useful when callers hold a
   *  separate base (e.g. enterprise API root) and a relative sub-path. */
  baseURL?: string;
}

/** The HTTP methods that require a CSRF token. */
const UNSAFE_METHODS = new Set(["POST", "PUT", "PATCH", "DELETE"]);

/**
 * Check whether a 403 response body indicates a CSRF rejection (as opposed to
 * a role-denial or other 403). We only retry on CSRF errors — non-CSRF 403s
 * (e.g. RequireRole denials) surface unchanged so the caller can act on them.
 */
async function isCsrfError(res: Response): Promise<boolean> {
  try {
    const clone = res.clone();
    const data = (await clone.json()) as { error?: string };
    return data?.error === "csrf_token_missing" || data?.error === "csrf_token_mismatch";
  } catch {
    return false;
  }
}

export async function authFetch(path: string, opts: AuthFetchOptions = {}): Promise<Response> {
  const { baseURL, ...init } = opts;
  if (baseURL) {
    path = `${baseURL}${path}`;
  }
  const token = getStoredToken();
  if (token && isTokenExpired(token)) {
    clearStoredToken();
    emitAuthFetchEvent("401");
    if (window.location.pathname !== "/login") {
      window.location.href = "/login";
    }
    throw new AuthFetchError("unauthorized", "Session expired", 401);
  }

  const method = (init?.method ?? "GET").toUpperCase();

  const headers = new Headers(init?.headers);
  if (token && !headers.has("Authorization")) {
    headers.set("Authorization", `Bearer ${token}`);
  }
  if (init?.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }

  // Inject CSRF token on non-safe verbs (POST/PUT/PATCH/DELETE).
  if (UNSAFE_METHODS.has(method) && !headers.has(CSRF_HEADER)) {
    const csrfToken = getCSRFToken();
    if (csrfToken) {
      headers.set(CSRF_HEADER, csrfToken);
    }
  }

  let res: Response;
  try {
    res = await fetch(path, { ...init, headers });
  } catch (error) {
    emitAuthFetchEvent("network-fail");
    throw new AuthFetchError(
      "network",
      error instanceof Error ? error.message : "Network request failed"
    );
  }

  if (res.status === 401) {
    clearStoredToken();
    emitAuthFetchEvent("401");
    if (window.location.pathname !== "/login") {
      window.location.href = "/login";
    }
    throw new AuthFetchError("unauthorized", "Session expired", 401);
  }
  if (res.status === 429) {
    emitAuthFetchEvent("429", { retryAfter: res.headers.get("Retry-After") });
  } else if (res.status === 503) {
    emitAuthFetchEvent("503", { retryAfter: res.headers.get("Retry-After") });
  }

  // CSRF 403 retry — one attempt only.
  // If the body is a ReadableStream the first fetch already consumed it; we
  // cannot replay the request body. Surface the 403 immediately in that case.
  if (res.status === 403 && UNSAFE_METHODS.has(method)) {
    if (init?.body instanceof ReadableStream) {
      // Cannot retry: stream is consumed. Surface the original 403.
      return res;
    }

    const csrfFailure = await isCsrfError(res);
    if (csrfFailure) {
      const newToken = await refreshCSRFToken();
      if (!newToken) {
        throw new AuthFetchError(
          "unauthorized",
          "Session expired — please refresh the page.",
          403
        );
      }

      // Retry once with the refreshed token.
      const retryHeaders = new Headers(headers);
      retryHeaders.set(CSRF_HEADER, newToken);
      const retryRes = await fetch(path, { ...init, headers: retryHeaders });

      if (retryRes.status === 401) {
        clearStoredToken();
        emitAuthFetchEvent("401");
        if (window.location.pathname !== "/login") {
          window.location.href = "/login";
        }
        throw new AuthFetchError("unauthorized", "Session expired", 401);
      }
      return retryRes;
    }
  }

  return res;
}
