"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { authFetch } from "@/lib/auth-fetch";
import { cn } from "@/lib/utils";

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

interface LivingWikiSettings {
  enabled?: boolean | null;
  workerCount?: number | null;
  eventTimeout?: string | null;
  githubToken?: string | null;
  gitlabToken?: string | null;
  confluenceSite?: string | null;
  confluenceEmail?: string | null;
  confluenceToken?: string | null;
  notionToken?: string | null;
  confluenceWebhookSecret?: string | null;
  notionWebhookSecret?: string | null;
  updatedAt?: string | null;
  updatedBy?: string | null;
}

interface ConnectionTestResult {
  ok: boolean;
  message?: string | null;
}

type TestState = "idle" | "testing" | "ok" | "error";

interface FieldMeta {
  testState: TestState;
  testMessage: string;
}

const SENTINEL = "********";

const TIMEOUT_OPTIONS = ["1m", "5m", "15m", "1h"] as const;
type TimeoutOption = (typeof TIMEOUT_OPTIONS)[number];

const TIMEOUT_LABELS: Record<TimeoutOption, string> = {
  "1m": "1 minute",
  "5m": "5 minutes (default)",
  "15m": "15 minutes",
  "1h": "1 hour",
};

// ─────────────────────────────────────────────────────────────────────────────
// GraphQL fragments
// ─────────────────────────────────────────────────────────────────────────────

const LIVING_WIKI_QUERY = `
  query LivingWikiSettings {
    livingWikiSettings {
      enabled
      workerCount
      eventTimeout
      githubToken
      gitlabToken
      confluenceSite
      confluenceEmail
      confluenceToken
      notionToken
      confluenceWebhookSecret
      notionWebhookSecret
      updatedAt
      updatedBy
    }
  }
`;

const UPDATE_LIVING_WIKI_MUTATION = `
  mutation UpdateLivingWikiSettings($input: UpdateLivingWikiSettingsInput!) {
    updateLivingWikiSettings(input: $input) {
      enabled
      workerCount
      eventTimeout
      githubToken
      gitlabToken
      confluenceSite
      confluenceEmail
      confluenceToken
      notionToken
      confluenceWebhookSecret
      notionWebhookSecret
      updatedAt
      updatedBy
    }
  }
`;

const TEST_CONNECTION_MUTATION = `
  mutation TestLivingWikiConnection($provider: String!) {
    testLivingWikiConnection(provider: $provider) {
      ok
      message
    }
  }
`;

// ─────────────────────────────────────────────────────────────────────────────
// GraphQL transport helpers
// ─────────────────────────────────────────────────────────────────────────────

async function gql<T>(query: string, variables?: Record<string, unknown>): Promise<T> {
  const res = await authFetch("/api/v1/graphql", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ query, variables }),
  });
  const json = await res.json();
  if (json.errors?.length) {
    throw new Error(json.errors[0].message ?? "GraphQL error");
  }
  return json.data as T;
}

// ─────────────────────────────────────────────────────────────────────────────
// Small presentational helpers
// ─────────────────────────────────────────────────────────────────────────────

function FieldLabel({
  label,
  help,
  required,
}: {
  label: string;
  help: string;
  required?: boolean;
}) {
  return (
    <div className="mb-1.5">
      <span className="text-sm font-medium text-[var(--text-primary)]">
        {label}
        {required && <span className="ml-0.5 text-[var(--color-error,#ef4444)]">*</span>}
      </span>
      <p className="mt-0.5 text-xs leading-relaxed text-[var(--text-tertiary)]">{help}</p>
    </div>
  );
}

function SecretInput({
  value,
  onChange,
  placeholder,
  disabled,
}: {
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  disabled?: boolean;
}) {
  const [revealed, setRevealed] = useState(false);
  const isSet = value === SENTINEL;

  return (
    <div className="flex gap-2">
      <input
        type={revealed ? "text" : "password"}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={isSet ? "Set — enter new value to replace" : placeholder}
        disabled={disabled}
        className={cn(
          "h-11 flex-1 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 font-mono text-sm text-[var(--text-primary)] placeholder:text-[var(--text-tertiary)] placeholder:font-sans focus:outline-none focus:ring-2 focus:ring-[var(--accent-focus)] disabled:opacity-60"
        )}
        autoComplete="off"
        spellCheck={false}
      />
      {!disabled && (
        <button
          type="button"
          onClick={() => setRevealed((r) => !r)}
          className="rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-surface)] px-3 text-xs text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
          aria-label={revealed ? "Hide" : "Reveal"}
        >
          {revealed ? "Hide" : "Show"}
        </button>
      )}
    </div>
  );
}

function TestButton({
  state,
  onTest,
  disabled,
}: {
  state: FieldMeta;
  onTest: () => void;
  disabled?: boolean;
}) {
  const { testState, testMessage } = state;
  return (
    <div className="mt-1 flex items-center gap-3">
      <button
        type="button"
        onClick={onTest}
        disabled={testState === "testing" || disabled}
        className="text-xs font-medium text-[var(--accent-primary)] hover:underline disabled:opacity-60"
      >
        {testState === "testing" ? "Testing…" : "Test connection"}
      </button>
      {testState === "ok" && (
        <span className="text-xs text-green-400">Connected successfully</span>
      )}
      {testState === "error" && (
        <span className="text-xs text-[var(--color-error,#ef4444)]">
          {testMessage || "Connection failed"}
        </span>
      )}
    </div>
  );
}

function SaveBanner({
  message,
  isError,
}: {
  message: string;
  isError: boolean;
}) {
  if (!message) return null;
  return (
    <div
      className={cn(
        "rounded-[var(--radius-md)] border px-4 py-2.5 text-sm",
        isError
          ? "border-[var(--color-error,#ef4444)] bg-[rgba(239,68,68,0.08)] text-[var(--color-error,#ef4444)]"
          : "border-[color:var(--color-success,#22c55e)] bg-[rgba(34,197,94,0.08)] text-green-400"
      )}
    >
      {message}
    </div>
  );
}

function Toggle({
  checked,
  onChange,
  label,
  description,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: string;
  description: string;
}) {
  return (
    <label className="flex cursor-pointer items-start gap-3">
      <div className="relative mt-0.5 shrink-0">
        <input
          type="checkbox"
          checked={checked}
          onChange={(e) => onChange(e.target.checked)}
          className="sr-only"
        />
        <div
          className={cn(
            "h-6 w-11 rounded-full border transition-colors",
            checked
              ? "border-[var(--accent-primary)] bg-[var(--accent-primary)]"
              : "border-[var(--border-default)] bg-[var(--bg-surface)]"
          )}
        >
          <div
            className={cn(
              "mt-[3px] h-[18px] w-[18px] rounded-full bg-white shadow transition-transform",
              checked ? "translate-x-[22px]" : "translate-x-[2px]"
            )}
          />
        </div>
      </div>
      <div>
        <div className="text-sm font-medium text-[var(--text-primary)]">{label}</div>
        <div className="text-xs leading-relaxed text-[var(--text-tertiary)]">{description}</div>
      </div>
    </label>
  );
}

function Disclosure({
  label,
  children,
  configured,
}: {
  label: string;
  children: React.ReactNode;
  /** When provided, renders a status badge beside the label. */
  configured?: boolean;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className="border-t border-[var(--border-subtle)] pt-4">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center justify-between text-sm font-medium text-[var(--text-primary)] hover:text-[var(--accent-primary)]"
      >
        <span className="flex items-center gap-2">
          {label}
          {configured === true && (
            <span className="inline-flex items-center rounded-full border border-[var(--color-success,#22c55e)] bg-[rgba(34,197,94,0.1)] px-2 py-0.5 text-xs font-medium text-[#16a34a]">
              Configured
            </span>
          )}
          {configured === false && (
            <span className="inline-flex items-center rounded-full border border-[var(--border-default)] bg-[var(--bg-subtle)] px-2 py-0.5 text-xs font-medium text-[var(--text-tertiary)]">
              Not configured
            </span>
          )}
        </span>
        <span className="text-xs text-[var(--text-secondary)]">{open ? "collapse" : "expand"}</span>
      </button>
      {open && <div className="mt-4 space-y-6">{children}</div>}
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Main page component
// ─────────────────────────────────────────────────────────────────────────────

export default function LivingWikiSettingsPage() {
  // ── Loading / save state ──
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveMessage, setSaveMessage] = useState("");
  const [saveError, setSaveError] = useState(false);
  const saveTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // ── Form fields ──
  const [enabled, setEnabled] = useState(false);
  const [workerCount, setWorkerCount] = useState(4);
  const [eventTimeout, setEventTimeout] = useState<TimeoutOption>("5m");

  const [githubToken, setGithubToken] = useState("");
  const [gitlabToken, setGitlabToken] = useState("");
  const [confluenceSite, setConfluenceSite] = useState("");
  const [confluenceEmail, setConfluenceEmail] = useState("");
  const [confluenceToken, setConfluenceToken] = useState("");
  const [notionToken, setNotionToken] = useState("");
  const [confluenceWebhookSecret, setConfluenceWebhookSecret] = useState("");
  const [notionWebhookSecret, setNotionWebhookSecret] = useState("");

  const [lastUpdated, setLastUpdated] = useState<string | null>(null);

  // ── Test-connection state per provider ──
  const [ghMeta, setGhMeta] = useState<FieldMeta>({ testState: "idle", testMessage: "" });
  const [glMeta, setGlMeta] = useState<FieldMeta>({ testState: "idle", testMessage: "" });
  const [cfMeta, setCfMeta] = useState<FieldMeta>({ testState: "idle", testMessage: "" });
  const [ntMeta, setNtMeta] = useState<FieldMeta>({ testState: "idle", testMessage: "" });

  // ── Load initial data ──
  const applySettings = useCallback((s: LivingWikiSettings) => {
    setEnabled(s.enabled ?? false);
    setWorkerCount(s.workerCount ?? 4);
    const to = (s.eventTimeout ?? "5m") as TimeoutOption;
    setEventTimeout(TIMEOUT_OPTIONS.includes(to) ? to : "5m");
    setGithubToken(s.githubToken ?? "");
    setGitlabToken(s.gitlabToken ?? "");
    setConfluenceSite(s.confluenceSite ?? "");
    setConfluenceEmail(s.confluenceEmail ?? "");
    setConfluenceToken(s.confluenceToken ?? "");
    setNotionToken(s.notionToken ?? "");
    setConfluenceWebhookSecret(s.confluenceWebhookSecret ?? "");
    setNotionWebhookSecret(s.notionWebhookSecret ?? "");
    setLastUpdated(s.updatedAt ?? null);
  }, []);

  useEffect(() => {
    gql<{ livingWikiSettings: LivingWikiSettings }>(LIVING_WIKI_QUERY)
      .then((d) => applySettings(d.livingWikiSettings))
      .catch((e) => setLoadError(String(e)))
      .finally(() => setLoading(false));
  }, [applySettings]);

  // ── Save ──
  const handleSave = async () => {
    setSaving(true);
    setSaveMessage("");
    setSaveError(false);
    if (saveTimer.current) clearTimeout(saveTimer.current);

    try {
      const data = await gql<{ updateLivingWikiSettings: LivingWikiSettings }>(
        UPDATE_LIVING_WIKI_MUTATION,
        {
          input: {
            enabled,
            workerCount,
            eventTimeout,
            githubToken: githubToken === SENTINEL ? SENTINEL : githubToken,
            gitlabToken: gitlabToken === SENTINEL ? SENTINEL : gitlabToken,
            confluenceSite,
            confluenceEmail: confluenceEmail === SENTINEL ? SENTINEL : confluenceEmail,
            confluenceToken: confluenceToken === SENTINEL ? SENTINEL : confluenceToken,
            notionToken: notionToken === SENTINEL ? SENTINEL : notionToken,
            confluenceWebhookSecret:
              confluenceWebhookSecret === SENTINEL ? SENTINEL : confluenceWebhookSecret,
            notionWebhookSecret:
              notionWebhookSecret === SENTINEL ? SENTINEL : notionWebhookSecret,
          },
        }
      );
      applySettings(data.updateLivingWikiSettings);
      setSaveMessage("Settings saved.");
      setSaveError(false);
    } catch (e) {
      setSaveMessage(String(e));
      setSaveError(true);
    } finally {
      setSaving(false);
      saveTimer.current = setTimeout(() => setSaveMessage(""), 5000);
    }
  };

  // ── Test connection ──
  const testConnection = async (
    provider: string,
    setMeta: React.Dispatch<React.SetStateAction<FieldMeta>>
  ) => {
    setMeta({ testState: "testing", testMessage: "" });
    try {
      const data = await gql<{ testLivingWikiConnection: ConnectionTestResult }>(
        TEST_CONNECTION_MUTATION,
        { provider }
      );
      const r = data.testLivingWikiConnection;
      setMeta({ testState: r.ok ? "ok" : "error", testMessage: r.message ?? "" });
    } catch (e) {
      setMeta({ testState: "error", testMessage: String(e) });
    }
  };

  const inputClass =
    "h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-focus)]";

  // ── Render ──
  if (loading) {
    return (
      <div className="space-y-6">
        <PageHeader eyebrow="Settings" title="Living Wiki" description="Loading…" />
        <Panel>
          <div className="flex h-32 items-center justify-center">
            <div className="h-5 w-5 animate-spin rounded-full border-2 border-[var(--border-default)] border-t-[var(--accent-primary)]" />
          </div>
        </Panel>
      </div>
    );
  }

  if (loadError) {
    return (
      <div className="space-y-6">
        <PageHeader eyebrow="Settings" title="Living Wiki" description="Failed to load settings." />
        <Panel>
          <p className="rounded-[var(--radius-md)] border border-[var(--color-error,#ef4444)] bg-[rgba(239,68,68,0.08)] px-4 py-3 text-sm text-[var(--color-error,#ef4444)]">
            {loadError}
          </p>
        </Panel>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Settings"
        title="Living Wiki"
        description="Connect repositories to documentation sinks so your wiki stays in sync as code changes."
      />

      {/* ── General ── */}
      <Panel className="space-y-6">
        <div className="space-y-1">
          <h2 className="text-lg font-semibold tracking-[-0.02em] text-[var(--text-primary)]">
            General
          </h2>
          <p className="text-sm leading-6 text-[var(--text-secondary)]">
            Master controls for the living-wiki feature.
          </p>
        </div>

        <Toggle
          checked={enabled}
          onChange={setEnabled}
          label="Enable Living Wiki"
          description="When enabled, the webhook dispatcher is running and SourceBridge will update your documentation sinks in response to code changes. Disable to pause all sync without losing configuration."
        />

        <div>
          <FieldLabel
            label="Worker count"
            help="How many goroutines drain the global overflow queue in parallel. Higher values recover from a backlog faster; lower values reduce database load. Default 4 is right for most teams."
          />
          <input
            type="range"
            min={1}
            max={16}
            value={workerCount}
            onChange={(e) => setWorkerCount(Number(e.target.value))}
            className="w-full accent-[var(--accent-primary)]"
          />
          <div className="mt-1 flex justify-between text-xs text-[var(--text-tertiary)]">
            <span>1 (minimal)</span>
            <span className="font-medium text-[var(--text-primary)]">{workerCount} workers</span>
            <span>16 (high-throughput)</span>
          </div>
        </div>

        <div>
          <FieldLabel
            label="Event timeout"
            help="Maximum time a single webhook event handler is allowed to run. If the orchestrator exceeds this, the event is abandoned and re-delivery (by the provider) retries it. Increase for very large repositories with slow AI inference."
          />
          <select
            value={eventTimeout}
            onChange={(e) => setEventTimeout(e.target.value as TimeoutOption)}
            className={inputClass}
          >
            {TIMEOUT_OPTIONS.map((t) => (
              <option key={t} value={t}>
                {TIMEOUT_LABELS[t]}
              </option>
            ))}
          </select>
        </div>
      </Panel>

      {/* ── GitHub integration ── */}
      <Panel className="space-y-6">
        <Disclosure label="GitHub integration" configured={!!githubToken}>
          <FieldLabel
            label="Personal Access Token (or App token)"
            help="A GitHub PAT or GitHub App installation token. Needs repo:read and pull_request:write scope to open wiki-diff PRs. Works with both PATs and GitHub App tokens — SourceBridge treats them identically."
          />
          <SecretInput value={githubToken} onChange={setGithubToken} placeholder="ghp_…" />
          <TestButton state={ghMeta} onTest={() => testConnection("github", setGhMeta)} />
        </Disclosure>
      </Panel>

      {/* ── GitLab integration ── */}
      <Panel className="space-y-6">
        <Disclosure label="GitLab integration" configured={!!gitlabToken}>
          <FieldLabel
            label="PRIVATE-TOKEN"
            help="A GitLab personal or project access token. Needs read_repository and write_repository scope to commit wiki changes as merge requests."
          />
          <SecretInput value={gitlabToken} onChange={setGitlabToken} placeholder="glpat-…" />
          <TestButton state={glMeta} onTest={() => testConnection("gitlab", setGlMeta)} />
        </Disclosure>
      </Panel>

      {/* ── Confluence integration ── */}
      <Panel className="space-y-6">
        <Disclosure label="Confluence integration" configured={!!confluenceToken}>
          <div className="space-y-4">
            <div>
              <FieldLabel
                label="Confluence site"
                help='The subdomain of your Atlassian Cloud site. For example, if your Confluence URL is mycompany.atlassian.net, enter "mycompany".'
                required
              />
              <Input
                type="text"
                value={confluenceSite}
                onChange={(e) => setConfluenceSite(e.target.value)}
                placeholder="mycompany"
                autoComplete="off"
                spellCheck={false}
              />
            </div>

            <div>
              <FieldLabel
                label="Atlassian account email"
                help="The email address of the Atlassian account used for API authentication. This is combined with the API token below in HTTP Basic auth."
              />
              <SecretInput
                value={confluenceEmail}
                onChange={setConfluenceEmail}
                placeholder="you@example.com"
              />
            </div>

            <div>
              <FieldLabel
                label="API token"
                help="Generate an Atlassian API token at id.atlassian.com/manage-profile/security/api-tokens. The token is used as the password in Basic auth paired with your email above."
              />
              <SecretInput
                value={confluenceToken}
                onChange={setConfluenceToken}
                placeholder="ATATT3xF…"
              />
            </div>

            <TestButton
              state={cfMeta}
              onTest={() => testConnection("confluence", setCfMeta)}
              disabled={!confluenceSite.trim() || !confluenceEmail || !confluenceToken}
            />
          </div>
        </Disclosure>
      </Panel>

      {/* ── Notion integration ── */}
      <Panel className="space-y-6">
        <Disclosure label="Notion integration" configured={!!notionToken}>
          <FieldLabel
            label="Integration token"
            help="Create an internal integration at notion.so/profile/integrations and share the target databases with it. The token starts with 'secret_'. SourceBridge uses this to read and write Notion page blocks."
          />
          <SecretInput value={notionToken} onChange={setNotionToken} placeholder="secret_…" />
          <TestButton state={ntMeta} onTest={() => testConnection("notion", setNtMeta)} />
        </Disclosure>
      </Panel>

      {/* ── Webhook secrets ── */}
      <Panel className="space-y-6">
        <Disclosure label="Webhook secrets">
          <div className="space-y-4">
            <div>
              <FieldLabel
                label="Confluence webhook secret"
                help="HMAC-SHA256 shared secret for validating the X-Confluence-Signature header on incoming Confluence webhooks. Configure the same value in the Confluence webhook settings UI. When empty, signature validation is skipped — only acceptable in development."
              />
              <SecretInput
                value={confluenceWebhookSecret}
                onChange={setConfluenceWebhookSecret}
                placeholder="long random string"
              />
            </div>

            <div>
              <FieldLabel
                label="Notion webhook secret"
                help="Reserved for Notion webhook validation when Notion expands its webhook model beyond automation-triggered callbacks. Leave empty for now."
              />
              <SecretInput
                value={notionWebhookSecret}
                onChange={setNotionWebhookSecret}
                placeholder="reserved — not yet used by Notion"
              />
            </div>
          </div>
        </Disclosure>
      </Panel>

      {/* ── Save bar ── */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex-1">
          <SaveBanner message={saveMessage} isError={saveError} />
          {lastUpdated && !saveMessage && (
            <p className="text-xs text-[var(--text-tertiary)]">
              Last saved {new Date(lastUpdated).toLocaleString()}
            </p>
          )}
        </div>
        <Button onClick={handleSave} disabled={saving}>
          {saving ? "Saving…" : "Save settings"}
        </Button>
      </div>
    </div>
  );
}
