// Compatibility adapter: preserved as a public TS surface per the no-removal rule.
// New code should compose @/components/ui/empty-state directly.

"use client";

import React from "react";
import { FileText } from "lucide-react";
import { EmptyState } from "./EmptyState";

export interface NoRequirementsProps {
  onImport?: () => void;
}

export function NoRequirements({ onImport }: NoRequirementsProps) {
  return (
    <EmptyState
      icon={<FileText size={48} />}
      title="No requirements found"
      description="Import requirements from markdown or CSV files to start tracing them to code."
      action={onImport ? { label: "Import Requirements", onClick: onImport } : undefined}
    />
  );
}
