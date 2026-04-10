"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";

import { Button } from "@/components/ui/button";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { TOKEN_KEY } from "@/lib/token-key";
import { cn } from "@/lib/utils";

/**
 * Comprehension Settings page (/admin/settings/comprehension).
 *
 * Phase 6 settings UI for the comprehension engine. Two modes:
 *   - **Simple** (default): 3 cards — model picker + recommended setup,
 *     strategy matrix, live monitor snapshot.
 *   - **Advanced**: overrides, orchestration knobs, model management.
 *
 * Follows the plan's "under 60 seconds for a new operator to configure"
 * success criterion.
 */

// --- Types ---

interface FieldOrigin {
  field: string;
  scopeType: string;
  scopeKey: string;
}

interface EffectiveSettings {
  scopeType: string;
  scopeKey: string;
  strategyPreferenceChain: string[];
  modelId: string;
  maxConcurrency: number;
  maxPromptTokens: number;
  leafBudgetTokens: number;
  refinePassEnabled: boolean;
  longContextMaxTokens: number;
  graphragEntityTypes: string[];
  cacheEnabled: boolean;
  allowUnsafeCombinations: boolean;
  inheritedFrom?: FieldOrigin[];
}

interface ModelCapability {
  id?: string;
  modelId: string;
  provider: string;
  declaredContextTokens: number;
  effectiveContextTokens: number;
  instructionFollowing: string;
  jsonMode: string;
  toolUse: string;
  extractionGrade: string;
  creativeGrade: string;
  embeddingModel: boolean;
  costPer1kInput?: number;
  costPer1kOutput?: number;
  lastProbedAt?: string;
  source: string;
  notes?: string;
}

interface HealthPayload {
  status: "healthy" | "degraded" | "unhealthy";
  summary: string;
  active_count: number;
  recent_failed: number;
  recent_succeeded: number;
}

// --- Strategy metadata ---

const STRATEGIES: Record<string, { label: string; desc: string; badge: string }> = {
  hierarchical: {
    label: "Hierarchical",
    desc: "Decomposes large codebases into a summary tree. Works on any model.",
    badge: "Recommended",
  },
  single_shot: {
    label: "Single Shot",
    desc: "Legacy single-call path. Only for small repos with large-context models.",
    badge: "Legacy",
  },
  long_context: {
    label: "Long Context Direct",
    desc: "Single call with the full corpus. Requires 32K+ context model.",
    badge: "Cloud",
  },
};

// --- Helpers ---

function gradeBadge(grade: string) {
  const colors: Record<string, string> = {
    high: "bg-green-500/20 text-green-400 border-green-500/30",
    medium: "bg-amber-500/20 text-amber-400 border-amber-500/30",
    low: "bg-red-500/20 text-red-400 border-red-500/30",
    none: "bg-gray-500/20 text-gray-400 border-gray-500/30",
    native: "bg-green-500/20 text-green-400 border-green-500/30",
    prompted: "bg-amber-500/20 text-amber-400 border-amber-500/30",
    supported: "bg-green-500/20 text-green-400 border-green-500/30",
  };
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium",
        colors[grade] || colors.none
      )}
    >
      {grade}
    </span>
  );
}

function formatContext(tokens: number): string {
  if (tokens >= 1_000_000) return `${(tokens / 1_000_000).toFixed(1)}M`;
  if (tokens >= 1_000) return `${Math.round(tokens / 1_000)}K`;
  return `${tokens}`;
}

// --- Main component ---

export default function ComprehensionSettingsPage() {
  const [settings, setSettings] = useState<EffectiveSettings | null>(null);
  const [models, setModels] = useState<ModelCapability[]>([]);
  const [health, setHealth] = useState<HealthPayload | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [saveMessage, setSaveMessage] = useState("");
  const [mode, setMode] = useState<"simple" | "advanced">("simple");
  const [undoSnapshot, setUndoSnapshot] = useState<EffectiveSettings | null>(null);
  const undoTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const initialLoad = useRef(false);

  // Editable fields (simple mode)
  const [selectedModel, setSelectedModel] = useState("");
  const [strategyChain, setStrategyChain] = useState<string[]>([]);
  // Advanced fields
  const [maxConcurrency, setMaxConcurrency] = useState(3);
  const [maxPromptTokens, setMaxPromptTokens] = useState(100000);
  const [leafBudgetTokens, setLeafBudgetTokens] = useState(3000);
  const [cacheEnabled, setCacheEnabled] = useState(false);

  const fetchWithAuth = useCallback(async (path: string, options?: RequestInit) => {
    const token = typeof window !== "undefined" ? localStorage.getItem(TOKEN_KEY) : null;
    return fetch(path, {
      ...options,
      headers: {
        "Content-Type": "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        ...options?.headers,
      },
    });
  }, []);

  const loadData = useCallback(async () => {
    try {
      const [settingsRes, modelsRes, healthRes] = await Promise.all([
        fetchWithAuth("/api/v1/admin/comprehension/settings/effective?scope_type=workspace&scope_key=default"),
        fetchWithAuth("/api/v1/admin/comprehension/models"),
        fetchWithAuth("/api/v1/admin/llm/activity").catch(() => null),
      ]);

      if (settingsRes.ok) {
        const s = await settingsRes.json();
        setSettings(s);
        if (!initialLoad.current) {
          setSelectedModel(s.modelId || "");
          setStrategyChain(s.strategyPreferenceChain || ["hierarchical", "single_shot"]);
          setMaxConcurrency(s.maxConcurrency || 3);
          setMaxPromptTokens(s.maxPromptTokens || 100000);
          setLeafBudgetTokens(s.leafBudgetTokens || 3000);
          setCacheEnabled(s.cacheEnabled || false);
          initialLoad.current = true;
        }
      }

      if (modelsRes.ok) {
        const m = await modelsRes.json();
        setModels(Array.isArray(m) ? m : []);
      }

      if (healthRes && healthRes.ok) {
        const h = await healthRes.json();
        setHealth(h.health || null);
      }
    } catch (e) {
      console.error("Failed to load comprehension settings:", e);
    } finally {
      setLoading(false);
    }
  }, [fetchWithAuth]);

  useEffect(() => {
    loadData();
  }, [loadData]);

  const handleSave = async () => {
    setSaving(true);
    setSaveMessage("");
    // Snapshot for undo
    if (settings) {
      setUndoSnapshot({ ...settings });
      if (undoTimer.current) clearTimeout(undoTimer.current);
      undoTimer.current = setTimeout(() => setUndoSnapshot(null), 30000);
    }
    try {
      const res = await fetchWithAuth("/api/v1/admin/comprehension/settings", {
        method: "PUT",
        body: JSON.stringify({
          scopeType: "workspace",
          scopeKey: "default",
          strategyPreferenceChain: strategyChain,
          modelId: selectedModel,
          maxConcurrency,
          maxPromptTokens,
          leafBudgetTokens,
          cacheEnabled,
        }),
      });
      if (res.ok) {
        const updated = await res.json();
        setSettings(updated);
        setSaveMessage("Settings saved.");
      } else {
        const err = await res.json().catch(() => ({ error: "Save failed" }));
        setSaveMessage(`Error: ${err.error || "Save failed"}`);
      }
    } catch {
      setSaveMessage("Network error.");
    } finally {
      setSaving(false);
    }
  };

  const handleUndo = async () => {
    if (!undoSnapshot) return;
    setSaving(true);
    try {
      const res = await fetchWithAuth("/api/v1/admin/comprehension/settings", {
        method: "PUT",
        body: JSON.stringify({
          scopeType: "workspace",
          scopeKey: "default",
          strategyPreferenceChain: undoSnapshot.strategyPreferenceChain,
          modelId: undoSnapshot.modelId,
          maxConcurrency: undoSnapshot.maxConcurrency,
          maxPromptTokens: undoSnapshot.maxPromptTokens,
          leafBudgetTokens: undoSnapshot.leafBudgetTokens,
          cacheEnabled: undoSnapshot.cacheEnabled,
        }),
      });
      if (res.ok) {
        const restored = await res.json();
        setSettings(restored);
        setSelectedModel(restored.modelId || "");
        setStrategyChain(restored.strategyPreferenceChain || []);
        setMaxConcurrency(restored.maxConcurrency || 3);
        setMaxPromptTokens(restored.maxPromptTokens || 100000);
        setLeafBudgetTokens(restored.leafBudgetTokens || 3000);
        setCacheEnabled(restored.cacheEnabled || false);
        setSaveMessage("Reverted to previous settings.");
      }
    } finally {
      setSaving(false);
      setUndoSnapshot(null);
      if (undoTimer.current) clearTimeout(undoTimer.current);
    }
  };

  const handleReset = async () => {
    setSaving(true);
    try {
      await fetchWithAuth("/api/v1/admin/comprehension/settings?scope_type=workspace&scope_key=default", {
        method: "DELETE",
      });
      initialLoad.current = false;
      await loadData();
      setSaveMessage("Reset to defaults.");
    } finally {
      setSaving(false);
    }
  };

  const handleUseRecommended = () => {
    setStrategyChain(["hierarchical", "single_shot"]);
    setMaxConcurrency(3);
    setMaxPromptTokens(100000);
    setLeafBudgetTokens(3000);
    setCacheEnabled(false);
  };

  // Determine which model is recommended for the current selected model
  const selectedModelCaps = models.find((m) => m.modelId === selectedModel);

  if (loading) {
    return (
      <PageFrame>
        <PageHeader eyebrow="Settings" title="Comprehension Engine" />
        <Panel>
          <p className="text-[var(--text-secondary)]">Loading settings...</p>
        </Panel>
      </PageFrame>
    );
  }

  return (
    <PageFrame>
      <PageHeader
        eyebrow="Settings"
        title="Comprehension Engine"
        description="Configure how SourceBridge generates cliff notes, learning paths, and other AI artifacts."
        actions={
          <div className="flex items-center gap-2">
            <Link
              href="/admin/llm"
              className="inline-flex items-center gap-1.5 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-sm text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)]"
            >
              Monitor →
            </Link>
            <button
              onClick={() => setMode(mode === "simple" ? "advanced" : "simple")}
              className="inline-flex items-center gap-1.5 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-sm text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)]"
            >
              {mode === "simple" ? "Advanced" : "Simple"}
            </button>
          </div>
        }
      />

      <div className="space-y-6">
        {/* Card 1: Model picker + recommended setup */}
        <Panel>
          <div className="space-y-4">
            <div className="flex items-start justify-between">
              <div>
                <h3 className="text-base font-semibold text-[var(--text-primary)]">Model</h3>
                <p className="mt-1 text-sm text-[var(--text-secondary)]">
                  Pick the model that powers artifact generation. The system recommends strategies automatically.
                </p>
              </div>
              <Button variant="secondary" size="sm" onClick={handleUseRecommended}>
                Use recommended
              </Button>
            </div>

            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div>
                <label className="mb-1.5 block text-sm font-medium text-[var(--text-secondary)]">
                  Active model
                </label>
                <select
                  value={selectedModel}
                  onChange={(e) => setSelectedModel(e.target.value)}
                  className="w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-sm text-[var(--text-primary)] outline-none focus:border-[var(--accent-primary)]"
                >
                  <option value="">— Use configured provider default —</option>
                  {models
                    .filter((m) => !m.embeddingModel)
                    .map((m) => (
                      <option key={m.modelId} value={m.modelId}>
                        {m.modelId} ({m.provider}, {formatContext(m.effectiveContextTokens)} ctx)
                      </option>
                    ))}
                </select>
              </div>

              {selectedModelCaps && (
                <div className="space-y-2">
                  <p className="text-sm font-medium text-[var(--text-secondary)]">Capabilities</p>
                  <div className="flex flex-wrap gap-2">
                    <span className="text-xs text-[var(--text-secondary)]">Instruction:</span>
                    {gradeBadge(selectedModelCaps.instructionFollowing)}
                    <span className="text-xs text-[var(--text-secondary)]">JSON:</span>
                    {gradeBadge(selectedModelCaps.jsonMode)}
                    <span className="text-xs text-[var(--text-secondary)]">Context:</span>
                    <span className="text-xs font-mono text-[var(--text-primary)]">
                      {formatContext(selectedModelCaps.effectiveContextTokens)}
                    </span>
                  </div>
                </div>
              )}
            </div>
          </div>
        </Panel>

        {/* Card 2: Strategy preference chain */}
        <Panel>
          <div className="space-y-4">
            <div>
              <h3 className="text-base font-semibold text-[var(--text-primary)]">Strategy Chain</h3>
              <p className="mt-1 text-sm text-[var(--text-secondary)]">
                Order of strategies to try for each artifact. The engine walks the chain and picks the first
                strategy compatible with your model.
              </p>
            </div>

            <div className="space-y-2">
              {strategyChain.map((strat, i) => {
                const meta = STRATEGIES[strat];
                return (
                  <div
                    key={strat}
                    className="flex items-center gap-3 rounded-[var(--panel-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-4 py-3"
                  >
                    <span className="text-xs font-mono text-[var(--text-muted)]">{i + 1}</span>
                    <div className="flex-1">
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-medium text-[var(--text-primary)]">
                          {meta?.label || strat}
                        </span>
                        {meta?.badge && (
                          <span className="rounded-full bg-[var(--accent-primary)]/10 px-2 py-0.5 text-xs text-[var(--accent-primary)]">
                            {meta.badge}
                          </span>
                        )}
                      </div>
                      {meta?.desc && (
                        <p className="mt-0.5 text-xs text-[var(--text-secondary)]">{meta.desc}</p>
                      )}
                    </div>
                    <button
                      onClick={() => setStrategyChain((prev) => prev.filter((_, j) => j !== i))}
                      className="text-[var(--text-muted)] hover:text-red-400"
                      title="Remove"
                    >
                      ×
                    </button>
                  </div>
                );
              })}

              {Object.keys(STRATEGIES)
                .filter((s) => !strategyChain.includes(s))
                .length > 0 && (
                <select
                  onChange={(e) => {
                    if (e.target.value) {
                      setStrategyChain((prev) => [...prev, e.target.value]);
                      e.target.value = "";
                    }
                  }}
                  className="w-full rounded-[var(--control-radius)] border border-dashed border-[var(--border-default)] bg-transparent px-3 py-2 text-sm text-[var(--text-secondary)] outline-none"
                  defaultValue=""
                >
                  <option value="" disabled>
                    + Add strategy...
                  </option>
                  {Object.entries(STRATEGIES)
                    .filter(([key]) => !strategyChain.includes(key))
                    .map(([key, meta]) => (
                      <option key={key} value={key}>
                        {meta.label}
                      </option>
                    ))}
                </select>
              )}
            </div>
          </div>
        </Panel>

        {/* Card 3: Live monitor snapshot */}
        <Panel>
          <div className="space-y-3">
            <div className="flex items-center justify-between">
              <h3 className="text-base font-semibold text-[var(--text-primary)]">System Status</h3>
              <Link
                href="/admin/llm"
                className="text-sm text-[var(--accent-primary)] hover:underline"
              >
                Full monitor →
              </Link>
            </div>
            {health ? (
              <div className="flex items-center gap-3">
                <span
                  className={cn(
                    "inline-block h-2.5 w-2.5 rounded-full",
                    health.status === "healthy"
                      ? "bg-green-500"
                      : health.status === "degraded"
                        ? "bg-amber-500"
                        : "bg-red-500"
                  )}
                />
                <span className="text-sm text-[var(--text-primary)]">{health.summary}</span>
              </div>
            ) : (
              <p className="text-sm text-[var(--text-secondary)]">
                No monitor data available. Generate an artifact to see system status here.
              </p>
            )}
          </div>
        </Panel>

        {/* Advanced mode panels */}
        {mode === "advanced" && (
          <>
            <Panel>
              <div className="space-y-4">
                <h3 className="text-base font-semibold text-[var(--text-primary)]">Orchestration</h3>
                <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
                  <div>
                    <label className="mb-1.5 block text-sm text-[var(--text-secondary)]">
                      Max concurrency
                    </label>
                    <input
                      type="number"
                      min={1}
                      max={20}
                      value={maxConcurrency}
                      onChange={(e) => setMaxConcurrency(Number(e.target.value))}
                      className="w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-sm text-[var(--text-primary)] outline-none focus:border-[var(--accent-primary)]"
                    />
                    <p className="mt-1 text-xs text-[var(--text-muted)]">
                      Parallel LLM jobs. 3 is safe for a single Ollama.
                    </p>
                  </div>
                  <div>
                    <label className="mb-1.5 block text-sm text-[var(--text-secondary)]">
                      Max prompt tokens
                    </label>
                    <input
                      type="number"
                      min={1000}
                      step={1000}
                      value={maxPromptTokens}
                      onChange={(e) => setMaxPromptTokens(Number(e.target.value))}
                      className="w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-sm text-[var(--text-primary)] outline-none focus:border-[var(--accent-primary)]"
                    />
                    <p className="mt-1 text-xs text-[var(--text-muted)]">
                      Budget guard ceiling per call.
                    </p>
                  </div>
                  <div>
                    <label className="mb-1.5 block text-sm text-[var(--text-secondary)]">
                      Leaf budget tokens
                    </label>
                    <input
                      type="number"
                      min={500}
                      step={500}
                      value={leafBudgetTokens}
                      onChange={(e) => setLeafBudgetTokens(Number(e.target.value))}
                      className="w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-sm text-[var(--text-primary)] outline-none focus:border-[var(--accent-primary)]"
                    />
                    <p className="mt-1 text-xs text-[var(--text-muted)]">
                      Max tokens per hierarchical leaf node.
                    </p>
                  </div>
                </div>

                <div className="flex items-center gap-3">
                  <label className="flex items-center gap-2 text-sm text-[var(--text-secondary)]">
                    <input
                      type="checkbox"
                      checked={cacheEnabled}
                      onChange={(e) => setCacheEnabled(e.target.checked)}
                      className="rounded border-[var(--border-default)]"
                    />
                    Enable prompt caching (Anthropic)
                  </label>
                </div>
              </div>
            </Panel>

            <Panel>
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <h3 className="text-base font-semibold text-[var(--text-primary)]">Model Registry</h3>
                  <Link
                    href="/admin/settings/comprehension/models"
                    className="text-sm text-[var(--accent-primary)] hover:underline"
                  >
                    Manage models →
                  </Link>
                </div>
                <div className="overflow-x-auto">
                  <table className="w-full text-sm">
                    <thead>
                      <tr className="border-b border-[var(--border-subtle)] text-left text-[var(--text-secondary)]">
                        <th className="pb-2 pr-4 font-medium">Model</th>
                        <th className="pb-2 pr-4 font-medium">Provider</th>
                        <th className="pb-2 pr-4 font-medium">Context</th>
                        <th className="pb-2 pr-4 font-medium">Instruction</th>
                        <th className="pb-2 pr-4 font-medium">JSON</th>
                        <th className="pb-2 font-medium">Source</th>
                      </tr>
                    </thead>
                    <tbody>
                      {models
                        .filter((m) => !m.embeddingModel)
                        .map((m) => (
                          <tr
                            key={m.modelId}
                            className="border-b border-[var(--border-subtle)] last:border-0"
                          >
                            <td className="py-2 pr-4 font-mono text-[var(--text-primary)]">
                              {m.modelId}
                            </td>
                            <td className="py-2 pr-4 text-[var(--text-secondary)]">{m.provider}</td>
                            <td className="py-2 pr-4 font-mono">
                              {formatContext(m.effectiveContextTokens)}
                            </td>
                            <td className="py-2 pr-4">{gradeBadge(m.instructionFollowing)}</td>
                            <td className="py-2 pr-4">{gradeBadge(m.jsonMode)}</td>
                            <td className="py-2 text-[var(--text-muted)]">{m.source}</td>
                          </tr>
                        ))}
                    </tbody>
                  </table>
                </div>
              </div>
            </Panel>
          </>
        )}

        {/* Save bar */}
        <div className="flex items-center gap-3">
          <Button variant="primary" size="sm" onClick={handleSave} disabled={saving}>
            {saving ? "Saving..." : "Save settings"}
          </Button>
          {undoSnapshot && (
            <Button variant="secondary" size="sm" onClick={handleUndo} disabled={saving}>
              Undo
            </Button>
          )}
          <Button variant="ghost" size="sm" onClick={handleReset} disabled={saving}>
            Reset to defaults
          </Button>
          {saveMessage && (
            <span className="text-sm text-[var(--text-secondary)]">{saveMessage}</span>
          )}
        </div>

        {/* Inheritance info (only in advanced mode) */}
        {mode === "advanced" && settings?.inheritedFrom && settings.inheritedFrom.length > 0 && (
          <Panel variant="surface" padding="sm">
            <p className="mb-2 text-xs font-medium text-[var(--text-secondary)]">
              Field inheritance
            </p>
            <div className="flex flex-wrap gap-x-4 gap-y-1">
              {settings.inheritedFrom.map((fo) => (
                <span key={fo.field} className="text-xs text-[var(--text-muted)]">
                  <span className="font-mono">{fo.field}</span>
                  {" ← "}
                  {fo.scopeType}
                  {fo.scopeKey ? `:${fo.scopeKey}` : ""}
                </span>
              ))}
            </div>
          </Panel>
        )}
      </div>
    </PageFrame>
  );
}
