import { RequirementsTreeProvider } from "../views/requirementsTree";
import { SourceBridgeClient } from "../graphql/client";

const mockFetch = jest.fn();
(global as any).fetch = mockFetch;

function graphqlResponse(body: unknown) {
  return Promise.resolve({
    ok: true,
    status: 200,
    text: async () => JSON.stringify({ data: body }),
  });
}

function seed(requirements: any[]) {
  // resolveRepository first queries /repositories, then the tree
  // queries /requirements.
  mockFetch
    .mockResolvedValueOnce(
      graphqlResponse({
        repositories: [
          {
            id: "repo_1",
            name: "test",
            path: "/workspace",
            status: "ready",
            hasAuth: false,
            fileCount: 1,
            functionCount: 1,
          },
        ],
      }),
    )
    .mockResolvedValueOnce(
      graphqlResponse({
        requirements: { nodes: requirements, totalCount: requirements.length },
      }),
    );
}

describe("RequirementsTreeProvider v2", () => {
  beforeEach(() => {
    mockFetch.mockReset();
  });

  it("renders an empty-state row when repo has no requirements", async () => {
    seed([]);
    const client = new SourceBridgeClient();
    const provider = new RequirementsTreeProvider(client);
    const rows = await provider.getChildren();
    expect(rows).toHaveLength(1);
    expect(rows[0].label).toBe("Create Requirement");
  });

  it("groups requirements by priority with correct ordering", async () => {
    seed([
      { id: "1", externalId: "A-1", title: "alpha", description: "", source: "m", priority: "low", tags: [] },
      { id: "2", externalId: "A-2", title: "beta", description: "", source: "m", priority: "high", tags: [] },
      { id: "3", externalId: "A-3", title: "gamma", description: "", source: "m", priority: "high", tags: [] },
    ]);
    const client = new SourceBridgeClient();
    const provider = new RequirementsTreeProvider(client);
    const rows = await provider.getChildren();
    // High should come before Low.
    expect(rows.map((r) => r.label)).toEqual(["High", "Low"]);
  });

  it("flattens when every requirement is unprioritized", async () => {
    seed([
      { id: "1", externalId: "A-1", title: "one", description: "", source: "m", priority: null, tags: [] },
      { id: "2", externalId: "A-2", title: "two", description: "", source: "m", priority: null, tags: [] },
    ]);
    const client = new SourceBridgeClient();
    const provider = new RequirementsTreeProvider(client);
    const rows = await provider.getChildren();
    // Should be RequirementItems, not a single GroupItem.
    expect(rows).toHaveLength(2);
    expect(rows[0].label).toBe("A-1");
  });

  it("filter narrows to matching requirements", async () => {
    seed([
      { id: "1", externalId: "AUTH-1", title: "login", description: "", source: "m", priority: null, tags: [] },
      { id: "2", externalId: "BILL-1", title: "pay", description: "", source: "m", priority: null, tags: [] },
    ]);
    const client = new SourceBridgeClient();
    const provider = new RequirementsTreeProvider(client);
    provider.setFilter("auth");
    const rows = await provider.getChildren();
    expect(rows).toHaveLength(1);
    expect(rows[0].label).toBe("AUTH-1");
  });
});
