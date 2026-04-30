"use client";

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

import { useState } from "react";

import { Button } from "@/components/ui/button";
import { authFetch } from "@/lib/auth-fetch";
import { cn } from "@/lib/utils";

import type { ProfileResponse } from "./profile-editor";

// ─────────────────────────────────────────────────────────────────────────
// Profile list — slice 2 (N>=2 mode).
//
// Top section of the /admin/llm page when the user has 2+ profiles.
// Renders one row per profile with:
//
//   [radio]  <name>  [active pill?]   [edit]  [duplicate]  [delete?]
//
// Radio click on a NON-active profile triggers the SwitchProfileDialog
// (parent handles the dialog open). Radio click on the active profile
// is a no-op. The radio is the at-a-glance status; the [active] pill
// is redundant by design (UX intake §2.3).
//
// `[edit]` selects the profile for the editor below — does NOT switch
// active. The selection vs activation distinction is critical.
// `[duplicate]` clones with name "<original> (copy)" and selects the
// new profile in the editor.
// `[delete]` is hidden for the active profile (D5 + UX §4.3); clicking
// it on a non-active profile triggers a confirm-then-delete inline
// flow (no separate modal — the row collapses into a yes/no prompt).
// ─────────────────────────────────────────────────────────────────────────

export interface ProfileListProps {
  profiles: ProfileResponse[];
  /** the profile currently selected in the editor (id) */
  selectedProfileId: string;
  /** locks all interaction (e.g. while repair banner is up) */
  disabled?: boolean;
  onSelectForEdit: (profileId: string) => void;
  onActivateRequested: (profileId: string) => void;
  onAddProfile: () => void;
  onDeleted?: () => void;
  onDuplicated?: (newProfileId: string) => void;
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

export function ProfileList({
  profiles,
  selectedProfileId,
  disabled,
  onSelectForEdit,
  onActivateRequested,
  onAddProfile,
  onDeleted,
  onDuplicated,
}: ProfileListProps) {
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [rowError, setRowError] = useState<{ id: string; msg: string } | null>(null);

  const performDelete = async (id: string) => {
    setBusyId(id);
    setRowError(null);
    try {
      const idPath = encodeURIComponent(id);
      const res = await authFetch(`/api/v1/admin/llm-profiles/${idPath}`, {
        method: "DELETE",
      });
      if (res.status === 204) {
        setConfirmDeleteId(null);
        onDeleted?.();
        return;
      }
      throw new Error(await handleApiError(res));
    } catch (e) {
      setRowError({ id, msg: (e as Error).message });
    } finally {
      setBusyId(null);
    }
  };

  const performDuplicate = async (source: ProfileResponse) => {
    setBusyId(source.id);
    setRowError(null);
    try {
      const body = {
        name: `${source.name} (copy)`,
        provider: source.provider,
        base_url: source.base_url,
        // api_key intentionally NOT copied — we don't have plaintext;
        // the user must re-enter or copy via a separate path. The
        // duplicated profile will have api_key_set=false.
        summary_model: source.summary_model,
        review_model: source.review_model,
        ask_model: source.ask_model,
        knowledge_model: source.knowledge_model,
        architecture_diagram_model: source.architecture_diagram_model,
        report_model: source.report_model,
        draft_model: source.draft_model,
        timeout_secs: source.timeout_secs,
        advanced_mode: source.advanced_mode,
      };
      const res = await authFetch("/api/v1/admin/llm-profiles", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok) throw new Error(await handleApiError(res));
      const data = (await res.json()) as { id: string };
      onDuplicated?.(data.id);
    } catch (e) {
      setRowError({ id: source.id, msg: (e as Error).message });
    } finally {
      setBusyId(null);
    }
  };

  return (
    <div className="space-y-3" data-testid="profile-list">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold uppercase tracking-wide text-[var(--text-secondary)]">
          Profiles
        </h3>
        <Button
          variant="secondary"
          size="sm"
          onClick={onAddProfile}
          disabled={disabled}
          data-testid="profile-list-add"
        >
          + Add profile
        </Button>
      </div>

      <ul className="divide-y divide-[var(--border-subtle)] rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--bg-raised)]">
        {profiles.map((p) => {
          const isSelected = p.id === selectedProfileId;
          const showConfirm = confirmDeleteId === p.id;
          const isBusy = busyId === p.id;
          const error = rowError?.id === p.id ? rowError.msg : null;
          return (
            <li
              key={p.id}
              data-testid={`profile-row-${p.id}`}
              className={cn(
                "flex flex-wrap items-center gap-3 px-4 py-3",
                isSelected && "bg-[var(--bg-hover)]",
              )}
            >
              <label className="flex flex-1 items-center gap-3 text-sm">
                <input
                  type="radio"
                  name="active-profile"
                  checked={p.is_active}
                  onChange={() => {
                    if (!p.is_active && !disabled) {
                      onActivateRequested(p.id);
                    }
                  }}
                  disabled={disabled || isBusy}
                  aria-label={`Activate ${p.name}`}
                  data-testid={`profile-row-${p.id}-radio`}
                  className="h-4 w-4"
                />
                <span className="font-medium text-[var(--text-primary)]">{p.name}</span>
                {p.is_active && (
                  <span
                    data-testid={`profile-row-${p.id}-active-pill`}
                    className="inline-flex items-center rounded-full border border-[var(--color-success,#22c55e)] bg-[rgba(34,197,94,0.1)] px-2 py-0.5 text-xs font-medium text-[var(--color-success,#22c55e)]"
                  >
                    Active
                  </span>
                )}
                <span className="text-xs text-[var(--text-tertiary)]">
                  {p.provider} · {p.summary_model || "(no model set)"}
                </span>
              </label>

              {showConfirm ? (
                <div className="flex items-center gap-2 text-xs">
                  <span className="text-[var(--text-secondary)]">Delete &quot;{p.name}&quot;?</span>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setConfirmDeleteId(null)}
                    disabled={isBusy}
                  >
                    Cancel
                  </Button>
                  <Button
                    variant="primary"
                    size="sm"
                    onClick={() => performDelete(p.id)}
                    disabled={isBusy}
                    data-testid={`profile-row-${p.id}-delete-confirm`}
                  >
                    {isBusy ? "Deleting…" : "Delete"}
                  </Button>
                </div>
              ) : (
                <div className="flex items-center gap-2">
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => onSelectForEdit(p.id)}
                    disabled={disabled || isBusy}
                    data-testid={`profile-row-${p.id}-edit`}
                  >
                    Edit
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => performDuplicate(p)}
                    disabled={disabled || isBusy}
                    data-testid={`profile-row-${p.id}-duplicate`}
                  >
                    {isBusy ? "Working…" : "Duplicate"}
                  </Button>
                  {!p.is_active && (
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => setConfirmDeleteId(p.id)}
                      disabled={disabled || isBusy}
                      data-testid={`profile-row-${p.id}-delete`}
                    >
                      Delete
                    </Button>
                  )}
                  {p.is_active && (
                    <span
                      title="Switch to another profile first to delete this one"
                      className="text-xs text-[var(--text-tertiary)]"
                    >
                      (active — switch first to delete)
                    </span>
                  )}
                </div>
              )}

              {error ? (
                <div
                  role="alert"
                  className="basis-full rounded-[var(--control-radius)] border border-[var(--color-error,#ef4444)] bg-[color:var(--color-error-subtle,rgba(239,68,68,0.08))] px-3 py-2 text-xs text-[var(--color-error,#ef4444)]"
                >
                  {error}
                </div>
              ) : null}
            </li>
          );
        })}
      </ul>
    </div>
  );
}
