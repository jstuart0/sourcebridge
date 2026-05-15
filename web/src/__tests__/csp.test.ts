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
    // CA-338: bare wss:/ws: scheme tokens were replaced with 'self' (+ localhost in dev).
    // Tests run with NODE_ENV=test (not "development"), so the CSP must contain
    // 'self' in connect-src and must NOT allow wss:/ws: to arbitrary hosts.
    // In dev builds the CSP additionally includes ws://localhost:* and wss://localhost:*.
    expect(csp).toContain(`connect-src 'self'`);
    // Verify the pre-CA-338 unrestricted wss:/ws: scheme tokens are gone.
    // This pins the security tightening and prevents accidental regression.
    const connectSrc = csp
      ?.split(";")
      .map((d) => d.trim())
      .find((d) => d.startsWith("connect-src"));
    expect(connectSrc).toBeDefined();
    // The connect-src directive must not contain a bare scheme-only token
    // (e.g. "wss:" with no host) which would allow WebSocket connections to any host.
    expect(connectSrc).not.toMatch(/\bwss:\s/);
    expect(connectSrc).not.toMatch(/\bws:\s/);
    expect(connectSrc).not.toMatch(/\bwss:$/);
    expect(connectSrc).not.toMatch(/\bws:$/);
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
