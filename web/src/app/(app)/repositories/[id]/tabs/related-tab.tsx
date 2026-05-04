"use client";

import { RelatedReposPanel } from "@/components/federation/RelatedReposPanel";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface RelatedTabProps {
  repoId: string;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function RelatedTab({ repoId }: RelatedTabProps) {
  return <RelatedReposPanel repositoryId={repoId} />;
}
