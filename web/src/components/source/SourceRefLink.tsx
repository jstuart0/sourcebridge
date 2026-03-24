"use client";

import Link from "next/link";
import type { ReactNode } from "react";
import { buildRepositorySourceHref, type SourceTarget } from "@/lib/source-target";
import { cn } from "@/lib/utils";

export function SourceRefLink({
  repositoryId,
  target,
  children,
  className,
}: {
  repositoryId: string;
  target: SourceTarget;
  children: ReactNode;
  className?: string;
}) {
  return (
    <Link
      href={buildRepositorySourceHref(repositoryId, target)}
      className={cn(
        "font-mono text-[var(--accent-primary)] underline decoration-[color:var(--accent-quiet)] underline-offset-4 hover:text-[var(--accent-primary-strong)]",
        className
      )}
    >
      {children}
    </Link>
  );
}
