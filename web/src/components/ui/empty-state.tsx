"use client";

import type { ReactNode } from "react";
import { Panel } from "@/components/ui/panel";

export function EmptyState({
  eyebrow,
  title,
  description,
  actions,
}: {
  eyebrow?: string;
  title: string;
  description: string;
  actions?: ReactNode;
}) {
  return (
    <Panel variant="accent" padding="lg" className="text-center">
      {eyebrow ? (
        <p className="text-[11px] font-semibold uppercase tracking-[0.18em] text-[var(--text-tertiary)]">
          {eyebrow}
        </p>
      ) : null}
      <h2 className="mt-3 text-2xl font-semibold tracking-[-0.03em] text-[var(--text-primary)]">
        {title}
      </h2>
      <p className="mx-auto mt-3 max-w-2xl text-sm leading-7 text-[var(--text-secondary)]">
        {description}
      </p>
      {actions ? <div className="mt-6 flex flex-wrap justify-center gap-3">{actions}</div> : null}
    </Panel>
  );
}
