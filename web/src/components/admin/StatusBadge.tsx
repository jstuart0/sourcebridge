"use client";

export function StatusBadge({ status }: { status: string }) {
  const color =
    status === "healthy" || status === "ok"
      ? "var(--color-success, #22c55e)"
      : status === "degraded"
      ? "var(--color-warning, #eab308)"
      : "var(--color-error, #ef4444)";
  return (
    <span
      className="rounded-full border px-2 py-0.5 text-xs"
      style={{ borderColor: color, color }}
    >
      {status}
    </span>
  );
}
