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

export function Brand({ size = "md", showTagline = false, className }: BrandProps) {
  return (
    <span className={cn("inline-flex flex-col", className)}>
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
  );
}

export function BrandEnterprise({ size = "md", className }: Omit<BrandProps, "showTagline">) {
  return (
    <span className={cn("inline-flex flex-col", className)}>
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
  );
}
