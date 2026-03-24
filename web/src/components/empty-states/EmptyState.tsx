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
