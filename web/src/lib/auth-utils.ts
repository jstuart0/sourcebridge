import { TOKEN_KEY } from "@/lib/token-key";

/**
 * Decode a JWT payload without verifying the signature.
 * Returns null if the token is malformed.
 */
function decodeJWTPayload(token: string): Record<string, unknown> | null {
  try {
    const parts = token.split(".");
    if (parts.length !== 3) return null;
    const payload = atob(parts[1].replace(/-/g, "+").replace(/_/g, "/"));
    return JSON.parse(payload);
  } catch {
    return null;
  }
}

/**
 * Check whether a JWT token is expired (or will expire within bufferMs).
 * Returns true if expired, malformed, or missing an exp claim.
 */
export function isTokenExpired(token: string, bufferMs = 0): boolean {
  const payload = decodeJWTPayload(token);
  if (!payload || typeof payload.exp !== "number") return true;
  return Date.now() >= payload.exp * 1000 - bufferMs;
}

/**
 * Returns the number of milliseconds until the token expires.
 * Returns 0 if already expired or malformed.
 */
export function msUntilExpiry(token: string): number {
  const payload = decodeJWTPayload(token);
  if (!payload || typeof payload.exp !== "number") return 0;
  return Math.max(0, payload.exp * 1000 - Date.now());
}

/**
 * Clear the stored token and hard-navigate to the login page.
 * Safe to call multiple times (idempotent).
 */
export function forceLogout() {
  if (typeof window === "undefined") return;
  localStorage.removeItem(TOKEN_KEY);
  // Avoid redirect loop if already on the login page
  if (window.location.pathname !== "/login") {
    window.location.href = "/login";
  }
}
