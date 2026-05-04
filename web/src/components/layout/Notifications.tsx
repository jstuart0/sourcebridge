"use client";

import { useCallback, useEffect, useState } from "react";
import { BellRing } from "lucide-react";
import { useEventStream, ServerEvent } from "@/lib/sse";
import { AppToastDetail, subscribeToToasts } from "@/lib/notifications";

interface Toast {
  id: number;
  message: string;
  isError?: boolean;
}

let nextId = 0;

function formatEvent(event: ServerEvent): string | null {
  const d = event.data ?? {};
  switch (event.type) {
    case "repo.index.started":
      return `Indexing "${d.repo_name || "repository"}" — this may take a moment.`;
    case "repo.index.completed":
      return `Repository "${d.repo_name || "unknown"}" indexing complete (${d.file_count ?? 0} files).`;
    case "repo.index.failed":
      return `Repository indexing failed: ${d.error || "unknown error"}`;
    case "requirement.imported":
      return `${d.imported ?? 0} requirements imported.`;
    case "requirement.linked":
      return `${d.links_created ?? 0} requirement links created.`;
    case "link.verified":
      return "Link verified.";
    case "link.rejected":
      return "Link rejected.";
    case "review.completed":
      return `Review complete for ${d.file_path || "file"} (${d.findings ?? 0} findings).`;
    default:
      return null;
  }
}

export function Notifications() {
  const [toasts, setToasts] = useState<Toast[]>([]);

  const dismiss = useCallback((id: number) => {
    setToasts((prev) => prev.filter((t) => t.id !== id));
  }, []);

  const push = useCallback((message: string, isError?: boolean) => {
    const id = ++nextId;
    setToasts((prev) => [...prev.slice(-4), { id, message, isError }]);

    setTimeout(() => {
      setToasts((prev) => prev.filter((t) => t.id !== id));
    }, 5000);
  }, []);

  const handleEvent = useCallback((event: ServerEvent) => {
    const message = formatEvent(event);
    if (!message) return;
    push(message, event.type.endsWith(".failed"));
  }, [push]);

  useEventStream(handleEvent);

  useEffect(() => {
    const unsubscribe = subscribeToToasts((detail: AppToastDetail) => {
      push(detail.message);
    });
    return unsubscribe;
  }, [push]);

  if (toasts.length === 0) return null;

  return (
    <div className="fixed bottom-6 right-6 z-[9999] flex max-w-[min(24rem,calc(100vw-3rem))] flex-col gap-3">
      {toasts.map((toast) => (
        <div
          key={toast.id}
          role={toast.isError ? "alert" : "status"}
          aria-live={toast.isError ? "assertive" : "polite"}
          aria-atomic="true"
          className="rounded-[var(--panel-radius)] border border-[var(--panel-border)] bg-[var(--panel-bg-glass)] px-4 py-3 text-sm text-[var(--text-primary)] shadow-[var(--panel-shadow-strong)] backdrop-blur-[var(--panel-blur)]"
        >
          <div className="flex items-start gap-3">
            <BellRing aria-hidden="true" className="mt-0.5 h-4 w-4 shrink-0 text-[var(--accent-primary)]" />
            <span className="flex-1 leading-6">{toast.message}</span>
            <button
              type="button"
              aria-label="Dismiss notification"
              onClick={() => dismiss(toast.id)}
              className="ml-1 mt-0.5 shrink-0 rounded p-0.5 text-[var(--text-tertiary)] transition-colors hover:text-[var(--text-primary)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-[var(--accent-focus)]"
            >
              ×
            </button>
          </div>
        </div>
      ))}
    </div>
  );
}
