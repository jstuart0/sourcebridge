"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { MessageSquarePlus, Pin, ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { buildRepositorySourceHref, type SourceTarget } from "@/lib/source-target";
import { TOKEN_KEY } from "@/lib/token-key";

interface SharedSourceContext {
  id: string;
  repository_id: string;
  file_path: string;
  line_start: number;
  line_end: number;
  label: string;
  focus_reason?: string;
  created_by: string;
  created_at: string;
}

interface SourceAnnotation {
  id: string;
  repository_id: string;
  file_path: string;
  line_start: number;
  line_end: number;
  kind: string;
  body: string;
  status: string;
  created_by: string;
  created_at: string;
}

async function enterpriseFetch(url: string, init?: RequestInit) {
  const token = typeof window !== "undefined" ? localStorage.getItem(TOKEN_KEY) : null;
  const res = await fetch(url, {
    credentials: "include",
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(init?.headers ?? {}),
    },
  });

  if (res.status === 401) {
    localStorage.removeItem(TOKEN_KEY);
    window.location.href = "/login";
    throw new Error("Session expired");
  }
  if (!res.ok) {
    const text = await res.text();
    throw new Error(text || `HTTP ${res.status}`);
  }
  return res.json();
}

export function EnterpriseSourcePanel({
  orgId = "default",
  repositoryId,
  target,
}: {
  orgId?: string;
  repositoryId: string;
  target: SourceTarget | null;
}) {
  const apiBase = `/api/v1/enterprise/source/${orgId}`;
  const [contexts, setContexts] = useState<SharedSourceContext[]>([]);
  const [annotations, setAnnotations] = useState<SourceAnnotation[]>([]);
  const [shareLabel, setShareLabel] = useState("");
  const [annotationBody, setAnnotationBody] = useState("");
  const [annotationKind, setAnnotationKind] = useState("review");
  const [loading, setLoading] = useState(false);

  const isActionable = Boolean(target?.filePath);
  const defaultLabel = useMemo(() => {
    if (!target?.filePath) return "";
    const suffix = target.line ? `:${target.line}${target.endLine && target.endLine !== target.line ? `-${target.endLine}` : ""}` : "";
    return `${target.filePath}${suffix}`;
  }, [target]);

  const loadContexts = useCallback(async () => {
    const data = await enterpriseFetch(`${apiBase}/contexts`);
    setContexts(data.contexts ?? []);
  }, [apiBase]);

  const loadAnnotations = useCallback(async () => {
    if (!target?.filePath) {
      setAnnotations([]);
      return;
    }
    const params = new URLSearchParams({
      repository_id: repositoryId,
      file_path: target.filePath,
    });
    const data = await enterpriseFetch(`${apiBase}/annotations?${params.toString()}`);
    setAnnotations(data.annotations ?? []);
  }, [apiBase, repositoryId, target?.filePath]);

  useEffect(() => {
    if (process.env.NEXT_PUBLIC_EDITION !== "enterprise") return;
    loadContexts().catch(() => undefined);
  }, [loadContexts]);

  useEffect(() => {
    if (process.env.NEXT_PUBLIC_EDITION !== "enterprise") return;
    loadAnnotations().catch(() => undefined);
  }, [loadAnnotations]);

  useEffect(() => {
    if (process.env.NEXT_PUBLIC_EDITION !== "enterprise" || !target?.filePath) return;
    const controller = new AbortController();
    enterpriseFetch(`${apiBase}/events/view`, {
      method: "POST",
      body: JSON.stringify({
        repository_id: repositoryId,
        file_path: target.filePath,
        line_start: target.line ?? 0,
        line_end: target.endLine ?? target.line ?? 0,
        reason: "source_viewer",
      }),
      signal: controller.signal,
    }).catch(() => undefined);
    return () => controller.abort();
  }, [apiBase, repositoryId, target?.filePath, target?.line, target?.endLine]);

  if (process.env.NEXT_PUBLIC_EDITION !== "enterprise") {
    return null;
  }

  async function handleShare() {
    if (!target?.filePath) return;
    setLoading(true);
    await enterpriseFetch(`${apiBase}/contexts`, {
      method: "POST",
      body: JSON.stringify({
        repository_id: repositoryId,
        file_path: target.filePath,
        line_start: target.line ?? 1,
        line_end: target.endLine ?? target.line ?? 1,
        label: shareLabel.trim() || defaultLabel,
        focus_reason: "shared_investigation",
      }),
    });
    setShareLabel("");
    await loadContexts();
    setLoading(false);
  }

  async function handleAnnotate() {
    if (!target?.filePath || !annotationBody.trim()) return;
    setLoading(true);
    await enterpriseFetch(`${apiBase}/annotations`, {
      method: "POST",
      body: JSON.stringify({
        repository_id: repositoryId,
        file_path: target.filePath,
        line_start: target.line ?? 1,
        line_end: target.endLine ?? target.line ?? 1,
        kind: annotationKind,
        body: annotationBody.trim(),
      }),
    });
    setAnnotationBody("");
    await loadAnnotations();
    setLoading(false);
  }

  return (
    <Panel variant="accent" className="space-y-6">
      <div className="space-y-1">
        <p className="text-xs font-semibold uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
          Enterprise Source Collaboration
        </p>
        <h3 className="text-lg font-semibold tracking-[-0.03em] text-[var(--text-primary)]">
          Team context and governance
        </h3>
        <p className="text-sm text-[var(--text-secondary)]">
          Share investigation targets, attach review notes to source ranges, and record governed source access.
        </p>
      </div>

      <div className="space-y-3 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] p-4">
        <div className="flex items-center gap-2 text-sm font-medium text-[var(--text-primary)]">
          <Pin className="h-4 w-4" />
          Shared team context
        </div>
        <input
          value={shareLabel}
          onChange={(e) => setShareLabel(e.target.value)}
          placeholder={defaultLabel || "Open a file to share context"}
          disabled={!isActionable || loading}
          className="h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-surface)] px-3 text-sm text-[var(--text-primary)]"
        />
        <Button disabled={!isActionable || loading} onClick={handleShare}>
          Share Current Range
        </Button>
        <div className="space-y-2">
          {contexts.slice(0, 8).map((ctx) => (
            <Link
              key={ctx.id}
              href={buildRepositorySourceHref(ctx.repository_id, {
                tab: "files",
                filePath: ctx.file_path,
                line: ctx.line_start,
                endLine: ctx.line_end,
              })}
              className="block rounded-[var(--control-radius)] border border-[var(--border-subtle)] bg-[var(--bg-surface)] px-3 py-2 text-sm hover:bg-[var(--bg-hover)]"
            >
              <div className="font-medium text-[var(--text-primary)]">{ctx.label}</div>
              <div className="mt-1 font-mono text-xs text-[var(--text-secondary)]">
                {ctx.file_path}:{ctx.line_start}
                {ctx.line_end && ctx.line_end !== ctx.line_start ? `-${ctx.line_end}` : ""}
              </div>
            </Link>
          ))}
          {contexts.length === 0 ? (
            <p className="text-sm text-[var(--text-secondary)]">No shared source context yet.</p>
          ) : null}
        </div>
      </div>

      <div className="space-y-3 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] p-4">
        <div className="flex items-center gap-2 text-sm font-medium text-[var(--text-primary)]">
          <MessageSquarePlus className="h-4 w-4" />
          Source annotations
        </div>
        <div className="flex gap-2">
          <select
            value={annotationKind}
            onChange={(e) => setAnnotationKind(e.target.value)}
            disabled={!isActionable || loading}
            className="h-11 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-surface)] px-3 text-sm text-[var(--text-primary)]"
          >
            <option value="review">Review</option>
            <option value="policy">Policy</option>
            <option value="compliance">Compliance</option>
          </select>
          <input
            value={annotationBody}
            onChange={(e) => setAnnotationBody(e.target.value)}
            placeholder={isActionable ? "Add a note for this range…" : "Open a file to annotate"}
            disabled={!isActionable || loading}
            className="h-11 flex-1 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-surface)] px-3 text-sm text-[var(--text-primary)]"
          />
        </div>
        <Button variant="secondary" disabled={!isActionable || !annotationBody.trim() || loading} onClick={handleAnnotate}>
          Add Annotation
        </Button>
        <div className="space-y-2">
          {annotations.map((annotation) => (
            <div
              key={annotation.id}
              className="rounded-[var(--control-radius)] border border-[var(--border-subtle)] bg-[var(--bg-surface)] px-3 py-2"
            >
              <div className="flex items-center gap-2 text-xs uppercase tracking-[0.12em] text-[var(--text-tertiary)]">
                <ShieldCheck className="h-3.5 w-3.5" />
                {annotation.kind}
              </div>
              <p className="mt-2 text-sm text-[var(--text-primary)]">{annotation.body}</p>
              <p className="mt-2 font-mono text-xs text-[var(--text-secondary)]">
                {annotation.file_path}:{annotation.line_start}
                {annotation.line_end && annotation.line_end !== annotation.line_start ? `-${annotation.line_end}` : ""}
              </p>
            </div>
          ))}
          {annotations.length === 0 ? (
            <p className="text-sm text-[var(--text-secondary)]">No annotations for this file yet.</p>
          ) : null}
        </div>
      </div>
    </Panel>
  );
}
