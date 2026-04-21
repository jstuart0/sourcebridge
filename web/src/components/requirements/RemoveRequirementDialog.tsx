"use client";

import { useEffect, useState } from "react";
import { useMutation } from "urql";
import { Button } from "@/components/ui/button";
import { MOVE_TO_TRASH_MUTATION } from "@/lib/graphql/queries";

type RemoveRequirementDialogProps = {
  open: boolean;
  requirementId: string;
  requirementLabel: string; // user-facing title or external id
  onClose: () => void;
  onRemoved?: () => void;
};

// Confirm-and-execute dialog for moving a requirement to Trash. The
// honest copy is deliberate: trashing a requirement does NOT cascade
// to its code links (see internal/trash/surrealstore.go:268-272). If
// we promised a cascade here we'd be lying — links survive in the DB
// and remain reachable via the code-side views even after the
// requirement itself disappears from requirement lists.
export function RemoveRequirementDialog({
  open,
  requirementId,
  requirementLabel,
  onClose,
  onRemoved,
}: RemoveRequirementDialogProps) {
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [result, moveToTrash] = useMutation(MOVE_TO_TRASH_MUTATION);
  const disabled = result.fetching;

  useEffect(() => {
    if (open) {
      setReason("");
      setError(null);
    }
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !disabled) onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, disabled, onClose]);

  const handleConfirm = async () => {
    setError(null);
    const vars: Record<string, unknown> = {
      type: "REQUIREMENT",
      id: requirementId,
    };
    if (reason.trim()) vars.reason = reason.trim();
    const res = await moveToTrash(vars);
    if (res.error) {
      setError(res.error.graphQLErrors[0]?.message ?? res.error.message ?? "Remove failed");
      return;
    }
    onRemoved?.();
    onClose();
  };

  if (!open) return null;

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="remove-requirement-title"
      className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4 sm:p-10"
      onClick={(e) => {
        if (e.target === e.currentTarget && !disabled) onClose();
      }}
    >
      <div className="mt-10 w-full max-w-lg rounded-[var(--panel-radius)] border border-[var(--panel-border)] bg-[var(--panel-bg-elevated,var(--panel-bg))] p-5 shadow-[var(--panel-shadow-strong,var(--panel-shadow))] sm:p-6">
        <h2
          id="remove-requirement-title"
          className="text-lg font-semibold text-[var(--text-primary)]"
        >
          Move &quot;{requirementLabel}&quot; to Trash?
        </h2>
        <div className="mt-3 space-y-3 text-sm text-[var(--text-secondary)]">
          <p>
            The requirement will be hidden from requirement lists and cannot be edited
            from the web app while it&apos;s in Trash. An admin can restore it later.
          </p>
          <p>
            <strong className="text-[var(--text-primary)]">Its links to code symbols stay in place.</strong>{" "}
            They remain visible from the code side, but this requirement will no longer
            appear alongside them in requirement views.
          </p>
        </div>

        <label className="mt-5 block space-y-1.5">
          <span className="text-xs font-medium uppercase tracking-wide text-[var(--text-secondary)]">
            Reason (optional)
          </span>
          <input
            type="text"
            value={reason}
            disabled={disabled}
            onChange={(e) => setReason(e.target.value)}
            className="w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-surface)] px-3 py-2 text-sm text-[var(--text-primary)] placeholder:text-[var(--text-tertiary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-focus)] disabled:opacity-60"
            placeholder="Stored with the Trash record for later context"
          />
        </label>

        {error ? (
          <div className="mt-4 rounded-[var(--control-radius)] border border-[var(--color-error,#ef4444)] bg-[color:var(--color-error-subtle,rgba(239,68,68,0.08))] px-3 py-2 text-sm text-[var(--color-error,#ef4444)]">
            {error}
          </div>
        ) : null}

        <div className="mt-6 flex items-center justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={onClose} disabled={disabled}>
            Cancel
          </Button>
          <Button variant="primary" size="sm" onClick={handleConfirm} disabled={disabled}>
            {disabled ? "Moving…" : "Move to Trash"}
          </Button>
        </div>
      </div>
    </div>
  );
}
