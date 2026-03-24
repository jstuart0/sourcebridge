"use client";

import { useMemo, useState } from "react";
import { ChevronDown, ChevronRight, FileCode2, FolderOpen } from "lucide-react";
import { cn } from "@/lib/utils";
import { AIBadge } from "@/components/ai-badge";

interface FileNode {
  id: string;
  path: string;
  language: string;
  lineCount: number;
  aiScore?: number;
  aiSignals?: string[];
}

interface TreeFolder {
  name: string;
  path: string;
  folders: Map<string, TreeFolder>;
  files: FileNode[];
}

function createFolder(name: string, path: string): TreeFolder {
  return { name, path, folders: new Map(), files: [] };
}

function buildTree(files: FileNode[]) {
  const root = createFolder("", "");
  for (const file of files) {
    const parts = file.path.split("/").filter(Boolean);
    let current = root;
    for (let i = 0; i < parts.length - 1; i++) {
      const name = parts[i];
      const nextPath = current.path ? `${current.path}/${name}` : name;
      if (!current.folders.has(name)) {
        current.folders.set(name, createFolder(name, nextPath));
      }
      current = current.folders.get(name)!;
    }
    current.files.push(file);
  }
  return root;
}

function FolderNode({
  folder,
  selectedPath,
  onSelect,
  depth: _depth,
}: {
  folder: TreeFolder;
  selectedPath?: string;
  onSelect: (file: FileNode) => void;
  depth: number;
}) {
  const [open, setOpen] = useState(_depth < 1);
  const folders = Array.from(folder.folders.values()).sort((a, b) => a.path.localeCompare(b.path));
  const files = [...folder.files].sort((a, b) => a.path.localeCompare(b.path));

  return (
    <div className="space-y-1">
      {folder.name ? (
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className="flex w-full items-center gap-2 rounded-[var(--control-radius)] px-2 py-1.5 text-left text-sm text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
        >
          {open ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
          <FolderOpen className="h-4 w-4" />
          <span className="truncate">{folder.name}</span>
        </button>
      ) : null}

      {open || !folder.name ? (
        <div className={cn("space-y-1", folder.name ? "ml-4 border-l border-[var(--border-subtle)] pl-2" : "")}>
          {folders.map((child) => (
            <FolderNode
              key={child.path}
              folder={child}
              selectedPath={selectedPath}
              onSelect={onSelect}
              depth={_depth + 1}
            />
          ))}
          {files.map((file) => (
            <button
              key={file.id}
              type="button"
              onClick={() => onSelect(file)}
              className={cn(
                "flex w-full items-center gap-2 rounded-[var(--control-radius)] px-2 py-1.5 text-left text-sm transition-colors",
                selectedPath === file.path
                  ? "bg-[var(--nav-item-bg-active)] text-[var(--text-primary)]"
                  : "text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
              )}
            >
              <FileCode2 className="h-4 w-4 shrink-0" />
              <span className="truncate font-mono">{file.path.split("/").pop()}</span>
              {file.aiScore != null && <AIBadge score={file.aiScore} />}
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}

export function FileTree({
  files,
  selectedPath,
  onSelect,
}: {
  files: FileNode[];
  selectedPath?: string;
  onSelect: (file: FileNode) => void;
}) {
  const tree = useMemo(() => buildTree(files), [files]);

  return (
    <div className="space-y-1">
      <FolderNode folder={tree} selectedPath={selectedPath} onSelect={onSelect} depth={0} />
    </div>
  );
}
