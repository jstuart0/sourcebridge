"use client";

/**
 * RepositoryLLMOverrideSection — collapsed-by-default per-repository LLM
 * override form. Lives below the per-repo wiki-settings form on the
 * repository detail page.
 *
 * Mirrors /admin/llm field layout: provider dropdown, base URL, API key,
 * advanced-mode toggle that reveals per-area model fields. Saving calls
 * setRepositoryLLMOverride; clearing calls clearRepositoryLLMOverride.
 *
 * Empty-key UX semantics:
 *   - Leaving the API Key field blank → "leave the saved cipher alone"
 *     (the resolver treats empty-string as omitted). To clear the saved
 *     key back to workspace inheritance, the user toggles the "Clear API
 *     key" checkbox, which sets clearAPIKey:true on the mutation.
 *   - Other text fields use empty-string-clears semantics: setting any
 *     model field to "" clears it back to workspace inheritance.
 */

import { useEffect, useMemo, useState } from "react";
import { useMutation } from "urql";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import {
  SET_REPOSITORY_LLM_OVERRIDE_MUTATION,
  CLEAR_REPOSITORY_LLM_OVERRIDE_MUTATION,
} from "@/lib/graphql/queries";

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

export interface RepositoryLLMOverride {
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
  /** Current saved override (null when no override is set). */
  override: RepositoryLLMOverride | null;
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

// ─────────────────────────────────────────────────────────────────────────────
// Component
// ─────────────────────────────────────────────────────────────────────────────

export function RepositoryLLMOverrideSection({
  repoId,
  override,
  onSaved,
  isEnterprise = false,
}: RepositoryLLMOverrideSectionProps) {
  const initial = override;

  // Form state. Empty-string values for model fields mean "no override
  // applies, inherit workspace". The mutation translates per the patch
  // semantics documented in the GraphQL schema.
  const [provider, setProvider] = useState(initial?.provider ?? "");
  const [baseURL, setBaseURL] = useState(initial?.baseURL ?? "");
  const [apiKey, setApiKey] = useState(""); // never pre-populated; password field
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
  // query resolves async; without this sync the form mounts with a null
  // initial and stays blank even after the saved override loads, which
  // would let an operator save blanks over the saved values when they
  // open the collapsed section).
  //
  // The local user-input edits are intentionally NOT preserved across
  // a prop update — the contract is "props reflect the saved state, the
  // form mirrors props on each load". Local mutations are short-lived
  // (the user clicks Save and the parent re-renders with the latest
  // saved value).
  //
  // apiKey + clearAPIKey are NOT synced (apiKey is a password field
  // that is never pre-populated by design; clearAPIKey is transient
  // local state).
  useEffect(() => {
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
  ]);

  const [saving, setSaving] = useState(false);
  const [errorMsg, setErrorMsg] = useState<string | null>(null);
  const [successMsg, setSuccessMsg] = useState<string | null>(null);

  const [, setMutation] = useMutation(SET_REPOSITORY_LLM_OVERRIDE_MUTATION);
  const [, clearMutation] = useMutation(CLEAR_REPOSITORY_LLM_OVERRIDE_MUTATION);

  // Saved-state hint. The hint summarizes what the saved override does
  // so an operator can confirm at a glance.
  const savedHint = useMemo(() => {
    if (!initial) return "Inheriting workspace LLM settings.";
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
  }, [initial]);

  const handleSave = async () => {
    setSaving(true);
    setErrorMsg(null);
    setSuccessMsg(null);

    // Build the patch input. Use empty-string semantics for non-secret
    // fields (server treats "" as "clear back to workspace inheritance").
    // For apiKey: only send when the user typed something; nil otherwise.
    const input: Record<string, unknown> = {
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
      // The server returns extension code "ENCRYPTION_KEY_REQUIRED" when
      // cfg.Security.EncryptionKey is missing; pull a precise message from
      // the GraphQL error so the user knows exactly what to do.
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

  const handleClear = async () => {
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
    setProvider("");
    setBaseURL("");
    setAdvancedMode(false);
    setSummaryModel("");
    setReviewModel("");
    setAskModel("");
    setKnowledgeModel("");
    setArchitectureDiagramModel("");
    setReportModel("");
    setDraftModel("");
    onSaved?.(null);
  };

  // Visual styles matching the existing wiki-settings-panel + admin/llm.
  const labelClass = "block text-xs font-medium text-[var(--text-secondary)]";
  const helpClass = "mt-1 text-xs text-[var(--text-tertiary)]";
  const inputClass =
    "mt-1 h-10 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-focus)] disabled:opacity-60";
  const monoInputClass = `${inputClass} font-mono`;
  const fieldWrapperClass = "grid gap-1";

  const showAPIKeyField = !provider || PROVIDERS_NEEDING_KEY.has(provider);

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
          Override the workspace LLM settings for this repository. Empty fields
          inherit from <a className="underline" href="/admin/llm">workspace settings</a>.
          Applies to every LLM operation for this repo (summary, review, Q&amp;A,
          knowledge, architecture diagrams, reports).
        </p>

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
          <div className="space-y-3 rounded-[var(--control-radius)] border border-[var(--border-subtle)] bg-[var(--bg-raised)] p-3">
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

        <div className="flex items-center gap-3 border-t border-[var(--border-subtle)] pt-3">
          <Button onClick={() => void handleSave()} disabled={saving}>
            {saving ? "Saving…" : "Save override"}
          </Button>
          {initial != null && (
            <Button
              variant="secondary"
              onClick={() => void handleClear()}
              disabled={saving}
            >
              Clear override (inherit workspace)
            </Button>
          )}
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
