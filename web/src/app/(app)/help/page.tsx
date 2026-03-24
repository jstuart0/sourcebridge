"use client";

import Link from "next/link";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";

function HelpSection({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <section className="space-y-3">
      <h2 className="text-xl font-semibold tracking-[-0.03em] text-[var(--text-primary)]">
        {title}
      </h2>
      <Panel className="text-sm leading-7 text-[var(--text-secondary)]">{children}</Panel>
    </section>
  );
}

const linkClass = "font-medium text-[var(--accent-primary)] transition-colors hover:opacity-80";

export default function HelpPage() {
  return (
    <PageFrame className="max-w-4xl">
      <PageHeader
        eyebrow="Guide"
        title="Help"
        description="Core workflows, shortcuts, and product concepts for navigating SourceBridge.ai effectively."
      />

      <div className="space-y-8">
        <HelpSection title="Getting Started">
          <p>
            SourceBridge.ai is a codebase field guide and context layer. It indexes repositories to
            discover files and symbols, then helps you explain the system, review risky areas, and
            optionally connect specs or requirements to implementation.
          </p>
          <ol className="mt-4 list-decimal space-y-2 pl-5">
            <li>
              <strong>Add a repository</strong> in{" "}
              <Link href="/repositories" className={linkClass}>
                Repositories
              </Link>
              .
            </li>
            <li>
              <strong>Wait for indexing</strong> so SourceBridge.ai can discover files, symbols, and call
              structure.
            </li>
            <li>
              <strong>Open the repository Field Guide</strong> to get oriented quickly.
            </li>
            <li>
              <strong>Import specs or requirements later</strong> if you want intent-to-code links and coverage.
            </li>
          </ol>
        </HelpSection>

        <HelpSection title="Repositories">
          <p>
            A repository represents a codebase that SourceBridge.ai indexes. You can add repositories by
            providing a git URL or a local filesystem path.
          </p>
          <p className="mt-3">
            Once indexed, you can browse files, inspect symbols, open the Field Guide, review changes,
            and run explain/discuss workflows from the repository workspace.
          </p>
          <p className="mt-3">
            <strong>Reindexing:</strong> use the repository settings view after significant code
            changes.
          </p>
        </HelpSection>

        <HelpSection title="Specs & Traceability">
          <p>
            Import specs or requirements from CSV or Markdown when you want to connect intent to symbols
            and track confidence and verification state.
          </p>
          <p className="mt-3">
            Auto-linking discovers candidate links semantically. Manual linking lets you add curated
            evidence when the automated path is not enough. This is optional; the understanding
            workflows still work without imported specs.
          </p>
        </HelpSection>

        <HelpSection title="AI Features">
          <ul className="list-disc space-y-2 pl-5">
            <li>
              <strong>Analyze Symbol</strong> for a summary, purpose, concerns, and suggestions.
            </li>
            <li>
              <strong>Discuss Code</strong> for whole-repository or focused questions.
            </li>
            <li>
              <strong>Review Code</strong> against security, performance, and reliability templates.
            </li>
            <li>
              <strong>Auto-Link Requirements</strong> to discover traceability candidates.
            </li>
            <li>
              <strong>Enrich Requirement</strong> to improve descriptions and tags.
            </li>
          </ul>
          <p className="mt-4">
            Configure providers in{" "}
            <Link href="/admin" className={linkClass}>
              Admin
            </Link>
            . Ollama remains the default local path.
          </p>
        </HelpSection>

        <HelpSection title="Keyboard Shortcuts">
          <div className="grid gap-3 sm:grid-cols-[160px_1fr]">
            <code className="rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-xs text-[var(--text-primary)]">
              Cmd+K
            </code>
            <span>Open the command palette.</span>
          </div>
        </HelpSection>

        <HelpSection title="Admin & Configuration">
          <p>
            The{" "}
            <Link href="/admin" className={linkClass}>
              Admin
            </Link>{" "}
            area shows system status, LLM configuration, and integration settings.
          </p>
          <p className="mt-3">
            All API requests require a bearer token. The local admin password is created during the
            first setup flow.
          </p>
        </HelpSection>
      </div>
    </PageFrame>
  );
}
