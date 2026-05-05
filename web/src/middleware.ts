import { NextRequest, NextResponse } from 'next/server';

// Stable Node.js runtime (Next.js 15.5+). Required because the default
// edge runtime cannot make outbound fetch() to private hostnames inside
// a Docker bridge network and does not support stream-by-default body
// passthrough the way Node.js fetch does.
export const runtime = 'nodejs';

// Hop-by-hop / forwarded headers stripped from the inbound request before
// forwarding upstream.
//
// Why each:
//   host                — leaving the inbound Host on the upstream request
//                         confuses Go server vhost-routing / log fields.
//   connection          — hop-by-hop per RFC 7230.
//   content-length      — let undici recompute for the streaming body.
//   x-forwarded-for     — browser can spoof this; if we forwarded, the Go
//                         API's chimiddleware.RealIP would trust it and
//                         httprate.LimitByIP would key off the spoofed IP,
//                         enabling rate-limit bypass. We strip and re-set.
//   x-real-ip           — same threat model as x-forwarded-for.
//   x-forwarded-host    — browser can spoof; not used by the Go API today
//                         but defense-in-depth.
//   x-forwarded-proto   — browser can spoof; same rationale.
const INBOUND_STRIP = new Set([
  'host',
  'connection',
  'content-length',
  'x-forwarded-for',
  'x-real-ip',
  'x-forwarded-host',
  'x-forwarded-proto',
]);

// Headers stripped from the upstream → browser response.
//
// Why each:
//   content-length           — must let Node compute or chunk; declaring
//                              it on a streaming SSE body breaks streaming.
//   transfer-encoding        — same reason.
//   set-cookie               — round-tripped explicitly via getSetCookie()
//                              below so multiple cookies aren't folded.
//   content-security-policy  | These five are set by the Go API's
//   x-frame-options          | securityHeaders middleware
//   x-content-type-options   | (internal/api/rest/router.go:1457-1473)
//   referrer-policy          | calibrated for a JSON API. The web UI sets
//   permissions-policy       | its own security headers tuned for the UI's
//                            | needs (HMR connections, PostHog, etc.). We
//                            | do not let the API's headers leak through.
const OUTBOUND_STRIP = new Set([
  'content-length',
  'transfer-encoding',
  'set-cookie',
  'content-security-policy',
  'x-frame-options',
  'x-content-type-options',
  'referrer-policy',
  'permissions-policy',
]);

function resolveUpstream(): string {
  // IMPORTANT: NEXT_PUBLIC_* vars are inlined at build time by webpack's
  // DefinePlugin in ALL compilation units — including server-side middleware.
  // Do NOT use process.env.NEXT_PUBLIC_API_URL here; it will always resolve
  // to the build-time default ("http://localhost:8080") regardless of what
  // the container's runtime environment has set.
  //
  // Instead, read SOURCEBRIDGE_WEB_DEV_PROXY (no NEXT_PUBLIC_ prefix → not
  // inlined → reads the actual runtime environment per request). Both
  // docker-compose.yml and docker-compose.hub.yml already set this var to
  // http://sourcebridge:8080. The Helm chart sets it at
  // deploy/helm/sourcebridge/templates/web-deployment.yaml:51.
  //
  // NEXT_PUBLIC_API_URL is still used for the client bundle (Settings page
  // display) via next.config.ts env:{} block — that path is intentionally
  // build-time baked and separate from this server-side resolution.
  return process.env.SOURCEBRIDGE_WEB_DEV_PROXY || 'http://localhost:8080';
}

function trustedClientIP(request: NextRequest): string | undefined {
  // The web container sits behind whatever ingress the operator deploys
  // (Traefik in homelab, Helm ingress in k8s, none in `docker compose up`).
  // We trust X-Forwarded-For from that ingress only because it is set by
  // *infrastructure* in front of us — the browser cannot reach this
  // middleware without traversing it. If the operator runs the web
  // container with no upstream proxy, fall back to the connection IP via
  // request.ip (Next.js exposes the socket peer addr there in Node.js
  // runtime). If neither is available, omit X-Forwarded-For entirely;
  // the Go API will then key rate-limits on the proxy's container IP,
  // which is acceptable degradation (worst case: shared limit across
  // all clients). DO NOT take X-Forwarded-For directly from the browser
  // request.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const ip = (request as any).ip;
  if (typeof ip === 'string' && ip.length > 0) return ip;
  return undefined;
}

export async function middleware(request: NextRequest): Promise<Response> {
  // /api/health is served by the Next.js-native handler at
  // web/src/app/api/health/route.ts — it must stay reachable even when the
  // upstream API container is stopped (kubelet liveness probe target). The
  // matcher below uses '/api/:path*' (no lookahead) because path-to-regexp
  // rejects capturing groups in Next.js 15; we guard here instead.
  //
  // CONVENTION (extend this guard if you add native /api/* routes): any new
  // web/src/app/api/<name>/route.ts file is silently proxied to the upstream
  // Go API by this middleware unless the route's path is added to the
  // exclusion below. If you intend a new route to be served *natively* by
  // Next.js (rare — most /api/* paths in this project are the Go API), add
  // its prefix to the early-return condition. If unsure, use a non-`/api/`
  // path or place the handler under a path that does not match the matcher.
  const { pathname } = new URL(request.url);
  if (pathname === '/api/health' || pathname.startsWith('/api/health/')) {
    // Let the App Router handle it natively.
    return NextResponse.next();
  }

  const upstreamBase = resolveUpstream();
  const incoming = new URL(request.url);
  const upstreamUrl = upstreamBase.replace(/\/$/, '') + incoming.pathname + incoming.search;

  // Build outgoing headers: clone, strip the inbound list, then set
  // X-Forwarded-For from a trusted source (not the inbound header).
  const headers = new Headers();
  request.headers.forEach((value, key) => {
    if (!INBOUND_STRIP.has(key.toLowerCase())) headers.set(key, value);
  });
  const clientIP = trustedClientIP(request);
  if (clientIP) headers.set('x-forwarded-for', clientIP);

  let upstream: Response;
  try {
    upstream = await fetch(upstreamUrl, {
      method: request.method,
      headers,
      body: request.method === 'GET' || request.method === 'HEAD' ? undefined : request.body,
      // duplex: 'half' is required when sending a streaming body in Node fetch.
      // @ts-expect-error — TS lib lag; supported by undici.
      duplex: 'half',
      redirect: 'manual',
      // Propagate browser disconnect → upstream cancellation. Without this,
      // SSE streams orphan upstream goroutines and DB cursors when the
      // browser closes the tab. NextRequest exposes `signal` on the
      // standard Web Request side. See Risk #5 in the plan.
      signal: request.signal,
    });
  } catch {
    // Body intentionally does NOT include the upstream host — that would
    // leak internal Docker DNS (e.g. http://sourcebridge:8080) to the
    // browser. Keep this terse.
    return new Response(
      JSON.stringify({ error: 'upstream unreachable' }),
      { status: 502, headers: { 'content-type': 'application/json' } }
    );
  }

  // Build response headers: copy everything not in OUTBOUND_STRIP,
  // then re-append Set-Cookie individually (must NOT be folded).
  const respHeaders = new Headers();
  upstream.headers.forEach((value, key) => {
    if (!OUTBOUND_STRIP.has(key.toLowerCase())) respHeaders.set(key, value);
  });
  // getSetCookie() is on the standard Headers interface in Node 20+ /
  // Next 15. Don't use .get('set-cookie') (folds) or .forEach() (also
  // folds in some implementations). The Go API never sets Domain= on
  // cookies (audited: zero matches in internal/), so cookies pass
  // through scoped to the web container's externally-visible origin.
  for (const cookie of upstream.headers.getSetCookie()) {
    respHeaders.append('set-cookie', cookie);
  }

  return new Response(upstream.body, {
    status: upstream.status,
    statusText: upstream.statusText,
    headers: respHeaders,
  });
}

export const config = {
  matcher: [
    // Note: the preferred lookahead pattern '/api/((?!health(/|$)).*)' is
    // rejected by Next.js 15's path-to-regexp (capturing groups disallowed).
    // We use the wider '/api/:path*' and guard against /api/health inside the
    // middleware function via an early NextResponse.next() return above.
    '/api/:path*',
    '/auth/:path*',
    '/healthz',
    '/readyz',
    '/metrics',
  ],
};
