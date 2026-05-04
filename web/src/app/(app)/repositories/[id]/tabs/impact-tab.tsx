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
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function ImpactTab({ repoId }: ImpactTabProps) {
  const [impactResult] = useQuery({
    query: LATEST_IMPACT_REPORT_QUERY,
    variables: { repositoryId: repoId },
  });

  return (
    <div className="space-y-6">
      <ChangeSimulationPanel repositoryId={repoId} />
      <ImpactReportPanel report={impactResult.data?.latestImpactReport} repositoryId={repoId} />
    </div>
  );
}
