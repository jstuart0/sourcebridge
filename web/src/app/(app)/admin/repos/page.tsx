"use client";

import { useMutation, useQuery } from "urql";

import { Button } from "@/components/ui/button";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { StatusBadge } from "@/components/admin/StatusBadge";
import {
  REPOSITORIES_LIGHT_QUERY as REPOSITORIES_QUERY,
  REINDEX_REPOSITORY_MUTATION,
} from "@/lib/graphql/queries";

export default function AdminReposPage() {
  const [reposResult] = useQuery({ query: REPOSITORIES_QUERY });
  const [, reindex] = useMutation(REINDEX_REPOSITORY_MUTATION);
  const repos = reposResult.data?.repositories || [];

  return (
    <PageFrame>
      <PageHeader
        eyebrow="Admin"
        title="Repository management"
        description="Reindex repositories and inspect ingestion status."
      />

      <Panel>
        <h3 className="mb-4 text-base font-semibold text-[var(--text-primary)]">
          Repositories ({repos.length})
        </h3>
        {repos.length === 0 ? (
          <p className="text-sm text-[var(--text-secondary)]">No repositories indexed.</p>
        ) : (
          <div>
            {repos.map(
              (repo: { id: string; name: string; status: string; fileCount: number }) => (
                <div
                  key={repo.id}
                  className="flex items-center justify-between border-b border-[var(--border-default)] py-2 text-sm last:border-b-0"
                >
                  <div>
                    <span className="font-medium text-[var(--text-primary)]">{repo.name}</span>
                    <span className="ml-3 text-[var(--text-secondary)]">{repo.fileCount} files</span>
                  </div>
                  <div className="flex items-center gap-2">
                    <StatusBadge
                      status={repo.status === "READY" ? "healthy" : repo.status.toLowerCase()}
                    />
                    <Button
                      size="sm"
                      variant="secondary"
                      onClick={() => reindex({ id: repo.id })}
                    >
                      Reindex
                    </Button>
                  </div>
                </div>
              )
            )}
          </div>
        )}
      </Panel>
    </PageFrame>
  );
}
