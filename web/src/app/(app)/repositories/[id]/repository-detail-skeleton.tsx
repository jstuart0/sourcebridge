export function RepositoryDetailSkeleton() {
  return (
    <div className="animate-pulse space-y-6">
      {/* Breadcrumb slot */}
      <div className="h-4 w-40 rounded bg-[var(--bg-surface)]" />

      {/* Header bar: title + status badge */}
      <div className="flex items-center gap-4">
        <div className="h-8 w-1/3 rounded bg-[var(--bg-surface)]" />
        <div className="h-6 w-16 rounded-full bg-[var(--bg-surface)]" />
      </div>

      {/* Tab strip: 4 visible tab placeholders */}
      <div className="flex gap-2 border-b border-[var(--border-subtle)] pb-4">
        {[120, 90, 110, 100].map((w, i) => (
          <div
            key={i}
            className="h-10 rounded-[var(--control-radius)] bg-[var(--bg-surface)]"
            style={{ width: w }}
          />
        ))}
      </div>

      {/* Panel placeholders (accordion-shaped) */}
      <div className="space-y-4">
        <div className="h-16 w-full rounded-[var(--control-radius)] bg-[var(--bg-surface)]" />
        <div className="h-48 w-full rounded-[var(--control-radius)] bg-[var(--bg-surface)]" />
        <div className="h-32 w-full rounded-[var(--control-radius)] bg-[var(--bg-surface)]" />
      </div>
    </div>
  );
}
