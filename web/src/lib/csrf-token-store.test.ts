import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { getCSRFToken, refreshCSRFToken } from "./csrf-token-store";

// ─── Helpers ─────────────────────────────────────────────────────────────────

function setCookies(...pairs: Array<[string, string]>) {
  Object.defineProperty(document, "cookie", {
    configurable: true,
    get: () => pairs.map(([k, v]) => `${k}=${v}`).join("; "),
    set: () => undefined,
  });
}

function clearCookies() {
  Object.defineProperty(document, "cookie", {
    configurable: true,
    get: () => "",
    set: () => undefined,
  });
}

// Reset the module between tests so the inFlight singleton is cleared.
// We do this by re-importing after mocking fetch — vitest's module isolation
// handles this via beforeEach re-import of the module.
beforeEach(() => {
  clearCookies();
  vi.restoreAllMocks();
});

afterEach(() => {
  clearCookies();
});

// ─── getCSRFToken ─────────────────────────────────────────────────────────────

describe("getCSRFToken", () => {
  it("returns the OSS CSRF cookie value when present", () => {
    setCookies(["sourcebridge_csrf", "abc123"]);
    expect(getCSRFToken()).toBe("abc123");
  });

  it("returns the enterprise CSRF cookie value when OSS cookie is absent", () => {
    setCookies(["sourcebridge_enterprise_csrf", "ent-token"]);
    expect(getCSRFToken()).toBe("ent-token");
  });

  it("prefers OSS cookie over enterprise when both present", () => {
    setCookies(
      ["sourcebridge_csrf", "oss-token"],
      ["sourcebridge_enterprise_csrf", "ent-token"]
    );
    expect(getCSRFToken()).toBe("oss-token");
  });

  it("returns undefined when neither cookie is present", () => {
    setCookies(["some_other_cookie", "value"]);
    expect(getCSRFToken()).toBeUndefined();
  });

  it("returns undefined when cookie jar is empty", () => {
    clearCookies();
    expect(getCSRFToken()).toBeUndefined();
  });

  it("does not confuse a session cookie name for a CSRF cookie name", () => {
    setCookies(["sourcebridge_session", "session-value"]);
    expect(getCSRFToken()).toBeUndefined();
  });

  it("handles URL-encoded cookie values", () => {
    setCookies(["sourcebridge_csrf", encodeURIComponent("tok en+val")]);
    expect(getCSRFToken()).toBe("tok en+val");
  });
});

// ─── getCSRFToken in SSR ──────────────────────────────────────────────────────

describe("getCSRFToken SSR guard", () => {
  it("returns undefined without throwing when document is undefined", async () => {
    // Re-import the module in a context where document is undefined by
    // temporarily stubbing document access. The easiest vitest approach is to
    // test the export via a dynamic import with vi.stubGlobal.
    const origDocument = globalThis.document;
    // @ts-expect-error deliberately removing document
    delete globalThis.document;
    try {
      const { getCSRFToken: get } = await import("./csrf-token-store");
      expect(get()).toBeUndefined();
    } finally {
      globalThis.document = origDocument;
    }
  });
});

// ─── refreshCSRFToken ─────────────────────────────────────────────────────────

describe("refreshCSRFToken", () => {
  it("resolves with the csrf_token value on a 200 response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValueOnce({
        ok: true,
        json: async () => ({ csrf_token: "fresh-token" }),
      })
    );

    const result = await refreshCSRFToken();
    expect(result).toBe("fresh-token");
  });

  it("resolves with undefined on a 403 response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValueOnce({ ok: false, status: 403 })
    );

    const result = await refreshCSRFToken();
    expect(result).toBeUndefined();
  });

  it("resolves with undefined on a 5xx response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValueOnce({ ok: false, status: 500 })
    );

    const result = await refreshCSRFToken();
    expect(result).toBeUndefined();
  });

  it("resolves with undefined on a fetch network failure", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockRejectedValueOnce(new Error("network error"))
    );

    const result = await refreshCSRFToken();
    expect(result).toBeUndefined();
  });

  it("resolves with undefined on a JSON parse error", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValueOnce({
        ok: true,
        json: async () => { throw new SyntaxError("bad json"); },
      })
    );

    const result = await refreshCSRFToken();
    expect(result).toBeUndefined();
  });

  it("resolves with undefined when csrf_token field is missing from response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValueOnce({
        ok: true,
        json: async () => ({ some_other_field: "value" }),
      })
    );

    const result = await refreshCSRFToken();
    expect(result).toBeUndefined();
  });

  it("concurrent calls share a single in-flight promise (single-flight)", async () => {
    let resolvePromise!: (value: Response) => void;
    const fetchMock = vi.fn(
      () => new Promise<Response>((res) => { resolvePromise = res; })
    );
    vi.stubGlobal("fetch", fetchMock);

    const p1 = refreshCSRFToken();
    const p2 = refreshCSRFToken();
    const p3 = refreshCSRFToken();

    // Only one fetch should have been initiated
    expect(fetchMock).toHaveBeenCalledTimes(1);

    // Now resolve it
    resolvePromise({
      ok: true,
      json: async () => ({ csrf_token: "shared-token" }),
    } as unknown as Response);

    const [r1, r2, r3] = await Promise.all([p1, p2, p3]);
    expect(r1).toBe("shared-token");
    expect(r2).toBe("shared-token");
    expect(r3).toBe("shared-token");
  });

  it("clears the singleton after rejection so next call re-fetches", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn()
        .mockRejectedValueOnce(new Error("network error"))
        .mockResolvedValueOnce({
          ok: true,
          json: async () => ({ csrf_token: "second-token" }),
        })
    );

    const first = await refreshCSRFToken();
    expect(first).toBeUndefined(); // network error → undefined, singleton cleared

    const second = await refreshCSRFToken();
    expect(second).toBe("second-token"); // second call re-fetches
  });

  it("clears the singleton after 403 so next call re-fetches", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn()
        .mockResolvedValueOnce({ ok: false, status: 403 })
        .mockResolvedValueOnce({
          ok: true,
          json: async () => ({ csrf_token: "refreshed" }),
        })
    );

    const first = await refreshCSRFToken();
    expect(first).toBeUndefined();

    const second = await refreshCSRFToken();
    expect(second).toBe("refreshed");
  });
});

// ─── refreshCSRFToken SSR guard ───────────────────────────────────────────────

describe("refreshCSRFToken SSR guard", () => {
  it("resolves with undefined without calling fetch when document is undefined", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    const origDocument = globalThis.document;
    // @ts-expect-error deliberately removing document
    delete globalThis.document;
    try {
      const { refreshCSRFToken: refresh } = await import("./csrf-token-store");
      const result = await refresh();
      expect(result).toBeUndefined();
      expect(fetchMock).not.toHaveBeenCalled();
    } finally {
      globalThis.document = origDocument;
    }
  });
});
