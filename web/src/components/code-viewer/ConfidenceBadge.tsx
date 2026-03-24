"use client";

import React from "react";
import { CheckCircle, AlertCircle, HelpCircle } from "lucide-react";
import { cn } from "@/lib/utils";

export type ConfidenceLevel = "high" | "medium" | "low" | "verified";

export interface ConfidenceBadgeProps {
  level: ConfidenceLevel;
  score?: number;
  className?: string;
}

const config: Record<ConfidenceLevel, { icon: React.ElementType; className: string; label: string }> = {
  verified: {
    icon: CheckCircle,
    className: "border-[var(--color-success,#22c55e)] text-[var(--color-success,#22c55e)]",
    label: "Verified",
  },
  high: {
    icon: CheckCircle,
    className: "border-[var(--color-accent,#3b82f6)] text-[var(--color-accent,#3b82f6)]",
    label: "High",
  },
  medium: {
    icon: AlertCircle,
    className: "border-[var(--color-warning,#eab308)] text-[var(--color-warning,#eab308)]",
    label: "Medium",
  },
  low: {
    icon: HelpCircle,
    className: "border-[var(--color-muted,#94a3b8)] text-[var(--color-muted,#94a3b8)]",
    label: "Low",
  },
};

export function ConfidenceBadge({ level, score, className = "" }: ConfidenceBadgeProps) {
  const { icon: Icon, className: toneClassName, label } = config[level];

  return (
    <span
      data-testid="confidence-badge"
      data-level={level}
      className={cn(
        "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs",
        toneClassName,
        className
      )}
    >
      <Icon size={12} />
      <span>{label}</span>
      {score !== undefined && <span>({Math.round(score * 100)}%)</span>}
    </span>
  );
}
