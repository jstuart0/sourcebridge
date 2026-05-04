"use client";

import { useState } from "react";
import { Panel } from "@/components/ui/panel";
import { ClusterTable } from "@/components/subsystems/ClusterTable";
import { ImproveLabelsButton } from "@/components/subsystems/ImproveLabelsButton";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface SubsystemsTabProps {
  repoId: string;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function SubsystemsTab({ repoId }: SubsystemsTabProps) {
  const [refreshKey, setRefreshKey] = useState(0);

  return (
    <Panel className="space-y-4">
      <div className="flex items-center justify-between gap-4">
        <div>
          <h3 className="text-lg font-semibold text-[var(--text-primary)]">Subsystems</h3>
          <p className="mt-0.5 text-sm text-[var(--text-secondary)]">
            Subsystems are groups of related symbols based on how they call each other. Use them to navigate the codebase, understand boundaries, and onboard faster.
          </p>
        </div>
        <ImproveLabelsButton
          repoId={repoId}
          onComplete={() => setRefreshKey((k) => k + 1)}
        />
      </div>
      <ClusterTable repoId={repoId} refreshKey={refreshKey} />
    </Panel>
  );
}
