"use client";

import { cva, type VariantProps } from "class-variance-authority";
import type { ButtonHTMLAttributes } from "react";
import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 rounded-[var(--control-radius)] border text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent-focus)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--bg-base)] disabled:pointer-events-none disabled:opacity-60",
  {
    variants: {
      variant: {
        primary:
          "border-transparent bg-[var(--accent-primary)] px-4 py-2.5 text-[var(--accent-contrast)] hover:bg-[var(--accent-primary-strong)]",
        secondary:
          "border-[var(--border-default)] bg-[var(--bg-surface)] px-4 py-2.5 text-[var(--text-primary)] hover:bg-[var(--bg-hover)]",
        ghost:
          "border-transparent bg-transparent px-3 py-2 text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]",
      },
      size: {
        sm: "h-10 px-3 text-xs",
        md: "h-11 px-4",
        lg: "h-12 px-5",
      },
    },
    defaultVariants: {
      variant: "primary",
      size: "md",
    },
  }
);

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> &
  VariantProps<typeof buttonVariants>;

export function Button({ className, variant, size, ...props }: ButtonProps) {
  return <button className={cn(buttonVariants({ variant, size }), className)} {...props} />;
}
