"use client";

import { useState } from "react";
import { useMutation } from "urql";
import {
  ANALYZE_SYMBOL_MUTATION,
  DISCUSS_CODE_MUTATION,
  REVIEW_CODE_MUTATION,
} from "@/lib/graphql/queries";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Panel } from "@/components/ui/panel";
import { cn } from "@/lib/utils";
import { trackEvent } from "@/lib/telemetry";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface SymbolNode {
  id: string;
  name: string;
  qualifiedName: string;
  kind: string;
  language: string;
  filePath: string;
  startLine: number;
  endLine: number;
  signature: string | null;
}

interface AnalysisTabProps {
  repoId: string;
  symbols: SymbolNode[];
  symbolQuery: string;
  setSymbolQuery: (q: string) => void;
  selectedSymbolId: string | null;
  setSelectedSymbolId: (id: string | null) => void;
  /** Per-op AI loading gate */
  isAiLoading: (key: string) => boolean;
  runAiOp: (key: string, fn: () => Promise<void>) => Promise<void>;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function AnalysisTab({
  repoId,
  symbols,
  symbolQuery,
  setSymbolQuery,
  selectedSymbolId,
  setSelectedSymbolId,
  isAiLoading,
  runAiOp,
}: AnalysisTabProps) {
  const [analysisResult, setAnalysisResult] = useState<{
    summary: string;
    purpose: string;
    concerns: string[];
    suggestions: string[];
  } | null>(null);
  const [discussQuestion, setDiscussQuestion] = useState("");
  const [discussResult, setDiscussResult] = useState<{ answer: string } | null>(null);
  const [reviewFile, setReviewFile] = useState("");
  const [reviewTemplate, setReviewTemplate] = useState("security");
  const [reviewResult, setReviewResult] = useState<{
    findings: { category: string; severity: string; message: string; suggestion: string | null }[];
    score: number;
  } | null>(null);

  const [, analyzeSymbol] = useMutation(ANALYZE_SYMBOL_MUTATION);
  const [, discussCode] = useMutation(DISCUSS_CODE_MUTATION);
  const [, reviewCode] = useMutation(REVIEW_CODE_MUTATION);

  async function handleAnalyze(symId: string) {
    trackEvent({ event: "analyze_symbol_used", repositoryId: repoId, metadata: { symbolId: symId } });
    await runAiOp("analysis:analyze", async () => {
      setAnalysisResult(null);
      const res = await analyzeSymbol({ repositoryId: repoId, symbolId: symId });
      if (res.data?.analyzeSymbol) setAnalysisResult(res.data.analyzeSymbol);
    });
  }

  async function handleDiscuss() {
    if (!discussQuestion.trim()) return;
    trackEvent({ event: "discuss_code_used", repositoryId: repoId, metadata: { questionLength: discussQuestion.trim().length } });
    await runAiOp("analysis:discuss", async () => {
      setDiscussResult({ answer: "" });
      // Use the SSE streaming endpoint so the user sees tokens as
      // they're generated. On error we fall back to the legacy
      // GraphQL mutation so older servers (where /discuss/stream
      // isn't mounted) still work.
      const { askStream } = await import("@/lib/askStream");
      let accumulated = "";
      let streamErrored = false;
      await askStream(
        { repositoryId: repoId, question: discussQuestion.trim() },
        {
          onToken: (delta) => {
            accumulated += delta;
            setDiscussResult({ answer: accumulated });
          },
          onDone: (result) => {
            // Server's final answer is authoritative — prefer it
            // when non-empty (it may have been post-processed) and
            // otherwise keep whatever we streamed.
            setDiscussResult({ answer: result.answer || accumulated });
          },
          onError: () => {
            streamErrored = true;
          },
        },
      );
      if (streamErrored) {
        const res = await discussCode({ input: { repositoryId: repoId, question: discussQuestion } });
        if (res.data?.discussCode) setDiscussResult(res.data.discussCode);
      }
    });
  }

  async function handleReview() {
    if (!reviewFile.trim()) return;
    trackEvent({ event: "review_code_used", repositoryId: repoId, metadata: { template: reviewTemplate, filePath: reviewFile } });
    await runAiOp("analysis:review", async () => {
      setReviewResult(null);
      const res = await reviewCode({ input: { repositoryId: repoId, filePath: reviewFile, template: reviewTemplate } });
      if (res.data?.reviewCode) setReviewResult(res.data.reviewCode);
    });
  }

  const analyzeBusy = isAiLoading("analysis:analyze");
  const discussBusy = isAiLoading("analysis:discuss");
  const reviewBusy = isAiLoading("analysis:review");
  // Any of the three ops in flight — used for the results panel placeholder
  const anyBusy = analyzeBusy || discussBusy || reviewBusy;

  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  const inputClass =
    "h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)]";
  const inputCompactClass =
    "rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-sm text-[var(--text-primary)]";
  const listRowClass =
    "border-b border-[var(--border-subtle)] px-0 py-2.5 text-sm last:border-b-0";

  return (
    <div className="grid gap-6 lg:grid-cols-2">
      <div>
        <Panel>
          <h3 className="mb-4 text-lg font-semibold text-[var(--text-primary)]">
            Select Symbol to Analyze
          </h3>
          <Input
            type="text"
            value={symbolQuery}
            onChange={(e) => setSymbolQuery(e.target.value)}
            placeholder="Search symbols..."
            className="mb-3"
          />
          <div className="max-h-[40vh] overflow-y-auto">
            {symbols.map((sym) => (
              <div
                key={sym.id}
                onClick={() => setSelectedSymbolId(sym.id)}
                className={cn(
                  `${listRowClass} cursor-pointer rounded-[var(--control-radius)] px-3`,
                  selectedSymbolId === sym.id ? "bg-[var(--bg-active)]" : "bg-transparent"
                )}
              >
                <span className="font-mono text-[var(--text-primary)]">{sym.name}</span>
                <span className="ml-2 text-[var(--text-secondary)]">{sym.kind}</span>
              </div>
            ))}
          </div>
          {selectedSymbolId && (
            <Button className="mt-3" onClick={() => handleAnalyze(selectedSymbolId)} disabled={analyzeBusy}>
              {analyzeBusy ? "Analyzing..." : "Analyze Symbol"}
            </Button>
          )}
        </Panel>

        <Panel className="mt-4">
          <h3 className="mb-3 text-lg font-semibold text-[var(--text-primary)]">Discuss Code</h3>
          <Input
            type="text"
            value={discussQuestion}
            onChange={(e) => setDiscussQuestion(e.target.value)}
            placeholder="Ask a question about this code..."
            className="mb-3"
          />
          <Button onClick={handleDiscuss} disabled={discussBusy || !discussQuestion.trim()}>
            {discussBusy ? "Thinking..." : "Ask"}
          </Button>
        </Panel>

        <Panel className="mt-4">
          <h3 className="mb-3 text-lg font-semibold text-[var(--text-primary)]">Review Code</h3>
          <Input
            type="text"
            value={reviewFile}
            onChange={(e) => setReviewFile(e.target.value)}
            placeholder="File path (e.g. internal/api/rest/router.go)"
            className="mb-3"
          />
          <div className="flex flex-wrap gap-2">
            <select
              value={reviewTemplate}
              onChange={(e) => setReviewTemplate(e.target.value)}
              className={inputCompactClass}
            >
              <option value="security">Security</option>
              <option value="performance">Performance</option>
              <option value="reliability">Reliability</option>
              <option value="maintainability">Maintainability</option>
              <option value="solid">SOLID</option>
              <option value="ai_detection">AI Detection</option>
            </select>
            <Button onClick={handleReview} disabled={reviewBusy || !reviewFile.trim()}>
              {reviewBusy ? "Reviewing..." : "Review"}
            </Button>
          </div>
        </Panel>
      </div>

      <div>
        <Panel>
          <h3 className="mb-4 text-lg font-semibold text-[var(--text-primary)]">Results</h3>
          {analysisResult ? (
            <div className="text-sm">
              <h4 className="mb-2 font-medium">Analysis</h4>
              <p><strong>Summary:</strong> {analysisResult.summary}</p>
              <p className="mt-2"><strong>Purpose:</strong> {analysisResult.purpose}</p>
              {analysisResult.concerns.length > 0 && (
                <div className="mt-2">
                  <strong>Concerns:</strong>
                  <ul className="my-1 pl-5">
                    {analysisResult.concerns.map((c, i) => <li key={i}>{c}</li>)}
                  </ul>
                </div>
              )}
              {analysisResult.suggestions.length > 0 && (
                <div className="mt-2">
                  <strong>Suggestions:</strong>
                  <ul className="my-1 pl-5">
                    {analysisResult.suggestions.map((s, i) => <li key={i}>{s}</li>)}
                  </ul>
                </div>
              )}
            </div>
          ) : discussResult ? (
            <div className="text-sm">
              <h4 className="mb-2 font-medium">Discussion</h4>
              <p className="whitespace-pre-wrap">{discussResult.answer}</p>
            </div>
          ) : reviewResult ? (
            <div className="text-sm">
              <h4 className="mb-2 font-medium">
                Review (Score: {Math.round(reviewResult.score * 100)}%)
              </h4>
              {reviewResult.findings.map((f, i) => (
                <div key={i} className="border-b border-[var(--border-subtle)] py-2.5 last:border-b-0">
                  <span className="font-medium">[{f.severity}] {f.category}</span>
                  <p className="mt-1">{f.message}</p>
                  {f.suggestion && <p className="mt-1 text-[var(--text-secondary)]">Suggestion: {f.suggestion}</p>}
                </div>
              ))}
            </div>
          ) : anyBusy ? (
            <p className="text-sm text-[var(--text-secondary)]">Processing…</p>
          ) : (
            <p className="text-sm text-[var(--text-secondary)]">
              Select a symbol and run an analysis, ask a question, or review a file.
            </p>
          )}
        </Panel>
      </div>
    </div>
  );
}
