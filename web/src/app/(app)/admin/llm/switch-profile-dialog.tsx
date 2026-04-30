"use client";

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

import { useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import { authFetch } from "@/lib/auth-fetch";

// ─────────────────────────────────────────────────────────────────────────
// Switch-profile confirmation dialog — slice 2.
//
// Modal text matches ruby UX §4.1 VERBATIM. The intake explicitly
// flagged "weakening this language ('are you sure?') loses the user's
// actual question (what happens to my running jobs?)". So the copy
// here is the contract; tests assert it.
//
// Activates the picked profile via POST /admin/llm-profiles/{id}/activate.
// On success calls onActivated; on cancel or close, no-op.
// ─────────────────────────────────────────────────────────────────────────

export interface SwitchProfileDialogProps {
  open: boolean;
  /** the currently-active profile (the "from" name in the modal copy) */
  fromProfileName: string;
  /** the profile being activated (the "to" name + id) */
  toProfileId: string;
  toProfileName: string;
  onClose: () => void;
  onActivated?: () => void;
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

export function SwitchProfileDialog({
  open,
  fromProfileName,
  toProfileId,
  toProfileName,
  onClose,
  onActivated,
}: SwitchProfileDialogProps) {
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (open) {
      setError(null);
      setSubmitting(false);
    }
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !submitting) onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, submitting, onClose]);

  const handleConfirm = async () => {
    setSubmitting(true);
    setError(null);
    try {
      const idPath = encodeURIComponent(toProfileId);
      const res = await authFetch(`/api/v1/admin/llm-profiles/${idPath}/activate`, {
        method: "POST",
      });
      if (!res.ok) throw new Error(await handleApiError(res));
      onActivated?.();
      onClose();
    } catch (e) {
      setError((e as Error).message);
      setSubmitting(false);
    }
  };

  if (!open) return null;

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="switch-profile-title"
      data-testid="switch-profile-dialog"
      className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4 sm:p-10"
      onClick={(e) => {
        if (e.target === e.currentTarget && !submitting) onClose();
      }}
    >
      <div className="mt-10 w-full max-w-lg rounded-[var(--panel-radius)] border border-[var(--panel-border)] bg-[var(--panel-bg-elevated,var(--panel-bg))] p-5 shadow-[var(--panel-shadow-strong,var(--panel-shadow))] sm:p-6">
        <h2
          id="switch-profile-title"
          className="text-lg font-semibold text-[var(--text-primary)]"
        >
          Switch active profile?
        </h2>

        {/* UX §4.1 verbatim. Test asserts this text. Do not change without
            a paired UX intake update + test update — the language was
            chosen specifically to answer the user's actual question
            (what happens to running jobs?) without being scary. */}
        <div className="mt-3 space-y-3 text-sm text-[var(--text-secondary)]" data-testid="switch-profile-body">
          <p>
            Switching from <strong className="text-[var(--text-primary)]">{fromProfileName}</strong> to{" "}
            <strong className="text-[var(--text-primary)]">{toProfileName}</strong>. Jobs already running keep
            using <strong className="text-[var(--text-primary)]">{fromProfileName}</strong>. Jobs started
            after this point use <strong className="text-[var(--text-primary)]">{toProfileName}</strong>.
            Switch?
          </p>
        </div>

        {error ? (
          <div
            role="alert"
            className="mt-4 rounded-[var(--control-radius)] border border-[var(--color-error,#ef4444)] bg-[color:var(--color-error-subtle,rgba(239,68,68,0.08))] px-3 py-2 text-sm text-[var(--color-error,#ef4444)]"
          >
            {error}
          </div>
        ) : null}

        <div className="mt-6 flex items-center justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={onClose} disabled={submitting}>
            Cancel
          </Button>
          <Button
            variant="primary"
            size="sm"
            onClick={handleConfirm}
            disabled={submitting}
            data-testid="switch-profile-confirm"
          >
            {submitting ? "Switching…" : "Switch"}
          </Button>
        </div>
      </div>
    </div>
  );
}
