"use client";

import { useCallback, useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { authFetch } from "@/lib/auth-fetch";

interface ApiToken {
  id: string;
  name: string;
  prefix: string;
  created_at: string;
  last_used_at?: string | null;
  kind?: string;
  client_type?: string;
}

interface CreateTokenResponse {
  id: string;
  name: string;
  prefix: string;
  token: string;
  created_at: string;
}

async function readError(res: Response): Promise<string> {
  const text = await res.text();
  try {
    const json = JSON.parse(text);
    if (json.error) return json.error;
  } catch {
    /* not JSON */
  }
  return text || `HTTP ${res.status}`;
}

export default function SettingsTokensPage() {
  const [tokens, setTokens] = useState<ApiToken[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [newName, setNewName] = useState("");
  const [creating, setCreating] = useState(false);
  const [createdToken, setCreatedToken] = useState<CreateTokenResponse | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await authFetch("/api/v1/tokens");
      if (!res.ok) throw new Error(await readError(res));
      setTokens(await res.json());
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
    setLoading(false);
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function createToken(e: React.FormEvent) {
    e.preventDefault();
    if (!newName.trim() || creating) return;
    setCreating(true);
    setError(null);
    try {
      const res = await authFetch("/api/v1/tokens", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: newName.trim() }),
      });
      if (!res.ok) throw new Error(await readError(res));
      const data = (await res.json()) as CreateTokenResponse;
      setCreatedToken(data);
      setNewName("");
      load();
    } catch (e) {
      setError((e as Error).message);
    }
    setCreating(false);
  }

  async function revoke(id: string) {
    if (!confirm("Revoke this token? Any client using it will lose access.")) return;
    try {
      const res = await authFetch(`/api/v1/tokens/${encodeURIComponent(id)}`, {
        method: "DELETE",
      });
      if (!res.ok) throw new Error(await readError(res));
      load();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  const inputClass =
    "h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)]";

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Preferences"
        title="API tokens"
        description="Personal access tokens for the CLI, IDE plugins, and other clients."
      />

      <Panel className="space-y-4">
        <h3 className="text-base font-semibold text-[var(--text-primary)]">Create token</h3>
        <form onSubmit={createToken} className="flex flex-col gap-3 sm:flex-row">
          <input
            type="text"
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
            placeholder="e.g. Laptop CLI, VS Code, CI runner"
            required
            className={`flex-1 ${inputClass}`}
          />
          <Button type="submit" disabled={creating || !newName.trim()}>
            {creating ? "Creating…" : "Create token"}
          </Button>
        </form>

        {createdToken && (
          <div className="rounded-[var(--radius-md)] border border-[var(--color-success,#22c55e)] bg-[rgba(34,197,94,0.08)] p-3">
            <p className="mb-2 text-sm font-medium text-[var(--text-primary)]">
              Token created. Copy it now — it won&apos;t be shown again.
            </p>
            <pre className="overflow-x-auto rounded-[var(--control-radius)] bg-black/30 p-2 font-mono text-xs text-[var(--text-primary)]">
              {createdToken.token}
            </pre>
            <button
              type="button"
              onClick={() => setCreatedToken(null)}
              className="mt-2 text-xs text-[var(--text-tertiary)] underline underline-offset-2 hover:text-[var(--text-primary)]"
            >
              Dismiss
            </button>
          </div>
        )}
      </Panel>

      <Panel>
        <h3 className="mb-3 text-base font-semibold text-[var(--text-primary)]">
          Active tokens ({tokens.length})
        </h3>

        {error && (
          <p className="mb-3 rounded-[var(--radius-md)] border border-[var(--color-error,#ef4444)] bg-[rgba(239,68,68,0.08)] px-3 py-2 text-sm text-[var(--color-error,#ef4444)]">
            {error}
          </p>
        )}

        {loading ? (
          <p className="text-sm text-[var(--text-secondary)]">Loading…</p>
        ) : tokens.length === 0 ? (
          <p className="text-sm text-[var(--text-secondary)]">No tokens yet.</p>
        ) : (
          <ul className="divide-y divide-[var(--border-subtle)]">
            {tokens.map((t) => (
              <li
                key={t.id}
                className="flex flex-col gap-2 py-3 sm:flex-row sm:items-center sm:justify-between"
              >
                <div className="min-w-0">
                  <div className="truncate text-sm font-medium text-[var(--text-primary)]">
                    {t.name}
                  </div>
                  <div className="mt-0.5 flex flex-wrap gap-x-3 gap-y-1 text-xs text-[var(--text-tertiary)]">
                    <span className="font-mono">{t.prefix}…</span>
                    <span>Created {new Date(t.created_at).toLocaleDateString()}</span>
                    {t.last_used_at ? (
                      <span>Last used {new Date(t.last_used_at).toLocaleDateString()}</span>
                    ) : (
                      <span>Never used</span>
                    )}
                    {t.client_type ? <span>Client: {t.client_type}</span> : null}
                  </div>
                </div>
                <Button size="sm" variant="secondary" onClick={() => revoke(t.id)}>
                  Revoke
                </Button>
              </li>
            ))}
          </ul>
        )}
      </Panel>
    </div>
  );
}
