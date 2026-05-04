// Next.js App Router health endpoint.
//
// Intentionally dependency-free: does NOT call the API server, database,
// or any external resource. The kubelet liveness/readiness probe hits this
// path directly on the pod IP — it must stay green even when downstream
// services are degraded so the web container itself is never falsely evicted.
export const dynamic = "force-dynamic"; // never cache

export async function GET() {
  return Response.json({ ok: true });
}
