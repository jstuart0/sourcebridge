import { describe, it, expect } from "vitest";
import config from "../../next.config";

describe("Content-Security-Policy header contract", () => {
  it("returns CSP with required directives", async () => {
    if (typeof config.headers !== "function") {
      throw new Error("next.config.ts must export a headers() function");
    }
    const result = await config.headers();
    const csp = result[0].headers.find(
      (h) => h.key === "Content-Security-Policy",
    )?.value;
    expect(csp).toBeTruthy();
    expect(csp).toContain(`frame-ancestors 'none'`);
    expect(csp).toContain(`default-src 'self'`);
    expect(csp).toContain(`object-src 'none'`);
    expect(csp).toContain("us.i.posthog.com");
    expect(csp).toContain("wss:");
  });

  it("sets X-Content-Type-Options and Referrer-Policy", async () => {
    if (typeof config.headers !== "function") {
      throw new Error("next.config.ts must export a headers() function");
    }
    const result = await config.headers();
    const hdrs = result[0].headers;
    expect(hdrs.find((h) => h.key === "X-Content-Type-Options")?.value).toBe(
      "nosniff",
    );
    expect(hdrs.find((h) => h.key === "Referrer-Policy")?.value).toBe(
      "strict-origin-when-cross-origin",
    );
  });
});
