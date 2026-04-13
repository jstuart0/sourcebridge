import Image from "next/image";
import { cn } from "@/lib/utils";

interface BrandProps {
  size?: "sm" | "md" | "lg" | "xl";
  showTagline?: boolean;
  className?: string;
}

const sizeClasses = {
  sm: "text-sm",
  md: "text-base",
  lg: "text-2xl",
  xl: "text-5xl",
};

const taglineSizes = {
  sm: "text-[9px]",
  md: "text-[11px]",
  lg: "text-xs",
  xl: "text-base",
};

const iconSizes = {
  sm: 24,
  md: 28,
  lg: 40,
  xl: 56,
};

export function Brand({ size = "md", showTagline = false, className }: BrandProps) {
  const iconPx = iconSizes[size];
  return (
    <span className={cn("inline-flex items-center gap-2", className)}>
      <Image src="/logo.png" alt="" width={iconPx} height={iconPx} className="rounded-lg" />
      <span className="inline-flex flex-col">
      <span className={cn("font-semibold tracking-[0.04em] text-[var(--text-primary)]", sizeClasses[size])}>
        SourceBridge<em className="not-italic font-light text-[var(--accent-primary)]">.ai</em>
      </span>
      {showTagline && (
        <span
          className={cn(
            "uppercase tracking-[0.16em] text-[var(--text-tertiary)]",
            taglineSizes[size]
          )}
        >
          understand any codebase, fast
        </span>
      )}
      </span>
    </span>
  );
}

export function BrandEnterprise({ size = "md", className }: Omit<BrandProps, "showTagline">) {
  const iconPx = iconSizes[size];
  return (
    <span className={cn("inline-flex items-center gap-2", className)}>
      <Image src="/logo.png" alt="" width={iconPx} height={iconPx} className="rounded-lg" />
      <span className="inline-flex flex-col">
      <span className={cn("font-semibold tracking-[0.04em] text-[var(--accent-primary)]", sizeClasses[size])}>
        SourceBridge<em className="not-italic font-light">.ai</em>
        <span className="ml-1.5 font-normal text-[var(--text-secondary)]">Enterprise</span>
      </span>
      <span
        className={cn(
          "uppercase tracking-[0.16em] text-[var(--text-tertiary)]",
          taglineSizes[size]
        )}
      >
        Control Workspace
      </span>
      </span>
    </span>
  );
}
