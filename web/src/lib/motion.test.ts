import { describe, it, expect } from "vitest";
import { duration, spring, variants } from "./motion";

describe("motion config", () => {
  it("has correct duration values", () => {
    expect(duration.fast).toBe(0.1);
    expect(duration.normal).toBe(0.2);
    expect(duration.slow).toBe(0.5);
  });

  it("has spring presets", () => {
    expect(spring.snappy.type).toBe("spring");
    expect(spring.responsive.stiffness).toBe(300);
    expect(spring.graph.mass).toBe(1.2);
  });

  it("has animation variants", () => {
    expect(variants.fadeIn.initial).toHaveProperty("opacity", 0);
    expect(variants.slideUp.initial).toHaveProperty("y", 8);
    expect(variants.scaleIn.initial).toHaveProperty("scale", 0.95);
  });
});
