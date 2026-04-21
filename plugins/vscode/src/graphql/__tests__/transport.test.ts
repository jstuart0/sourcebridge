import { graphqlRequest, requestJSON, TransportError } from "../transport";

// Use `any` for the input arg because lib.dom types for RequestInfo
// aren't reliably present in every TS target; we only need the mock
// to resolve on *any* call.
type FetchMock = jest.Mock<Promise<Response>, [string, RequestInit?]>;

let originalFetch: typeof fetch;
let mockFetch: FetchMock;

beforeEach(() => {
  originalFetch = global.fetch;
  mockFetch = jest.fn();
  global.fetch = mockFetch as unknown as typeof fetch;
});

afterEach(() => {
  global.fetch = originalFetch;
});

function jsonResponse(body: unknown, init?: ResponseInit): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "content-type": "application/json" },
    ...init,
  });
}

describe("transport", () => {
  test("requestJSON happy path", async () => {
    mockFetch.mockResolvedValueOnce(jsonResponse({ ok: true }));
    const result = await requestJSON<{ ok: boolean }>({
      url: "http://x/api",
      method: "GET",
    });
    expect(result.ok).toBe(true);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  test("401 fails immediately (no retry)", async () => {
    mockFetch.mockResolvedValueOnce(new Response("nope", { status: 401 }));
    await expect(
      requestJSON({ url: "http://x/api", method: "GET" }),
    ).rejects.toMatchObject({ kind: "unauthenticated" });
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  test("404 fails immediately", async () => {
    mockFetch.mockResolvedValueOnce(new Response("", { status: 404 }));
    const err = await requestJSON({ url: "http://x/api", method: "GET" }).catch((e) => e) as TransportError;
    expect(err).toBeInstanceOf(TransportError);
    expect(err.kind).toBe("not-found");
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  test("500 retries then succeeds", async () => {
    mockFetch
      .mockResolvedValueOnce(new Response("fail", { status: 500 }))
      .mockResolvedValueOnce(new Response("fail2", { status: 502 }))
      .mockResolvedValueOnce(jsonResponse({ ok: true }));
    const result = await requestJSON<{ ok: boolean }>({
      url: "http://x/api",
      method: "GET",
      opts: { retryBaseMs: 1 },
    });
    expect(result.ok).toBe(true);
    expect(mockFetch).toHaveBeenCalledTimes(3);
  });

  test("network error retries up to maxRetries then throws", async () => {
    const err = Object.assign(new Error("connection refused"), { name: "TypeError" });
    mockFetch.mockRejectedValue(err);
    await expect(
      requestJSON({
        url: "http://x/api",
        method: "GET",
        opts: { retryBaseMs: 1, maxRetries: 2 },
      }),
    ).rejects.toMatchObject({ kind: "network" });
    expect(mockFetch).toHaveBeenCalledTimes(3); // initial + 2 retries
  });

  test("timeout surfaces as TransportError(kind=timeout)", async () => {
    mockFetch.mockImplementation(
      (_url, init) =>
        new Promise((_resolve, reject) => {
          // Simulate fetch that respects AbortSignal.
          (init as RequestInit).signal?.addEventListener("abort", () => {
            const e: Error & { name?: string } = new Error("aborted");
            e.name = "AbortError";
            reject(e);
          });
        }),
    );
    await expect(
      requestJSON({
        url: "http://x/api",
        method: "GET",
        opts: { timeoutMs: 25, retryBaseMs: 1, maxRetries: 0 },
      }),
    ).rejects.toMatchObject({ kind: "timeout" });
  });

  test("graphqlRequest surfaces response-level errors", async () => {
    mockFetch.mockResolvedValueOnce(
      jsonResponse({ errors: [{ message: "boom" }] }),
    );
    const err = await graphqlRequest("http://x/graphql", "{ q }", undefined, {
      retryBaseMs: 1,
    }).catch((e) => e) as TransportError;
    expect(err).toBeInstanceOf(TransportError);
    expect(err.kind).toBe("graphql");
    expect(err.message).toContain("boom");
  });

  test("graphqlRequest returns data on success", async () => {
    mockFetch.mockResolvedValueOnce(
      jsonResponse({ data: { foo: "bar" } }),
    );
    const data = await graphqlRequest<{ foo: string }>(
      "http://x/graphql",
      "{ foo }",
      undefined,
      {},
    );
    expect(data.foo).toBe("bar");
  });
});
