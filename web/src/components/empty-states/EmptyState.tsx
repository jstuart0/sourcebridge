// Compatibility adapter: this file is the public TS surface for the legacy
// empty-states prop shape (icon + action: {label, onClick}). It is intentionally
// preserved under web/src/components/empty-states/ per the no-removal rule.
//
// New code should import from @/components/ui/empty-state directly, which uses the
// canonical design-system shape (eyebrow + actions: ReactNode).
//
// This file keeps its own implementation because the two prop shapes are intentionally
// different; bridging them would silently drop icon/action semantics.

"use client";

import React from "react";
import { Button } from "@/components/ui/button";

export interface EmptyStateProps {
  icon?: React.ReactNode;
  title: string;
  description: string;
  action?: {
    label: string;
    onClick: () => void;
  };
}

export function EmptyState({ icon, title, description, action }: EmptyStateProps) {
  return (
    <div data-testid="empty-state" className="flex min-h-[200px] flex-col items-center justify-center px-6 py-12 text-center">
      {icon && (
        <div className="mb-4 text-[var(--color-text-muted,#64748b)] opacity-60">{icon}</div>
      )}
      <h3 className="mb-2 text-lg font-semibold text-[var(--color-text-primary,#e2e8f0)]">{title}</h3>
      <p className="mb-6 max-w-[400px] text-sm text-[var(--color-text-secondary,#94a3b8)]">
        {description}
      </p>
      {action && (
        <Button
          data-testid="empty-state-action"
          onClick={action.onClick}
        >
          {action.label}
        </Button>
      )}
    </div>
  );
}
