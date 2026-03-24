"use client";

import React, { useEffect, useRef } from "react";
import { EditorView, basicSetup } from "codemirror";
import { EditorSelection, EditorState, RangeSetBuilder } from "@codemirror/state";
import { go } from "@codemirror/lang-go";
import { javascript } from "@codemirror/lang-javascript";
import { python } from "@codemirror/lang-python";
import { java } from "@codemirror/lang-java";
import { rust } from "@codemirror/lang-rust";
import { cpp } from "@codemirror/lang-cpp";
import { ViewPlugin, Decoration, DecorationSet } from "@codemirror/view";
import type { ViewUpdate } from "@codemirror/view";

export interface RequirementOverlayData {
  startLine: number;
  endLine: number;
  requirementId: string;
  category: string;
  confidence: number;
}

export interface CodeViewerProps {
  code: string;
  language: string;
  overlays?: RequirementOverlayData[];
  onLineClick?: (line: number) => void;
  focusLine?: number;
  focusEndLine?: number;
}

function getLanguageExtension(lang: string) {
  switch (lang.toLowerCase()) {
    case "go":
    case "golang":
      return go();
    case "python":
    case "py":
      return python();
    case "typescript":
    case "ts":
    case "javascript":
    case "js":
      return javascript({ typescript: lang.startsWith("t") });
    case "java":
      return java();
    case "rust":
    case "rs":
      return rust();
    case "cpp":
    case "c++":
    case "c":
    case "csharp":
    case "cs":
      return cpp();
    default:
      return javascript();
  }
}

const categoryColors: Record<string, string> = {
  business: "rgba(59, 130, 246, 0.08)",
  security: "rgba(239, 68, 68, 0.08)",
  data: "rgba(34, 197, 94, 0.08)",
  compliance: "rgba(168, 85, 247, 0.08)",
  performance: "rgba(234, 179, 8, 0.08)",
  default: "rgba(148, 163, 184, 0.08)",
};

function createOverlayPlugin(overlays: RequirementOverlayData[]) {
  return ViewPlugin.fromClass(
    class {
      decorations: DecorationSet;
      constructor(view: EditorView) {
        this.decorations = this.buildDecorations(view);
      }
      update(update: ViewUpdate) {
        if (update.docChanged || update.viewportChanged) {
          this.decorations = this.buildDecorations(update.view);
        }
      }
      buildDecorations(view: EditorView) {
        const builder: { from: number; to: number; value: Decoration }[] = [];
        for (const overlay of overlays) {
          const startLine = Math.max(1, overlay.startLine);
          const endLine = Math.min(view.state.doc.lines, overlay.endLine);
          for (let line = startLine; line <= endLine; line++) {
            const lineObj = view.state.doc.line(line);
            const color = categoryColors[overlay.category] || categoryColors.default;
            builder.push({
              from: lineObj.from,
              to: lineObj.from,
              value: Decoration.line({ attributes: { style: `background-color: ${color}` } }),
            });
          }
        }
        builder.sort((a, b) => a.from - b.from);
        return Decoration.set(builder.map((b) => b.value.range(b.from)));
      }
    },
    { decorations: (v) => v.decorations }
  );
}

function createFocusedLineTheme(isDark: boolean) {
  return EditorView.theme(
    {
      "&": {
        height: "100%",
        fontSize: "13px",
        backgroundColor: "transparent",
      },
      ".cm-scroller": {
        fontFamily:
          "var(--font-mono, ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace)",
        lineHeight: "1.6",
      },
      ".cm-content": {
        padding: "12px 0",
      },
      ".cm-gutters": {
        backgroundColor: isDark ? "rgba(255,255,255,0.02)" : "rgba(15,23,42,0.03)",
        borderRight: isDark ? "1px solid rgba(255,255,255,0.08)" : "1px solid rgba(15,23,42,0.08)",
        color: "var(--text-tertiary)",
      },
      ".cm-activeLine": {
        backgroundColor: "transparent",
      },
      ".cm-focused": {
        outline: "none",
      },
      ".cm-selectionBackground": {
        backgroundColor: "rgba(59,130,246,0.22) !important",
      },
    },
    { dark: isDark }
  );
}

function createFocusPlugin(startLine?: number, endLine?: number) {
  if (!startLine || startLine < 1) {
    return null;
  }

  return ViewPlugin.fromClass(
    class {
      decorations: DecorationSet;
      constructor(view: EditorView) {
        this.decorations = this.buildDecorations(view);
        this.scrollIntoView(view);
      }
      update(update: ViewUpdate) {
        if (update.docChanged || update.viewportChanged) {
          this.decorations = this.buildDecorations(update.view);
        }
      }
      buildDecorations(view: EditorView) {
        const fromLine = Math.max(1, startLine);
        const toLine = Math.min(view.state.doc.lines, endLine ?? startLine);
        const builder = new RangeSetBuilder<Decoration>();
        for (let line = fromLine; line <= toLine; line++) {
          const lineObj = view.state.doc.line(line);
          builder.add(
            lineObj.from,
            lineObj.from,
            Decoration.line({ attributes: { class: "cm-sourcebridge-focus-line" } })
          );
        }
        return builder.finish();
      }
      scrollIntoView(view: EditorView) {
        const lineObj = view.state.doc.line(Math.min(view.state.doc.lines, startLine));
        view.dispatch({
          selection: EditorSelection.cursor(lineObj.from),
          effects: EditorView.scrollIntoView(lineObj.from, { y: "center" }),
        });
      }
    },
    { decorations: (v) => v.decorations }
  );
}

export function CodeViewer({
  code,
  language,
  overlays = [],
  onLineClick,
  focusLine,
  focusEndLine,
}: CodeViewerProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);

  useEffect(() => {
    if (!containerRef.current) return;

    const isDark = document.documentElement.dataset.theme !== "light";

    const extensions = [
      basicSetup,
      createFocusedLineTheme(isDark),
      getLanguageExtension(language),
      EditorState.readOnly.of(true),
    ];

    if (overlays.length > 0) {
      extensions.push(createOverlayPlugin(overlays));
    }

    const focusPlugin = createFocusPlugin(focusLine, focusEndLine);
    if (focusPlugin) {
      extensions.push(focusPlugin);
    }

    if (onLineClick) {
      extensions.push(
        EditorView.domEventHandlers({
          click: (event, view) => {
            const pos = view.posAtCoords({ x: event.clientX, y: event.clientY });
            if (pos !== null) {
              const line = view.state.doc.lineAt(pos).number;
              onLineClick(line);
            }
          },
        })
      );
    }

    const state = EditorState.create({ doc: code, extensions });
    const view = new EditorView({ state, parent: containerRef.current });
    viewRef.current = view;

    return () => view.destroy();
  }, [code, language, overlays, onLineClick, focusLine, focusEndLine]);

  return (
    <div
      ref={containerRef}
      data-testid="code-viewer"
      data-language={language}
      className="min-h-[200px] w-full overflow-x-auto text-xs sm:text-sm [&_.cm-sourcebridge-focus-line]:bg-[rgba(59,130,246,0.12)]"
    />
  );
}
