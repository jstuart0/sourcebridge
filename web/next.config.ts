import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  output: "standalone",
  env: {
    NEXT_PUBLIC_API_URL: process.env.NEXT_PUBLIC_API_URL || "http://localhost:8080",
    NEXT_PUBLIC_POSTHOG_KEY: process.env.NEXT_PUBLIC_POSTHOG_KEY || "",
    NEXT_PUBLIC_POSTHOG_HOST: process.env.NEXT_PUBLIC_POSTHOG_HOST || "https://us.i.posthog.com",
    // Build-time version metadata (CA-136). Set by the Makefile via
    // `NEXT_PUBLIC_VERSION="$(VERSION)" npm run build` and by the web
    // Dockerfile via `ENV NEXT_PUBLIC_VERSION=${VERSION}`. Empty when
    // unwired (rare; signals a misconfigured build).
    NEXT_PUBLIC_VERSION: process.env.NEXT_PUBLIC_VERSION || "",
    NEXT_PUBLIC_COMMIT: process.env.NEXT_PUBLIC_COMMIT || "",
    NEXT_PUBLIC_BUILD_DATE: process.env.NEXT_PUBLIC_BUILD_DATE || "",
  },
  async headers() {
    // NEXT_PUBLIC_* vars are inlined at build time by webpack's DefinePlugin —
    // this is intentional here. The PostHog host is also baked at build time
    // (see web/src/lib/posthog.ts), so the CSP follows the same contract.
    //
    // The middleware.ts proxy strips content-security-policy from API responses
    // so the API's JSON-API CSP does not leak into web HTML responses. This
    // headers() function sets the web UI's own CSP (HTML pages + Next.js assets).
    //
    // CA-337 DEFERRED: 'unsafe-inline' on script-src is required by Next.js 15
    // streaming RSC hydration. The runtime inserts `self.__next_f.push(...)` inline
    // scripts into every SSR HTML page; removing 'unsafe-inline' breaks hydration.
    // The correct fix is nonce-based CSP (generate a nonce per-request in
    // middleware.ts, pass it to Next.js via the `nonce` prop, add it to CSP headers).
    // That refactor is a separate 1.0 ticket — tracking as CA-337.
    //
    // CA-537: 'unsafe-eval' is added to script-src in dev mode only. Webpack HMR
    // (hot module replacement) uses eval() to inject updated modules at runtime;
    // without this token Chrome blocks the entire page when running `next dev`.
    // Production CSP is byte-identical — NODE_ENV is never "development" in a
    // production build or `next start` run. The same isDev gate already controls
    // the dev-only WebSocket origins in connect-src (CA-338 below).
    //
    // CA-338: restrict wss:/ws: in connect-src to same-origin instead of allowing
    // the entire wss: / ws: scheme globally. In production, WebSocket connections
    // are same-origin. In dev, Next.js HMR uses ws://localhost:<port>; we allow
    // ws://localhost:* only for development builds.
    const posthogHost =
      process.env.NEXT_PUBLIC_POSTHOG_HOST || "https://us.i.posthog.com";
    const isDev = process.env.NODE_ENV === "development";
    // In dev mode, allow HMR WebSocket connections from localhost on any port.
    // In production, 'self' covers same-origin wss: connections (SSE + WS).
    const wsOrigins = isDev ? `ws://localhost:* wss://localhost:*` : ``;
    const cspDirectives = [
      `default-src 'self'`,
      `script-src 'self' 'unsafe-inline'${isDev ? " 'unsafe-eval'" : ""}`, // CA-537: unsafe-eval dev-only (webpack HMR); absent in production
      `style-src 'self' 'unsafe-inline'`,
      `img-src 'self' data: blob:`,
      // CA-338: pin WebSocket/SSE connections to same-origin (+ localhost in dev).
      // Bare 'wss:' / 'ws:' scheme tokens were removed — they allowed connecting
      // to any host on those schemes, which is unnecessary and expands the
      // exfiltration surface.
      `connect-src 'self' ${wsOrigins} ${posthogHost}`.trimEnd(),
      `font-src 'self' data:`,
      `form-action 'self'`,
      `frame-ancestors 'none'`,
      `base-uri 'self'`,
      `object-src 'none'`,
    ].join("; ");
    return [
      {
        source: "/:path*",
        headers: [
          { key: "Content-Security-Policy", value: cspDirectives },
          { key: "X-Content-Type-Options", value: "nosniff" },
          { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
        ],
      },
    ];
  },
  async redirects() {
    return [
      {
        source: "/admin/settings/comprehension",
        destination: "/admin/comprehension",
        permanent: true,
      },
      {
        source: "/admin/settings/comprehension/:path*",
        destination: "/admin/comprehension/:path*",
        permanent: true,
      },
      {
        // CA-538: /setup was a 404 — the setup form lives at /login (the
        // login page renders the first-time setup form when setup_done=false).
        // Use 307 (permanent: false) so browsers never cache this redirect;
        // a future dedicated /setup route can override by removing this entry.
        source: "/setup",
        destination: "/login",
        permanent: false,
      },
    ];
  },
};

export default nextConfig;
