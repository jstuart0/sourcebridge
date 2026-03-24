"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { useMutation } from "urql";
import { ADD_REPOSITORY_MUTATION } from "@/lib/graphql/queries";
import { useEventStream, ServerEvent } from "@/lib/sse";
import { Button } from "@/components/ui/button";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { cn } from "@/lib/utils";
import { trackEvent } from "@/lib/telemetry";

type Step = "welcome" | "add-repo" | "done";

export default function OnboardingPage() {
  const router = useRouter();
  const [step, setStep] = useState<Step>("welcome");
  const [repoName, setRepoName] = useState("");
  const [repoPath, setRepoPath] = useState("");
  const [addResult, addRepo] = useMutation(ADD_REPOSITORY_MUTATION);
  const [addedRepo, setAddedRepo] = useState<{ id: string; name: string } | null>(null);

  useEventStream((event: ServerEvent) => {
    if (event.type !== "repo.index.completed" || !addedRepo) return;
    const eventRepoId = String(event.data?.repo_id || "");
    if (eventRepoId === addedRepo.id) {
      trackEvent({ event: "repository_index_completed", repositoryId: addedRepo.id, metadata: { source: "onboarding" } });
      router.push(`/repositories/${addedRepo.id}?tab=knowledge`);
    }
  });

  async function handleAddRepo() {
    if (!repoName.trim() || !repoPath.trim()) return;
    const res = await addRepo({ input: { name: repoName.trim(), path: repoPath.trim() } });
    if (res.data?.addRepository) {
      setAddedRepo(res.data.addRepository);
      trackEvent({ event: "repository_added", repositoryId: res.data.addRepository.id, metadata: { source: "onboarding" } });
      setStep("done");
    }
  }

  const steps: Step[] = ["welcome", "add-repo", "done"];
  const stepIndex = steps.indexOf(step);

  return (
    <PageFrame className="max-w-4xl">
      <PageHeader
        eyebrow="Onboarding"
        title="Set up your first repository"
        description="Get value quickly: add a repo, index it, and open its field guide."
      />

      <div className="mx-auto w-full max-w-3xl space-y-8">
        <div className="grid grid-cols-3 gap-3">
          {steps.map((item, index) => (
            <div
              key={item}
              className={cn(
                "h-1.5 rounded-full",
                index <= stepIndex ? "bg-[var(--accent-primary)]" : "bg-[var(--border-default)]"
              )}
            />
          ))}
        </div>

        {step === "welcome" ? (
          <Panel padding="lg" className="space-y-6">
            <div className="space-y-3">
              <h2 className="text-2xl font-semibold tracking-[-0.03em] text-[var(--text-primary)]">
                Welcome to SourceBridge.ai
              </h2>
              <p className="text-sm leading-7 text-[var(--text-secondary)]">
                SourceBridge.ai helps you understand unfamiliar codebases quickly.
                Start by bringing one repository into the workspace.
              </p>
            </div>

            <ul className="list-disc space-y-2 pl-5 text-sm leading-7 text-[var(--text-secondary)]">
              <li>Index repositories to discover files, symbols, and structure.</li>
              <li>Open a repository field guide to see what matters first.</li>
              <li>Use AI to explain, discuss, and review implementation.</li>
              <li>Add specs or requirements later if you want intent-to-code links.</li>
            </ul>

            <div className="flex flex-wrap gap-3">
              <Button onClick={() => setStep("add-repo")}>Get Started</Button>
              <Button variant="secondary" onClick={() => router.push("/")}>
                Skip to Dashboard
              </Button>
            </div>
          </Panel>
        ) : null}

        {step === "add-repo" ? (
          <Panel padding="lg" className="space-y-6">
            <div className="space-y-2">
              <h2 className="text-2xl font-semibold tracking-[-0.03em] text-[var(--text-primary)]">
                Add your first repository
              </h2>
              <p className="text-sm leading-7 text-[var(--text-secondary)]">
                Provide a name and a git URL or local path. SourceBridge.ai will clone and index it
                automatically, then take you into the repository workspace.
              </p>
            </div>

            <div className="grid gap-5">
              <div className="space-y-2">
                <label className="block text-sm font-medium text-[var(--text-primary)]">
                  Repository Name
                </label>
                <input
                  className="h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)]"
                  value={repoName}
                  onChange={(e) => setRepoName(e.target.value)}
                  placeholder="e.g. my-project"
                />
              </div>

              <div className="space-y-2">
                <label className="block text-sm font-medium text-[var(--text-primary)]">
                  Git URL or Local Path
                </label>
                <input
                  className="h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)]"
                  value={repoPath}
                  onChange={(e) => setRepoPath(e.target.value)}
                  placeholder="https://github.com/org/repo.git or /path/to/repo"
                />
              </div>

              {addResult.error ? (
                <div className="rounded-[var(--control-radius)] border border-[var(--danger-border)] bg-[var(--danger-bg)] px-3 py-2 text-sm text-[var(--danger-text)]">
                  {addResult.error.message}
                </div>
              ) : null}

              <div className="flex flex-wrap gap-3">
                <Button
                  disabled={addResult.fetching || !repoName.trim() || !repoPath.trim()}
                  onClick={handleAddRepo}
                >
                  {addResult.fetching ? "Adding…" : "Add Repository"}
                </Button>
                <Button variant="secondary" onClick={() => setStep("done")}>
                  Skip
                </Button>
              </div>
            </div>
          </Panel>
        ) : null}

        {step === "done" ? (
          <Panel variant="elevated" padding="lg" className="space-y-6 text-center">
            <div className="space-y-3">
              <h2 className="text-2xl font-semibold tracking-[-0.03em] text-[var(--text-primary)]">
                Setup complete
              </h2>
              <p className="text-sm leading-7 text-[var(--text-secondary)]">
                {addedRepo
                  ? `"${addedRepo.name}" is being indexed. We’ll take you straight into its field guide as soon as it is ready.`
                  : "You can add repositories and optional specs or requirements at any time."}
              </p>
            </div>

            <div className="flex flex-wrap justify-center gap-3">
              <Button onClick={() => router.push("/")}>Go to Dashboard</Button>
              {addedRepo ? (
                <Button variant="secondary" onClick={() => router.push(`/repositories/${addedRepo.id}`)}>
                  View Repository
                </Button>
              ) : null}
              <Button variant="ghost" onClick={() => router.push("/help")}>
                Read the Guide
              </Button>
            </div>
          </Panel>
        ) : null}
      </div>
    </PageFrame>
  );
}
