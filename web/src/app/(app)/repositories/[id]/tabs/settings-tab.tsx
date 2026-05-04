"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { useMutation } from "urql";
import {
  REINDEX_REPOSITORY_MUTATION,
  REMOVE_REPOSITORY_MUTATION,
  UPDATE_REPOSITORY_KNOWLEDGE_SETTINGS_MUTATION,
} from "@/lib/graphql/queries";
import { useServerCapabilities } from "@/lib/use-server-capabilities";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { cn, getServerOrigin } from "@/lib/utils";
import { WikiSettingsPanel, type RepositoryLivingWikiSettings } from "../wiki-settings-panel";
import { ClaudeCodeWizard } from "../_components/claude-code-wizard";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type RepositoryGenerationMode = "CLASSIC" | "UNDERSTANDING_FIRST";

interface RepoInfo {
  name?: string | null;
  generationModeDefault?: string | null;
  livingWikiSettings?: RepositoryLivingWikiSettings | null;
}

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface SettingsTabProps {
  repoId: string;
  repo: RepoInfo | null | undefined;
  knowledgeLoading: boolean;
  startLoading: (op: string) => void;
  finishLoading: (op: string) => void;
  repoGenerationModeDefault: RepositoryGenerationMode;
  agentSetupEnabled: boolean;
  onGenerationModeChange: () => void;
}

// ---------------------------------------------------------------------------
// SettingsTab component
// ---------------------------------------------------------------------------

export function SettingsTab({
  repoId,
  repo,
  knowledgeLoading,
  startLoading,
  finishLoading,
  repoGenerationModeDefault,
  agentSetupEnabled,
  onGenerationModeChange,
}: SettingsTabProps) {
  const router = useRouter();
  const serverCaps = useServerCapabilities();
  const [removeRepoConfirmOpen, setRemoveRepoConfirmOpen] = useState(false);
  const [copiedSetupCmd, setCopiedSetupCmd] = useState(false);
  const [useExistingToken, setUseExistingToken] = useState(false);

  const [, reindex] = useMutation(REINDEX_REPOSITORY_MUTATION);
  const [, removeRepo] = useMutation(REMOVE_REPOSITORY_MUTATION);
  const [, updateRepositoryKnowledgeSettings] = useMutation(UPDATE_REPOSITORY_KNOWLEDGE_SETTINGS_MUTATION);

  const artifactStatusClass =
    "rounded-full border border-[var(--border-default)] bg-[var(--bg-hover)] px-2.5 py-1 text-xs text-[var(--text-secondary)]";

  async function handleSaveRepositoryGenerationMode(nextMode: RepositoryGenerationMode) {
    startLoading("generation-mode-save");
    try {
      await updateRepositoryKnowledgeSettings({
        input: {
          repositoryId: repoId,
          generationModeDefault: nextMode,
        },
      });
      onGenerationModeChange();
    } finally {
      finishLoading("generation-mode-save");
    }
  }

  return (
    <Panel>
      <h3 className="mb-4 text-lg font-semibold text-[var(--text-primary)]">Repository Settings</h3>
      <div className="flex gap-3">
        <Button variant="secondary" onClick={() => reindex({ id: repoId })}>
          Reindex
        </Button>
      </div>
      <div className="mt-6 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] p-4">
        <div className="flex items-start justify-between gap-4">
          <div>
            <h4 className="text-sm font-semibold text-[var(--text-primary)]">Knowledge Engine Default</h4>
            <p className="mt-1 text-sm text-[var(--text-secondary)]">
              Sets the repository-level default generation engine. Request-time selections in the field guide still override this.
            </p>
          </div>
          <span className={artifactStatusClass}>{repoGenerationModeDefault === "CLASSIC" ? "Classic" : "Understanding First"}</span>
        </div>
        <div className="mt-4 flex flex-wrap gap-2">
          {[
            { key: "UNDERSTANDING_FIRST", label: "Understanding First" },
            { key: "CLASSIC", label: "Classic" },
          ].map((mode) => (
            <button
              key={mode.key}
              type="button"
              onClick={() => void handleSaveRepositoryGenerationMode(mode.key as RepositoryGenerationMode)}
              disabled={knowledgeLoading}
              className={cn(
                "rounded-full border px-3 py-1.5 text-xs font-medium transition-colors",
                repoGenerationModeDefault === mode.key
                  ? "border-[var(--accent-primary)] bg-[var(--accent-primary)] text-[var(--accent-contrast)]"
                  : "border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)]"
              )}
            >
              {mode.label}
            </button>
          ))}
        </div>
      </div>
      {/* Living Wiki panel — sits between Knowledge Engine Default and Danger Zone */}
      <div className="mt-6">
        <WikiSettingsPanel
          repoId={repoId}
          repoName={repo?.name ?? ""}
          initialSettings={repo?.livingWikiSettings ?? null}
        />
      </div>

      {/* Use with Claude Code card — capability-gated on agent_setup */}
      {agentSetupEnabled && repoId && (
        <div className="mt-6 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] p-4">
          <h4 className="text-sm font-semibold text-[var(--text-primary)]">Use with Claude Code</h4>
          <p className="mt-1 text-sm text-[var(--text-secondary)]">
            Generate a <code className="rounded bg-[var(--bg-subtle)] px-1 py-0.5 text-xs">.claude/CLAUDE.md</code> skill card with per-subsystem sections so Claude Code understands how this codebase is structured before you start refactoring.
          </p>

          {serverCaps.loading ? (
            /* Loading skeleton — don't flash the wrong block */
            <div className="mt-3 space-y-2" aria-busy="true" aria-label="Detecting server configuration">
              <div className="h-8 w-full animate-pulse rounded-[var(--control-radius)] bg-[var(--bg-subtle)]" />
              <div className="h-4 w-2/3 animate-pulse rounded bg-[var(--bg-subtle)]" />
            </div>
          ) : !serverCaps.mcpEnabled ? (
            /* MCP disabled — admin must enable it */
            <div className="mt-3 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-subtle)] px-3 py-2.5">
              <p className="text-sm text-[var(--text-secondary)]">
                MCP isn&apos;t enabled on this SourceBridge instance. Ask your admin to set{" "}
                <code className="rounded bg-[var(--bg-base)] px-1 py-0.5 text-xs">SOURCEBRIDGE_MCP_ENABLED=true</code>{" "}
                and restart the server.
              </p>
            </div>
          ) : serverCaps.authRequired ? (
            /* Cloud / auth-required — inline wizard (Slice 7) */
            <>
              {serverCaps.error && (
                <p className="mt-3 text-xs text-[var(--text-tertiary,var(--text-secondary))]">
                  Couldn&apos;t detect this server&apos;s auth configuration automatically — showing the hosted-instance flow. If you&apos;re on a local install, use{" "}
                  <code className="rounded bg-[var(--bg-subtle)] px-1 py-0.5 text-xs">sourcebridge setup claude --repo-id {repoId}</code> instead.
                </p>
              )}
              {useExistingToken ? (
                /* Fallback: slice-3 manual 3-step block */
                <div className="mt-3 space-y-3">
                  {/* Step 1 */}
                  <div className="flex items-start gap-3">
                    <span className="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full border border-[var(--border-default)] text-xs font-medium text-[var(--text-tertiary)]">1</span>
                    <div className="min-w-0 flex-1">
                      <p className="text-sm text-[var(--text-secondary)]">
                        Mint an API token for Claude Code.
                      </p>
                      <a
                        href={`/settings/tokens?suggested_name=Claude%20Code`}
                        className="mt-1.5 inline-flex items-center rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-1.5 text-xs font-medium text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
                      >
                        Go to API tokens
                      </a>
                    </div>
                  </div>
                  {/* Step 2 */}
                  <div className="flex items-start gap-3">
                    <span className="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full border border-[var(--border-default)] text-xs font-medium text-[var(--text-tertiary)]">2</span>
                    <div className="min-w-0 flex-1">
                      <p className="text-sm text-[var(--text-secondary)]">Run this command, replacing <code className="rounded bg-[var(--bg-subtle)] px-1 py-0.5 text-xs">&lt;paste-here&gt;</code> with your token.</p>
                      <div className="mt-1.5 flex items-center gap-2">
                        <code className="min-w-0 flex-1 overflow-x-auto rounded bg-[var(--bg-subtle)] px-3 py-2 text-xs font-mono text-[var(--text-primary)]">
                          {`sourcebridge setup claude --server ${getServerOrigin()} --token <paste-here> --repo-id ${repoId}`}
                        </code>
                        <button
                          type="button"
                          onClick={() => {
                            void navigator.clipboard.writeText(
                              `sourcebridge setup claude --server ${getServerOrigin()} --token <paste-here> --repo-id ${repoId}`
                            );
                            setCopiedSetupCmd(true);
                            setTimeout(() => setCopiedSetupCmd(false), 2000);
                          }}
                          className="shrink-0 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-xs font-medium text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
                          aria-label="Copy setup command"
                        >
                          {copiedSetupCmd ? "Copied!" : "Copy"}
                        </button>
                      </div>
                    </div>
                  </div>
                  {/* Step 3 */}
                  <div className="flex items-start gap-3">
                    <span className="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full border border-[var(--border-default)] text-xs font-medium text-[var(--text-tertiary)]">3</span>
                    <p className="text-sm text-[var(--text-secondary)]">
                      Add <code className="rounded bg-[var(--bg-subtle)] px-1 py-0.5 text-xs">export SOURCEBRIDGE_API_TOKEN=&lt;your-token&gt;</code> to your shell profile and restart Claude Code.
                    </p>
                  </div>
                  <button
                    type="button"
                    onClick={() => setUseExistingToken(false)}
                    className="text-xs text-[var(--text-tertiary)] underline underline-offset-2 hover:text-[var(--text-primary)]"
                  >
                    Back to wizard
                  </button>
                </div>
              ) : (
                <ClaudeCodeWizard
                  repoId={repoId}
                  onUseExisting={() => setUseExistingToken(true)}
                />
              )}
            </>
          ) : (
            /* Local / no-auth — single-step legacy flow */
            <div className="mt-3 flex items-center gap-2">
              <code className="flex-1 rounded bg-[var(--bg-subtle)] px-3 py-2 text-xs font-mono text-[var(--text-primary)]">
                {`sourcebridge setup claude --repo-id ${repoId}`}
              </code>
              <button
                type="button"
                onClick={() => {
                  void navigator.clipboard.writeText(`sourcebridge setup claude --repo-id ${repoId}`);
                  setCopiedSetupCmd(true);
                  setTimeout(() => setCopiedSetupCmd(false), 2000);
                }}
                className="shrink-0 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-xs font-medium text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
                aria-label="Copy setup command"
              >
                {copiedSetupCmd ? "Copied!" : "Copy"}
              </button>
            </div>
          )}

          <p className="mt-3 text-xs text-[var(--text-tertiary,var(--text-secondary))]">
            <a
              href="https://docs.claude.com/en/docs/claude-code/memory"
              target="_blank"
              rel="noopener noreferrer"
              className="underline hover:text-[var(--text-primary)]"
            >
              Learn more about Claude Code memory
              <span className="sr-only"> (opens in new tab)</span>
            </a>
          </p>
        </div>
      )}

      <div className="mt-8 rounded-[var(--control-radius)] border border-[var(--color-error,#ef4444)] p-4">
        <h4 className="mb-2 font-semibold text-[var(--color-error,#ef4444)]">Danger Zone</h4>
        <p className="mb-3 text-sm text-[var(--text-secondary)]">
          Removing this repository will delete all indexed data, symbols, and requirement links.
        </p>
        <Button
          onClick={() => setRemoveRepoConfirmOpen(true)}
          variant="danger"
        >
          Remove Repository
        </Button>
        <ConfirmDialog
          open={removeRepoConfirmOpen}
          title="Remove repository"
          body={`Remove "${repo?.name}"? This cannot be undone.`}
          confirmLabel="Remove"
          cancelLabel="Cancel"
          destructive
          onConfirm={async () => {
            setRemoveRepoConfirmOpen(false);
            const res = await removeRepo({ id: repoId });
            if (res.error) return;
            router.push("/repositories");
          }}
          onCancel={() => setRemoveRepoConfirmOpen(false)}
        />
      </div>
    </Panel>
  );
}
