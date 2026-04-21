import { RequirementDecorator, DecorationRange } from "../providers/decorator";

describe("RequirementDecorator", () => {
  it("computes decoration ranges from mock data", () => {
    const symbols = [
      { startLine: 1, endLine: 10, id: "sym-1" },
      { startLine: 15, endLine: 25, id: "sym-2" },
      { startLine: 30, endLine: 35, id: "sym-3" },
    ];

    const links = new Map<string, Array<{ confidence: string }>>();
    links.set("sym-1", [{ confidence: "HIGH" }, { confidence: "LOW" }]);
    links.set("sym-2", [{ confidence: "VERIFIED" }]);
    // sym-3 has no links

    const ranges = RequirementDecorator.computeRangesFromMockData(symbols, links);

    expect(ranges).toHaveLength(2);

    // sym-1 has HIGH as max confidence
    expect(ranges[0]).toEqual<DecorationRange>({
      startLine: 1,
      endLine: 10,
      confidence: "HIGH",
    });

    // sym-2 has VERIFIED
    expect(ranges[1]).toEqual<DecorationRange>({
      startLine: 15,
      endLine: 25,
      confidence: "VERIFIED",
    });
  });

  it("returns empty array when no symbols have links", () => {
    const symbols = [{ startLine: 1, endLine: 10, id: "sym-1" }];
    const links = new Map<string, Array<{ confidence: string }>>();

    const ranges = RequirementDecorator.computeRangesFromMockData(symbols, links);
    expect(ranges).toHaveLength(0);
  });

  it("picks highest confidence from multiple links", () => {
    const symbols = [{ startLine: 1, endLine: 10, id: "sym-1" }];
    const links = new Map<string, Array<{ confidence: string }>>();
    links.set("sym-1", [
      { confidence: "LOW" },
      { confidence: "MEDIUM" },
      { confidence: "VERIFIED" },
    ]);

    const ranges = RequirementDecorator.computeRangesFromMockData(symbols, links);
    expect(ranges[0].confidence).toBe("VERIFIED");
  });
});
