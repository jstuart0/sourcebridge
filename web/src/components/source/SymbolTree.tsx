"use client";

import { useMemo, useState } from "react";
import { ChevronDown, ChevronRight, FileCode2, FolderOpen } from "lucide-react";
import { cn } from "@/lib/utils";
import { kindBadgeClass, kindLabel } from "./symbol-kind";

interface SymbolNode {
  id: string;
  name: string;
  qualifiedName: string;
  kind: string;
  language: string;
  filePath: string;
  startLine: number;
  endLine: number;
  signature: string | null;
}

interface TreeFolder {
  name: string;
  path: string;
  folders: Map<string, TreeFolder>;
  files: Map<string, SymbolNode[]>;
}

function createFolder(name: string, path: string): TreeFolder {
  return { name, path, folders: new Map(), files: new Map() };
}

function buildSymbolTree(symbols: SymbolNode[]) {
  const root = createFolder("", "");
  for (const sym of symbols) {
    const parts = sym.filePath.split("/").filter(Boolean);
    let current = root;
    for (let i = 0; i < parts.length - 1; i++) {
      const name = parts[i];
      const nextPath = current.path ? `${current.path}/${name}` : name;
      if (!current.folders.has(name)) {
        current.folders.set(name, createFolder(name, nextPath));
      }
      current = current.folders.get(name)!;
    }
    const fileName = parts[parts.length - 1];
    if (!current.files.has(fileName)) {
      current.files.set(fileName, []);
    }
    current.files.get(fileName)!.push(sym);
  }
  return root;
}

function SymbolFolderNode({
  folder,
  selectedId,
  onSelect,
  depth,
  cachedScopePaths,
}: {
  folder: TreeFolder;
  selectedId: string | null;
  onSelect: (sym: SymbolNode) => void;
  depth: number;
  cachedScopePaths?: Set<string>;
}) {
  const [open, setOpen] = useState(depth < 1);
  const folders = Array.from(folder.folders.values()).sort((a, b) => a.path.localeCompare(b.path));
  const files = Array.from(folder.files.entries()).sort(([a], [b]) => a.localeCompare(b));

  return (
    <div className="space-y-0.5">
      {folder.name ? (
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className="flex w-full items-center gap-2 rounded-[var(--control-radius)] px-2 py-1.5 text-left text-sm text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
        >
          {open ? <ChevronDown className="h-4 w-4 shrink-0" /> : <ChevronRight className="h-4 w-4 shrink-0" />}
          <FolderOpen className="h-4 w-4 shrink-0" />
          <span className="truncate">{folder.name}</span>
        </button>
      ) : null}

      {open || !folder.name ? (
        <div className={cn("space-y-0.5", folder.name ? "ml-4 border-l border-[var(--border-subtle)] pl-2" : "")}>
          {folders.map((child) => (
            <SymbolFolderNode
              key={child.path}
              folder={child}
              selectedId={selectedId}
              onSelect={onSelect}
              depth={depth + 1}
              cachedScopePaths={cachedScopePaths}
            />
          ))}
          {files.map(([fileName, syms]) => (
            <SymbolFileNode
              key={fileName}
              fileName={fileName}
              symbols={syms}
              selectedId={selectedId}
              onSelect={onSelect}
              cachedScopePaths={cachedScopePaths}
            />
          ))}
        </div>
      ) : null}
    </div>
  );
}

function SymbolFileNode({
  fileName,
  symbols,
  selectedId,
  onSelect,
  cachedScopePaths,
}: {
  fileName: string;
  symbols: SymbolNode[];
  selectedId: string | null;
  onSelect: (sym: SymbolNode) => void;
  cachedScopePaths?: Set<string>;
}) {
  const [open, setOpen] = useState(false);
  const sorted = useMemo(() => [...symbols].sort((a, b) => a.name.localeCompare(b.name)), [symbols]);

  return (
    <div className="space-y-0.5">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 rounded-[var(--control-radius)] px-2 py-1.5 text-left text-sm text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
      >
        {open ? <ChevronDown className="h-4 w-4 shrink-0" /> : <ChevronRight className="h-4 w-4 shrink-0" />}
        <FileCode2 className="h-4 w-4 shrink-0" />
        <span className="truncate font-mono">{fileName}</span>
        <span className="ml-auto text-xs text-[var(--text-tertiary)]">{symbols.length}</span>
      </button>

      {open ? (
        <div className="ml-4 space-y-0.5 border-l border-[var(--border-subtle)] pl-2">
          {sorted.map((sym) => (
            <button
              key={sym.id}
              type="button"
              onClick={() => onSelect(sym)}
              className={cn(
                "flex w-full items-center gap-2 rounded-[var(--control-radius)] px-2 py-1.5 text-left text-sm transition-colors",
                selectedId === sym.id
                  ? "bg-[var(--nav-item-bg-active)] text-[var(--text-primary)]"
                  : "text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
              )}
            >
              <span className={kindBadgeClass(sym.kind)}>{kindLabel(sym.kind)}</span>
              <span className="truncate font-mono">{sym.name}</span>
              {cachedScopePaths?.has(`${sym.filePath}#${sym.name}`) ? (
                <span
                  className="h-2.5 w-2.5 shrink-0 rounded-full bg-[var(--accent-primary)]"
                  aria-label="Cached field guide available"
                  title="Cached field guide available"
                />
              ) : null}
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}

export function SymbolTree({
  symbols,
  selectedId,
  onSelect,
  cachedScopePaths,
}: {
  symbols: SymbolNode[];
  selectedId: string | null;
  onSelect: (sym: SymbolNode) => void;
  cachedScopePaths?: Set<string>;
}) {
  const tree = useMemo(() => buildSymbolTree(symbols), [symbols]);

  if (symbols.length === 0) {
    return <p className="text-sm text-[var(--text-secondary)]">No symbols found.</p>;
  }

  return (
    <div className="space-y-0.5">
      <SymbolFolderNode
        folder={tree}
        selectedId={selectedId}
        onSelect={onSelect}
        depth={0}
        cachedScopePaths={cachedScopePaths}
      />
    </div>
  );
}
