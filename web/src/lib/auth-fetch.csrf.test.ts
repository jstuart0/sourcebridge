import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { authFetch, AuthFetchError } from "./auth-fetch";

// ─── Module mock setup ────────────────────────────────────────────────────────

// We mock the two CSRF-store exports so we can control token values per test
// without manipulating document.cookie.
const mockGetCSRFToken = vi.fn<() => string | undefined>();
const mockRefreshCSRFToken = vi.fn<() => Promise<string | undefined>>();

vi.mock("@/lib/csrf-token-store", () => ({
  CSRF_HEADER: "X-CSRF-Token",
  getCSRFToken: () => mockGetCSRFToken(),
  refreshCSRFToken: () => mockRefreshCSRFToken(),
}));

// Mock auth-token-store so we can control the stored JWT.
const mockGetStoredToken = vi.fn<() => string | null>();
vi.mock("@/lib/auth-token-store", () => ({
  getStoredToken: () => mockGetStoredToken(),
  clearStoredToken: vi.fn(),
}));

// Mock auth-utils so token expiry is predictable.
vi.mock("@/lib/auth-utils", () => ({
  isTokenExpired: vi.fn(() => false),
}));

// ─── Test helpers ─────────────────────────────────────────────────────────────

function makeResponse(status: number, body: unknown = {}): Response {
  const bodyStr = JSON.stringify(body);
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: new Headers({ "Content-Type": "application/json" }),
    clone: () => makeResponse(status, body),
    json: async () => body,
    text: async () => bodyStr,
    body: null,
  } as unknown as Response;
}

const csrfMissingResponse = () => makeResponse(403, { error: "csrf_token_missing" });
const csrfMismatchResponse = () => makeResponse(403, { error: "csrf_token_mismatch" });
const forbiddenResponse = () => makeResponse(403, { error: "forbidden" });

beforeEach(() => {
  vi.restoreAllMocks();
  mockGetStoredToken.mockReturnValue("test-jwt-token");
  mockGetCSRFToken.mockReturnValue(undefined);
  mockRefreshCSRFToken.mockResolvedValue(undefined);

  // Stub window.location so redirect checks don't throw
  Object.defineProperty(window, "location", {
    configurable: true,
    value: { pathname: "/dashboard", href: "" },
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

// ─── CSRF token injection ─────────────────────────────────────────────────────

describe("CSRF token injection", () => {
  it("injects X-CSRF-Token on POST when cookie is present", async () => {
    mockGetCSRFToken.mockReturnValue("my-csrf-token");
    const fetchMock = vi.fn().mockResolvedValue(makeResponse(200));
    vi.stubGlobal("fetch", fetchMock);

    await authFetch("/api/v1/admin/test", { method: "POST", body: '{}' });

    const sentHeaders = fetchMock.mock.calls[0][1]?.headers as Headers;
    expect(sentHeaders.get("X-CSRF-Token")).toBe("my-csrf-token");
  });

  it("does NOT inject X-CSRF-Token on GET", async () => {
    mockGetCSRFToken.mockReturnValue("my-csrf-token");
    const fetchMock = vi.fn().mockResolvedValue(makeResponse(200));
    vi.stubGlobal("fetch", fetchMock);

    await authFetch("/api/v1/repositories", { method: "GET" });

    const sentHeaders = fetchMock.mock.calls[0][1]?.headers as Headers;
    expect(sentHeaders.get("X-CSRF-Token")).toBeNull();
  });

  it("does NOT inject X-CSRF-Token when cookie is absent", async () => {
    mockGetCSRFToken.mockReturnValue(undefined);
    const fetchMock = vi.fn().mockResolvedValue(makeResponse(200));
    vi.stubGlobal("fetch", fetchMock);

    await authFetch("/api/v1/admin/test", { method: "POST", body: '{}' });

    const sentHeaders = fetchMock.mock.calls[0][1]?.headers as Headers;
    expect(sentHeaders.get("X-CSRF-Token")).toBeNull();
  });

  it("injects X-CSRF-Token on PUT, PATCH, and DELETE", async () => {
    mockGetCSRFToken.mockReturnValue("csrf-val");
    const fetchMock = vi.fn().mockResolvedValue(makeResponse(200));
    vi.stubGlobal("fetch", fetchMock);

    for (const method of ["PUT", "PATCH", "DELETE"]) {
      fetchMock.mockClear();
      await authFetch("/api/v1/admin/test", { method });
      const sentHeaders = fetchMock.mock.calls[0][1]?.headers as Headers;
      expect(sentHeaders.get("X-CSRF-Token")).toBe("csrf-val");
    }
  });

  it("stale-token: reads cookie at request time, not at module load time", async () => {
    // First call gets token A, second call gets token B (simulates rotation).
    mockGetCSRFToken
      .mockReturnValueOnce("token-A")
      .mockReturnValueOnce("token-B");

    const fetchMock = vi.fn().mockResolvedValue(makeResponse(200));
    vi.stubGlobal("fetch", fetchMock);

    await authFetch("/api/v1/admin/test", { method: "POST", body: '{}' });
    const headers1 = fetchMock.mock.calls[0][1]?.headers as Headers;
    expect(headers1.get("X-CSRF-Token")).toBe("token-A");

    await authFetch("/api/v1/admin/test", { method: "POST", body: '{}' });
    const headers2 = fetchMock.mock.calls[1][1]?.headers as Headers;
    expect(headers2.get("X-CSRF-Token")).toBe("token-B");
  });
});

// ─── 403 CSRF retry ───────────────────────────────────────────────────────────

describe("403 CSRF retry", () => {
  it("refreshes and retries once on csrf_token_missing", async () => {
    mockGetCSRFToken.mockReturnValue("old-token");
    mockRefreshCSRFToken.mockResolvedValue("new-token");

    const fetchMock = vi.fn()
      .mockResolvedValueOnce(csrfMissingResponse())
      .mockResolvedValueOnce(makeResponse(200));
    vi.stubGlobal("fetch", fetchMock);

    const res = await authFetch("/api/v1/admin/test", { method: "POST", body: '{}' });

    expect(res.status).toBe(200);
    expect(fetchMock).toHaveBeenCalledTimes(2);

    // Retry must use the new token
    const retryHeaders = fetchMock.mock.calls[1][1]?.headers as Headers;
    expect(retryHeaders.get("X-CSRF-Token")).toBe("new-token");
  });

  it("refreshes and retries once on csrf_token_mismatch", async () => {
    mockGetCSRFToken.mockReturnValue("stale-token");
    mockRefreshCSRFToken.mockResolvedValue("fresh-token");

    const fetchMock = vi.fn()
      .mockResolvedValueOnce(csrfMismatchResponse())
      .mockResolvedValueOnce(makeResponse(200));
    vi.stubGlobal("fetch", fetchMock);

    const res = await authFetch("/api/v1/admin/test", { method: "POST", body: '{}' });

    expect(res.status).toBe(200);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    const retryHeaders = fetchMock.mock.calls[1][1]?.headers as Headers;
    expect(retryHeaders.get("X-CSRF-Token")).toBe("fresh-token");
  });

  it("throws AuthFetchError when refresh returns undefined on csrf_token_missing", async () => {
    mockGetCSRFToken.mockReturnValue("old-token");
    mockRefreshCSRFToken.mockResolvedValue(undefined);

    const fetchMock = vi.fn().mockResolvedValue(csrfMissingResponse());
    vi.stubGlobal("fetch", fetchMock);

    await expect(
      authFetch("/api/v1/admin/test", { method: "POST", body: '{}' })
    ).rejects.toThrow(AuthFetchError);

    // Should not have retried the original request
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("does NOT retry on non-CSRF 403 (role denial)", async () => {
    mockGetCSRFToken.mockReturnValue("token");
    mockRefreshCSRFToken.mockResolvedValue("new-token");

    const fetchMock = vi.fn().mockResolvedValue(forbiddenResponse());
    vi.stubGlobal("fetch", fetchMock);

    // Should NOT throw — returns the 403 response unchanged
    const res = await authFetch("/api/v1/admin/test", { method: "POST", body: '{}' });

    expect(res.status).toBe(403);
    expect(mockRefreshCSRFToken).not.toHaveBeenCalled();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("does NOT retry when body is a ReadableStream (stream consumed)", async () => {
    mockGetCSRFToken.mockReturnValue("token");
    mockRefreshCSRFToken.mockResolvedValue("new-token");

    const stream = new ReadableStream();
    const fetchMock = vi.fn().mockResolvedValue(csrfMissingResponse());
    vi.stubGlobal("fetch", fetchMock);

    // Should return the 403 response, not retry
    const res = await authFetch("/api/v1/admin/test", {
      method: "POST",
      body: stream,
    });

    expect(res.status).toBe(403);
    expect(mockRefreshCSRFToken).not.toHaveBeenCalled();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("does not recurse — second 403 returns the response, does not retry again", async () => {
    mockGetCSRFToken.mockReturnValue("token");
    mockRefreshCSRFToken.mockResolvedValue("new-token");

    const fetchMock = vi.fn()
      .mockResolvedValueOnce(csrfMissingResponse()) // first attempt → 403
      .mockResolvedValueOnce(csrfMissingResponse()); // retry → also 403
    vi.stubGlobal("fetch", fetchMock);

    // The retry 403 is returned to the caller; no further recursion.
    const res = await authFetch("/api/v1/admin/test", { method: "POST", body: '{}' });

    expect(res.status).toBe(403);
    // exactly two fetch calls: original + one retry
    expect(fetchMock).toHaveBeenCalledTimes(2);
    // refreshCSRFToken called exactly once
    expect(mockRefreshCSRFToken).toHaveBeenCalledTimes(1);
  });
});
