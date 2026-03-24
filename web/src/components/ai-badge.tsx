"use client";

import { cn } from "@/lib/utils";

function aiColor(score: number): string {
  if (score >= 0.7) return "text-violet-500";
  if (score >= 0.4) return "text-amber-500";
  return "text-slate-400";
}

function aiBorder(score: number): string {
  if (score >= 0.7) return "border-violet-500/40";
  if (score >= 0.4) return "border-amber-500/40";
  return "border-slate-400/40";
}

/** Small inline badge indicating AI-generated confidence. */
export function AIBadge({ score, className }: { score: number; className?: string }) {
  if (score < 0.3) return null;
  const pct = Math.round(score * 100);
  return (
    <span
      className={cn(
        "inline-flex items-center gap-0.5 rounded-full border px-1.5 py-0 text-[10px] font-semibold tabular-nums leading-4",
        aiColor(score),
        aiBorder(score),
        className,
      )}
      title={`AI-generated confidence: ${pct}%`}
    >
      AI {pct}%
    </span>
  );
}

/** Tooltip-style list of which heuristic signals fired. */
export function AISignalList({ signals }: { signals: string[] }) {
  if (!signals.length) return null;
  return (
    <div className="flex flex-wrap gap-1">
      {signals.map((s) => (
        <span
          key={s}
          className="rounded bg-violet-500/10 px-1.5 py-0.5 text-[10px] text-violet-400"
        >
          {s.replace(/_/g, " ")}
        </span>
      ))}
    </div>
  );
}
