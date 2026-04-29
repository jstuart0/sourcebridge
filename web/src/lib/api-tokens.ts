// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Shared utility for minting API tokens via POST /api/v1/tokens.
 *
 * Used by both the /settings/tokens page and the inline Claude Code wizard.
 * The request shape, auth mechanism (authFetch / web session), and response
 * handling are identical in both surfaces.
 */

import { authFetch } from "@/lib/auth-fetch";

export interface CreatedToken {
  id: string;
  name: string;
  prefix: string;
  /** Full token value — returned once, never again. */
  token: string;
  created_at: string;
}

export type MintTokenError =
  | { kind: "forbidden" }
  | { kind: "duplicate"; name: string }
  | { kind: "network" }
  | { kind: "unknown"; message: string };

export interface MintTokenResult {
  ok: true;
  token: CreatedToken;
}

export interface MintTokenFailure {
  ok: false;
  error: MintTokenError;
}

async function readErrorText(res: Response): Promise<string> {
  const text = await res.text();
  try {
    const json = JSON.parse(text);
    if (typeof json.error === "string") return json.error;
  } catch {
    /* not JSON */
  }
  return text || `HTTP ${res.status}`;
}

/**
 * Mint a new API token with the given name.
 *
 * Classified errors:
 *   - 401 / 403 → forbidden (authFetch may redirect to /login on 401)
 *   - 409       → duplicate name
 *   - network   → fetch/abort threw
 *   - unknown   → any other status
 */
export async function mintApiToken(
  name: string,
  signal?: AbortSignal,
): Promise<MintTokenResult | MintTokenFailure> {
  try {
    const res = await authFetch("/api/v1/tokens", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name }),
      signal,
    });

    if (res.ok) {
      const data = (await res.json()) as CreatedToken;
      return { ok: true, token: data };
    }

    if (res.status === 401 || res.status === 403) {
      return { ok: false, error: { kind: "forbidden" } };
    }

    if (res.status === 409) {
      return { ok: false, error: { kind: "duplicate", name } };
    }

    const message = await readErrorText(res);
    return { ok: false, error: { kind: "unknown", message } };
  } catch {
    // authFetch throws AuthFetchError on 401 (and redirects) — treat as network
    // since the redirect will handle the auth case.
    return {
      ok: false,
      error: {
        kind: "network",
      },
    };
  }
}
