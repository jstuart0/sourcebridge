"use client";

import { useCallback, useState } from "react";
import { BellRing } from "lucide-react";
import { useEventStream, ServerEvent } from "@/lib/sse";

interface Toast {
  id: number;
  message: string;
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

  const handleEvent = useCallback((event: ServerEvent) => {
    const message = formatEvent(event);
    if (!message) return;

    const id = ++nextId;
    setToasts((prev) => [...prev.slice(-4), { id, message }]);

    setTimeout(() => {
      setToasts((prev) => prev.filter((t) => t.id !== id));
    }, 5000);
  }, []);

  useEventStream(handleEvent);

  if (toasts.length === 0) return null;

  return (
    <div className="fixed bottom-6 right-6 z-[9999] flex max-w-[min(24rem,calc(100vw-3rem))] flex-col gap-3">
      {toasts.map((toast) => (
        <div
          key={toast.id}
          className="rounded-[var(--panel-radius)] border border-[var(--panel-border)] bg-[var(--panel-bg-glass)] px-4 py-3 text-sm text-[var(--text-primary)] shadow-[var(--panel-shadow-strong)] backdrop-blur-[var(--panel-blur)]"
        >
          <div className="flex items-start gap-3">
            <BellRing className="mt-0.5 h-4 w-4 shrink-0 text-[var(--accent-primary)]" />
            <span className="leading-6">{toast.message}</span>
          </div>
        </div>
      ))}
    </div>
  );
}
