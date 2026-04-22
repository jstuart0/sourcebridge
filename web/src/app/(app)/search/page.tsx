"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { Search as SearchIcon } from "lucide-react";
import { useQuery } from "urql";
import { SEARCH_QUERY } from "@/lib/graphql/queries";
import { EmptyState } from "@/components/ui/empty-state";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { buildRepositorySourceHref } from "@/lib/source-target";

interface SearchSignals {
  exact: number | null;
  lexical: number | null;
  semantic: number | null;
  graph: number | null;
  requirement: number | null;
}

interface SearchResultItem {
  type: string;
  id: string;
  title: string;
  description: string | null;
  filePath: string | null;
  line: number | null;
  repositoryId: string;
  repositoryName: string;
  score: number | null;
  signals: SearchSignals | null;
}

// SIGNAL_CHIPS is the display model for result-level signal
// explanations. Order matters — it mirrors the canonical order in
// internal/search.Signals so the UI is consistent across the stack.
const SIGNAL_CHIPS: Array<{ key: keyof SearchSignals; label: string; title: string }> = [
  { key: "exact", label: "exact", title: "Exact name or qualified-name hit." },
  { key: "lexical", label: "match", title: "Full-text match (BM25 or substring fallback)." },
  { key: "semantic", label: "semantic", title: "Vector similarity hit from the embedding index." },
  { key: "graph", label: "graph", title: "Reachable via the call graph from a seed symbol." },
  { key: "requirement", label: "req", title: "This symbol has a linked requirement." },
];

function firedSignals(signals: SearchSignals | null): typeof SIGNAL_CHIPS {
  if (!signals) return [];
  return SIGNAL_CHIPS.filter((c) => (signals[c.key] ?? 0) > 0);
}

export default function SearchPage() {
  const [query, setQuery] = useState("");
  const [debouncedQuery, setDebouncedQuery] = useState("");

  useEffect(() => {
    const timer = setTimeout(() => setDebouncedQuery(query), 300);
    return () => clearTimeout(timer);
  }, [query]);

  const [result] = useQuery({
    query: SEARCH_QUERY,
    variables: { query: debouncedQuery, limit: 50 },
    pause: debouncedQuery.length < 2,
  });

  const results: SearchResultItem[] = result.data?.search || [];

  const grouped = {
    symbol: results.filter((r) => r.type === "symbol"),
    requirement: results.filter((r) => r.type === "requirement"),
    file: results.filter((r) => r.type === "file"),
  };

  function resultHref(item: SearchResultItem): string {
    switch (item.type) {
      case "symbol":
        if (item.filePath) {
          return buildRepositorySourceHref(item.repositoryId, {
            tab: "symbols",
            filePath: item.filePath,
            line: item.line ?? undefined,
          });
        }
        return `/repositories/${item.repositoryId}?tab=symbols`;
      case "requirement":
        return `/requirements/${item.id}`;
      case "file":
        if (item.filePath) {
          return buildRepositorySourceHref(item.repositoryId, {
            tab: "files",
            filePath: item.filePath,
            line: item.line ?? undefined,
          });
        }
        return `/repositories/${item.repositoryId}?tab=files`;
      default:
        return "#";
    }
  }

  function typeLabel(type: string): string {
    switch (type) {
      case "symbol":
        return "Symbols";
      case "requirement":
        return "Requirements";
      case "file":
        return "Files";
      default:
        return type;
    }
  }

  return (
    <PageFrame>
      <PageHeader
        eyebrow="Search"
        title="Search across code and requirements"
        description="Find symbols, files, and requirements from a single indexed view of the system."
      />

      <div className="max-w-4xl space-y-6">
        <div className="relative">
          <SearchIcon className="pointer-events-none absolute left-4 top-1/2 h-4 w-4 -translate-y-1/2 text-[var(--text-tertiary)]" />
          <input
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search symbols, requirements, files…"
            autoFocus
            className="h-12 w-full rounded-[var(--panel-radius)] border border-[var(--border-default)] bg-[var(--panel-bg)] pl-11 pr-4 text-base text-[var(--text-primary)] shadow-[var(--panel-shadow-soft)] sm:h-13"
          />
        </div>

        {debouncedQuery.length >= 2 && result.fetching ? (
          <p className="text-sm text-[var(--text-secondary)]">Searching…</p>
        ) : null}

        {debouncedQuery.length >= 2 && !result.fetching && results.length === 0 ? (
          <EmptyState
            title="No results found"
            description="Try a broader query, or search by requirement ID, symbol name, or file path."
          />
        ) : null}

        {(["symbol", "requirement", "file"] as const).map((type) => {
          const items = grouped[type];
          if (items.length === 0) return null;

          return (
            <section key={type} className="space-y-3">
              <h2 className="text-base font-semibold tracking-[-0.02em] text-[var(--text-primary)]">
                {typeLabel(type)} ({items.length})
              </h2>
              <Panel padding="none" className="overflow-hidden">
                {items.map((item) => (
                  <Link
                    key={`${item.type}-${item.id}`}
                    href={resultHref(item)}
                    className="block border-b border-[var(--border-subtle)] px-5 py-4 text-sm transition-colors last:border-b-0 hover:bg-[var(--bg-hover)]"
                  >
                    <div className="flex flex-col gap-2 md:flex-row md:items-start md:justify-between">
                      <span className="font-medium text-[var(--text-primary)]">{item.title}</span>
                      <span className="text-xs uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
                        {item.repositoryName}
                      </span>
                    </div>
                    {item.description ? (
                      <div className="mt-2 text-sm text-[var(--text-secondary)]">
                        {item.description.slice(0, 140)}
                      </div>
                    ) : null}
                    {item.filePath ? (
                      <div className="mt-2 font-mono text-xs text-[var(--text-tertiary)]">
                        {item.filePath}
                        {item.line ? `:${item.line}` : ""}
                      </div>
                    ) : null}
                    {firedSignals(item.signals).length > 0 ? (
                      <div className="mt-3 flex flex-wrap gap-1.5">
                        {firedSignals(item.signals).map((c) => (
                          <span
                            key={c.key}
                            title={c.title}
                            className="rounded-full border border-[var(--border-subtle)] bg-[var(--bg-subtle)] px-2 py-0.5 text-[10px] uppercase tracking-[0.12em] text-[var(--text-tertiary)]"
                          >
                            {c.label}
                          </span>
                        ))}
                      </div>
                    ) : null}
                  </Link>
                ))}
              </Panel>
            </section>
          );
        })}
      </div>
    </PageFrame>
  );
}
