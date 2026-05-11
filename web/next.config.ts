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
    // 'unsafe-inline' on script-src is required by Next.js hydration scripts
    // (chunk loaders, runtime config). Migration to nonce-based CSP is a
    // separate ticket.
    const posthogHost =
      process.env.NEXT_PUBLIC_POSTHOG_HOST || "https://us.i.posthog.com";
    const cspDirectives = [
      `default-src 'self'`,
      `script-src 'self' 'unsafe-inline'`,
      `style-src 'self' 'unsafe-inline'`,
      `img-src 'self' data: blob:`,
      // wss: + ws: cover SSE/WebSocket connections (dev HMR + production EventSource)
      `connect-src 'self' wss: ws: ${posthogHost}`,
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
    ];
  },
};

export default nextConfig;
