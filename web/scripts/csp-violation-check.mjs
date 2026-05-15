/**
 * CA-156: CSP regression check.
 *
 * Starts the Next.js production server (assumes `next build` already ran),
 * navigates key routes in a headless Chromium browser, and fails if any
 * Content-Security-Policy violations are reported by the browser.
 *
 * Catches future dependency drift that would reintroduce unsafe-eval
 * (regression of Phase 5 Slice 7 from the 2026-05-04 audit).
 *
 * Usage (from the web/ directory):
 *   node scripts/csp-violation-check.mjs
 *
 * Playwright must be available:
 *   npx playwright install --with-deps chromium
 *
 * Environment variables:
 *   PORT                   Port for `next start` (default: 3300)
 *   NEXT_START_TIMEOUT_MS  Max wait for server ready (default: 30000)
 */

import { chromium } from "playwright";
import { spawn } from "node:child_process";
import { createConnection } from "node:net";

const PORT = parseInt(process.env.PORT ?? "3300", 10);
const BASE_URL = `http://localhost:${PORT}`;
const START_TIMEOUT_MS = parseInt(process.env.NEXT_START_TIMEOUT_MS ?? "30000", 10);

// Routes to probe. The app redirects unauthenticated users to /login; probing
// /login loads all critical client-side bundles, which is where eval-based
// dependencies would appear.
const PROBE_ROUTES = ["/", "/login", "/onboarding"];

// ─── Port-ready helper ────────────────────────────────────────────────────────

async function waitForPortReady(port, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (true) {
    const ready = await new Promise((resolve) => {
      const client = createConnection({ port, host: "127.0.0.1" });
      client.once("connect", () => { client.destroy(); resolve(true); });
      client.once("error", () => { client.destroy(); resolve(false); });
    });
    if (ready) return;
    if (Date.now() > deadline) {
      throw new Error(`Timed out waiting for localhost:${port} after ${timeoutMs}ms`);
    }
    await new Promise((r) => setTimeout(r, 500));
  }
}

// ─── Main ─────────────────────────────────────────────────────────────────────

async function main() {
  console.log(`[csp-check] Starting next start on port ${PORT}…`);

  // Start Next.js production server. Caller must have already run `next build`.
  const server = spawn(
    "node_modules/.bin/next",
    ["start", "--port", String(PORT)],
    {
      stdio: ["ignore", "pipe", "pipe"],
      env: { ...process.env, PORT: String(PORT) },
    }
  );

  let serverExited = false;
  server.on("exit", (code) => {
    serverExited = true;
    if (code !== 0 && code !== null) {
      console.error(`[csp-check] next start exited with code ${code}`);
    }
  });
  server.stdout.on("data", (d) => process.stdout.write(d));
  server.stderr.on("data", (d) => process.stderr.write(d));

  const violations = [];
  let browser;

  try {
    console.log(`[csp-check] Waiting for server on port ${PORT}…`);
    await waitForPortReady(PORT, START_TIMEOUT_MS);
    console.log(`[csp-check] Server ready. Launching headless Chromium…`);

    browser = await chromium.launch({ args: ["--no-sandbox"] });
    const context = await browser.newContext();
    const page = await context.newPage();

    // Capture CSP violations reported via the browser console (Chromium emits
    // these as console errors) and any page-level errors from refused eval/script.
    page.on("console", (msg) => {
      const text = msg.text();
      if (
        msg.type() === "error" &&
        (text.includes("Content Security Policy") ||
          text.includes("content security policy") ||
          text.includes("unsafe-eval") ||
          text.includes("EvalError"))
      ) {
        console.error(`[csp-check] CSP console error on ${page.url()}: ${text}`);
        violations.push({ route: page.url(), message: text });
      }
    });

    page.on("pageerror", (err) => {
      const text = err.message ?? String(err);
      if (
        text.includes("Content Security Policy") ||
        text.includes("unsafe-eval") ||
        text.includes("EvalError")
      ) {
        console.error(`[csp-check] CSP page error on ${page.url()}: ${text}`);
        violations.push({ route: page.url(), message: text });
      }
    });

    for (const route of PROBE_ROUTES) {
      if (serverExited) {
        throw new Error("next start exited unexpectedly before probing completed");
      }
      const url = `${BASE_URL}${route}`;
      console.log(`[csp-check] Probing ${url}…`);
      try {
        // networkidle gives JS time to execute and any lazy imports to run.
        // A 15s timeout is generous; auth redirects settle quickly.
        await page.goto(url, { waitUntil: "networkidle", timeout: 15000 });
      } catch (err) {
        // Timeout or redirect chain — not itself a failure, but log it.
        console.warn(`[csp-check] Warning: ${url} did not settle: ${err.message}`);
      }
    }

    await browser.close();
    browser = undefined;
  } finally {
    if (browser) {
      await browser.close().catch(() => {});
    }
    // Graceful then forceful shutdown.
    if (!serverExited) {
      server.kill("SIGTERM");
      await new Promise((r) => setTimeout(r, 1500));
      if (!serverExited) server.kill("SIGKILL");
    }
  }

  if (violations.length > 0) {
    console.error("\n[csp-check] FAILED — CSP violations detected:");
    for (const v of violations) {
      console.error(`  route:   ${v.route}`);
      console.error(`  message: ${v.message}`);
    }
    console.error(
      "\nIf this is a new dependency that requires eval(), add a narrow exception " +
        "to next.config.ts rather than re-enabling unsafe-eval globally. " +
        "See CA-156 and the 2026-05-04 audit (Phase 5 Slice 7)."
    );
    process.exit(1);
  }

  console.log(
    `[csp-check] PASS — no CSP violations on ${PROBE_ROUTES.length} probed route(s).`
  );
  process.exit(0);
}

main().catch((err) => {
  console.error("[csp-check] Unexpected error:", err);
  process.exit(1);
});
