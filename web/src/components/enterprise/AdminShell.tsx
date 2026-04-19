"use client";

import React, { useState, useEffect, useCallback } from "react";
import {
  BadgeDollarSign,
  BellRing,
  ClipboardList,
  LockKeyhole,
  Settings2,
  Users,
} from "lucide-react";
import { clearStoredToken } from "@/lib/auth-token-store";
import { cn } from "@/lib/utils";
import { Panel as UiPanel } from "@/components/ui/panel";

type AdminTab = "billing" | "sso" | "audit" | "notifications" | "team" | "settings";

interface AdminShellProps {
  defaultTab?: AdminTab;
  orgId?: string;
  apiBase?: string;
}

/** Parse an API error response into a user-friendly message.
 *  On 401, clears the token and redirects to login. */
async function handleApiError(res: Response): Promise<string> {
  if (res.status === 401) {
    clearStoredToken();
    window.location.href = "/login";
    return "Session expired — redirecting to login...";
  }
  const text = await res.text();
  try {
    const json = JSON.parse(text);
    if (json.error) return json.error;
  } catch { /* not JSON */ }
  if (text.trimStart().startsWith("<")) {
    return `Server error (HTTP ${res.status}). The API may be restarting — try again in a moment.`;
  }
  return text || `HTTP ${res.status}`;
}

function useEnterpriseFetch<T>(url: string) {
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const refetch = useCallback(() => {
    setLoading(true);
    setError(null);
    fetch(url, { credentials: "include" })
      .then(async (res) => {
        if (!res.ok) throw new Error(await handleApiError(res));
        return res.json();
      })
      .then(setData)
      .catch((err) => setError(err.message))
      .finally(() => setLoading(false));
  }, [url]);

  useEffect(() => { refetch(); }, [refetch]);

  return { data, loading, error, refetch };
}

export function AdminShell({ defaultTab = "billing", orgId = "default", apiBase = "/api/v1/enterprise" }: AdminShellProps) {
  const [activeTab, setActiveTab] = useState<AdminTab>(defaultTab);

  const tabs: { id: AdminTab; label: string; icon: React.ComponentType<{ className?: string }> }[] = [
    { id: "billing", label: "Billing", icon: BadgeDollarSign },
    { id: "sso", label: "SSO / Identity", icon: LockKeyhole },
    { id: "audit", label: "Audit Log", icon: ClipboardList },
    { id: "notifications", label: "Notifications", icon: BellRing },
    { id: "team", label: "Team", icon: Users },
    { id: "settings", label: "Settings", icon: Settings2 },
  ];

  return (
    <div data-testid="admin-shell" className="grid min-h-screen grid-cols-1 gap-0 md:grid-cols-[var(--sidebar-width)_minmax(0,1fr)] md:gap-6">
      <nav
        data-testid="admin-nav"
        className="border-b border-[var(--border-subtle)] bg-[var(--nav-bg)]/90 px-4 py-4 md:border-b-0 md:border-r md:py-6"
      >
        <div className="mb-4 space-y-1 md:mb-6">
          <p className="text-[11px] font-semibold uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
            Enterprise
          </p>
          <h2 className="text-xl font-semibold tracking-[-0.03em] text-[var(--text-primary)]">
            Admin
          </h2>
        </div>
        <div className="-mx-4 flex gap-1 overflow-x-auto px-4 md:mx-0 md:flex-col md:overflow-visible md:px-0">
        {tabs.map((tab) => (
          <button
            key={tab.id}
            data-testid={`admin-tab-${tab.id}`}
            onClick={() => setActiveTab(tab.id)}
            className={cn(
              "mb-1 flex shrink-0 items-center gap-3 rounded-[var(--control-radius)] border px-3 py-2.5 text-left text-sm transition-colors md:w-full",
              activeTab === tab.id
                ? "border-[var(--nav-item-border)] bg-[var(--nav-item-bg-active)] text-[var(--text-primary)]"
                : "border-transparent text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
            )}
          >
            <tab.icon className="h-4 w-4 shrink-0" />
            <span className="whitespace-nowrap">{tab.label}</span>
          </button>
        ))}
        </div>
      </nav>
      <main data-testid="admin-content" className="min-w-0 px-3 py-4 md:px-2 md:py-6 md:pr-6">
        <UiPanel variant="elevated" className="min-h-[calc(100vh-3rem)]">
          <div className="mb-6 border-b border-[var(--border-subtle)] pb-5">
            <p className="text-[11px] font-semibold uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
              Enterprise Control Plane
            </p>
            <h1 className="mt-2 text-3xl font-semibold tracking-[-0.04em] text-[var(--text-primary)]">
              {tabs.find((tab) => tab.id === activeTab)?.label}
            </h1>
          </div>
        {activeTab === "billing" && <BillingPanel orgId={orgId} apiBase={apiBase} />}
        {activeTab === "sso" && <SSOPanel orgId={orgId} apiBase={apiBase} />}
        {activeTab === "audit" && <AuditLogPanel orgId={orgId} apiBase={apiBase} />}
        {activeTab === "notifications" && <NotificationsPanel orgId={orgId} apiBase={apiBase} />}
        {activeTab === "team" && <TeamPanel orgId={orgId} apiBase={apiBase} />}
        {activeTab === "settings" && <SettingsPanel orgId={orgId} apiBase={apiBase} />}
        </UiPanel>
      </main>
    </div>
  );
}

interface PanelProps {
  orgId: string;
  apiBase: string;
}

function BillingPanel({ orgId, apiBase }: PanelProps) {
  const { data, loading } = useEnterpriseFetch<{
    plan: string;
    status: string;
    entitlements: Record<string, { allowed: boolean; limit: number; current_usage: number; reason: string }>;
    subscription?: { current_period_start: string; current_period_end: string };
  }>(`${apiBase}/billing/${orgId}`);

  if (loading) return <div data-testid="billing-panel">Loading billing...</div>;

  const plan = data?.plan || "free";
  const entitlements = data?.entitlements || {};
  const maxRepos = entitlements["max_repos"];
  const maxUsers = entitlements["max_users"];

  return (
    <div data-testid="billing-panel">
      <h3 className="text-lg font-semibold text-[var(--text-primary)]">Billing & Subscription</h3>
      <div className={sectionCardClass}>
        <div data-testid="current-plan">
          <strong>Current Plan:</strong> <span className="capitalize">{plan}</span>
          {data?.status && <span className={cn("ml-2", data.status === "active" ? "text-emerald-500" : "text-amber-500")}>({data.status})</span>}
        </div>
        {maxRepos && (
          <div className="mt-2">
            <strong>Repositories:</strong> {maxRepos.current_usage ?? 0} / {maxRepos.limit === -1 ? "Unlimited" : maxRepos.limit}
          </div>
        )}
        {maxUsers && (
          <div className="mt-2">
            <strong>Team Members:</strong> {maxUsers.current_usage ?? 0} / {maxUsers.limit === -1 ? "Unlimited" : maxUsers.limit}
          </div>
        )}
        {data?.subscription && (
          <div className="mt-2 text-sm text-[var(--text-tertiary)]">
            Period: {new Date(data.subscription.current_period_start).toLocaleDateString()} — {new Date(data.subscription.current_period_end).toLocaleDateString()}
          </div>
        )}
        <button className={primaryButtonClass}>
          Upgrade Plan
        </button>
      </div>
    </div>
  );
}

function SSOPanel({ orgId, apiBase }: PanelProps) {
  const { data, loading, refetch } = useEnterpriseFetch<{
    sso: { provider: string; enabled: boolean; status: string; issuer_url?: string; metadata_url?: string; client_id?: string; entity_id?: string };
  }>(`${apiBase}/sso/${orgId}`);
  const [provider, setProvider] = useState("oidc");
  const [issuerURL, setIssuerURL] = useState("");
  const [clientID, setClientID] = useState("");
  const [metadataURL, setMetadataURL] = useState("");
  const [entityID, setEntityID] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (data?.sso) {
      if (data.sso.provider !== "none") setProvider(data.sso.provider);
      if (data.sso.issuer_url) setIssuerURL(data.sso.issuer_url);
      if (data.sso.client_id) setClientID(data.sso.client_id);
      if (data.sso.metadata_url) setMetadataURL(data.sso.metadata_url);
      if (data.sso.entity_id) setEntityID(data.sso.entity_id);
    }
  }, [data]);

  async function handleSave() {
    setSaving(true);
    await fetch(`${apiBase}/sso/${orgId}`, {
      method: "PUT",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        provider,
        enabled: true,
        issuer_url: issuerURL,
        client_id: clientID,
        metadata_url: metadataURL,
        entity_id: entityID,
      }),
    });
    setSaving(false);
    refetch();
  }

  if (loading) return <div data-testid="sso-panel">Loading SSO config...</div>;

  const sso = data?.sso;

  return (
    <div data-testid="sso-panel">
      <h3 className="text-lg font-semibold text-[var(--text-primary)]">SSO / Identity Provider</h3>
      <div className={sectionCardClass}>
        <div className="mb-3">
          <strong>Status:</strong>{" "}
          <span className={sso?.status === "active" ? "text-emerald-500" : "text-[var(--text-tertiary)]"}>
            {sso?.status === "active" ? "Active" : "Not configured"}
          </span>
        </div>

        <div className="mb-2">
          <label className={labelClass}>Provider</label>
          <select value={provider} onChange={(e) => setProvider(e.target.value)} className={inputClass}>
            <option value="oidc">OIDC</option>
            <option value="saml">SAML</option>
          </select>
        </div>

        {provider === "oidc" && (
          <>
            <div className="mb-2">
              <label className={labelClass}>Issuer URL</label>
              <input value={issuerURL} onChange={(e) => setIssuerURL(e.target.value)} placeholder="https://auth.example.com" className={inputClass} />
            </div>
            <div className="mb-2">
              <label className={labelClass}>Client ID</label>
              <input value={clientID} onChange={(e) => setClientID(e.target.value)} placeholder="your-client-id" className={inputClass} />
            </div>
          </>
        )}

        {provider === "saml" && (
          <>
            <div className="mb-2">
              <label className={labelClass}>IdP Metadata URL</label>
              <input value={metadataURL} onChange={(e) => setMetadataURL(e.target.value)} placeholder="https://idp.example.com/metadata" className={inputClass} />
            </div>
            <div className="mb-2">
              <label className={labelClass}>Entity ID</label>
              <input value={entityID} onChange={(e) => setEntityID(e.target.value)} placeholder="https://app.sourcebridge.dev" className={inputClass} />
            </div>
          </>
        )}

        <button onClick={handleSave} disabled={saving} className={primaryButtonClass}>
          {saving ? "Saving..." : "Save Configuration"}
        </button>
      </div>
    </div>
  );
}

function AuditLogPanel({ orgId, apiBase }: PanelProps) {
  const [offset, setOffset] = useState(0);
  const limit = 25;
  const { data, loading } = useEnterpriseFetch<{
    entries: { id: string; actor: string; action: string; resource_type: string; resource_id: string; timestamp: string }[];
    total: number;
  }>(`${apiBase}/audit/${orgId}?limit=${limit}&offset=${offset}`);

  const entries = data?.entries || [];
  const total = data?.total || 0;

  return (
    <div data-testid="audit-panel">
      <h3 className="text-lg font-semibold text-[var(--text-primary)]">Audit Log</h3>
      <div className="mt-4 overflow-x-auto">
        <table className="w-full min-w-[500px] border-collapse">
          <thead>
            <tr>
              <th className={thClass}>Time</th>
              <th className={thClass}>Actor</th>
              <th className={thClass}>Action</th>
              <th className={thClass}>Resource</th>
            </tr>
          </thead>
          <tbody data-testid="audit-entries">
            {loading ? (
              <tr><td colSpan={4} className="p-4 text-center text-[var(--text-tertiary)]">Loading...</td></tr>
            ) : entries.length === 0 ? (
              <tr><td colSpan={4} className="p-4 text-center text-[var(--text-tertiary)]">No audit entries yet</td></tr>
            ) : (
              entries.map((entry) => (
                <tr key={entry.id}>
                  <td className={tdClass}>{new Date(entry.timestamp).toLocaleString()}</td>
                  <td className={tdClass}>{entry.actor}</td>
                  <td className={tdClass}>{entry.action}</td>
                  <td className={tdClass}>{entry.resource_type}/{entry.resource_id}</td>
                </tr>
              ))
            )}
          </tbody>
        </table>
        {total > limit && (
          <div className="mt-3 flex items-center justify-center gap-2">
            <button disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - limit))} className={secondaryButtonClass}>Previous</button>
            <span className="text-sm text-[var(--text-tertiary)]">
              {offset + 1}–{Math.min(offset + limit, total)} of {total}
            </span>
            <button disabled={offset + limit >= total} onClick={() => setOffset(offset + limit)} className={secondaryButtonClass}>Next</button>
          </div>
        )}
      </div>
    </div>
  );
}

function NotificationsPanel({ orgId, apiBase }: PanelProps) {
  const { data, loading, refetch } = useEnterpriseFetch<{
    configured: boolean;
    host?: string;
    port?: number;
    from?: string;
    tls?: boolean;
  }>(`${apiBase}/notifications/${orgId}`);
  const [host, setHost] = useState("");
  const [port, setPort] = useState("587");
  const [fromAddr, setFromAddr] = useState("");
  const [useTLS, setUseTLS] = useState(true);
  const [saving, setSaving] = useState(false);
  const [testResult, setTestResult] = useState<{ success: boolean; error?: string } | null>(null);

  useEffect(() => {
    if (data) {
      if (data.host) setHost(data.host);
      if (data.port) setPort(String(data.port));
      if (data.from) setFromAddr(data.from);
      if (data.tls !== undefined) setUseTLS(data.tls);
    }
  }, [data]);

  async function handleSave() {
    setSaving(true);
    await fetch(`${apiBase}/notifications/${orgId}`, {
      method: "PUT",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ host, port: parseInt(port), from_address: fromAddr, tls: useTLS }),
    });
    setSaving(false);
    refetch();
  }

  async function handleTest() {
    setTestResult(null);
    const res = await fetch(`${apiBase}/notifications/${orgId}/test`, {
      method: "POST",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ to: fromAddr }),
    });
    const result = await res.json();
    setTestResult(result);
  }

  if (loading) return <div data-testid="notifications-panel">Loading...</div>;

  return (
    <div data-testid="notifications-panel">
      <h3 className="text-lg font-semibold text-[var(--text-primary)]">Notification Settings</h3>
      <div className={sectionCardClass}>
        <div className="mb-3">
          <strong>Email Provider:</strong> {data?.configured ? "Configured" : "Not configured"}
        </div>

        <div className="mb-2">
          <label className={labelClass}>SMTP Host</label>
          <input value={host} onChange={(e) => setHost(e.target.value)} placeholder="smtp.example.com" className={inputClass} />
        </div>
        <div className="mb-2">
          <label className={labelClass}>Port</label>
          <input value={port} onChange={(e) => setPort(e.target.value)} placeholder="587" className={inputClass} />
        </div>
        <div className="mb-2">
          <label className={labelClass}>From Address</label>
          <input value={fromAddr} onChange={(e) => setFromAddr(e.target.value)} placeholder="noreply@example.com" className={inputClass} />
        </div>
        <div className="mb-3">
          <label className="flex cursor-pointer items-center gap-2 text-sm text-[var(--text-primary)]">
            <input type="checkbox" checked={useTLS} onChange={(e) => setUseTLS(e.target.checked)} />
            Use TLS
          </label>
        </div>

        <div className="flex gap-2">
          <button onClick={handleSave} disabled={saving} className={primaryButtonClass}>
            {saving ? "Saving..." : "Save"}
          </button>
          <button onClick={handleTest} disabled={!data?.configured} className={secondaryButtonClass}>
            Send Test Email
          </button>
        </div>

        {testResult && (
          <div className={cn("mt-3 rounded-md px-3 py-2 text-sm", testResult.success ? "bg-emerald-500/10 text-emerald-500" : "bg-rose-500/10 text-rose-500")}>
            {testResult.success ? "Test email sent successfully!" : `Failed: ${testResult.error}`}
          </div>
        )}
      </div>
    </div>
  );
}

function TeamPanel({ orgId, apiBase }: PanelProps) {
  const { data, loading, refetch } = useEnterpriseFetch<{
    members: { email: string; role: string; status: string; invited_at: string; invited_by: string }[];
  }>(`${apiBase}/team/${orgId}`);
  const [inviteEmail, setInviteEmail] = useState("");
  const [inviteRole, setInviteRole] = useState("member");

  async function handleInvite(e: React.FormEvent) {
    e.preventDefault();
    if (!inviteEmail.trim()) return;
    await fetch(`${apiBase}/team/${orgId}/invite`, {
      method: "POST",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email: inviteEmail.trim(), role: inviteRole }),
    });
    setInviteEmail("");
    refetch();
  }

  if (loading) return <div data-testid="team-panel">Loading team...</div>;

  const members = data?.members || [];

  return (
    <div data-testid="team-panel">
      <h3 className="text-lg font-semibold text-[var(--text-primary)]">Team Management</h3>

      {/* Invite form */}
      <div className={`${sectionCardClass} mb-4`}>
        <h4 className="mb-2 font-medium text-[var(--text-primary)]">Invite Member</h4>
        <form onSubmit={handleInvite} className="flex flex-col gap-2 sm:flex-row">
          <input
            type="email"
            value={inviteEmail}
            onChange={(e) => setInviteEmail(e.target.value)}
            placeholder="email@example.com"
            required
            className={inputClass}
          />
          <select value={inviteRole} onChange={(e) => setInviteRole(e.target.value)} className={`${inputClass} sm:w-auto`}>
            <option value="admin">Admin</option>
            <option value="member">Member</option>
            <option value="viewer">Viewer</option>
          </select>
          <button type="submit" className={`${primaryButtonClass} mt-0`}>
            Invite
          </button>
        </form>
      </div>

      {/* Members list */}
      <div className={sectionCardClass}>
        <h4 className="mb-2 font-medium text-[var(--text-primary)]">Members ({members.length})</h4>
        {members.length === 0 ? (
          <p className="text-[var(--text-tertiary)]">No team members yet. Send an invitation above.</p>
        ) : (
          members.map((m, i) => (
            <div key={i} className="flex justify-between border-b border-[var(--border-subtle)] py-2 last:border-b-0">
              <div>
                <div>{m.email}</div>
                <div className="text-sm text-[var(--text-tertiary)]">{m.role} — {m.status}</div>
              </div>
              <div className="text-sm text-[var(--text-tertiary)]">
                {m.invited_at ? new Date(m.invited_at).toLocaleDateString() : ""}
              </div>
            </div>
          ))
        )}
      </div>
    </div>
  );
}

function SettingsPanel({ orgId, apiBase }: PanelProps) {
  const { data, loading, refetch } = useEnterpriseFetch<{
    settings: {
      org_name: string;
      data_retention_days: number;
      default_role: string;
      ai_policy?: { enabled: boolean; max_ai_score: number; notify_on_violation: boolean; notify_emails: string[] };
      impact_notifications?: { enabled: boolean; notify_emails: string[]; min_affected_requirements: number };
    };
  }>(`${apiBase}/settings/${orgId}`);
  const [orgName, setOrgName] = useState("My Organization");
  const [retention, setRetention] = useState("90");
  const [defaultRole, setDefaultRole] = useState("member");
  // AI Code Policy
  const [aiEnabled, setAiEnabled] = useState(false);
  const [aiMaxScore, setAiMaxScore] = useState(70);
  const [aiNotify, setAiNotify] = useState(false);
  const [aiEmails, setAiEmails] = useState<string[]>([]);
  const [aiEmailInput, setAiEmailInput] = useState("");
  // Impact Notifications
  const [impactEnabled, setImpactEnabled] = useState(false);
  const [impactMinReqs, setImpactMinReqs] = useState(1);
  const [impactEmails, setImpactEmails] = useState<string[]>([]);
  const [impactEmailInput, setImpactEmailInput] = useState("");

  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (data?.settings) {
      setOrgName(data.settings.org_name);
      setRetention(String(data.settings.data_retention_days));
      setDefaultRole(data.settings.default_role);
      if (data.settings.ai_policy) {
        setAiEnabled(data.settings.ai_policy.enabled);
        setAiMaxScore(Math.round(data.settings.ai_policy.max_ai_score * 100));
        setAiNotify(data.settings.ai_policy.notify_on_violation);
        setAiEmails(data.settings.ai_policy.notify_emails || []);
      }
      if (data.settings.impact_notifications) {
        setImpactEnabled(data.settings.impact_notifications.enabled);
        setImpactMinReqs(data.settings.impact_notifications.min_affected_requirements || 1);
        setImpactEmails(data.settings.impact_notifications.notify_emails || []);
      }
    }
  }, [data]);

  async function handleSave() {
    setSaving(true);
    await fetch(`${apiBase}/settings/${orgId}`, {
      method: "PUT",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        org_name: orgName,
        data_retention_days: parseInt(retention),
        default_role: defaultRole,
        ai_policy: {
          enabled: aiEnabled,
          max_ai_score: aiMaxScore / 100,
          notify_on_violation: aiNotify,
          notify_emails: aiEmails,
        },
        impact_notifications: {
          enabled: impactEnabled,
          notify_emails: impactEmails,
          min_affected_requirements: impactMinReqs,
        },
      }),
    });
    setSaving(false);
    refetch();
  }

  if (loading) return <div data-testid="settings-panel">Loading settings...</div>;

  return (
    <div data-testid="settings-panel">
      <h3 className="text-lg font-semibold text-[var(--text-primary)]">Organization Settings</h3>
      <div className={sectionCardClass}>
        <div className="mb-3">
          <label className={labelClass}>Organization Name</label>
          <input value={orgName} onChange={(e) => setOrgName(e.target.value)} className={inputClass} />
        </div>
        <div className="mb-3">
          <label className={labelClass}>Data Retention (days)</label>
          <input value={retention} onChange={(e) => setRetention(e.target.value)} type="number" min="30" max="365" className={inputClass} />
        </div>
        <div className="mb-3">
          <label className={labelClass}>Default Role for New Members</label>
          <select value={defaultRole} onChange={(e) => setDefaultRole(e.target.value)} className={inputClass}>
            <option value="admin">Admin</option>
            <option value="member">Member</option>
            <option value="viewer">Viewer</option>
          </select>
        </div>
      </div>

      {/* AI Code Policy */}
      <h3 className="mt-6 text-lg font-semibold text-[var(--text-primary)]">AI Code Policy</h3>
      <div className={sectionCardClass}>
        <label className="flex cursor-pointer items-center gap-2 text-sm text-[var(--text-primary)]">
          <input type="checkbox" checked={aiEnabled} onChange={(e) => setAiEnabled(e.target.checked)} />
          Enforce AI-generated code threshold
        </label>
        {aiEnabled && (
          <div className="mt-3 space-y-3">
            <div>
              <label className={labelClass}>Max AI Score Threshold: {aiMaxScore}%</label>
              <input type="range" min="10" max="100" value={aiMaxScore} onChange={(e) => setAiMaxScore(Number(e.target.value))} className="w-full" />
              <div className="flex justify-between text-xs text-[var(--text-tertiary)]"><span>10%</span><span>100%</span></div>
            </div>
            <label className="flex cursor-pointer items-center gap-2 text-sm text-[var(--text-primary)]">
              <input type="checkbox" checked={aiNotify} onChange={(e) => setAiNotify(e.target.checked)} />
              Email notification on violation
            </label>
            {aiNotify && (
              <div>
                <label className={labelClass}>Notification Recipients</label>
                <div className="flex gap-2">
                  <input value={aiEmailInput} onChange={(e) => setAiEmailInput(e.target.value)} placeholder="email@example.com" className={inputClass}
                    onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); if (aiEmailInput.trim() && !aiEmails.includes(aiEmailInput.trim())) { setAiEmails([...aiEmails, aiEmailInput.trim()]); setAiEmailInput(""); } } }} />
                  <button type="button" onClick={() => { if (aiEmailInput.trim() && !aiEmails.includes(aiEmailInput.trim())) { setAiEmails([...aiEmails, aiEmailInput.trim()]); setAiEmailInput(""); } }} className={secondaryButtonClass}>Add</button>
                </div>
                {aiEmails.length > 0 && (
                  <div className="mt-2 flex flex-wrap gap-1">
                    {aiEmails.map((email) => (
                      <span key={email} className="inline-flex items-center gap-1 rounded-full bg-[var(--bg-hover)] px-2 py-0.5 text-xs text-[var(--text-secondary)]">
                        {email}
                        <button onClick={() => setAiEmails(aiEmails.filter((e) => e !== email))} className="text-[var(--text-tertiary)] hover:text-[var(--text-primary)]">&times;</button>
                      </span>
                    ))}
                  </div>
                )}
              </div>
            )}
          </div>
        )}
      </div>

      {/* Impact Notifications */}
      <h3 className="mt-6 text-lg font-semibold text-[var(--text-primary)]">Impact Notifications</h3>
      <div className={sectionCardClass}>
        <label className="flex cursor-pointer items-center gap-2 text-sm text-[var(--text-primary)]">
          <input type="checkbox" checked={impactEnabled} onChange={(e) => setImpactEnabled(e.target.checked)} />
          Send notifications when changes affect requirements
        </label>
        {impactEnabled && (
          <div className="mt-3 space-y-3">
            <div>
              <label className={labelClass}>Minimum affected requirements to trigger: {impactMinReqs}</label>
              <input type="range" min="1" max="20" value={impactMinReqs} onChange={(e) => setImpactMinReqs(Number(e.target.value))} className="w-full" />
              <div className="flex justify-between text-xs text-[var(--text-tertiary)]"><span>1</span><span>20</span></div>
            </div>
            <div>
              <label className={labelClass}>Notification Recipients</label>
              <div className="flex gap-2">
                <input value={impactEmailInput} onChange={(e) => setImpactEmailInput(e.target.value)} placeholder="email@example.com" className={inputClass}
                  onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); if (impactEmailInput.trim() && !impactEmails.includes(impactEmailInput.trim())) { setImpactEmails([...impactEmails, impactEmailInput.trim()]); setImpactEmailInput(""); } } }} />
                <button type="button" onClick={() => { if (impactEmailInput.trim() && !impactEmails.includes(impactEmailInput.trim())) { setImpactEmails([...impactEmails, impactEmailInput.trim()]); setImpactEmailInput(""); } }} className={secondaryButtonClass}>Add</button>
              </div>
              {impactEmails.length > 0 && (
                <div className="mt-2 flex flex-wrap gap-1">
                  {impactEmails.map((email) => (
                    <span key={email} className="inline-flex items-center gap-1 rounded-full bg-[var(--bg-hover)] px-2 py-0.5 text-xs text-[var(--text-secondary)]">
                      {email}
                      <button onClick={() => setImpactEmails(impactEmails.filter((e) => e !== email))} className="text-[var(--text-tertiary)] hover:text-[var(--text-primary)]">&times;</button>
                    </span>
                  ))}
                </div>
              )}
            </div>
          </div>
        )}
      </div>

      <button onClick={handleSave} disabled={saving} className={primaryButtonClass}>
        {saving ? "Saving..." : "Save Settings"}
      </button>

      {/* Repository Dependencies */}
      <RepoDependenciesSection orgId={orgId} apiBase={apiBase} />
    </div>
  );
}

function RepoDependenciesSection({ orgId, apiBase }: PanelProps) {
  const { data, loading, refetch } = useEnterpriseFetch<{
    dependencies: { upstream_repo_id: string; downstream_repo_id: string }[];
  }>(`${apiBase}/dependencies/${orgId}`);
  const [upstreamId, setUpstreamId] = useState("");
  const [downstreamId, setDownstreamId] = useState("");
  const [adding, setAdding] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleAdd(e: React.FormEvent) {
    e.preventDefault();
    if (!upstreamId.trim() || !downstreamId.trim()) return;
    setAdding(true);
    setError(null);
    const res = await fetch(`${apiBase}/dependencies/${orgId}`, {
      method: "POST",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ upstream_repo_id: upstreamId.trim(), downstream_repo_id: downstreamId.trim() }),
    });
    if (!res.ok) {
      const msg = await handleApiError(res);
      setError(msg);
    } else {
      setUpstreamId("");
      setDownstreamId("");
    }
    setAdding(false);
    refetch();
  }

  async function handleRemove(up: string, down: string) {
    await fetch(`${apiBase}/dependencies/${orgId}/${up}/${down}`, {
      method: "DELETE",
      credentials: "include",
    });
    refetch();
  }

  const deps = data?.dependencies || [];
  const forbidden = !loading && !data;

  return (
    <>
      <h3 className="mt-6 text-lg font-semibold text-[var(--text-primary)]">Repository Dependencies</h3>
      <div className={sectionCardClass}>
        {forbidden ? (
          <p className="text-sm text-[var(--text-tertiary)]">Cross-repo impact analysis requires an Enterprise plan.</p>
        ) : loading ? (
          <p className="text-sm text-[var(--text-tertiary)]">Loading dependencies...</p>
        ) : (
          <>
            {deps.length === 0 ? (
              <p className="mb-3 text-sm text-[var(--text-tertiary)]">No repository dependencies configured. Add a dependency to track cross-repo impact.</p>
            ) : (
              <div className="mb-4 overflow-x-auto"><table className="w-full min-w-[400px] border-collapse">
                <thead>
                  <tr>
                    <th className={thClass}>Upstream Repo</th>
                    <th className={thClass}>Downstream Repo</th>
                    <th className={thClass}></th>
                  </tr>
                </thead>
                <tbody>
                  {deps.map((dep) => (
                    <tr key={`${dep.upstream_repo_id}-${dep.downstream_repo_id}`}>
                      <td className={tdClass}>{dep.upstream_repo_id}</td>
                      <td className={tdClass}>{dep.downstream_repo_id}</td>
                      <td className={tdClass}>
                        <button onClick={() => handleRemove(dep.upstream_repo_id, dep.downstream_repo_id)} className="text-sm text-rose-500 hover:text-rose-400">Remove</button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table></div>
            )}
            <form onSubmit={handleAdd} className="flex flex-col gap-2 sm:flex-row">
              <input value={upstreamId} onChange={(e) => setUpstreamId(e.target.value)} placeholder="Upstream repo ID" required className={inputClass} />
              <input value={downstreamId} onChange={(e) => setDownstreamId(e.target.value)} placeholder="Downstream repo ID" required className={inputClass} />
              <button type="submit" disabled={adding} className={`${primaryButtonClass} mt-0`} style={{ marginTop: 0 }}>
                {adding ? "Adding..." : "Add"}
              </button>
            </form>
            {error && <p className="mt-2 text-sm text-rose-500">{error}</p>}
          </>
        )}
      </div>
    </>
  );
}

const sectionCardClass =
  "mt-4 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] p-4";
const inputClass =
  "w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-surface)] px-3 py-2 text-sm text-[var(--text-primary)]";
const labelClass = "mb-1 block text-sm text-[var(--text-tertiary)]";
const primaryButtonClass =
  "mt-4 rounded-[var(--control-radius)] bg-[var(--accent-primary)] px-4 py-2 text-sm font-medium text-[var(--accent-contrast)]";
const secondaryButtonClass =
  "rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-surface)] px-4 py-2 text-sm text-[var(--text-primary)]";
const thClass =
  "border-b border-[var(--border-default)] px-2 py-2 text-left text-sm font-medium text-[var(--text-secondary)]";
const tdClass =
  "border-b border-[var(--border-subtle)] px-2 py-2 text-sm text-[var(--text-primary)]";

export default AdminShell;
