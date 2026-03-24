"use client";

import Link from "next/link";
import { useCallback, useState } from "react";
import { useRouter } from "next/navigation";
import { ChevronRight, FileText, FolderPlus, Trash2, Upload } from "lucide-react";
import { useMutation, useQuery } from "urql";
import {
  ADD_REPOSITORY_MUTATION,
  IMPORT_REQUIREMENTS_MUTATION as IMPORT_REQUIREMENTS,
  REMOVE_REPOSITORY_MUTATION,
  REPOSITORIES_LIGHT_QUERY as REPOSITORIES,
} from "@/lib/graphql/queries";
import { useEventStream, ServerEvent } from "@/lib/sse";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { LazyScoreBadge } from "@/components/understanding-score";
import { trackEvent } from "@/lib/telemetry";

interface Repository {
  id: string;
  name: string;
  path: string;
  status: string;
  fileCount: number;
  functionCount: number;
  classCount: number;
  requirementCount: number;
  lastIndexedAt: string | null;
}

function statusTone(status: string) {
  switch (status) {
    case "READY":
      return "text-emerald-500 border-emerald-500/40 bg-emerald-500/10";
    case "INDEXING":
      return "text-amber-500 border-amber-500/40 bg-amber-500/10";
    case "ERROR":
      return "text-rose-500 border-rose-500/40 bg-rose-500/10";
    default:
      return "text-[var(--text-secondary)] border-[var(--border-default)] bg-[var(--bg-base)]";
  }
}

export default function RepositoriesPage() {
  const router = useRouter();
  const [result, reexecute] = useQuery({ query: REPOSITORIES });
  const [, importReqs] = useMutation(IMPORT_REQUIREMENTS);
  const [, addRepo] = useMutation(ADD_REPOSITORY_MUTATION);
  const [, removeRepo] = useMutation(REMOVE_REPOSITORY_MUTATION);

  const [importPath, setImportPath] = useState("");
  const [importRepoId, setImportRepoId] = useState<string | null>(null);
  const [importFileName, setImportFileName] = useState("");
  const [importing, setImporting] = useState(false);
  const [showAddForm, setShowAddForm] = useState(false);
  const [newName, setNewName] = useState("");
  const [newPath, setNewPath] = useState("");
  const [addError, setAddError] = useState("");
  const [adding, setAdding] = useState(false);
  const [addSuccess, setAddSuccess] = useState("");
  const [pendingRedirectRepoId, setPendingRedirectRepoId] = useState<string | null>(null);

  const repos: Repository[] = result.data?.repositories || [];

  const handleSSEEvent = useCallback(
    (event: ServerEvent) => {
      if (event.type === "repo.index.completed" || event.type === "repo.index.failed") {
        reexecute({ requestPolicy: "network-only" });
      }
      if (event.type === "repo.index.completed" && pendingRedirectRepoId) {
        const eventRepoId = String(event.data?.repo_id || "");
        if (eventRepoId === pendingRedirectRepoId) {
          trackEvent({
            event: "repository_index_completed",
            repositoryId: pendingRedirectRepoId,
            metadata: { source: "repositories_page" },
          });
          router.push(`/repositories/${pendingRedirectRepoId}?tab=knowledge`);
        }
      }
    },
    [pendingRedirectRepoId, reexecute, router]
  );
  useEventStream(handleSSEEvent);

  function handleFileSelect(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    if (!file) return;
    setImportFileName(file.name);
    const reader = new FileReader();
    reader.onload = () => setImportPath(reader.result as string);
    reader.readAsText(file);
  }

  function detectFormat(fileName: string): string {
    return fileName.endsWith(".csv") ? "CSV" : "MARKDOWN";
  }

  async function handleImport(repoId: string) {
    if (!importPath.trim() || importing) return;
    setImporting(true);
    try {
      await importReqs({
        input: {
          repositoryId: repoId,
          content: importPath,
          format: detectFormat(importFileName),
          sourcePath: importFileName || undefined,
        },
      });
      setImportPath("");
      setImportFileName("");
      setImportRepoId(null);
      reexecute({ requestPolicy: "network-only" });
    } finally {
      setImporting(false);
    }
  }

  async function handleAddRepo() {
    if (!newName.trim() || !newPath.trim() || adding) return;
    setAddError("");
    setAddSuccess("");
    setAdding(true);
    try {
      const res = await addRepo({ input: { name: newName.trim(), path: newPath.trim() } });
      if (res.error) {
        setAddError(res.error.message);
        return;
      }
      const addedId = res.data?.addRepository?.id ?? null;
      setPendingRedirectRepoId(addedId);
      trackEvent({
        event: "repository_added",
        repositoryId: addedId || undefined,
        metadata: { source: "repositories_page" },
      });
      setAddSuccess(`Repository "${newName.trim()}" added. Cloning and indexing are in progress. You’ll be taken straight to its field guide when indexing completes.`);
      setNewName("");
      setNewPath("");
      reexecute({ requestPolicy: "network-only" });
      setTimeout(() => {
        setShowAddForm(false);
        setAddSuccess("");
      }, 4000);
    } finally {
      setAdding(false);
    }
  }

  async function handleRemoveRepo(id: string, name: string) {
    if (!confirm(`Remove repository "${name}"? This cannot be undone.`)) return;
    await removeRepo({ id });
    reexecute({ requestPolicy: "network-only" });
  }

  return (
    <PageFrame>
      <PageHeader
        eyebrow="Repositories"
        title="Manage indexed repositories"
        description="Add codebases, track indexing status, and jump straight into the repository workspace as soon as understanding is ready."
        actions={
          <Button onClick={() => setShowAddForm((value) => !value)}>
            <FolderPlus className="h-4 w-4" />
            {showAddForm ? "Cancel" : "Add Repository"}
          </Button>
        }
      />

      {showAddForm ? (
        <Panel variant="elevated" className="max-w-3xl space-y-5">
          <div className="space-y-1">
            <h2 className="text-xl font-semibold tracking-[-0.03em] text-[var(--text-primary)]">
              Add Repository
            </h2>
            <p className="text-sm leading-7 text-[var(--text-secondary)]">
              Provide a local path or git URL. SourceBridge.ai will clone and index the repository.
            </p>
          </div>

          <div className="grid gap-5">
            <div className="space-y-2">
              <label className="block text-sm font-medium text-[var(--text-primary)]">Name</label>
              <input
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
                placeholder="my-project"
                className="h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)]"
              />
            </div>
            <div className="space-y-2">
              <label className="block text-sm font-medium text-[var(--text-primary)]">
                Path or Git URL
              </label>
              <input
                value={newPath}
                onChange={(e) => setNewPath(e.target.value)}
                placeholder="https://github.com/org/repo.git or /path/to/local/repo"
                className="h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)]"
              />
            </div>

            {addError ? (
              <div className="rounded-[var(--control-radius)] border border-[var(--danger-border)] bg-[var(--danger-bg)] px-3 py-2 text-sm text-[var(--danger-text)]">
                {addError}
              </div>
            ) : null}

            {addSuccess ? (
              <div className="rounded-[var(--control-radius)] border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-500">
                {addSuccess}
              </div>
            ) : null}

            <div>
              <Button disabled={!newName.trim() || !newPath.trim() || adding} onClick={handleAddRepo}>
                {adding ? "Adding…" : "Add & Index"}
              </Button>
            </div>
          </div>
        </Panel>
      ) : null}

      {repos.length === 0 && !result.fetching ? (
        <EmptyState
          title="No repositories indexed yet"
          description="Start by adding a repository. Once indexing completes, SourceBridge.ai builds a field guide for the system: files, symbols, structure, and guided understanding."
          actions={<Button onClick={() => setShowAddForm(true)}>Add Repository</Button>}
        />
      ) : (
        <div className="grid gap-5">
          {repos.map((repo) => (
            <Panel key={repo.id} className="space-y-5">
              <div className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
                <div className="space-y-2">
                  <div className="flex items-center gap-2">
                    <Link
                      href={`/repositories/${repo.id}`}
                      className="group/link inline-flex items-center gap-1.5 text-xl font-semibold tracking-[-0.03em] text-[var(--text-primary)] transition-colors hover:text-[var(--accent-primary)]"
                    >
                      {repo.name}
                      <ChevronRight className="h-4 w-4 opacity-0 transition-opacity group-hover/link:opacity-100" />
                    </Link>
                    <LazyScoreBadge repositoryId={repo.id} />
                  </div>
                  <p className="font-mono text-xs text-[var(--text-tertiary)]">{repo.path}</p>
                </div>

                <div className="flex items-center gap-3">
                  <span
                    className={`rounded-full border px-3 py-1 text-[11px] font-semibold uppercase tracking-[0.16em] ${statusTone(repo.status)}`}
                  >
                    {repo.status}
                  </span>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => handleRemoveRepo(repo.id, repo.name)}
                    title="Remove repository"
                  >
                    <Trash2 className="h-4 w-4" />
                    Remove
                  </Button>
                </div>
              </div>

              <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 sm:gap-4 md:grid-cols-5">
                <div>
                  <p className="text-[11px] uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
                    Files
                  </p>
                  <p className="mt-1 text-lg font-semibold text-[var(--text-primary)]">{repo.fileCount}</p>
                </div>
                <div>
                  <p className="text-[11px] uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
                    Functions
                  </p>
                  <p className="mt-1 text-lg font-semibold text-[var(--text-primary)]">
                    {repo.functionCount}
                  </p>
                </div>
                <div>
                  <p className="text-[11px] uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
                    Classes
                  </p>
                  <p className="mt-1 text-lg font-semibold text-[var(--text-primary)]">{repo.classCount}</p>
                </div>
                <Link
                  href={`/repositories/${repo.id}?tab=requirements`}
                  className="group/req transition-colors"
                >
                  <p className="text-[11px] uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
                    Requirements
                  </p>
                  <p className="mt-1 flex items-center gap-1.5 text-lg font-semibold text-[var(--text-primary)] group-hover/req:text-[var(--accent-primary)]">
                    {repo.requirementCount}
                    {repo.requirementCount > 0 && (
                      <FileText className="h-4 w-4 opacity-50 group-hover/req:opacity-100" />
                    )}
                  </p>
                </Link>
                <div>
                  <p className="text-[11px] uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
                    Last Indexed
                  </p>
                  <p className="mt-1 text-sm text-[var(--text-secondary)]">
                    {repo.lastIndexedAt ? new Date(repo.lastIndexedAt).toLocaleString() : "Pending"}
                  </p>
                </div>
              </div>

              <div className="border-t border-[var(--border-subtle)] pt-5">
                {importRepoId === repo.id ? (
                  <div className="flex flex-col gap-3 lg:flex-row lg:items-center">
                    <label className="flex-1 cursor-pointer rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2.5 text-sm text-[var(--text-secondary)]">
                      {importFileName || "Choose a Markdown or CSV file…"}
                      <input
                        type="file"
                        accept=".md,.markdown,.csv,.txt"
                        onChange={handleFileSelect}
                        className="hidden"
                      />
                    </label>
                    <div className="flex gap-3">
                      <Button
                        disabled={!importPath.trim() || importing}
                        onClick={() => handleImport(repo.id)}
                      >
                        <Upload className="h-4 w-4" />
                        {importing ? "Importing…" : "Import"}
                      </Button>
                      <Button
                        variant="secondary"
                        onClick={() => {
                          setImportRepoId(null);
                          setImportPath("");
                          setImportFileName("");
                        }}
                      >
                        Cancel
                      </Button>
                    </div>
                  </div>
                ) : (
                  <Button variant="secondary" onClick={() => setImportRepoId(repo.id)}>
                    <Upload className="h-4 w-4" />
                    Add Specs or Requirements
                  </Button>
                )}
              </div>
            </Panel>
          ))}
        </div>
      )}
    </PageFrame>
  );
}
