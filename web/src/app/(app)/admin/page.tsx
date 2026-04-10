"use client";

import { useState, useEffect, useCallback, useRef } from "react";
import { useQuery, useMutation } from "urql";
import { HEALTH_QUERY, REPOSITORIES_LIGHT_QUERY as REPOSITORIES_QUERY, REINDEX_REPOSITORY_MUTATION } from "@/lib/graphql/queries";
import { Button } from "@/components/ui/button";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { StatCard } from "@/components/ui/stat-card";
import { TOKEN_KEY } from "@/lib/token-key";
import { cn } from "@/lib/utils";

type Tab = "status" | "llm" | "auth" | "repos" | "git" | "knowledge";

interface AdminStatus {
  version: string;
  commit: string;
  uptime: string;
  database: string;
  worker: string;
  env: string;
  knowledge?: {
    configured: boolean;
    artifacts?: {
      total: number;
      ready: number;
      stale: number;
      generating: number;
      failed: number;
      pending: number;
      by_type: Record<string, number>;
    };
  };
}

interface KnowledgeAdminStatus {
  configured: boolean;
  stats?: {
    total: number;
    ready: number;
    stale: number;
    generating: number;
    failed: number;
    pending: number;
    by_type: Record<string, number>;
  };
  repositories?: Array<{
    repo_id: string;
    repo_name: string;
    artifacts: Array<{
      id: string;
      type: string;
      status: string;
      stale: boolean;
      audience: string;
      depth: string;
      generated_at?: string;
      commit_sha?: string;
    }>;
  }>;
}

interface AdminConfig {
  llm: { provider: string; base_url: string; summary_model: string; review_model: string; ask_model: string };
  security: { csrf_enabled: boolean; mode: string; oidc_configured: boolean };
  worker: { address: string };
  git?: { default_token_set: boolean; ssh_key_path: string };
}

interface LLMConfigState {
  provider: string;
  base_url: string;
  api_key_set: boolean;
  api_key_hint?: string;
  summary_model: string;
  review_model: string;
  ask_model: string;
  knowledge_model: string;
  draft_model: string;
  timeout_secs: number;
  advanced_mode: boolean;
}

interface GitConfigState {
  default_token_set: boolean;
  default_token_hint?: string;
  ssh_key_path: string;
}

/** Parse an API error response into a user-friendly message.
 *  On 401, clears the token and redirects to login. */
async function handleApiError(res: Response): Promise<string> {
  if (res.status === 401) {
    localStorage.removeItem(TOKEN_KEY);
    window.location.href = "/login";
    return "Session expired — redirecting to login...";
  }
  const text = await res.text();
  // Try to parse JSON error body
  try {
    const json = JSON.parse(text);
    if (json.error) return json.error;
  } catch { /* not JSON */ }
  // Check for HTML responses (e.g. proxy error pages)
  if (text.trimStart().startsWith("<")) {
    return `Server error (HTTP ${res.status}). The API may be restarting — try again in a moment.`;
  }
  return text || `HTTP ${res.status}`;
}

function useAdminFetch<T>(path: string) {
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const fetchData = useCallback(async () => {
    setLoading(true);
    try {
      const token = localStorage.getItem(TOKEN_KEY);
      const res = await fetch(path, { headers: { Authorization: `Bearer ${token}` } });
      if (!res.ok) throw new Error(await handleApiError(res));
      setData(await res.json());
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
    setLoading(false);
  }, [path]);

  useEffect(() => { fetchData(); }, [fetchData]);

  return { data, loading, error, refetch: fetchData };
}

export default function AdminPage() {
  const [tab, setTab] = useState<Tab>("status");
  const { data: status, refetch: refetchStatus } = useAdminFetch<AdminStatus>("/api/v1/admin/status");
  const { data: config } = useAdminFetch<AdminConfig>("/api/v1/admin/config");
  const { data: knowledgeStatus, refetch: refetchKnowledge } = useAdminFetch<KnowledgeAdminStatus>("/api/v1/admin/knowledge");
  const [healthResult] = useQuery({ query: HEALTH_QUERY });
  const [reposResult] = useQuery({ query: REPOSITORIES_QUERY });
  const [, reindex] = useMutation(REINDEX_REPOSITORY_MUTATION);
  const [testResult, setTestResult] = useState<string | null>(null);
  const { data: gitConfig, refetch: refetchGitConfig } = useAdminFetch<GitConfigState>("/api/v1/admin/git-config");
  const { data: llmConfig, refetch: refetchLLMConfig } = useAdminFetch<LLMConfigState>("/api/v1/admin/llm-config");
  const [llmProvider, setLlmProvider] = useState("");
  const [llmBaseURL, setLlmBaseURL] = useState("");
  const [llmAPIKey, setLlmAPIKey] = useState("");
  const [llmSummaryModel, setLlmSummaryModel] = useState("");
  const [llmReviewModel, setLlmReviewModel] = useState("");
  const [llmAskModel, setLlmAskModel] = useState("");
  const [llmKnowledgeModel, setLlmKnowledgeModel] = useState("");
  const [llmTimeoutSecs, setLlmTimeoutSecs] = useState(30);
  const [llmAdvancedMode, setLlmAdvancedMode] = useState(false);
  const [llmDraftModel, setLlmDraftModel] = useState("");
  const [llmSaving, setLlmSaving] = useState(false);
  const [llmMessage, setLlmMessage] = useState<string | null>(null);
  const [llmSuccess, setLlmSuccess] = useState(false);
  const [llmModels, setLlmModels] = useState<{ id: string; name?: string; context_window?: number; max_output?: number; price_tier?: string }[]>([]);
  const [llmModelFilter, setLlmModelFilter] = useState("");
  const [llmModelsLoading, setLlmModelsLoading] = useState(false);
  const [llmModelsError, setLlmModelsError] = useState<string | null>(null);
  const [gitToken, setGitToken] = useState("");
  const [gitSSHKeyPath, setGitSSHKeyPath] = useState("");
  const [gitSaving, setGitSaving] = useState(false);
  const [gitMessage, setGitMessage] = useState<string | null>(null);
  const [gitSuccess, setGitSuccess] = useState(false);

  const providerDefaults: Record<string, { baseURL: string; model: string }> = {
    openai: { baseURL: "https://api.openai.com/v1", model: "gpt-4o" },
    anthropic: { baseURL: "https://api.anthropic.com", model: "claude-sonnet-4-20250514" },
    ollama: { baseURL: "http://localhost:11434", model: "" },
    vllm: { baseURL: "http://localhost:8000/v1", model: "" },
    "llama-cpp": { baseURL: "http://localhost:8080/v1", model: "" },
    sglang: { baseURL: "http://localhost:30000/v1", model: "" },
    lmstudio: { baseURL: "http://localhost:1234/v1", model: "" },
    gemini: { baseURL: "https://generativelanguage.googleapis.com/v1beta/openai/", model: "gemini-2.5-flash" },
    openrouter: { baseURL: "https://openrouter.ai/api/v1", model: "google/gemini-2.5-flash" },
  };

  const fetchModels = useCallback(async (provider: string, baseURL: string) => {
    setLlmModelsLoading(true);
    setLlmModelsError(null);
    try {
      const token = localStorage.getItem(TOKEN_KEY);
      const params = new URLSearchParams();
      if (provider) params.set("provider", provider);
      if (baseURL) params.set("base_url", baseURL);
      const res = await fetch(`/api/v1/admin/llm-models?${params}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!res.ok) throw new Error(await handleApiError(res));
      const data = await res.json();
      setLlmModels(data.models || []);
      if (data.error) setLlmModelsError(data.error);
    } catch (e) {
      setLlmModels([]);
      setLlmModelsError((e as Error).message);
    }
    setLlmModelsLoading(false);
  }, []);

  const filteredModels = llmModelFilter
    ? llmModels.filter((m) =>
        m.id.toLowerCase().includes(llmModelFilter.toLowerCase()) ||
        (m.name && m.name.toLowerCase().includes(llmModelFilter.toLowerCase()))
      )
    : llmModels;

  function formatCtx(n?: number) {
    if (!n) return "";
    if (n >= 1000000) return `${Math.round(n / 1000)}K ctx`;
    if (n >= 1000) return `${Math.round(n / 1000)}K ctx`;
    return `${n} ctx`;
  }

  // Track whether initial load from server config has happened
  const initialLoadDone = useRef(false);

  useEffect(() => {
    if (llmConfig) {
      setLlmProvider(llmConfig.provider || "ollama");
      setLlmBaseURL(llmConfig.base_url || "");
      setLlmSummaryModel(llmConfig.summary_model || "");
      setLlmReviewModel(llmConfig.review_model || "");
      setLlmAskModel(llmConfig.ask_model || "");
      setLlmKnowledgeModel(llmConfig.knowledge_model || "");
      setLlmTimeoutSecs(llmConfig.timeout_secs || 30);
      setLlmAdvancedMode(llmConfig.advanced_mode || false);
      setLlmDraftModel(llmConfig.draft_model || "");
      initialLoadDone.current = true;
      // Fetch models for the configured provider
      fetchModels(llmConfig.provider || "ollama", llmConfig.base_url || "");
    }
  }, [llmConfig, fetchModels]);

  function handleProviderChange(newProvider: string) {
    setLlmProvider(newProvider);
    const defaults = providerDefaults[newProvider];
    if (defaults) {
      setLlmBaseURL(defaults.baseURL);
      if (defaults.model) {
        setLlmSummaryModel(defaults.model);
        setLlmReviewModel(defaults.model);
        setLlmAskModel(defaults.model);
        setLlmKnowledgeModel(defaults.model);
      }
      fetchModels(newProvider, defaults.baseURL);
    }
  }

  useEffect(() => {
    if (gitConfig) {
      setGitSSHKeyPath(gitConfig.ssh_key_path || "");
    }
  }, [gitConfig]);

  async function saveLLMConfig() {
    if (llmSaving) return;
    setLlmSaving(true);
    setLlmMessage(null);
    setLlmSuccess(false);
    try {
      const token = localStorage.getItem(TOKEN_KEY);
      const body: Record<string, unknown> = {
        provider: llmProvider,
        base_url: llmBaseURL,
        summary_model: llmSummaryModel,
        review_model: llmReviewModel,
        ask_model: llmAskModel,
        knowledge_model: llmKnowledgeModel,
        draft_model: llmDraftModel,
        timeout_secs: llmTimeoutSecs,
        advanced_mode: llmAdvancedMode,
      };
      if (llmAPIKey) body.api_key = llmAPIKey;
      const res = await fetch("/api/v1/admin/llm-config", {
        method: "PUT",
        headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok) throw new Error(await handleApiError(res));
      const data = await res.json();
      setLlmMessage("LLM configuration saved. " + (data.note || ""));
      setLlmSuccess(true);
      setLlmAPIKey("");
      refetchLLMConfig();
    } catch (e) {
      setLlmSuccess(false);
      setLlmMessage(`Error: ${(e as Error).message}`);
    }
    setLlmSaving(false);
  }

  async function saveGitConfig() {
    if (gitSaving) return; // prevent double-click
    setGitSaving(true);
    setGitMessage(null);
    setGitSuccess(false);
    try {
      const token = localStorage.getItem(TOKEN_KEY);
      const body: Record<string, string> = {};
      if (gitToken) body.default_token = gitToken;
      body.ssh_key_path = gitSSHKeyPath;
      const res = await fetch("/api/v1/admin/git-config", {
        method: "PUT",
        headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok) throw new Error(await handleApiError(res));
      const data = await res.json();
      setGitMessage("Token saved successfully. " + (data.note || ""));
      setGitSuccess(true);
      setGitToken("");
      refetchGitConfig();
    } catch (e) {
      setGitSuccess(false);
      setGitMessage(`Error: ${(e as Error).message}`);
    }
    setGitSaving(false);
  }

  const repos = reposResult.data?.repositories || [];

  async function testEndpoint(path: string) {
    setTestResult(null);
    const token = localStorage.getItem(TOKEN_KEY);
    const res = await fetch(path, { method: "POST", headers: { Authorization: `Bearer ${token}` } });
    const data = await res.json();
    setTestResult(JSON.stringify(data, null, 2));
  }

  function isLocalProvider(provider: string): boolean {
    return ["ollama", "vllm", "llama-cpp", "sglang", "lmstudio"].includes(provider);
  }

  const tabs: { key: Tab; label: string }[] = [
    { key: "status", label: "System Status" },
    { key: "llm", label: "LLM Configuration" },
    { key: "auth", label: "Authentication" },
    { key: "repos", label: "Repositories" },
    { key: "git", label: "Git Credentials" },
    { key: "knowledge", label: "Knowledge" },
  ];

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
  const messageClass = (success: boolean) =>
    cn(
      "rounded-[var(--radius-md)] border px-3 py-2 text-sm",
      success
        ? "border-[var(--color-success,#22c55e)] bg-[rgba(34,197,94,0.1)] text-[var(--color-success,#22c55e)]"
        : "border-[var(--color-error,#ef4444)] bg-[rgba(239,68,68,0.1)] text-[var(--color-error,#ef4444)]"
    );
  const badgeClass = (status: string) => {
    const color =
      status === "healthy" || status === "ok"
        ? "var(--color-success, #22c55e)"
        : status === "degraded"
          ? "var(--color-warning, #eab308)"
          : "var(--color-error, #ef4444)";
    return {
      borderColor: color,
      color,
    };
  };

  function StatusBadge({ status }: { status: string }) {
    const statusStyle = badgeClass(status);
    return (
      <span
        className="rounded-full border px-2 py-0.5 text-xs"
        style={statusStyle}
      >
        {status}
      </span>
    );
  }

  return (
    <PageFrame>
      <PageHeader
        eyebrow="Operations"
        title="Admin"
        description="Monitor service health, configure providers, and manage repository-level operational settings."
        actions={
          <div className="flex items-center gap-2">
            <a
              href="/admin/settings/comprehension"
              className="inline-flex items-center gap-2 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-sm font-medium text-[var(--text-primary)] transition-colors hover:bg-[var(--bg-hover)]"
            >
              Comprehension Settings →
            </a>
            <a
              href="/admin/llm"
              className="inline-flex items-center gap-2 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-sm font-medium text-[var(--text-primary)] transition-colors hover:bg-[var(--bg-hover)]"
            >
              Generation Monitor →
            </a>
          </div>
        }
      />

      <div className="-mx-3 flex gap-2 overflow-x-auto border-b border-[var(--border-subtle)] px-3 pb-4 sm:mx-0 sm:flex-wrap sm:overflow-visible sm:px-0">
        {tabs.map((t) => (
          <button
            key={t.key}
            onClick={() => setTab(t.key)}
            className={cn(
              "shrink-0 rounded-[var(--control-radius)] border px-3 py-2 text-sm transition-colors",
              tab === t.key
                ? "border-[var(--nav-item-border)] bg-[var(--nav-item-bg-active)] font-medium text-[var(--text-primary)]"
                : "border-transparent text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
            )}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === "status" && (
        <div className="space-y-6">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 sm:gap-4 xl:grid-cols-4">
            {status && (
              <>
                <StatCard label="Version" value={status.version} detail={`Commit ${status.commit?.slice(0, 8) || "—"}`} />
                <StatCard label="Uptime" value={status.uptime} />
                <Panel>
                  <div className="text-sm text-[var(--text-secondary)]">Database</div>
                  <StatusBadge status={status.database} />
                </Panel>
                <Panel>
                  <div className="text-sm text-[var(--text-secondary)]">Worker</div>
                  <StatusBadge status={status.worker} />
                </Panel>
              </>
            )}
          </div>

          <div className="flex flex-wrap gap-3">
            <Button onClick={() => testEndpoint("/api/v1/admin/test-worker")}>
              Test Worker
            </Button>
            <Button onClick={() => testEndpoint("/api/v1/admin/test-llm")}>
              Test LLM
            </Button>
            <Button variant="secondary" onClick={() => { refetchStatus(); setTestResult(null); }}>
              Refresh
            </Button>
          </div>

          {testResult && (
            <Panel>
              <pre className={codeBlockClass}>{testResult}</pre>
            </Panel>
          )}

          {healthResult.data?.health && (
            <Panel>
              <h3 className="mb-2 text-base font-semibold text-[var(--text-primary)]">GraphQL Health</h3>
              <p className="text-sm text-[var(--text-primary)]">Status: {healthResult.data.health.status}</p>
              {healthResult.data.health.services?.map((svc: { name: string; status: string }) => (
                <div key={svc.name} className="flex justify-between py-1 text-sm text-[var(--text-primary)]">
                  <span>{svc.name}</span>
                  <StatusBadge status={svc.status} />
                </div>
              ))}
            </Panel>
          )}
        </div>
      )}

      {tab === "llm" && (
        <div>
          <Panel className="mb-4">
            <h3 className="mb-2 text-base font-semibold text-[var(--text-primary)]">LLM Configuration</h3>
            <p className="mb-4 text-sm text-[var(--text-secondary)]">
              Configure the AI model provider and models used for code analysis, review, and chat.
            </p>

            <div className={stackClass}>
              <div className={fieldWrapClass}>
                <label className={labelClass}>Provider</label>
                <select
                  value={llmProvider}
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
                    value={llmBaseURL}
                    onChange={(e) => setLlmBaseURL(e.target.value)}
                    placeholder={providerDefaults[llmProvider]?.baseURL || "http://localhost:11434"}
                    className={`flex-1 ${monoInputClass}`}
                  />
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => fetchModels(llmProvider, llmBaseURL)}
                    disabled={llmModelsLoading}
                  >
                    {llmModelsLoading ? "Loading..." : "Refresh models"}
                  </Button>
                </div>
                <p className={helpTextClass}>
                  {llmProvider === "ollama" || llmProvider === "vllm"
                    ? "Required for local providers. Include /v1 suffix for OpenAI-compatible endpoints."
                    : llmProvider === "llama-cpp"
                      ? "llama.cpp server with OpenAI-compatible API. Supports speculative decoding when launched with --model-draft."
                      : llmProvider === "sglang"
                        ? "SGLang server with OpenAI-compatible API. Supports EAGLE-based speculative decoding at launch."
                        : llmProvider === "lmstudio"
                          ? "LM Studio with OpenAI-compatible API. Supports per-request speculative decoding via draft model."
                          : llmProvider === "openrouter"
                          ? "OpenRouter uses the OpenAI-compatible API. Models from 300+ providers available."
                          : llmProvider === "gemini"
                            ? "Google Gemini uses an OpenAI-compatible endpoint. Default URL works for most setups."
                            : "Default URL for this provider. Change it to use a custom proxy or endpoint."}
                </p>
              </div>

              {(llmProvider === "anthropic" || llmProvider === "openai" || llmProvider === "gemini" || llmProvider === "openrouter") && (
                <div className={fieldWrapClass}>
                  <label className={labelClass}>API Key</label>
                  <div className="flex items-center gap-2">
                    <input
                      type="password"
                      value={llmAPIKey}
                      onChange={(e) => setLlmAPIKey(e.target.value)}
                      placeholder={llmConfig?.api_key_set ? "Key is configured (enter new to replace)" : llmProvider === "anthropic" ? "sk-ant-..." : llmProvider === "gemini" ? "AIza..." : "sk-..."}
                      className={`flex-1 ${monoInputClass}`}
                    />
                    {llmConfig?.api_key_set && (
                      <span className="whitespace-nowrap font-mono text-xs text-[var(--color-success,#22c55e)]">
                        {llmConfig.api_key_hint || "Configured"}
                      </span>
                    )}
                  </div>
                  <p className={helpTextClass}>
                    Required for cloud providers. After saving a new key, click &quot;Refresh models&quot; to load the model list.
                  </p>
                </div>
              )}

              {llmProvider === "lmstudio" && (
                <div className={fieldWrapClass}>
                  <label className={labelClass}>Draft Model (Speculative Decoding)</label>
                  <input
                    type="text"
                    value={llmDraftModel}
                    onChange={(e) => setLlmDraftModel(e.target.value)}
                    placeholder="e.g. lmstudio-community/Qwen2.5-0.5B-Instruct-GGUF"
                    className={monoInputClass}
                  />
                  <p className={helpTextClass}>
                    Optional. Smaller model used for speculative decoding. LM Studio sends candidate tokens from this model
                    and verifies them with the main model in a single pass, improving throughput 1.5-3x.
                  </p>
                </div>
              )}

              <div className={fieldWrapClass}>
                <label className={labelClass}>Model {llmAdvancedMode && "(Analysis / Default)"}</label>
                {llmModels.length > 0 ? (
                  <div className="space-y-2">
                    {llmModels.length > 20 && (
                      <input
                        type="text"
                        value={llmModelFilter}
                        onChange={(e) => setLlmModelFilter(e.target.value)}
                        placeholder="Filter models..."
                        className={inputClass}
                      />
                    )}
                    <select
                      value={llmModels.some((m) => m.id === llmSummaryModel) ? llmSummaryModel : "__custom__"}
                      onChange={(e) => {
                        if (e.target.value !== "__custom__") {
                          setLlmSummaryModel(e.target.value);
                          if (!llmAdvancedMode) {
                            setLlmReviewModel(e.target.value);
                            setLlmAskModel(e.target.value);
                            setLlmKnowledgeModel(e.target.value);
                          }
                        }
                      }}
                      className={selectClass}
                    >
                      {!llmModels.some((m) => m.id === llmSummaryModel) && llmSummaryModel && (
                        <option value="__custom__">{llmSummaryModel} (custom)</option>
                      )}
                      {filteredModels.map((m) => (
                        <option key={m.id} value={m.id}>
                          {m.name ? `${m.name} (${m.id})` : m.id}{m.context_window ? ` [${formatCtx(m.context_window)}]` : ""}
                        </option>
                      ))}
                    </select>
                    <input
                      type="text"
                      value={llmSummaryModel}
                      onChange={(e) => {
                        setLlmSummaryModel(e.target.value);
                        if (!llmAdvancedMode) {
                          setLlmReviewModel(e.target.value);
                          setLlmAskModel(e.target.value);
                          setLlmKnowledgeModel(e.target.value);
                        }
                      }}
                      placeholder="Or type a model ID manually"
                      className={monoInputClass}
                    />
                  </div>
                ) : (
                  <input
                    type="text"
                    value={llmSummaryModel}
                    onChange={(e) => {
                      setLlmSummaryModel(e.target.value);
                      if (!llmAdvancedMode) {
                        setLlmReviewModel(e.target.value);
                        setLlmAskModel(e.target.value);
                        setLlmKnowledgeModel(e.target.value);
                      }
                    }}
                    placeholder={providerDefaults[llmProvider]?.model || "model name"}
                    className={monoInputClass}
                  />
                )}
                <p className={helpTextClass}>
                  {llmModelsLoading
                    ? "Loading available models..."
                    : llmModelsError
                      ? `Could not load models: ${llmModelsError}`
                      : llmModels.length > 0
                        ? `${llmModels.length} model${llmModels.length !== 1 ? "s" : ""} available. Select from the list or type a custom model ID.`
                        : "Used for code summaries, reviews, and chat. All three tasks use the same model by default."}
                </p>
              </div>

              <div className="flex items-center gap-3">
                <label className="relative inline-flex cursor-pointer items-center">
                  <input
                    type="checkbox"
                    checked={llmAdvancedMode}
                    onChange={(e) => setLlmAdvancedMode(e.target.checked)}
                    className="peer sr-only"
                  />
                  <div className="peer h-5 w-9 rounded-full bg-[var(--border-default)] after:absolute after:left-[2px] after:top-[2px] after:h-4 after:w-4 after:rounded-full after:border after:border-gray-300 after:bg-white after:transition-all after:content-[''] peer-checked:bg-[hsl(var(--accent-hue,250),60%,60%)] peer-checked:after:translate-x-full peer-checked:after:border-white" />
                </label>
                <div>
                  <span className={labelClass}>Advanced: Per-operation models</span>
                  <p className={helpTextClass}>Use different models for different operation types based on token weight and quality needs.</p>
                </div>
              </div>

              {llmAdvancedMode && (
                <div className="rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--bg-raised)] p-4 space-y-4">
                  <p className="text-sm text-[var(--text-secondary)]">
                    Assign models to operation groups. The default model above is used for Analysis. Empty fields fall back to the default.
                  </p>

                  {([
                    { label: "Code Review", key: "review", value: llmReviewModel, setter: setLlmReviewModel, badge: "Medium ~5K tok", help: "reviewCode (all templates)" },
                    { label: "Discussion & Q&A", key: "discussion", value: llmAskModel, setter: setLlmAskModel, badge: "Medium ~1-5K tok", help: "discussCode, answerQuestion" },
                    { label: "Knowledge Generation", key: "knowledge", value: llmKnowledgeModel, setter: setLlmKnowledgeModel, badge: "High ~10-37K tok", help: "cliffNotes, learningPath, codeTour, workflowStory, explainSystem" },
                  ] as const).map((group) => (
                    <div key={group.key} className={fieldWrapClass}>
                      <div className="flex items-center gap-2">
                        <label className={labelClass}>{group.label}</label>
                        <span className="rounded-full bg-[var(--bg-base)] px-2 py-0.5 text-[10px] font-medium text-[var(--text-secondary)] border border-[var(--border-subtle)]">
                          {group.badge}
                        </span>
                      </div>
                      {llmModels.length > 0 ? (
                        <select
                          value={llmModels.some((m) => m.id === group.value) ? group.value : "__custom__"}
                          onChange={(e) => { if (e.target.value !== "__custom__") group.setter(e.target.value); }}
                          className={selectClass}
                        >
                          {!llmModels.some((m) => m.id === group.value) && group.value && (
                            <option value="__custom__">{group.value} (custom)</option>
                          )}
                          {filteredModels.map((m) => (
                            <option key={m.id} value={m.id}>
                              {m.name ? `${m.name} (${m.id})` : m.id}{m.context_window ? ` [${formatCtx(m.context_window)}]` : ""}
                            </option>
                          ))}
                        </select>
                      ) : (
                        <input
                          type="text"
                          value={group.value}
                          onChange={(e) => group.setter(e.target.value)}
                          placeholder={llmSummaryModel || "same as default"}
                          className={monoInputClass}
                        />
                      )}
                      <p className={helpTextClass}>{group.help}</p>
                    </div>
                  ))}
                </div>
              )}

              <div className={fieldWrapClass}>
                <label className={labelClass}>Timeout (seconds)</label>
                <input
                  type="number"
                  value={llmTimeoutSecs}
                  onChange={(e) => setLlmTimeoutSecs(parseInt(e.target.value) || 30)}
                  min={5}
                  max={300}
                  className="h-11 w-32 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)]"
                />
              </div>

              <div className="flex items-center gap-2">
                <Button onClick={saveLLMConfig} disabled={llmSaving}>
                  {llmSaving ? "Saving..." : "Save"}
                </Button>
                <Button variant="secondary" onClick={() => testEndpoint("/api/v1/admin/test-llm")}>
                  Test Connection
                </Button>
              </div>

              {llmMessage && (
                <p className={messageClass(llmSuccess)}>{llmMessage}</p>
              )}

              {testResult && (
                <pre className={codeBlockClass}>{testResult}</pre>
              )}
            </div>
          </Panel>

          {isLocalProvider(llmProvider) && (
            <Panel>
              <h3 className="mb-2 text-base font-semibold text-[var(--text-primary)]">Speculative Decoding</h3>
              <p className="text-sm text-[var(--text-secondary)]">
                {llmProvider === "lmstudio"
                  ? "LM Studio supports per-request speculative decoding via the Draft Model field above. Configure a smaller draft model for 1.5-3x throughput improvement."
                  : llmProvider === "llama-cpp"
                    ? "llama.cpp supports speculative decoding when launched with --model-draft. Performance metrics (tokens/sec, acceptance rate) appear in operation results."
                    : llmProvider === "sglang"
                      ? "SGLang supports EAGLE-based speculative decoding configured at server launch. Performance metrics appear in operation results."
                      : llmProvider === "vllm"
                        ? "vLLM supports EAGLE3 speculative decoding configured at server launch. Performance metrics appear in operation results."
                        : "Performance metrics (tokens/sec) from your local inference server appear in operation results when available."}
              </p>
              <p className="mt-1 text-xs text-[var(--text-secondary)]">
                Tip: Higher tokens/sec indicates speculative decoding is working. Acceptance rate below 60% means the draft model is a poor match for the target model.
              </p>
            </Panel>
          )}

          {config && (
            <Panel>
              <h3 className="mb-2 text-base font-semibold text-[var(--text-primary)]">Worker Connection</h3>
              <div className="text-sm">
                <span className="text-[var(--text-secondary)]">Worker Address: </span>
                <span className="font-mono text-[var(--text-primary)]">{config.worker.address}</span>
              </div>
              <p className="mt-2 text-xs text-[var(--text-secondary)]">
                The Python worker handles LLM calls. Worker address is configured via SOURCEBRIDGE_WORKER_GRPC_ADDRESS environment variable.
              </p>
            </Panel>
          )}
        </div>
      )}

      {tab === "auth" && config && (
        <div>
          <Panel className="mb-4">
            <h3 className="mb-3 text-base font-semibold text-[var(--text-primary)]">Authentication</h3>
            <div className="grid gap-2 text-sm text-[var(--text-primary)]">
              <div>
                <span className="text-[var(--text-secondary)]">Mode: </span>
                <span className="font-medium">{config.security.mode}</span>
              </div>
              <div>
                <span className="text-[var(--text-secondary)]">CSRF Enabled: </span>
                <span>{config.security.csrf_enabled ? "Yes" : "No"}</span>
              </div>
              <div>
                <span className="text-[var(--text-secondary)]">OIDC Configured: </span>
                <span>{config.security.oidc_configured ? "Yes" : "No"}</span>
              </div>
            </div>
          </Panel>
          <Panel>
            <h3 className="mb-3 text-base font-semibold text-[var(--text-primary)]">Change Password</h3>
            <p className="text-sm text-[var(--text-secondary)]">
              Use the <a href="/settings" className="text-[var(--accent-primary)]">Settings</a> page to change your password.
            </p>
          </Panel>
        </div>
      )}

      {tab === "repos" && (
        <Panel>
          <h3 className="mb-4 text-base font-semibold text-[var(--text-primary)]">Repository Management ({repos.length})</h3>
          {repos.length === 0 ? (
            <p className="text-sm text-[var(--text-secondary)]">No repositories indexed.</p>
          ) : (
            <div>
              {repos.map((repo: { id: string; name: string; status: string; fileCount: number }) => (
                <div key={repo.id} className="flex items-center justify-between border-b border-[var(--border-default)] py-2 text-sm last:border-b-0">
                  <div>
                    <span className="font-medium text-[var(--text-primary)]">{repo.name}</span>
                    <span className="ml-3 text-[var(--text-secondary)]">{repo.fileCount} files</span>
                  </div>
                  <div className="flex items-center gap-2">
                    <StatusBadge status={repo.status === "READY" ? "healthy" : repo.status.toLowerCase()} />
                    <Button size="sm" variant="secondary" onClick={() => reindex({ id: repo.id })}>
                      Reindex
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </Panel>
      )}

      {tab === "git" && (
        <div>
          <Panel className="mb-4">
            <h3 className="mb-2 text-base font-semibold text-[var(--text-primary)]">Git Credentials</h3>
            <p className="mb-4 text-sm text-[var(--text-secondary)]">
              Configure credentials for cloning and updating private repositories. Per-repository tokens override the default.
            </p>

            <div className={stackClass}>
              <div className={fieldWrapClass}>
                <label className={labelClass}>Default Access Token (PAT)</label>
                <div className="flex items-center gap-2">
                  <input
                    type="password"
                    value={gitToken}
                    onChange={(e) => setGitToken(e.target.value)}
                    placeholder={gitConfig?.default_token_set ? "Token is configured (enter new to replace)" : "ghp_... or glpat-..."}
                    className={`flex-1 ${monoInputClass}`}
                  />
                  {gitConfig?.default_token_set && (
                    <span className="whitespace-nowrap font-mono text-xs text-[var(--color-success,#22c55e)]">
                      {gitConfig.default_token_hint || "Configured"}
                    </span>
                  )}
                </div>
                <p className={helpTextClass}>
                  Works with GitHub, GitLab, and Bitbucket personal access tokens for HTTPS repos.
                </p>
              </div>

              <div className={fieldWrapClass}>
                <label className={labelClass}>SSH Private Key Path</label>
                <input
                  type="text"
                  value={gitSSHKeyPath}
                  onChange={(e) => setGitSSHKeyPath(e.target.value)}
                  placeholder="~/.ssh/id_ed25519"
                  className={monoInputClass}
                />
                <p className={helpTextClass}>
                  Used for git@ SSH URLs. Leave empty to use the system SSH agent.
                </p>
              </div>

              <div className="flex items-center gap-2">
                <Button onClick={saveGitConfig} disabled={gitSaving}>
                  {gitSaving ? "Saving..." : "Save"}
                </Button>
              </div>

              {gitMessage && (
                <p className={messageClass(gitSuccess)}>{gitMessage}</p>
              )}
            </div>
          </Panel>

          <Panel>
            <h3 className="mb-2 text-base font-semibold text-[var(--text-primary)]">Environment Variables</h3>
            <p className="mb-3 text-sm text-[var(--text-secondary)]">
              For persistent configuration across server restarts, set these environment variables:
            </p>
            <div className={codeBlockClass}>
              <div>SOURCEBRIDGE_GIT_DEFAULT_TOKEN=ghp_your_token</div>
              <div>SOURCEBRIDGE_GIT_SSH_KEY_PATH=/path/to/key</div>
            </div>
          </Panel>
        </div>
      )}

      {tab === "knowledge" && (
        <div>
          <div className="mb-4 flex items-center justify-between">
            <h3 className="text-base font-semibold text-[var(--text-primary)]">Knowledge Engine</h3>
            <Button size="sm" variant="secondary" onClick={() => refetchKnowledge()}>
              Refresh
            </Button>
          </div>

          {knowledgeStatus && !knowledgeStatus.configured && (
            <Panel>
              <p className="text-sm text-[var(--text-secondary)]">Knowledge store not configured.</p>
            </Panel>
          )}

          {knowledgeStatus?.stats && (
            <div className="mb-6 grid grid-cols-2 gap-3 sm:gap-4 md:grid-cols-3 xl:grid-cols-5">
              <StatCard label="Total Artifacts" value={knowledgeStatus.stats.total} />
              <StatCard label="Ready" value={knowledgeStatus.stats.ready} />
              <StatCard label="Stale" value={knowledgeStatus.stats.stale} />
              <StatCard label="Failed" value={knowledgeStatus.stats.failed} />
              <StatCard label="Generating" value={knowledgeStatus.stats.generating} />
            </div>
          )}

          {knowledgeStatus?.stats?.by_type && Object.keys(knowledgeStatus.stats.by_type).length > 0 && (
            <Panel className="mb-6">
              <h4 className="mb-2 text-sm font-medium text-[var(--text-primary)]">By Type</h4>
              {Object.entries(knowledgeStatus.stats.by_type).map(([type, count]) => (
                <div key={type} className="flex justify-between py-1 text-sm text-[var(--text-primary)]">
                  <span>{type.replace(/_/g, " ")}</span>
                  <span className="font-medium">{count}</span>
                </div>
              ))}
            </Panel>
          )}

          {knowledgeStatus?.repositories && knowledgeStatus.repositories.length > 0 && (
            <Panel>
              <h4 className="mb-3 text-sm font-medium text-[var(--text-primary)]">Per-Repository Status</h4>
              {knowledgeStatus.repositories.map((repo) => (
                <div key={repo.repo_id} className="mb-4 last:mb-0">
                  <div className="mb-1 text-sm font-medium text-[var(--text-primary)]">{repo.repo_name}</div>
                  {repo.artifacts.map((a) => (
                    <div key={a.id} className="flex items-center justify-between border-b border-[var(--border-default)] px-2 py-1.5 text-xs last:border-b-0">
                      <span>{a.type.replace(/_/g, " ")} ({a.audience}/{a.depth})</span>
                      <div className="flex items-center gap-2">
                        {a.stale && (
                          <span className="rounded-full border border-[var(--border-default)] bg-[var(--bg-hover)] px-1.5 py-0.5 text-[var(--text-secondary)]">stale</span>
                        )}
                        <StatusBadge status={a.status === "ready" ? "healthy" : a.status === "failed" ? "error" : a.status} />
                        {a.generated_at && (
                          <span className="text-[var(--text-tertiary)]">{new Date(a.generated_at).toLocaleDateString()}</span>
                        )}
                      </div>
                    </div>
                  ))}
                </div>
              ))}
            </Panel>
          )}
        </div>
      )}
    </PageFrame>
  );
}
