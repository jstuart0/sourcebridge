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
