"use client";

import React from "react";

export interface RequirementOverlayProps {
  requirementId: string;
  category: string;
  startLine: number;
  endLine: number;
  confidence: number;
}

const categoryStyles: Record<string, { containerClassName: string; dotClassName: string }> = {
  business: {
    containerClassName: "border-l-[3px] border-l-[#3b82f6] bg-[rgba(59,130,246,0.1)]",
    dotClassName: "bg-[#3b82f6]",
  },
  security: {
    containerClassName: "border-l-[3px] border-l-[#ef4444] bg-[rgba(239,68,68,0.1)]",
    dotClassName: "bg-[#ef4444]",
  },
  data: {
    containerClassName: "border-l-[3px] border-l-[#22c55e] bg-[rgba(34,197,94,0.1)]",
    dotClassName: "bg-[#22c55e]",
  },
  compliance: {
    containerClassName: "border-l-[3px] border-l-[#a855f7] bg-[rgba(168,85,247,0.1)]",
    dotClassName: "bg-[#a855f7]",
  },
  performance: {
    containerClassName: "border-l-[3px] border-l-[#eab308] bg-[rgba(234,179,8,0.1)]",
    dotClassName: "bg-[#eab308]",
  },
};

export function RequirementOverlay({
  requirementId,
  category,
  startLine,
  endLine,
  confidence,
}: RequirementOverlayProps) {
  const styles = categoryStyles[category] || categoryStyles.business;

  return (
    <div
      data-testid="requirement-overlay"
      data-requirement-id={requirementId}
      data-category={category}
      className={`my-0.5 flex items-center gap-2 rounded px-2 py-1 text-xs ${styles.containerClassName}`}
    >
      <span className={`h-2 w-2 shrink-0 rounded-full ${styles.dotClassName}`} />
      <span className="text-[var(--color-text-secondary,#94a3b8)]">
        {requirementId} (L{startLine}-{endLine})
      </span>
      <span className="ml-auto text-[var(--color-text-muted,#64748b)]">
        {Math.round(confidence * 100)}%
      </span>
    </div>
  );
}
