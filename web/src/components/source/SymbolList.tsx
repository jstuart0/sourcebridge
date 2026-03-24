"use client";

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

export function SymbolList({
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
  if (symbols.length === 0) {
    return <p className="text-sm text-[var(--text-secondary)]">No symbols found.</p>;
  }

  // Group by file for visual separation
  let lastFile = "";

  return (
    <div className="space-y-0.5">
      {symbols.map((sym) => {
        const showFileHeader = sym.filePath !== lastFile;
        lastFile = sym.filePath;
        return (
          <div key={sym.id}>
            {showFileHeader && (
              <div className="mt-3 first:mt-0 border-b border-[var(--border-subtle)] px-3 pb-1.5 pt-1 text-[11px] font-medium uppercase tracking-[0.12em] text-[var(--text-tertiary)]">
                {sym.filePath}
              </div>
            )}
            <button
              type="button"
              onClick={() => onSelect(sym)}
              className={cn(
                "flex w-full items-center gap-2.5 rounded-[var(--control-radius)] px-3 py-2 text-left text-sm transition-colors",
                selectedId === sym.id
                  ? "bg-[var(--bg-active)] text-[var(--text-primary)]"
                  : "text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
              )}
            >
              <span className={kindBadgeClass(sym.kind)}>{kindLabel(sym.kind)}</span>
              <span className="min-w-0 truncate font-mono font-medium text-[var(--text-primary)]">{sym.name}</span>
              {cachedScopePaths?.has(`${sym.filePath}#${sym.name}`) ? (
                <span
                  className="h-2.5 w-2.5 shrink-0 rounded-full bg-[var(--accent-primary)]"
                  aria-label="Cached field guide available"
                  title="Cached field guide available"
                />
              ) : null}
              <span className="ml-auto shrink-0 text-xs text-[var(--text-tertiary)]">:{sym.startLine}</span>
            </button>
          </div>
        );
      })}
    </div>
  );
}
