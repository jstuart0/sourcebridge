"use client";

import type { HTMLAttributes } from "react";
import { cn } from "@/lib/utils";

export function PageFrame({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "mx-auto flex w-full max-w-[var(--content-max-width)] flex-col gap-5 px-3 py-4 sm:gap-6 sm:px-4 sm:py-6 md:gap-8 md:px-8 md:py-8",
        className
      )}
      {...props}
    />
  );
}
