"use client";

import { useState } from "react";

import { Button } from "@/components/ui/button";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { authFetch } from "@/lib/auth-fetch";
import { cn } from "@/lib/utils";

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

export default function SettingsSecurityPage() {
  const [oldPw, setOldPw] = useState("");
  const [newPw, setNewPw] = useState("");
  const [confirmPw, setConfirmPw] = useState("");
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [success, setSuccess] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setMessage(null);
    setSuccess(false);

    if (newPw.length < 8) {
      setMessage("New password must be at least 8 characters.");
      setSuccess(false);
      return;
    }
    if (newPw !== confirmPw) {
      setMessage("New password and confirmation do not match.");
      setSuccess(false);
      return;
    }

    setSaving(true);
    try {
      const res = await authFetch("/auth/change-password", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ old_password: oldPw, new_password: newPw }),
      });
      if (!res.ok) throw new Error(await readError(res));
      setSuccess(true);
      setMessage("Password updated successfully.");
      setOldPw("");
      setNewPw("");
      setConfirmPw("");
    } catch (e) {
      setSuccess(false);
      setMessage((e as Error).message);
    }
    setSaving(false);
  }

  const inputClass =
    "h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)]";
  const labelClass = "text-sm font-medium text-[var(--text-primary)]";
  const messageClass = cn(
    "rounded-[var(--radius-md)] border px-3 py-2 text-sm",
    success
      ? "border-[var(--color-success,#22c55e)] bg-[rgba(34,197,94,0.08)] text-[var(--color-success,#22c55e)]"
      : "border-[var(--color-error,#ef4444)] bg-[rgba(239,68,68,0.08)] text-[var(--color-error,#ef4444)]"
  );

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Preferences"
        title="Security"
        description="Change your local password. OIDC / enterprise logins use your identity provider."
      />

      <Panel>
        <form onSubmit={submit} className="grid max-w-md gap-4">
          <div className="grid gap-1.5">
            <label className={labelClass}>Current password</label>
            <input
              type="password"
              value={oldPw}
              onChange={(e) => setOldPw(e.target.value)}
              required
              autoComplete="current-password"
              className={inputClass}
            />
          </div>
          <div className="grid gap-1.5">
            <label className={labelClass}>New password</label>
            <input
              type="password"
              value={newPw}
              onChange={(e) => setNewPw(e.target.value)}
              required
              minLength={8}
              autoComplete="new-password"
              className={inputClass}
            />
            <p className="text-xs text-[var(--text-tertiary)]">Minimum 8 characters.</p>
          </div>
          <div className="grid gap-1.5">
            <label className={labelClass}>Confirm new password</label>
            <input
              type="password"
              value={confirmPw}
              onChange={(e) => setConfirmPw(e.target.value)}
              required
              minLength={8}
              autoComplete="new-password"
              className={inputClass}
            />
          </div>

          <div>
            <Button type="submit" disabled={saving || !oldPw || !newPw || !confirmPw}>
              {saving ? "Updating…" : "Change password"}
            </Button>
          </div>

          {message && <p className={messageClass}>{message}</p>}
        </form>
      </Panel>
    </div>
  );
}
