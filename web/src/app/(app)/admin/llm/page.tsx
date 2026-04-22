"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { authFetch } from "@/lib/auth-fetch";
import { cn } from "@/lib/utils";
import { ModelCombobox, type ModelOption } from "@/components/llm/ModelCombobox";

interface LLMConfigState {
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
}

interface AdminConfigWorker {
  worker: { address: string };
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

const providerDefaults: Record<string, { baseURL: string; model: string }> = {
  openai: { baseURL: "https://api.openai.com/v1", model: "gpt-4o" },
  anthropic: { baseURL: "https://api.anthropic.com", model: "claude-sonnet-4-20250514" },
  ollama: { baseURL: "http://localhost:11434", model: "" },
  vllm: { baseURL: "http://localhost:8000/v1", model: "" },
  "llama-cpp": { baseURL: "http://localhost:8080/v1", model: "" },
  sglang: { baseURL: "http://localhost:30000/v1", model: "" },
  lmstudio: { baseURL: "http://localhost:1234/v1", model: "" },
  gemini: {
    baseURL: "https://generativelanguage.googleapis.com/v1beta/openai/",
    model: "gemini-2.5-flash",
  },
  openrouter: { baseURL: "https://openrouter.ai/api/v1", model: "google/gemini-2.5-flash" },
};

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

export default function AdminLLMPage() {
  const isEnterprise = process.env.NEXT_PUBLIC_EDITION === "enterprise";

  const [serverConfig, setServerConfig] = useState<LLMConfigState | null>(null);
  const [workerAddr, setWorkerAddr] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);

  // Editable state
  const [provider, setProvider] = useState("");
  const [baseURL, setBaseURL] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [summaryModel, setSummaryModel] = useState("");
  const [reviewModel, setReviewModel] = useState("");
  const [askModel, setAskModel] = useState("");
  const [knowledgeModel, setKnowledgeModel] = useState("");
  const [architectureDiagramModel, setArchitectureDiagramModel] = useState("");
  const [reportModel, setReportModel] = useState("");
  const [timeoutSecs, setTimeoutSecs] = useState(900);
  const [advancedMode, setAdvancedMode] = useState(false);
  const [draftModel, setDraftModel] = useState("");

  // Saved-snapshot baseline for dirty detection
  const savedSnapshotRef = useRef<string>("");
  const [lastSavedAt, setLastSavedAt] = useState<number | null>(null);
  const [, setTick] = useState(0); // triggers "saved Xm ago" updates

  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [success, setSuccess] = useState(false);
  const [testResult, setTestResult] = useState<string | null>(null);

  // Models list
  const [models, setModels] = useState<ModelOption[]>([]);
  const [modelFilter, setModelFilter] = useState("");
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsError, setModelsError] = useState<string | null>(null);

  const currentSnapshot = useMemo(() => {
    return JSON.stringify({
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
    });
  }, [
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
  ]);

  const dirty = savedSnapshotRef.current !== "" && currentSnapshot !== savedSnapshotRef.current;
  const hasPendingApiKey = apiKey.length > 0;

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

  const loadConfig = useCallback(async () => {
    try {
      const [cfgRes, wkRes] = await Promise.all([
        authFetch("/api/v1/admin/llm-config"),
        authFetch("/api/v1/admin/config"),
      ]);
      if (!cfgRes.ok) throw new Error(await handleApiError(cfgRes));
      const cfg = (await cfgRes.json()) as LLMConfigState;
      setServerConfig(cfg);
      setProvider(cfg.provider || "ollama");
      setBaseURL(cfg.base_url || "");
      setSummaryModel(cfg.summary_model || "");
      setReviewModel(cfg.review_model || "");
      setAskModel(cfg.ask_model || "");
      setKnowledgeModel(cfg.knowledge_model || "");
      setArchitectureDiagramModel(cfg.architecture_diagram_model || "");
      setReportModel(cfg.report_model || "");
      setTimeoutSecs(cfg.timeout_secs || 900);
      setAdvancedMode(cfg.advanced_mode || false);
      setDraftModel(cfg.draft_model || "");

      savedSnapshotRef.current = JSON.stringify({
        provider: cfg.provider || "ollama",
        baseURL: cfg.base_url || "",
        summaryModel: cfg.summary_model || "",
        reviewModel: cfg.review_model || "",
        askModel: cfg.ask_model || "",
        knowledgeModel: cfg.knowledge_model || "",
        architectureDiagramModel: cfg.architecture_diagram_model || "",
        reportModel: cfg.report_model || "",
        draftModel: cfg.draft_model || "",
        timeoutSecs: cfg.timeout_secs || 900,
        advancedMode: cfg.advanced_mode || false,
      });

      if (wkRes.ok) {
        const wk = (await wkRes.json()) as AdminConfigWorker;
        setWorkerAddr(wk.worker?.address || null);
      }

      fetchModels(cfg.provider || "ollama", cfg.base_url || "");
    } catch (e) {
      setLoadError((e as Error).message);
    }
  }, [fetchModels]);

  useEffect(() => {
    loadConfig();
  }, [loadConfig]);

  // Re-render every 30s so "saved Xm ago" stays fresh
  useEffect(() => {
    if (!lastSavedAt) return;
    const id = setInterval(() => setTick((t) => t + 1), 30_000);
    return () => clearInterval(id);
  }, [lastSavedAt]);

  const filteredModels = useMemo(() => {
    if (!modelFilter) return models;
    const f = modelFilter.toLowerCase();
    return models.filter(
      (m) => m.id.toLowerCase().includes(f) || (m.name && m.name.toLowerCase().includes(f))
    );
  }, [modelFilter, models]);

  function handleProviderChange(next: string) {
    setProvider(next);
    const defaults = providerDefaults[next];
    if (defaults) {
      setBaseURL(defaults.baseURL);
      if (defaults.model) {
        setSummaryModel(defaults.model);
        setReviewModel(defaults.model);
        setAskModel(defaults.model);
        setKnowledgeModel(defaults.model);
        setArchitectureDiagramModel(defaults.model);
        if (isEnterprise) setReportModel(defaults.model);
      }
      fetchModels(next, defaults.baseURL);
    }
  }

  async function saveLLMConfig() {
    if (saving) return;
    setSaving(true);
    setMessage(null);
    setSuccess(false);
    try {
      const body: Record<string, unknown> = {
        provider,
        base_url: baseURL,
        summary_model: summaryModel,
        review_model: reviewModel,
        ask_model: askModel,
        knowledge_model: knowledgeModel,
        architecture_diagram_model: architectureDiagramModel,
        draft_model: draftModel,
        timeout_secs: timeoutSecs,
        advanced_mode: advancedMode,
      };
      if (isEnterprise) body.report_model = reportModel;
      if (apiKey) body.api_key = apiKey;

      const res = await authFetch("/api/v1/admin/llm-config", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok) throw new Error(await handleApiError(res));
      const data = await res.json();
      setMessage("LLM configuration saved." + (data.note ? ` ${data.note}` : ""));
      setSuccess(true);
      setApiKey("");
      setLastSavedAt(Date.now());
      // Refresh snapshot so dirty indicator clears
      savedSnapshotRef.current = currentSnapshot;
      // Pull the server-echoed state (e.g. api_key_hint updates)
      loadConfig();
    } catch (e) {
      setSuccess(false);
      setMessage(`Error: ${(e as Error).message}`);
    }
    setSaving(false);
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
    "h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)]";
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
        : "border-[var(--color-error,#ef4444)] bg-[rgba(239,68,68,0.1)] text-[var(--color-error,#ef4444)]"
    );

  if (loadError) {
    return (
      <PageFrame>
        <PageHeader eyebrow="Admin" title="LLM configuration" />
        <Panel>
          <p className="text-sm text-[var(--color-error,#ef4444)]">
            Could not load LLM configuration: {loadError}
          </p>
        </Panel>
      </PageFrame>
    );
  }

  return (
    <PageFrame>
      <PageHeader
        eyebrow="Admin"
        title="LLM configuration"
        description="Provider, model, and per-operation overrides for code analysis, review, and chat."
        actions={
          <div className="flex items-center gap-3">
            {dirty ? (
              <span className="inline-flex items-center gap-1.5 rounded-full border border-[var(--border-default)] bg-[var(--bg-raised)] px-2.5 py-1 text-xs font-medium text-[var(--text-secondary)]">
                <span className="h-1.5 w-1.5 rounded-full bg-amber-400" />
                Unsaved changes
              </span>
            ) : lastSavedAt ? (
              <span className="text-xs text-[var(--text-tertiary)]">
                Saved {formatRelativeSaved(lastSavedAt)}
              </span>
            ) : null}
          </div>
        }
      />

      <Panel className="mb-4">
        <div className={stackClass}>
          <div className={fieldWrapClass}>
            <label className={labelClass}>Provider</label>
            <select
              value={provider}
              onChange={(e) => handleProviderChange(e.target.value)}
              className={selectClass}
            >
              <option value="ollama">Ollama (Local)</option>
              <option value="openai">OpenAI</option>
              <option value="anthropic">Anthropic</option>
              <option value="vllm">vLLM (Local)</option>
              <option value="llama-cpp">llama.cpp (Local)</option>
              <option value="sglang">SGLang (Local)</option>
              <option value="lmstudio">LM Studio (Local)</option>
              <option value="gemini">Google Gemini</option>
              <option value="openrouter">OpenRouter</option>
            </select>
          </div>

          <div className={fieldWrapClass}>
            <label className={labelClass}>Base URL</label>
            <div className="flex items-center gap-2">
              <input
                type="text"
                value={baseURL}
                onChange={(e) => setBaseURL(e.target.value)}
                placeholder={providerDefaults[provider]?.baseURL || "http://localhost:11434"}
                className={`flex-1 ${monoInputClass}`}
              />
              <Button
                variant="secondary"
                size="sm"
                onClick={() => fetchModels(provider, baseURL)}
                disabled={modelsLoading}
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

          {(provider === "anthropic" ||
            provider === "openai" ||
            provider === "gemini" ||
            provider === "openrouter") && (
            <div className={fieldWrapClass}>
              <label className={labelClass}>API Key</label>
              <div className="flex items-center gap-2">
                <input
                  type="password"
                  value={apiKey}
                  onChange={(e) => setApiKey(e.target.value)}
                  placeholder={
                    serverConfig?.api_key_set
                      ? "Key is configured (enter new to replace)"
                      : provider === "anthropic"
                      ? "sk-ant-..."
                      : provider === "gemini"
                      ? "AIza..."
                      : "sk-..."
                  }
                  className={`flex-1 ${monoInputClass}`}
                />
                {serverConfig?.api_key_set && (
                  <span className="whitespace-nowrap font-mono text-xs text-[var(--color-success,#22c55e)]">
                    {serverConfig.api_key_hint || "Configured"}
                  </span>
                )}
              </div>
              <p className={helpTextClass}>
                Required for cloud providers. After saving a new key, click &quot;Refresh models&quot; to load
                the model list.
              </p>
            </div>
          )}

          {provider === "lmstudio" && (
            <div className={fieldWrapClass}>
              <label className={labelClass}>Draft Model (Speculative Decoding)</label>
              <input
                type="text"
                value={draftModel}
                onChange={(e) => setDraftModel(e.target.value)}
                placeholder="e.g. lmstudio-community/Qwen2.5-0.5B-Instruct-GGUF"
                className={monoInputClass}
              />
              <p className={helpTextClass}>
                Optional. Smaller model used for speculative decoding. LM Studio sends candidate tokens from
                this model and verifies them with the main model in a single pass, improving throughput
                1.5-3x.
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
                models.length > 0
                  ? "Pick from list or type a custom model ID"
                  : providerDefaults[provider]?.model || "model name"
              }
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
                className="peer sr-only"
              />
              <div className="peer h-5 w-9 rounded-full bg-[var(--border-default)] after:absolute after:left-[2px] after:top-[2px] after:h-4 after:w-4 after:rounded-full after:border after:border-gray-300 after:bg-white after:transition-all after:content-[''] peer-checked:bg-[hsl(var(--accent-hue,250),60%,60%)] peer-checked:after:translate-x-full peer-checked:after:border-white" />
            </label>
            <div>
              <span className={labelClass}>Advanced: Per-operation models</span>
              <p className={helpTextClass}>
                Use different models for different operation types. Turning this off resets all operations
                to the default model.
              </p>
            </div>
          </div>

          {advancedMode && (
            <div className="space-y-4 rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--bg-raised)] p-4">
              <p className="text-sm text-[var(--text-secondary)]">
                Assign models to operation groups. The default model above is used for Analysis. Empty fields
                fall back to the default.
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
              className="h-11 w-32 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)]"
            />
          </div>

          <div className="flex items-center gap-2">
            <Button onClick={saveLLMConfig} disabled={saving || (!dirty && !hasPendingApiKey)}>
              {saving ? "Saving..." : "Save"}
            </Button>
            <Button variant="secondary" onClick={testConnection}>
              Test Connection
            </Button>
          </div>

          {message && <p className={messageClass(success)}>{message}</p>}
          {testResult && <pre className={codeBlockClass}>{testResult}</pre>}
        </div>
      </Panel>

      {isLocalProvider(provider) && (
        <Panel className="mb-4">
          <h3 className="mb-2 text-base font-semibold text-[var(--text-primary)]">
            Speculative Decoding
          </h3>
          <p className="text-sm text-[var(--text-secondary)]">
            {provider === "lmstudio"
              ? "LM Studio supports per-request speculative decoding via the Draft Model field above. Configure a smaller draft model for 1.5-3x throughput improvement."
              : provider === "llama-cpp"
              ? "llama.cpp supports speculative decoding when launched with --model-draft. Performance metrics (tokens/sec, acceptance rate) appear in operation results."
              : provider === "sglang"
              ? "SGLang supports EAGLE-based speculative decoding configured at server launch. Performance metrics appear in operation results."
              : provider === "vllm"
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
    </PageFrame>
  );
}
