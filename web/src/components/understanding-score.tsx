"use client";

import { useQuery } from "urql";
import { UNDERSTANDING_SCORE_QUERY } from "@/lib/graphql/queries";

interface UnderstandingScoreData {
  overall: number;
  traceabilityCoverage: number;
  documentationCoverage: number;
  reviewCoverage: number;
  testCoverage: number;
  knowledgeFreshness: number;
  aiCodeRatio: number;
}

function scoreColor(score: number): string {
  if (score >= 70) return "text-emerald-500";
  if (score >= 40) return "text-amber-500";
  return "text-rose-500";
}

function scoreBg(score: number): string {
  if (score >= 70) return "bg-emerald-500";
  if (score >= 40) return "bg-amber-500";
  return "bg-rose-500";
}

function scoreBorder(score: number): string {
  if (score >= 70) return "border-emerald-500/40";
  if (score >= 40) return "border-amber-500/40";
  return "border-rose-500/40";
}

/** Compact badge showing just the overall score number. */
export function ScoreBadge({ score }: { score: number | null | undefined }) {
  if (score == null) return null;
  const rounded = Math.round(score);
  return (
    <span
      className={`inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-semibold tabular-nums ${scoreColor(rounded)} ${scoreBorder(rounded)}`}
    >
      {rounded}
    </span>
  );
}

/** Score badge that fetches the understanding score lazily — page renders instantly. */
export function LazyScoreBadge({ repositoryId }: { repositoryId: string }) {
  const [result] = useQuery({
    query: UNDERSTANDING_SCORE_QUERY,
    variables: { repositoryId },
    pause: !repositoryId,
  });

  if (result.fetching) {
    return (
      <span className="inline-flex h-5 w-8 animate-pulse rounded-full bg-[var(--bg-active)]" />
    );
  }

  const score = result.data?.understandingScore?.overall;
  return <ScoreBadge score={score ?? null} />;
}

/** Score breakdown that fetches lazily — parent page renders instantly. */
export function LazyScoreBreakdown({ repositoryId }: { repositoryId: string }) {
  const [result] = useQuery({
    query: UNDERSTANDING_SCORE_QUERY,
    variables: { repositoryId },
    pause: !repositoryId,
  });

  if (result.fetching) {
    return (
      <div className="space-y-5">
        <div className="flex items-center gap-6">
          <div className="h-24 w-24 animate-pulse rounded-full bg-[var(--bg-active)]" />
          <div className="flex-1 space-y-2">
            <div className="h-4 w-32 animate-pulse rounded bg-[var(--bg-active)]" />
            <div className="h-3 w-48 animate-pulse rounded bg-[var(--bg-active)]" />
          </div>
        </div>
        <div className="space-y-3">
          {[1, 2, 3, 4, 5].map((i) => (
            <div key={i} className="space-y-1">
              <div className="h-3 w-24 animate-pulse rounded bg-[var(--bg-active)]" />
              <div className="h-1.5 w-full animate-pulse rounded-full bg-[var(--bg-active)]" />
            </div>
          ))}
        </div>
      </div>
    );
  }

  return <ScoreBreakdown data={result.data?.understandingScore ?? null} />;
}

const subScores: { key: keyof UnderstandingScoreData; label: string }[] = [
  { key: "traceabilityCoverage", label: "Traceability" },
  { key: "documentationCoverage", label: "Documentation" },
  { key: "reviewCoverage", label: "Review" },
  { key: "testCoverage", label: "Test" },
  { key: "knowledgeFreshness", label: "Knowledge" },
];

/** Full breakdown card with gauge and sub-score bars. */
export function ScoreBreakdown({ data }: { data: UnderstandingScoreData | null | undefined }) {
  if (!data) return null;

  const overall = Math.round(data.overall);

  return (
    <div className="space-y-5">
      {/* Circular gauge */}
      <div className="flex items-center gap-6">
        <div className="relative flex h-24 w-24 items-center justify-center">
          <svg viewBox="0 0 100 100" className="h-full w-full -rotate-90">
            <circle
              cx="50"
              cy="50"
              r="42"
              fill="none"
              stroke="var(--border-subtle)"
              strokeWidth="8"
            />
            <circle
              cx="50"
              cy="50"
              r="42"
              fill="none"
              className={scoreColor(overall).replace("text-", "stroke-")}
              strokeWidth="8"
              strokeLinecap="round"
              strokeDasharray={`${(overall / 100) * 264} 264`}
            />
          </svg>
          <span
            className={`absolute text-2xl font-bold tabular-nums ${scoreColor(overall)}`}
          >
            {overall}
          </span>
        </div>
        <div className="space-y-1">
          <p className="text-sm font-semibold text-[var(--text-primary)]">
            Understanding Score
          </p>
          <p className="text-xs text-[var(--text-secondary)]">
            Composite metric of traceability, documentation, review, test coverage, and knowledge freshness.
          </p>
        </div>
      </div>

      {/* Sub-score bars */}
      <div className="space-y-3">
        {subScores.map(({ key, label }) => {
          const val = Math.round(data[key]);
          return (
            <div key={key} className="space-y-1">
              <div className="flex items-center justify-between text-xs">
                <span className="text-[var(--text-secondary)]">{label}</span>
                <span className={`font-semibold tabular-nums ${scoreColor(val)}`}>
                  {val}%
                </span>
              </div>
              <div className="h-1.5 w-full overflow-hidden rounded-full bg-[var(--bg-base)]">
                <div
                  className={`h-full rounded-full transition-all ${scoreBg(val)}`}
                  style={{ width: `${val}%` }}
                />
              </div>
            </div>
          );
        })}
      </div>

      {/* AI code ratio (informational) */}
      {data.aiCodeRatio > 0 && (
        <p className="text-xs text-[var(--text-tertiary)]">
          AI-generated code: {Math.round(data.aiCodeRatio)}% of files
        </p>
      )}
    </div>
  );
}
