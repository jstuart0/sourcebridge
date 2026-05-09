import { Client, cacheExchange, fetchExchange, mapExchange } from "urql";
import { isTokenExpired, forceLogout } from "@/lib/auth-utils";
import { CSRF_HEADER, getCSRFToken, refreshCSRFToken } from "@/lib/csrf-token-store";

/**
 * CSRF-aware fetch wrapper for URQL.
 *
 * Reads the CSRF token at request time (not at client-creation time) so
 * token rotation between requests is reflected immediately. On a 403 with a
 * CSRF error body, refreshes the token and retries once. Any other 403 (e.g.
 * role denial) flows through to URQL's normal error handling unchanged.
 *
 * If refreshCSRFToken() itself throws (network failure during refresh), the
 * original 403 response is returned rather than propagating a secondary error
 * through fetchExchange.
 */
async function csrfAwareFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const method = (init?.method ?? "GET").toUpperCase();
  const isUnsafe = ["POST", "PUT", "PATCH", "DELETE"].includes(method);

  const headers = new Headers(init?.headers);

  if (isUnsafe) {
    const csrfToken = getCSRFToken();
    if (csrfToken && !headers.has(CSRF_HEADER)) {
      headers.set(CSRF_HEADER, csrfToken);
    }
  }

  const res = await fetch(input, { ...init, headers });

  // CSRF 403 retry — one attempt only.
  if (res.status === 403 && isUnsafe) {
    let isCsrf = false;
    try {
      const clone = res.clone();
      const data = (await clone.json()) as { error?: string };
      isCsrf = data?.error === "csrf_token_missing" || data?.error === "csrf_token_mismatch";
    } catch {
      // parse failure → not a CSRF error we can handle
    }

    if (isCsrf) {
      let newToken: string | undefined;
      try {
        newToken = await refreshCSRFToken();
      } catch {
        // refreshCSRFToken() threw — return the original 403 rather than
        // propagating a secondary error through fetchExchange (bob L3).
        return res;
      }

      if (!newToken) {
        // Cannot recover; return the original 403.
        return res;
      }

      // Retry once with the new token.
      const retryHeaders = new Headers(init?.headers);
      retryHeaders.set(CSRF_HEADER, newToken);
      return fetch(input, { ...init, headers: retryHeaders });
    }
  }

  return res;
}

export function createClient(token?: string) {
  // If the token we're about to bake into the client is already expired,
  // force logout immediately instead of building a client that will 401.
  if (token && isTokenExpired(token)) {
    forceLogout();
    token = undefined;
  }

  return new Client({
    url: "/api/v1/graphql",
    exchanges: [
      cacheExchange,
      mapExchange({
        onError(error) {
          const isGraphQLAuth = error.graphQLErrors?.some(
            (e) => e.extensions?.code === "UNAUTHENTICATED"
          );

          // CombinedError.response is the raw Response object from fetch
          const isNetworkAuth =
            (error.response as { status?: number } | undefined)?.status === 401;

          if (isGraphQLAuth || isNetworkAuth) {
            forceLogout();
          }
        },
      }),
      fetchExchange,
    ],
    fetch: csrfAwareFetch,
    fetchOptions: () => ({
      headers: {
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        "Content-Type": "application/json",
      },
    }),
  });
}
