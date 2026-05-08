"use client";

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

import { useCallback, useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { authFetch } from "@/lib/auth-fetch";

import { ProfileEditor, type ProfileResponse } from "./profile-editor";
import { ProfileList } from "./profile-list";
import { ProfileNamePill } from "./profile-name-pill";
import { SwitchProfileDialog } from "./switch-profile-dialog";

// ─────────────────────────────────────────────────────────────────────────
// /admin/llm — slice 2 of LLM provider profiles.
//
// Information architecture (UX intake §2.1, option C — single page that
// reshapes by N). The state machine is:
//
//                     fetch /admin/llm-profiles
//                            │
//        active_profile_missing? ──yes──▶ render REPAIR BANNER on top,
//                            │            disable editor + list, prompt
//                            │            user to pick + activate
//                            no
//                            │
//          profiles.length ──┤
//             │              │              │
//             0              1            >=2
//             │              │              │
//             │              │              │
//             │              │              ▼
//             │              │     N>=2 LAYOUT
//             │              │     - profile list (top)
//             │              │     - active pill at top
//             │              │     - editor below (selected profile)
//             │              │
//             │              ▼
//             │     N=1 LAYOUT (looks like today's /admin/llm exactly)
//             │     - profile name pill in header
//             │     - + Add profile button
//             │     - editor (today's grid)
//             │
//             ▼
//   "loading; migration may still be running" — auto-refresh once;
//   if still empty, typed error banner pointing at runbook
//
// Slice-1-flag absorption (each is enforced in code below):
//
//   #1: ProfileResponse.is_active and active_profile_missing come from
//       the LIST response. We never recompute these client-side. The
//       repair banner gating is exactly `active_profile_missing === true`.
//
//   #2: Legacy GET /admin/llm-config carries new fields
//       (active_profile_id, active_profile_name, active_profile_missing).
//       We use active_profile_name to render the N=1 layout's "you're
//       editing the 'Default' profile" pill so the pill stays correct
//       even on a non-N=1 transition.
//
//   #3: PUT /admin/llm-profiles/{id} can return
//       `409 target_no_longer_active` when concurrent activation
//       happens during the user's edit. The editor surfaces a window
//       CustomEvent; we listen for it here, refetch the profile list,
//       and render an inline conflict-resolution panel with three
//       actions (retry-against-now-active / switch-back-and-retry /
//       discard).
// ─────────────────────────────────────────────────────────────────────────

interface LegacyLLMConfigResponse {
  provider: string;
  base_url: string;
  api_key_set: boolean;
  api_key_hint?: string;
  summary_model: string;
  review_model: string;
  ask_model: string;
  knowledge_model: string;
  architecture_diagram_model: string;
  report_model?: string;
  draft_model: string;
  timeout_secs: number;
  advanced_mode: boolean;
  // slice-1-flag #2: legacy GET extension fields
  active_profile_id?: string;
  active_profile_name?: string;
  active_profile_missing?: boolean;
}

interface ListProfilesResponse {
  profiles: ProfileResponse[];
  active_profile_missing: boolean;
  // Phase 3 / r1 H2: exposed by the API when the encryption key is present.
  // Defaults to true when missing so older API replicas during a rolling
  // deploy don't accidentally show the State 1 onboarding banner.
  encryption_key_set?: boolean;
}

interface ConflictState {
  /** the profile id we tried to PUT and got 409 on */
  targetProfileId: string;
  /** display name of the target (frozen at the time of conflict) */
  targetProfileName: string;
  /** the hint copy from the server */
  hint: string;
}

async function handleApiError(res: Response): Promise<string> {
  const text = await res.text();
  let serverMsg = text;
  try {
    const json = JSON.parse(text);
    if (json.error) serverMsg = json.error;
  } catch {
    /* not JSON */
  }

  // r1 H7 — translate operator-vocabulary error strings into user-facing copy.
  // Ordered by priority; first match wins.
  if (
    res.status === 422 ||
    serverMsg.toLowerCase().includes("encryption key") ||
    serverMsg.includes("ErrEncryptionKeyRequired")
  ) {
    return "Your API key can't be saved yet. This server isn't fully configured — contact your administrator to complete the setup. (Error: encryption key required)";
  }
  if (res.status >= 500) {
    return "Something went wrong on the server. Try again, or check the server logs.";
  }
  if (
    serverMsg.toLowerCase().includes("validation") ||
    serverMsg.toLowerCase().includes("must be") ||
    serverMsg.toLowerCase().includes("invalid format")
  ) {
    return "The API key format looks wrong. Check that you've copied the full key from your provider's dashboard.";
  }
  if (text.trimStart().startsWith("<")) {
    return `Server error (HTTP ${res.status}). The API may be restarting — try again in a moment.`;
  }
  return serverMsg || `HTTP ${res.status}`;
}

const RUNBOOK_PATH = "/docs/admin-runbooks/llm-config.md";

export default function AdminLLMPage() {
  const [profiles, setProfiles] = useState<ProfileResponse[] | null>(null);
  const [activeProfileMissing, setActiveProfileMissing] = useState(false);
  // r1 H2: encryption key presence flag from the LIST response.
  // Initialise to true so existing sessions don't flash State 1 before
  // the first fetch completes.
  const [encryptionKeySet, setEncryptionKeySet] = useState(true);
  const [legacyConfig, setLegacyConfig] = useState<LegacyLLMConfigResponse | null>(null);
  const [workerAddr, setWorkerAddr] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [emptyRetryAttempts, setEmptyRetryAttempts] = useState(0);

  // Editor target — the profile selected in the editor below the list.
  // In N=1 mode this is just the single profile. In N>=2 mode the user
  // picks via [Edit] on a row.
  const [selectedProfileId, setSelectedProfileId] = useState<string | null>(null);

  // Switch-profile dialog state.
  const [switchDialog, setSwitchDialog] = useState<{
    fromName: string;
    toId: string;
    toName: string;
  } | null>(null);

  // 409 target_no_longer_active conflict state. Driven by the
  // CustomEvent dispatched from ProfileEditor (slice-1-flag #3).
  const [conflict, setConflict] = useState<ConflictState | null>(null);

  // Repair-picker for active_profile_missing.
  const [repairTargetId, setRepairTargetId] = useState<string>("");
  const [repairBusy, setRepairBusy] = useState(false);
  const [repairError, setRepairError] = useState<string | null>(null);

  // Last-saved breadcrumb in the header.
  const [lastSavedAt, setLastSavedAt] = useState<number | null>(null);
  const [, setTick] = useState(0);
  const emptyRetryTimerRef = useRef<number | null>(null);

  const fetchProfiles = useCallback(async (): Promise<ListProfilesResponse | null> => {
    const res = await authFetch("/api/v1/admin/llm-profiles");
    if (!res.ok) {
      throw new Error(await handleApiError(res));
    }
    return (await res.json()) as ListProfilesResponse;
  }, []);

  const fetchLegacyConfig = useCallback(async (): Promise<LegacyLLMConfigResponse | null> => {
    const res = await authFetch("/api/v1/admin/llm-config");
    if (!res.ok) return null;
    return (await res.json()) as LegacyLLMConfigResponse;
  }, []);

  const fetchWorker = useCallback(async (): Promise<string | null> => {
    try {
      const res = await authFetch("/api/v1/admin/config");
      if (!res.ok) return null;
      const wk = await res.json();
      return wk.worker?.address ?? null;
    } catch {
      return null;
    }
  }, []);

  const loadAll = useCallback(async () => {
    setLoadError(null);
    try {
      const [list, legacy, worker] = await Promise.all([
        fetchProfiles(),
        fetchLegacyConfig(),
        fetchWorker(),
      ]);
      if (list) {
        setProfiles(list.profiles);
        // CA-260: older API replicas may omit `active_profile_missing`
        // entirely. Default to false so the repair banner stays hidden
        // rather than silently flickering on.
        setActiveProfileMissing(list.active_profile_missing ?? false);
        // Default to true when the field is absent (older API replica).
        setEncryptionKeySet(list.encryption_key_set ?? true);
        // Auto-select an editor target. Prefer the active profile;
        // otherwise the first profile in the list. If the user already
        // picked a profile via [Edit], keep that selection if it's
        // still in the list.
        setSelectedProfileId((prev) => {
          if (prev && list.profiles.some((p) => p.id === prev)) return prev;
          const active = list.profiles.find((p) => p.is_active);
          if (active) return active.id;
          return list.profiles[0]?.id ?? null;
        });
        // Pre-fill the repair picker with the first profile if needed.
        if ((list.active_profile_missing ?? false) && list.profiles.length > 0) {
          setRepairTargetId((prev) => prev || list.profiles[0].id);
        }
      }
      if (legacy) {
        setLegacyConfig(legacy);
      }
      setWorkerAddr(worker);
    } catch (e) {
      setLoadError((e as Error).message);
    }
  }, [fetchProfiles, fetchLegacyConfig, fetchWorker]);

  useEffect(() => {
    loadAll();
  }, [loadAll]);

  // Empty-state safety net — codex-H1 guarantees the migration eagerly
  // seeds Default at boot, so this branch should virtually never fire
  // in practice. If it does, the migration is in flight or a partial
  // failure left zero profiles. We auto-retry once after 1.5s; if
  // still empty we surface a typed runbook-pointing banner.
  useEffect(() => {
    if (profiles && profiles.length === 0 && !activeProfileMissing && emptyRetryAttempts < 1) {
      if (emptyRetryTimerRef.current) {
        window.clearTimeout(emptyRetryTimerRef.current);
      }
      emptyRetryTimerRef.current = window.setTimeout(() => {
        setEmptyRetryAttempts((n) => n + 1);
        loadAll();
      }, 1500);
    }
    return () => {
      if (emptyRetryTimerRef.current) {
        window.clearTimeout(emptyRetryTimerRef.current);
        emptyRetryTimerRef.current = null;
      }
    };
  }, [profiles, activeProfileMissing, emptyRetryAttempts, loadAll]);

  // Re-render every 30s so "saved Xm ago" stays fresh.
  useEffect(() => {
    if (!lastSavedAt) return;
    const id = setInterval(() => setTick((t) => t + 1), 30_000);
    return () => clearInterval(id);
  }, [lastSavedAt]);

  // Slice-1-flag #3: listen for the editor's CustomEvent so we can
  // open the panel-scoped 409 conflict resolution UI.
  useEffect(() => {
    const handler = (ev: Event) => {
      const ce = ev as CustomEvent<{ profileId: string; hint: string }>;
      const targetId = ce.detail?.profileId ?? "";
      const targetProfile = profiles?.find((p) => p.id === targetId);
      setConflict({
        targetProfileId: targetId,
        targetProfileName: targetProfile?.name ?? targetId,
        hint:
          ce.detail?.hint ??
          "Another writer activated a different profile during your edit.",
      });
      // Refresh the list so the user sees the now-active profile
      // immediately, and the repair-picker / dropdowns reflect truth.
      loadAll();
    };
    window.addEventListener("sourcebridge:profile-target-no-longer-active", handler);
    return () => {
      window.removeEventListener("sourcebridge:profile-target-no-longer-active", handler);
    };
  }, [profiles, loadAll]);

  const profileCount = profiles?.length ?? 0;
  const activeProfile = profiles?.find((p) => p.is_active) ?? null;
  const selectedProfile = profiles?.find((p) => p.id === selectedProfileId) ?? null;

  const handleAddProfile = useCallback(async () => {
    // Create a new profile with sensible defaults. The user lands in
    // the editor below to fill it in. We don't pop a modal — the page
    // already has the right shape for editing one profile.
    const seedName = computeSeedName(profiles ?? []);
    try {
      const res = await authFetch("/api/v1/admin/llm-profiles", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: seedName,
          provider: activeProfile?.provider ?? "ollama",
          base_url: activeProfile?.base_url ?? "http://localhost:11434",
          summary_model: "",
          review_model: "",
          ask_model: "",
          knowledge_model: "",
          architecture_diagram_model: "",
          draft_model: "",
          timeout_secs: 900,
          advanced_mode: false,
        }),
      });
      if (!res.ok) {
        throw new Error(await handleApiError(res));
      }
      const data = (await res.json()) as { id: string };
      // Refetch so the new profile shows up; then select it for edit.
      await loadAll();
      setSelectedProfileId(data.id);
    } catch (e) {
      setLoadError((e as Error).message);
    }
  }, [profiles, activeProfile, loadAll]);

  const handleSwitchRequested = useCallback(
    (toId: string) => {
      const target = profiles?.find((p) => p.id === toId);
      const fromName = activeProfile?.name ?? "current profile";
      if (!target) return;
      setSwitchDialog({
        fromName,
        toId,
        toName: target.name,
      });
    },
    [profiles, activeProfile],
  );

  const handleRepair = useCallback(async () => {
    if (!repairTargetId) return;
    setRepairBusy(true);
    setRepairError(null);
    try {
      const idPath = encodeURIComponent(repairTargetId);
      const res = await authFetch(`/api/v1/admin/llm-profiles/${idPath}/activate`, {
        method: "POST",
      });
      if (!res.ok) throw new Error(await handleApiError(res));
      await loadAll();
    } catch (e) {
      setRepairError((e as Error).message);
    } finally {
      setRepairBusy(false);
    }
  }, [repairTargetId, loadAll]);

  // 409 conflict resolution actions:
  const conflictRetryOnNowActive = useCallback(() => {
    // Switch the editor target to the currently-active profile (which
    // is now the "winner"), so the user can re-paste their edits if
    // they want them on the now-active profile.
    if (activeProfile) {
      setSelectedProfileId(activeProfile.id);
    }
    setConflict(null);
  }, [activeProfile]);

  const conflictKeepOriginalTarget = useCallback(() => {
    // Keep the editor on the same profile (the user's intended target),
    // which is now NON-active. Subsequent saves go through the
    // non-active write path (which is allowed) so the user's edits land
    // on the originally-targeted profile even though it's no longer
    // active. The user can re-activate later if they want.
    if (conflict) {
      setSelectedProfileId(conflict.targetProfileId);
    }
    setConflict(null);
  }, [conflict]);

  const conflictDiscard = useCallback(() => {
    // Trigger a hydrate-from-server: changing selectedProfileId resets
    // the editor's local state from the (now-fresh) profile prop. The
    // simplest way is to bounce the selection.
    if (selectedProfileId) {
      const id = selectedProfileId;
      setSelectedProfileId(null);
      setTimeout(() => setSelectedProfileId(id), 0);
    }
    setConflict(null);
  }, [selectedProfileId]);

  // Loading shell.
  if (profiles === null && loadError === null) {
    return (
      <PageFrame>
        <PageHeader eyebrow="Admin" title="LLM configuration" />
        <Panel>
          <p className="text-sm text-[var(--text-secondary)]">Loading…</p>
        </Panel>
      </PageFrame>
    );
  }

  if (loadError) {
    return (
      <PageFrame>
        <PageHeader eyebrow="Admin" title="LLM configuration" />
        <Panel>
          <p className="text-sm text-[var(--color-error,#ef4444)]">
            Could not load LLM configuration: {loadError}
          </p>
          <div className="mt-3">
            <Button variant="secondary" size="sm" onClick={loadAll}>
              Retry
            </Button>
          </div>
        </Panel>
      </PageFrame>
    );
  }

  // Empty-state safety net (post one auto-retry).
  if (profiles && profiles.length === 0) {
    if (emptyRetryAttempts < 1) {
      return (
        <PageFrame>
          <PageHeader eyebrow="Admin" title="LLM configuration" />
          <Panel>
            <p className="text-sm text-[var(--text-secondary)]" data-testid="empty-loading-hint">
              Loading… migration may still be running. Refreshing automatically.
            </p>
          </Panel>
        </PageFrame>
      );
    }
    return (
      <PageFrame>
        <PageHeader eyebrow="Admin" title="LLM configuration" />
        <Panel>
          <div role="alert" data-testid="empty-runbook-banner" className="space-y-2">
            <p className="text-sm font-medium text-[var(--color-error,#ef4444)]">
              No LLM profiles found. The boot-time migration may have failed or hasn&apos;t run yet.
            </p>
            <p className="text-sm text-[var(--text-secondary)]">
              See the{" "}
              <a
                href={RUNBOOK_PATH}
                className="underline hover:text-[var(--text-primary)]"
                target="_blank"
                rel="noreferrer"
              >
                LLM-config admin runbook
              </a>{" "}
              for recovery steps. You can also retry the page load.
            </p>
            <div>
              <Button variant="secondary" size="sm" onClick={loadAll}>
                Retry
              </Button>
            </div>
          </div>
        </Panel>
      </PageFrame>
    );
  }

  const isMultiProfileMode = profileCount >= 2;
  const editorDisabled = activeProfileMissing;

  // Derive the "you're editing the 'X' profile" pill name. Slice-1-flag
  // #2: legacy GET response provides active_profile_name. We use that
  // for the N=1 header pill so it stays correct on a non-N=1 transition.
  const headerActiveProfileName =
    legacyConfig?.active_profile_name || activeProfile?.name || profiles?.[0]?.name || "Default";

  return (
    <PageFrame>
      <PageHeader
        eyebrow="Admin"
        title="LLM configuration"
        description="Provider, model, and per-operation overrides for code analysis, review, and chat."
        actions={
          <div className="flex flex-wrap items-center gap-2">
            {isMultiProfileMode && activeProfile ? (
              // N≥2: green Active pill showing the active profile name.
              // r1 L5: text color #16a34a (green-700) gives ≥4.5:1 contrast
              // on the rgba(34,197,94,0.1) background; replaces the former
              // #22c55e which failed WCAG AA at ~3.1:1.
              <span
                data-testid="header-active-pill"
                className="inline-flex items-center gap-1.5 rounded-full border border-[var(--color-success,#22c55e)] bg-[rgba(34,197,94,0.1)] px-2.5 py-1 text-xs font-medium text-[#16a34a]"
              >
                Active: {activeProfile.name}
              </span>
            ) : !isMultiProfileMode && profiles && profiles[0] ? (
              // N=1: show the ProfileNamePill (click-to-rename) AND an Active
              // pill whose color encodes functional completeness (r1 H5).
              <>
                <ProfileNamePill
                  profileId={profiles[0].id}
                  currentName={headerActiveProfileName}
                  onRenamed={loadAll}
                  disabled={editorDisabled}
                  testIdPrefix="header-profile-pill"
                />
                {(() => {
                  const p = profiles[0];
                  const isComplete =
                    isLocalProvider(p.provider) || (p.api_key_set && encryptionKeySet);
                  return isComplete ? (
                    // r1 L5: same contrast fix as the N≥2 pill above.
                    <span
                      data-testid="header-active-pill-single-complete"
                      className="inline-flex items-center rounded-full border border-[var(--color-success,#22c55e)] bg-[rgba(34,197,94,0.1)] px-2.5 py-1 text-xs font-medium text-[#16a34a]"
                    >
                      Active
                    </span>
                  ) : (
                    <span
                      data-testid="header-active-pill-single-incomplete"
                      className="inline-flex items-center rounded-full border border-[var(--border-default)] bg-[var(--bg-subtle)] px-2.5 py-1 text-xs font-medium text-[var(--text-secondary)]"
                    >
                      Active (not configured)
                    </span>
                  );
                })()}
              </>
            ) : null}
            {!isMultiProfileMode && (
              <Button
                variant="secondary"
                size="sm"
                onClick={handleAddProfile}
                disabled={editorDisabled}
                data-testid="header-add-profile"
              >
                + Add profile
              </Button>
            )}
            {lastSavedAt && (
              <span className="text-xs text-[var(--text-tertiary)]">
                Saved {formatRelativeSaved(lastSavedAt)}
              </span>
            )}
          </div>
        }
      />

      {/* Repair banner — slice-1-flag #1: gated only by active_profile_missing
          from the LIST response, never recomputed client-side. */}
      {activeProfileMissing && (
        <Panel className="mb-4 border-[var(--color-error,#ef4444)]" data-testid="repair-banner">
          <div className="space-y-3">
            <div>
              <h3 className="text-base font-semibold text-[var(--color-error,#ef4444)]">
                Active profile is missing
              </h3>
              <p className="mt-1 text-sm text-[var(--text-secondary)]">
                The workspace has profiles, but the active-profile pointer no longer matches any of them.
                Pick a profile to activate. Editing is disabled until you do.
              </p>
            </div>
            <div className="flex flex-wrap items-end gap-2">
              <label className="grid gap-1.5 text-sm">
                <span className="text-xs font-medium uppercase tracking-wide text-[var(--text-secondary)]">
                  Pick profile to activate
                </span>
                <select
                  value={repairTargetId}
                  onChange={(e) => setRepairTargetId(e.target.value)}
                  disabled={repairBusy}
                  data-testid="repair-picker"
                  className="h-11 min-w-[16rem] rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)]"
                >
                  {profiles?.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))}
                </select>
              </label>
              <Button
                variant="primary"
                size="md"
                onClick={handleRepair}
                disabled={repairBusy || !repairTargetId}
                data-testid="repair-activate"
              >
                {repairBusy ? "Activating…" : "Activate"}
              </Button>
            </div>
            {repairError ? (
              <div
                role="alert"
                className="rounded-[var(--control-radius)] border border-[var(--color-error,#ef4444)] bg-[color:var(--color-error-subtle,rgba(239,68,68,0.08))] px-3 py-2 text-xs text-[var(--color-error,#ef4444)]"
              >
                {repairError}
              </div>
            ) : null}
          </div>
        </Panel>
      )}

      {/* 409 conflict resolution panel — slice-1-flag #3. Panel-scoped, not
          a toast (UX intake §6: error states for destructive ops are
          panel-scoped so the user has time to read and decide). */}
      {conflict && (
        <Panel className="mb-4 border-amber-400" data-testid="conflict-banner">
          <div className="space-y-3">
            <div>
              <h3 className="text-base font-semibold text-amber-400">
                Profile &quot;{conflict.targetProfileName}&quot; is no longer active
              </h3>
              <p className="mt-1 text-sm text-[var(--text-secondary)]">
                Your edits were not saved. {conflict.hint} Pick how to proceed:
              </p>
            </div>
            <div className="flex flex-wrap gap-2">
              <Button
                variant="primary"
                size="sm"
                onClick={conflictRetryOnNowActive}
                disabled={!activeProfile}
                data-testid="conflict-retry-active"
              >
                Edit the now-active profile{activeProfile ? ` (${activeProfile.name})` : ""}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={conflictKeepOriginalTarget}
                data-testid="conflict-keep-target"
              >
                Keep editing &quot;{conflict.targetProfileName}&quot; (now non-active)
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={conflictDiscard}
                data-testid="conflict-discard"
              >
                Discard edits
              </Button>
            </div>
          </div>
        </Panel>
      )}

      {/* List rendered only when N>=2 (progressive disclosure). */}
      {isMultiProfileMode && profiles && (
        <Panel className="mb-4">
          <ProfileList
            profiles={profiles}
            selectedProfileId={selectedProfileId ?? ""}
            disabled={editorDisabled}
            onSelectForEdit={(id) => setSelectedProfileId(id)}
            onActivateRequested={handleSwitchRequested}
            onAddProfile={handleAddProfile}
            onDeleted={loadAll}
            onDuplicated={(id) => {
              loadAll();
              setSelectedProfileId(id);
            }}
          />
        </Panel>
      )}

      {/* r1 H6 — onboarding banner (N=1 only; 4 states; L4 aria roles).
          State precedence: 1 > 2 > 3 > 4.
          State 4 = no banner. The banner is informational (role=status)
          and non-intrusive (aria-live=polite). Gated on profileCount === 1
          strictly so N>=2 is never affected. Also suppressed while the
          repair banner is active (repair wins visually). */}
      {profileCount === 1 && !activeProfileMissing && selectedProfile && (() => {
        const p = selectedProfile;
        const isLocal = isLocalProvider(p.provider);
        const hasProvider = p.provider !== "";
        // State 1: encryption key absent + non-local or no provider.
        if (!encryptionKeySet && (!isLocal || !hasProvider)) {
          return (
            <Panel
              className="mb-4 border-[var(--color-warning,#f59e0b)]"
              data-testid="onboarding-banner-state1"
              role="status"
              aria-live="polite"
            >
              <p className="text-sm text-[var(--text-primary)]">
                Your LLM profile isn&apos;t ready to use yet. This server needs an encryption key
                before it can store cloud API keys. Ask your administrator to set{" "}
                <code className="rounded bg-[var(--bg-subtle)] px-1 font-mono text-xs">
                  SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY
                </code>{" "}
                and restart the server. If you&apos;re the administrator, see the{" "}
                <a
                  href="/docs/admin-runbooks/llm-config.md"
                  className="underline hover:text-[var(--text-primary)]"
                  target="_blank"
                  rel="noreferrer"
                >
                  LLM configuration guide
                </a>
                . In the meantime, you can switch to Ollama below — it runs locally and
                doesn&apos;t need an API key or an encryption key.
              </p>
            </Panel>
          );
        }
        // State 2: key set + (no provider, OR cloud with no key).
        if (encryptionKeySet && (!hasProvider || (!isLocal && !p.api_key_set))) {
          return (
            <Panel
              className="mb-4 border-[var(--color-info,#3b82f6)]"
              data-testid="onboarding-banner-state2"
              role="status"
              aria-live="polite"
            >
              <p className="text-sm text-[var(--text-primary)]">
                Choose a provider to enable AI features. Ollama runs on your machine and
                requires no API key. Anthropic and OpenAI are cloud providers that require an
                API key. Pick one below, fill in your credentials, and save.
              </p>
            </Panel>
          );
        }
        // State 3: key set + local provider + no model configured.
        if (encryptionKeySet && isLocal && !p.summary_model) {
          return (
            <Panel
              className="mb-4 border-[var(--color-info,#3b82f6)]"
              data-testid="onboarding-banner-state3"
              role="status"
              aria-live="polite"
            >
              <p className="text-sm text-[var(--text-primary)]">
                Almost there — set a model name below. Ollama is configured but no model is
                selected. Specify a model (e.g.{" "}
                <code className="rounded bg-[var(--bg-subtle)] px-1 font-mono text-xs">
                  llama3.2
                </code>
                ) and save.
              </p>
            </Panel>
          );
        }
        // State 4: fully configured — no banner.
        return null;
      })()}

      {/* Editor — always present unless we hit empty-state. In N=1 mode
          this looks exactly like today's /admin/llm grid. In N>=2 mode
          it picks up the "Profile name" + "Make active" affordances. */}
      {selectedProfile && (
        <Panel
          className="mb-4"
          data-testid={isMultiProfileMode ? "editor-panel-multi" : "editor-panel-single"}
        >
          {(isMultiProfileMode || profileCount === 1) && (
            <div className="mb-3 text-sm text-[var(--text-secondary)]">
              <span className="font-medium text-[var(--text-primary)]">
                {isMultiProfileMode ? `Editing: ${selectedProfile.name}` : selectedProfile.name}
              </span>
              {selectedProfile.is_active && (() => {
                const isComplete =
                  isLocalProvider(selectedProfile.provider) ||
                  (selectedProfile.api_key_set && encryptionKeySet);
                return isComplete ? (
                  // r1 L5: #16a34a for WCAG AA compliance on rgba bg.
                  <span className="ml-2 inline-flex items-center rounded-full border border-[var(--color-success,#22c55e)] bg-[rgba(34,197,94,0.1)] px-2 py-0.5 text-xs font-medium text-[#16a34a]">
                    Active
                  </span>
                ) : (
                  <span className="ml-2 inline-flex items-center rounded-full border border-[var(--border-default)] bg-[var(--bg-subtle)] px-2 py-0.5 text-xs font-medium text-[var(--text-secondary)]">
                    Active (not configured)
                  </span>
                );
              })()}
            </div>
          )}
          <ProfileEditor
            profile={selectedProfile}
            multiProfileMode={isMultiProfileMode}
            disabled={editorDisabled}
            onSaved={() => {
              setLastSavedAt(Date.now());
              loadAll();
            }}
            onActivateRequested={() => handleSwitchRequested(selectedProfile.id)}
            testIdPrefix={isMultiProfileMode ? "editor-multi" : "editor-single"}
          />
        </Panel>
      )}

      {selectedProfile && isLocalProvider(selectedProfile.provider) && (
        <Panel className="mb-4">
          <h3 className="mb-2 text-base font-semibold text-[var(--text-primary)]">
            Speculative Decoding
          </h3>
          <p className="text-sm text-[var(--text-secondary)]">
            {selectedProfile.provider === "lmstudio"
              ? "LM Studio supports per-request speculative decoding via the Draft Model field above. Configure a smaller draft model for 1.5-3x throughput improvement."
              : selectedProfile.provider === "llama-cpp"
                ? "llama.cpp supports speculative decoding when launched with --model-draft. Performance metrics (tokens/sec, acceptance rate) appear in operation results."
                : selectedProfile.provider === "sglang"
                  ? "SGLang supports EAGLE-based speculative decoding configured at server launch. Performance metrics appear in operation results."
                  : selectedProfile.provider === "vllm"
                    ? "vLLM supports EAGLE3 speculative decoding configured at server launch. Performance metrics appear in operation results."
                    : "Performance metrics (tokens/sec) from your local inference server appear in operation results when available."}
          </p>
          <p className="mt-1 text-xs text-[var(--text-secondary)]">
            Tip: Higher tokens/sec indicates speculative decoding is working. Acceptance rate below 60% means
            the draft model is a poor match for the target model.
          </p>
        </Panel>
      )}

      {workerAddr && (
        <Panel>
          <h3 className="mb-2 text-base font-semibold text-[var(--text-primary)]">Worker Connection</h3>
          <div className="text-sm">
            <span className="text-[var(--text-secondary)]">Worker Address: </span>
            <span className="font-mono text-[var(--text-primary)]">{workerAddr}</span>
          </div>
          <p className="mt-2 text-xs text-[var(--text-secondary)]">
            The Python worker handles LLM calls. Worker address is configured via
            SOURCEBRIDGE_WORKER_GRPC_ADDRESS environment variable.
          </p>
        </Panel>
      )}

      {switchDialog && (
        <SwitchProfileDialog
          open
          fromProfileName={switchDialog.fromName}
          toProfileId={switchDialog.toId}
          toProfileName={switchDialog.toName}
          onClose={() => setSwitchDialog(null)}
          onActivated={() => {
            setSwitchDialog(null);
            loadAll();
          }}
        />
      )}
    </PageFrame>
  );
}

function isLocalProvider(provider: string): boolean {
  return ["ollama", "vllm", "llama-cpp", "sglang", "lmstudio"].includes(provider);
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

// Picks a fresh seed name like "Profile 2", "Profile 3", etc. that
// doesn't collide with existing names (case-insensitive). Slice 2's
// `+ Add profile` lands the user in the editor immediately, so the
// name is editable before the first Save.
function computeSeedName(profiles: ProfileResponse[]): string {
  const taken = new Set(profiles.map((p) => p.name.toLowerCase().trim()));
  let n = profiles.length + 1;
  for (let i = 0; i < 100; i++) {
    const candidate = `Profile ${n}`;
    if (!taken.has(candidate.toLowerCase())) return candidate;
    n++;
  }
  return `Profile ${Date.now()}`;
}
