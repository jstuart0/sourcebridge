"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { authFetch } from "@/lib/auth-fetch";
import { Button } from "@/components/ui/button";

interface Props {
  repoId: string;
  onComplete?: () => void;
}

type JobStatus = "pending" | "generating" | "ready" | "failed" | "cancelled";

const POLL_INTERVAL_MS = 2500;
// Stop polling after 10 minutes regardless of job status.
const POLL_TIMEOUT_MS = 10 * 60 * 1000;

/**
 * ImproveLabelsButton — triggers the batch LLM relabel job for all clusters
 * in the given repository. While the job is running the button shows a spinner
 * and is disabled. On completion (ready or failed) the button re-enables and
 * calls onComplete so the parent can refresh the table. Polling stops after
 * 10 minutes and shows a timeout notice.
 */
export function ImproveLabelsButton({ repoId, onComplete }: Props) {
  const [jobId, setJobId] = useState<string | null>(null);
  const [status, setStatus] = useState<JobStatus | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [timedOut, setTimedOut] = useState(false);
  const pollRef = useRef<number | null>(null);
  const timeoutRef = useRef<number | null>(null);

  const stopPolling = useCallback(() => {
    if (pollRef.current !== null) {
      window.clearInterval(pollRef.current);
      pollRef.current = null;
    }
    if (timeoutRef.current !== null) {
      window.clearTimeout(timeoutRef.current);
      timeoutRef.current = null;
    }
  }, []);

  const pollJobStatus = useCallback(
    async (id: string) => {
      try {
        const res = await authFetch(`/api/v1/repositories/${encodeURIComponent(repoId)}/llm-jobs/${encodeURIComponent(id)}`);
        if (!res.ok) return;
        const data = (await res.json()) as { status: JobStatus };
        const next = data.status;
        setStatus(next);
        if (next === "ready" || next === "failed" || next === "cancelled") {
          stopPolling();
          onComplete?.();
        }
      } catch {
        // Network error — keep polling, don't surface as a blocking error.
      }
    },
    [stopPolling, onComplete],
  );

  useEffect(() => {
    if (!jobId) return;
    // Start polling immediately then on interval.
    void pollJobStatus(jobId);
    pollRef.current = window.setInterval(() => {
      void pollJobStatus(jobId);
    }, POLL_INTERVAL_MS);

    // 10-minute wall-clock ceiling.
    timeoutRef.current = window.setTimeout(() => {
      stopPolling();
      setStatus(null);
      setTimedOut(true);
    }, POLL_TIMEOUT_MS);

    return stopPolling;
  }, [jobId, pollJobStatus, stopPolling]);

  const handleClick = useCallback(async () => {
    setError(null);
    setTimedOut(false);
    setStatus("pending");
    try {
      const res = await authFetch(
        `/api/v1/repositories/${encodeURIComponent(repoId)}/clusters/relabel`,
        { method: "POST" },
      );
      if (!res.ok) {
        const text = await res.text();
        setError(text || "Couldn't improve labels. Try again, or contact your admin if this persists.");
        setStatus(null);
        return;
      }
      const data = (await res.json()) as { job_id: string };
      setJobId(data.job_id);
    } catch {
      setError("Couldn't improve labels. Try again, or contact your admin if this persists.");
      setStatus(null);
    }
  }, [repoId]);

  const running = status === "pending" || status === "generating";

  return (
    <div className="flex items-center gap-2">
      <Button
        onClick={handleClick}
        disabled={running}
        className="flex items-center gap-1.5"
      >
        {running ? (
          <>
            <span
              className="inline-block h-3.5 w-3.5 animate-spin rounded-full border-2 border-current border-t-transparent"
              aria-hidden="true"
            />
            Improving labels…
          </>
        ) : (
          "Improve labels"
        )}
      </Button>
      {status === "ready" && (
        <span className="text-sm text-[var(--text-secondary)]">Labels updated.</span>
      )}
      {status === "failed" && (
        <span className="text-sm text-[var(--text-danger,red)]">
          Couldn&apos;t improve labels. Try again, or contact your admin if this persists.
        </span>
      )}
      {timedOut && (
        <span className="text-sm text-[var(--text-secondary)]">
          Improving labels is taking longer than expected. The job may still complete in the background.
        </span>
      )}
      {error ? (
        <span className="text-sm text-[var(--text-danger,red)]">{error}</span>
      ) : null}
    </div>
  );
}
