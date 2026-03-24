"use client";

import { cva, type VariantProps } from "class-variance-authority";
import type { HTMLAttributes } from "react";
import { cn } from "@/lib/utils";

const panelVariants = cva(
  "rounded-[var(--panel-radius)] border border-[var(--panel-border)] bg-[var(--panel-bg)] text-[var(--text-primary)] shadow-[var(--panel-shadow)] backdrop-blur-[var(--panel-blur)] transition-colors",
  {
    variants: {
      variant: {
        surface: "",
        elevated:
          "bg-[var(--panel-bg-elevated)] border-[var(--border-strong)] shadow-[var(--panel-shadow-strong)]",
        glass: "bg-[var(--panel-bg-glass)] border-[var(--panel-border-strong)]",
        accent:
          "bg-[var(--panel-bg-accent)] border-[color:var(--accent-quiet)] shadow-[var(--panel-shadow-soft)]",
      },
      padding: {
        none: "",
        sm: "p-3 sm:p-4",
        md: "p-3.5 sm:p-5",
        lg: "p-4 sm:p-7",
      },
    },
    defaultVariants: {
      variant: "surface",
      padding: "md",
    },
  }
);

type PanelProps = HTMLAttributes<HTMLDivElement> &
  VariantProps<typeof panelVariants>;

export function Panel({ className, variant, padding, ...props }: PanelProps) {
  return <div className={cn(panelVariants({ variant, padding }), className)} {...props} />;
}
