"use client";

import { useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
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
  // CA-321: server-configured minimum password length. Defaults to 8 so
  // the form works against old servers that don't expose the field.
  const [passwordMinLength, setPasswordMinLength] = useState(8);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await fetch("/auth/info", { cache: "no-store" });
        if (!res.ok) return;
        const data = await res.json();
        if (!cancelled && typeof data?.password_min_length === "number" && data.password_min_length > 0) {
          setPasswordMinLength(data.password_min_length);
        }
      } catch {
        /* keep default 8 */
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setMessage(null);
    setSuccess(false);

    if (newPw.length < passwordMinLength) {
      setMessage(`New password must be at least ${passwordMinLength} characters.`);
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

  // eslint-disable-next-line @typescript-eslint/no-unused-vars
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
            <Input
              type="password"
              value={oldPw}
              onChange={(e) => setOldPw(e.target.value)}
              required
              autoComplete="current-password"
            />
          </div>
          <div className="grid gap-1.5">
            <label className={labelClass}>New password</label>
            <Input
              type="password"
              value={newPw}
              onChange={(e) => setNewPw(e.target.value)}
              required
              minLength={passwordMinLength}
              autoComplete="new-password"
            />
            <p className="text-xs text-[var(--text-tertiary)]">Minimum {passwordMinLength} characters.</p>
          </div>
          <div className="grid gap-1.5">
            <label className={labelClass}>Confirm new password</label>
            <Input
              type="password"
              value={confirmPw}
              onChange={(e) => setConfirmPw(e.target.value)}
              required
              minLength={passwordMinLength}
              autoComplete="new-password"
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
