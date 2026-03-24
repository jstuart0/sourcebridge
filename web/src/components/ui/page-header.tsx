"use client";

import type { HTMLAttributes, ReactNode } from "react";
import { cn } from "@/lib/utils";

type PageHeaderProps = HTMLAttributes<HTMLDivElement> & {
  title: string;
  eyebrow?: string;
  description?: string;
  actions?: ReactNode;
};

export function PageHeader({
  title,
  eyebrow,
  description,
  actions,
  className,
  ...props
}: PageHeaderProps) {
  return (
    <div
      className={cn(
        "flex flex-col gap-4 border-b border-[var(--border-subtle)] pb-5 md:flex-row md:items-end md:justify-between",
        className
      )}
      {...props}
    >
      <div className="max-w-3xl space-y-2">
        {eyebrow ? (
          <p className="text-[11px] font-semibold uppercase tracking-[0.18em] text-[var(--text-tertiary)]">
            {eyebrow}
          </p>
        ) : null}
        <h1 className="text-2xl font-semibold tracking-[-0.03em] text-[var(--text-primary)] sm:text-3xl md:text-4xl">
          {title}
        </h1>
        {description ? (
          <p className="text-sm leading-7 text-[var(--text-secondary)] md:text-[15px]">
            {description}
          </p>
        ) : null}
      </div>
      {actions ? <div className="flex items-center gap-3">{actions}</div> : null}
    </div>
  );
}
