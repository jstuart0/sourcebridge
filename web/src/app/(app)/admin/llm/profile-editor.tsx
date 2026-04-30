"use client";

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { ModelCombobox, type ModelOption } from "@/components/llm/ModelCombobox";
import { authFetch } from "@/lib/auth-fetch";
import { cn } from "@/lib/utils";

// ─────────────────────────────────────────────────────────────────────────
// Profile editor — slice 2 of LLM provider profiles
//
// Extracted from the today-shape /admin/llm grid, refactored to be
// PROFILE-SCOPED rather than workspace-singleton-scoped.
//
// Critical invariants the parent depends on:
//
// - Provider/base-URL change triggers a model-list fetch against the
//   EDITOR-target profile's connection params, NOT the workspace-active
//   profile. This is ruby-M1 in the plan: when the user is editing the
//   non-active profile, "Refresh models" must hit that profile's
//   provider/base-url, not the active profile's. Implemented here by
//   passing provider/baseURL state directly to fetchModels.
// - Save (PUT) is functionally and visually distinct from Activate
//   (POST /activate). Different colors, different click handlers, no
//   merge of intent. The "Make active" button only renders for
//   non-active profiles (N>=2 only).
// - In N=1 mode the editor renders without "Profile name" / "Make
//   active" — it looks exactly like today's /admin/llm. Progressive
//   disclosure: the J3 user sees nothing new.
// - The 409 target_no_longer_active wire shape (slice-1-flag #3) is
//   surfaced from the parent via a `conflictError` prop; the editor
//   itself does NOT decide how to resolve it (that's the parent's
//   job — the panel-scoped resolution UI lives in page.tsx).
// ─────────────────────────────────────────────────────────────────────────

// Slice 3 of the LLM provider profiles plan: ProfileResponse moved to
// `@/lib/llm/profile` so the per-repo override picker can share the
// type without an awkward cross-(app) import. Re-exported here so
// existing slice-2 imports keep working unchanged.
export type { ProfileResponse } from "@/lib/llm/profile";
import type { ProfileResponse } from "@/lib/llm/profile";

export interface ProfileEditorHandle {
  /** snapshot of the *saved* state for dirty detection */
  resetDirty: () => void;
  /** returns the current dirty state */
  isDirty: () => boolean;
}

interface ProfileEditorProps {
  /** the profile being edited */
  profile: ProfileResponse;
  /** N>=2 mode renders the profile-name field + Make active button */
  multiProfileMode: boolean;
  /** disable all inputs (e.g. while repair banner is up) */
  disabled?: boolean;
  /** called after a successful save; parent refetches the list */
  onSaved?: () => void;
  /** called when the user clicks Make active (N>=2 only) */
  onActivateRequested?: () => void;
  /** user-visible test-id prefix for stable selectors in tests */
  testIdPrefix?: string;
}

interface AvailableProvider {
  value: string;
  label: string;
  defaultBaseURL: string;
  defaultModel: string;
  supportsAPIKey: boolean;
  supportsDraftModel: boolean;
  isLocal: boolean;
}

const providers: AvailableProvider[] = [
  { value: "ollama", label: "Ollama (Local)", defaultBaseURL: "http://localhost:11434", defaultModel: "", supportsAPIKey: false, supportsDraftModel: false, isLocal: true },
  { value: "openai", label: "OpenAI", defaultBaseURL: "https://api.openai.com/v1", defaultModel: "gpt-4o", supportsAPIKey: true, supportsDraftModel: false, isLocal: false },
  { value: "anthropic", label: "Anthropic", defaultBaseURL: "https://api.anthropic.com", defaultModel: "claude-sonnet-4-20250514", supportsAPIKey: true, supportsDraftModel: false, isLocal: false },
  { value: "vllm", label: "vLLM (Local)", defaultBaseURL: "http://localhost:8000/v1", defaultModel: "", supportsAPIKey: false, supportsDraftModel: false, isLocal: true },
  { value: "llama-cpp", label: "llama.cpp (Local)", defaultBaseURL: "http://localhost:8080/v1", defaultModel: "", supportsAPIKey: false, supportsDraftModel: false, isLocal: true },
  { value: "sglang", label: "SGLang (Local)", defaultBaseURL: "http://localhost:30000/v1", defaultModel: "", supportsAPIKey: false, supportsDraftModel: false, isLocal: true },
  { value: "lmstudio", label: "LM Studio (Local)", defaultBaseURL: "http://localhost:1234/v1", defaultModel: "", supportsAPIKey: false, supportsDraftModel: true, isLocal: true },
  { value: "gemini", label: "Google Gemini", defaultBaseURL: "https://generativelanguage.googleapis.com/v1beta/openai/", defaultModel: "gemini-2.5-flash", supportsAPIKey: true, supportsDraftModel: false, isLocal: false },
  { value: "openrouter", label: "OpenRouter", defaultBaseURL: "https://openrouter.ai/api/v1", defaultModel: "google/gemini-2.5-flash", supportsAPIKey: true, supportsDraftModel: false, isLocal: false },
];

function lookupProvider(value: string): AvailableProvider {
  return providers.find((p) => p.value === value) ?? providers[0];
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

export function ProfileEditor({
  profile,
  multiProfileMode,
  disabled,
  onSaved,
  onActivateRequested,
  testIdPrefix = "profile-editor",
}: ProfileEditorProps) {
  const isEnterprise = process.env.NEXT_PUBLIC_EDITION === "enterprise";

  // Editable state. `profile` is the saved snapshot; the form starts from it
  // and each field is independent so the user can change any of them.
  const [name, setName] = useState(profile.name);
  const [provider, setProvider] = useState(profile.provider || "ollama");
  const [baseURL, setBaseURL] = useState(profile.base_url || "");
  const [apiKey, setApiKey] = useState("");
  const [summaryModel, setSummaryModel] = useState(profile.summary_model || "");
  const [reviewModel, setReviewModel] = useState(profile.review_model || "");
  const [askModel, setAskModel] = useState(profile.ask_model || "");
  const [knowledgeModel, setKnowledgeModel] = useState(profile.knowledge_model || "");
  const [architectureDiagramModel, setArchitectureDiagramModel] = useState(profile.architecture_diagram_model || "");
  const [reportModel, setReportModel] = useState(profile.report_model || "");
  const [draftModel, setDraftModel] = useState(profile.draft_model || "");
  const [timeoutSecs, setTimeoutSecs] = useState(profile.timeout_secs || 900);
  const [advancedMode, setAdvancedMode] = useState(profile.advanced_mode || false);

  // Saved-snapshot baseline for dirty detection. Re-syncs on profile prop change.
  const savedSnapshotRef = useRef<string>("");
  const buildSnapshot = useCallback(
    (
      override?: Partial<{
        name: string;
        provider: string;
        baseURL: string;
        summaryModel: string;
        reviewModel: string;
        askModel: string;
        knowledgeModel: string;
        architectureDiagramModel: string;
        reportModel: string;
        draftModel: string;
        timeoutSecs: number;
        advancedMode: boolean;
      }>,
    ) =>
      JSON.stringify({
        name: override?.name ?? name,
        provider: override?.provider ?? provider,
        baseURL: override?.baseURL ?? baseURL,
        summaryModel: override?.summaryModel ?? summaryModel,
        reviewModel: override?.reviewModel ?? reviewModel,
        askModel: override?.askModel ?? askModel,
        knowledgeModel: override?.knowledgeModel ?? knowledgeModel,
        architectureDiagramModel: override?.architectureDiagramModel ?? architectureDiagramModel,
        reportModel: override?.reportModel ?? reportModel,
        draftModel: override?.draftModel ?? draftModel,
        timeoutSecs: override?.timeoutSecs ?? timeoutSecs,
        advancedMode: override?.advancedMode ?? advancedMode,
      }),
    [
      name,
      provider,
      baseURL,
      summaryModel,
      reviewModel,
      askModel,
      knowledgeModel,
      architectureDiagramModel,
      reportModel,
      draftModel,
      timeoutSecs,
      advancedMode,
    ],
  );

  // When the parent passes a new profile (e.g. user picked a different row),
  // hydrate every field from the new profile and reset the snapshot.
  useEffect(() => {
    setName(profile.name);
    setProvider(profile.provider || "ollama");
    setBaseURL(profile.base_url || "");
    setApiKey("");
    setSummaryModel(profile.summary_model || "");
    setReviewModel(profile.review_model || "");
    setAskModel(profile.ask_model || "");
    setKnowledgeModel(profile.knowledge_model || "");
    setArchitectureDiagramModel(profile.architecture_diagram_model || "");
    setReportModel(profile.report_model || "");
    setDraftModel(profile.draft_model || "");
    setTimeoutSecs(profile.timeout_secs || 900);
    setAdvancedMode(profile.advanced_mode || false);
    savedSnapshotRef.current = JSON.stringify({
      name: profile.name,
      provider: profile.provider || "ollama",
      baseURL: profile.base_url || "",
      summaryModel: profile.summary_model || "",
      reviewModel: profile.review_model || "",
      askModel: profile.ask_model || "",
      knowledgeModel: profile.knowledge_model || "",
      architectureDiagramModel: profile.architecture_diagram_model || "",
      reportModel: profile.report_model || "",
      draftModel: profile.draft_model || "",
      timeoutSecs: profile.timeout_secs || 900,
      advancedMode: profile.advanced_mode || false,
    });
  }, [profile]);

  const currentSnapshot = useMemo(() => buildSnapshot(), [buildSnapshot]);
  const dirty = savedSnapshotRef.current !== "" && currentSnapshot !== savedSnapshotRef.current;
  const hasPendingApiKey = apiKey.length > 0;

  // Save / state for the action buttons.
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [success, setSuccess] = useState(false);
  const [testResult, setTestResult] = useState<string | null>(null);

  // Models list (provider-driven). The fetch is keyed on the EDITOR's
  // provider/baseURL, not the workspace-active profile (ruby-M1). The
  // existing /admin/llm-models endpoint accepts those as query params,
  // which already does the right thing — we just have to pass the
  // editor's values.
  const [models, setModels] = useState<ModelOption[]>([]);
  const [modelFilter, setModelFilter] = useState("");
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsError, setModelsError] = useState<string | null>(null);

  const fetchModels = useCallback(async (prov: string, url: string) => {
    setModelsLoading(true);
    setModelsError(null);
    try {
      const params = new URLSearchParams();
      if (prov) params.set("provider", prov);
      if (url) params.set("base_url", url);
      const res = await authFetch(`/api/v1/admin/llm-models?${params}`);
      if (!res.ok) throw new Error(await handleApiError(res));
      const data = await res.json();
      setModels(data.models || []);
      if (data.error) setModelsError(data.error);
    } catch (e) {
      setModels([]);
      setModelsError((e as Error).message);
    }
    setModelsLoading(false);
  }, []);

  // Initial model fetch when the profile changes.
  useEffect(() => {
    fetchModels(profile.provider || "ollama", profile.base_url || "");
  }, [profile.id, profile.provider, profile.base_url, fetchModels]);

  const filteredModels = useMemo(() => {
    if (!modelFilter) return models;
    const f = modelFilter.toLowerCase();
    return models.filter(
      (m) => m.id.toLowerCase().includes(f) || (m.name && m.name.toLowerCase().includes(f)),
    );
  }, [modelFilter, models]);

  function handleProviderChange(next: string) {
    setProvider(next);
    const def = lookupProvider(next);
    setBaseURL(def.defaultBaseURL);
    if (def.defaultModel) {
      setSummaryModel(def.defaultModel);
      setReviewModel(def.defaultModel);
      setAskModel(def.defaultModel);
      setKnowledgeModel(def.defaultModel);
      setArchitectureDiagramModel(def.defaultModel);
      if (isEnterprise) setReportModel(def.defaultModel);
    }
    fetchModels(next, def.defaultBaseURL);
  }

  // ProfileUpdateRequest: pointer-patch — we always send everything as
  // pointers so the server preserves vs explicit-sets correctly. The
  // api_key field is special: we send it only when the user typed
  // something (a new key); empty stays out of the request entirely so
  // the server preserves the existing key. The "clear key" option
  // is intentionally NOT in the editor — clearing a key is rare and
  // a future enhancement (a small "Clear key" link could be added).
  async function saveProfile() {
    if (saving || disabled) return;
    setSaving(true);
    setMessage(null);
    setSuccess(false);
    try {
      const body: Record<string, unknown> = {};
      // For multi-profile mode, name is settable. In N=1 mode we don't
      // expose the name field; the user can rename via the header pill
      // (a later enhancement) but for now N=1 saves preserve the name.
      if (multiProfileMode && name.trim() !== profile.name) {
        body.name = name.trim();
      }
      body.provider = provider;
      body.base_url = baseURL;
      body.summary_model = summaryModel;
      body.review_model = reviewModel;
      body.ask_model = askModel;
      body.knowledge_model = knowledgeModel;
      body.architecture_diagram_model = architectureDiagramModel;
      body.draft_model = draftModel;
      body.timeout_secs = timeoutSecs;
      body.advanced_mode = advancedMode;
      if (isEnterprise) body.report_model = reportModel;
      if (apiKey) body.api_key = apiKey;

      // Trim the prefix from the id when it's the full SurrealDB form;
      // the REST handler accepts both shapes.
      const idPath = encodeURIComponent(profile.id);
      const res = await authFetch(`/api/v1/admin/llm-profiles/${idPath}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok) {
        // 409 target_no_longer_active is special — bubble up a
        // structured error for the parent page to render the
        // resolution UI (slice-1-flag #3).
        if (res.status === 409) {
          let bodyText = "";
          try {
            bodyText = await res.text();
            const json = JSON.parse(bodyText);
            if (json.error === "target_no_longer_active") {
              throw new ProfileTargetNoLongerActiveError(
                json.hint || "Another writer activated a different profile during your edit.",
              );
            }
          } catch (e) {
            if (e instanceof ProfileTargetNoLongerActiveError) throw e;
            // fall through to generic 409 handling
          }
          throw new Error(bodyText || `HTTP ${res.status}`);
        }
        throw new Error(await handleApiError(res));
      }
      setMessage("Profile saved.");
      setSuccess(true);
      setApiKey("");
      // The parent will refetch + pass us a fresh profile; the prop
      // effect resets our dirty snapshot from the new profile.
      onSaved?.();
    } catch (e) {
      if (e instanceof ProfileTargetNoLongerActiveError) {
        // Re-throw to the parent. The parent owns the resolution UI.
        // We clear our local saving state first so the editor isn't
        // wedged.
        setSaving(false);
        setSuccess(false);
        // Bubble via a panel-scoped error message; parent listens via
        // onSaved-not-called + a dedicated error prop OR via re-throw.
        // For simplicity we surface inline AND signal via a window
        // CustomEvent so the parent can render the resolution UI.
        if (typeof window !== "undefined") {
          window.dispatchEvent(
            new CustomEvent("sourcebridge:profile-target-no-longer-active", {
              detail: { profileId: profile.id, hint: (e as Error).message },
            }),
          );
        }
        setMessage(`Conflict: ${(e as Error).message}`);
        return;
      }
      setSuccess(false);
      setMessage(`Error: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  async function testConnection() {
    setTestResult(null);
    const res = await authFetch("/api/v1/admin/test-llm", { method: "POST" });
    const data = await res.json();
    setTestResult(JSON.stringify(data, null, 2));
  }

  const fieldWrapClass = "grid gap-1.5";
  const labelClass = "text-sm font-medium text-[var(--text-primary)]";
  const helpTextClass = "text-xs text-[var(--text-secondary)]";
  const inputClass =
    "h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)] disabled:opacity-60";
  const monoInputClass = `${inputClass} font-mono`;
  const selectClass = inputClass;
  const stackClass = "grid gap-4 max-w-[32rem]";
  const codeBlockClass =
    "rounded-[var(--radius-md)] bg-black/20 p-3 font-mono text-sm whitespace-pre-wrap text-[var(--text-primary)]";
  const messageClass = (ok: boolean) =>
    cn(
      "rounded-[var(--radius-md)] border px-3 py-2 text-sm",
      ok
        ? "border-[var(--color-success,#22c55e)] bg-[rgba(34,197,94,0.1)] text-[var(--color-success,#22c55e)]"
        : "border-[var(--color-error,#ef4444)] bg-[rgba(239,68,68,0.1)] text-[var(--color-error,#ef4444)]",
    );

  const def = lookupProvider(provider);

  return (
    <div className={stackClass} data-testid={testIdPrefix}>
      {multiProfileMode && (
        <div className={fieldWrapClass}>
          <label className={labelClass} htmlFor={`${testIdPrefix}-name`}>
            Profile name
          </label>
          <input
            id={`${testIdPrefix}-name`}
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            disabled={disabled}
            maxLength={64}
            className={inputClass}
            placeholder="e.g. Anthropic prod"
            data-testid={`${testIdPrefix}-name-input`}
          />
          <p className={helpTextClass}>The name shown in the profile list. Up to 64 characters.</p>
        </div>
      )}

      <div className={fieldWrapClass}>
        <label className={labelClass}>Provider</label>
        <select
          value={provider}
          onChange={(e) => handleProviderChange(e.target.value)}
          disabled={disabled}
          className={selectClass}
        >
          {providers.map((p) => (
            <option key={p.value} value={p.value}>
              {p.label}
            </option>
          ))}
        </select>
      </div>

      <div className={fieldWrapClass}>
        <label className={labelClass}>Base URL</label>
        <div className="flex items-center gap-2">
          <input
            type="text"
            value={baseURL}
            onChange={(e) => setBaseURL(e.target.value)}
            placeholder={def.defaultBaseURL}
            disabled={disabled}
            className={`flex-1 ${monoInputClass}`}
          />
          <Button
            variant="secondary"
            size="sm"
            onClick={() => fetchModels(provider, baseURL)}
            disabled={modelsLoading || disabled}
          >
            {modelsLoading ? "Loading..." : "Refresh models"}
          </Button>
        </div>
        <p className={helpTextClass}>
          {provider === "ollama" || provider === "vllm"
            ? "Required for local providers. Include /v1 suffix for OpenAI-compatible endpoints."
            : provider === "llama-cpp"
              ? "llama.cpp server with OpenAI-compatible API. Supports speculative decoding when launched with --model-draft."
              : provider === "sglang"
                ? "SGLang server with OpenAI-compatible API. Supports EAGLE-based speculative decoding at launch."
                : provider === "lmstudio"
                  ? "LM Studio with OpenAI-compatible API. Supports per-request speculative decoding via draft model."
                  : provider === "openrouter"
                    ? "OpenRouter uses the OpenAI-compatible API. Models from 300+ providers available."
                    : provider === "gemini"
                      ? "Google Gemini uses an OpenAI-compatible endpoint. Default URL works for most setups."
                      : "Default URL for this provider. Change it to use a custom proxy or endpoint."}
        </p>
      </div>

      {def.supportsAPIKey && (
        <div className={fieldWrapClass}>
          <label className={labelClass}>API Key</label>
          <div className="flex items-center gap-2">
            <input
              type="password"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              disabled={disabled}
              placeholder={
                profile.api_key_set
                  ? "Key is configured (enter new to replace)"
                  : provider === "anthropic"
                    ? "sk-ant-..."
                    : provider === "gemini"
                      ? "AIza..."
                      : "sk-..."
              }
              className={`flex-1 ${monoInputClass}`}
            />
            {profile.api_key_set && (
              <span className="whitespace-nowrap font-mono text-xs text-[var(--color-success,#22c55e)]">
                {profile.api_key_hint || "Configured"}
              </span>
            )}
          </div>
          <p className={helpTextClass}>
            Required for cloud providers. After saving a new key, click &quot;Refresh models&quot; to load the
            model list.
          </p>
        </div>
      )}

      {def.supportsDraftModel && (
        <div className={fieldWrapClass}>
          <label className={labelClass}>Draft Model (Speculative Decoding)</label>
          <input
            type="text"
            value={draftModel}
            onChange={(e) => setDraftModel(e.target.value)}
            disabled={disabled}
            placeholder="e.g. lmstudio-community/Qwen2.5-0.5B-Instruct-GGUF"
            className={monoInputClass}
          />
          <p className={helpTextClass}>
            Optional. Smaller model used for speculative decoding. LM Studio sends candidate tokens from this
            model and verifies them with the main model in a single pass, improving throughput 1.5-3x.
          </p>
        </div>
      )}

      <div className={fieldWrapClass}>
        <label className={labelClass}>Model {advancedMode && "(Analysis / Default)"}</label>
        {models.length > 20 ? (
          <input
            type="text"
            value={modelFilter}
            onChange={(e) => setModelFilter(e.target.value)}
            disabled={disabled}
            placeholder="Filter models..."
            className={inputClass}
          />
        ) : null}
        <ModelCombobox
          value={summaryModel}
          onChange={(v) => {
            setSummaryModel(v);
            if (!advancedMode) {
              setReviewModel(v);
              setAskModel(v);
              setKnowledgeModel(v);
              setArchitectureDiagramModel(v);
              if (isEnterprise) setReportModel(v);
            }
          }}
          models={filteredModels}
          placeholder={
            models.length > 0 ? "Pick from list or type a custom model ID" : def.defaultModel || "model name"
          }
          disabled={disabled}
          className={monoInputClass}
        />
        <p className={helpTextClass}>
          {modelsLoading
            ? "Loading available models..."
            : modelsError
              ? `Could not load models: ${modelsError}`
              : models.length > 0
                ? `${models.length} model${models.length !== 1 ? "s" : ""} available. Start typing to filter or enter a custom model ID.`
                : "Used for code summaries, reviews, and chat. All operations use the same model by default."}
        </p>
      </div>

      <div className="flex items-center gap-3">
        <label className="relative inline-flex cursor-pointer items-center">
          <input
            type="checkbox"
            checked={advancedMode}
            onChange={(e) => {
              const next = e.target.checked;
              setAdvancedMode(next);
              if (!next) {
                setReviewModel(summaryModel);
                setAskModel(summaryModel);
                setKnowledgeModel(summaryModel);
                setArchitectureDiagramModel(summaryModel);
                if (isEnterprise) setReportModel(summaryModel);
              }
            }}
            disabled={disabled}
            className="peer sr-only"
          />
          <div className="peer h-5 w-9 rounded-full bg-[var(--border-default)] after:absolute after:left-[2px] after:top-[2px] after:h-4 after:w-4 after:rounded-full after:border after:border-gray-300 after:bg-white after:transition-all after:content-[''] peer-checked:bg-[hsl(var(--accent-hue,250),60%,60%)] peer-checked:after:translate-x-full peer-checked:after:border-white" />
        </label>
        <div>
          <span className={labelClass}>Advanced: Per-operation models</span>
          <p className={helpTextClass}>
            Use different models for different operation types. Turning this off resets all operations to the
            default model.
          </p>
        </div>
      </div>

      {advancedMode && (
        <div className="space-y-4 rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--bg-raised)] p-4">
          <p className="text-sm text-[var(--text-secondary)]">
            Assign models to operation groups. The default model above is used for Analysis. Empty fields fall
            back to the default.
          </p>

          {(
            [
              {
                label: "Code Review",
                key: "review",
                value: reviewModel,
                setter: setReviewModel,
                badge: "Medium ~5K tok",
                help: "reviewCode (all templates)",
              },
              {
                label: "Discussion & Q&A",
                key: "discussion",
                value: askModel,
                setter: setAskModel,
                badge: "Medium ~1-5K tok",
                help: "discussCode, answerQuestion",
              },
              {
                label: "Knowledge Generation",
                key: "knowledge",
                value: knowledgeModel,
                setter: setKnowledgeModel,
                badge: "High ~10-37K tok",
                help: "cliffNotes, learningPath, codeTour, workflowStory, explainSystem",
              },
              {
                label: "Architecture Diagrams",
                key: "architecture",
                value: architectureDiagramModel,
                setter: setArchitectureDiagramModel,
                badge: "Visual reasoning",
                help: "AI-generated architecture diagrams. Benefits from vision / reasoning models.",
              },
              ...(isEnterprise
                ? [
                    {
                      label: "Reports",
                      key: "report",
                      value: reportModel,
                      setter: setReportModel,
                      badge: "High long-form",
                      help: "architecture baseline, SWOT, due diligence, portfolio and compliance reports",
                    },
                  ]
                : []),
            ] as const
          ).map((group) => (
            <div key={group.key} className={fieldWrapClass}>
              <div className="flex items-center gap-2">
                <label className={labelClass}>{group.label}</label>
                <span className="rounded-full border border-[var(--border-subtle)] bg-[var(--bg-base)] px-2 py-0.5 text-[10px] font-medium text-[var(--text-secondary)]">
                  {group.badge}
                </span>
              </div>
              <ModelCombobox
                value={group.value}
                onChange={group.setter}
                models={filteredModels}
                placeholder={summaryModel || "same as default"}
                disabled={disabled}
                className={monoInputClass}
              />
              <p className={helpTextClass}>{group.help}</p>
            </div>
          ))}
        </div>
      )}

      <div className={fieldWrapClass}>
        <label className={labelClass}>Timeout (seconds)</label>
        <input
          type="number"
          value={timeoutSecs}
          onChange={(e) => setTimeoutSecs(parseInt(e.target.value) || 900)}
          min={5}
          max={3600}
          disabled={disabled}
          className="h-11 w-32 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)] disabled:opacity-60"
        />
      </div>

      <div className="flex items-center gap-2">
        <Button
          onClick={saveProfile}
          disabled={saving || disabled || (!dirty && !hasPendingApiKey)}
          data-testid={`${testIdPrefix}-save`}
        >
          {saving ? "Saving..." : multiProfileMode ? "Save profile" : "Save"}
        </Button>
        <Button variant="secondary" onClick={testConnection} disabled={disabled}>
          Test Connection
        </Button>
        {multiProfileMode && !profile.is_active && onActivateRequested && (
          // Visual distinction from "Save" — success-tinted, separate
          // intent. Activating is a state change; saving is a content
          // change. Conflating them is the #1 thing ruby called out in
          // the UX intake (§6).
          <button
            type="button"
            onClick={onActivateRequested}
            disabled={disabled}
            data-testid={`${testIdPrefix}-make-active`}
            className={cn(
              "inline-flex h-11 items-center justify-center gap-2 rounded-[var(--control-radius)] border border-[var(--color-success,#22c55e)] bg-[rgba(34,197,94,0.1)] px-4 text-sm font-medium text-[var(--color-success,#22c55e)] transition-colors",
              "hover:bg-[rgba(34,197,94,0.2)]",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent-focus)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--bg-base)]",
              "disabled:pointer-events-none disabled:opacity-60",
            )}
          >
            Make active
          </button>
        )}
      </div>

      {message && <p className={messageClass(success)}>{message}</p>}
      {testResult && <pre className={codeBlockClass}>{testResult}</pre>}
    </div>
  );
}

// Distinct error type so the parent page can detect the
// 409 target_no_longer_active wire shape (slice-1-flag #3) and render
// the panel-scoped resolution UI. Plain Error would be ambiguous.
export class ProfileTargetNoLongerActiveError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ProfileTargetNoLongerActiveError";
  }
}
