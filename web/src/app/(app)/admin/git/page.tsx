"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { authFetch } from "@/lib/auth-fetch";
import { cn } from "@/lib/utils";

interface GitConfigState {
  default_token_set: boolean;
  default_token_hint?: string;
  ssh_key_path: string;
}

async function handleApiError(res: Response): Promise<string> {
  const text = await res.text();
  try {
    const json = JSON.parse(text);
    if (json.error) return json.error;
  } catch {
    /* not JSON */
  }
  if (text.trimStart().startsWith("<")) {
    return `Server error (HTTP ${res.status}). The API may be restarting — try again in a moment.`;
  }
  return text || `HTTP ${res.status}`;
}

function formatRelativeSaved(ts: number | null): string {
  if (!ts) return "";
  const secs = Math.floor((Date.now() - ts) / 1000);
  if (secs < 5) return "just now";
  if (secs < 60) return `${secs}s ago`;
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}

export default function AdminGitPage() {
  const [serverConfig, setServerConfig] = useState<GitConfigState | null>(null);
  const [sshKeyPath, setSshKeyPath] = useState("");
  const [token, setToken] = useState("");
  const [loadError, setLoadError] = useState<string | null>(null);

  const savedSnapshotRef = useRef<string>("");
  const [lastSavedAt, setLastSavedAt] = useState<number | null>(null);
  const [, setTick] = useState(0);

  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [success, setSuccess] = useState(false);

  const snapshot = useMemo(() => JSON.stringify({ sshKeyPath }), [sshKeyPath]);
  const dirty = savedSnapshotRef.current !== "" && snapshot !== savedSnapshotRef.current;
  const hasPendingToken = token.length > 0;

  const loadConfig = useCallback(async () => {
    try {
      const res = await authFetch("/api/v1/admin/git-config");
      if (!res.ok) throw new Error(await handleApiError(res));
      const cfg = (await res.json()) as GitConfigState;
      setServerConfig(cfg);
      setSshKeyPath(cfg.ssh_key_path || "");
      savedSnapshotRef.current = JSON.stringify({ sshKeyPath: cfg.ssh_key_path || "" });
    } catch (e) {
      setLoadError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    loadConfig();
  }, [loadConfig]);

  useEffect(() => {
    if (!lastSavedAt) return;
    const id = setInterval(() => setTick((t) => t + 1), 30_000);
    return () => clearInterval(id);
  }, [lastSavedAt]);

  async function saveGitConfig() {
    if (saving) return;
    setSaving(true);
    setMessage(null);
    setSuccess(false);
    try {
      const body: Record<string, string> = {};
      if (token) body.default_token = token;
      body.ssh_key_path = sshKeyPath;
      const res = await authFetch("/api/v1/admin/git-config", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok) throw new Error(await handleApiError(res));
      const data = await res.json();
      setMessage("Git credentials saved." + (data.note ? ` ${data.note}` : ""));
      setSuccess(true);
      setToken("");
      setLastSavedAt(Date.now());
      savedSnapshotRef.current = snapshot;
      loadConfig();
    } catch (e) {
      setSuccess(false);
      setMessage(`Error: ${(e as Error).message}`);
    }
    setSaving(false);
  }

  const fieldWrapClass = "grid gap-1.5";
  const labelClass = "text-sm font-medium text-[var(--text-primary)]";
  const helpTextClass = "text-xs text-[var(--text-secondary)]";
  const monoInputClass =
    "h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm font-mono text-[var(--text-primary)]";
  const stackClass = "grid gap-4 max-w-[32rem]";
  const codeBlockClass =
    "rounded-[var(--radius-md)] bg-black/20 p-3 font-mono text-sm whitespace-pre-wrap text-[var(--text-primary)]";
  const messageClass = (ok: boolean) =>
    cn(
      "rounded-[var(--radius-md)] border px-3 py-2 text-sm",
      ok
        ? "border-[var(--color-success,#22c55e)] bg-[rgba(34,197,94,0.1)] text-[var(--color-success,#22c55e)]"
        : "border-[var(--color-error,#ef4444)] bg-[rgba(239,68,68,0.1)] text-[var(--color-error,#ef4444)]"
    );

  if (loadError) {
    return (
      <PageFrame>
        <PageHeader eyebrow="Admin" title="Git credentials" />
        <Panel>
          <p className="text-sm text-[var(--color-error,#ef4444)]">
            Could not load git configuration: {loadError}
          </p>
        </Panel>
      </PageFrame>
    );
  }

  return (
    <PageFrame>
      <PageHeader
        eyebrow="Admin"
        title="Git credentials"
        description="Credentials for cloning and updating private repositories. Per-repository tokens override the default."
        actions={
          <div className="flex items-center gap-3">
            {dirty || hasPendingToken ? (
              <span className="inline-flex items-center gap-1.5 rounded-full border border-[var(--border-default)] bg-[var(--bg-raised)] px-2.5 py-1 text-xs font-medium text-[var(--text-secondary)]">
                <span className="h-1.5 w-1.5 rounded-full bg-amber-400" />
                Unsaved changes
              </span>
            ) : lastSavedAt ? (
              <span className="text-xs text-[var(--text-tertiary)]">
                Saved {formatRelativeSaved(lastSavedAt)}
              </span>
            ) : null}
          </div>
        }
      />

      <Panel className="mb-4">
        <div className={stackClass}>
          <div className={fieldWrapClass}>
            <label className={labelClass}>Default Access Token (PAT)</label>
            <div className="flex items-center gap-2">
              <input
                type="password"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder={
                  serverConfig?.default_token_set
                    ? "Token is configured (enter new to replace)"
                    : "ghp_... or glpat-..."
                }
                className={`flex-1 ${monoInputClass}`}
              />
              {serverConfig?.default_token_set && (
                <span className="whitespace-nowrap font-mono text-xs text-[var(--color-success,#22c55e)]">
                  {serverConfig.default_token_hint || "Configured"}
                </span>
              )}
            </div>
            <p className={helpTextClass}>
              Works with GitHub, GitLab, and Bitbucket personal access tokens for HTTPS repos.
            </p>
          </div>

          <div className={fieldWrapClass}>
            <label className={labelClass}>SSH Private Key Path</label>
            <input
              type="text"
              value={sshKeyPath}
              onChange={(e) => setSshKeyPath(e.target.value)}
              placeholder="~/.ssh/id_ed25519"
              className={monoInputClass}
            />
            <p className={helpTextClass}>
              Used for git@ SSH URLs. Leave empty to use the system SSH agent.
            </p>
          </div>

          <div className="flex items-center gap-2">
            <Button onClick={saveGitConfig} disabled={saving || (!dirty && !hasPendingToken)}>
              {saving ? "Saving..." : "Save"}
            </Button>
          </div>

          {message && <p className={messageClass(success)}>{message}</p>}
        </div>
      </Panel>

      <Panel>
        <h3 className="mb-2 text-base font-semibold text-[var(--text-primary)]">Environment Variables</h3>
        <p className="mb-3 text-sm text-[var(--text-secondary)]">
          For persistent configuration across server restarts, set these environment variables:
        </p>
        <div className={codeBlockClass}>
          <div>SOURCEBRIDGE_GIT_DEFAULT_TOKEN=ghp_your_token</div>
          <div>SOURCEBRIDGE_GIT_SSH_KEY_PATH=/path/to/key</div>
        </div>
      </Panel>
    </PageFrame>
  );
}
