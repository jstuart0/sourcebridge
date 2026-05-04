"use client";

import { ArchitectureDiagram } from "@/components/architecture/ArchitectureDiagram";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type Tab = "files" | "symbols" | "requirements" | "specs" | "analysis" | "impact" | "architecture" | "related" | "knowledge" | "subsystems" | "settings";

interface ArchitectureTabProps {
  repoId: string;
  setActiveTab: (tab: Tab) => void;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function ArchitectureTab({ repoId, setActiveTab }: ArchitectureTabProps) {
  return (
    <ArchitectureDiagram
      repositoryId={repoId}
      onModuleClick={(_path) => {
        setActiveTab("files");
      }}
    />
  );
}
