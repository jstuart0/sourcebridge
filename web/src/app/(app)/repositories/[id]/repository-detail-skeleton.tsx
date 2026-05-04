export function RepositoryDetailSkeleton() {
  return (
    <div className="animate-pulse space-y-4">
      <div className="h-8 w-1/2 bg-[var(--bg-surface)] rounded" />
      <div className="h-10 w-full bg-[var(--bg-surface)] rounded" />
      <div className="h-64 w-full bg-[var(--bg-surface)] rounded" />
    </div>
  );
}
