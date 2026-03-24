"use client";

import { Panel } from "@/components/ui/panel";

export function StatCard({
  label,
  value,
  detail,
}: {
  label: string;
  value: string | number;
  detail?: string;
}) {
  return (
    <Panel variant="surface" className="min-w-0 flex-1 basis-full sm:basis-auto">
      <p className="text-xs font-medium uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
        {label}
      </p>
      <div className="mt-3 flex items-end justify-between gap-4">
        <p className="text-2xl font-semibold tracking-[-0.03em] text-[var(--text-primary)] sm:text-3xl">
          {value}
        </p>
        {detail ? <p className="text-xs text-[var(--text-secondary)]">{detail}</p> : null}
      </div>
    </Panel>
  );
}
