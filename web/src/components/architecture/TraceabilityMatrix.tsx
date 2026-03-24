"use client";

import React, { useMemo } from "react";
import { cn } from "@/lib/utils";

export interface MatrixLink {
  requirementId: string;
  symbolId: string;
  confidence: string;
  verified: boolean;
}

export interface MatrixRequirement {
  id: string;
  externalId: string;
  title: string;
}

export interface MatrixSymbol {
  id: string;
  name: string;
  filePath: string;
  kind: string;
}

export interface TraceabilityMatrixProps {
  requirements: MatrixRequirement[];
  symbols: MatrixSymbol[];
  links: MatrixLink[];
  coverage: number;
  onCellClick?: (requirementId: string, symbolId: string) => void;
}

const confidenceClasses: Record<string, string> = {
  VERIFIED: "bg-[var(--confidence-high,#22c55e)] ring-2 ring-[var(--confidence-high,#22c55e)]",
  HIGH: "bg-[var(--accent-primary,#3b82f6)]",
  MEDIUM: "bg-[var(--confidence-medium,#eab308)]",
  LOW: "bg-[var(--confidence-low,#94a3b8)]",
};

export function TraceabilityMatrix({
  requirements,
  symbols,
  links,
  coverage,
  onCellClick,
}: TraceabilityMatrixProps) {
  const linkMap = useMemo(() => {
    const map = new Map<string, MatrixLink>();
    for (const link of links) {
      map.set(`${link.requirementId}:${link.symbolId}`, link);
    }
    return map;
  }, [links]);

  const displaySymbols = symbols.slice(0, 20);
  const displayReqs = requirements.slice(0, 30);

  return (
    <div data-testid="traceability-matrix">
      <div className="mb-4 flex items-center justify-between">
        <h3 className="text-base font-semibold text-[var(--text-primary)]">Traceability Matrix</h3>
        <span className="text-sm text-[var(--text-secondary)]">
          Coverage: {Math.round(coverage * 100)}%
        </span>
      </div>

      <div className="overflow-x-auto">
        <table className="w-full border-collapse text-xs">
          <thead>
            <tr>
              <th className="sticky left-0 z-[1] min-w-[120px] border-b border-[var(--border-default)] bg-[var(--bg-surface)] px-2 py-2 text-left font-medium text-[var(--text-primary)]">
                Requirement
              </th>
              {displaySymbols.map((sym) => (
                <th
                  key={sym.id}
                  className="max-w-[30px] overflow-hidden border-b border-[var(--border-default)] px-1 py-1 text-ellipsis whitespace-nowrap text-[var(--text-secondary)] [text-orientation:mixed] [writing-mode:vertical-rl]"
                  title={`${sym.name} (${sym.filePath})`}
                >
                  {sym.name}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {displayReqs.map((req) => (
              <tr key={req.id}>
                <td className="sticky left-0 z-[1] max-w-[160px] overflow-hidden border-b border-[var(--border-default)] bg-[var(--bg-surface)] px-2 py-1.5 text-ellipsis whitespace-nowrap text-[var(--text-primary)]" title={req.title}>
                  {req.externalId}
                </td>
                {displaySymbols.map((sym) => {
                  const link = linkMap.get(`${req.id}:${sym.id}`);
                  return (
                    <td
                      key={sym.id}
                      data-testid={`matrix-cell-${req.id}-${sym.id}`}
                      onClick={() => onCellClick?.(req.id, sym.id)}
                      className={cn(
                        "border-b border-[var(--border-default)] px-1 py-1 text-center",
                        onCellClick ? "cursor-pointer" : "cursor-default"
                      )}
                    >
                      {link && (
                        <div
                          className={cn(
                            "mx-auto h-3 w-3 rounded-full",
                            confidenceClasses[link.confidence] || confidenceClasses.LOW,
                            !link.verified && "ring-0"
                          )}
                          title={`${link.confidence}${link.verified ? " (verified)" : ""}`}
                        />
                      )}
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {(requirements.length > 30 || symbols.length > 20) && (
        <p className="mt-2 text-xs text-[var(--text-tertiary)]">
          Showing {displayReqs.length} of {requirements.length} requirements and {displaySymbols.length} of{" "}
          {symbols.length} symbols.
        </p>
      )}
    </div>
  );
}
