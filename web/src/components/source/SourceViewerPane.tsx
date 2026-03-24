"use client";

import dynamic from "next/dynamic";
import { useEffect, useMemo, useState } from "react";
import { Copy, Search, Waypoints } from "lucide-react";
import { useQuery } from "urql";
import { SOURCE_FILE_QUERY } from "@/lib/graphql/queries";
import type { SourceTarget } from "@/lib/source-target";
import { TOKEN_KEY } from "@/lib/token-key";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Panel } from "@/components/ui/panel";

const CodeViewer = dynamic(
  () => import("@/components/code-viewer/CodeViewer").then((m) => m.CodeViewer),
  { ssr: false }
);

interface SourceFilePayload {
  repositoryId: string;
  filePath: string;
  language: string;
  lineCount: number;
  content: string;
  contentHash?: string | null;
}

interface SourceFileResult {
  ok: boolean;
  errorCode?: string | null;
  message?: string | null;
  file?: SourceFilePayload | null;
}

interface EnterpriseAnnotation {
  id: string;
  line_start: number;
  line_end: number;
  kind: string;
}

async function loadEnterpriseAnnotations(repositoryId: string, filePath: string) {
  const token = typeof window !== "undefined" ? localStorage.getItem(TOKEN_KEY) : null;
  const params = new URLSearchParams({ repository_id: repositoryId, file_path: filePath });
  const res = await fetch(`/api/v1/enterprise/source/default/annotations?${params.toString()}`, {
    credentials: "include",
    headers: token ? { Authorization: `Bearer ${token}` } : undefined,
  });
  if (!res.ok) {
    return [];
  }
  const data = await res.json();
  return (data.annotations ?? []) as EnterpriseAnnotation[];
}

export function SourceViewerPane({
  repositoryId,
  target,
}: {
  repositoryId: string;
  target: SourceTarget | null;
}) {
  const [lineFilter, setLineFilter] = useState("");
  const [enterpriseAnnotations, setEnterpriseAnnotations] = useState<EnterpriseAnnotation[]>([]);
  const [{ data, fetching }] = useQuery({
    query: SOURCE_FILE_QUERY,
    variables: { repositoryId, filePath: target?.filePath ?? "" },
    pause: !target?.filePath,
  });

  const result: SourceFileResult | undefined = data?.sourceFile;
  const file = result?.file ?? null;

  const activeLine = useMemo(() => {
    if (lineFilter.trim()) {
      const parsed = Number.parseInt(lineFilter, 10);
      if (Number.isFinite(parsed) && parsed > 0) {
        return parsed;
      }
    }
    return target?.line;
  }, [lineFilter, target?.line]);

  useEffect(() => {
    if (process.env.NEXT_PUBLIC_EDITION !== "enterprise" || !target?.filePath) {
      setEnterpriseAnnotations([]);
      return;
    }
    loadEnterpriseAnnotations(repositoryId, target.filePath)
      .then(setEnterpriseAnnotations)
      .catch(() => setEnterpriseAnnotations([]));
  }, [repositoryId, target?.filePath]);

  if (!target) {
    return (
      <EmptyState
        eyebrow="Source Viewer"
        title="Open a file in context"
        description="Choose a file from the repository tree, a symbol, a search result, or a requirement link to open source here."
      />
    );
  }

  if (fetching) {
    return (
      <Panel variant="elevated" className="min-h-[32rem]">
        <p className="text-sm text-[var(--text-secondary)]">Loading source…</p>
      </Panel>
    );
  }

  if (!result?.ok || !file) {
    return (
      <EmptyState
        eyebrow="Source Viewer"
        title={result?.errorCode === "SOURCE_UNAVAILABLE" ? "Source unavailable" : "Unable to open file"}
        description={
          result?.message ??
          "This file could not be opened from the current repository source."
        }
      />
    );
  }

  return (
    <Panel variant="elevated" padding="none" className="overflow-hidden max-w-full">
      <div className="flex flex-col gap-3 border-b border-[var(--border-subtle)] px-3 py-3 sm:px-5 sm:py-4 lg:flex-row lg:items-center lg:justify-between">
        <div className="space-y-1">
          <p className="text-xs font-semibold uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
            Source Viewer
          </p>
          <h3 className="font-mono text-sm text-[var(--text-primary)]">{file.filePath}</h3>
          <p className="text-xs text-[var(--text-secondary)]">
            {file.language} · {file.lineCount} lines
            {target.line ? ` · line ${target.line}` : ""}
            {target.endLine && target.endLine !== target.line ? `-${target.endLine}` : ""}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <label className="flex items-center gap-2 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-sm text-[var(--text-secondary)]">
            <Search className="h-4 w-4" />
            <input
              value={lineFilter}
              onChange={(e) => setLineFilter(e.target.value)}
              placeholder="Go to line"
              className="w-24 bg-transparent text-[var(--text-primary)] outline-none"
              inputMode="numeric"
            />
          </label>
          <Button
            type="button"
            variant="secondary"
            size="sm"
            onClick={async () => {
              await navigator.clipboard.writeText(file.filePath + (target.line ? `:${target.line}` : ""));
            }}
          >
            <Copy className="h-4 w-4" />
            Copy Ref
          </Button>
          {target.line ? (
            <div className="inline-flex items-center gap-2 rounded-[var(--control-radius)] border border-[var(--border-default)] px-3 py-2 text-xs text-[var(--text-secondary)]">
              <Waypoints className="h-3.5 w-3.5" />
              Focused range
            </div>
          ) : null}
        </div>
      </div>
      <div className="min-h-[20rem] overflow-x-auto bg-[var(--bg-base)] p-1 sm:min-h-[32rem] sm:p-2">
        <CodeViewer
          code={file.content}
          language={file.language}
          overlays={enterpriseAnnotations.map((annotation) => ({
            startLine: annotation.line_start,
            endLine: annotation.line_end || annotation.line_start,
            requirementId: annotation.id,
            category: annotation.kind,
            confidence: 1,
          }))}
          focusLine={activeLine}
          focusEndLine={target.endLine}
        />
      </div>
    </Panel>
  );
}
