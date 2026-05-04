// Compatibility adapter: preserved as a public TS surface per the no-removal rule.
// New code should compose @/components/ui/empty-state directly.

"use client";

import React from "react";
import { FolderGit2 } from "lucide-react";
import { EmptyState } from "./EmptyState";

export interface NoRepositoriesProps {
  onImport?: () => void;
}

export function NoRepositories({ onImport }: NoRepositoriesProps) {
  return (
    <EmptyState
      icon={<FolderGit2 size={48} />}
      title="No repositories indexed"
      description="Import a repository to start analyzing code and tracing requirements."
      action={onImport ? { label: "Import Repository", onClick: onImport } : undefined}
    />
  );
}
