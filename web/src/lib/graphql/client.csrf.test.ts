// TEST CONTRACT: the tests in this file use the canonical backend response shape.
//
// The backend emits exactly these bodies on CSRF rejection (internal/api/rest/csrf.go):
//   {"error":"csrf_token_missing"}   — header absent
//   {"error":"csrf_token_mismatch"}  — header present but wrong
//
// If jackson changes csrfReject() bodies, these fixtures AND the matching
// backend test (internal/api/rest/csrf_test.go::TestCSRFRejectionResponseShape)
// must be updated together. The snake_case strings here must match the backend
// exactly — the retry detector in client.ts checks for these exact values.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { _csrfAwareFetch, createClient } from "./client";

// ─── Module mock setup ────────────────────────────────────────────────────────

const mockGetCSRFToken = vi.fn<() => string | undefined>();
const mockRefreshCSRFToken = vi.fn<() => Promise<string | undefined>>();

vi.mock("@/lib/csrf-token-store", () => ({
  CSRF_HEADER: "X-CSRF-Token",
  getCSRFToken: () => mockGetCSRFToken(),
  refreshCSRFToken: () => mockRefreshCSRFToken(),
}));

vi.mock("@/lib/auth-utils", () => ({
  isTokenExpired: vi.fn(() => false),
  forceLogout: vi.fn(),
}));

// ─── Canonical backend response fixtures ─────────────────────────────────────
//
// These use the real Response constructor (not hand-rolled objects) so the
// shape is as close to production as possible. The body strings are the exact
// JSON the backend writes in csrfReject() after jackson's change.

function makeRealResponse(status: number, body: unknown = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// Canonical backend shapes — these are the strings jackson's fix will emit.
const backendCsrfMissing = () =>
  new Response('{"error":"csrf_token_missing"}', {
    status: 403,
    headers: { "Content-Type": "application/json" },
  });

const backendCsrfMismatch = () =>
  new Response('{"error":"csrf_token_mismatch"}', {
    status: 403,
    headers: { "Content-Type": "application/json" },
  });

const nonCsrf403 = () =>
  new Response('{"error":"forbidden"}', {
    status: 403,
    headers: { "Content-Type": "application/json" },
  });

beforeEach(() => {
  vi.restoreAllMocks();
  mockGetCSRFToken.mockReturnValue(undefined);
  mockRefreshCSRFToken.mockResolvedValue(undefined);
});

afterEach(() => {
  vi.restoreAllMocks();
});

// ─── Direct _csrfAwareFetch tests ─────────────────────────────────────────────
//
// These tests call _csrfAwareFetch directly (the same function wired into the
// URQL Client as the `fetch` option). Testing it directly avoids duplicating
// the retry logic in tests and ensures any future implementation change is
// caught immediately.

describe("_csrfAwareFetch — canonical backend response shape", () => {
  it("triggers refresh+retry on real backend csrf_token_missing body", async () => {
    mockGetCSRFToken.mockReturnValue("old-token");
    mockRefreshCSRFToken.mockResolvedValue("new-token");

    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(backendCsrfMissing())
      .mockResolvedValueOnce(makeRealResponse(200, { data: {} }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await _csrfAwareFetch("/api/v1/graphql", {
      method: "POST",
      body: "{}",
    });

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(result.status).toBe(200);

    // Retry must carry the refreshed token.
    const retryHeaders = new Headers(fetchMock.mock.calls[1][1]?.headers);
    expect(retryHeaders.get("X-CSRF-Token")).toBe("new-token");
  });

  it("triggers refresh+retry on real backend csrf_token_mismatch body", async () => {
    mockGetCSRFToken.mockReturnValue("stale-token");
    mockRefreshCSRFToken.mockResolvedValue("fresh-token");

    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(backendCsrfMismatch())
      .mockResolvedValueOnce(makeRealResponse(200, { data: {} }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await _csrfAwareFetch("/api/v1/graphql", {
      method: "POST",
      body: "{}",
    });

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(result.status).toBe(200);

    const retryHeaders = new Headers(fetchMock.mock.calls[1][1]?.headers);
    expect(retryHeaders.get("X-CSRF-Token")).toBe("fresh-token");
  });

  it("does NOT retry on non-CSRF 403 (role denial)", async () => {
    mockGetCSRFToken.mockReturnValue("token");
    mockRefreshCSRFToken.mockResolvedValue("new-token");

    const fetchMock = vi.fn().mockResolvedValue(nonCsrf403());
    vi.stubGlobal("fetch", fetchMock);

    const result = await _csrfAwareFetch("/api/v1/graphql", {
      method: "POST",
      body: "{}",
    });

    expect(result.status).toBe(403);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(mockRefreshCSRFToken).not.toHaveBeenCalled();
  });

  it("returns original 403 when refresh throws (does not propagate secondary error)", async () => {
    mockGetCSRFToken.mockReturnValue("token");
    mockRefreshCSRFToken.mockRejectedValue(new Error("refresh network failure"));

    const fetchMock = vi.fn().mockResolvedValue(backendCsrfMissing());
    vi.stubGlobal("fetch", fetchMock);

    // Must not throw — returns the original 403.
    const result = await _csrfAwareFetch("/api/v1/graphql", {
      method: "POST",
      body: "{}",
    });

    expect(result.status).toBe(403);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("returns original 403 when refresh resolves undefined", async () => {
    mockGetCSRFToken.mockReturnValue("token");
    mockRefreshCSRFToken.mockResolvedValue(undefined);

    const fetchMock = vi.fn().mockResolvedValue(backendCsrfMissing());
    vi.stubGlobal("fetch", fetchMock);

    const result = await _csrfAwareFetch("/api/v1/graphql", {
      method: "POST",
      body: "{}",
    });

    expect(result.status).toBe(403);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("injects X-CSRF-Token on POST when cookie is present", async () => {
    mockGetCSRFToken.mockReturnValue("csrf-abc");

    const fetchMock = vi
      .fn()
      .mockResolvedValue(makeRealResponse(200, { data: {} }));
    vi.stubGlobal("fetch", fetchMock);

    await _csrfAwareFetch("/api/v1/graphql", { method: "POST", body: "{}" });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [, init] = fetchMock.mock.calls[0];
    const headers = new Headers(init?.headers);
    expect(headers.get("X-CSRF-Token")).toBe("csrf-abc");
  });

  it("does NOT inject X-CSRF-Token on GET", async () => {
    mockGetCSRFToken.mockReturnValue("csrf-abc");

    const fetchMock = vi
      .fn()
      .mockResolvedValue(makeRealResponse(200, { data: {} }));
    vi.stubGlobal("fetch", fetchMock);

    await _csrfAwareFetch("/api/v1/graphql", { method: "GET" });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [, init] = fetchMock.mock.calls[0];
    const headers = new Headers(init?.headers);
    expect(headers.get("X-CSRF-Token")).toBeNull();
  });
});

// ─── URQL integration — stale-token rotation ──────────────────────────────────
//
// These tests exercise _csrfAwareFetch via the URQL Client's fetch option to
// verify the per-request (not per-client-creation) token read behavior.
// Assertions are unconditional: if fetch is not called, the test MUST fail.

describe("URQL client stale-token rotation (via _csrfAwareFetch)", () => {
  it("sends X-CSRF-Token header on POST when cookie is present", async () => {
    mockGetCSRFToken.mockReturnValue("csrf-abc");

    const fetchMock = vi
      .fn()
      .mockResolvedValue(makeRealResponse(200, { data: {} }));
    vi.stubGlobal("fetch", fetchMock);

    const client = createClient("test-token");
    client.mutation("mutation Test { __typename }", {}).toPromise().catch(() => {});
    await new Promise((r) => setTimeout(r, 0));

    // Unconditional: if fetch was not called, the CSRF injection is untestable.
    expect(fetchMock).toHaveBeenCalled();
    const [, init] = fetchMock.mock.calls[0];
    const headers = new Headers(init?.headers);
    expect(headers.get("X-CSRF-Token")).toBe("csrf-abc");
  });

  it("stale-token: second mutation reads post-rotation token, not stale baked-in value", async () => {
    mockGetCSRFToken
      .mockReturnValueOnce("token-A")
      .mockReturnValueOnce("token-B");

    const fetchMock = vi
      .fn()
      .mockResolvedValue(makeRealResponse(200, { data: {} }));
    vi.stubGlobal("fetch", fetchMock);

    const client = createClient("test-token");

    client.mutation("mutation One { __typename }", {}).toPromise().catch(() => {});
    await new Promise((r) => setTimeout(r, 0));

    client.mutation("mutation Two { __typename }", {}).toPromise().catch(() => {});
    await new Promise((r) => setTimeout(r, 0));

    // Unconditional: both mutations must have resulted in a fetch call.
    expect(fetchMock).toHaveBeenCalledTimes(2);
    const headers1 = new Headers(fetchMock.mock.calls[0][1]?.headers);
    const headers2 = new Headers(fetchMock.mock.calls[1][1]?.headers);
    expect(headers1.get("X-CSRF-Token")).toBe("token-A");
    expect(headers2.get("X-CSRF-Token")).toBe("token-B");
  });
});

// ─── xander CSRF-M1: retry preserves Authorization header ────────────────────
//
// Regression guard: when csrfAwareFetch retries after a CSRF 403, the retry
// must carry the Authorization header from the original init. Losing it would
// cause the retry to 401 rather than succeed.

describe("_csrfAwareFetch — Authorization header preserved on CSRF retry", () => {
  it("retry request includes Authorization header from original init", async () => {
    mockGetCSRFToken.mockReturnValue("csrf-token");
    mockRefreshCSRFToken.mockResolvedValue("new-csrf-token");

    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(backendCsrfMissing())
      .mockResolvedValueOnce(makeRealResponse(200, { data: {} }));
    vi.stubGlobal("fetch", fetchMock);

    await _csrfAwareFetch("/api/v1/graphql", {
      method: "POST",
      body: "{}",
      headers: {
        Authorization: "Bearer test-jwt-token",
        "Content-Type": "application/json",
      },
    });

    expect(fetchMock).toHaveBeenCalledTimes(2);

    // The retry (second call) must carry Authorization.
    const retryHeaders = new Headers(fetchMock.mock.calls[1][1]?.headers);
    expect(retryHeaders.get("Authorization")).toBe("Bearer test-jwt-token");
    expect(retryHeaders.get("X-CSRF-Token")).toBe("new-csrf-token");
  });
});
