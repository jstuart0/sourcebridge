"use client";

/**
 * RepositoryLLMOverrideSection — collapsed-by-default per-repository
 * LLM override form. Lives below the per-repo wiki-settings form on the
 * repository detail page.
 *
 * Slice 3 of the LLM provider profiles plan reshapes this from a
 * single-mode override to a THREE-MODE radio:
 *
 *   1. Workspace (default for new repos): inherit the workspace's
 *      active profile. No override row is persisted.
 *
 *   2. Saved profile: point at a workspace profile by id. The dropdown
 *      lists every profile from GET /admin/llm-profiles; the active
 *      one is marked with the same "Active" pill style as the admin
 *      page (slice 2). Selecting a profile saves
 *      { profileId } via setRepositoryLLMOverride; the inline fields
 *      below are atomically cleared server-side.
 *
 *   3. Override fields (today's behavior, expanded): per-field
 *      inline override. Save sends { clearProfile: true,
 *      provider, baseURL, apiKey, ...models } so the saved row is
 *      inline-only.
 *
 * Mode switching is non-destructive: toggling between modes preserves
 * the values in each mode. Switching from "Saved profile" to
 * "Override fields" doesn't lose what the user typed previously.
 *
 * Conflict handling (PROFILE_NO_LONGER_EXISTS): if the saved
 * profileId references a deleted profile, the page-level GraphQL
 * field resolver returns the override + a non-fatal error with
 * extensions.code = "PROFILE_NO_LONGER_EXISTS". The override prop's
 * `profileMissing` flag (set by the parent on that error) drives an
 * inline resolution panel, mirroring the slice-2 409 panel pattern.
 *
 * Empty-key UX semantics (inline mode):
 *   - Leaving the API Key field blank → "leave the saved cipher
 *     alone" (the resolver treats empty-string as omitted). To clear
 *     the saved key back to workspace inheritance, the user toggles
 *     the "Clear API key" checkbox.
 *   - Other text fields use empty-string-clears semantics.
 */

import { useEffect, useMemo, useState } from "react";
import { useMutation } from "urql";

import { Button } from "@/components/ui/button";
import { authFetch } from "@/lib/auth-fetch";
import { cn } from "@/lib/utils";
import type { ProfileResponse, ListProfilesResponse } from "@/lib/llm/profile";
import {
  SET_REPOSITORY_LLM_OVERRIDE_MUTATION,
  CLEAR_REPOSITORY_LLM_OVERRIDE_MUTATION,
} from "@/lib/graphql/queries";

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

export interface RepositoryLLMOverride {
  /** Slice 3: when set, the override is in "saved profile" mode. */
  profileId?: string | null;
  /** Slice 3: read-only convenience; null when profileId is null OR when the referenced profile no longer exists. */
  profileName?: string | null;
  provider?: string | null;
  baseURL?: string | null;
  apiKeySet: boolean;
  apiKeyHint?: string | null;
  advancedMode: boolean;
  summaryModel?: string | null;
  reviewModel?: string | null;
  askModel?: string | null;
  knowledgeModel?: string | null;
  architectureDiagramModel?: string | null;
  reportModel?: string | null;
  draftModel?: string | null;
  updatedAt?: string | null;
  updatedBy?: string | null;
}

export interface RepositoryLLMOverrideSectionProps {
  repoId: string;
  /** Current saved override (null when no override is set, i.e., workspace mode). */
  override: RepositoryLLMOverride | null;
  /**
   * When true, the saved override references a profile that has been
   * deleted (PROFILE_NO_LONGER_EXISTS surfaced from the GraphQL field
   * resolver). The component renders the resolution panel and blocks
   * Save on the stale profileId until the user picks another, switches
   * modes, or reverts to workspace.
   */
  profileMissing?: boolean;
  /** Called after a successful save/clear so the parent can re-render. */
  onSaved?: (next: RepositoryLLMOverride | null) => void;
  /** When true, the panel is enterprise edition (shows reportModel field). */
  isEnterprise?: boolean;
}

// Provider list mirrors /admin/llm exactly.
const PROVIDER_OPTIONS = [
  { value: "", label: "(leave inherited from workspace)" },
  { value: "ollama", label: "Ollama (Local)" },
  { value: "openai", label: "OpenAI" },
  { value: "anthropic", label: "Anthropic" },
  { value: "vllm", label: "vLLM (Local)" },
  { value: "llama-cpp", label: "llama.cpp (Local)" },
  { value: "sglang", label: "SGLang (Local)" },
  { value: "lmstudio", label: "LM Studio (Local)" },
  { value: "gemini", label: "Google Gemini" },
  { value: "openrouter", label: "OpenRouter" },
];

const PROVIDERS_NEEDING_KEY = new Set(["anthropic", "openai", "gemini", "openrouter"]);

type Mode = "workspace" | "profile" | "inline";

/**
 * Decide the initial mode from the saved override row.
 *  - null  → workspace
 *  - profileId set → profile
 *  - otherwise → inline (any inline fields populated)
 */
function deriveInitialMode(ov: RepositoryLLMOverride | null): Mode {
  if (!ov) return "workspace";
  if (ov.profileId) return "profile";
  return "inline";
}

// ─────────────────────────────────────────────────────────────────────────────
// Component
// ─────────────────────────────────────────────────────────────────────────────

export function RepositoryLLMOverrideSection({
  repoId,
  override,
  profileMissing = false,
  onSaved,
  isEnterprise = false,
}: RepositoryLLMOverrideSectionProps) {
  const initial = override;

  // Mode state. Initialized from the saved override; user toggles
  // between the three modes via the radio. Mode switching is
  // non-destructive — see inline state below for the per-mode values
  // that survive mode toggles.
  const [mode, setMode] = useState<Mode>(deriveInitialMode(initial));

  // Profile-mode state.
  const [selectedProfileId, setSelectedProfileId] = useState<string>(
    initial?.profileId ?? ""
  );
  const [profilesList, setProfilesList] = useState<ProfileResponse[]>([]);
  const [profilesLoading, setProfilesLoading] = useState(false);
  const [profilesError, setProfilesError] = useState<string | null>(null);
  const [activeProfileMissingFlag, setActiveProfileMissingFlag] = useState(false);

  // Inline-mode state. Initialized from the saved override (when in
  // inline mode) OR left blank otherwise. We don't drop these on mode
  // toggle, so the user can switch to "Saved profile" and back without
  // re-typing.
  const [provider, setProvider] = useState(initial?.provider ?? "");
  const [baseURL, setBaseURL] = useState(initial?.baseURL ?? "");
  const [apiKey, setApiKey] = useState(""); // password field; never pre-populated
  const [clearAPIKey, setClearAPIKey] = useState(false);
  const [advancedMode, setAdvancedMode] = useState(initial?.advancedMode ?? false);
  const [summaryModel, setSummaryModel] = useState(initial?.summaryModel ?? "");
  const [reviewModel, setReviewModel] = useState(initial?.reviewModel ?? "");
  const [askModel, setAskModel] = useState(initial?.askModel ?? "");
  const [knowledgeModel, setKnowledgeModel] = useState(initial?.knowledgeModel ?? "");
  const [architectureDiagramModel, setArchitectureDiagramModel] = useState(
    initial?.architectureDiagramModel ?? ""
  );
  const [reportModel, setReportModel] = useState(initial?.reportModel ?? "");
  const [draftModel, setDraftModel] = useState(initial?.draftModel ?? "");

  // Sync form state when the override prop changes (parent's GraphQL
  // query resolves async; without this sync the form mounts with a
  // null initial and stays blank even after the saved override loads).
  // Mode is also re-synced — but only when the prop transitions. The
  // user's in-flight mode toggle survives a no-op rerender.
  useEffect(() => {
    setMode(deriveInitialMode(initial));
    setSelectedProfileId(initial?.profileId ?? "");
    setProvider(initial?.provider ?? "");
    setBaseURL(initial?.baseURL ?? "");
    setAdvancedMode(initial?.advancedMode ?? false);
    setSummaryModel(initial?.summaryModel ?? "");
    setReviewModel(initial?.reviewModel ?? "");
    setAskModel(initial?.askModel ?? "");
    setKnowledgeModel(initial?.knowledgeModel ?? "");
    setArchitectureDiagramModel(initial?.architectureDiagramModel ?? "");
    setReportModel(initial?.reportModel ?? "");
    setDraftModel(initial?.draftModel ?? "");
  }, [
    initial?.profileId,
    initial?.provider,
    initial?.baseURL,
    initial?.advancedMode,
    initial?.summaryModel,
    initial?.reviewModel,
    initial?.askModel,
    initial?.knowledgeModel,
    initial?.architectureDiagramModel,
    initial?.reportModel,
    initial?.draftModel,
    initial,
  ]);

  // Lazy-load the profile list when the user opens the section AND
  // the profile mode is reachable. The list is small (typically
  // single-digit profiles) and the fetch is cheap.
  const fetchProfiles = async () => {
    if (profilesLoading) return;
    setProfilesLoading(true);
    setProfilesError(null);
    try {
      const res = await authFetch("/api/v1/admin/llm-profiles");
      if (!res.ok) {
        throw new Error(`Could not load profiles (HTTP ${res.status})`);
      }
      const data = (await res.json()) as ListProfilesResponse;
      setProfilesList(data.profiles ?? []);
      setActiveProfileMissingFlag(data.active_profile_missing ?? false);
    } catch (e) {
      setProfilesError((e as Error).message);
    } finally {
      setProfilesLoading(false);
    }
  };

  // Load profiles on first mount (so the dropdown is ready when the
  // user opens the section). The fetch runs at most once unless the
  // user explicitly retries.
  useEffect(() => {
    void fetchProfiles();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const [saving, setSaving] = useState(false);
  const [errorMsg, setErrorMsg] = useState<string | null>(null);
  const [successMsg, setSuccessMsg] = useState<string | null>(null);

  const [, setMutation] = useMutation(SET_REPOSITORY_LLM_OVERRIDE_MUTATION);
  const [, clearMutation] = useMutation(CLEAR_REPOSITORY_LLM_OVERRIDE_MUTATION);

  // Saved-state hint summarizes what the saved override does so an
  // operator can confirm at a glance (visible while the section is
  // collapsed).
  const savedHint = useMemo(() => {
    if (!initial) return "Inheriting workspace LLM settings.";
    if (initial.profileId) {
      if (profileMissing) return "Override broken — referenced profile no longer exists.";
      return `Using saved profile: ${initial.profileName ?? initial.profileId}.`;
    }
    const parts: string[] = [];
    if (initial.provider) parts.push(`provider=${initial.provider}`);
    if (initial.apiKeySet) parts.push(`api_key=${initial.apiKeyHint ?? "set"}`);
    if (initial.advancedMode) {
      const models = [
        initial.summaryModel,
        initial.reviewModel,
        initial.askModel,
        initial.knowledgeModel,
        initial.architectureDiagramModel,
        initial.reportModel,
      ].filter(Boolean);
      if (models.length > 0) parts.push(`per-area models: ${models.length} set`);
    } else if (initial.summaryModel) {
      parts.push(`model=${initial.summaryModel}`);
    }
    if (parts.length === 0) return "Override row exists but no fields set; inheriting workspace LLM settings.";
    return `Override active — ${parts.join(", ")}.`;
  }, [initial, profileMissing]);

  const handleSaveProfile = async () => {
    if (!selectedProfileId) {
      setErrorMsg("Pick a profile from the dropdown first.");
      return;
    }
    setSaving(true);
    setErrorMsg(null);
    setSuccessMsg(null);

    const result = await setMutation({
      repositoryId: repoId,
      input: { profileId: selectedProfileId },
    });
    setSaving(false);

    if (result.error) {
      const gqlErr = result.error.graphQLErrors?.[0];
      const code = (gqlErr?.extensions as Record<string, unknown> | undefined)?.code;
      if (code === "PROFILE_NO_LONGER_EXISTS") {
        setErrorMsg(
          gqlErr?.message ??
            "The selected profile no longer exists. Pick another, switch modes, or revert to workspace."
        );
        // Refresh the profile list so the deleted entry disappears.
        void fetchProfiles();
      } else {
        setErrorMsg(result.error.message);
      }
      return;
    }

    setSuccessMsg("Per-repository override now uses the selected profile.");
    if (result.data?.setRepositoryLLMOverride) {
      onSaved?.(result.data.setRepositoryLLMOverride as RepositoryLLMOverride);
    }
  };

  const handleSaveInline = async () => {
    setSaving(true);
    setErrorMsg(null);
    setSuccessMsg(null);

    // Build the inline patch. Use empty-string-clears semantics for
    // non-secret fields. clearProfile:true is sent so a save while
    // currently in profile mode atomically swaps to inline mode.
    const input: Record<string, unknown> = {
      clearProfile: true,
      provider,
      baseURL,
      advancedMode,
      summaryModel,
    };
    if (advancedMode) {
      input.reviewModel = reviewModel;
      input.askModel = askModel;
      input.knowledgeModel = knowledgeModel;
      input.architectureDiagramModel = architectureDiagramModel;
      if (isEnterprise) input.reportModel = reportModel;
      input.draftModel = draftModel;
    }
    if (apiKey) input.apiKey = apiKey;
    if (clearAPIKey) input.clearAPIKey = true;

    const result = await setMutation({ repositoryId: repoId, input });
    setSaving(false);

    if (result.error) {
      const gqlErr = result.error.graphQLErrors?.[0];
      const code = (gqlErr?.extensions as Record<string, unknown> | undefined)?.code;
      if (code === "ENCRYPTION_KEY_REQUIRED") {
        setErrorMsg(
          gqlErr?.message ??
            "Server has no encryption key configured. Set SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY and retry."
        );
      } else {
        setErrorMsg(result.error.message);
      }
      return;
    }

    setSuccessMsg("Per-repository LLM override saved.");
    setApiKey("");
    setClearAPIKey(false);
    if (result.data?.setRepositoryLLMOverride) {
      onSaved?.(result.data.setRepositoryLLMOverride as RepositoryLLMOverride);
    }
  };

  const handleSaveWorkspace = async () => {
    setSaving(true);
    setErrorMsg(null);
    setSuccessMsg(null);
    const result = await clearMutation({ repositoryId: repoId });
    setSaving(false);
    if (result.error) {
      setErrorMsg(result.error.message);
      return;
    }
    setSuccessMsg("Per-repository LLM override cleared. Inheriting workspace LLM settings.");
    setApiKey("");
    setClearAPIKey(false);
    onSaved?.(null);
  };

  // Dispatch the right save handler based on the user's chosen mode.
  const handleSave = async () => {
    if (mode === "workspace") {
      await handleSaveWorkspace();
    } else if (mode === "profile") {
      await handleSaveProfile();
    } else {
      await handleSaveInline();
    }
  };

  // Visual styles matching the existing wiki-settings-panel + admin/llm.
  const labelClass = "block text-xs font-medium text-[var(--text-secondary)]";
  const helpClass = "mt-1 text-xs text-[var(--text-tertiary)]";
  const inputClass =
    "mt-1 h-10 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-focus)] disabled:opacity-60";
  const monoInputClass = `${inputClass} font-mono`;
  const fieldWrapperClass = "grid gap-1";

  const showAPIKeyField = !provider || PROVIDERS_NEEDING_KEY.has(provider);

  // Resolve the picked profile (for the read-only preview in profile
  // mode). Non-secret fields only — never the api_key_hint.
  const previewProfile = profilesList.find((p) => p.id === selectedProfileId);

  // Save button label varies by mode for clarity.
  const saveLabel = saving
    ? "Saving…"
    : mode === "workspace"
      ? "Save (clear override)"
      : mode === "profile"
        ? "Save profile selection"
        : "Save override";

  return (
    <details className="mt-4 rounded-[var(--control-radius)] border border-[var(--border-subtle)] bg-[var(--bg-base)]">
      <summary
        className="cursor-pointer select-none px-4 py-3 text-sm font-medium text-[var(--text-primary)] hover:bg-[var(--bg-hover)]"
        data-testid="repo-llm-override-summary"
      >
        Advanced: per-repository LLM override
        <span className="ml-2 text-xs font-normal text-[var(--text-tertiary)]">
          {savedHint}
        </span>
      </summary>

      <div className="space-y-4 border-t border-[var(--border-subtle)] p-4">
        <p className="text-xs text-[var(--text-secondary)]">
          Override the workspace LLM settings for this repository. Pick a saved
          profile from <a className="underline" href="/admin/llm">workspace settings</a>
          {" "}or set inline values. Applies to every LLM operation for this repo
          (summary, review, Q&amp;A, knowledge, architecture diagrams, reports).
        </p>

        {/* PROFILE_NO_LONGER_EXISTS resolution panel.
            Renders ONLY when the saved profileId references a deleted
            profile. Inline; the user picks a different profile, switches
            modes, or reverts to workspace. Mirrors slice-2's 409 panel
            UX (typed error → resolution panel with explicit actions). */}
        {profileMissing && initial?.profileId && (
          <div
            className="rounded-[var(--control-radius)] border border-[var(--color-warning,#f59e0b)] bg-[rgba(245,158,11,0.1)] p-3"
            role="alert"
            data-testid="repo-llm-override-profile-missing"
          >
            <p className="text-sm font-medium text-[var(--color-warning,#f59e0b)]">
              The profile this repository was using no longer exists.
            </p>
            <p className="mt-1 text-xs text-[var(--text-secondary)]">
              Saved profile id: <code className="font-mono">{initial.profileId}</code>.
              Pick another profile, switch to inline override, or revert to
              workspace inheritance.
            </p>
          </div>
        )}

        {/* Three-mode radio. */}
        <fieldset
          className="grid gap-3 rounded-[var(--control-radius)] border border-[var(--border-subtle)] p-3"
          data-testid="repo-llm-override-mode-radio"
        >
          <legend className="px-1 text-xs font-medium text-[var(--text-secondary)]">
            Override mode
          </legend>

          <label className="flex items-start gap-2 text-sm">
            <input
              type="radio"
              name="repo-llm-override-mode"
              value="workspace"
              checked={mode === "workspace"}
              onChange={() => setMode("workspace")}
              disabled={saving}
              className="mt-1"
              data-testid="repo-llm-override-mode-workspace"
            />
            <span>
              <span className="font-medium text-[var(--text-primary)]">
                Inherit workspace settings
              </span>
              <span className="block text-xs text-[var(--text-tertiary)]">
                Use whichever profile is active in /admin/llm. Recommended for
                most repos.
              </span>
            </span>
          </label>

          <label className="flex items-start gap-2 text-sm">
            <input
              type="radio"
              name="repo-llm-override-mode"
              value="profile"
              checked={mode === "profile"}
              onChange={() => setMode("profile")}
              disabled={saving}
              className="mt-1"
              data-testid="repo-llm-override-mode-profile"
            />
            <span>
              <span className="font-medium text-[var(--text-primary)]">
                Use a saved profile
              </span>
              <span className="block text-xs text-[var(--text-tertiary)]">
                Point this repo at a specific profile from /admin/llm. The
                api_key stays on the profile — you don&apos;t enter it here.
              </span>
            </span>
          </label>

          <label className="flex items-start gap-2 text-sm">
            <input
              type="radio"
              name="repo-llm-override-mode"
              value="inline"
              checked={mode === "inline"}
              onChange={() => setMode("inline")}
              disabled={saving}
              className="mt-1"
              data-testid="repo-llm-override-mode-inline"
            />
            <span>
              <span className="font-medium text-[var(--text-primary)]">
                Override fields
              </span>
              <span className="block text-xs text-[var(--text-tertiary)]">
                Set provider, API key, and per-area models inline for this
                repo. Use this for one-off setups that don&apos;t warrant a
                workspace profile.
              </span>
            </span>
          </label>
        </fieldset>

        {/* Mode 2: profile picker. */}
        {mode === "profile" && (
          <div
            className="space-y-3 rounded-[var(--control-radius)] border border-[var(--border-subtle)] bg-[var(--bg-raised)] p-3"
            data-testid="repo-llm-override-profile-panel"
          >
            <div className={fieldWrapperClass}>
              <label className={labelClass} htmlFor="repo-llm-profile-picker">
                Saved profile
              </label>
              <select
                id="repo-llm-profile-picker"
                value={selectedProfileId}
                onChange={(e) => setSelectedProfileId(e.target.value)}
                disabled={saving || profilesLoading}
                className={inputClass}
                data-testid="repo-llm-override-profile-picker"
              >
                <option value="">{profilesLoading ? "Loading…" : "Pick a profile"}</option>
                {profilesList.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                    {p.is_active ? " (Active)" : ""}
                  </option>
                ))}
              </select>
              {profilesError && (
                <p className="mt-1 text-xs text-[var(--color-error,#ef4444)]">
                  {profilesError}{" "}
                  <button
                    type="button"
                    className="underline"
                    onClick={() => void fetchProfiles()}
                  >
                    Retry
                  </button>
                </p>
              )}
              {activeProfileMissingFlag && !profilesError && (
                <p className="mt-1 text-xs text-[var(--color-warning,#f59e0b)]">
                  Workspace has no active profile right now. Picking one here
                  will work, but the workspace also needs repair on{" "}
                  <a className="underline" href="/admin/llm">/admin/llm</a>.
                </p>
              )}
            </div>

            {previewProfile && (
              <div
                className="grid gap-1 rounded-[var(--control-radius)] border border-[var(--border-subtle)] bg-[var(--bg-base)] p-3 text-xs text-[var(--text-secondary)]"
                data-testid="repo-llm-override-profile-preview"
              >
                <div>
                  <span className="font-medium text-[var(--text-primary)]">Provider:</span>{" "}
                  {previewProfile.provider || "(not set)"}
                </div>
                {previewProfile.base_url && (
                  <div>
                    <span className="font-medium text-[var(--text-primary)]">Base URL:</span>{" "}
                    <code className="font-mono">{previewProfile.base_url}</code>
                  </div>
                )}
                <div>
                  <span className="font-medium text-[var(--text-primary)]">API key:</span>{" "}
                  {previewProfile.api_key_set ? "configured" : "not set"}
                </div>
                {previewProfile.summary_model && (
                  <div>
                    <span className="font-medium text-[var(--text-primary)]">Model:</span>{" "}
                    <code className="font-mono">{previewProfile.summary_model}</code>
                    {previewProfile.advanced_mode && " (advanced mode — per-area models)"}
                  </div>
                )}
                {previewProfile.is_active && (
                  <div className="mt-1 inline-flex items-center gap-1 self-start rounded-full bg-[var(--accent-bg-soft,rgba(99,102,241,0.15))] px-2 py-0.5 text-[10px] font-medium text-[var(--accent-text,#818cf8)]">
                    Active workspace profile
                  </div>
                )}
              </div>
            )}
          </div>
        )}

        {/* Mode 3: inline override. */}
        {mode === "inline" && (
          <div
            className="space-y-4 rounded-[var(--control-radius)] border border-[var(--border-subtle)] bg-[var(--bg-raised)] p-3"
            data-testid="repo-llm-override-inline-panel"
          >
            <div className={fieldWrapperClass}>
              <label className={labelClass}>Provider</label>
              <select
                value={provider}
                onChange={(e) => setProvider(e.target.value)}
                disabled={saving}
                className={inputClass}
              >
                {PROVIDER_OPTIONS.map((opt) => (
                  <option key={opt.value} value={opt.value}>{opt.label}</option>
                ))}
              </select>
              <p className={helpClass}>
                Leave blank to use the workspace provider.
              </p>
            </div>

            <div className={fieldWrapperClass}>
              <label className={labelClass}>Base URL</label>
              <input
                type="text"
                value={baseURL}
                onChange={(e) => setBaseURL(e.target.value)}
                placeholder="(leave blank to inherit)"
                disabled={saving}
                className={monoInputClass}
              />
            </div>

            {showAPIKeyField && (
              <div className={fieldWrapperClass}>
                <label className={labelClass}>API Key</label>
                <input
                  type="password"
                  value={apiKey}
                  onChange={(e) => setApiKey(e.target.value)}
                  placeholder={
                    initial?.apiKeySet
                      ? `Saved (${initial.apiKeyHint ?? "configured"}). Type a new key to replace, or leave blank.`
                      : "Leave blank to inherit the workspace API key."
                  }
                  disabled={saving || clearAPIKey}
                  className={monoInputClass}
                />
                {initial?.apiKeySet && (
                  <label className="mt-2 flex items-center gap-2 text-xs text-[var(--text-secondary)]">
                    <input
                      type="checkbox"
                      checked={clearAPIKey}
                      onChange={(e) => setClearAPIKey(e.target.checked)}
                      disabled={saving}
                      className="h-3.5 w-3.5"
                    />
                    Clear saved API key (revert to workspace key)
                  </label>
                )}
              </div>
            )}

            <div className="flex items-center gap-3 rounded-[var(--control-radius)] border border-[var(--border-subtle)] p-3">
              <label className="relative inline-flex cursor-pointer items-center">
                <input
                  type="checkbox"
                  checked={advancedMode}
                  onChange={(e) => {
                    const next = e.target.checked;
                    setAdvancedMode(next);
                    if (!next) {
                      // Mirror /admin/llm: turning advanced-mode off resets all
                      // per-area fields to the summary model so a subsequent save
                      // doesn't keep stale values.
                      setReviewModel(summaryModel);
                      setAskModel(summaryModel);
                      setKnowledgeModel(summaryModel);
                      setArchitectureDiagramModel(summaryModel);
                      if (isEnterprise) setReportModel(summaryModel);
                    }
                  }}
                  disabled={saving}
                  className="peer sr-only"
                />
                <div className="peer h-5 w-9 rounded-full bg-[var(--border-default)] after:absolute after:left-[2px] after:top-[2px] after:h-4 after:w-4 after:rounded-full after:border after:border-gray-300 after:bg-white after:transition-all after:content-[''] peer-checked:bg-[hsl(var(--accent-hue,250),60%,60%)] peer-checked:after:translate-x-full peer-checked:after:border-white" />
              </label>
              <div>
                <span className="text-sm font-medium text-[var(--text-primary)]">
                  Advanced: Per-operation models
                </span>
                <p className="text-xs text-[var(--text-tertiary)]">
                  Use different models for different operations. Off uses the summary model for everything.
                </p>
              </div>
            </div>

            <div className={fieldWrapperClass}>
              <label className={labelClass}>
                Model {advancedMode && "(Analysis / Default)"}
              </label>
              <input
                type="text"
                value={summaryModel}
                onChange={(e) => setSummaryModel(e.target.value)}
                placeholder="(leave blank to inherit)"
                disabled={saving}
                className={monoInputClass}
              />
            </div>

            {advancedMode && (
              <div className="space-y-3 rounded-[var(--control-radius)] border border-[var(--border-subtle)] bg-[var(--bg-base)] p-3">
                <div className={fieldWrapperClass}>
                  <label className={labelClass}>Code Review</label>
                  <input
                    type="text"
                    value={reviewModel}
                    onChange={(e) => setReviewModel(e.target.value)}
                    placeholder="(blank inherits)"
                    disabled={saving}
                    className={monoInputClass}
                  />
                </div>
                <div className={fieldWrapperClass}>
                  <label className={labelClass}>Discussion &amp; Q&amp;A</label>
                  <input
                    type="text"
                    value={askModel}
                    onChange={(e) => setAskModel(e.target.value)}
                    placeholder="(blank inherits)"
                    disabled={saving}
                    className={monoInputClass}
                  />
                </div>
                <div className={fieldWrapperClass}>
                  <label className={labelClass}>Knowledge Generation</label>
                  <input
                    type="text"
                    value={knowledgeModel}
                    onChange={(e) => setKnowledgeModel(e.target.value)}
                    placeholder="(blank inherits)"
                    disabled={saving}
                    className={monoInputClass}
                  />
                </div>
                <div className={fieldWrapperClass}>
                  <label className={labelClass}>Architecture Diagrams</label>
                  <input
                    type="text"
                    value={architectureDiagramModel}
                    onChange={(e) => setArchitectureDiagramModel(e.target.value)}
                    placeholder="(blank inherits)"
                    disabled={saving}
                    className={monoInputClass}
                  />
                </div>
                {isEnterprise && (
                  <div className={fieldWrapperClass}>
                    <label className={labelClass}>Reports</label>
                    <input
                      type="text"
                      value={reportModel}
                      onChange={(e) => setReportModel(e.target.value)}
                      placeholder="(blank inherits)"
                      disabled={saving}
                      className={monoInputClass}
                    />
                  </div>
                )}
                <div className={fieldWrapperClass}>
                  <label className={labelClass}>Draft Model (Speculative Decoding)</label>
                  <input
                    type="text"
                    value={draftModel}
                    onChange={(e) => setDraftModel(e.target.value)}
                    placeholder="(blank inherits; LM Studio / llama.cpp / SGLang only)"
                    disabled={saving}
                    className={monoInputClass}
                  />
                </div>
              </div>
            )}
          </div>
        )}

        <div className="flex items-center gap-3 border-t border-[var(--border-subtle)] pt-3">
          <Button onClick={() => void handleSave()} disabled={saving}>
            {saveLabel}
          </Button>
        </div>

        {errorMsg && (
          <div
            className="rounded-[var(--control-radius)] border border-[var(--color-error,#ef4444)] bg-[rgba(239,68,68,0.1)] px-3 py-2 text-sm text-[var(--color-error,#ef4444)]"
            role="alert"
          >
            {errorMsg}
          </div>
        )}
        {successMsg && (
          <div
            className={cn(
              "rounded-[var(--control-radius)] border px-3 py-2 text-sm",
              "border-[var(--color-success,#22c55e)] bg-[rgba(34,197,94,0.1)] text-[var(--color-success,#22c55e)]"
            )}
            role="status"
          >
            {successMsg}
          </div>
        )}
      </div>
    </details>
  );
}
