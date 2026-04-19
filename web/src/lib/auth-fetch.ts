"use client";

import { clearStoredToken, getStoredToken } from "@/lib/auth-token-store";
import { isTokenExpired } from "@/lib/auth-utils";

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

export async function authFetch(path: string, init?: RequestInit): Promise<Response> {
  const token = getStoredToken();
  if (token && isTokenExpired(token)) {
    clearStoredToken();
    emitAuthFetchEvent("401");
    if (window.location.pathname !== "/login") {
      window.location.href = "/login";
    }
    throw new AuthFetchError("unauthorized", "Session expired", 401);
  }

  const headers = new Headers(init?.headers);
  if (token && !headers.has("Authorization")) {
    headers.set("Authorization", `Bearer ${token}`);
  }
  if (init?.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
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

  return res;
}
