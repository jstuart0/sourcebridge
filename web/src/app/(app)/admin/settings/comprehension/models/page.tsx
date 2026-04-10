"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";

import { Button } from "@/components/ui/button";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { TOKEN_KEY } from "@/lib/token-key";
import { cn } from "@/lib/utils";

/**
 * Model Capability Registry page (/admin/settings/comprehension/models).
 *
 * Viewer and editor for the model capability registry. Operators can:
 *   - View all known models with capability badges
 *   - Edit capability profiles for manual overrides
 *   - Delete custom model entries
 *   - (Future: Probe a new model to auto-detect capabilities)
 */

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

const GRADE_OPTIONS = ["low", "medium", "high"];
const JSON_MODE_OPTIONS = ["none", "prompted", "native"];
const TOOL_USE_OPTIONS = ["none", "supported", "native"];

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

export default function ModelsPage() {
  const [models, setModels] = useState<ModelCapability[]>([]);
  const [loading, setLoading] = useState(true);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editForm, setEditForm] = useState<Partial<ModelCapability>>({});
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState("");

  // Add new model form
  const [showAdd, setShowAdd] = useState(false);
  const [newModel, setNewModel] = useState<Partial<ModelCapability>>({
    modelId: "",
    provider: "",
    declaredContextTokens: 4096,
    effectiveContextTokens: 4096,
    instructionFollowing: "low",
    jsonMode: "none",
    toolUse: "none",
    extractionGrade: "low",
    creativeGrade: "low",
    embeddingModel: false,
    source: "manual",
  });

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

  const loadModels = useCallback(async () => {
    try {
      const res = await fetchWithAuth("/api/v1/admin/comprehension/models");
      if (res.ok) {
        const data = await res.json();
        setModels(Array.isArray(data) ? data : []);
      }
    } catch (e) {
      console.error("Failed to load models:", e);
    } finally {
      setLoading(false);
    }
  }, [fetchWithAuth]);

  useEffect(() => {
    loadModels();
  }, [loadModels]);

  const handleEdit = (model: ModelCapability) => {
    setEditingId(model.modelId);
    setEditForm({ ...model });
  };

  const handleSaveEdit = async () => {
    if (!editForm.modelId) return;
    setSaving(true);
    setMessage("");
    try {
      const res = await fetchWithAuth("/api/v1/admin/comprehension/models", {
        method: "PUT",
        body: JSON.stringify(editForm),
      });
      if (res.ok) {
        setEditingId(null);
        setMessage("Model updated.");
        await loadModels();
      } else {
        const err = await res.json().catch(() => ({ error: "Save failed" }));
        setMessage(`Error: ${err.error || "Save failed"}`);
      }
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async (modelId: string) => {
    if (!confirm(`Delete capability profile for "${modelId}"?`)) return;
    try {
      await fetchWithAuth(`/api/v1/admin/comprehension/models/${encodeURIComponent(modelId)}`, {
        method: "DELETE",
      });
      setMessage(`Deleted ${modelId}.`);
      await loadModels();
    } catch {
      setMessage("Delete failed.");
    }
  };

  const handleAddModel = async () => {
    if (!newModel.modelId) return;
    setSaving(true);
    setMessage("");
    try {
      const res = await fetchWithAuth("/api/v1/admin/comprehension/models", {
        method: "PUT",
        body: JSON.stringify(newModel),
      });
      if (res.ok) {
        setShowAdd(false);
        setNewModel({
          modelId: "",
          provider: "",
          declaredContextTokens: 4096,
          effectiveContextTokens: 4096,
          instructionFollowing: "low",
          jsonMode: "none",
          toolUse: "none",
          extractionGrade: "low",
          creativeGrade: "low",
          embeddingModel: false,
          source: "manual",
        });
        setMessage("Model added.");
        await loadModels();
      } else {
        const err = await res.json().catch(() => ({ error: "Save failed" }));
        setMessage(`Error: ${err.error || "Save failed"}`);
      }
    } finally {
      setSaving(false);
    }
  };

  const inputClass =
    "w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-1.5 text-sm text-[var(--text-primary)] outline-none focus:border-[var(--accent-primary)]";
  const selectClass = inputClass;

  function renderEditableRow(model: ModelCapability, form: Partial<ModelCapability>, setForm: (f: Partial<ModelCapability>) => void) {
    return (
      <tr className="border-b border-[var(--border-subtle)]">
        <td className="py-3 pr-3 font-mono text-[var(--text-primary)]">{model.modelId}</td>
        <td className="py-3 pr-3">
          <input
            className={inputClass}
            value={form.provider || ""}
            onChange={(e) => setForm({ ...form, provider: e.target.value })}
            style={{ width: 100 }}
          />
        </td>
        <td className="py-3 pr-3">
          <input
            type="number"
            className={inputClass}
            value={form.effectiveContextTokens || 0}
            onChange={(e) => setForm({ ...form, effectiveContextTokens: Number(e.target.value) })}
            style={{ width: 90 }}
          />
        </td>
        <td className="py-3 pr-3">
          <select
            className={selectClass}
            value={form.instructionFollowing || "low"}
            onChange={(e) => setForm({ ...form, instructionFollowing: e.target.value })}
            style={{ width: 90 }}
          >
            {GRADE_OPTIONS.map((g) => (
              <option key={g} value={g}>{g}</option>
            ))}
          </select>
        </td>
        <td className="py-3 pr-3">
          <select
            className={selectClass}
            value={form.jsonMode || "none"}
            onChange={(e) => setForm({ ...form, jsonMode: e.target.value })}
            style={{ width: 100 }}
          >
            {JSON_MODE_OPTIONS.map((g) => (
              <option key={g} value={g}>{g}</option>
            ))}
          </select>
        </td>
        <td className="py-3 pr-3">
          <select
            className={selectClass}
            value={form.toolUse || "none"}
            onChange={(e) => setForm({ ...form, toolUse: e.target.value })}
            style={{ width: 100 }}
          >
            {TOOL_USE_OPTIONS.map((g) => (
              <option key={g} value={g}>{g}</option>
            ))}
          </select>
        </td>
        <td className="py-3 pr-3 text-[var(--text-muted)]">{form.source}</td>
        <td className="py-3">
          <div className="flex gap-1">
            <Button variant="primary" size="sm" onClick={handleSaveEdit} disabled={saving}>
              Save
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setEditingId(null)}>
              Cancel
            </Button>
          </div>
        </td>
      </tr>
    );
  }

  if (loading) {
    return (
      <PageFrame>
        <PageHeader eyebrow="Settings / Comprehension" title="Model Registry" />
        <Panel>
          <p className="text-[var(--text-secondary)]">Loading models...</p>
        </Panel>
      </PageFrame>
    );
  }

  const generationModels = models.filter((m) => !m.embeddingModel);
  const embeddingModels = models.filter((m) => m.embeddingModel);

  return (
    <PageFrame>
      <PageHeader
        eyebrow="Settings / Comprehension"
        title="Model Registry"
        description="View and manage model capability profiles. These determine which strategies the engine can use with each model."
        actions={
          <div className="flex items-center gap-2">
            <Link
              href="/admin/settings/comprehension"
              className="inline-flex items-center gap-1.5 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-sm text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)]"
            >
              ← Settings
            </Link>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setShowAdd(!showAdd)}
            >
              {showAdd ? "Cancel" : "+ Add model"}
            </Button>
          </div>
        }
      />

      {message && (
        <p className="mb-4 text-sm text-[var(--text-secondary)]">{message}</p>
      )}

      {/* Add model form */}
      {showAdd && (
        <Panel className="mb-6">
          <h3 className="mb-3 text-sm font-semibold text-[var(--text-primary)]">Add Model</h3>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <div>
              <label className="mb-1 block text-xs text-[var(--text-secondary)]">Model ID</label>
              <input
                className={inputClass}
                placeholder="e.g., llama3:8b"
                value={newModel.modelId || ""}
                onChange={(e) => setNewModel({ ...newModel, modelId: e.target.value })}
              />
            </div>
            <div>
              <label className="mb-1 block text-xs text-[var(--text-secondary)]">Provider</label>
              <input
                className={inputClass}
                placeholder="e.g., ollama"
                value={newModel.provider || ""}
                onChange={(e) => setNewModel({ ...newModel, provider: e.target.value })}
              />
            </div>
            <div>
              <label className="mb-1 block text-xs text-[var(--text-secondary)]">Context tokens</label>
              <input
                type="number"
                className={inputClass}
                value={newModel.effectiveContextTokens || 4096}
                onChange={(e) =>
                  setNewModel({
                    ...newModel,
                    effectiveContextTokens: Number(e.target.value),
                    declaredContextTokens: Number(e.target.value),
                  })
                }
              />
            </div>
            <div>
              <label className="mb-1 block text-xs text-[var(--text-secondary)]">Instruction following</label>
              <select
                className={selectClass}
                value={newModel.instructionFollowing || "low"}
                onChange={(e) => setNewModel({ ...newModel, instructionFollowing: e.target.value })}
              >
                {GRADE_OPTIONS.map((g) => (
                  <option key={g} value={g}>{g}</option>
                ))}
              </select>
            </div>
          </div>
          <div className="mt-3 flex gap-2">
            <Button variant="primary" size="sm" onClick={handleAddModel} disabled={saving || !newModel.modelId}>
              Add
            </Button>
          </div>
        </Panel>
      )}

      {/* Generation models table */}
      <Panel className="mb-6">
        <h3 className="mb-3 text-sm font-semibold text-[var(--text-primary)]">
          Generation Models ({generationModels.length})
        </h3>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-[var(--border-subtle)] text-left text-[var(--text-secondary)]">
                <th className="pb-2 pr-3 font-medium">Model</th>
                <th className="pb-2 pr-3 font-medium">Provider</th>
                <th className="pb-2 pr-3 font-medium">Context</th>
                <th className="pb-2 pr-3 font-medium">Instruction</th>
                <th className="pb-2 pr-3 font-medium">JSON</th>
                <th className="pb-2 pr-3 font-medium">Tool Use</th>
                <th className="pb-2 pr-3 font-medium">Source</th>
                <th className="pb-2 font-medium">Actions</th>
              </tr>
            </thead>
            <tbody>
              {generationModels.map((m) =>
                editingId === m.modelId ? (
                  renderEditableRow(m, editForm, setEditForm)
                ) : (
                  <tr
                    key={m.modelId}
                    className="border-b border-[var(--border-subtle)] last:border-0"
                  >
                    <td className="py-2.5 pr-3 font-mono text-[var(--text-primary)]">{m.modelId}</td>
                    <td className="py-2.5 pr-3 text-[var(--text-secondary)]">{m.provider}</td>
                    <td className="py-2.5 pr-3 font-mono">
                      {formatContext(m.effectiveContextTokens)}
                    </td>
                    <td className="py-2.5 pr-3">{gradeBadge(m.instructionFollowing)}</td>
                    <td className="py-2.5 pr-3">{gradeBadge(m.jsonMode)}</td>
                    <td className="py-2.5 pr-3">{gradeBadge(m.toolUse)}</td>
                    <td className="py-2.5 pr-3 text-[var(--text-muted)]">{m.source}</td>
                    <td className="py-2.5">
                      <div className="flex gap-1">
                        <button
                          onClick={() => handleEdit(m)}
                          className="text-xs text-[var(--accent-primary)] hover:underline"
                        >
                          Edit
                        </button>
                        {m.source !== "builtin" && (
                          <button
                            onClick={() => handleDelete(m.modelId)}
                            className="text-xs text-red-400 hover:underline"
                          >
                            Delete
                          </button>
                        )}
                      </div>
                    </td>
                  </tr>
                )
              )}
              {generationModels.length === 0 && (
                <tr>
                  <td colSpan={8} className="py-8 text-center text-[var(--text-secondary)]">
                    No models registered. Add a model or seed from builtin profiles.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </Panel>

      {/* Embedding models (read-only) */}
      {embeddingModels.length > 0 && (
        <Panel>
          <h3 className="mb-3 text-sm font-semibold text-[var(--text-primary)]">
            Embedding Models ({embeddingModels.length})
          </h3>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-[var(--border-subtle)] text-left text-[var(--text-secondary)]">
                  <th className="pb-2 pr-3 font-medium">Model</th>
                  <th className="pb-2 pr-3 font-medium">Provider</th>
                  <th className="pb-2 pr-3 font-medium">Context</th>
                  <th className="pb-2 font-medium">Source</th>
                </tr>
              </thead>
              <tbody>
                {embeddingModels.map((m) => (
                  <tr
                    key={m.modelId}
                    className="border-b border-[var(--border-subtle)] last:border-0"
                  >
                    <td className="py-2.5 pr-3 font-mono text-[var(--text-primary)]">{m.modelId}</td>
                    <td className="py-2.5 pr-3 text-[var(--text-secondary)]">{m.provider}</td>
                    <td className="py-2.5 pr-3 font-mono">
                      {formatContext(m.effectiveContextTokens)}
                    </td>
                    <td className="py-2.5 text-[var(--text-muted)]">{m.source}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Panel>
      )}
    </PageFrame>
  );
}
