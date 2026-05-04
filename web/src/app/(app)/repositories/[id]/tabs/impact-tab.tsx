"use client";

import { useQuery } from "urql";
import { LATEST_IMPACT_REPORT_QUERY } from "@/lib/graphql/queries";
import { ImpactReportPanel } from "@/components/impact-report";
import { ChangeSimulationPanel } from "@/components/change-simulation";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface ImpactTabProps {
  repoId: string;
  /** True when this tab is the currently visible tab. Gates the initial query. */
  active?: boolean;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function ImpactTab({ repoId, active = true }: ImpactTabProps) {
  const [impactResult] = useQuery({
    query: LATEST_IMPACT_REPORT_QUERY,
    variables: { repositoryId: repoId },
    pause: !active,
  });

  return (
    <div className="space-y-6">
      <ChangeSimulationPanel repositoryId={repoId} />
      <ImpactReportPanel report={impactResult.data?.latestImpactReport} repositoryId={repoId} />
    </div>
  );
}
