import { SourceBridgeClient } from "../graphql/client";

// Mock fetch globally
const mockFetch = jest.fn();
(global as any).fetch = mockFetch;

describe("SourceBridgeClient", () => {
  let client: SourceBridgeClient;

  beforeEach(() => {
    mockFetch.mockReset();
    client = new SourceBridgeClient();
  });

  it("connects and returns data", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: { health: { status: "ok" } },
      }),
    });

    const result = await client.query<{ health: { status: string } }>(
      "query { health { status } }"
    );
    expect(result.health.status).toBe("ok");
    expect(mockFetch).toHaveBeenCalledWith(
      "http://localhost:8080/api/v1/graphql",
      expect.objectContaining({
        method: "POST",
        headers: expect.objectContaining({ "Content-Type": "application/json" }),
      })
    );
  });

  it("throws on GraphQL errors", async () => {
    // The transport uses Response#text() to buffer bodies; provide a
    // shape that exposes it. Only one mock suffices because GraphQL
    // "response-level" errors are not retryable.
    mockFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({ errors: [{ message: "Something went wrong" }] }),
        { status: 200, headers: { "content-type": "application/json" } }
      )
    );

    // TransportError message for a graphql error is the joined
    // `errors[].message` values; check by substring.
    await expect(client.query("query { bad }")).rejects.toThrow("Something went wrong");
  });

  it("throws on HTTP errors", async () => {
    // Transport retries 5xx up to 3 times. Provide three responses.
    const bad = () =>
      new Response("fail", { status: 500, statusText: "Internal Server Error" });
    mockFetch
      .mockResolvedValueOnce(bad())
      .mockResolvedValueOnce(bad())
      .mockResolvedValueOnce(bad())
      .mockResolvedValueOnce(bad());

    await expect(client.query("query { bad }")).rejects.toThrow("HTTP 500");
  });

  it("checks server health", async () => {
    mockFetch.mockResolvedValueOnce({ ok: true });

    const running = await client.isServerRunning();
    expect(running).toBe(true);
  });

  it("returns false when server is down", async () => {
    mockFetch.mockRejectedValueOnce(new Error("Connection refused"));

    const running = await client.isServerRunning();
    expect(running).toBe(false);
  });
});

describe("SourceBridgeClient.getRepositories", () => {
  let client: SourceBridgeClient;

  beforeEach(() => {
    mockFetch.mockReset();
    client = new SourceBridgeClient();
  });

  it("returns a list of repositories", async () => {
    const mockRepos = [
      {
        id: "repo-1",
        name: "my-project",
        path: "/workspace/my-project",
        status: "indexed",
        fileCount: 42,
        functionCount: 128,
      },
      {
        id: "repo-2",
        name: "another-project",
        path: "/workspace/another-project",
        status: "indexing",
        fileCount: 10,
        functionCount: 30,
      },
    ];

    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ data: { repositories: mockRepos } }),
    });

    const repos = await client.getRepositories();
    expect(repos).toHaveLength(2);
    expect(repos[0].id).toBe("repo-1");
    expect(repos[0].name).toBe("my-project");
    expect(repos[0].fileCount).toBe(42);
    expect(repos[1].status).toBe("indexing");
  });

  it("returns an empty array when no repositories exist", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ data: { repositories: [] } }),
    });

    const repos = await client.getRepositories();
    expect(repos).toHaveLength(0);
  });

  it("sends the REPOSITORIES query string", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ data: { repositories: [] } }),
    });

    await client.getRepositories();

    expect(mockFetch).toHaveBeenCalledWith(
      "http://localhost:8080/api/v1/graphql",
      expect.objectContaining({
        method: "POST",
        body: expect.stringContaining("Repositories"),
      })
    );
  });
});

describe("SourceBridgeClient.discussCode", () => {
  let client: SourceBridgeClient;

  beforeEach(() => {
    mockFetch.mockReset();
    client = new SourceBridgeClient();
  });

  it("returns discussion result with all fields", async () => {
    const mockResult = {
      answer: "This function calculates the sum of two numbers.",
      references: ["src/math.ts:10", "src/utils.ts:25"],
      relatedRequirements: ["REQ-001"],
      model: "llama3",
      inputTokens: 150,
      outputTokens: 50,
    };

    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ data: { discussCode: mockResult } }),
    });

    const result = await client.discussCode(
      "repo-1",
      "What does this do?",
      "src/math.ts",
      "function add(a, b) { return a + b; }",
      "typescript"
    );

    expect(result.answer).toBe("This function calculates the sum of two numbers.");
    expect(result.references).toEqual(["src/math.ts:10", "src/utils.ts:25"]);
    expect(result.relatedRequirements).toEqual(["REQ-001"]);
    expect(result.model).toBe("llama3");
    expect(result.inputTokens).toBe(150);
    expect(result.outputTokens).toBe(50);
  });

  it("sends all parameters correctly in the request body", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          discussCode: {
            answer: "answer",
            references: [],
            relatedRequirements: [],
            model: "llama3",
            inputTokens: 0,
            outputTokens: 0,
          },
        },
      }),
    });

    await client.discussCode(
      "repo-42",
      "Explain this code",
      "src/index.ts",
      "console.log('hello')",
      "typescript"
    );

    expect(mockFetch).toHaveBeenCalledTimes(1);
    const [, options] = mockFetch.mock.calls[0];
    const body = JSON.parse(options.body);
    expect(body.variables.input).toEqual({
      repositoryId: "repo-42",
      question: "Explain this code",
      filePath: "src/index.ts",
      code: "console.log('hello')",
      language: "TYPESCRIPT",
    });
  });

  it("omits optional parameters when not provided", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          discussCode: {
            answer: "answer",
            references: [],
            relatedRequirements: [],
            model: "llama3",
            inputTokens: 0,
            outputTokens: 0,
          },
        },
      }),
    });

    await client.discussCode("repo-1", "What is this project about?");

    const [, options] = mockFetch.mock.calls[0];
    const body = JSON.parse(options.body);
    expect(body.variables.input).toEqual({
      repositoryId: "repo-1",
      question: "What is this project about?",
    });
    expect(body.variables.input.filePath).toBeUndefined();
    expect(body.variables.input.code).toBeUndefined();
    expect(body.variables.input.language).toBeUndefined();
  });

  it("sends the DISCUSS_CODE mutation string", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          discussCode: {
            answer: "",
            references: [],
            relatedRequirements: [],
            model: "",
            inputTokens: 0,
            outputTokens: 0,
          },
        },
      }),
    });

    await client.discussCode("repo-1", "question");

    const [, options] = mockFetch.mock.calls[0];
    const body = JSON.parse(options.body);
    expect(body.query).toContain("DiscussCode");
    expect(body.query).toContain("DiscussCodeInput");
  });
});

describe("SourceBridgeClient.reviewCode", () => {
  let client: SourceBridgeClient;

  beforeEach(() => {
    mockFetch.mockReset();
    client = new SourceBridgeClient();
  });

  it("returns review result with findings", async () => {
    const mockResult = {
      template: "security",
      findings: [
        {
          category: "injection",
          severity: "high",
          message: "Potential SQL injection",
          filePath: "src/db.ts",
          startLine: 10,
          endLine: 12,
          suggestion: "Use parameterized queries",
        },
      ],
      score: 72,
      model: "llama3",
      inputTokens: 200,
      outputTokens: 100,
    };

    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ data: { reviewCode: mockResult } }),
    });

    const result = await client.reviewCode("repo-1", "src/db.ts", "security");

    expect(result.template).toBe("security");
    expect(result.score).toBe(72);
    expect(result.findings).toHaveLength(1);
    expect(result.findings[0].severity).toBe("high");
    expect(result.findings[0].message).toBe("Potential SQL injection");
    expect(result.findings[0].suggestion).toBe("Use parameterized queries");
    expect(result.model).toBe("llama3");
  });

  it("sends all parameters correctly in the request body", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          reviewCode: {
            template: "solid",
            findings: [],
            score: 95,
            model: "llama3",
            inputTokens: 0,
            outputTokens: 0,
          },
        },
      }),
    });

    await client.reviewCode(
      "repo-99",
      "src/handlers/auth.ts",
      "solid",
      "export async function login() {}",
      "typescript"
    );

    expect(mockFetch).toHaveBeenCalledTimes(1);
    const [, options] = mockFetch.mock.calls[0];
    const body = JSON.parse(options.body);
    expect(body.variables.input).toEqual({
      repositoryId: "repo-99",
      filePath: "src/handlers/auth.ts",
      template: "solid",
      code: "export async function login() {}",
      language: "TYPESCRIPT",
    });
  });

  it("sends the REVIEW_CODE mutation string", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          reviewCode: {
            template: "performance",
            findings: [],
            score: 100,
            model: "",
            inputTokens: 0,
            outputTokens: 0,
          },
        },
      }),
    });

    await client.reviewCode("repo-1", "src/main.ts", "performance");

    const [, options] = mockFetch.mock.calls[0];
    const body = JSON.parse(options.body);
    expect(body.query).toContain("ReviewCode");
    expect(body.query).toContain("ReviewCodeInput");
  });

  it("returns empty findings array when code is clean", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          reviewCode: {
            template: "security",
            findings: [],
            score: 100,
            model: "llama3",
            inputTokens: 50,
            outputTokens: 20,
          },
        },
      }),
    });

    const result = await client.reviewCode("repo-1", "src/clean.ts", "security");
    expect(result.findings).toHaveLength(0);
    expect(result.score).toBe(100);
  });
});

describe("SourceBridgeClient.isServerRunning", () => {
  let client: SourceBridgeClient;

  beforeEach(() => {
    mockFetch.mockReset();
    client = new SourceBridgeClient();
  });

  it("probes /healthz first (liveness) so a degraded /readyz doesn't block the extension", async () => {
    mockFetch.mockResolvedValueOnce({ ok: true });

    await client.isServerRunning();

    expect(mockFetch).toHaveBeenCalledWith(
      "http://localhost:8080/healthz",
      expect.objectContaining({ method: "GET" })
    );
    // Verify it is NOT calling the old health endpoint.
    expect(mockFetch).not.toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/health"),
      expect.anything()
    );
  });

  it("falls back to /readyz when /healthz fails", async () => {
    mockFetch
      .mockRejectedValueOnce(new Error("network error"))
      .mockResolvedValueOnce({ ok: true });

    const ok = await client.isServerRunning();
    expect(ok).toBe(true);
    expect(mockFetch).toHaveBeenCalledWith(
      "http://localhost:8080/healthz",
      expect.objectContaining({ method: "GET" }),
    );
    expect(mockFetch).toHaveBeenCalledWith(
      "http://localhost:8080/readyz",
      expect.objectContaining({ method: "GET" }),
    );
  });

  it("uses a timeout signal", async () => {
    mockFetch.mockResolvedValueOnce({ ok: true });

    await client.isServerRunning();

    const [, options] = mockFetch.mock.calls[0];
    expect(options.signal).toBeDefined();
  });

  it("returns false when server responds with non-ok status", async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, status: 503 });

    const running = await client.isServerRunning();
    expect(running).toBe(false);
  });

  it("returns false on network error", async () => {
    mockFetch.mockRejectedValueOnce(new Error("ECONNREFUSED"));

    const running = await client.isServerRunning();
    expect(running).toBe(false);
  });

  it("returns false on timeout", async () => {
    mockFetch.mockRejectedValueOnce(new DOMException("The operation was aborted", "AbortError"));

    const running = await client.isServerRunning();
    expect(running).toBe(false);
  });
});
