"use client";

import { useMemo } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { Panel } from "@/components/ui/panel";
import { FileTree } from "@/components/source/FileTree";
import { EnterpriseSourcePanel } from "@/components/source/EnterpriseSourcePanel";
import { SourceViewerPane } from "@/components/source/SourceViewerPane";
import {
  sourceTargetFromSearchParams,
  type SourceTarget,
} from "@/lib/source-target";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface FileNode {
  id: string;
  path: string;
  language: string;
  lineCount: number;
  aiScore?: number;
  aiSignals?: string[];
}

interface FilesTabProps {
  repoId: string;
  files: FileNode[];
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function FilesTab({ repoId, files }: FilesTabProps) {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();

  const sourceTarget = useMemo(
    () => sourceTargetFromSearchParams(new URLSearchParams(searchParams.toString())),
    [searchParams]
  );

  const selectedFilePath = sourceTarget?.filePath;

  function updateSearchParams(mutator: (params: URLSearchParams) => void) {
    const next = new URLSearchParams(searchParams.toString());
    mutator(next);
    router.replace(`${pathname}?${next.toString()}`, { scroll: false });
  }

  function openSource(target: SourceTarget) {
    updateSearchParams((next) => {
      next.set("tab", target.tab ?? "files");
      next.set("file", target.filePath);
      if (typeof target.line === "number" && target.line > 0) {
        next.set("line", String(target.line));
      } else {
        next.delete("line");
      }
      if (typeof target.endLine === "number" && target.endLine > 0) {
        next.set("endLine", String(target.endLine));
      } else {
        next.delete("endLine");
      }
    });
  }

  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(18rem,24rem)_minmax(0,1fr)]">
      <Panel className="min-h-[32rem]">
        <div className="mb-4 flex items-center justify-between gap-4">
          <div>
            <h3 className="text-lg font-semibold text-[var(--text-primary)]">
              Files ({files.length})
            </h3>
            <p className="mt-1 text-sm text-[var(--text-secondary)]">
              Browse directories and open source in the shared viewer.
            </p>
          </div>
        </div>
        {files.length === 0 ? (
          <p className="text-sm text-[var(--text-secondary)]">No files indexed yet.</p>
        ) : (
          <div className="max-h-[42rem] overflow-y-auto">
            <FileTree
              files={files}
              selectedPath={selectedFilePath}
              onSelect={(file) => openSource({ filePath: file.path, tab: "files" })}
            />
          </div>
        )}
      </Panel>
      <div className="space-y-4">
        <SourceViewerPane repositoryId={repoId} target={sourceTarget} />
        <EnterpriseSourcePanel repositoryId={repoId} target={sourceTarget} />
      </div>
    </div>
  );
}
