import { describe, it, expect } from "vitest";
import { getFeatures, ossFeatures } from "./features";

describe("feature flags", () => {
  it("OSS features default to false for enterprise flags", () => {
    const features = ossFeatures;
    expect(features.multiTenant).toBe(false);
    expect(features.sso).toBe(false);
    expect(features.billing).toBe(false);
  });

  it("OSS features default to false for knowledge flags until server confirms", () => {
    const features = ossFeatures;
    expect(features.cliffNotes).toBe(false);
    expect(features.learningPaths).toBe(false);
    expect(features.symbolScopedAnalysis).toBe(false);
  });

  it("getFeatures returns OSS defaults for non-React contexts", () => {
    const features = getFeatures();
    expect(features).toEqual(ossFeatures);
  });
});
